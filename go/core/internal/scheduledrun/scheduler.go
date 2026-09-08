package scheduledrun

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	recoveryInterval = 5 * time.Second
	writeTimeout     = 10 * time.Second
	maxWorkers       = 32
)

type executionStore interface {
	GetScheduledRunBinding(context.Context, string, string, string) (*database.ScheduledRunBinding, error)
	CreateScheduledRunExecution(context.Context, *database.ScheduledRunExecution) (*database.ScheduledRunExecution, bool, error)
	UpdateScheduledRunExecution(context.Context, *database.ScheduledRunExecution) error
	GetScheduledRunExecution(context.Context, string) (*database.ScheduledRunExecution, error)
	GetAgentInstanceByRequestID(context.Context, string, string) (*apiv1alpha1.AgentInstance, error)
	ListScheduledRunExecutions(context.Context, string, string, string, database.ScheduledRunExecutionQuery) ([]database.ScheduledRunExecution, error)
	ListInProgressScheduledRunExecutions(context.Context) ([]database.ScheduledRunExecution, error)
}

type instanceCreator interface {
	CreateForOwner(context.Context, *apiv1alpha1.ResourceReference, *apiv1alpha1.ResourceReference, string, string, string) (*apiv1alpha1.AgentInstance, error)
}

type taskGateway interface {
	SendMessage(context.Context, *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error)
	GetTask(context.Context, *a2atype.GetTaskRequest) (*a2atype.Task, error)
	ListTasks(context.Context, *a2atype.ListTasksRequest) (*a2atype.ListTasksResponse, error)
	SubscribeToTask(context.Context, *a2atype.SubscribeToTaskRequest) iter.Seq2[a2atype.Event, error]
	CancelTask(context.Context, *a2atype.CancelTaskRequest) (*a2atype.Task, error)
}

// Config supplies the existing control-plane boundaries used by the scheduler.
// Instances must authorize the scheduler's internal identity to create instances.
type Config struct {
	Kube       client.Client
	Store      executionStore
	Instances  instanceCreator
	Gateway    taskGateway
	Registerer prometheus.Registerer
	// WatchNamespaces limits recovery to namespaces owned by this installation.
	// An empty list follows the manager convention of watching all namespaces.
	WatchNamespaces []string
}

type scheduledEntry struct {
	id       cron.EntryID
	schedule string
	timeZone string
	uid      types.UID
}

// Scheduler runs automatic dispatch and recovery only on the elected leader.
// API replicas only reserve durable manual executions, so a caller disconnect or
// follower shutdown cannot cancel accepted work.
type Scheduler struct {
	kube       client.Client
	store      executionStore
	instances  instanceCreator
	gateway    taskGateway
	cron       *cron.Cron
	metrics    *schedulerMetrics
	wake       chan struct{}
	namespaces map[string]struct{}

	mu      sync.Mutex
	entries map[types.NamespacedName]scheduledEntry
	workers map[string]struct{}
	wg      sync.WaitGroup
}

var _ manager.LeaderElectionRunnable = (*Scheduler)(nil)
var _ manager.Runnable = (*Scheduler)(nil)

func NewScheduler(config Config) (*Scheduler, error) {
	if config.Kube == nil || config.Store == nil || config.Instances == nil || config.Gateway == nil {
		return nil, fmt.Errorf("scheduler requires Kubernetes, store, instance service, and A2A gateway")
	}
	metrics, err := newMetrics(config.Registerer)
	if err != nil {
		return nil, err
	}
	namespaces := make(map[string]struct{}, len(config.WatchNamespaces))
	for _, namespace := range config.WatchNamespaces {
		namespaces[namespace] = struct{}{}
	}
	return &Scheduler{
		kube: config.Kube, store: config.Store, instances: config.Instances, gateway: config.Gateway,
		cron: cron.New(), metrics: metrics, wake: make(chan struct{}, 1),
		entries: make(map[types.NamespacedName]scheduledEntry), workers: make(map[string]struct{}),
		namespaces: namespaces,
	}, nil
}

func (s *Scheduler) NeedLeaderElection() bool { return true }

