// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/controller/clientpool"
)

var (
	apiGVStr = temporaliov1alpha1.GroupVersion.String()
)

const (
	// TODO(jlegrone): add this everywhere
	deployOwnerKey = ".metadata.controller"
	buildIDLabel   = "temporal.io/build-id"
)

// TemporalWorkerDeploymentReconciler reconciles a TemporalWorkerDeployment object
type TemporalWorkerDeploymentReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	TemporalClientPool *clientpool.ClientPool
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
// TODO(carlydf): Add watching of temporal connection custom resource (may have issue)
func (r *TemporalWorkerDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	// TODO(jlegrone): Set defaults via webhook rather than manually
	workerDeploy.Default()

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
		HostPort:  temporalConnection.Spec.HostPort,
		Namespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
	})
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

	// Compute a new status from k8s and temporal state
	status, err := r.generateStatus(ctx, l, temporalClient, req, &workerDeploy)
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
	workerDeploy.Default()

	// Generate a plan to get to desired spec from current status
	plan, err := r.generatePlan(ctx, l, &workerDeploy, temporalConnection.Spec)
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

	return ctrl.NewControllerManagedBy(mgr).
		For(&temporaliov1alpha1.TemporalWorkerDeployment{}).
		Owns(&appsv1.Deployment{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100,
		}).
		Complete(r)
}
