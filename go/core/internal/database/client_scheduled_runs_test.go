package database

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/stretchr/testify/require"
)

func pendingScheduledExecution(id, uid string, start time.Time) *ScheduledRunExecution {
	return &ScheduledRunExecution{
		ID: id, ScheduledRunNamespace: "team-a", ScheduledRunName: "nightly", ScheduledRunUID: uid, UserID: auth.ScheduledRunUserID,
		StartTime: start, Deadline: start.Add(time.Hour), Prompt: "Check cluster health", Trigger: v1alpha3.ScheduledRunExecutionTrigger_Manual,
		Phase: ScheduledRunExecutionPhaseCreating, Status: v1alpha3.ScheduledRunExecutionStatus_InProgress,
	}
}

func TestScheduledRunBindingIsImmutableAndScopedToUID(t *testing.T) {
	client := NewClient(setupTestDB(t))
	binding := &ScheduledRunBinding{
		ScheduledRunNamespace: "team-a", ScheduledRunName: "nightly", ScheduledRunUID: uuid.NewString(), BoundUserID: "alice",
	}
	_, err := client.GetScheduledRunBinding(t.Context(), binding.ScheduledRunNamespace, binding.ScheduledRunName, binding.ScheduledRunUID)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, client.CreateScheduledRunBinding(t.Context(), binding))
	require.NoError(t, client.CreateScheduledRunBinding(t.Context(), binding), "retry preserves the same owner")
	for _, mutate := range []func(*ScheduledRunBinding){
		func(b *ScheduledRunBinding) { b.BoundUserID = "bob" },
		func(b *ScheduledRunBinding) { b.ScheduledRunNamespace = "other-team" },
		func(b *ScheduledRunBinding) { b.ScheduledRunName = "different" },
	} {
		conflict := *binding
		mutate(&conflict)
		require.ErrorIs(t, client.CreateScheduledRunBinding(t.Context(), &conflict), ErrIdempotencyConflict)
	}
	stored, err := client.GetScheduledRunBinding(t.Context(), "team-a", "nightly", binding.ScheduledRunUID)
	require.NoError(t, err)
	require.Equal(t, binding, stored)
	_, err = client.GetScheduledRunBinding(t.Context(), "other-team", "nightly", binding.ScheduledRunUID)
	require.ErrorIs(t, err, ErrNotFound)
	replacement := *binding
	replacement.ScheduledRunUID, replacement.BoundUserID = uuid.NewString(), "bob"
	require.NoError(t, client.CreateScheduledRunBinding(t.Context(), &replacement))
	empty := replacement
	empty.ScheduledRunUID, empty.BoundUserID = uuid.NewString(), ""
	require.Error(t, client.CreateScheduledRunBinding(t.Context(), &empty))
	empty.BoundUserID = auth.ScheduledRunUserID
	require.Error(t, client.CreateScheduledRunBinding(t.Context(), &empty), "reserved scheduler identity cannot become a user binding")
}

