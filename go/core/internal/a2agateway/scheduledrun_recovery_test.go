package a2agateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"
)

type missingScheduledTaskRuntime struct {
	gatewayTestRuntime
	closed chan struct{}
	once   sync.Once
}

func (r *missingScheduledTaskRuntime) Destroy() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

// A controller can stop after reserving the public task, before the private
// runtime receives it. ScheduledRun recovery attaches to that durable task and
// releases its stream after the recovered event. The gateway must persist a
// terminal failure when the runtime confirms that the task does not exist.
func TestScheduledRunRecoveryFailsTaskMissingFromRuntime(t *testing.T) {
	task := &a2atype.Task{
		ID: "reserved-task", ContextID: gatewayTestContextID,
		Status:  a2atype.TaskStatus{State: a2atype.TaskStateSubmitted},
		History: []*a2atype.Message{{ID: "scheduled-execution", Role: a2atype.MessageRoleUser}},
	}
	store := &gatewayTestStore{instance: gatewayTestInstance(), task: task, active: task}
	runtime := &missingScheduledTaskRuntime{
		gatewayTestRuntime: gatewayTestRuntime{subscribeErr: a2atype.ErrTaskNotFound, taskErr: a2atype.ErrTaskNotFound},
		closed:             make(chan struct{}),
	}
	gateway := New(store, &gatewayTestAuthorizer{}, &gatewayTestDialer{client: gatewayTestClient(t, runtime)}, &gatewayTestWorkflow{}, gatewayTestURL)
	ctx, cancel := context.WithCancel(gatewayTestContext())
	defer cancel()
	observed := false
	for event, err := range gateway.SubscribeToTask(ctx, &a2atype.SubscribeToTaskRequest{ID: task.ID}) {
		require.NoError(t, err)
		recovered, ok := event.(*a2atype.Task)
		require.True(t, ok)
		require.Equal(t, a2atype.TaskStateFailed, recovered.Status.State)
		observed = true
		break
	}
	require.True(t, observed)
	cancel()
	select {
	case <-runtime.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway task ingester did not finish after runtime TaskNotFound")
	}

	// Recovery publishes the outcome only after storing it.
	stored, err := gateway.GetTask(gatewayTestContext(), &a2atype.GetTaskRequest{ID: task.ID})
	require.NoError(t, err)
	require.Equal(t, a2atype.TaskStateFailed, stored.Status.State)
	require.Equal(t, "scheduled-execution", stored.History[0].ID)
	require.Nil(t, store.active)
	require.Equal(t, 1, runtime.subscribeCalls)
	require.Equal(t, 1, runtime.getTaskCalls)
	require.Zero(t, runtime.sendCalls, "ambiguous recovery must not dispatch the prompt twice")
}

func TestScheduledRunRecoveryReadsRuntimeOutcomeWithoutActiveExecution(t *testing.T) {
	for _, state := range []a2atype.TaskState{
		a2atype.TaskStateCompleted, a2atype.TaskStateFailed,
		a2atype.TaskStateInputRequired, a2atype.TaskStateAuthRequired,
	} {
		t.Run(string(state), func(t *testing.T) {
			task := &a2atype.Task{
				ID: "offline-task", ContextID: gatewayTestContextID,
				Status:  a2atype.TaskStatus{State: a2atype.TaskStateWorking},
				History: []*a2atype.Message{{ID: "scheduled-execution", Role: a2atype.MessageRoleUser}},
			}
			outcome := &a2atype.Task{ID: task.ID, ContextID: task.ContextID, Status: a2atype.TaskStatus{State: state}}
			store := &gatewayTestStore{instance: gatewayTestInstance(), task: task, active: task}
			runtime := &gatewayTestRuntime{subscribeErr: a2atype.ErrTaskNotFound, task: outcome}
			workflow := &gatewayTestWorkflow{}
			gateway := New(store, &gatewayTestAuthorizer{}, &gatewayTestDialer{client: gatewayTestClient(t, runtime)}, workflow, gatewayTestURL)
			observed := false
			for _, err := range gateway.SubscribeToTask(gatewayTestContext(), &a2atype.SubscribeToTaskRequest{ID: task.ID}) {
				require.NoError(t, err)
				// This matches the scheduler: stop observing after recovery and read
				// the public task to decide the outcome before checking the deadline.
				stored, getErr := gateway.GetTask(gatewayTestContext(), &a2atype.GetTaskRequest{ID: task.ID})
				require.NoError(t, getErr)
				require.Equal(t, state, stored.Status.State)
				require.Equal(t, "scheduled-execution", stored.History[0].ID)
				observed = true
				break
			}
			require.True(t, observed)
			require.Equal(t, 1, runtime.getTaskCalls)
			require.Zero(t, runtime.sendCalls)
			require.Equal(t, 1, workflow.quiesceCalls)
		})
	}
}

func TestScheduledRunRecoveryPreservesTaskOnRuntimeOutage(t *testing.T) {
	for _, lookup := range []bool{false, true} {
		name := "subscription"
		if lookup {
			name = "task lookup"
		}
		t.Run(name, func(t *testing.T) {
			task := &a2atype.Task{ID: "offline-task", ContextID: gatewayTestContextID, Status: a2atype.TaskStatus{State: a2atype.TaskStateWorking}}
			store := &gatewayTestStore{instance: gatewayTestInstance(), task: task, active: task}
			outage := errors.New("runtime unavailable")
			runtime := &gatewayTestRuntime{subscribeErr: outage}
			if lookup {
				runtime.subscribeErr, runtime.taskErr = a2atype.ErrTaskNotFound, outage
			}
			gateway := New(store, &gatewayTestAuthorizer{}, &gatewayTestDialer{client: gatewayTestClient(t, runtime)}, &gatewayTestWorkflow{}, gatewayTestURL)
			var recoveryErr error
			for _, err := range gateway.SubscribeToTask(gatewayTestContext(), &a2atype.SubscribeToTaskRequest{ID: task.ID}) {
				recoveryErr = err
			}
			require.ErrorIs(t, recoveryErr, outage)
			require.Equal(t, a2atype.TaskStateWorking, store.task.Status.State)
			require.Same(t, task, store.active)
			require.Empty(t, store.stored)
		})
	}
}
