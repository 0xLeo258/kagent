package scheduledrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type executionHistoryStore struct {
	store
	calls []dbpkg.ScheduledRunExecutionQuery
	uid   string
	items []dbpkg.ScheduledRunExecution
	err   error
}

func (s *executionHistoryStore) ListScheduledRunExecutions(_ context.Context, _, _, uid string, query dbpkg.ScheduledRunExecutionQuery) ([]dbpkg.ScheduledRunExecution, error) {
	s.uid = uid
	s.calls = append(s.calls, query)
	return s.items, s.err
}

var _ store = (*executionHistoryStore)(nil)

type manualTrigger struct {
	key    types.NamespacedName
	called bool
}

func (t *manualTrigger) TriggerManualExecution(_ context.Context, key types.NamespacedName) (*dbpkg.ScheduledRunExecution, error) {
	t.called, t.key = true, key
	return &dbpkg.ScheduledRunExecution{ID: "accepted", Status: v1alpha3.ScheduledRunExecutionStatus_InProgress}, nil
}

var _ trigger = (*manualTrigger)(nil)

func testScheduledRun() *v1alpha3.ScheduledRun {
	group := v1alpha3.GroupVersion.Group
	return &v1alpha3.ScheduledRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "nightly", UID: "owner-uid", Labels: map[string]string{"managed-by": "gitops"}},
		Spec:       v1alpha3.ScheduledRunSpec{Schedule: "0 * * * *", Prompt: "Check health", TargetRef: corev1.TypedLocalObjectReference{APIGroup: &group, Kind: "AgentTemplate", Name: "template"}, HarnessRef: corev1.LocalObjectReference{Name: "harness"}},
		Status:     v1alpha3.ScheduledRunStatus{ObservedGeneration: 7},
	}
}

func serviceContext(t *testing.T) context.Context {
	t.Helper()
	return auth.AuthSessionTo(t.Context(), &authimpl.SimpleSession{P: auth.Principal{User: auth.User{ID: "alice"}}})
}

func TestUpdateScheduledRunPreservesMetadataAndStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	original := testScheduledRun()
	original.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "gitops", Operation: metav1.ManagedFieldsOperationApply, APIVersion: v1alpha3.GroupVersion.String(), FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:spec":{}}`)}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(original).WithObjects(original).Build()
	service := NewService(kube, &authimpl.NoopAuthorizer{}, nil, nil)
	ctx := serviceContext(t)
	fetched, err := service.Get(ctx, types.NamespacedName{Namespace: "team", Name: "nightly"})
	require.NoError(t, err)
	fetched.Spec.Suspended = new(true)
	fetched.Status.ObservedGeneration = 999
	fetched.UID = "untrusted-uid"
	fetched.Labels = map[string]string{"untrusted": "label"}
	updated, err := service.Update(ctx, fetched)
	require.NoError(t, err)
	require.True(t, *updated.Spec.Suspended)
	require.Equal(t, types.UID("owner-uid"), updated.UID)
	require.EqualValues(t, 7, updated.Status.ObservedGeneration)
	require.Equal(t, original.Labels, updated.Labels)
	updated.Spec.Suspended = new(false)
	resumed, err := service.Update(ctx, updated)
	require.NoError(t, err)
	require.False(t, *resumed.Spec.Suspended)
}

func TestUpdateScheduledRunRejectsTargetAndHarnessChanges(t *testing.T) {
	for _, change := range []string{"target", "harness"} {
		t.Run(change, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha3.AddToScheme(scheme))
			original := testScheduledRun()
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(original).Build()
			service := NewService(kube, &authimpl.NoopAuthorizer{}, nil, nil)
			if change == "target" {
				original.Spec.TargetRef.Name = "another"
			} else {
				original.Spec.HarnessRef.Name = "another"
			}
			_, err := service.Update(serviceContext(t), original)
			require.Equal(t, serviceerrors.CodeInvalidArgument, serviceerrors.CodeOf(err))
		})
	}
}

func TestExecutionHistoryCursorScopesUIDAndKeepsEqualTimestamps(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(testScheduledRun()).Build()
	now := time.Now().UTC()
	history := &executionHistoryStore{items: []dbpkg.ScheduledRunExecution{{ID: "c", StartTime: now}, {ID: "b", StartTime: now}, {ID: "a", StartTime: now}}}
	service := NewService(kube, &authimpl.NoopAuthorizer{}, history, nil)
	request := ListExecutionsRequest{Ref: types.NamespacedName{Namespace: "team", Name: "nightly"}, PageSize: 2}
	page, err := service.ListExecutions(serviceContext(t), request)
	require.NoError(t, err)
	require.Len(t, page.Executions, 2)
	require.NotEmpty(t, page.NextPageToken)
	require.Equal(t, "owner-uid", history.uid)
	history.items = history.items[2:]
	request.PageToken = page.NextPageToken
	next, err := service.ListExecutions(serviceContext(t), request)
	require.NoError(t, err)
	require.Len(t, next.Executions, 1)
	require.Empty(t, next.NextPageToken)
	require.Equal(t, "b", history.calls[1].BeforeID)
	require.True(t, now.Equal(history.calls[1].Before))
	_, err = executionQuery(2, page.NextPageToken, "replacement-uid")
	require.Equal(t, serviceerrors.CodeInvalidArgument, serviceerrors.CodeOf(err))
}

func TestTriggerRequiresCreateAuthorizationAndQueuesOnce(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "denied"}[denied], func(t *testing.T) {
			authorizer := &accessAuthorizer{}
			if denied {
				authorizer.err = errors.New("denied")
			}
			dispatcher := &manualTrigger{}
			service := NewService(nil, authorizer, nil, dispatcher)
			key := types.NamespacedName{Namespace: "team", Name: "nightly"}
			result, err := service.Trigger(serviceContext(t), key)
			require.Equal(t, auth.VerbCreate, authorizer.verb)
			require.Equal(t, auth.Resource{Type: "ScheduledRun", Name: key.String()}, authorizer.resource)
			require.Equal(t, !denied, dispatcher.called)
			if denied {
				require.Equal(t, serviceerrors.CodePermissionDenied, serviceerrors.CodeOf(err))
			} else {
				require.NoError(t, err)
				require.Equal(t, "accepted", result.ID)
			}
		})
	}
}
