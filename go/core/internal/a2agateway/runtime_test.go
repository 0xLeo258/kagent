package a2agateway

import (
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"testing"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	adka2a "github.com/kagent-dev/kagent/go/adk/pkg/a2a"
	adkauth "github.com/kagent-dev/kagent/go/adk/pkg/auth"
	"github.com/kagent-dev/kagent/go/adk/pkg/models"
	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/stretchr/testify/require"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type runtimeRecordingAuth struct {
	auth.AuthProvider
	session auth.Session
}

func (a *runtimeRecordingAuth) UpstreamAuth(req *http.Request, session auth.Session, principal auth.Principal) error {
	a.session = session
	return a.AuthProvider.UpstreamAuth(req, session, principal)
}

func TestRuntimeOwnerPreservesADKConversationAcrossCalls(t *testing.T) {
	const owner = "alice"
	const appName = "history-agent"
	instance := &apiv1alpha1.AgentInstance{Id: "instance-1", Creator: owner}
	sessions := adksession.InMemoryService()
	var histories [][]string
	var tokens []any
	agent, err := adkagent.New(adkagent.Config{
		Name: appName,
		Run: func(ic adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				if got := ic.Session().UserID(); got != owner {
					yield(nil, fmt.Errorf("ADK session owner = %q, want %q", got, owner))
					return
				}
				if got := adkauth.UserIDFromContext(ic); got != owner {
					yield(nil, fmt.Errorf("ADK memory owner = %q, want %q", got, owner))
					return
				}
				histories = append(histories, runtimeSessionTexts(ic.Session()))
				tokens = append(tokens, ic.Value(models.BearerTokenKey))
				yield(&adksession.Event{
					Author: ic.Agent().Name(), InvocationID: ic.InvocationID(), Branch: ic.Branch(),
					LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("remembered", genai.RoleModel)},
				}, nil)
			}
		},
	})
	require.NoError(t, err)
	executor := adka2a.NewKAgentExecutor(adka2a.KAgentExecutorConfig{
		AppName: appName, SessionService: sessions, Logger: slog.New(slog.DiscardHandler),
		RunnerConfig: runner.Config{AppName: appName, Agent: agent},
	})
	const token = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJhbGljZSJ9.signature"
	provider := authimpl.NewProxyAuthenticator("sub")
	ownerSession, err := provider.Authenticate(t.Context(), http.Header{"Authorization": {"Bearer " + token}}, nil)
	require.NoError(t, err)
	recording := &runtimeRecordingAuth{AuthProvider: provider}
	interceptor := &upstreamAuthInterceptor{authenticator: recording, instance: instance}
	// The scheduler calls as the system principal; the bound owner later
	// continues the same session using their own credentials.
	calls := []struct {
		session auth.Session
		text    string
	}{
		{&authimpl.SimpleSession{P: auth.Principal{User: auth.User{ID: auth.ScheduledRunUserID}}}, "initial scheduled prompt"},
		{ownerSession, "continue my conversation"},
	}
	for index, call := range calls {
		ctx := auth.AuthSessionTo(t.Context(), call.session)
		req := &a2aclient.Request{BaseURL: "runtime.test", ServiceParams: make(a2aclient.ServiceParams)}
		forwardedCtx, _, err := interceptor.Before(ctx, req)
		require.NoError(t, err)
		require.Same(t, call.session, recording.session)
		forwardedSession, ok := auth.AuthSessionFrom(forwardedCtx)
		require.True(t, ok)
		require.Same(t, call.session, forwardedSession)
		// Rebuild the runtime context from wire metadata: controller context
		// values do not cross the A2A transport boundary.
		runtimeCtx, callCtx := a2asrv.NewCallContext(t.Context(), a2asrv.NewServiceParams(req.ServiceParams))
		runtimeCtx, _, err = adka2a.UserIDCallInterceptor().Before(runtimeCtx, callCtx, nil)
		require.NoError(t, err)
		request := &a2asrv.ExecutorContext{
			TaskID: a2atype.TaskID(fmt.Sprint("task-", index)), ContextID: instance.Id,
			Message: a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart(call.text)),
		}
		for _, err := range executor.Execute(runtimeCtx, request) {
			require.NoError(t, err)
		}
	}
	require.Equal(t, [][]string{
		{"initial scheduled prompt"},
		{"initial scheduled prompt", "remembered", "continue my conversation"},
	}, histories)
	require.Equal(t, []any{nil, token}, tokens)
}

func runtimeSessionTexts(session adksession.Session) []string {
	var texts []string
	for event := range session.Events().All() {
		if event.Content != nil {
			for _, part := range event.Content.Parts {
				if part.Text != "" {
					texts = append(texts, part.Text)
				}
			}
		}
	}
	return texts
}
