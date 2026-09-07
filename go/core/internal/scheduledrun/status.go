package scheduledrun

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
)

var errScheduledRunReplaced = errors.New("ScheduledRun was replaced")

const (
	maxInProgressSummary   = 100
	maxSummaryMessageRunes = 1024
)

// executionSummary is rebuilt from durable history, including terminal writes
// whose process died before it could update the Kubernetes status cache.
func (s *Scheduler) executionSummary(ctx context.Context, sr *v1alpha3.ScheduledRun) ([]v1alpha3.ScheduledRunExecution, error) {
	limit := recentExecutionsLimit(sr)
	completed := make([]v1alpha3.ScheduledRunExecution, 0, limit)
	query := database.ScheduledRunExecutionQuery{Limit: 100}
	for len(completed) < limit {
		records, err := s.store.ListScheduledRunExecutions(ctx, sr.Namespace, sr.Name, string(sr.UID), query)
		if err != nil {
			return nil, fmt.Errorf("failed to read execution history: %w", err)
		}
		for _, record := range records {
			if record.Status != v1alpha3.ScheduledRunExecutionStatus_InProgress && len(completed) < limit {
				completed = append(completed, executionStatus(record))
			}
		}
		if len(records) < query.Limit || len(completed) >= limit {
			break
		}
		last := records[len(records)-1]
		query.Before, query.BeforeID = last.StartTime, last.ID
	}
	inProgress, err := s.store.ListInProgressScheduledRunExecutions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read in-progress executions: %w", err)
	}
	slices.SortFunc(inProgress, func(a, b database.ScheduledRunExecution) int {
		if byTime := b.StartTime.Compare(a.StartTime); byTime != 0 {
			return byTime
		}
		return cmp.Compare(b.ID, a.ID)
	})
	active := 0
	for _, record := range inProgress {
		if record.ScheduledRunNamespace == sr.Namespace && record.ScheduledRunName == sr.Name && record.ScheduledRunUID == string(sr.UID) {
			completed = append(completed, executionStatus(record))
			active++
			if active == maxInProgressSummary {
				break
			}
		}
	}
	sortExecutions(completed)
	if len(completed) == 0 {
		return nil, nil
	}
	return completed, nil
}

func executionStatus(record database.ScheduledRunExecution) v1alpha3.ScheduledRunExecution {
	message := []rune(record.StatusMessage)
	if len(message) > maxSummaryMessageRunes {
		message = message[:maxSummaryMessageRunes]
	}
	result := v1alpha3.ScheduledRunExecution{
		ID: record.ID, StartTime: metav1.NewTime(record.StartTime.Truncate(time.Second)), Trigger: record.Trigger,
		AgentInstanceID: optionalString(record.AgentInstanceID), TaskID: optionalString(record.TaskID),
		Status: record.Status, StatusMessage: optionalString(string(message)),
	}
	if record.CompletionTime != nil {
		completed := metav1.NewTime(record.CompletionTime.Truncate(time.Second))
		result.CompletionTime = &completed
	}
	return result
}

func (s *Scheduler) writeExecutionStatus(ctx context.Context, execution *database.ScheduledRunExecution) error {
	key := types.NamespacedName{Namespace: execution.ScheduledRunNamespace, Name: execution.ScheduledRunName}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha3.ScheduledRun
		if err := s.kube.Get(writeCtx, key, &latest); err != nil {
			return err
		}
		if string(latest.UID) != execution.ScheduledRunUID || !latest.DeletionTimestamp.IsZero() {
			return errScheduledRunReplaced
		}
		mergeExecutionStatus(&latest, executionStatus(*execution))
		return s.kube.Status().Update(writeCtx, &latest)
	})
}

func mergeExecutionStatus(sr *v1alpha3.ScheduledRun, execution v1alpha3.ScheduledRunExecution) {
	found := false
	for i := range sr.Status.RecentExecutions {
		if sr.Status.RecentExecutions[i].ID == execution.ID {
			// A delayed in-progress writer must not revert a terminal summary.
			if sr.Status.RecentExecutions[i].Status != v1alpha3.ScheduledRunExecutionStatus_InProgress && execution.Status == v1alpha3.ScheduledRunExecutionStatus_InProgress {
				return
			}
			sr.Status.RecentExecutions[i] = execution
			found = true
			break
		}
	}
	if !found {
		sr.Status.RecentExecutions = append(sr.Status.RecentExecutions, execution)
	}
	sortExecutions(sr.Status.RecentExecutions)
	completed := 0
	active := 0
	for _, item := range sr.Status.RecentExecutions {
		if item.Status != v1alpha3.ScheduledRunExecutionStatus_InProgress {
			completed++
		} else {
			active++
		}
	}
	drop := completed - recentExecutionsLimit(sr)
	dropActive := active - maxInProgressSummary
	kept := sr.Status.RecentExecutions[:0]
	for _, item := range sr.Status.RecentExecutions {
		if item.Status == v1alpha3.ScheduledRunExecutionStatus_InProgress && dropActive > 0 {
			dropActive--
			continue
		}
		if item.Status != v1alpha3.ScheduledRunExecutionStatus_InProgress && drop > 0 {
			drop--
			continue
		}
		kept = append(kept, item)
	}
	sr.Status.RecentExecutions = kept
	updateLastExecutionTime(sr)
}

func sortExecutions(executions []v1alpha3.ScheduledRunExecution) {
	slices.SortFunc(executions, func(a, b v1alpha3.ScheduledRunExecution) int {
		if byTime := a.StartTime.Compare(b.StartTime.Time); byTime != 0 {
			return byTime
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func updateLastExecutionTime(sr *v1alpha3.ScheduledRun) {
	if len(sr.Status.RecentExecutions) > 0 {
		last := sr.Status.RecentExecutions[len(sr.Status.RecentExecutions)-1].StartTime
		if sr.Status.LastExecutionTime == nil || last.After(sr.Status.LastExecutionTime.Time) {
			sr.Status.LastExecutionTime = &last
		}
	}
}
