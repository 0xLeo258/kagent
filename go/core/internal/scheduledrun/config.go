// Package scheduledrun executes durable schedules through AgentInstances and A2A.
package scheduledrun

import (
	"fmt"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/robfig/cron/v3"
)

// SystemUserID authenticates scheduler work and owns unbound schedules' instances.
// Public authentication must never assign this identity to an external caller.
const SystemUserID = auth.ScheduledRunUserID

// CRD admission validates the resource fields; cron validates the expression
// and IANA time zone. Descriptors such as @hourly are not enabled.
func parseSchedule(spec v1alpha3.ScheduledRunSpec) (cron.Schedule, error) {
	timeZone := v1alpha3.DefaultScheduledRunTimeZone
	if spec.TimeZone != nil {
		timeZone = *spec.TimeZone
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	parsed, err := parser.Parse("CRON_TZ=" + timeZone + " " + spec.Schedule)
	if err != nil {
		return nil, fmt.Errorf("failed to parse schedule: %w", err)
	}
	return parsed, nil
}

func isSuspended(sr *v1alpha3.ScheduledRun) bool {
	return sr.Spec.Suspended != nil && *sr.Spec.Suspended
}

func executionTimeout(sr *v1alpha3.ScheduledRun) time.Duration {
	if sr.Spec.ExecutionTimeout != nil {
		return sr.Spec.ExecutionTimeout.Duration
	}
	return v1alpha3.DefaultScheduledRunExecutionTimeout
}

func recentExecutionsLimit(sr *v1alpha3.ScheduledRun) int {
	if sr.Spec.RecentExecutionsLimit != nil {
		return int(*sr.Spec.RecentExecutionsLimit)
	}
	return int(v1alpha3.DefaultScheduledRunRecentExecutionsLimit)
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func statusMessage(value string) string {
	runes := []rune(value)
	if len(runes) > v1alpha3.MaxScheduledRunStatusMessageLength {
		return string(runes[:v1alpha3.MaxScheduledRunStatusMessageLength])
	}
	return value
}
