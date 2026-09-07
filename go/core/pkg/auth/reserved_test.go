package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type reservedTestSession string

func (s reservedTestSession) Principal() Principal { return Principal{User: User{ID: string(s)}} }

type reservedTestProvider struct{ session Session }

func (p reservedTestProvider) Authenticate(context.Context, http.Header, url.Values) (Session, error) {
	return p.session, nil
}

func (reservedTestProvider) UpstreamAuth(*http.Request, Session, Principal) error { return nil }

func TestAuthnMiddlewareRejectsExternalSchedulerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		user string
		want int
	}{
		{name: "ordinary caller", user: "alice", want: http.StatusNoContent},
		{name: "reserved scheduler caller", user: ScheduledRunUserID, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := AuthnMiddleware(reservedTestProvider{reservedTestSession(tc.user)})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				session, ok := AuthSessionFrom(r.Context())
				if !ok || session.Principal().User.ID != tc.user {
					t.Fatal("authenticated caller was not preserved")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d", response.Code, tc.want)
			}
		})
	}
}
