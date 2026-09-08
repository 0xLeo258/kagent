package v1alpha3

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestScheduledRunCRDValidation(t *testing.T) {
	testEnv := &envtest.Environment{
		BinaryAssetsDirectory: envtestAssetsDir(t),
		CRDDirectoryPaths:     []string{crdBasesDir(t)}, ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testEnv.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, AddToScheme(scheme))
	kube, err := ctrlclient.New(cfg, ctrlclient.Options{Scheme: scheme})
	require.NoError(t, err)
	const namespace = "scheduled-run-validation"
	require.NoError(t, kube.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	newSchedule := func(name string) *ScheduledRun {
		return &ScheduledRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: ScheduledRunSpec{
				Schedule: "0 9 * * *", Prompt: "Summarize today's status.",
				TargetRef:  corev1.TypedLocalObjectReference{APIGroup: new("kagent.dev"), Kind: "AgentTemplate", Name: "assistant"},
				HarnessRef: corev1.LocalObjectReference{Name: "kagent"},
			},
		}
	}
	t.Run("defaults and mutable settings", func(t *testing.T) {
		sr := newSchedule("defaults")
		require.NoError(t, kube.Create(t.Context(), sr))
		require.Equal(t, "UTC", *sr.Spec.TimeZone)
		require.False(t, *sr.Spec.Suspended)
		require.Equal(t, 15*time.Minute, sr.Spec.ExecutionTimeout.Duration)
		require.EqualValues(t, 10, *sr.Spec.RecentExecutionsLimit)
		sr.Spec.Prompt = "Updated prompt"
		sr.Spec.Suspended = new(true)
		require.NoError(t, kube.Update(t.Context(), sr))
	})
	for _, tc := range []struct {
		name   string
		mutate func(*ScheduledRun)
	}{
		{"legacy-agent", func(sr *ScheduledRun) { sr.Spec.TargetRef.Kind = "Agent" }},
		{"missing-api-group", func(sr *ScheduledRun) { sr.Spec.TargetRef.APIGroup = nil }},
		{"invalid-target", func(sr *ScheduledRun) { sr.Spec.TargetRef.Name = "bad..name" }},
		{"invalid-harness", func(sr *ScheduledRun) { sr.Spec.HarnessRef.Name = "bad..name" }},
		{"missing-harness", func(sr *ScheduledRun) { sr.Spec.HarnessRef.Name = "" }},
		{"six-field-cron", func(sr *ScheduledRun) { sr.Spec.Schedule = "0 0 9 * * *" }},
		{"blank-prompt", func(sr *ScheduledRun) { sr.Spec.Prompt = " \n " }},
		{"oversized-prompt", func(sr *ScheduledRun) { sr.Spec.Prompt = strings.Repeat("x", 32769) }},
		{"zero-timeout", func(sr *ScheduledRun) { sr.Spec.ExecutionTimeout = &metav1.Duration{} }},
		{"negative-timeout", func(sr *ScheduledRun) { sr.Spec.ExecutionTimeout = &metav1.Duration{Duration: -time.Second} }},
		{"history-limit", func(sr *ScheduledRun) { sr.Spec.RecentExecutionsLimit = new(int32(101)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := newSchedule(tc.name)
			tc.mutate(sr)
			err := kube.Create(t.Context(), sr)
			require.True(t, apierrors.IsInvalid(err), "expected admission rejection, got %v", err)
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ScheduledRun)
	}{
		{"immutable-target", func(sr *ScheduledRun) { sr.Spec.TargetRef.Name = "different" }},
		{"immutable-harness", func(sr *ScheduledRun) { sr.Spec.HarnessRef.Name = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := newSchedule(tc.name)
			require.NoError(t, kube.Create(t.Context(), sr))
			tc.mutate(sr)
			err := kube.Update(t.Context(), sr)
			require.True(t, apierrors.IsInvalid(err), "expected immutable field rejection, got %v", err)
		})
	}
}
