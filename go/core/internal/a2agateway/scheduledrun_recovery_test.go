package a2agateway

import (
	"context"
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
// releases its stream after the initial event. The gateway must still persist a
// terminal failure when the runtime reports that the task does not exist.
func TestScheduledRunRecoveryFailsUndispatchedTaskAfterObserverLeaves(t *testing.T) {
	task := &a2atype.Task{
		ID: "reserved-task", ContextID: gatewayTestContextID,
		Status:  a2atype.TaskStatus{State: a2atype.TaskStateSubmitted},
		History: []*a2atype.Message{{ID: "scheduled-execution", Role: a2atype.MessageRoleUser}},
	}
	store := &gatewayTestStore{instance: gatewayTestInstance(), task: task, active: task}
	runtime := &missingScheduledTaskRuntime{
		gatewayTestRuntime: gatewayTestRuntime{subscribeErr: a2atype.ErrTaskNotFound},
		closed:             make(chan struct{}),
	}
	gateway := New(store, &gatewayTestAuthorizer{}, &gatewayTestDialer{client: gatewayTestClient(t, runtime)}, &gatewayTestWorkflow{}, gatewayTestURL)
	ctx, cancel := context.WithCancel(gatewayTestContext())
	defer cancel()
	observed := false
	for event, err := range gateway.SubscribeToTask(ctx, &a2atype.SubscribeToTaskRequest{ID: task.ID}) {
		require.NoError(t, err)
		require.Equal(t, task, event)
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

	// The ingester closes the runtime only after storing the failure, so these
	// reads are synchronized with its background persistence.
	stored, err := gateway.GetTask(gatewayTestContext(), &a2atype.GetTaskRequest{ID: task.ID})
	require.NoError(t, err)
	require.Equal(t, a2atype.TaskStateFailed, stored.Status.State)
	require.Equal(t, "scheduled-execution", stored.History[0].ID)
	require.Nil(t, store.active)
	require.Equal(t, 1, runtime.subscribeCalls)
	require.Zero(t, runtime.sendCalls, "ambiguous recovery must not dispatch the prompt twice")
}
