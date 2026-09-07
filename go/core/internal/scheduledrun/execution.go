package scheduledrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	apia2a "github.com/kagent-dev/kagent/go/api/a2a"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"google.golang.org/grpc/metadata"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

func (s *Scheduler) runExecution(ctx context.Context, execution *database.ScheduledRunExecution) error {
	key := types.NamespacedName{Namespace: execution.ScheduledRunNamespace, Name: execution.ScheduledRunName}
	var sr v1alpha3.ScheduledRun
	if err := s.kube.Get(ctx, key, &sr); err != nil {
		if apierrors.IsNotFound(err) {
			return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_DispatchFailed, "ScheduledRun was deleted")
		}
		return fmt.Errorf("failed to get ScheduledRun %s: %w", key, err)
	}
	if string(sr.UID) != execution.ScheduledRunUID || !sr.DeletionTimestamp.IsZero() {
		return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_DispatchFailed, "ScheduledRun was deleted or replaced")
	}
	if execution.Phase == database.ScheduledRunExecutionPhaseCreating {
		if !time.Now().Before(execution.Deadline) {
			// Create reserves its instance before provisioning. A lost response or
			// failed phase write must not leave that reservation unlinked in history.
			lookupCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			instance, err := s.store.GetAgentInstanceByRequestID(lookupCtx, SystemUserID, execution.ID)
			cancel()
			if err != nil && !errors.Is(err, database.ErrNotFound) {
				return fmt.Errorf("failed to recover reserved instance for execution %s: %w", execution.ID, err)
			}
			if instance != nil {
				execution.AgentInstanceID = instance.GetId()
				return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_TimedOut, "executionTimeout expired during instance creation; the reserved instance remains available for inspection")
			}
			return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_TimedOut, "executionTimeout expired before instance creation completed")
		}
		createCtx, cancel := context.WithDeadline(systemContext(ctx), execution.Deadline)
		instance, err := s.instances.Create(createCtx,
			&apiv1alpha1.ResourceReference{Namespace: sr.Namespace, Name: sr.Spec.HarnessRef.Name},
			&apiv1alpha1.ResourceReference{Namespace: sr.Namespace, Name: sr.Spec.TargetRef.Name},
			execution.ID, sr.Name)
		cancel()
		if err != nil {
			return fmt.Errorf("failed to create instance for execution %s: %w", execution.ID, err)
		}
		if instance.GetId() == "" {
			return fmt.Errorf("instance creation returned an empty identifier for execution %s", execution.ID)
		}
		execution.AgentInstanceID = instance.GetId()
		execution.Phase = database.ScheduledRunExecutionPhaseDispatching
		if err := s.persist(ctx, execution); err != nil {
			return err
		}
	}
	if execution.AgentInstanceID == "" {
		return fmt.Errorf("execution %s in phase %s has no AgentInstance", execution.ID, execution.Phase)
	}
	if execution.Phase == database.ScheduledRunExecutionPhaseDispatching {
		// A process may have died after the gateway accepted the message but before
		// TaskID was persisted here. The upstream task is authoritative, including
		// when its instance has already suspended after finishing the turn.
		task, err := s.findExecutionTask(ctx, execution)
		if err != nil {
			return err
		}
		if task == nil {
			if !time.Now().Before(execution.Deadline) {
				return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_TimedOut, "executionTimeout expired before dispatch completed")
			}
			message := a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart(execution.Prompt))
			// The gateway resolves the runtime context from the instance routing ID.
			message.ID = execution.ID
			dispatchCtx, cancel := context.WithDeadline(gatewayContext(ctx, execution), execution.Deadline)
			result, dispatchErr := s.gateway.SendMessage(dispatchCtx, &a2atype.SendMessageRequest{Message: message})
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if dispatchErr != nil {
				// A transport error does not establish whether the message was accepted.
				// Recover its task before deciding the execution's terminal outcome.
				task, err = s.findExecutionTask(ctx, execution)
				if err != nil {
					return err
				}
				if task == nil {
					status := v1alpha3.ScheduledRunExecutionStatus_DispatchFailed
					if errors.Is(dispatchErr, context.DeadlineExceeded) || !time.Now().Before(execution.Deadline) {
						status = v1alpha3.ScheduledRunExecutionStatus_TimedOut
					}
					return s.complete(ctx, execution, status, dispatchErr.Error())
				}
			} else {
				switch result := result.(type) {
				case *a2atype.Task:
					task = result
				case *a2atype.Message:
					if result != nil {
						execution.TaskID = string(message.TaskID)
						return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_Succeeded, "")
					}
				}
				if task == nil || task.ID == "" {
					return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_DispatchFailed, "agent dispatch did not return a valid task or message")
				}
			}
		}
		execution.TaskID = string(task.ID)
		if status, message, terminal := executionStatusForTask(task); terminal {
			return s.complete(ctx, execution, status, message)
		}
		execution.Phase = database.ScheduledRunExecutionPhasePolling
		if err := s.persist(ctx, execution); err != nil {
			return err
		}
	}
	if execution.Phase != database.ScheduledRunExecutionPhasePolling || execution.TaskID == "" {
		return fmt.Errorf("execution %s has invalid polling state", execution.ID)
	}
	if err := s.writeExecutionStatus(ctx, execution); err != nil && !errors.Is(err, errScheduledRunReplaced) && !apierrors.IsNotFound(err) {
		return err
	}
	return s.pollOutcome(ctx, execution)
}

