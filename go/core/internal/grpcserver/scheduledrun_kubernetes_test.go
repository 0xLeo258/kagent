package grpcserver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	apiv1alpha1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/structuredobject"
	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Exercise actual CRD generation and resourceVersion behavior; the fake client
// deliberately does not implement Kubernetes generation semantics.
func TestScheduledRunGRPCOptimisticUpdate(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	goRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		matches, err := filepath.Glob(filepath.Join(goRoot, "bin/k8s/*/kube-apiserver"))
		require.NoError(t, err)
		if len(matches) == 0 {
			t.Skip("envtest binaries unavailable; run make -C go setup-envtest or set KUBEBUILDER_ASSETS")
		}
		assets = filepath.Dir(matches[len(matches)-1])
	}
	testEnv := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDDirectoryPaths:     []string{filepath.Join(goRoot, "api/config/crd/bases/kagent.dev_scheduledruns.yaml")},
		ErrorIfCRDPathMissing: true,
	}
	config, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })
	scheme := k8sruntime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha3.AddToScheme(scheme))
	kube, err := ctrlclient.New(config, ctrlclient.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, kube.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}}))
	client := newScheduledRunConnectionWithKube(t, kube)
	ref := &apiv1alpha1.ResourceReference{Namespace: "team", Name: "nightly"}
	encode := func(resource *v1alpha3.ScheduledRun) *apiv1alpha1.StructuredObject {
		wire, err := structuredobject.FromGo(resource, v1alpha3.GroupVersion.String(), "ScheduledRun", DefaultMaxMessageSize)
		require.NoError(t, err)
		return wire
	}
	decode := func(resource *apiv1alpha1.ScheduledRun) *v1alpha3.ScheduledRun {
		var result v1alpha3.ScheduledRun
		require.NoError(t, structuredobject.ToGo(resource.GetResource(), "ScheduledRun", &result, DefaultMaxMessageSize))
		return &result
	}
	get := func() *v1alpha3.ScheduledRun {
		response, err := client.GetScheduledRun(t.Context(), &apiv1alpha1.GetScheduledRunRequest{Ref: ref})
		require.NoError(t, err)
		return decode(response.GetScheduledRun())
	}
	update := func(resource *v1alpha3.ScheduledRun) (*apiv1alpha1.UpdateScheduledRunResponse, error) {
		return client.UpdateScheduledRun(t.Context(), &apiv1alpha1.UpdateScheduledRunRequest{Ref: ref, Resource: encode(resource)})
	}
	_, err = client.CreateScheduledRun(t.Context(), &apiv1alpha1.CreateScheduledRunRequest{Ref: ref, Resource: encode(grpcScheduledRun())})
	require.NoError(t, err)

	first, second := get(), get()
	first.Spec.Prompt = "updated by first editor"
	saved, err := update(first)
	require.NoError(t, err)
	require.Equal(t, first.Generation+1, decode(saved.GetScheduledRun()).Generation)
	second.Spec.Suspended = new(true)
	_, err = update(second)
	require.Equal(t, codes.Aborted, status.Code(err))
	current := get()
	require.Equal(t, first.Spec.Prompt, current.Spec.Prompt)
	require.False(t, *current.Spec.Suspended)

	// Status updates advance resourceVersion but must not invalidate a spec draft.
	statusUpdate := current.DeepCopy()
	statusUpdate.Status.ObservedGeneration = statusUpdate.Generation
	require.NoError(t, kube.Status().Update(t.Context(), statusUpdate))
	require.NotEqual(t, current.ResourceVersion, statusUpdate.ResourceVersion)
	require.Equal(t, current.Generation, statusUpdate.Generation)
	current.Spec.Suspended = new(true)
	saved, err = update(current)
	require.NoError(t, err)
	persisted := decode(saved.GetScheduledRun())
	require.True(t, *persisted.Spec.Suspended)
	require.Equal(t, first.Spec.Prompt, persisted.Spec.Prompt)
	require.Equal(t, statusUpdate.Status, persisted.Status)

	// A same-name Kubernetes replacement must reject stale edits and cannot
	// inherit the previous API-created schedule's user binding.
	_, err = client.DeleteScheduledRun(t.Context(), &apiv1alpha1.DeleteScheduledRunRequest{Ref: ref})
	require.NoError(t, err)
	direct := grpcScheduledRun()
	direct.ObjectMeta = metav1.ObjectMeta{Namespace: ref.Namespace, Name: ref.Name, Annotations: map[string]string{"kagent.dev/bound-user-id": "admin@kagent.dev"}}
	require.NoError(t, kube.Create(t.Context(), direct))
	require.NotEqual(t, second.UID, direct.UID)
	require.Equal(t, second.Generation, direct.Generation)
	_, err = update(second)
	require.Equal(t, codes.Aborted, status.Code(err))
	fetched, err := client.GetScheduledRun(t.Context(), &apiv1alpha1.GetScheduledRunRequest{Ref: ref})
	require.NoError(t, err)
	require.Empty(t, fetched.GetScheduledRun().GetBoundUserId())
	direct.Spec.Prompt = "edited from API"
	response, err := client.UpdateScheduledRun(metadata.AppendToOutgoingContext(t.Context(), "x-user-id", "alice"), &apiv1alpha1.UpdateScheduledRunRequest{Ref: ref, Resource: encode(direct)})
	require.NoError(t, err)
	require.Empty(t, response.GetScheduledRun().GetBoundUserId())
}
