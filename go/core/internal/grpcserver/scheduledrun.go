package grpcserver

import (
	"context"

	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	scheduledrunservice "github.com/kagent-dev/kagent/go/core/internal/service/scheduledrun"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"
)

type scheduledRunServer struct {
	apiv1alpha1.UnimplementedScheduledRunServiceServer
	service         *scheduledrunservice.Service
	maxMessageBytes int
}

var _ apiv1alpha1.ScheduledRunServiceServer = (*scheduledRunServer)(nil)

func (s *scheduledRunServer) ListScheduledRuns(ctx context.Context, request *apiv1alpha1.ListScheduledRunsRequest) (*apiv1alpha1.ListScheduledRunsResponse, error) {
	items, err := s.service.List(ctx, request.GetNamespace())
	if err != nil {
		return nil, err
	}
	response := &apiv1alpha1.ListScheduledRunsResponse{ScheduledRuns: make([]*apiv1alpha1.ScheduledRun, 0, len(items))}
	for _, item := range items {
		resource, err := s.encodeScheduledRun(ctx, item)
		if err != nil {
			return nil, err
		}
		response.ScheduledRuns = append(response.ScheduledRuns, resource)
	}
	return response, nil
}

func (s *scheduledRunServer) GetScheduledRun(ctx context.Context, request *apiv1alpha1.GetScheduledRunRequest) (*apiv1alpha1.GetScheduledRunResponse, error) {
	item, err := s.service.Get(ctx, scheduledRunRef(request.GetRef()))
	if err != nil {
		return nil, err
	}
	resource, err := s.encodeScheduledRun(ctx, item)
	if err != nil {
		return nil, err
	}
	return &apiv1alpha1.GetScheduledRunResponse{ScheduledRun: resource}, nil
}

func (s *scheduledRunServer) CreateScheduledRun(ctx context.Context, request *apiv1alpha1.CreateScheduledRunRequest) (*apiv1alpha1.CreateScheduledRunResponse, error) {
	incoming, err := s.decodeScheduledRun(request.GetRef(), request.GetResource())
	if err != nil {
		return nil, err
	}
	item, err := s.service.Create(ctx, incoming)
	if err != nil {
		return nil, err
	}
	resource, err := s.encodeScheduledRun(ctx, item)
	if err != nil {
		return nil, err
	}
	return &apiv1alpha1.CreateScheduledRunResponse{ScheduledRun: resource}, nil
}

func (s *scheduledRunServer) UpdateScheduledRun(ctx context.Context, request *apiv1alpha1.UpdateScheduledRunRequest) (*apiv1alpha1.UpdateScheduledRunResponse, error) {
	incoming, err := s.decodeScheduledRun(request.GetRef(), request.GetResource())
	if err != nil {
		return nil, err
	}
	item, err := s.service.Update(ctx, incoming)
	if err != nil {
		return nil, err
	}
	resource, err := s.encodeScheduledRun(ctx, item)
	if err != nil {
		return nil, err
	}
	return &apiv1alpha1.UpdateScheduledRunResponse{ScheduledRun: resource}, nil
}

func (s *scheduledRunServer) DeleteScheduledRun(ctx context.Context, request *apiv1alpha1.DeleteScheduledRunRequest) (*apiv1alpha1.DeleteScheduledRunResponse, error) {
	if err := s.service.Delete(ctx, scheduledRunRef(request.GetRef())); err != nil {
		return nil, err
	}
	return &apiv1alpha1.DeleteScheduledRunResponse{}, nil
}

func (s *scheduledRunServer) ListScheduledRunExecutions(ctx context.Context, request *apiv1alpha1.ListScheduledRunExecutionsRequest) (*apiv1alpha1.ListScheduledRunExecutionsResponse, error) {
	result, err := s.service.ListExecutions(ctx, scheduledrunservice.ListExecutionsRequest{
		Ref: scheduledRunRef(request.GetRef()), PageSize: int(request.GetPage().GetLimit()), PageToken: request.GetPage().GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	executions := make([]*apiv1alpha1.ScheduledRunExecution, 0, len(result.Executions))
	for i := range result.Executions {
		executions = append(executions, scheduledRunExecutionProto(&result.Executions[i]))
	}
	return &apiv1alpha1.ListScheduledRunExecutionsResponse{Executions: executions, Page: &apiv1alpha1.PageResponse{NextPageToken: result.NextPageToken}}, nil
}

func (s *scheduledRunServer) TriggerScheduledRun(ctx context.Context, request *apiv1alpha1.TriggerScheduledRunRequest) (*apiv1alpha1.TriggerScheduledRunResponse, error) {
	execution, err := s.service.Trigger(ctx, scheduledRunRef(request.GetRef()))
	if err != nil {
		return nil, err
	}
	return &apiv1alpha1.TriggerScheduledRunResponse{Execution: scheduledRunExecutionProto(execution)}, nil
}

func scheduledRunRef(ref *apiv1alpha1.ResourceReference) types.NamespacedName {
	return types.NamespacedName{Namespace: ref.GetNamespace(), Name: ref.GetName()}
}

func (s *scheduledRunServer) decodeScheduledRun(ref *apiv1alpha1.ResourceReference, resource *apiv1alpha1.StructuredObject) (*v1alpha3.ScheduledRun, error) {
	var incoming v1alpha3.ScheduledRun
	if err := structuredobject.ToGo(resource, "ScheduledRun", &incoming, s.maxMessageBytes); err != nil {
		return nil, serviceerrors.NewInvalidArgument("Invalid ScheduledRun resource", err)
	}
	if (incoming.Name != "" && incoming.Name != ref.GetName()) || (incoming.Namespace != "" && incoming.Namespace != ref.GetNamespace()) {
		return nil, serviceerrors.NewInvalidArgument("ScheduledRun reference does not match resource metadata", nil)
	}
	incoming.Name, incoming.Namespace = ref.GetName(), ref.GetNamespace()
	return &incoming, nil
}

func (s *scheduledRunServer) encodeScheduledRun(ctx context.Context, item *v1alpha3.ScheduledRun) (*apiv1alpha1.ScheduledRun, error) {
	resource, err := structuredobject.FromGo(item, v1alpha3.GroupVersion.String(), "ScheduledRun", s.maxMessageBytes)
	if err != nil {
		return nil, serviceerrors.NewInternal("Failed to encode ScheduledRun resource", err)
	}
	boundUserID, err := s.service.BoundUserID(ctx, item)
	if err != nil {
		return nil, err
	}
	return &apiv1alpha1.ScheduledRun{BoundUserId: boundUserID, Ref: &apiv1alpha1.ResourceReference{Namespace: item.Namespace, Name: item.Name}, Resource: resource}, nil
}

func scheduledRunExecutionProto(item *dbpkg.ScheduledRunExecution) *apiv1alpha1.ScheduledRunExecution {
	result := &apiv1alpha1.ScheduledRunExecution{
		Id: item.ID, StartTime: timestamppb.New(item.StartTime), Trigger: string(item.Trigger),
		AgentInstanceId: item.AgentInstanceID, TaskId: item.TaskID, Status: string(item.Status), StatusMessage: item.StatusMessage,
	}
	if item.CompletionTime != nil {
		result.CompletionTime = timestamppb.New(*item.CompletionTime)
	}
	return result
}
