package scheduledrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/internal/service/kubecrud"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultPageSize = 50
	resourceKind    = "ScheduledRun"
)

type store interface {
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
	// Only user-owned metadata is accepted on create. In particular, do not persist
	// a copied UID, resourceVersion or controller-written status from a GET response.
	resource := &v1alpha3.ScheduledRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha3.GroupVersion.String(), Kind: resourceKind},
		ObjectMeta: metav1.ObjectMeta{Name: incoming.Name, Namespace: incoming.Namespace, Labels: incoming.Labels, Annotations: incoming.Annotations},
		Spec:       *incoming.Spec.DeepCopy(),
	}
	return s.crud.Create(ctx, resource)
}

func (s *Service) Update(ctx context.Context, incoming *v1alpha3.ScheduledRun) (*v1alpha3.ScheduledRun, error) {
	existing, err := s.crud.GetForUpdate(ctx, types.NamespacedName{Namespace: incoming.Namespace, Name: incoming.Name})
	if err != nil {
		return nil, err
	}
	if !sameTarget(existing.Spec.TargetRef, incoming.Spec.TargetRef) || existing.Spec.HarnessRef != incoming.Spec.HarnessRef {
		return nil, serviceerrors.NewInvalidArgument("ScheduledRun targetRef and harnessRef are immutable", nil)
	}
	// Update the live resource's spec instead of applying a GET response. This
	// preserves resourceVersion, managedFields and status, and reports concurrent
	// modifications instead of taking ownership of unrelated fields.
	existing.Spec = *incoming.Spec.DeepCopy()
	if err := s.kube.Update(ctx, existing); err != nil {
		switch {
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
	return existing, nil
}

func sameTarget(left, right corev1.TypedLocalObjectReference) bool {
	if left.Kind != right.Kind || left.Name != right.Name {
		return false
	}
	if left.APIGroup == nil || right.APIGroup == nil {
		return left.APIGroup == nil && right.APIGroup == nil
	}
	return *left.APIGroup == *right.APIGroup
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
