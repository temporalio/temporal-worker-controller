package controller

import (
	"context"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// deprecatedTWDMigratedLabel is set on a TemporalWorkerDeployment once its
	// ownership has been transferred to the corresponding WorkerDeployment.
	deprecatedTWDMigratedLabel = "temporal.io/migrated-to-wd"
)

// DeprecatedTWDReconciler reconciles TemporalWorkerDeployment stubs.
//
// IMPORTANT: this reconciler must NEVER call Temporal APIs or manage k8s
// Deployments. Doing so would race with the WorkerDeploymentReconciler for
// resources that share the same Temporal deployment name.
//
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/finalizers,verbs=update
type DeprecatedTWDReconciler struct {
	client.Client
}

func (r *DeprecatedTWDReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var twd temporaliov1alpha1.TemporalWorkerDeployment
	if err := r.Get(ctx, req.NamespacedName, &twd); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var (
		reason  string
		message string
	)

	if twd.Labels[deprecatedTWDMigratedLabel] == "true" {
		reason = "MigratedToWorkerDeployment"
		message = "Migration complete. Delete this TemporalWorkerDeployment."
	} else {
		// Check whether a WorkerDeployment with the same name exists.
		var wd temporaliov1alpha1.WorkerDeployment
		err := r.Get(ctx, req.NamespacedName, &wd)
		switch {
		case err == nil:
			reason = "WorkerDeploymentExists"
			message = "A WorkerDeployment with the same name exists. Migration is in progress."
		case apierrors.IsNotFound(err):
			reason = "Deprecated"
			message = "TemporalWorkerDeployment is deprecated. Create a WorkerDeployment with the same name and spec to migrate."
		default:
			return ctrl.Result{}, err
		}
	}

	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: twd.Generation,
	}
	setOrReplaceCondition(&twd.Status.Conditions, cond)

	if err := r.Status().Update(ctx, &twd); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// setOrReplaceCondition upserts a condition by Type.
func setOrReplaceCondition(conditions *[]metav1.Condition, cond metav1.Condition) {
	now := metav1.Now()
	for i, c := range *conditions {
		if c.Type == cond.Type {
			if c.Status != cond.Status || c.Reason != cond.Reason {
				cond.LastTransitionTime = now
			} else {
				cond.LastTransitionTime = c.LastTransitionTime
			}
			(*conditions)[i] = cond
			return
		}
	}
	cond.LastTransitionTime = now
	*conditions = append(*conditions, cond)
}

func (r *DeprecatedTWDReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&temporaliov1alpha1.TemporalWorkerDeployment{}).
		Named("deprecated-twd").
		Complete(r)
}