func (s *Scheduler) Start(ctx context.Context) error {
	s.cron.Start()
	defer func() {
		<-s.cron.Stop().Done()
		s.wg.Wait()
	}()
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()
	for {
		if err := s.recoverExecutions(ctx); err != nil && ctx.Err() == nil {
			logging.FromContext(ctx).ErrorContext(ctx, "failed to recover scheduled executions", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// updateSchedule converges cron registration without resetting unchanged entries.
func (s *Scheduler) updateSchedule(ctx context.Context, sr *v1alpha3.ScheduledRun, parsed cron.Schedule) error {
	if _, err := s.resolveUserID(ctx, sr); err != nil {
		s.RemoveSchedule(client.ObjectKeyFromObject(sr))
		return err
	}
	key := client.ObjectKeyFromObject(sr)
	timeZone := v1alpha3.DefaultScheduledRunTimeZone
	if sr.Spec.TimeZone != nil {
		timeZone = *sr.Spec.TimeZone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[key]; ok {
		if !isSuspended(sr) && old.uid == sr.UID && old.schedule == sr.Spec.Schedule && old.timeZone == timeZone {
			return nil
		}
		s.cron.Remove(old.id)
		delete(s.entries, key)
	}
	if !isSuspended(sr) {
		uid := sr.UID
		// Controller request contexts end after Reconcile. Each short cron reservation
		// has its own bounded context; the leader-owned worker performs runtime I/O.
		job := cron.FuncJob(func() {
			reserveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
			defer cancel()
			if _, err := s.reserve(reserveCtx, key, uid, false); err != nil && !apierrors.IsNotFound(err) {
				logging.FromContext(reserveCtx).ErrorContext(reserveCtx, "failed to reserve scheduled execution", "error", err, "scheduled_run", key.String())
			}
		})
		id := s.cron.Schedule(parsed, cron.NewChain(cron.Recover(cronLog{ctx: context.WithoutCancel(ctx)})).Then(job))
		s.entries[key] = scheduledEntry{id: id, uid: uid, schedule: sr.Spec.Schedule, timeZone: timeZone}
	}
	s.metrics.activeSchedules.Set(float64(len(s.entries)))
	return nil
}

func (s *Scheduler) RemoveSchedule(key types.NamespacedName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[key]; ok {
		s.cron.Remove(entry.id)
		delete(s.entries, key)
		s.metrics.activeSchedules.Set(float64(len(s.entries)))
	}
}

// TriggerManualExecution acknowledges only after the execution is durably queued.
// A suspended schedule can still be triggered manually.
func (s *Scheduler) TriggerManualExecution(ctx context.Context, key types.NamespacedName) (*database.ScheduledRunExecution, error) {
	return s.reserve(ctx, key, "", true)
}

func (s *Scheduler) reserve(ctx context.Context, key types.NamespacedName, uid types.UID, manual bool) (*database.ScheduledRunExecution, error) {
	if !s.watchesNamespace(key.Namespace) {
		return nil, fmt.Errorf("ScheduledRun namespace %q is not watched by this installation", key.Namespace)
	}
	var sr v1alpha3.ScheduledRun
	if err := s.kube.Get(ctx, key, &sr); err != nil {
		return nil, fmt.Errorf("failed to get ScheduledRun %s: %w", key, err)
	}
	if (uid != "" && sr.UID != uid) || !sr.DeletionTimestamp.IsZero() || (!manual && isSuspended(&sr)) {
		return nil, nil
	}

	if _, err := parseSchedule(sr.Spec); err != nil {
		return nil, err
	}
	if err := validateTargets(ctx, s.kube, &sr); err != nil {
		return nil, err
	}
	owner, err := s.resolveUserID(ctx, &sr)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate execution identifier: %w", err)
	}
	trigger := v1alpha3.ScheduledRunExecutionTrigger_Scheduled
	if manual {
		trigger = v1alpha3.ScheduledRunExecutionTrigger_Manual
	}
	start := time.Now().UTC()
	record, _, err := s.store.CreateScheduledRunExecution(ctx, &database.ScheduledRunExecution{
		ID: id.String(), ScheduledRunNamespace: sr.Namespace, ScheduledRunName: sr.Name, ScheduledRunUID: string(sr.UID),
		UserID:    owner,
		StartTime: start, Deadline: start.Add(executionTimeout(&sr)), Trigger: trigger, Prompt: sr.Spec.Prompt,
		Status: v1alpha3.ScheduledRunExecutionStatus_InProgress, Phase: database.ScheduledRunExecutionPhaseCreating,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to reserve execution for ScheduledRun %s: %w", key, err)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return record, nil
}

func (s *Scheduler) recoverExecutions(ctx context.Context) error {
	records, err := s.store.ListInProgressScheduledRunExecutions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list pending scheduled executions: %w", err)
	}
	for i := range records {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		record := records[i]
		if !s.watchesNamespace(record.ScheduledRunNamespace) {
			continue
		}
		s.mu.Lock()
		_, running := s.workers[record.ID]
		if running || len(s.workers) >= maxWorkers {
			s.mu.Unlock()
			continue
		}
		s.workers[record.ID] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.workers, record.ID)
				s.mu.Unlock()
				s.wg.Done()
			}()
			if err := s.runExecution(ctx, &record); err != nil && ctx.Err() == nil {
				logging.FromContext(ctx).ErrorContext(ctx, "failed to advance scheduled execution", "error", err, "execution_id", record.ID)
			}
		}()
	}
	return nil
}

func (s *Scheduler) watchesNamespace(namespace string) bool {
	if len(s.namespaces) == 0 {
		return true
	}
	_, watched := s.namespaces[namespace]
	return watched
}

func (s *Scheduler) persist(ctx context.Context, execution *database.ScheduledRunExecution) error {
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := s.store.UpdateScheduledRunExecution(writeCtx, execution); err != nil {
		return fmt.Errorf("failed to persist scheduled execution %s: %w", execution.ID, err)
	}
	return nil
}

func (s *Scheduler) complete(ctx context.Context, execution *database.ScheduledRunExecution, status v1alpha3.ScheduledRunExecutionStatus, message string) error {
	now := time.Now().UTC()
	execution.Status, execution.StatusMessage = status, statusMessage(message)
	execution.CompletionTime, execution.Phase = &now, database.ScheduledRunExecutionPhaseComplete
	if err := s.persist(ctx, execution); err != nil {
		return err
	}
	if status == v1alpha3.ScheduledRunExecutionStatus_TimedOut && execution.TaskID != "" {
		cancelCtx, cancel := context.WithTimeout(gatewayContext(context.WithoutCancel(ctx), execution), writeTimeout)
		_, err := s.gateway.CancelTask(cancelCtx, &a2atype.CancelTaskRequest{ID: a2atype.TaskID(execution.TaskID)})
		cancel()
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "failed to cancel timed-out scheduled task", "error", err, "execution_id", execution.ID, "task_id", execution.TaskID)
		}
	}
	s.metrics.executions.WithLabelValues(string(status), string(execution.Trigger)).Inc()
	s.metrics.duration.Observe(now.Sub(execution.StartTime).Seconds())
	if err := s.writeExecutionStatus(ctx, execution); err != nil && !errors.Is(err, errScheduledRunReplaced) && !apierrors.IsNotFound(err) {
		// The controller rebuilds its summary from durable history on every recheck.
		return err
	}
	return nil
}

type cronLog struct{ ctx context.Context }

func (c cronLog) Info(message string, values ...any) {
	logging.FromContext(c.ctx).DebugContext(c.ctx, "cron scheduler event", "event", message, "details", values)
}

func (c cronLog) Error(err error, message string, values ...any) {
	logging.FromContext(c.ctx).ErrorContext(c.ctx, "cron scheduler job failed", "event", message, "details", values, "error", err)
}

var errBindingPending = errors.New("ScheduledRun user binding is pending")

// The marker prevents an API create interrupted before its database write from
// executing as the system user. Only the binding for this UID supplies identity.
func (s *Scheduler) resolveUserID(ctx context.Context, sr *v1alpha3.ScheduledRun) (string, error) {
	binding, err := s.store.GetScheduledRunBinding(ctx, sr.Namespace, sr.Name, string(sr.UID))
	if errors.Is(err, database.ErrNotFound) {
		if _, required := sr.Annotations[v1alpha3.ScheduledRunBindingRequiredAnnotation]; required {
			return "", errBindingPending
		}
		return SystemUserID, nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to resolve ScheduledRun user: %w", err)
	}
	return binding.BoundUserID, nil
}