func TestScheduledExecutionRequestLookupDoesNotMatchAnotherOwner(t *testing.T) {
	client := NewClient(setupTestDB(t))
	agentInstanceFixture(t, client, t.Context(), "team-a", "request-revision", "assistant", "kagent")
	requestID := uuid.NewString()
	execution := pendingScheduledExecution(requestID, uuid.NewString(), time.Now())
	execution.UserID = "bob"
	_, _, err := client.CreateScheduledRunExecution(t.Context(), execution)
	require.NoError(t, err)
	input := newAgentInstanceRequest(uuid.NewString(), "assistant", "kagent", "")
	input.Creator = "alice"
	instance, _, err := client.CreateAgentInstance(t.Context(), input, requestID)
	require.NoError(t, err)
	_, err = client.GetScheduledRunExecutionByAgentInstanceID(t.Context(), instance.GetId())
	require.ErrorIs(t, err, ErrNotFound)
	listed, err := client.ListAgentInstances(t.Context(), AgentInstanceQuery{UserID: "alice", ExcludeScheduledRuns: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, instance.GetId(), listed[0].GetId())
}

func TestScheduledRunExecutionOwnerIsAnImmutableSnapshot(t *testing.T) {
	client := NewClient(setupTestDB(t))
	request := pendingScheduledExecution(uuid.NewString(), uuid.NewString(), time.Now())
	request.UserID = "alice"
	stored, _, err := client.CreateScheduledRunExecution(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, "alice", stored.UserID)
	request.UserID = "bob"
	_, _, err = client.CreateScheduledRunExecution(t.Context(), request)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	stored.UserID = "bob"
	require.ErrorIs(t, client.UpdateScheduledRunExecution(t.Context(), stored), ErrScheduledRunExecutionConflict)
	stored, err = client.GetScheduledRunExecution(t.Context(), request.ID)
	require.NoError(t, err)
	require.Equal(t, "alice", stored.UserID)
	invalid := pendingScheduledExecution(uuid.NewString(), uuid.NewString(), time.Now())
	invalid.UserID = ""
	_, _, err = client.CreateScheduledRunExecution(t.Context(), invalid)
	require.Error(t, err, "the store must reject an omitted execution owner")
}

func TestScheduledRunExecutionRecoveryAndTransitions(t *testing.T) {
	pool := setupTestDB(t)
	client := NewClient(pool)
	ctx := context.Background()
	input := pendingScheduledExecution(uuid.NewString(), uuid.NewString(), time.Now())
	created, fresh, err := client.CreateScheduledRunExecution(ctx, input)
	require.NoError(t, err)
	require.True(t, fresh)
	require.Equal(t, ScheduledRunExecutionPhaseCreating, created.Phase)
	require.Equal(t, v1alpha3.ScheduledRunExecutionStatus_InProgress, created.Status)

	// Reconstruct the store, as a new leader does after restart. A duplicate
	// request must recover the same immutable prompt/deadline rather than reset it.
	restarted := NewClient(pool)
	replay, fresh, err := restarted.CreateScheduledRunExecution(ctx, input)
	require.NoError(t, err)
	require.False(t, fresh)
	require.Equal(t, created.ID, replay.ID)
	conflicting := *input
	conflicting.Prompt = "Different prompt"
	_, _, err = restarted.CreateScheduledRunExecution(ctx, &conflicting)
	require.ErrorIs(t, err, ErrIdempotencyConflict)

	replay.AgentInstanceID = uuid.NewString()
	replay.Phase = ScheduledRunExecutionPhaseDispatching
	require.NoError(t, restarted.UpdateScheduledRunExecution(ctx, replay))
	byInstance, err := restarted.GetScheduledRunExecutionByAgentInstanceID(ctx, replay.AgentInstanceID)
	require.NoError(t, err)
	require.Equal(t, input.ScheduledRunUID, byInstance.ScheduledRunUID)
	require.Equal(t, input.Prompt, byInstance.Prompt)

	replay.TaskID = "task-1"
	replay.Phase = ScheduledRunExecutionPhasePolling
	require.NoError(t, restarted.UpdateScheduledRunExecution(ctx, replay))
	regressed := *replay
	regressed.Phase = ScheduledRunExecutionPhaseDispatching
	require.ErrorIs(t, restarted.UpdateScheduledRunExecution(ctx, &regressed), ErrScheduledRunExecutionConflict)
	moved := *replay
	moved.AgentInstanceID = uuid.NewString()
	require.ErrorIs(t, restarted.UpdateScheduledRunExecution(ctx, &moved), ErrScheduledRunExecutionConflict)

	now := time.Now()
	replay.Phase = ScheduledRunExecutionPhaseComplete
	replay.Status = v1alpha3.ScheduledRunExecutionStatus_Succeeded
	replay.CompletionTime = &now
	require.NoError(t, restarted.UpdateScheduledRunExecution(ctx, replay))
	require.NoError(t, restarted.UpdateScheduledRunExecution(ctx, replay), "retry after a lost commit response is idempotent")
	replay.Status = v1alpha3.ScheduledRunExecutionStatus_Failed
	require.ErrorIs(t, restarted.UpdateScheduledRunExecution(ctx, replay), ErrScheduledRunExecutionConflict)
	active, err := restarted.ListInProgressScheduledRunExecutions(ctx)
	require.NoError(t, err)
	for _, execution := range active {
		require.NotEqual(t, input.ID, execution.ID)
	}
}

func TestScheduledRunExecutionHistoryPaginationAndOwnerUID(t *testing.T) {
	client := NewClient(setupTestDB(t))
	ctx := context.Background()
	start := time.Now().Truncate(time.Microsecond)
	uid := uuid.NewString()
	ids := []string{uuid.NewString() + "-a", uuid.NewString() + "-b", uuid.NewString() + "-c"}
	for _, id := range ids {
		_, _, err := client.CreateScheduledRunExecution(ctx, pendingScheduledExecution(id, uid, start))
		require.NoError(t, err)
	}
	// A replacement CR with the same namespace/name must not inherit history.
	_, _, err := client.CreateScheduledRunExecution(ctx, pendingScheduledExecution(uuid.NewString(), "replacement-"+uid, start))
	require.NoError(t, err)
	first, err := client.ListScheduledRunExecutions(ctx, "team-a", "nightly", uid, ScheduledRunExecutionQuery{Limit: 2})
	require.NoError(t, err)
	require.Len(t, first, 2)
	last := first[1]
	second, err := client.ListScheduledRunExecutions(ctx, "team-a", "nightly", uid, ScheduledRunExecutionQuery{Limit: 2, Before: last.StartTime, BeforeID: last.ID})
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.NotEqual(t, first[0].ID, second[0].ID)
	require.NotEqual(t, first[1].ID, second[0].ID)
	require.Equal(t, uid, second[0].ScheduledRunUID)
}

func TestScheduledRunExecutionRejectsIncompletePollingTransition(t *testing.T) {
	client := NewClient(setupTestDB(t))
	execution, _, err := client.CreateScheduledRunExecution(t.Context(), pendingScheduledExecution(uuid.NewString(), uuid.NewString(), time.Now()))
	require.NoError(t, err)
	execution.Phase = ScheduledRunExecutionPhasePolling
	require.Error(t, client.UpdateScheduledRunExecution(t.Context(), execution))
	stored, err := client.GetScheduledRunExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	require.Equal(t, ScheduledRunExecutionPhaseCreating, stored.Phase)
	require.Empty(t, stored.AgentInstanceID)
}

func TestListAgentInstancesExcludesScheduledExecutionsBeforePagination(t *testing.T) {
	client := NewClient(setupTestDB(t))
	ctx := t.Context()
	agentInstanceFixture(t, client, ctx, "team-a", "list-revision", "assistant", "kagent")
	fixtures := []struct{ id, owner string }{
		{"11111111-1111-4111-8111-111111111111", "alice"},
		{"22222222-2222-4222-8222-222222222222", "alice"},
		{"33333333-3333-4333-8333-333333333333", "bob"},
		{"44444444-4444-4444-8444-444444444444", auth.ScheduledRunUserID},
		{"55555555-5555-4555-8555-555555555555", "alice"},
	}
	for _, fixture := range fixtures {
		instance := newAgentInstanceRequest(fixture.id, "assistant", "kagent", "")
		instance.Creator = fixture.owner
		_, created, err := client.CreateAgentInstance(ctx, instance, fixture.id)
		require.NoError(t, err)
		require.True(t, created)
		if fixture.id == fixtures[1].id || fixture.id == fixtures[3].id {
			execution := pendingScheduledExecution(fixture.id, uuid.NewString(), time.Now())
			execution.UserID = fixture.owner
			stored, _, err := client.CreateScheduledRunExecution(ctx, execution)
			require.NoError(t, err)
			if fixture.id == fixtures[3].id {
				stored.AgentInstanceID, stored.Phase = fixture.id, ScheduledRunExecutionPhaseDispatching
				require.NoError(t, client.UpdateScheduledRunExecution(ctx, stored))
			}
			matched, err := client.GetScheduledRunExecutionByAgentInstanceID(ctx, fixture.id)
			require.NoError(t, err, "both reserved and linked scheduled instances are discoverable")
			require.Equal(t, fixture.owner, matched.UserID)
		}
	}
	query := AgentInstanceQuery{AllUsers: true, ExcludeScheduledRuns: true, Limit: 2}
	first, err := client.ListAgentInstances(ctx, query)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, fixtures[0].id, first[0].GetId())
	require.Equal(t, fixtures[2].id, first[1].GetId())
	query.AfterID = first[1].GetId()
	second, err := client.ListAgentInstances(ctx, query)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, fixtures[4].id, second[0].GetId())
	// Internal consumers can still explicitly inspect all records.
	all, err := client.ListAgentInstances(ctx, AgentInstanceQuery{AllUsers: true, Limit: 10})
	require.NoError(t, err)
	require.Len(t, all, 5)
}

func TestAgentInstanceRecoveryByRequestIDDoesNotCreateAndScopesOwner(t *testing.T) {
	client := NewClient(setupTestDB(t))
	ctx := t.Context()
	agentInstanceFixture(t, client, ctx, "team-a", "recover-revision", "assistant", "kagent")
	_, err := client.GetAgentInstanceByRequestID(ctx, auth.ScheduledRunUserID, "execution-1")
	require.ErrorIs(t, err, ErrNotFound)
	input := newAgentInstanceRequest("11111111-1111-4111-8111-111111111111", "assistant", "kagent", "")
	input.Creator = auth.ScheduledRunUserID
	instance, _, err := client.CreateAgentInstance(ctx, input, "execution-1")
	require.NoError(t, err)
	recovered, err := client.GetAgentInstanceByRequestID(ctx, auth.ScheduledRunUserID, "execution-1")
	require.NoError(t, err)
	require.Equal(t, instance.GetId(), recovered.GetId())
	require.Equal(t, instance.GetState(), recovered.GetState())
	_, err = client.GetAgentInstanceByRequestID(ctx, "alice", "execution-1")
	require.ErrorIs(t, err, ErrNotFound)
}
