package scheduledrun

import (
	"context"
	"errors"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestControllerConvergesScheduleAndDurableSummary(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, _ := testScheduler(t, sr)
	controller := NewController(s)
	key := client.ObjectKeyFromObject(sr)
	execution, err := s.TriggerManualExecution(t.Context(), key)
	require.NoError(t, err)
	// Simulate terminal persistence immediately before a process crash; there is
	// no worker left that can update the CRD summary.
	execution.Status = v1alpha3.ScheduledRunExecutionStatus_Succeeded
	execution.Phase = database.ScheduledRunExecutionPhaseComplete
	now := time.Now()
	execution.CompletionTime = &now
	store.records[execution.ID] = *execution
	result, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Equal(t, summaryRecheckInterval, result.RequeueAfter)
	var got v1alpha3.ScheduledRun
	require.NoError(t, s.kube.Get(t.Context(), key, &got))
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, v1alpha3.ScheduledRunConditionTypeAccepted))
	require.Len(t, got.Status.RecentExecutions, 1)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_Succeeded, got.Status.RecentExecutions[0].Status)
	assert.NotNil(t, got.Status.NextExecutionTime)
	assert.NotNil(t, got.Status.LastExecutionTime)
	entry := s.entries[key]
	_, err = controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Equal(t, entry.id, s.entries[key].id, "unchanged reconcile must not reset cron registration")
	version := got.ResourceVersion
	require.NoError(t, s.kube.Get(t.Context(), key, &got))
	assert.Equal(t, version, got.ResourceVersion, "unchanged durable summary must not rewrite status")
	got.Spec.Suspended = new(true)
	require.NoError(t, s.kube.Update(t.Context(), &got))
	_, err = controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	require.NoError(t, s.kube.Get(t.Context(), key, &got))
	assert.Empty(t, s.entries)
	assert.Nil(t, got.Status.NextExecutionTime)
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, v1alpha3.ScheduledRunConditionTypeAccepted))
}

func TestControllerRejectsMissingHarnessAndRecovers(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, _ := testScheduler(t, sr)
	controller := NewController(s)
	key := client.ObjectKeyFromObject(sr)
	harness := &v1alpha3.Harness{ObjectMeta: metav1.ObjectMeta{Name: sr.Spec.HarnessRef.Name, Namespace: sr.Namespace}}
	require.NoError(t, s.kube.Delete(t.Context(), harness))
	_, err := controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var got v1alpha3.ScheduledRun
	require.NoError(t, s.kube.Get(t.Context(), key, &got))
	condition := meta.FindStatusCondition(got.Status.Conditions, v1alpha3.ScheduledRunConditionTypeAccepted)
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, targetNotFoundReason, condition.Reason)
	assert.Empty(t, s.entries)
	assert.Empty(t, store.records, "a rejected schedule must not reserve executions")
	require.NoError(t, s.kube.Create(t.Context(), harness))
	requests := controller.schedulesForTarget(t.Context(), harness)
	require.Len(t, requests, 1)
	assert.Equal(t, key, requests[0].NamespacedName)
	_, err = controller.Reconcile(t.Context(), requests[0])
	require.NoError(t, err)
	assert.Len(t, s.entries, 1)
	require.NoError(t, s.kube.Delete(t.Context(), &got))
	_, err = controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Empty(t, s.entries)
}

func TestLeaderCancellationPreservesRecoverableDispatch(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, gateway := testScheduler(t, sr)
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	started := make(chan struct{})
	gateway.send = func(ctx context.Context, _ *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("leader did not dispatch durable work")
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not drain on cancellation")
	}
	stored, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, database.ScheduledRunExecutionPhaseDispatching, stored.Phase)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_InProgress, stored.Status)
	assert.NotEmpty(t, stored.AgentInstanceID)
	assert.Nil(t, stored.CompletionTime)
}

func TestRecoveryDeduplicatesWorkersAndSkipsUnwatchedNamespaces(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, gateway := testScheduler(t, sr)
	s.namespaces = map[string]struct{}{sr.Namespace: {}}
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	foreign := *execution
	foreign.ID, foreign.ScheduledRunNamespace = "foreign", "other-installation"
	store.records[foreign.ID] = foreign
	started, release := make(chan struct{}), make(chan struct{})
	gateway.send = func(context.Context, *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error) {
		close(started)
		<-release
		return &a2atype.Message{}, nil
	}
	require.NoError(t, s.recoverExecutions(t.Context()))
	<-started
	require.NoError(t, s.recoverExecutions(t.Context()))
	close(release)
	s.wg.Wait()
	assert.Equal(t, 1, gateway.sends)
	foreignStored, err := store.GetScheduledRunExecution(t.Context(), foreign.ID)
	require.NoError(t, err)
	assert.Equal(t, database.ScheduledRunExecutionPhaseCreating, foreignStored.Phase)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_InProgress, foreignStored.Status)
}

func TestTerminalPersistenceFailureIsRetriedFromDurableState(t *testing.T) {
	sr := testScheduledRun()
	s, store, _, gateway := testScheduler(t, sr)
	execution, err := s.TriggerManualExecution(t.Context(), client.ObjectKeyFromObject(sr))
	require.NoError(t, err)
	gateway.send = func(_ context.Context, request *a2atype.SendMessageRequest) (a2atype.SendMessageResult, error) {
		task := &a2atype.Task{ID: "task", History: []*a2atype.Message{request.Message}, Status: a2atype.TaskStatus{State: a2atype.TaskStateCompleted}}
		gateway.tasks = []*a2atype.Task{task}
		store.updateErr = errors.New("temporary database outage")
		return task, nil
	}
	require.ErrorContains(t, s.runExecution(t.Context(), execution), "temporary database outage")
	store.updateErr = nil
	recoverable, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_InProgress, recoverable.Status)
	require.NoError(t, s.runExecution(t.Context(), recoverable))
	completed, err := store.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	assert.Equal(t, v1alpha3.ScheduledRunExecutionStatus_Succeeded, completed.Status)
	assert.Equal(t, 1, gateway.sends)
}
