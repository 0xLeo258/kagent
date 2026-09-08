package a2agateway

import (
	"context"
	"errors"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/stretchr/testify/require"
)

func TestGatewayCancelPersistsWithoutTaskIngester(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		name := "no subscription"
		if recovered {
			name = "failed recovery subscription"
		}
		t.Run(name, func(t *testing.T) {
			task := &a2atype.Task{
				ID: "cancel-task", ContextID: gatewayTestContextID,
				Status:  a2atype.TaskStatus{State: a2atype.TaskStateWorking},
				History: []*a2atype.Message{{ID: "original-prompt", Role: a2atype.MessageRoleUser}},
			}
			store := &gatewayTestStore{instance: gatewayTestInstance(), task: task, active: task}
			canceled := &a2atype.Task{ID: task.ID, ContextID: task.ContextID, Status: a2atype.TaskStatus{State: a2atype.TaskStateCanceled}}
			runtime := &gatewayTestRuntime{task: canceled}
			dialer := &gatewayTestDialer{client: gatewayTestClient(t, runtime)}
			workflow := &gatewayTestWorkflow{}
			gateway := newGateway(store, &gatewayTestAuthorizer{}, dialer, workflow, gatewayTestURL, &memoryRuntimeCoordinator{})
			ctx, cancel := context.WithTimeout(gatewayTestContext(), 2*time.Second)
			defer cancel()
			if recovered {
				outage := errors.New("subscription transport unavailable")
				dialer.client = gatewayTestClient(t, &gatewayTestRuntime{subscribeErr: outage})
				var recoveryErr error
				for _, err := range gateway.SubscribeToTask(ctx, &a2atype.SubscribeToTaskRequest{ID: task.ID}) {
					recoveryErr = err
				}
				require.ErrorIs(t, recoveryErr, outage)
				require.Empty(t, store.stored)
				dialer.client = gatewayTestClient(t, runtime)
			}
			response, err := gateway.CancelTask(ctx, &a2atype.CancelTaskRequest{ID: task.ID})
			require.NoError(t, err)
			require.Equal(t, a2atype.TaskStateCanceled, response.Status.State)
			persisted, err := gateway.GetTask(ctx, &a2atype.GetTaskRequest{ID: task.ID})
			require.NoError(t, err)
			require.Equal(t, a2atype.TaskStateCanceled, persisted.Status.State)
			require.Equal(t, "original-prompt", persisted.History[0].ID)
			require.Nil(t, store.active)
			require.Len(t, store.stored, 1)
			require.Equal(t, 1, workflow.quiesceCalls)
			require.NotNil(t, store.snapshot)
			require.True(t, runtime.destroyed)
		})
	}
}

func TestGatewayCancelKeepsPersistedTerminalOutcome(t *testing.T) {
	task := &a2atype.Task{ID: "finished-task", ContextID: gatewayTestContextID, Status: a2atype.TaskStatus{State: a2atype.TaskStateCanceled}}
	store := &gatewayTestStore{instance: gatewayTestInstance(), task: task}
	runtime := &gatewayTestRuntime{task: task}
	workflow := &gatewayTestWorkflow{}
	gateway := newGateway(store, &gatewayTestAuthorizer{}, &gatewayTestDialer{client: gatewayTestClient(t, runtime)}, workflow, gatewayTestURL, &memoryRuntimeCoordinator{})
	response, err := gateway.CancelTask(gatewayTestContext(), &a2atype.CancelTaskRequest{ID: task.ID})
	require.NoError(t, err)
	require.Equal(t, a2atype.TaskStateCanceled, response.Status.State)
	require.Empty(t, store.stored)
	require.Zero(t, workflow.quiesceCalls)
}
