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

// ScheduledRunBinding stores the authenticated creator separately from writable
// Kubernetes metadata. A ScheduledRun UID can be bound to only one owner.
type ScheduledRunBinding struct {
	ScheduledRunNamespace string
	ScheduledRunName      string
	ScheduledRunUID       string
	BoundUserID           string
}

// ScheduledRunExecution is durable execution history, independent of Kubernetes status retention.
// UserID, Prompt and Deadline are immutable snapshots so retries cannot change ownership or inputs.
type ScheduledRunExecution struct {
	ID                    string
	ScheduledRunNamespace string
	ScheduledRunName      string
	ScheduledRunUID       string
	UserID                string
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
