package grpcserver

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	scheduledrunservice "github.com/kagent-dev/kagent/go/core/internal/service/scheduledrun"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type grpcExecutionStore struct {
	dbpkg.Client
	mu       sync.Mutex
	bindings map[string]dbpkg.ScheduledRunBinding
}

func (s *grpcExecutionStore) CreateScheduledRunBinding(_ context.Context, binding *dbpkg.ScheduledRunBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings == nil {
		s.bindings = make(map[string]dbpkg.ScheduledRunBinding)
	}
	s.bindings[binding.ScheduledRunUID] = *binding
	return nil
}

func (s *grpcExecutionStore) GetScheduledRunBinding(_ context.Context, namespace, name, uid string) (*dbpkg.ScheduledRunBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[uid]
	if !ok || binding.ScheduledRunNamespace != namespace || binding.ScheduledRunName != name {
		return nil, dbpkg.ErrNotFound
	}
	return &binding, nil
}

func (s *grpcExecutionStore) ListScheduledRunExecutions(_ context.Context, namespace, name, uid string, _ dbpkg.ScheduledRunExecutionQuery) ([]dbpkg.ScheduledRunExecution, error) {
	return []dbpkg.ScheduledRunExecution{{ID: "run-1", ScheduledRunNamespace: namespace, ScheduledRunName: name, ScheduledRunUID: uid, StartTime: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), AgentInstanceID: "01993019-e480-7412-96fb-f239f1f000c1", Status: v1alpha3.ScheduledRunExecutionStatus_InProgress}}, nil
}

type grpcExecutionTrigger struct{}

func (t *grpcExecutionTrigger) TriggerManualExecution(_ context.Context, key types.NamespacedName) (*dbpkg.ScheduledRunExecution, error) {
	return &dbpkg.ScheduledRunExecution{ID: "queued", ScheduledRunNamespace: key.Namespace, ScheduledRunName: key.Name, StartTime: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Status: v1alpha3.ScheduledRunExecutionStatus_InProgress, Trigger: v1alpha3.ScheduledRunExecutionTrigger_Manual}, nil
}

func newScheduledRunConnection(t *testing.T, objects ...ctrlclient.Object) apiv1alpha1.ScheduledRunServiceClient {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha3.ScheduledRun{}).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c ctrlclient.WithWatch, obj ctrlclient.Object, opts ...ctrlclient.CreateOption) error {
			obj.SetUID(types.UID(uuid.NewString()))
			obj.SetGeneration(1)
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	return newScheduledRunConnectionWithKube(t, kube)
}

func newScheduledRunConnectionWithKube(t *testing.T, kube ctrlclient.Client) apiv1alpha1.ScheduledRunServiceClient {
	t.Helper()
	listener := bufconn.Listen(DefaultMaxMessageSize)
	server, err := New(Config{Listener: listener, Registerer: prometheus.NewRegistry(), Authenticator: &authimpl.UnsecureAuthenticator{}, SystemService: testSystemService(), ScheduledRunService: scheduledrunservice.NewService(kube, &authimpl.NoopAuthorizer{}, &grpcExecutionStore{}, &grpcExecutionTrigger{})})
	require.NoError(t, err)
	serverCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Start(serverCtx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	return apiv1alpha1.NewScheduledRunServiceClient(connection)
}

func grpcScheduledRun() *v1alpha3.ScheduledRun {
	group := v1alpha3.GroupVersion.Group
	return &v1alpha3.ScheduledRun{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "nightly", UID: "owner", Generation: 1, ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "gitops", Operation: metav1.ManagedFieldsOperationApply, APIVersion: v1alpha3.GroupVersion.String(), FieldsType: "FieldsV1", FieldsV1: metav1.NewFieldsV1(`{"f:spec":{}}`)}}}, Spec: v1alpha3.ScheduledRunSpec{Schedule: "0 * * * *", Prompt: "Check health", TargetRef: corev1.TypedLocalObjectReference{APIGroup: &group, Kind: "AgentTemplate", Name: "template"}, HarnessRef: corev1.LocalObjectReference{Name: "harness"}}, Status: v1alpha3.ScheduledRunStatus{ObservedGeneration: 7}}
}

