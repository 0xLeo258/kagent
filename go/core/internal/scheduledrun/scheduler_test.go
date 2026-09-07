package scheduledrun

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type testStore struct {
	mu        sync.Mutex
	records   map[string]database.ScheduledRunExecution
	createErr error
	updateErr error
	instances *testInstances
}

var _ executionStore = (*testStore)(nil)

func (s *testStore) GetAgentInstanceByRequestID(_ context.Context, _, requestID string) (*apiv1alpha1.AgentInstance, error) {
	s.instances.mu.Lock()
	defer s.instances.mu.Unlock()
	id := s.instances.byID[requestID]
	if id == "" {
		return nil, database.ErrNotFound
	}
	return &apiv1alpha1.AgentInstance{Id: id}, nil
}

func (s *testStore) CreateScheduledRunExecution(_ context.Context, record *database.ScheduledRunExecution) (*database.ScheduledRunExecution, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, false, s.createErr
	}
	if old, found := s.records[record.ID]; found {
		return &old, false, nil
	}
	s.records[record.ID] = *record
	result := *record
	return &result, true, nil
}

func (s *testStore) UpdateScheduledRunExecution(_ context.Context, record *database.ScheduledRunExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	old, found := s.records[record.ID]
	if !found {
		return database.ErrNotFound
	}
	if old.Status != v1alpha3.ScheduledRunExecutionStatus_InProgress {
		return database.ErrScheduledRunExecutionConflict
	}
	s.records[record.ID] = *record
	return nil
}

func (s *testStore) GetScheduledRunExecution(_ context.Context, id string) (*database.ScheduledRunExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.records[id]
	if !found {
		return nil, database.ErrNotFound
	}
	return &record, nil
}

func (s *testStore) ListScheduledRunExecutions(_ context.Context, namespace, name, uid string, query database.ScheduledRunExecutionQuery) ([]database.ScheduledRunExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []database.ScheduledRunExecution
	for _, record := range s.records {
		if record.ScheduledRunNamespace != namespace || record.ScheduledRunName != name || record.ScheduledRunUID != uid {
			continue
		}
		if !query.Before.IsZero() && (record.StartTime.After(query.Before) || (record.StartTime.Equal(query.Before) && record.ID >= query.BeforeID)) {
			continue
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b database.ScheduledRunExecution) int {
		if byTime := b.StartTime.Compare(a.StartTime); byTime != 0 {
			return byTime
		}
		return strings.Compare(b.ID, a.ID)
	})
	return records[:min(len(records), query.Limit)], nil
}

func (s *testStore) ListInProgressScheduledRunExecutions(context.Context) ([]database.ScheduledRunExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []database.ScheduledRunExecution
	for _, record := range s.records {
		if record.Status == v1alpha3.ScheduledRunExecutionStatus_InProgress {
			records = append(records, record)
		}
	}
	return records, nil
}

type testInstances struct {
	mu       sync.Mutex
	byID     map[string]string
	requests []string
	after    func()
	harness  *apiv1alpha1.ResourceReference
	template *apiv1alpha1.ResourceReference
}

var _ instanceCreator = (*testInstances)(nil)

func (i *testInstances) Create(ctx context.Context, harness, template *apiv1alpha1.ResourceReference, requestID, _ string) (*apiv1alpha1.AgentInstance, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	session, ok := auth.AuthSessionFrom(ctx)
	if !ok || session.Principal().User.ID != SystemUserID {
		return nil, errors.New("missing scheduler identity")
	}
	i.harness, i.template = harness, template
	i.requests = append(i.requests, requestID)
	if i.byID[requestID] == "" {
		i.byID[requestID] = uuid.NewString()
	}
	if i.after != nil {
		i.after()
	}
	return &apiv1alpha1.AgentInstance{Id: i.byID[requestID]}, nil
}

type testGateway struct {
	send      func(context.Context, *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error)
	get       func(context.Context, *a2atype.GetTaskRequest) (*a2atype.Task, error)
	tasks     []*a2atype.Task
	sends     int
	subs      int
	cancels   int
	cancelErr error
}

var _ taskGateway = (*testGateway)(nil)

