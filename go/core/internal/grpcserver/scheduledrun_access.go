package grpcserver

import (
	"context"
	"strings"

	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type scheduledRunAccessKey struct{}

func scheduledRunAccessFrom(ctx context.Context) *auth.ShareContext {
	access, _ := ctx.Value(scheduledRunAccessKey{}).(*auth.ShareContext)
	return access
}

func scheduledRunUnaryInterceptor(resolver ScheduledRunAccessResolver) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		ctx, err := resolveScheduledRunRequest(ctx, request, info.FullMethod, resolver)
		if err != nil {
			return nil, mapError(err)
		}
		return next(ctx, request)
	}
}

func scheduledRunStreamInterceptor(resolver ScheduledRunAccessResolver) grpc.StreamServerInterceptor {
	return func(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		ctx, err := resolveScheduledRunRequest(stream.Context(), nil, info.FullMethod, resolver)
		if err != nil {
			return mapError(err)
		}
		return next(server, &contextServerStream{ServerStream: stream, ctx: ctx})
	}
}

func resolveScheduledRunRequest(ctx context.Context, request any, method string, resolver ScheduledRunAccessResolver) (context.Context, error) {
	if resolver == nil {
		return ctx, nil
	}
	var instanceID string
	switch {
	case strings.HasPrefix(method, "/"+a2apb.A2AService_ServiceDesc.ServiceName+"/"):
		ids := metadata.ValueFromIncomingContext(ctx, apia2a.AgentInstanceIDHeader)
		if len(ids) != 1 {
			// The gateway validates malformed routing metadata before accessing an instance.
			return ctx, nil
		}
		instanceID = ids[0]
	case strings.HasPrefix(method, "/"+apiv1alpha1.AgentInstanceService_ServiceDesc.ServiceName+"/"):
		identity, ok := request.(interface {
			GetAgentInstanceId() string
		})
		if !ok {
			return ctx, nil
		}
		instanceID = identity.GetAgentInstanceId()
	default:
		return ctx, nil
	}
	if instanceID == "" {
		return ctx, nil
	}
	access, err := resolver.ResolveInstanceAccess(ctx, instanceID)
	if err != nil || access == nil {
		return ctx, err
	}
	if !access.IsForAgentInstance(instanceID) {
		return ctx, status.Error(codes.Internal, "scheduled execution access does not match the requested conversation")
	}
	if !scheduledRunMethodAllowed(method, access.ReadOnly) {
		return ctx, status.Error(codes.PermissionDenied, "this operation is not permitted for the scheduled conversation")
	}
	ctx = auth.ShareContextTo(ctx, access)
	return context.WithValue(ctx, scheduledRunAccessKey{}, access), nil
}

func scheduledRunMethodAllowed(method string, readOnly bool) bool {
	switch method {
	case apiv1alpha1.AgentInstanceService_GetAgentInstance_FullMethodName,
		a2apb.A2AService_GetTask_FullMethodName,
		a2apb.A2AService_ListTasks_FullMethodName,
		a2apb.A2AService_SubscribeToTask_FullMethodName,
		a2apb.A2AService_GetExtendedAgentCard_FullMethodName:
		return true
	case apiv1alpha1.AgentInstanceService_ResumeAgentInstance_FullMethodName,
		apiv1alpha1.AgentInstanceService_SuspendAgentInstance_FullMethodName,
		a2apb.A2AService_SendMessage_FullMethodName,
		a2apb.A2AService_SendStreamingMessage_FullMethodName,
		a2apb.A2AService_CancelTask_FullMethodName:
		return !readOnly
	default:
		// Schedule readers cannot delete history, rename it, or mint independent shares.
		return false
	}
}
