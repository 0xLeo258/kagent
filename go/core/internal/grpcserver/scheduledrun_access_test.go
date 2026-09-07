package grpcserver

import (
	"context"
	"errors"
	"testing"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type scheduledAccessResolverFunc func(context.Context, string) (*auth.ShareContext, error)

func (f scheduledAccessResolverFunc) ResolveInstanceAccess(ctx context.Context, id string) (*auth.ShareContext, error) {
	return f(ctx, id)
}

func TestScheduledRunRequestAccess(t *testing.T) {
	const instanceID = "01993019-e480-7412-96fb-f239f1f000c1"
	tests := []struct {
		name     string
		method   string
		readOnly bool
		allowed  bool
	}{
		{"read-only instance", apiv1alpha1.AgentInstanceService_GetAgentInstance_FullMethodName, true, true},
		{"read-only history", a2apb.A2AService_ListTasks_FullMethodName, true, true},
		{"read-only task", a2apb.A2AService_GetTask_FullMethodName, true, true},
		{"read-only subscription", a2apb.A2AService_SubscribeToTask_FullMethodName, true, true},
		{"read-only send", a2apb.A2AService_SendMessage_FullMethodName, true, false},
		{"read-only streaming send", a2apb.A2AService_SendStreamingMessage_FullMethodName, true, false},
		{"read-only cancel", a2apb.A2AService_CancelTask_FullMethodName, true, false},
		{"read-only resume", apiv1alpha1.AgentInstanceService_ResumeAgentInstance_FullMethodName, true, false},
		{"writable send", a2apb.A2AService_SendMessage_FullMethodName, false, true},
		{"writable resume", apiv1alpha1.AgentInstanceService_ResumeAgentInstance_FullMethodName, false, true},
		{"writable suspend", apiv1alpha1.AgentInstanceService_SuspendAgentInstance_FullMethodName, false, true},
		{"writable rename", apiv1alpha1.AgentInstanceService_UpdateAgentInstanceName_FullMethodName, false, false},
		{"writable delete", apiv1alpha1.AgentInstanceService_DeleteAgentInstance_FullMethodName, false, false},
		{"writable share creation", apiv1alpha1.AgentInstanceService_CreateAgentInstanceShare_FullMethodName, false, false},
		{"writable share listing", apiv1alpha1.AgentInstanceService_ListAgentInstanceShares_FullMethodName, false, false},
		{"writable push config", a2apb.A2AService_CreateTaskPushNotificationConfig_FullMethodName, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(
				apia2a.AgentInstanceIDHeader, instanceID,
			))
			resolver := scheduledAccessResolverFunc(func(_ context.Context, id string) (*auth.ShareContext, error) {
				require.Equal(t, instanceID, id)
				return &auth.ShareContext{AgentInstanceID: id, UserID: "scheduler-owner", ReadOnly: tt.readOnly}, nil
			})
			// All instance requests expose these same identity getters; permission comes
			// from the registered method, never from the request's concrete Go type.
			request := &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: instanceID}
			resolved, err := resolveScheduledRunRequest(ctx, request, tt.method, resolver)
			if !tt.allowed {
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				return
			}
			require.NoError(t, err)
			access := scheduledRunAccessFrom(resolved)
			require.NotNil(t, access)
			assert.Equal(t, "scheduler-owner", access.UserID)
			assert.Equal(t, tt.readOnly, access.ReadOnly)
			shared, ok := auth.ShareContextFrom(resolved)
			require.True(t, ok)
			assert.Same(t, access, shared)
		})
	}
}

func TestScheduledRunAccessLeavesOrdinaryConversationsUnchanged(t *testing.T) {
	ctx := t.Context()
	request := &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: "ordinary"}
	resolver := scheduledAccessResolverFunc(func(context.Context, string) (*auth.ShareContext, error) { return nil, nil })
	resolved, err := resolveScheduledRunRequest(ctx, request, apiv1alpha1.AgentInstanceService_DeleteAgentInstance_FullMethodName, resolver)
	require.NoError(t, err)
	assert.Equal(t, ctx, resolved)
	assert.Nil(t, scheduledRunAccessFrom(resolved))
}

func TestScheduledRunAccessRejectsOwnerLookupFailure(t *testing.T) {
	request := &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: "instance"}
	lookupErr := serviceerrors.NewPermissionDenied("not authorized to read schedule", errors.New("denied"))
	resolver := scheduledAccessResolverFunc(func(context.Context, string) (*auth.ShareContext, error) { return nil, lookupErr })
	called := false
	_, err := scheduledRunUnaryInterceptor(resolver)(t.Context(), request,
		&grpc.UnaryServerInfo{FullMethod: apiv1alpha1.AgentInstanceService_GetAgentInstance_FullMethodName},
		func(context.Context, any) (any, error) { called = true; return nil, nil },
	)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.False(t, called)
}

func TestScheduledRunAccessRejectsDifferentInstance(t *testing.T) {
	resolver := scheduledAccessResolverFunc(func(context.Context, string) (*auth.ShareContext, error) {
		return &auth.ShareContext{AgentInstanceID: "another-instance", UserID: "owner"}, nil
	})
	_, err := resolveScheduledRunRequest(t.Context(), &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: "instance"},
		apiv1alpha1.AgentInstanceService_GetAgentInstance_FullMethodName, resolver)
	assert.Equal(t, codes.Internal, status.Code(err))
}