func (g *testGateway) SendMessage(ctx context.Context, request *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error) {
	g.sends++
	if request.Message.ContextID != "" && request.Message.ContextID != "scheduled-runtime-context" {
		return nil, a2atype.ErrInvalidRequest
	}
	request.Message.ContextID = "scheduled-runtime-context"
	if g.send != nil {
		return g.send(ctx, request)
	}
	task := &a2atype.Task{ID: "task", ContextID: request.Message.ContextID, History: []*a2atype.Message{request.Message}, Status: a2atype.TaskStatus{State: a2atype.TaskStateCompleted}}
	g.tasks = append(g.tasks, task)
	return task, nil
}

func (g *testGateway) GetTask(ctx context.Context, request *a2atype.GetTaskRequest) (*a2atype.Task, error) {
	if g.get != nil {
		return g.get(ctx, request)
	}
	for _, task := range g.tasks {
		if task.ID == request.ID {
			return task, nil
		}
	}
	return nil, a2atype.ErrTaskNotFound
}

func (g *testGateway) ListTasks(_ context.Context, request *a2atype.ListTasksRequest) (*a2atype.ListTasksResponse, error) {
	if request.ContextID != "" && request.ContextID != "scheduled-runtime-context" {
		return &a2atype.ListTasksResponse{}, nil
	}
	return &a2atype.ListTasksResponse{Tasks: g.tasks}, nil
}

func (g *testGateway) SubscribeToTask(_ context.Context, request *a2atype.SubscribeToTaskRequest) iter.Seq2[a2atype.Event, error] {
	g.subs++
	return func(yield func(a2atype.Event, error) bool) {
		yield(&a2atype.Task{ID: request.ID, Status: a2atype.TaskStatus{State: a2atype.TaskStateWorking}}, nil)
	}
}

func (g *testGateway) CancelTask(context.Context, *a2atype.CancelTaskRequest) (*a2atype.Task, error) {
	g.cancels++
	return nil, g.cancelErr
}

func testScheduledRun() *v1alpha3.ScheduledRun {
	return &v1alpha3.ScheduledRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "schedule", UID: "original", Generation: 1},
		Spec: v1alpha3.ScheduledRunSpec{
			TargetRef:  corev1.TypedLocalObjectReference{APIGroup: new("kagent.dev"), Kind: "AgentTemplate", Name: "template"},
			HarnessRef: corev1.LocalObjectReference{Name: "harness"}, Schedule: "0 * * * *", Prompt: "original prompt",
		},
	}
}

func testScheduler(t *testing.T, sr *v1alpha3.ScheduledRun) (*Scheduler, *testStore, *testInstances, *testGateway) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha3.ScheduledRun{}).
		WithObjects(sr, &v1alpha3.AgentTemplate{ObjectMeta: metav1.ObjectMeta{Namespace: sr.Namespace, Name: sr.Spec.TargetRef.Name}}, &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Namespace: sr.Namespace, Name: sr.Spec.HarnessRef.Name}}).Build()
	store := &testStore{records: make(map[string]database.ScheduledRunExecution)}
	instances := &testInstances{byID: make(map[string]string)}
	store.instances = instances
	gateway := &testGateway{}
	scheduler, err := NewScheduler(Config{Kube: kube, Store: store, Instances: instances, Gateway: gateway})
	require.NoError(t, err)
	return scheduler, store, instances, gateway
}

