package database

import (
	"errors"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
)

// ScheduledRunExecutionPhase records recoverable steps across persistence and runtime calls.
type ScheduledRunExecutionPhase string

const (
	ScheduledRunExecutionPhaseCreating    ScheduledRunExecutionPhase = "Creating"
	ScheduledRunExecutionPhaseDispatching ScheduledRunExecutionPhase = "Dispatching"
	ScheduledRunExecutionPhasePolling     ScheduledRunExecutionPhase = "Polling"
	ScheduledRunExecutionPhaseComplete    ScheduledRunExecutionPhase = "Complete"
)

var ErrScheduledRunExecutionConflict = errors.New("ScheduledRun execution transition conflicts with its current state")

// ScheduledRunExecution is durable execution history, independent of Kubernetes status retention.
// Prompt and Deadline are immutable snapshots so retries do not pick up later spec edits.
type ScheduledRunExecution struct {
	ID                    string
	ScheduledRunNamespace string
	ScheduledRunName      string
	ScheduledRunUID       string
	StartTime             time.Time
	Deadline              time.Time
	CompletionTime        *time.Time
	Trigger               v1alpha3.ScheduledRunExecutionTrigger
	AgentInstanceID       string
	TaskID                string
	Status                v1alpha3.ScheduledRunExecutionStatus
	StatusMessage         string
	Prompt                string
	Phase                 ScheduledRunExecutionPhase
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ScheduledRunExecutionQuery uses (start time, ID) for stable descending pagination.
type ScheduledRunExecutionQuery struct {
	Before   time.Time
	BeforeID string
	Limit    int
}
