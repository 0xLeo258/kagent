package grpcserver

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	adkauth "github.com/kagent-dev/kagent/go/adk/pkg/auth"
	"github.com/kagent-dev/kagent/go/adk/pkg/controllerclient"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/service/agentinstance"
	memoryservice "github.com/kagent-dev/kagent/go/core/internal/service/memory"
	scheduledrunservice "github.com/kagent-dev/kagent/go/core/internal/service/scheduledrun"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/pgvector/pgvector-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type scheduledMemoryStore struct {
	mu    sync.Mutex
	calls []string
}

func (s *scheduledMemoryStore) record(ctx context.Context, userID, method string) error {
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session.Principal().User.ID != auth.ScheduledRunUserID || userID != auth.ScheduledRunUserID {
		return fmt.Errorf("memory callback lost scheduled execution identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, method)
	return nil
}

func (s *scheduledMemoryStore) StoreAgentMemory(ctx context.Context, memory *database.Memory) error {
	return s.record(ctx, memory.UserID, "add")
}

func (s *scheduledMemoryStore) StoreAgentMemories(ctx context.Context, memories []*database.Memory) error {
	return s.record(ctx, memories[0].UserID, "batch")
}

func (s *scheduledMemoryStore) SearchAgentMemory(ctx context.Context, _, userID string, _ pgvector.Vector, _ int) ([]database.AgentMemorySearchResult, error) {
	return nil, s.record(ctx, userID, "search")
}

func (s *scheduledMemoryStore) ListAgentMemories(ctx context.Context, _, userID string) ([]database.Memory, error) {
	return nil, s.record(ctx, userID, "list")
}

func (s *scheduledMemoryStore) DeleteAgentMemory(ctx context.Context, _, userID string) error {
	return s.record(ctx, userID, "delete")
}

func TestScheduledRunMemoryCallbacksPreserveReservedIdentityGuard(t *testing.T) {
	store := &scheduledMemoryStore{}
	authorizer := &authimpl.NoopAuthorizer{}
	listener := bufconn.Listen(DefaultMaxMessageSize)
	server, err := New(Config{
		Listener: listener, Registerer: prometheus.NewRegistry(),
		Authenticator:        &authimpl.UnsecureAuthenticator{},
		SystemService:        testSystemService(),
		MemoryService:        memoryservice.NewService(store),
		AgentInstanceService: agentinstance.NewService(nil, authorizer, nil),
		ScheduledRunService:  scheduledrunservice.NewService(nil, authorizer, nil, nil),
		A2AHandler:           &scheduledIntegrationGateway{},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	dial := grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() })
	callback, err := controllerclient.New(controllerclient.Config{APIURL: "http://controller.test", AgentName: "scheduled-agent", DialOptions: []grpc.DialOption{dial}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, callback.Close()) })
	// Use the same identity propagation as the GoADK A2A executor and memory client.
	callCtx, callCancel := callback.CallContext(adkauth.WithUserID(t.Context(), auth.ScheduledRunUserID), "")
	defer callCancel()
	client := callback.MemoryService()
	input := &apiv1alpha1.SessionMemoryInput{AgentName: "scheduled-agent", UserId: auth.ScheduledRunUserID, Content: "scheduled result", Vector: make([]float32, memoryservice.VectorDimension)}
	_, err = client.AddSession(callCtx, &apiv1alpha1.MemoryServiceAddSessionRequest{Memory: input})
	require.NoError(t, err)
	batch, err := client.AddSessionBatch(callCtx, &apiv1alpha1.MemoryServiceAddSessionBatchRequest{Items: []*apiv1alpha1.SessionMemoryInput{input}})
	require.NoError(t, err)
	require.EqualValues(t, 1, batch.GetCount())
	_, err = client.Search(callCtx, &apiv1alpha1.MemoryServiceSearchRequest{AgentName: input.AgentName, UserId: input.UserId, Vector: input.Vector})
	require.NoError(t, err)
	_, err = client.List(callCtx, &apiv1alpha1.MemoryServiceListRequest{AgentName: input.AgentName, UserId: input.UserId})
	require.NoError(t, err)
	_, err = client.Delete(callCtx, &apiv1alpha1.MemoryServiceDeleteRequest{AgentName: input.AgentName, UserId: input.UserId})
	require.NoError(t, err)
	store.mu.Lock()
	require.Equal(t, []string{"add", "batch", "search", "list", "delete"}, store.calls)
	store.mu.Unlock()

	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), dial)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	instances := apiv1alpha1.NewAgentInstanceServiceClient(connection)
	schedules := apiv1alpha1.NewScheduledRunServiceClient(connection)
	_, err = instances.GetAgentInstance(callCtx, &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = instances.DeleteAgentInstance(callCtx, &apiv1alpha1.DeleteAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = schedules.ListScheduledRuns(callCtx, &apiv1alpha1.ListScheduledRunsRequest{Namespace: "team"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = schedules.TriggerScheduledRun(callCtx, &apiv1alpha1.TriggerScheduledRunRequest{Ref: &apiv1alpha1.ResourceReference{Namespace: "team", Name: "nightly"}})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = a2apb.NewA2AServiceClient(connection).ListTasks(callCtx, &a2apb.ListTasksRequest{})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
