package a2agateway

import (
	"context"
	"iter"
	"strings"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/stretchr/testify/require"
)

type artifactStreamingRuntime struct {
	gatewayTestRuntime
	ready   chan struct{}
	release chan struct{}
}

func (r *artifactStreamingRuntime) SendStreamingMessage(_ context.Context, _ a2aclient.ServiceParams, request *a2atype.SendMessageRequest) iter.Seq2[a2atype.Event, error] {
	return func(yield func(a2atype.Event, error) bool) {
		task := a2atype.NewSubmittedTask(request.Message, request.Message)
		for index, text := range []string{"first", "second"} {
			if !yield(&a2atype.TaskArtifactUpdateEvent{
				TaskID: task.ID, ContextID: task.ContextID, Append: index > 0,
				Artifact: &a2atype.Artifact{ID: "result", Parts: a2atype.ContentParts{a2atype.NewTextPart(text)}},
			}, nil) {
				return
			}
		}
		close(r.ready)
		<-r.release
		yield(a2atype.NewStatusUpdateEvent(task, a2atype.TaskStateCompleted, nil), nil)
	}
}

func TestGatewayResubscriptionStartsWithAccumulatedTask(t *testing.T) {
	runtime := &artifactStreamingRuntime{ready: make(chan struct{}), release: make(chan struct{})}
	store := &gatewayTestStore{instance: gatewayTestInstance()}
	gateway := New(store, &gatewayTestAuthorizer{}, &gatewayTestDialer{client: gatewayTestClient(t, runtime)}, &gatewayTestWorkflow{}, gatewayTestURL)
	ctx, cancel := context.WithTimeout(gatewayTestContext(), 5*time.Second)
	t.Cleanup(cancel)
	stream := gateway.SendStreamingMessage(ctx, gatewayTestRequest())
	terminal := make(chan a2atype.TaskState, 1)
	go collectTerminalState(stream, terminal)
	t.Cleanup(func() {
		close(runtime.release)
		select {
		case state := <-terminal:
			require.Equal(t, a2atype.TaskStateCompleted, state)
		case <-time.After(5 * time.Second):
			t.Error("runtime did not finish after releasing the stream")
		}
	})
	select {
	case <-runtime.ready:
	case <-ctx.Done():
		t.Fatal("runtime did not publish artifact chunks")
	}
	observed := false
	for event, err := range gateway.SubscribeToTask(ctx, &a2atype.SubscribeToTaskRequest{ID: store.task.ID}) {
		require.NoError(t, err)
		snapshot, ok := event.(*a2atype.Task)
		require.True(t, ok, "a reconnect must receive a task, not a repeated append delta")
		require.Len(t, snapshot.Artifacts, 1)
		var parts []string
		for _, part := range snapshot.Artifacts[0].Parts {
			parts = append(parts, part.Text())
		}
		require.Equal(t, "firstsecond", strings.Join(parts, ""))
		observed = true
		break
	}
	require.True(t, observed)
}
