package a2agateway

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

const recoveryLookupTimeout = 10 * time.Second

// SubscribeToTask addresses a live execution, whereas GetTask can also return a
// task that finished while the gateway was offline. A missing subscription alone
// therefore cannot establish that the task failed.
func subscribeToRuntimeTask(ctx context.Context, client *a2aclient.Client, task *a2atype.Task, request *a2atype.SubscribeToTaskRequest) iter.Seq2[a2atype.Event, error] {
	return func(yield func(a2atype.Event, error) bool) {
		for event, err := range client.SubscribeToTask(ctx, request) {
			if errors.Is(err, a2atype.ErrTaskNotFound) {
				lookupCtx, cancel := context.WithTimeout(ctx, recoveryLookupTimeout)
				latest, lookupErr := client.GetTask(lookupCtx, &a2atype.GetTaskRequest{ID: task.ID})
				cancel()
				if lookupErr != nil && !errors.Is(lookupErr, a2atype.ErrTaskNotFound) {
					yield(nil, fmt.Errorf("failed to recover runtime task %s: %w", task.ID, lookupErr))
					return
				}
				if lookupErr == nil {
					if latest == nil {
						yield(nil, fmt.Errorf("runtime returned no task %s", task.ID))
						return
					}
					if err := validateTaskInfo(latest, task); err != nil {
						yield(nil, fmt.Errorf("failed to recover runtime task %s: %w", task.ID, err))
						return
					}
					if isQuiescent(latest.Status.State) {
						yield(latest, nil)
						return
					}
				}
				// The runtime has neither an execution nor a completed or parked task.
				// Preserve the public history, but do not resend an ambiguous request.
				failed := *task
				now := time.Now()
				failed.Status = a2atype.TaskStatus{State: a2atype.TaskStateFailed, Timestamp: &now}
				yield(&failed, nil)
				return
			}
			if !yield(event, err) {
				return
			}
		}
	}
}
