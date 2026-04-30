package controller

import (
	"context"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// deprecatedTWDMigratedLabel is set on a TemporalWorkerDeployment once its
	// ownership has been transferred to the corresponding WorkerDeployment.
	deprecatedTWDMigratedLabel = "temporal.io/migrated-to-wd"

	// deprecatedMigrationFinalizer is placed on TemporalWorkerDeployment and
	// TemporalConnection resources by the deprecated reconcilers. It prevents
	// garbage collection until migration to the new CRD kind is confirmed.
	// The deprecated reconcilers are the only actors that add or remove it.
	// This is intentionally distinct from the existing "temporal.io/delete-protection"
	// finalizer, which lives on WorkerDeployment/Connection and triggers Temporal
	// server-side cleanup — the two finalizers have unrelated lifecycles.
	deprecatedMigrationFinalizer = "temporal.io/migration-guard"
)

// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=temporal.io,resources=workerdeployments,verbs=get;list;watch

// DeprecatedTWDReconciler reconciles TemporalWorkerDeployment stubs.
//
// IMPORTANT: this reconciler must NEVER call Temporal APIs or manage k8s
// Deployments. Doing so would race with the WorkerDeploymentReconciler for
// resources that share the same Temporal deployment name.
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

	// Handle deletion: if the resource is being deleted and we hold the
	// migration-guard finalizer, only release it once migration is confirmed
	// (i.e. the WorkerDeploymentReconciler has set the migrated label).
	if !twd.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(&twd, deprecatedMigrationFinalizer) {
		if twd.Labels[deprecatedTWDMigratedLabel] == "true" {
			controllerutil.RemoveFinalizer(&twd, deprecatedMigrationFinalizer)
			if err := r.Update(ctx, &twd); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		// Migration not yet confirmed — block deletion and surface an actionable message.
		cond := metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "DeletingPendingMigration",
			Message:            "This TemporalWorkerDeployment is marked for deletion. Create a WorkerDeployment with the same name and spec to complete migration; deletion will proceed automatically once migration is confirmed.",
			ObservedGeneration: twd.Generation,
		}
		setOrReplaceCondition(&twd.Status.Conditions, cond)
		if err := r.Status().Update(ctx, &twd); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Ensure the migration-guard finalizer is present on live resources.
	if twd.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(&twd, deprecatedMigrationFinalizer) {
		controllerutil.AddFinalizer(&twd, deprecatedMigrationFinalizer)
		if err := r.Update(ctx, &twd); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
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