// findExecutionTask matches the stable message ID rather than selecting the newest
// task: interactive conversations may already contain later user-created tasks.
func (s *Scheduler) findExecutionTask(ctx context.Context, execution *database.ScheduledRunExecution) (*a2atype.Task, error) {
	lookupCtx, cancel := context.WithTimeout(gatewayContext(ctx, execution), writeTimeout)
	defer cancel()
	// Routing metadata already scopes the query to this instance; its runtime
	// context is a separate identifier and must not be inferred from the ID.
	request := &a2atype.ListTasksRequest{PageSize: 100}
	for {
		response, err := s.gateway.ListTasks(lookupCtx, request)
		if err != nil {
			return nil, fmt.Errorf("failed to recover task for execution %s: %w", execution.ID, err)
		}
		if response == nil {
			return nil, fmt.Errorf("task listing returned no response for execution %s", execution.ID)
		}
		for _, task := range response.Tasks {
			if task == nil {
				continue
			}
			for _, message := range task.History {
				if message != nil && message.ID == execution.ID {
					return task, nil
				}
			}
		}
		if response.NextPageToken == "" {
			return nil, nil
		}
		if response.NextPageToken == request.PageToken {
			return nil, fmt.Errorf("task listing returned a repeated page token for execution %s", execution.ID)
		}
		request.PageToken = response.NextPageToken
	}
}

func (s *Scheduler) pollOutcome(ctx context.Context, execution *database.ScheduledRunExecution) error {
	attached := false
	for {
		// Recovery gets one real lookup even after the original deadline. Passing
		// an already-expired context would misclassify an offline completion.
		lookupCtx, cancel := context.WithTimeout(gatewayContext(ctx, execution), writeTimeout)
		task, err := s.gateway.GetTask(lookupCtx, &a2atype.GetTaskRequest{ID: a2atype.TaskID(execution.TaskID)})
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			if status, message, terminal := executionStatusForTask(task); terminal {
				return s.complete(ctx, execution, status, message)
			}
		}
		remaining := time.Until(execution.Deadline)
		if remaining <= 0 {
			message := "task did not reach a terminal state before executionTimeout"
			if err != nil {
				message += "; last task lookup failed: " + err.Error()
			}
			return s.complete(ctx, execution, v1alpha3.ScheduledRunExecutionStatus_TimedOut, message)
		}
		if !attached {
			if err := s.attachTaskRun(ctx, execution); err != nil {
				return err
			}
			attached = true
		}
		timer := time.NewTimer(min(recoveryInterval, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// The gateway owns the upstream stream and its durable task events. Closing this
// observer only releases its reader; taskRun.ingest keeps running independently.
func (s *Scheduler) attachTaskRun(ctx context.Context, execution *database.ScheduledRunExecution) error {
	attachCtx, cancel := context.WithTimeout(gatewayContext(ctx, execution), writeTimeout)
	defer cancel()
	for _, err := range s.gateway.SubscribeToTask(attachCtx, &a2atype.SubscribeToTaskRequest{ID: a2atype.TaskID(execution.TaskID)}) {
		if err != nil {
			return fmt.Errorf("failed to attach to scheduled task %s: %w", execution.TaskID, err)
		}
		return nil
	}
	return fmt.Errorf("scheduled task %s subscription returned no state", execution.TaskID)
}

func executionStatusForTask(task *a2atype.Task) (v1alpha3.ScheduledRunExecutionStatus, string, bool) {
	if task == nil {
		return v1alpha3.ScheduledRunExecutionStatus_InProgress, "", false
	}
	switch task.Status.State {
	case a2atype.TaskStateCompleted:
		return v1alpha3.ScheduledRunExecutionStatus_Succeeded, "", true
	case a2atype.TaskStateFailed, a2atype.TaskStateCanceled, a2atype.TaskStateRejected:
		var message string
		if task.Status.Message != nil {
			for _, part := range task.Status.Message.Parts {
				if text := part.Text(); text != "" {
					message = text
					break
				}
			}
		}
		return v1alpha3.ScheduledRunExecutionStatus_Failed, message, true
	default:
		return v1alpha3.ScheduledRunExecutionStatus_InProgress, "", false
	}
}

type systemSession struct{}

var _ auth.Session = systemSession{}

func (systemSession) Principal() auth.Principal {
	return auth.Principal{User: auth.User{ID: SystemUserID}}
}

func systemContext(ctx context.Context) context.Context {
	return auth.AuthSessionTo(ctx, systemSession{})
}

func gatewayContext(ctx context.Context, execution *database.ScheduledRunExecution) context.Context {
	ctx = systemContext(ctx)
	ctx = auth.ShareContextTo(ctx, &auth.ShareContext{AgentInstanceID: execution.AgentInstanceID, UserID: SystemUserID})
	return metadata.NewIncomingContext(ctx, metadata.Pairs(
		apia2a.AgentInstanceIDHeader, execution.AgentInstanceID,
	))
}
