package scheduledrun

import (
	"context"
	"errors"
	"testing"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	dbpkg "github.com/kagent-dev/kagent/go/core/internal/database"
	authimpl "github.com/kagent-dev/kagent/go/core/internal/httpserver/auth"
	"github.com/kagent-dev/kagent/go/core/internal/service/serviceerrors"
	"github.com/kagent-dev/kagent/go/core/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type accessStore struct {
	store
	execution *dbpkg.ScheduledRunExecution
	err       error
}

func (s *accessStore) GetScheduledRunExecutionByAgentInstanceID(context.Context, string) (*dbpkg.ScheduledRunExecution, error) {
	return s.execution, s.err
}

type accessAuthorizer struct {
	err       error
	principal auth.Principal
	resource  auth.Resource
	verb      auth.Verb
}

func (a *accessAuthorizer) Check(_ context.Context, principal auth.Principal, verb auth.Verb, resource auth.Resource) error {
	a.principal, a.verb, a.resource = principal, verb, resource
	return a.err
}

func TestResolveInstanceAccessUsesLiveSchedule(t *testing.T) {
	const instanceID = "01993019-e480-7412-96fb-f239f1f000c1"
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	run := &v1alpha3.ScheduledRun{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "daily", UID: "original"}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	authorizer := &accessAuthorizer{}
	s := &Service{kube: kube, authorizer: authorizer, store: &accessStore{execution: &dbpkg.ScheduledRunExecution{
		AgentInstanceID: instanceID, ScheduledRunNamespace: "team", ScheduledRunName: "daily", ScheduledRunUID: "original",
	}}}
	ctx := auth.AuthSessionTo(t.Context(), &authimpl.SimpleSession{P: auth.Principal{User: auth.User{ID: "reader"}}})
	access, err := s.ResolveInstanceAccess(ctx, instanceID)
	require.NoError(t, err)
	require.NotNil(t, access)
	assert.True(t, access.ReadOnly)
	assert.Equal(t, auth.ScheduledRunUserID, access.UserID)
	assert.Equal(t, "reader", authorizer.principal.User.ID)
	assert.Equal(t, auth.VerbGet, authorizer.verb)
	assert.Equal(t, auth.Resource{Type: "ScheduledRun", Name: "team/daily"}, authorizer.resource)

	// Permission changes apply to existing conversations on the next call.
	key := types.NamespacedName{Namespace: "team", Name: "daily"}
	require.NoError(t, kube.Get(ctx, key, run))
	run.Spec.AllowSessionInteraction = new(true)
	require.NoError(t, kube.Update(ctx, run))
	access, err = s.ResolveInstanceAccess(ctx, instanceID)
	require.NoError(t, err)
	assert.False(t, access.ReadOnly)
	authorizer.err = errors.New("access revoked")
	_, err = s.ResolveInstanceAccess(ctx, instanceID)
	assert.Equal(t, serviceerrors.CodePermissionDenied, serviceerrors.CodeOf(err))
	authorizer.err = nil

	// Reusing the name cannot transfer an earlier schedule's conversations.
	require.NoError(t, kube.Delete(ctx, run))
	run.ResourceVersion = ""
	run.UID = "replacement"
	require.NoError(t, kube.Create(ctx, run))
	_, err = s.ResolveInstanceAccess(ctx, instanceID)
	assert.Equal(t, serviceerrors.CodeNotFound, serviceerrors.CodeOf(err))
	require.NoError(t, kube.Delete(ctx, run))
	_, err = s.ResolveInstanceAccess(ctx, instanceID)
	assert.Equal(t, serviceerrors.CodeNotFound, serviceerrors.CodeOf(err))
}

func TestResolveInstanceAccessOrdinaryAndUnavailableStore(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code serviceerrors.Code
	}{
		{"ordinary instance", dbpkg.ErrNotFound, ""},
		{"database unavailable", errors.New("connection lost"), serviceerrors.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{store: &accessStore{err: tt.err}}
			access, err := s.ResolveInstanceAccess(t.Context(), "01993019-e480-7412-96fb-f239f1f000c1")
			assert.Nil(t, access)
			assert.Equal(t, tt.code, serviceerrors.CodeOf(err))
		})
	}
}
