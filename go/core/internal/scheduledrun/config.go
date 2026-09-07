// Package scheduledrun executes durable schedules through AgentInstances and A2A.
package scheduledrun

import (
	"fmt"
	"strings"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/robfig/cron/v3"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

// SystemUserID owns conversations created by the scheduler. Public authentication
// must never assign this identity to an external caller.
const SystemUserID = auth.ScheduledRunUserID

// ValidateSpec checks invariants also used by the API before creating a schedule.
func ValidateSpec(spec v1alpha3.ScheduledRunSpec) error {
	if spec.TargetRef.APIGroup == nil || *spec.TargetRef.APIGroup != v1alpha3.ScheduledRunTargetAPIGroup || spec.TargetRef.Kind != v1alpha3.ScheduledRunTargetKindAgentTemplate {
		return fmt.Errorf("targetRef must identify a kagent.dev AgentTemplate")
	}
	if problems := utilvalidation.IsDNS1123Subdomain(spec.TargetRef.Name); len(problems) > 0 {
		return fmt.Errorf("targetRef.name is invalid: %s", strings.Join(problems, "; "))
	}
	if problems := utilvalidation.IsDNS1123Subdomain(spec.HarnessRef.Name); len(problems) > 0 {
		return fmt.Errorf("harnessRef.name is invalid: %s", strings.Join(problems, "; "))
	}
	if strings.TrimSpace(spec.Prompt) == "" || len([]rune(spec.Prompt)) > 32768 {
		return fmt.Errorf("prompt must contain 1-32768 characters and non-whitespace text")
	}
	if spec.ExecutionTimeout != nil && spec.ExecutionTimeout.Duration <= 0 {
		return fmt.Errorf("executionTimeout must be greater than zero")
	}
	if spec.RecentExecutionsLimit != nil && (*spec.RecentExecutionsLimit < 1 || *spec.RecentExecutionsLimit > 100) {
		return fmt.Errorf("recentExecutionsLimit must be between 1 and 100")
	}
	_, err := parseSchedule(spec)
	return err
}

func parseSchedule(spec v1alpha3.ScheduledRunSpec) (cron.Schedule, error) {
	timeZone := v1alpha3.DefaultScheduledRunTimeZone
	if spec.TimeZone != nil {
		timeZone = *spec.TimeZone
	}
	if _, err := time.LoadLocation(timeZone); err != nil || timeZone == "" {
		return nil, fmt.Errorf("invalid timeZone %q", timeZone)
	}
	if len(strings.Fields(spec.Schedule)) != 5 {
		return nil, fmt.Errorf("schedule must be a standard five-field cron expression")
	}
	parsed, err := cron.ParseStandard("CRON_TZ=" + timeZone + " " + strings.TrimSpace(spec.Schedule))
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
