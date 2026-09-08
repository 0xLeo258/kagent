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
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type bindingStore struct {
	store
	bindings  map[string]dbpkg.ScheduledRunBinding
	createErr error
	onCreate  func(*dbpkg.ScheduledRunBinding)
}

func (s *bindingStore) CreateScheduledRunBinding(_ context.Context, binding *dbpkg.ScheduledRunBinding) error {
	if s.onCreate != nil {
		s.onCreate(binding)
	}
	if s.createErr != nil {
		return s.createErr
	}
	s.bindings[binding.ScheduledRunUID] = *binding
	return nil
}
func (s *bindingStore) GetScheduledRunBinding(_ context.Context, namespace, name, uid string) (*dbpkg.ScheduledRunBinding, error) {
	binding, ok := s.bindings[uid]
	if !ok || binding.ScheduledRunNamespace != namespace || binding.ScheduledRunName != name {
		return nil, dbpkg.ErrNotFound
	}
	return &binding, nil
}

func TestCreateScheduledRunBindsAuthenticatedCreator(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bindingErr error
	}{
		{name: "success"},
		{name: "binding failure", bindingErr: errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha3.AddToScheme(scheme))
			kube := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					obj.SetUID("actual-created-uid")
					obj.SetGeneration(1)
					return c.Create(ctx, obj, opts...)
				},
			}).Build()
			db := &bindingStore{bindings: make(map[string]dbpkg.ScheduledRunBinding)}
			db.onCreate = func(binding *dbpkg.ScheduledRunBinding) {
				var pending v1alpha3.ScheduledRun
				require.NoError(t, kube.Get(t.Context(), types.NamespacedName{Namespace: "team", Name: "nightly"}, &pending))
				require.Contains(t, pending.Annotations, v1alpha3.ScheduledRunBindingRequiredAnnotation)
				require.Equal(t, "actual-created-uid", binding.ScheduledRunUID)
				require.Equal(t, "alice", binding.BoundUserID)
			}
			db.createErr = tc.bindingErr
			service := NewService(kube, &authimpl.NoopAuthorizer{}, db, nil)
			incoming := testScheduledRun()
			incoming.Annotations = map[string]string{"kagent.dev/bound-user-id": "mallory", v1alpha3.ScheduledRunBindingRequiredAnnotation: "forged"}
			created, err := service.Create(serviceContext(t), incoming)
			var persisted v1alpha3.ScheduledRun
			require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(incoming), &persisted))
			if tc.bindingErr != nil {
				require.Equal(t, serviceerrors.CodeInternal, serviceerrors.CodeOf(err))
				require.Contains(t, persisted.Annotations, v1alpha3.ScheduledRunBindingRequiredAnnotation)
				require.Empty(t, db.bindings)
				return
			}
			require.NoError(t, err)
			require.Contains(t, created.Annotations, v1alpha3.ScheduledRunBindingRequiredAnnotation)
			owner, err := service.BoundUserID(t.Context(), created)
			require.NoError(t, err)
			require.Equal(t, "alice", owner)
			require.Equal(t, "forged", incoming.Annotations[v1alpha3.ScheduledRunBindingRequiredAnnotation], "create must not mutate caller's object")
			edited := created.DeepCopy()
			edited.Spec.Prompt = "new prompt"
			edited.Annotations["kagent.dev/bound-user-id"] = "bob"
			bobCtx := auth.AuthSessionTo(t.Context(), &authimpl.SimpleSession{P: auth.Principal{User: auth.User{ID: "bob"}}})
			updated, err := service.Update(bobCtx, edited)
			require.NoError(t, err)
			owner, err = service.BoundUserID(t.Context(), updated)
			require.NoError(t, err)
			require.Equal(t, "alice", owner, "editing never changes the bound user")
			require.NoError(t, kube.Delete(t.Context(), updated))
			replacement := updated.DeepCopy()
			replacement.ObjectMeta = metav1.ObjectMeta{Namespace: updated.Namespace, Name: updated.Name, UID: "replacement"}
			// Read-only projection is scoped to UID, regardless of copied YAML metadata.
			owner, err = service.BoundUserID(t.Context(), replacement)
			require.NoError(t, err)
			require.Empty(t, owner)
		})
	}
}

func TestCreateScheduledRunRequiresHumanIdentity(t *testing.T) {
	for _, principal := range []auth.Principal{
		{}, {User: auth.User{ID: auth.ScheduledRunUserID}}, {User: auth.User{ID: "alice"}, Agent: auth.Agent{ID: "team/agent"}},
	} {
		service := NewService(nil, &authimpl.NoopAuthorizer{}, nil, nil)
		ctx := auth.AuthSessionTo(t.Context(), &authimpl.SimpleSession{P: principal})
		_, err := service.Create(ctx, testScheduledRun())
		require.Error(t, err)
	}
}
