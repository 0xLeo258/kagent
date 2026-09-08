package grpcserver

import (
	"context"
	"fmt"
	"iter"
	"net"
	"sync/atomic"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/a2aproject/a2a-go/v2/a2apb/v1/pbconv"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/service/agentinstance"
	scheduledrunservice "github.com/kagent-dev/kagent/go/core/internal/service/scheduledrun"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const scheduledIntegrationInstanceID = "01993019-e480-7412-96fb-f239f1f000c1"
const scheduledIntegrationContextID = "01993019-e480-7412-96fb-f239f1f000c2"

type scheduledIntegrationStore struct {
	*dbpkg.Client
	userID string
}

func (s *scheduledIntegrationStore) GetScheduledRunExecutionByAgentInstanceID(_ context.Context, id string) (*dbpkg.ScheduledRunExecution, error) {
	if id != scheduledIntegrationInstanceID {
		return nil, dbpkg.ErrNotFound
	}
	return &dbpkg.ScheduledRunExecution{ID: "execution-1", AgentInstanceID: id, ScheduledRunNamespace: "team", ScheduledRunName: "nightly", ScheduledRunUID: "owner", UserID: s.userID}, nil
}

func (s *scheduledIntegrationStore) GetAgentInstance(_ context.Context, id, owner string) (*apiv1alpha1.AgentInstance, error) {
	if id != scheduledIntegrationInstanceID || owner != s.userID {
		return nil, dbpkg.ErrNotFound
	}
	return &apiv1alpha1.AgentInstance{Id: id, ContextId: scheduledIntegrationContextID, Creator: owner, State: apiv1alpha1.AgentInstanceState_AGENT_INSTANCE_STATE_READY}, nil
}

type scheduledIntegrationGateway struct {
	a2asrv.RequestHandler
	sends atomic.Int32
	reads atomic.Int32
	owner string
}

func (g *scheduledIntegrationGateway) identity(ctx context.Context, caller string) error {
	share, ok := auth.ShareContextFrom(ctx)
	if !ok || !share.IsForAgentInstance(scheduledIntegrationInstanceID) || share.UserID != g.owner {
		return fmt.Errorf("scheduled owner is missing from gateway context")
	}
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session.Principal().User.ID != caller {
		return fmt.Errorf("gateway lost the initiating user")
	}
	return nil
}

func (g *scheduledIntegrationGateway) ListTasks(ctx context.Context, _ *a2atype.ListTasksRequest) (*a2atype.ListTasksResponse, error) {
	if err := g.identity(ctx, "bob"); err != nil {
		return nil, err
	}
	g.reads.Add(1)
	return &a2atype.ListTasksResponse{Tasks: []*a2atype.Task{{ID: "task-1", ContextID: scheduledIntegrationContextID, Status: a2atype.TaskStatus{State: a2atype.TaskStateWorking}}}}, nil
}

func (g *scheduledIntegrationGateway) SendMessage(ctx context.Context, _ *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error) {
	if err := g.identity(ctx, "alice"); err != nil {
		return nil, err
	}
	g.sends.Add(1)
	return a2atype.NewMessage(a2atype.MessageRoleAgent, a2atype.NewTextPart("continued")), nil
}

func (g *scheduledIntegrationGateway) SendStreamingMessage(ctx context.Context, request *a2atype.SendMessageRequest) iter.Seq2[a2atype.Event, error] {
	return func(yield func(a2atype.Event, error) bool) {
		message, err := g.SendMessage(ctx, request)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(message.(*a2atype.Message), nil)
	}
}

var _ a2asrv.RequestHandler = (*scheduledIntegrationGateway)(nil)

func TestScheduledRunAccessThroughRegisteredGRPCServices(t *testing.T) {
	for _, userID := range []string{"alice", auth.ScheduledRunUserID} {
		for _, provider := range []auth.AuthProvider{&authimpl.UnsecureAuthenticator{}, authimpl.NewProxyAuthenticator("sub")} {
			t.Run(fmt.Sprintf("owner=%s/%T", userID, provider), func(t *testing.T) {
				testScheduledRunAccessThroughRegisteredGRPCServices(t, userID, provider)
			})
		}
	}
}

func testScheduledRunAccessThroughRegisteredGRPCServices(t *testing.T, userID string, provider auth.AuthProvider) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	run := grpcScheduledRun()
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	store := &scheduledIntegrationStore{userID: userID}
	authorizer := &authimpl.NoopAuthorizer{}
	schedules := scheduledrunservice.NewService(kube, authorizer, store, nil)
	gateway := &scheduledIntegrationGateway{owner: store.userID}
	listener := bufconn.Listen(DefaultMaxMessageSize)
	server, err := New(Config{Listener: listener, Registerer: prometheus.NewRegistry(), Authenticator: provider, SystemService: testSystemService(), ScheduledRunAccessResolver: schedules, AgentInstanceService: agentinstance.NewService(store, authorizer, nil), A2AHandler: gateway})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	instances := apiv1alpha1.NewAgentInstanceServiceClient(connection)
	a2aclient := a2apb.NewA2AServiceClient(connection)
	const bobToken = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJib2IifQ.signature"
	const aliceToken = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhbGljZSJ9.signature"
	callContext := func(caller, token string) context.Context {
		return metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token, "x-user-id", caller, apia2a.AgentInstanceIDHeader, scheduledIntegrationInstanceID))
	}
	callCtx := callContext("bob", bobToken)
	getRequest := &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID}
	response, err := instances.GetAgentInstance(callCtx, getRequest)
	require.NoError(t, err)
	require.True(t, response.GetReadOnly())
	require.True(t, response.GetScheduledRun())
	require.Equal(t, store.userID, response.GetAgentInstance().GetCreator())
	history, err := a2aclient.ListTasks(callCtx, &a2apb.ListTasksRequest{ContextId: scheduledIntegrationContextID})
	require.NoError(t, err)
	require.Len(t, history.GetTasks(), 1)
	require.EqualValues(t, 1, gateway.reads.Load())
	message := a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart("continue"))
	message.ContextID = scheduledIntegrationContextID
	sendRequest, err := pbconv.ToProtoSendMessageRequest(&a2atype.SendMessageRequest{Message: message})
	require.NoError(t, err)
	_, err = a2aclient.SendMessage(callCtx, sendRequest)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Zero(t, gateway.sends.Load())
	stream, err := a2aclient.SendStreamingMessage(callCtx, sendRequest)
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Zero(t, gateway.sends.Load())

	// Only the immutable execution owner can continue; unbound executions
	// remain read-only for every human caller.
	callCtx = callContext("alice", aliceToken)
	response, err = instances.GetAgentInstance(callCtx, getRequest)
	require.NoError(t, err)
	require.Equal(t, userID != "alice", response.GetReadOnly())
	require.True(t, response.GetScheduledRun())
	_, err = a2aclient.SendMessage(callCtx, sendRequest)
	if userID == "alice" {
		require.NoError(t, err)
		require.EqualValues(t, 1, gateway.sends.Load())
	} else {
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Zero(t, gateway.sends.Load())
	}
	stream, err = a2aclient.SendStreamingMessage(callCtx, sendRequest)
	require.NoError(t, err)
	_, err = stream.Recv()
	if userID == "alice" {
		require.NoError(t, err)
		require.EqualValues(t, 2, gateway.sends.Load())
	} else {
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Zero(t, gateway.sends.Load())
	}

	// Schedule permission never grants control over durable history ownership.
	_, err = instances.DeleteAgentInstance(callCtx, &apiv1alpha1.DeleteAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// Proxy auth accepts explicit user IDs on agent callbacks for memory, but
	// those delegated identities cannot impersonate the bound human owner.
	spoofedCtx := metadata.AppendToOutgoingContext(callContext("alice", bobToken), "x-agent-name", "untrusted-agent")
	response, err = instances.GetAgentInstance(spoofedCtx, getRequest)
	require.NoError(t, err)
	require.True(t, response.GetReadOnly())
	previousSends := gateway.sends.Load()
	_, err = a2aclient.SendMessage(spoofedCtx, sendRequest)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	stream, err = a2aclient.SendStreamingMessage(spoofedCtx, sendRequest)
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, previousSends, gateway.sends.Load())
	_, err = a2aclient.CancelTask(spoofedCtx, &a2apb.CancelTaskRequest{Id: "task-1"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = instances.ResumeAgentInstance(spoofedCtx, &apiv1alpha1.ResumeAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = instances.SuspendAgentInstance(spoofedCtx, &apiv1alpha1.SuspendAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
