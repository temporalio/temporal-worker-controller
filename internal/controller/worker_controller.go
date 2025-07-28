// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	apiGVStr = temporaliov1alpha1.GroupVersion.String()
)

const (
	// TODO(jlegrone): add this everywhere
	deployOwnerKey = ".metadata.controller"
	buildIDLabel   = "temporal.io/build-id"
	// TemporalWorkerDeploymentFinalizer is the finalizer used to ensure proper cleanup of resources
	TemporalWorkerDeploymentFinalizer = "temporal.io/temporal-worker-deployment-finalizer"
)

// TemporalWorkerDeploymentReconciler reconciles a TemporalWorkerDeployment object
type TemporalWorkerDeploymentReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	TemporalClientPool *clientpool.ClientPool

	// Disables panic recovery if true
	DisableRecoverPanic bool
}

//+kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/finalizers,verbs=update
//+kubebuilder:rbac:groups=temporal.io,resources=temporalconnections,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments/scale,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// The loop runs on a regular interval, or every time one of the watched resources listed above changes.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *TemporalWorkerDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// TODO(Shivam): Monitor if the time taken for a successful reconciliation loop is closing in on 5 minutes. If so, we
	// may need to increase the timeout value.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	l := log.FromContext(ctx)
	l.Info("Running Reconcile loop")

	// Fetch the worker deployment
	var workerDeploy temporaliov1alpha1.TemporalWorkerDeployment
	if err := r.Get(ctx, req.NamespacedName, &workerDeploy); err != nil {
		l.Error(err, "unable to fetch TemporalWorker")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if workerDeploy.ObjectMeta.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, l, &workerDeploy)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(&workerDeploy, TemporalWorkerDeploymentFinalizer) {
		controllerutil.AddFinalizer(&workerDeploy, TemporalWorkerDeploymentFinalizer)
		if err := r.Update(ctx, &workerDeploy); err != nil {
			l.Error(err, "unable to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue with normal reconciliation after adding finalizer
		return ctrl.Result{Requeue: true}, nil
	}

	// TODO(jlegrone): Set defaults via webhook rather than manually
	if err := workerDeploy.Default(ctx, &workerDeploy); err != nil {
		l.Error(err, "TemporalWorkerDeployment defaulter failed")
		return ctrl.Result{}, err
	}

	// TODO(carlydf): Handle warnings once we have some, handle ValidateUpdate once it is different from ValidateCreate
	if _, err := workerDeploy.ValidateCreate(ctx, &workerDeploy); err != nil {
		l.Error(err, "invalid TemporalWorkerDeployment")
		return ctrl.Result{
			Requeue:      true,
			RequeueAfter: 5 * time.Minute, // user needs time to fix this, if it changes, it will be re-queued immediately
		}, nil
	}

	// Verify that a connection is configured
	if workerDeploy.Spec.WorkerOptions.TemporalConnection == "" {
		err := fmt.Errorf("TemporalConnection must be set")
		l.Error(err, "")
		return ctrl.Result{}, err
	}

	// Fetch the connection parameters
	var temporalConnection temporaliov1alpha1.TemporalConnection
	if err := r.Get(ctx, types.NamespacedName{
		Name:      workerDeploy.Spec.WorkerOptions.TemporalConnection,
		Namespace: workerDeploy.Namespace,
	}, &temporalConnection); err != nil {
		l.Error(err, "unable to fetch TemporalConnection")
		return ctrl.Result{}, err
	}

	// Get or update temporal client for connection
	temporalClient, ok := r.TemporalClientPool.GetSDKClient(clientpool.ClientPoolKey{
		HostPort:        temporalConnection.Spec.HostPort,
		Namespace:       workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		MutualTLSSecret: temporalConnection.Spec.MutualTLSSecret,
	}, temporalConnection.Spec.MutualTLSSecret != "")
	if !ok {
		c, err := r.TemporalClientPool.UpsertClient(ctx, clientpool.NewClientOptions{
			K8sNamespace:      workerDeploy.Namespace,
			TemporalNamespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
			Spec:              temporalConnection.Spec,
		})
		if err != nil {
			l.Error(err, "unable to create TemporalClient")
			return ctrl.Result{}, err
		}
		temporalClient = c
	}

	// Fetch Temporal worker deployment state
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(&workerDeploy)
	temporalState, err := temporal.GetWorkerDeploymentState(
		ctx,
		temporalClient,
		workerDeploymentName,
		workerDeploy.Spec.WorkerOptions.TemporalNamespace,
	)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to get Temporal worker deployment state: %w", err)
	}

	// Compute a new status from k8s and temporal state
	status, err := r.generateStatus(ctx, l, temporalClient, req, &workerDeploy, temporalState)
	if err != nil {
		return ctrl.Result{}, err
	}
	workerDeploy.Status = *status
	if err := r.Status().Update(ctx, &workerDeploy); err != nil {
		// Ignore "object has been modified" errors, since we'll just re-fetch
		// on the next reconciliation loop.
		if apierrors.IsConflict(err) {
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: time.Second,
			}, nil
		}
		l.Error(err, "unable to update TemporalWorker status")
		return ctrl.Result{}, err
	}

	// TODO(jlegrone): Set defaults via webhook rather than manually
	//                 (defaults were already set above, but have to be set again after status update)
	if err := workerDeploy.Default(ctx, &workerDeploy); err != nil {
		l.Error(err, "TemporalWorkerDeployment defaulter failed")
		return ctrl.Result{}, err
	}

	// Generate a plan to get to desired spec from current status
	plan, err := r.generatePlan(ctx, l, &workerDeploy, temporalConnection.Spec, temporalState)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Execute the plan, handling any errors
	if err := r.executePlan(ctx, l, temporalClient, plan); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{
		Requeue: true,
		// TODO(jlegrone): Consider increasing this value if the only thing we need to check for is unreachable versions.
		RequeueAfter: 10 * time.Second,
		// For demo purposes only!
		//RequeueAfter: 1 * time.Second,
	}, nil
}

