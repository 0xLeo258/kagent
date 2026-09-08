package e2e_test

import (
	"context"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	a2apb "github.com/a2aproject/a2a-go/v2/a2apb/v1"
	"github.com/a2aproject/a2a-go/v2/a2apb/v1/pbconv"
	"github.com/google/uuid"
	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TestScheduledRunInteraction exercises the public schedule API, leader dispatch,
// real AgentInstance runtime and the normal conversation API with a mock LLM.
func TestScheduledRunInteraction(t *testing.T) {
	target := interactionTarget(t)
	template := createInteractionTemplate(t, startInteractionMock(t))
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	ctx, cancel := context.WithTimeout(metadata.AppendToOutgoingContext(t.Context(), "x-user-id", "e2e"), 6*time.Minute)
	defer cancel()
	schedules := apiv1alpha1.NewScheduledRunServiceClient(conn)
	instances := apiv1alpha1.NewAgentInstanceServiceClient(conn)
	a2a := a2apb.NewA2AServiceClient(conn)
	ref := &apiv1alpha1.ResourceReference{Namespace: "kagent", Name: "scheduled-e2e-" + uuid.NewString()[:8]}
	sr := &v1alpha3.ScheduledRun{
		ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace},
		Spec: v1alpha3.ScheduledRunSpec{
			Schedule: "0 0 1 1 *", Suspended: new(true), Prompt: "What is 2+2?",
			TargetRef:  corev1.TypedLocalObjectReference{APIGroup: new("kagent.dev"), Kind: "AgentTemplate", Name: template},
			HarnessRef: corev1.LocalObjectReference{Name: "kagent"}, RecentExecutionsLimit: new(int32(1)),
		},
	}
	encode := func(resource *v1alpha3.ScheduledRun) *apiv1alpha1.StructuredObject {
		wire, encodeErr := structuredobject.FromGo(resource, v1alpha3.GroupVersion.String(), "ScheduledRun", 4<<20)
		require.NoError(t, encodeErr)
		return wire
	}
	created, err := schedules.CreateScheduledRun(ctx, &apiv1alpha1.CreateScheduledRunRequest{Ref: ref, Resource: encode(sr)})
	require.NoError(t, err)
	require.Equal(t, "e2e", created.GetScheduledRun().GetBoundUserId())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(metadata.AppendToOutgoingContext(context.Background(), "x-user-id", "e2e"), time.Minute)
		defer cleanupCancel()
		_, deleteErr := schedules.DeleteScheduledRun(cleanupCtx, &apiv1alpha1.DeleteScheduledRunRequest{Ref: ref})
		if status.Code(deleteErr) != codes.NotFound {
			require.NoError(t, deleteErr)
		}
	})
	var completed []*apiv1alpha1.ScheduledRunExecution
	for range 2 {
		triggered, triggerErr := schedules.TriggerScheduledRun(ctx, &apiv1alpha1.TriggerScheduledRunRequest{Ref: ref})
		require.NoError(t, triggerErr, "manual execution must work while suspended")
		id := triggered.GetExecution().GetId()
		require.NotEmpty(t, id)
		err = wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			history, listErr := schedules.ListScheduledRunExecutions(ctx, &apiv1alpha1.ListScheduledRunExecutionsRequest{Ref: ref})
			if listErr != nil {
				return false, listErr
			}
			for _, execution := range history.GetExecutions() {
				if execution.GetId() == id && execution.GetStatus() != "InProgress" {
					require.Equal(t, "Succeeded", execution.GetStatus(), execution.GetStatusMessage())
					completed = append(completed, execution)
					return true, nil
				}
			}
			return false, nil
		})
		require.NoError(t, err)
	}
	require.NotEqual(t, completed[0].GetAgentInstanceId(), completed[1].GetAgentInstanceId())
	page, err := schedules.ListScheduledRunExecutions(ctx, &apiv1alpha1.ListScheduledRunExecutionsRequest{Ref: ref, Page: &apiv1alpha1.PageRequest{Limit: 1}})
	require.NoError(t, err)
	require.Len(t, page.GetExecutions(), 1)
	require.NotEmpty(t, page.GetPage().GetNextPageToken())
	older, err := schedules.ListScheduledRunExecutions(ctx, &apiv1alpha1.ListScheduledRunExecutionsRequest{Ref: ref, Page: &apiv1alpha1.PageRequest{Limit: 1, PageToken: page.GetPage().GetNextPageToken()}})
	require.NoError(t, err)
	require.Len(t, older.GetExecutions(), 1, "full history survives summary pruning")
	require.NotEqual(t, page.GetExecutions()[0].GetId(), older.GetExecutions()[0].GetId())

	instanceID := completed[0].GetAgentInstanceId()
	get := &apiv1alpha1.GetAgentInstanceRequest{AgentInstanceId: instanceID}
	instance, err := instances.GetAgentInstance(ctx, get)
	require.NoError(t, err)
	require.True(t, instance.GetScheduledRun())
	require.False(t, instance.GetReadOnly())
	require.Equal(t, "e2e", instance.GetAgentInstance().GetCreator())
	chatCtx := metadata.AppendToOutgoingContext(ctx, apia2a.AgentInstanceIDHeader, instanceID)
	taskRequest, err := pbconv.ToProtoGetTaskRequest(&a2atype.GetTaskRequest{ID: a2atype.TaskID(completed[0].GetTaskId())})
	require.NoError(t, err)
	_, err = a2a.GetTask(chatCtx, taskRequest)
	require.NoError(t, err, "schedule reader can retrieve the scheduler-owned task")
	readerCtx := metadata.AppendToOutgoingContext(t.Context(), "x-user-id", "other-reader", apia2a.AgentInstanceIDHeader, instanceID)
	readerInstance, err := instances.GetAgentInstance(readerCtx, get)
	require.NoError(t, err)
	require.True(t, readerInstance.GetReadOnly())
	_, err = a2a.GetTask(readerCtx, taskRequest)
	require.NoError(t, err)
	_, message := newMessageRequest(t, "What is 2+2?")
	_, err = a2a.SendMessage(readerCtx, message)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, _, task := (&interactionFixture{ctx: chatCtx, client: a2a}).send(t, "What is 2+2?")
	require.Equal(t, a2atype.TaskStateCompleted, task.Status.State)
	_, err = schedules.DeleteScheduledRun(ctx, &apiv1alpha1.DeleteScheduledRunRequest{Ref: ref})
	require.NoError(t, err)
	_, err = schedules.CreateScheduledRun(ctx, &apiv1alpha1.CreateScheduledRunRequest{Ref: ref, Resource: encode(sr)})
	require.NoError(t, err)
	_, err = instances.GetAgentInstance(ctx, get)
	require.Equal(t, codes.NotFound, status.Code(err), "replacement schedule must not inherit old conversation access")
}
