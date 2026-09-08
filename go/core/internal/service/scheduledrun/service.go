package scheduledrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultPageSize = 50
	resourceKind    = "ScheduledRun"
)

type store interface {
	CreateScheduledRunBinding(context.Context, *dbpkg.ScheduledRunBinding) error
	GetScheduledRunBinding(context.Context, string, string, string) (*dbpkg.ScheduledRunBinding, error)
	ListScheduledRunExecutions(context.Context, string, string, string, dbpkg.ScheduledRunExecutionQuery) ([]dbpkg.ScheduledRunExecution, error)
	GetScheduledRunExecutionByAgentInstanceID(context.Context, string) (*dbpkg.ScheduledRunExecution, error)
}

type trigger interface {
	TriggerManualExecution(context.Context, types.NamespacedName) (*dbpkg.ScheduledRunExecution, error)
}

type Service struct {
	kube       client.Client
	authorizer auth.Authorizer
	store      store
	trigger    trigger
	crud       *kubecrud.Service[*v1alpha3.ScheduledRun, *v1alpha3.ScheduledRunList]
}

func NewService(kube client.Client, authorizer auth.Authorizer, store store, trigger trigger) *Service {
	return &Service{
		kube: kube, authorizer: authorizer, store: store, trigger: trigger,
		crud: kubecrud.NewService(kube, authorizer, &v1alpha3.ScheduledRun{}, &v1alpha3.ScheduledRunList{}, resourceKind),
	}
}

func (s *Service) List(ctx context.Context, namespace string) ([]*v1alpha3.ScheduledRun, error) {
	return s.crud.List(ctx, namespace)
}

func (s *Service) Get(ctx context.Context, ref types.NamespacedName) (*v1alpha3.ScheduledRun, error) {
	return s.crud.Get(ctx, ref)
}

func (s *Service) Create(ctx context.Context, incoming *v1alpha3.ScheduledRun) (*v1alpha3.ScheduledRun, error) {
	if incoming == nil {
		return nil, serviceerrors.NewInvalidArgument("ScheduledRun resource is required", nil)
	}
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session.Principal().User.ID == "" {
		return nil, serviceerrors.NewUnauthenticated("Authentication is required", nil)
	}
	principal := session.Principal()
	if principal.Agent.ID != "" || principal.User.ID == auth.ScheduledRunUserID {
		return nil, serviceerrors.NewPermissionDenied("ScheduledRun creation requires a user identity", nil)
	}
	// Only user-owned metadata is accepted on create. In particular, do not persist
	// a copied UID, resourceVersion or controller-written status from a GET response.
	resource := &v1alpha3.ScheduledRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha3.GroupVersion.String(), Kind: resourceKind},
		ObjectMeta: metav1.ObjectMeta{Name: incoming.Name, Namespace: incoming.Namespace, Labels: incoming.Labels, Annotations: maps.Clone(incoming.Annotations)},
		Spec:       *incoming.Spec.DeepCopy(),
	}
	if resource.Annotations == nil {
		resource.Annotations = make(map[string]string)
	}
	// Kubernetes and PostgreSQL cannot commit atomically. Block dispatch until
	// the actual UID returned by Create has a durable binding. Never infer the
	// creator from metadata, including this required-binding marker.
	resource.Annotations[v1alpha3.ScheduledRunBindingRequiredAnnotation] = "true"
	created, err := s.crud.Create(ctx, resource)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreateScheduledRunBinding(ctx, &dbpkg.ScheduledRunBinding{
		ScheduledRunNamespace: created.Namespace, ScheduledRunName: created.Name,
		ScheduledRunUID: string(created.UID), BoundUserID: principal.User.ID,
	}); err != nil {
		// A failed binding leaves the resource blocked from execution.
		return nil, serviceerrors.NewInternal("Failed to bind ScheduledRun; delete the pending resource and retry creation", err)
	}
	return created, nil
}

// BoundUserID returns the server-owned binding for an already authorized
// resource. CRD fields and annotations cannot establish or change this binding.
func (s *Service) BoundUserID(ctx context.Context, resource *v1alpha3.ScheduledRun) (string, error) {
	binding, err := s.store.GetScheduledRunBinding(ctx, resource.Namespace, resource.Name, string(resource.UID))
	if errors.Is(err, dbpkg.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", serviceerrors.NewInternal("Failed to get ScheduledRun user binding", err)
	}
	return binding.BoundUserID, nil
}