func TestValidateSpec(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*v1alpha3.ScheduledRunSpec)
		wantErr string
	}{
		{name: "default", change: func(*v1alpha3.ScheduledRunSpec) {}},
		{name: "legacy target", change: func(s *v1alpha3.ScheduledRunSpec) { s.TargetRef.Kind = "Agent" }, wantErr: "AgentTemplate"},
		{name: "invalid harness", change: func(s *v1alpha3.ScheduledRunSpec) { s.HarnessRef.Name = "bad/name" }, wantErr: "harnessRef"},
		{name: "cron descriptor", change: func(s *v1alpha3.ScheduledRunSpec) { s.Schedule = "@hourly" }, wantErr: "five-field"},
		{name: "invalid timezone", change: func(s *v1alpha3.ScheduledRunSpec) { s.TimeZone = new("Mars/Olympus") }, wantErr: "timeZone"},
		{name: "blank prompt", change: func(s *v1alpha3.ScheduledRunSpec) { s.Prompt = "\n " }, wantErr: "prompt"},
		{name: "nonpositive timeout", change: func(s *v1alpha3.ScheduledRunSpec) { s.ExecutionTimeout = &metav1.Duration{} }, wantErr: "executionTimeout"},
		{name: "unbounded history", change: func(s *v1alpha3.ScheduledRunSpec) { s.RecentExecutionsLimit = new(int32(101)) }, wantErr: "recentExecutionsLimit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := testScheduledRun().Spec
			tt.change(&spec)
			err := ValidateSpec(spec)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestManualTriggerDurablyQueuesSuspendedRun(t *testing.T) {
	sr := testScheduledRun()
	sr.Spec.Suspended = new(true)
	s, store, instances, gateway := testScheduler(t, sr)
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	require.NotNil(t, execution)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_InProgress, execution.Status)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionTrigger_Manual, execution.Trigger)
	assert.Equal(t, sr.Spec.Prompt, execution.Prompt)
	assert.Equal(t, v1alpha3.DefaultScheduledRunExecutionTimeout, execution.Deadline.Sub(execution.StartTime))
	assert.Empty(t, instances.requests)
	assert.Zero(t, gateway.sends)
	_, err = store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	automatic, err := s.reserve(t.Context(), client.ObjectKeyFromObject(sr), sr.UID, false)
	require.NoError(t, err)
	assert.Nil(t, automatic)
}

func TestFailedReservationDoesNotDispatch(t *testing.T) {
	sr := testScheduledRun()
	s, store, instances, gateway := testScheduler(t, sr)
	store.createErr = errors.New("database unavailable")
	_, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.ErrorContains(t, err, "database unavailable")
	assert.Empty(t, instances.requests)
	assert.Zero(t, gateway.sends)
}

func TestCreationRecoveryUsesSameRequestAndPromptSnapshot(t *testing.T) {
	sr := testScheduledRun()
	s, store, instances, gateway := testScheduler(t, sr)
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	store.updateErr = errors.New("write failed after creation")
	require.ErrorContains(t, s.runExecution(t.Context(), execution), "write failed")
	stored, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, database.ScheduledRunExecutionPhaseCreating, stored.Phase)
	assert.Empty(t, stored.AgentInstanceID)
	store.updateErr = nil
	latest := &v1alpha3.ScheduledRun{}
	require.NoError(t, s.kube.Get(t.Context(), client.ObjectKeyFromObject(sr), latest))
	latest.Spec.Prompt = "changed after dispatch was reserved"
	require.NoError(t, s.kube.Update(t.Context(), latest))
	require.NoError(t, s.runExecution(t.Context(), stored))
	assert.Equal(t, []string{execution.ID, execution.ID}, instances.requests)
	assert.Len(t, instances.byID, 1)
	assert.Equal(t, &apiv1alpha1.ResourceReference{Namespace: sr.Namespace, Name: sr.Spec.HarnessRef.Name}, instances.harness)
	assert.Equal(t, &apiv1alpha1.ResourceReference{Namespace: sr.Namespace, Name: sr.Spec.TargetRef.Name}, instances.template)
	assert.Equal(t, "original prompt", gateway.tasks[0].History[0].Parts[0].Text())
	assert.Equal(t, execution.ID, gateway.tasks[0].History[0].ID)
	completed, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_Succeeded, completed.Status)
}

func TestDispatchRecoveryFindsOriginalTaskWithoutResending(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, gateway := testScheduler(t, sr)
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	gateway.send = func(_ context.Context, request *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error) {
		task := &a2atype.Task{ID: "original-task", History: []*a2atype.Message{request.Message}, Status: a2atype.TaskStatus{State: a2atype.TaskStateWorking}}
		gateway.tasks = []*a2atype.Task{task}
		store.updateErr = errors.New("crash before task ID persisted")
		return task, nil
	}
	require.ErrorContains(t, s.runExecution(t.Context(), execution), "crash before task ID")
	stored, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, database.ScheduledRunExecutionPhaseDispatching, stored.Phase)
	assert.Empty(t, stored.TaskID)
	store.updateErr = nil
	gateway.tasks[0].Status.State = a2atype.TaskStateCompleted
	newer := &a2atype.Task{ID: "unrelated-newer-task", History: []*a2atype.Message{{ID: "user-message"}}, Status: a2atype.TaskStatus{State: a2atype.TaskStateFailed}}
	gateway.tasks = append([]*a2atype.Task{newer}, gateway.tasks...)
	require.NoError(t, s.runExecution(t.Context(), stored))
	assert.Equal(t, 1, gateway.sends)
	completed, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, "original-task", completed.TaskID)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_Succeeded, completed.Status)
}

