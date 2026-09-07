package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbgen "github.com/kagent-dev/kagent/go/core/internal/database/internal/dbgen"
)

func (c *Client) CreateScheduledRunExecution(ctx context.Context, execution *ScheduledRunExecution) (*ScheduledRunExecution, bool, error) {
	row, err := c.q.CreateScheduledRunExecution(ctx, dbgen.CreateScheduledRunExecutionParams{
		ID: execution.ID, ScheduledRunNamespace: execution.ScheduledRunNamespace,
		ScheduledRunName: execution.ScheduledRunName, ScheduledRunUid: execution.ScheduledRunUID,
		StartTime: execution.StartTime, Deadline: execution.Deadline, Trigger: string(execution.Trigger), Prompt: execution.Prompt,
	})
	if err == nil {
		return toScheduledRunExecution(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("failed to create ScheduledRun execution %s: %w", execution.ID, err)
	}
	existing, err := c.GetScheduledRunExecution(ctx, execution.ID)
	if err != nil {
		return nil, false, err
	}
	if !sameExecutionRequest(existing, execution) {
		return nil, false, ErrIdempotencyConflict
	}
	return existing, false, nil
}

func sameExecutionRequest(left, right *ScheduledRunExecution) bool {
	return left.ScheduledRunNamespace == right.ScheduledRunNamespace && left.ScheduledRunName == right.ScheduledRunName &&
		left.ScheduledRunUID == right.ScheduledRunUID && left.Prompt == right.Prompt && left.Trigger == right.Trigger &&
		left.StartTime.Truncate(time.Microsecond).Equal(right.StartTime.Truncate(time.Microsecond)) &&
		left.Deadline.Truncate(time.Microsecond).Equal(right.Deadline.Truncate(time.Microsecond))
}

func (c *Client) UpdateScheduledRunExecution(ctx context.Context, execution *ScheduledRunExecution) error {
	var instanceID *uuid.UUID
	if execution.AgentInstanceID != "" {
		id, err := uuid.Parse(execution.AgentInstanceID)
		if err != nil {
			return fmt.Errorf("failed to parse execution AgentInstance ID %s: %w", execution.AgentInstanceID, err)
		}
		instanceID = &id
	}
	var taskID *string
	if execution.TaskID != "" {
		taskID = &execution.TaskID
	}
	affected, err := c.q.UpdateScheduledRunExecution(ctx, dbgen.UpdateScheduledRunExecutionParams{
		ID: execution.ID, AgentInstanceID: instanceID, TaskID: taskID, Status: string(execution.Status),
		Phase: string(execution.Phase), StatusMessage: execution.StatusMessage, CompletionTime: execution.CompletionTime,
	})
	if err != nil {
		return fmt.Errorf("failed to update ScheduledRun execution %s: %w", execution.ID, err)
	}
	if affected > 0 {
		return nil
	}
	existing, err := c.GetScheduledRunExecution(ctx, execution.ID)
	if err != nil {
		return err
	}
	// A committed terminal transition may be retried after the client loses the
	// response. Accept exactly that retry, never a change to completed history.
	if sameExecutionOutcome(existing, execution) {
		return nil
	}
	return fmt.Errorf("failed to advance ScheduledRun execution %s: %w", execution.ID, ErrScheduledRunExecutionConflict)
}

func sameExecutionOutcome(left, right *ScheduledRunExecution) bool {
	if left.Phase != right.Phase || left.Status != right.Status || left.AgentInstanceID != right.AgentInstanceID ||
		left.TaskID != right.TaskID || left.StatusMessage != right.StatusMessage {
		return false
	}
	if left.CompletionTime == nil || right.CompletionTime == nil {
		return left.CompletionTime == nil && right.CompletionTime == nil
	}
	return left.CompletionTime.Truncate(time.Microsecond).Equal(right.CompletionTime.Truncate(time.Microsecond))
}

func (c *Client) GetScheduledRunExecution(ctx context.Context, id string) (*ScheduledRunExecution, error) {
	row, err := c.q.GetScheduledRunExecution(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get ScheduledRun execution %s: %w", id, notFoundOr(err))
	}
	return toScheduledRunExecution(row), nil
}

func (c *Client) GetScheduledRunExecutionByAgentInstanceID(ctx context.Context, instanceID string) (*ScheduledRunExecution, error) {
	id, err := uuid.Parse(instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse execution AgentInstance ID %s: %w", instanceID, err)
	}
	row, err := c.q.GetScheduledRunExecutionByAgentInstanceID(ctx, &id)
	if err != nil {
		return nil, fmt.Errorf("failed to get ScheduledRun execution for AgentInstance %s: %w", instanceID, notFoundOr(err))
	}
	return toScheduledRunExecution(row), nil
}

func (c *Client) ListScheduledRunExecutions(ctx context.Context, namespace, name, uid string, options ScheduledRunExecutionQuery) ([]ScheduledRunExecution, error) {
	limit := options.Limit
	if limit == 0 {
		limit = 50
	}
	// One extra row lets API callers detect whether another page exists.
	if limit < 1 || limit > 101 {
		return nil, fmt.Errorf("execution history limit must be between 1 and 101")
	}
	var before *time.Time
	if !options.Before.IsZero() {
		before = &options.Before
	}
	rows, err := c.q.ListScheduledRunExecutions(ctx, dbgen.ListScheduledRunExecutionsParams{
		ScheduledRunNamespace: namespace, ScheduledRunName: name, ScheduledRunUid: uid,
		BeforeTime: before, BeforeID: options.BeforeID, PageLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ScheduledRun executions for %s/%s: %w", namespace, name, err)
	}
	return scheduledRunExecutions(rows), nil
}

func (c *Client) ListInProgressScheduledRunExecutions(ctx context.Context) ([]ScheduledRunExecution, error) {
	rows, err := c.q.ListInProgressScheduledRunExecutions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list in-progress ScheduledRun executions: %w", err)
	}
	return scheduledRunExecutions(rows), nil
}

func scheduledRunExecutions(rows []dbgen.ScheduledRunExecution) []ScheduledRunExecution {
	executions := make([]ScheduledRunExecution, 0, len(rows))
	for _, row := range rows {
		executions = append(executions, *toScheduledRunExecution(row))
	}
	return executions
}

func toScheduledRunExecution(row dbgen.ScheduledRunExecution) *ScheduledRunExecution {
	execution := &ScheduledRunExecution{
		ID: row.ID, ScheduledRunNamespace: row.ScheduledRunNamespace, ScheduledRunName: row.ScheduledRunName,
		ScheduledRunUID: row.ScheduledRunUid, StartTime: row.StartTime, Deadline: row.Deadline,
		CompletionTime: row.CompletionTime, Trigger: v1alpha3.ScheduledRunExecutionTrigger(row.Trigger),
		Status: v1alpha3.ScheduledRunExecutionStatus(row.Status), StatusMessage: row.StatusMessage,
		Prompt: row.Prompt, Phase: ScheduledRunExecutionPhase(row.Phase), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.AgentInstanceID != nil {
		execution.AgentInstanceID = row.AgentInstanceID.String()
	}
	if row.TaskID != nil {
		execution.TaskID = *row.TaskID
	}
	return execution
}