func (s *Service) Update(ctx context.Context, incoming *v1alpha3.ScheduledRun) (*v1alpha3.ScheduledRun, error) {
	if incoming == nil || incoming.UID == "" || incoming.Generation < 1 {
		return nil, serviceerrors.NewInvalidArgument("ScheduledRun metadata.uid and metadata.generation from the read resource are required", nil)
	}
	ref := types.NamespacedName{Namespace: incoming.Namespace, Name: incoming.Name}
	var updated *v1alpha3.ScheduledRun
	// Generation fences spec edits without rejecting a draft whenever the
	// controller writes status. The live resourceVersion makes each write atomic;
	// a retry must recheck the original UID and generation before applying it.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := s.crud.GetForUpdate(ctx, ref)
		if err != nil {
			return err
		}
		if existing.UID != incoming.UID || existing.Generation != incoming.Generation {
			return serviceerrors.NewAborted("ScheduledRun was replaced or its spec changed; reload before editing", nil)
		}
		existing.Spec = *incoming.Spec.DeepCopy()
		if err := s.kube.Update(ctx, existing); err != nil {
			return err
		}
		updated = existing
		return nil
	})
	if err != nil {
		switch {
		case serviceerrors.CodeOf(err) != "":
			return nil, err
		case apierrors.IsConflict(err):
			return nil, serviceerrors.NewAborted("ScheduledRun changed during update; retry with its current state", err)
		case apierrors.IsInvalid(err):
			return nil, serviceerrors.NewInvalidArgument("Invalid ScheduledRun", err)
		case apierrors.IsNotFound(err):
			return nil, serviceerrors.NewNotFound("ScheduledRun not found", err)
		default:
			return nil, serviceerrors.NewInternal("Failed to update ScheduledRun", err)
		}
	}
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, ref types.NamespacedName) error {
	return s.crud.Delete(ctx, ref)
}

type ListExecutionsRequest struct {
	Ref       types.NamespacedName
	PageSize  int
	PageToken string
}

type ListExecutionsResult struct {
	Executions    []dbpkg.ScheduledRunExecution
	NextPageToken string
}

type executionCursor struct {
	UID      string    `json:"uid"`
	Before   time.Time `json:"before"`
	BeforeID string    `json:"beforeID"`
}

func (s *Service) ListExecutions(ctx context.Context, request ListExecutionsRequest) (*ListExecutionsResult, error) {
	sr, err := s.crud.Get(ctx, request.Ref)
	if err != nil {
		return nil, err
	}
	options, err := executionQuery(request.PageSize, request.PageToken, string(sr.UID))
	if err != nil {
		return nil, err
	}
	executions, err := s.store.ListScheduledRunExecutions(ctx, sr.Namespace, sr.Name, string(sr.UID), options)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to list ScheduledRun executions", err)
	}
	result := &ListExecutionsResult{Executions: executions}
	pageSize := options.Limit - 1
	if len(executions) > pageSize {
		result.Executions = executions[:pageSize]
		last := result.Executions[len(result.Executions)-1]
		cursor, err := json.Marshal(executionCursor{UID: string(sr.UID), Before: last.StartTime, BeforeID: last.ID})
		if err != nil {
			return nil, serviceerrors.NewInternal("Failed to encode execution page", err)
		}
		result.NextPageToken = base64.RawURLEncoding.EncodeToString(cursor)
	}
	return result, nil
}

func executionQuery(pageSize int, pageToken, uid string) (dbpkg.ScheduledRunExecutionQuery, error) {
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	options := dbpkg.ScheduledRunExecutionQuery{Limit: pageSize + 1}
	if pageToken == "" {
		return options, nil
	}
	cursorData, err := base64.RawURLEncoding.DecodeString(pageToken)
	if err != nil {
		return options, serviceerrors.NewInvalidArgument("Invalid execution page token", err)
	}
	var cursor executionCursor
	if err := json.Unmarshal(cursorData, &cursor); err != nil {
		return options, serviceerrors.NewInvalidArgument("Invalid execution page token", err)
	}
	if cursor.UID != uid || cursor.Before.IsZero() || cursor.BeforeID == "" {
		return options, serviceerrors.NewInvalidArgument("Execution page token does not match this ScheduledRun", nil)
	}
	options.Before, options.BeforeID = cursor.Before, cursor.BeforeID
	return options, nil
}

func (s *Service) Trigger(ctx context.Context, ref types.NamespacedName) (*dbpkg.ScheduledRunExecution, error) {
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok {
		return nil, serviceerrors.NewUnauthenticated("Authentication is required", nil)
	}
	if err := s.authorizer.Check(ctx, session.Principal(), auth.VerbCreate, auth.Resource{Type: resourceKind, Name: ref.String()}); err != nil {
		return nil, serviceerrors.NewPermissionDenied("Not authorized to trigger ScheduledRun", err)
	}
	execution, err := s.trigger.TriggerManualExecution(ctx, ref)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, serviceerrors.NewNotFound("ScheduledRun not found", err)
		}
		return nil, serviceerrors.NewInternal("Failed to trigger ScheduledRun", fmt.Errorf("failed to enqueue ScheduledRun %s: %w", ref, err))
	}
	if execution == nil {
		return nil, serviceerrors.NewFailedPrecondition("ScheduledRun is being deleted", nil)
	}
	return execution, nil
}