func TestRecoveryTerminatesExpiredExecutionsWithoutTaskID(t *testing.T) {
	for _, phase := range []database.ScheduledRunExecutionPhase{database.ScheduledRunExecutionPhaseCreating, database.ScheduledRunExecutionPhaseDispatching} {
		t.Run(string(phase), func(t *testing.T) {
			sr := testScheduledRun()
			s, store, _, gateway := testScheduler(t, sr)
			execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
			require.NoError(t, err)
			execution.Deadline = time.Now().Add(-time.Hour)
			execution.Phase = phase
			if phase == database.ScheduledRunExecutionPhaseDispatching {
				execution.AgentInstanceID = uuid.NewString()
			}
			store.records[execution.ID] = *execution
			require.NoError(t, s.runExecution(t.Context(), execution))
			completed, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
			require.NoError(t, err)
			assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_TimedOut, completed.Status)
			assert.NotNil(t, completed.CompletionTime)
			assert.Zero(t, gateway.sends)
		})
	}
}

func TestExpiredCreationRecoversReservedInstanceWithoutProvisioning(t *testing.T) {
	sr := testScheduledRun()
	s, store, instances, gateway := testScheduler(t, sr)
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	reservedID := uuid.NewString()
	instances.byID[execution.ID] = reservedID
	execution.Deadline = time.Now().Add(-time.Minute)
	store.records[execution.ID] = *execution
	require.NoError(t, s.runExecution(t.Context(), execution))
	completed, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, reservedID, completed.AgentInstanceID)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_TimedOut, completed.Status)
	assert.Contains(t, completed.StatusMessage, "reserved instance")
	assert.Empty(t, instances.requests)
	assert.Zero(t, gateway.sends)
}

func TestExpiredPollingReadsActualOutcomeAndCancelsActiveTasks(t *testing.T) {
	for _, state := range []a2atype.TaskState{a2atype.TaskStateCompleted, a2atype.TaskStateWorking} {
		t.Run(string(state), func(t *testing.T) {
			sr := testScheduledRun()
			s, store, _, gateway := testScheduler(t, sr)
			execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
			require.NoError(t, err)
			execution.AgentInstanceID, execution.TaskID = uuid.NewString(), "task"
			execution.Phase = database.ScheduledRunExecutionPhasePolling
			execution.Deadline = time.Now().Add(-time.Hour)
			store.records[execution.ID] = *execution
			gateway.get = func(ctx context.Context, _ *a2atype.GetTaskRequest) (*a2atype.Task, error) {
				require.NoError(t, ctx.Err(), "final lookup must have a live bounded context")
				return &a2atype.Task{ID: "task", Status: a2atype.TaskStatus{State: state}}, nil
			}
			gateway.cancelErr = errors.New("runtime unreachable")
			require.NoError(t, s.runExecution(t.Context(), execution))
			completed, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
			require.NoError(t, err)
			if state == a2atype.TaskStateCompleted {
				assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_Succeeded, completed.Status)
				assert.Zero(t, gateway.cancels)
			} else {
				assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_TimedOut, completed.Status)
				assert.Equal(t, 1, gateway.cancels)
			}
		})
	}
}

func TestStatusWritesFenceRecreatedScheduledRun(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, _ := testScheduler(t, sr)
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	require.NoError(t, s.kube.Delete(t.Context(), sr))
	replacement := testScheduledRun()
	replacement.UID = "replacement"
	require.NoError(t, s.kube.Create(t.Context(), replacement))
	require.ErrorIs(t, s.writeExecutionStatus(t.Context(), execution), errScheduledRunReplaced)
	var got v1alpha3.ScheduledRun
	require.NoError(t, s.kube.Get(t.Context(), client.ObjectKeyFromObject(sr), &got))
	assert.Empty(t, got.Status.RecentExecutions)
	assert.Nil(t, got.Status.LastExecutionTime)
	stored, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, string(sr.UID), stored.ScheduledRunUID, "history retains its original owner")
}