// handleDeletion handles the deletion process for TemporalWorkerDeployment resources
func (r *TemporalWorkerDeploymentReconciler) handleDeletion(ctx context.Context, l logr.Logger, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment) (ctrl.Result, error) {
	l.Info("Handling deletion of TemporalWorkerDeployment")

	if !controllerutil.ContainsFinalizer(workerDeploy, TemporalWorkerDeploymentFinalizer) {
		// Finalizer has already been removed, allow deletion to proceed
		return ctrl.Result{}, nil
	}

	// Clean up managed resources
	if err := r.cleanupManagedResources(ctx, l, workerDeploy); err != nil {
		l.Error(err, "Failed to cleanup managed resources")
		return ctrl.Result{}, err
	}

	// Remove the finalizer to allow deletion
	controllerutil.RemoveFinalizer(workerDeploy, TemporalWorkerDeploymentFinalizer)
	if err := r.Update(ctx, workerDeploy); err != nil {
		l.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	l.Info("Successfully removed finalizer, resource will be deleted")
	return ctrl.Result{}, nil
}

// cleanupManagedResources ensures all resources managed by this TemporalWorkerDeployment are properly cleaned up
func (r *TemporalWorkerDeploymentReconciler) cleanupManagedResources(ctx context.Context, l logr.Logger, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment) error {
	l.Info("Cleaning up managed resources")

	// List all deployments owned by this TemporalWorkerDeployment
	deploymentList := &appsv1.DeploymentList{}
	listOpts := &client.ListOptions{
		Namespace: workerDeploy.Namespace,
	}

	if err := r.List(ctx, deploymentList, listOpts); err != nil {
		return fmt.Errorf("failed to list deployments: %w", err)
	}

	// Filter deployments owned by this TemporalWorkerDeployment and delete them
	for _, deployment := range deploymentList.Items {
		if r.isOwnedByWorkerDeployment(&deployment, workerDeploy) {
			l.Info("Deleting managed deployment", "deployment", deployment.Name)
			if err := r.Delete(ctx, &deployment); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete deployment %s: %w", deployment.Name, err)
			}
		}
	}

	// Wait for all owned deployments to be deleted
	for _, deployment := range deploymentList.Items {
		if r.isOwnedByWorkerDeployment(&deployment, workerDeploy) {
			// Check if deployment still exists
			currentDeployment := &appsv1.Deployment{}
			err := r.Get(ctx, types.NamespacedName{
				Namespace: deployment.Namespace,
				Name:      deployment.Name,
			}, currentDeployment)

			if err == nil {
				// Deployment still exists, requeue to wait for deletion
				l.Info("Waiting for deployment to be deleted", "deployment", deployment.Name)
				return fmt.Errorf("still waiting for deployment %s to be deleted", deployment.Name)
			} else if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to check deployment status %s: %w", deployment.Name, err)
			}
			// IsNotFound error means deployment was successfully deleted
		}
	}

	l.Info("All managed resources have been cleaned up")
	return nil
}

// isOwnedByWorkerDeployment checks if a deployment is owned by the given TemporalWorkerDeployment
func (r *TemporalWorkerDeploymentReconciler) isOwnedByWorkerDeployment(deployment *appsv1.Deployment, workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment) bool {
	for _, ownerRef := range deployment.OwnerReferences {
		if ownerRef.Kind == "TemporalWorkerDeployment" &&
			ownerRef.APIVersion == apiGVStr &&
			ownerRef.Name == workerDeploy.Name &&
			ownerRef.UID == workerDeploy.UID {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *TemporalWorkerDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &appsv1.Deployment{}, deployOwnerKey, func(rawObj client.Object) []string {
		// grab the job object, extract the owner...
		deploy := rawObj.(*appsv1.Deployment)
		owner := metav1.GetControllerOf(deploy)

		if owner == nil {
			return nil
		}
		// ...make sure it's a TemporalWorker...
		// TODO(jlegrone): double check apiGVStr has the correct value
		if owner.APIVersion != apiGVStr || owner.Kind != "TemporalWorkerDeployment" {
			return nil
		}

		// ...and if so, return it
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	recoverPanic := !r.DisableRecoverPanic
	return ctrl.NewControllerManagedBy(mgr).
		For(&temporaliov1alpha1.TemporalWorkerDeployment{}).
		Owns(&appsv1.Deployment{}).
		Watches(&temporaliov1alpha1.TemporalConnection{}, handler.EnqueueRequestsFromMapFunc(r.findTWDsUsingConnection)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100,
			RecoverPanic:            &recoverPanic,
		}).
		Complete(r)
}

func (r *TemporalWorkerDeploymentReconciler) findTWDsUsingConnection(ctx context.Context, tc client.Object) []reconcile.Request {
	var requests []reconcile.Request

	// Find all TWDs in same namespace that reference this TC
	var twds temporaliov1alpha1.TemporalWorkerDeploymentList
	if err := r.List(ctx, &twds, client.InNamespace(tc.GetNamespace())); err != nil {
		return requests
	}

	// Filter to ones using this connection
	for _, twd := range twds.Items {
		if twd.Spec.WorkerOptions.TemporalConnection == tc.GetName() {
			// Enqueue a reconcile request for this TWD
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      twd.Name,
					Namespace: twd.Namespace,
				},
			})
		}
	}

	return requests
}