func TestScheduledRunGRPCGetUpdateRoundTrip(t *testing.T) {
	original := grpcScheduledRun()
	client := newScheduledRunConnection(t, original)
	ctx := t.Context()
	ref := &apiv1alpha1.ResourceReference{Namespace: "team", Name: "nightly"}
	fetched, err := client.GetScheduledRun(ctx, &apiv1alpha1.GetScheduledRunRequest{Ref: ref})
	require.NoError(t, err)
	var resource v1alpha3.ScheduledRun
	require.Empty(t, fetched.GetScheduledRun().GetBoundUserId(), "direct Kubernetes resources have no user binding")
	require.NoError(t, structuredobject.ToGo(fetched.GetScheduledRun().GetResource(), "ScheduledRun", &resource, DefaultMaxMessageSize))
	resource.Spec.Suspended = new(true)
	resource.Status.ObservedGeneration = 1000
	wire, err := structuredobject.FromGo(&resource, v1alpha3.GroupVersion.String(), "ScheduledRun", DefaultMaxMessageSize)
	require.NoError(t, err)
	updated, err := client.UpdateScheduledRun(ctx, &apiv1alpha1.UpdateScheduledRunRequest{Ref: ref, Resource: wire})
	require.NoError(t, err)
	var persisted v1alpha3.ScheduledRun
	require.NoError(t, structuredobject.ToGo(updated.GetScheduledRun().GetResource(), "ScheduledRun", &persisted, DefaultMaxMessageSize))
	require.True(t, *persisted.Spec.Suspended)
	require.Equal(t, original.UID, persisted.UID)
	require.Equal(t, original.Status, persisted.Status)
	history, err := client.ListScheduledRunExecutions(ctx, &apiv1alpha1.ListScheduledRunExecutionsRequest{Ref: ref})
	require.NoError(t, err)
	require.Len(t, history.GetExecutions(), 1)
	require.NotEmpty(t, history.GetExecutions()[0].GetAgentInstanceId())
	triggered, err := client.TriggerScheduledRun(ctx, &apiv1alpha1.TriggerScheduledRunRequest{Ref: ref})
	require.NoError(t, err)
	require.Equal(t, "InProgress", triggered.GetExecution().GetStatus())
	require.Equal(t, "Manual", triggered.GetExecution().GetTrigger())
	_, err = client.DeleteScheduledRun(ctx, &apiv1alpha1.DeleteScheduledRunRequest{Ref: ref})
	require.NoError(t, err)
	_, err = client.GetScheduledRun(ctx, &apiv1alpha1.GetScheduledRunRequest{Ref: ref})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestScheduledRunGRPCValidation(t *testing.T) {
	client := newScheduledRunConnection(t)
	for _, test := range []struct {
		name string
		call func(context.Context) error
	}{
		{"empty list namespace", func(ctx context.Context) error {
			_, err := client.ListScheduledRuns(ctx, &apiv1alpha1.ListScheduledRunsRequest{})
			return err
		}},
		{"missing reference", func(ctx context.Context) error {
			_, err := client.GetScheduledRun(ctx, &apiv1alpha1.GetScheduledRunRequest{})
			return err
		}},
		{"empty reference name", func(ctx context.Context) error {
			_, err := client.GetScheduledRun(ctx, &apiv1alpha1.GetScheduledRunRequest{Ref: &apiv1alpha1.ResourceReference{Namespace: "team"}})
			return err
		}},
		{"missing create resource", func(ctx context.Context) error {
			_, err := client.CreateScheduledRun(ctx, &apiv1alpha1.CreateScheduledRunRequest{Ref: &apiv1alpha1.ResourceReference{Namespace: "team", Name: "nightly"}})
			return err
		}},
		{"negative page size", func(ctx context.Context) error {
			_, err := client.ListScheduledRunExecutions(ctx, &apiv1alpha1.ListScheduledRunExecutionsRequest{Ref: &apiv1alpha1.ResourceReference{Namespace: "team", Name: "nightly"}, Page: &apiv1alpha1.PageRequest{Limit: -1}})
			return err
		}},
		{"excessive page size", func(ctx context.Context) error {
			_, err := client.ListScheduledRunExecutions(ctx, &apiv1alpha1.ListScheduledRunExecutionsRequest{Ref: &apiv1alpha1.ResourceReference{Namespace: "team", Name: "nightly"}, Page: &apiv1alpha1.PageRequest{Limit: 101}})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, codes.InvalidArgument, status.Code(test.call(t.Context()))) })
	}
}

func TestScheduledRunGRPCCreateRejectsMismatchedMetadata(t *testing.T) {
	client := newScheduledRunConnection(t)
	resource := grpcScheduledRun()
	wire, err := structuredobject.FromGo(resource, v1alpha3.GroupVersion.String(), "ScheduledRun", DefaultMaxMessageSize)
	require.NoError(t, err)
	_, err = client.CreateScheduledRun(t.Context(), &apiv1alpha1.CreateScheduledRunRequest{Ref: &apiv1alpha1.ResourceReference{Namespace: "other", Name: "nightly"}, Resource: wire})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestScheduledRunGRPCCreateDropsServerMetadata(t *testing.T) {
	client := newScheduledRunConnection(t)
	resource := grpcScheduledRun()
	resource.Labels = map[string]string{"team": "operations"}
	wire, err := structuredobject.FromGo(resource, v1alpha3.GroupVersion.String(), "ScheduledRun", DefaultMaxMessageSize)
	require.NoError(t, err)
	ref := &apiv1alpha1.ResourceReference{Namespace: resource.Namespace, Name: resource.Name}
	created, err := client.CreateScheduledRun(t.Context(), &apiv1alpha1.CreateScheduledRunRequest{Ref: ref, Resource: wire})
	require.NoError(t, err)
	require.Equal(t, "admin@kagent.dev", created.GetScheduledRun().GetBoundUserId())
	var persisted v1alpha3.ScheduledRun
	require.NoError(t, structuredobject.ToGo(created.GetScheduledRun().GetResource(), "ScheduledRun", &persisted, DefaultMaxMessageSize))
	require.NotEqual(t, resource.UID, persisted.UID)
	require.Zero(t, persisted.Status.ObservedGeneration)
	require.Equal(t, resource.Labels, persisted.Labels)
	require.Equal(t, resource.Spec.Prompt, persisted.Spec.Prompt)
	require.Contains(t, persisted.Annotations, v1alpha3.ScheduledRunBindingRequiredAnnotation)
	_, err = client.CreateScheduledRun(t.Context(), &apiv1alpha1.CreateScheduledRunRequest{Ref: ref, Resource: wire})
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	listed, err := client.ListScheduledRuns(t.Context(), &apiv1alpha1.ListScheduledRunsRequest{Namespace: resource.Namespace})
	require.NoError(t, err)
	require.Len(t, listed.GetScheduledRuns(), 1)
	require.Equal(t, "admin@kagent.dev", listed.GetScheduledRuns()[0].GetBoundUserId())
}
