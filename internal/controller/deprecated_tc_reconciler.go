package controller

import (
	"context"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups=temporal.io,resources=temporalconnections,verbs=get;list;watch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalconnections/status,verbs=get;update;patch

// DeprecatedTCReconciler reconciles TemporalConnection stubs.
//
// IMPORTANT: this reconciler must NEVER call Temporal APIs or manage k8s
// resources. Its only job is to surface a status condition guiding users to
// migrate to the Connection CRD.
type DeprecatedTCReconciler struct {
	client.Client
}

func (r *DeprecatedTCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var tc temporaliov1alpha1.TemporalConnection
	if err := r.Get(ctx, req.NamespacedName, &tc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	var (
		reason  string
		message string
	)

	var conn temporaliov1alpha1.Connection
	err := r.Get(ctx, req.NamespacedName, &conn)
	switch {
	case err == nil:
		reason = "MigratedToConnection"
		message = "Migration complete. Delete this TemporalConnection."
	case apierrors.IsNotFound(err):
		reason = "Deprecated"
		message = "TemporalConnection is deprecated. Create a Connection with the same name and spec to migrate."
	default:
		return ctrl.Result{}, err
	}

	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: tc.Generation,
	}
	setOrReplaceCondition(&tc.Status.Conditions, cond)

	if err := r.Status().Update(ctx, &tc); err != nil && !apierrors.IsConflict(err) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *DeprecatedTCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&temporaliov1alpha1.TemporalConnection{}).
		Named("deprecated-tc").
		Complete(r)
}
