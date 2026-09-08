package scheduledrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kagent-dev/kagent/go/api/v1alpha3"
	"github.com/kagent-dev/kagent/go/pkg/logging"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	summaryRecheckInterval = 30 * time.Second
	acceptedReason         = "ScheduleAccepted"
	invalidSpecReason      = "InvalidSpec"
	targetNotFoundReason   = "TargetNotFound"
	bindingPendingReason   = "BindingPending"
)

// Controller validates references and keeps cron registration and the durable
// execution summary synchronized with each ScheduledRun's current generation.
type Controller struct{ scheduler *Scheduler }

var _ reconcile.Reconciler = (*Controller)(nil)

func NewController(scheduler *Scheduler) *Controller {
	return &Controller{scheduler: scheduler}
}

// +kubebuilder:rbac:groups=kagent.dev,resources=scheduledruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kagent.dev,resources=scheduledruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kagent.dev,resources=agenttemplates;harnesses,verbs=get;list;watch

func (c *Controller) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var sr v1alpha3.ScheduledRun
	if err := c.scheduler.kube.Get(ctx, request.NamespacedName, &sr); err != nil {
		if apierrors.IsNotFound(err) {
			c.scheduler.RemoveSchedule(request.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get ScheduledRun %s: %w", request.NamespacedName, err)
	}
	if !sr.DeletionTimestamp.IsZero() {
		c.scheduler.RemoveSchedule(request.NamespacedName)
		return ctrl.Result{}, nil
	}
	previous := sr.DeepCopy().Status
	condition := metav1.Condition{
		Type: v1alpha3.ScheduledRunConditionTypeAccepted, Status: metav1.ConditionTrue,
		Reason: acceptedReason, Message: "ScheduledRun is accepted", ObservedGeneration: sr.Generation,
	}

	parsed, err := parseSchedule(sr.Spec)
	if err != nil {
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, invalidSpecReason, err.Error()
	} else if err := validateTargets(ctx, c.scheduler.kube, &sr); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, targetNotFoundReason, err.Error()
	}
	sr.Status.NextExecutionTime = nil
	if condition.Status == metav1.ConditionTrue {
		if err := c.scheduler.updateSchedule(ctx, &sr, parsed); errors.Is(err, errBindingPending) {
			condition.Status, condition.Reason, condition.Message = metav1.ConditionFalse, bindingPendingReason, err.Error()
		} else if err != nil {
			return ctrl.Result{}, err
		} else if !isSuspended(&sr) {
			next := metav1.NewTime(parsed.Next(time.Now()))
			sr.Status.NextExecutionTime = &next
		}
	} else {
		c.scheduler.RemoveSchedule(request.NamespacedName)
	}
	summary, err := c.scheduler.executionSummary(ctx, &sr)
	if err != nil {
		return ctrl.Result{}, err
	}
	sr.Status.RecentExecutions = summary
	updateLastExecutionTime(&sr)
	meta.SetStatusCondition(&sr.Status.Conditions, condition)
	sr.Status.ObservedGeneration = sr.Generation
	if !equality.Semantic.DeepEqual(previous, sr.Status) {
		if err := c.scheduler.kube.Status().Update(ctx, &sr); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update ScheduledRun %s status: %w", request.NamespacedName, err)
		}
	}
	return ctrl.Result{RequeueAfter: summaryRecheckInterval}, nil
}

func validateTargets(ctx context.Context, kube client.Client, sr *v1alpha3.ScheduledRun) error {
	for _, target := range []client.Object{&v1alpha3.AgentTemplate{}, &v1alpha3.Harness{}} {
		name := sr.Spec.TargetRef.Name
		if _, harness := target.(*v1alpha3.Harness); harness {
			name = sr.Spec.HarnessRef.Name
		}
		key := client.ObjectKey{Namespace: sr.Namespace, Name: name}
		if err := kube.Get(ctx, key, target); err != nil {
			return fmt.Errorf("failed to resolve %T %s: %w", target, key, err)
		}
	}
	return nil
}

func (c *Controller) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&v1alpha3.ScheduledRun{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Watches(&v1alpha3.AgentTemplate{}, handler.EnqueueRequestsFromMapFunc(c.schedulesForTarget), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha3.Harness{}, handler.EnqueueRequestsFromMapFunc(c.schedulesForTarget), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controllerconfig.Options{NeedLeaderElection: new(true)}).
		Named("scheduledrun").
		Complete(c)
}

func (c *Controller) schedulesForTarget(ctx context.Context, target client.Object) []reconcile.Request {
	var list v1alpha3.ScheduledRunList
	if err := c.scheduler.kube.List(ctx, &list, client.InNamespace(target.GetNamespace())); err != nil {
		logging.FromContext(ctx).ErrorContext(ctx, "failed to list schedules for target", "error", err)
		return nil
	}
	var requests []reconcile.Request
	for _, sr := range list.Items {
		name := sr.Spec.TargetRef.Name
		if _, harness := target.(*v1alpha3.Harness); harness {
			name = sr.Spec.HarnessRef.Name
		}
		if name == target.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&sr)})
		}
	}
	return requests
}