func TestSummaryRecoversTerminalHistoryAndKeepsOlderInProgress(t *testing.T) {
	sr := testScheduledRun()
	sr.Spec.RecentExecutionsLimit = new(int32(2))
	s, store, _, _ := testScheduler(t, sr)
	start := time.Now()
	for index := range 105 {
		status := v1alpha3.ScheduledRunExecutionStatus_Succeeded
		if index == 0 {
			status = v1alpha3.ScheduledRunExecutionStatus_InProgress
		}
		id := fmt.Sprintf("execution-%03d", index)
		store.records[id] = database.ScheduledRunExecution{ID: id, ScheduledRunNamespace: sr.Namespace, ScheduledRunName: sr.Name, ScheduledRunUID: string(sr.UID), StartTime: start.Add(time.Duration(index) * time.Minute), Status: status}
	}
	summary, err := s.executionSummary(t.Context(), sr)
	require.NoError(t, err)
	require.Len(t, summary, 3)
	assert.Equal(t, "execution-000", summary[0].ID)
	assert.Equal(t, "execution-103", summary[1].ID)
	assert.Equal(t, "execution-104", summary[2].ID)
}

func TestSummaryBoundsActiveRecordsAndMessageSize(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, _ := testScheduler(t, sr)
	start := time.Now()
	for index := range maxInProgressSummary + 5 {
		id := fmt.Sprintf("active-%03d", index)
		store.records[id] = database.ScheduledRunExecution{
			ID: id, ScheduledRunNamespace: sr.Namespace, ScheduledRunName: sr.Name, ScheduledRunUID: string(sr.UID),
			StartTime: start.Add(time.Duration(index) * time.Minute), Status: v1alpha3.ScheduledRunExecutionStatus_InProgress,
			StatusMessage: strings.Repeat("界", 4096),
		}
	}
	summary, err := s.executionSummary(t.Context(), sr)
	require.NoError(t, err)
	require.Len(t, summary, maxInProgressSummary)
	assert.Equal(t, "active-005", summary[0].ID)
	assert.Equal(t, "active-104", summary[len(summary)-1].ID)
	require.NotNil(t, summary[0].StatusMessage)
	assert.Len(t, []rune(*summary[0].StatusMessage), maxSummaryMessageRunes)
	assert.Len(t, []rune(store.records[summary[0].ID].StatusMessage), 4096, "durable history keeps the complete diagnostic")
}

func TestTaskOutcomeClassification(t *testing.T) {
	tests := []struct {
		name     string
		state    a2atype.TaskState
		want     v1alpha3.ScheduledRunExecutionStatus
		terminal bool
	}{
		{name: "completed", state: a2atype.TaskStateCompleted, want: v1alpha3.ScheduledRunExecutionStatus_Succeeded, terminal: true},
		{name: "failed", state: a2atype.TaskStateFailed, want: v1alpha3.ScheduledRunExecutionStatus_Failed, terminal: true},
		{name: "canceled", state: a2atype.TaskStateCanceled, want: v1alpha3.ScheduledRunExecutionStatus_Failed, terminal: true},
		{name: "rejected", state: a2atype.TaskStateRejected, want: v1alpha3.ScheduledRunExecutionStatus_Failed, terminal: true},
		{name: "working", state: a2atype.TaskStateWorking, want: v1alpha3.ScheduledRunExecutionStatus_InProgress},
		{name: "waiting for interaction", state: a2atype.TaskStateInputRequired, want: v1alpha3.ScheduledRunExecutionStatus_InProgress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, message, terminal := executionStatusForTask(&a2atype.Task{Status: a2atype.TaskStatus{State: tt.state, Message: a2atype.NewMessage(a2atype.MessageRoleAgent, a2atype.NewTextPart("diagnostic"))}})
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.terminal, terminal)
			if got == v1alpha3.ScheduledRunExecutionStatus_Failed {
				assert.Equal(t, "diagnostic", message)
			}
		})
	}
}
