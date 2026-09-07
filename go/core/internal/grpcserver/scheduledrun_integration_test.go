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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const scheduledIntegrationInstanceID = "01993019-e480-7412-96fb-f239f1f000c1"
const scheduledIntegrationContextID = "01993019-e480-7412-96fb-f239f1f000c2"

type scheduledIntegrationStore struct{ *dbpkg.Client }

func (s *scheduledIntegrationStore) GetScheduledRunExecutionByAgentInstanceID(_ context.Context, id string) (*dbpkg.ScheduledRunExecution, error) {
	if id != scheduledIntegrationInstanceID {
		return nil, dbpkg.ErrNotFound
	}
	return &dbpkg.ScheduledRunExecution{ID: "execution-1", AgentInstanceID: id, ScheduledRunNamespace: "team", ScheduledRunName: "nightly", ScheduledRunUID: "owner"}, nil
}

func (s *scheduledIntegrationStore) GetAgentInstance(_ context.Context, id, owner string) (*apiv1alpha1.AgentInstance, error) {
	if id != scheduledIntegrationInstanceID || owner != auth.ScheduledRunUserID {
		return nil, dbpkg.ErrNotFound
	}
	return &apiv1alpha1.AgentInstance{Id: id, ContextId: scheduledIntegrationContextID, Creator: owner, State: apiv1alpha1.AgentInstanceState_AGENT_INSTANCE_STATE_READY}, nil
}

type scheduledIntegrationGateway struct {
	a2asrv.RequestHandler
	sends atomic.Int32
	reads atomic.Int32
}

func scheduledIntegrationIdentity(ctx context.Context) error {
	share, ok := auth.ShareContextFrom(ctx)
	if !ok || !share.IsForAgentInstance(scheduledIntegrationInstanceID) || share.UserID != auth.ScheduledRunUserID {
		return fmt.Errorf("scheduled owner is missing from gateway context")
	}
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session.Principal().User.ID != "alice" {
		return fmt.Errorf("gateway lost the initiating user")
	}
	return nil
}

func (g *scheduledIntegrationGateway) ListTasks(ctx context.Context, _ *a2atype.ListTasksRequest) (*a2atype.ListTasksResponse, error) {
	if err := scheduledIntegrationIdentity(ctx); err != nil {
		return nil, err
	}
	g.reads.Add(1)
	return &a2atype.ListTasksResponse{Tasks: []*a2atype.Task{{ID: "task-1", ContextID: scheduledIntegrationContextID, Status: a2atype.TaskStatus{State: a2atype.TaskStateWorking}}}}, nil
}

func (g *scheduledIntegrationGateway) SendMessage(ctx context.Context, _ *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error) {
	if err := scheduledIntegrationIdentity(ctx); err != nil {
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
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	run := grpcScheduledRun()
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	store := &scheduledIntegrationStore{}
	authorizer := &authimpl.NoopAuthorizer{}
	schedules := scheduledrunservice.NewService(kube, authorizer, store, nil)
	gateway := &scheduledIntegrationGateway{}
	listener := bufconn.Listen(DefaultMaxMessageSize)
	server, err := New(Config{Listener: listener, Registerer: prometheus.NewRegistry(), Authenticator: &authimpl.UnsecureAuthenticator{}, SystemService: testSystemService(), ScheduledRunAccessResolver: schedules, AgentInstanceService: agentinstance.NewService(store, authorizer, nil), A2AHandler: gateway})
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
	callCtx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("x-user-id", "alice", apia2a.AgentInstanceIDHeader, scheduledIntegrationInstanceID))
	getRequest := &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID}
	response, err := instances.GetAgentInstance(callCtx, getRequest)
	require.NoError(t, err)
	require.True(t, response.GetReadOnly())
	require.True(t, response.GetScheduledRun())
	require.Equal(t, auth.ScheduledRunUserID, response.GetAgentInstance().GetCreator())
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

	// A changed CR permission must take effect without reissuing a token or restarting the server.
	require.NoError(t, kube.Get(t.Context(), types.NamespacedName{Namespace: "team", Name: "nightly"}, run))
	run.Spec.AllowSessionInteraction = new(true)
	require.NoError(t, kube.Update(t.Context(), run))
	response, err = instances.GetAgentInstance(callCtx, getRequest)
	require.NoError(t, err)
	require.False(t, response.GetReadOnly())
	require.True(t, response.GetScheduledRun())
	_, err = a2aclient.SendMessage(callCtx, sendRequest)
	require.NoError(t, err)
	require.EqualValues(t, 1, gateway.sends.Load())
	stream, err = a2aclient.SendStreamingMessage(callCtx, sendRequest)
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	require.EqualValues(t, 2, gateway.sends.Load())

	// Schedule permission never grants control over durable history ownership.
	_, err = instances.DeleteAgentInstance(callCtx, &apiv1alpha1.DeleteAgentInstanceRequest{AgentInstanceId: scheduledIntegrationInstanceID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
