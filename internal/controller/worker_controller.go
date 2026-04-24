// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"go.temporal.io/api/serviceerror"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
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

	// wrtWorkerRefKey is the field index key for WorkerResourceTemplate by temporalWorkerDeploymentRef.name.
	wrtWorkerRefKey = ".spec.temporalWorkerDeploymentRef.name"
)

// getAPIKeySecretName extracts the secret name from a SecretKeySelector
func getAPIKeySecretName(secretRef *corev1.SecretKeySelector) (string, error) {
	if secretRef != nil && secretRef.Name != "" {
		return secretRef.Name, nil
	}

	return "", errors.New("API key secret name is not set")
}

func getTLSSecretName(secretRef *temporaliov1alpha1.SecretReference) (string, error) {
	if secretRef != nil && secretRef.Name != "" {
		return secretRef.Name, nil
	}

	return "", errors.New("TLS secret name is not set")
}

func resolveAuthSecretName(tc *temporaliov1alpha1.TemporalConnection) (clientpool.AuthMode, string, error) {
	if tc.Spec.MutualTLSSecretRef != nil {
		name, err := getTLSSecretName(tc.Spec.MutualTLSSecretRef)
		return clientpool.AuthModeTLS, name, err
	} else if tc.Spec.APIKeySecretRef != nil {
		name, err := getAPIKeySecretName(tc.Spec.APIKeySecretRef)
		return clientpool.AuthModeAPIKey, name, err
	}
	return clientpool.AuthModeNoCredentials, "", nil
}

// TemporalWorkerDeploymentReconciler reconciles a TemporalWorkerDeployment object
type TemporalWorkerDeploymentReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	TemporalClientPool *clientpool.ClientPool
	Recorder           record.EventRecorder

	// Disables panic recovery if true
	DisableRecoverPanic bool

	// When a Worker Deployment has the maximum number of versions (100 per Worker Deployment by default),
	// it will delete the oldest eligible version when a worker with the 101st version arrives.
	// If no versions are eligible for deletion, that worker's poll will fail, which is dangerous.
	// To protect against this, when a Worker Deployment has too many versions ineligible for deletion,
	// the controller will stop deploying new workers in order to give the user the opportunity to adjust
	// their sunset policy to avoid this situation before it actually blocks deployment of a new worker
	// version on the server side.
	//
	// MaxDeploymentVersionsIneligibleForDeletion is currently defaulted to 75, which is safe for the default
	// server value of `matching.maxVersionsInDeployment=100`.
	// Users who reduce `matching.maxVersionsInDeployment` in their dynamicconfig should also reduce this value.
	MaxDeploymentVersionsIneligibleForDeletion int32
}

// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=temporal.io,resources=temporalconnections,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/scale,verbs=update
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=temporal.io,resources=workerresourcetemplates,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=temporal.io,resources=workerresourcetemplates/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

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
	l.V(1).Info("Running Reconcile loop")

	// Fetch the worker deployment
	var workerDeploy temporaliov1alpha1.TemporalWorkerDeployment
	if err := r.Get(ctx, req.NamespacedName, &workerDeploy); err != nil {
		if !apierrors.IsNotFound(err) {
			l.Error(err, "unable to fetch TemporalWorkerDeployment")
			return ctrl.Result{}, err
		}
		// TWD not found: set Ready=False on any WRTs that reference it so users get a
		// clear signal rather than a silent no-op. No requeue for the not-found itself —
		// the next reconcile fires naturally when the TWD is created. If the List or
		// status updates fail (transient API errors), return the error to requeue with backoff.
		return ctrl.Result{}, r.markWRTsTWDNotFound(ctx, req.NamespacedName)
	}

	// TODO(jlegrone): Set defaults via webhook rather than manually
	if err := workerDeploy.Default(ctx, &workerDeploy); err != nil {
		l.Error(err, "TemporalWorkerDeployment defaulter failed")
		return ctrl.Result{}, err
	}

	// Fallback validation for spec constraints the CRD schema cannot enforce (rampPercentage
	// ordering, gate input/inputFrom exclusivity). When the optional TWD webhook is disabled
	// these checks would otherwise go unreported; this surfaces them as a condition and event.
	if _, err := workerDeploy.ValidateCreate(ctx, &workerDeploy); err != nil {
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonInvalidSpec,
			fmt.Sprintf("Invalid TemporalWorkerDeployment spec: %v", err),
			err.Error())
		return ctrl.Result{}, nil
	}

	// Note: TemporalConnectionRef.Name is validated by webhook due to +kubebuilder:validation:Required

	// Fetch the connection parameters
	var temporalConnection temporaliov1alpha1.TemporalConnection
	if err := r.Get(ctx, types.NamespacedName{
		Name:      workerDeploy.Spec.WorkerOptions.TemporalConnectionRef.Name,
		Namespace: workerDeploy.Namespace,
	}, &temporalConnection); err != nil {
		l.Error(err, "unable to fetch TemporalConnection")
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonTemporalConnectionNotFound,
			fmt.Sprintf("Unable to fetch TemporalConnection %q: %v", workerDeploy.Spec.WorkerOptions.TemporalConnectionRef.Name, err),
			fmt.Sprintf("TemporalConnection %q not found: %v", workerDeploy.Spec.WorkerOptions.TemporalConnectionRef.Name, err))
		return ctrl.Result{}, err
	}

	// Get the Auth Mode and Secret Name
	authMode, secretName, err := resolveAuthSecretName(&temporalConnection)
	if err != nil {
		l.Error(err, "unable to resolve auth secret name")
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonAuthSecretInvalid,
			fmt.Sprintf("Unable to resolve auth secret from TemporalConnection %q: %v", temporalConnection.Name, err),
			fmt.Sprintf("Unable to resolve auth secret: %v", err))
		return ctrl.Result{}, err
	}

	// Get or update temporal client for connection
	temporalClient, ok := r.TemporalClientPool.GetSDKClient(clientpool.ClientPoolKey{
		HostPort:   temporalConnection.Spec.HostPort,
		Namespace:  workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		SecretName: secretName,
		AuthMode:   authMode,
	})
	if !ok {
		clientOpts, key, clientAuth, err := r.TemporalClientPool.ParseClientSecret(ctx, secretName, authMode, clientpool.NewClientOptions{
			K8sNamespace:      workerDeploy.Namespace,
			TemporalNamespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
			Spec:              temporalConnection.Spec,
			Identity:          getControllerIdentity(),
		})
		if err != nil {
			l.Error(err, "invalid Temporal auth secret")
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonAuthSecretInvalid,
				fmt.Sprintf("Invalid Temporal auth secret for %s:%s: %v", temporalConnection.Spec.HostPort, workerDeploy.Spec.WorkerOptions.TemporalNamespace, err),
				fmt.Sprintf("Invalid auth secret: %v", err))
			return ctrl.Result{}, err
		}

		c, err := r.TemporalClientPool.DialAndUpsertClient(*clientOpts, *key, *clientAuth)
		if err != nil {
			l.Error(err, "unable to create TemporalClient")
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonTemporalClientCreationFailed,
				fmt.Sprintf("Unable to create Temporal client for %s:%s: %v", temporalConnection.Spec.HostPort, workerDeploy.Spec.WorkerOptions.TemporalNamespace, err),
				fmt.Sprintf("Failed to connect to Temporal: %v", err))
			return ctrl.Result{}, err
		}
		temporalClient = c
	}

	workerDeploymentName := k8s.ComputeWorkerDeploymentName(&workerDeploy)
	targetBuildID := k8s.ComputeBuildID(&workerDeploy)

	// Fetch Kubernetes deployment state
	k8sState, err := k8s.GetDeploymentState(
		ctx,
		r.Client,
		req.Namespace,
		req.Name,
		workerDeploymentName,
	)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to get Kubernetes deployment state: %w", err)
	}

	// Fetch Temporal worker deployment state
	temporalState, err := temporal.GetWorkerDeploymentState(
		ctx,
		temporalClient,
		workerDeploymentName,
		workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		k8sState.Deployments,
		targetBuildID,
		workerDeploy.Spec.RolloutStrategy.Strategy,
		getControllerIdentity(),
	)
	if err != nil {
		var rateLimitErr *serviceerror.ResourceExhausted
		if errors.As(err, &rateLimitErr) {
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonTemporalStateFetchFailed,
				fmt.Sprintf("Rate limited fetching Temporal worker deployment state: %v", err),
				fmt.Sprintf("Rate limited by Temporal server: %v", err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonTemporalStateFetchFailed,
			fmt.Sprintf("Unable to get Temporal worker deployment state: %v", err),
			fmt.Sprintf("Failed to query Temporal worker deployment state: %v", err))
		return ctrl.Result{}, fmt.Errorf("unable to get Temporal worker deployment state: %w", err)
	}

	// Compute a new status from k8s and temporal state
	status, err := r.generateStatus(ctx, l, temporalClient, req, &workerDeploy, temporalState, k8sState)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Preserve conditions that were set during this reconciliation
	status.Conditions = workerDeploy.Status.Conditions
	workerDeploy.Status = *status

	// TODO(jlegrone): Set defaults via webhook rather than manually
	//                 (defaults were already set above, but have to be set again after status update)
	if err := workerDeploy.Default(ctx, &workerDeploy); err != nil {
		l.Error(err, "TemporalWorkerDeployment defaulter failed")
		return ctrl.Result{}, err
	}

	// Generate a plan to get to desired spec from current status
	plan, err := r.generatePlan(ctx, l, &workerDeploy, temporalConnection.Spec, temporalState)
	if err != nil {
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			ReasonPlanGenerationFailed,
			fmt.Sprintf("Unable to generate reconciliation plan: %v", err),
			fmt.Sprintf("Plan generation failed: %v", err))
		return ctrl.Result{}, err
	}

	// Execute the plan, handling any errors
	if err := r.executePlan(ctx, l, &workerDeploy, temporalClient, plan); err != nil {
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			ReasonPlanExecutionFailed,
			fmt.Sprintf("Unable to execute reconciliation plan: %v", err),
			fmt.Sprintf("Plan execution failed: %v", err))
		return ctrl.Result{}, err
	}

	// Derive Ready/Progressing from rollout state before the final write.
	r.syncConditions(&workerDeploy)

	// Single status write per reconcile: persists the generated status and
	// conditions set during this loop (Ready, Progressing).
	if err := r.Status().Update(ctx, &workerDeploy); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{
				Requeue:      true,
				RequeueAfter: time.Second,
			}, nil
		}
		l.Error(err, "unable to update TemporalWorker status")
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

// markWRTsTWDNotFound sets Ready=False/TWDNotFound on all WorkerResourceTemplates that reference
// a TemporalWorkerDeployment that could not be found. This covers the case where the WRT is
// created before the TWD exists, or where the TWD was deleted before the controller set an owner
// reference on the WRT (which would otherwise cause Kubernetes GC to delete the WRT).
// Returns an error if the List or any status update fails so the caller can requeue.
func (r *TemporalWorkerDeploymentReconciler) markWRTsTWDNotFound(ctx context.Context, twd types.NamespacedName) error {
	l := log.FromContext(ctx)
	var wrtList temporaliov1alpha1.WorkerResourceTemplateList
	if err := r.List(ctx, &wrtList,
		client.InNamespace(twd.Namespace),
		client.MatchingFields{wrtWorkerRefKey: twd.Name},
	); err != nil {
		return fmt.Errorf("list WorkerResourceTemplates referencing missing TemporalWorkerDeployment %q: %w", twd.Name, err)
	}
	var errs []error
	for i := range wrtList.Items {
		wrt := &wrtList.Items[i]
		meta.SetStatusCondition(&wrt.Status.Conditions, metav1.Condition{
			Type:               temporaliov1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             temporaliov1alpha1.ReasonWRTTWDNotFound,
			Message:            fmt.Sprintf("TemporalWorkerDeployment %q not found", twd.Name),
			ObservedGeneration: wrt.Generation,
		})
		if err := r.Status().Update(ctx, wrt); err != nil {
			l.Error(err, "unable to update WorkerResourceTemplate status for missing TemporalWorkerDeployment", "wrt", wrt.Name, "twd", twd.Name)
			errs = append(errs, fmt.Errorf("update status for WorkerResourceTemplate %s/%s: %w", wrt.Namespace, wrt.Name, err))
		}
	}
	return errors.Join(errs...)
}

// setCondition sets a condition on the TemporalWorkerDeployment status.
func (r *TemporalWorkerDeploymentReconciler) setCondition(
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
	conditionType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&workerDeploy.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: workerDeploy.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// syncConditions sets Ready and Progressing based on the current rollout state.
// It must be called at the end of a successful reconcile (no errors) so that
// Progressing/Ready reflect the latest Temporal version status.
func (r *TemporalWorkerDeploymentReconciler) syncConditions(twd *temporaliov1alpha1.TemporalWorkerDeployment) {
	// Deprecated: set TemporalConnectionHealthy=True on all successful reconciles for v1.3.x compat.
	r.setCondition(twd, temporaliov1alpha1.ConditionTemporalConnectionHealthy, //nolint:staticcheck // backward compat
		metav1.ConditionTrue, temporaliov1alpha1.ReasonTemporalConnectionHealthy, //nolint:staticcheck // backward compat
		"TemporalConnection is healthy and auth secret is resolved")

	switch twd.Status.TargetVersion.Status {
	case temporaliov1alpha1.VersionStatusCurrent:
		r.setCondition(twd, temporaliov1alpha1.ConditionReady,
			metav1.ConditionTrue, temporaliov1alpha1.ReasonRolloutComplete,
			fmt.Sprintf("Rollout complete for buildID %s", twd.Status.TargetVersion.BuildID))
		r.setCondition(twd, temporaliov1alpha1.ConditionProgressing,
			metav1.ConditionFalse, temporaliov1alpha1.ReasonRolloutComplete,
			fmt.Sprintf("Target version %s is current", twd.Status.TargetVersion.BuildID))
		// Deprecated: set RolloutComplete=True for v1.3.x compat.
		r.setCondition(twd, temporaliov1alpha1.ConditionRolloutComplete, //nolint:staticcheck // backward compat
			metav1.ConditionTrue, temporaliov1alpha1.ReasonRolloutComplete,
			fmt.Sprintf("Rollout complete for buildID %s", twd.Status.TargetVersion.BuildID))
	case temporaliov1alpha1.VersionStatusRamping:
		r.setCondition(twd, temporaliov1alpha1.ConditionReady,
			metav1.ConditionFalse, temporaliov1alpha1.ReasonRamping,
			fmt.Sprintf("Target version %s is ramping", twd.Status.TargetVersion.BuildID))
		r.setCondition(twd, temporaliov1alpha1.ConditionProgressing,
			metav1.ConditionTrue, temporaliov1alpha1.ReasonRamping,
			fmt.Sprintf("Target version %s is receiving a percentage of new workflows", twd.Status.TargetVersion.BuildID))
	case temporaliov1alpha1.VersionStatusInactive:
		r.setCondition(twd, temporaliov1alpha1.ConditionReady,
			metav1.ConditionFalse, temporaliov1alpha1.ReasonWaitingForPromotion,
			fmt.Sprintf("Target version %s is registered but not yet promoted", twd.Status.TargetVersion.BuildID))
		r.setCondition(twd, temporaliov1alpha1.ConditionProgressing,
			metav1.ConditionTrue, temporaliov1alpha1.ReasonWaitingForPromotion,
			fmt.Sprintf("Target version %s is waiting for promotion to current", twd.Status.TargetVersion.BuildID))
	default: // NotRegistered or unset: workers have not started polling yet
		r.setCondition(twd, temporaliov1alpha1.ConditionReady,
			metav1.ConditionFalse, temporaliov1alpha1.ReasonWaitingForPollers,
			fmt.Sprintf("Target version %s is not yet registered with Temporal", twd.Status.TargetVersion.BuildID))
		r.setCondition(twd, temporaliov1alpha1.ConditionProgressing,
			metav1.ConditionTrue, temporaliov1alpha1.ReasonWaitingForPollers,
			fmt.Sprintf("Waiting for workers with buildID %s to start polling", twd.Status.TargetVersion.BuildID))
	}
}

// recordWarningAndSetBlocked emits a warning event, sets Progressing=False and Ready=False
// with the given reason, and persists the status immediately. Called on all error paths that
// block reconciliation progress.
func (r *TemporalWorkerDeploymentReconciler) recordWarningAndSetBlocked(
	ctx context.Context,
	workerDeploy *temporaliov1alpha1.TemporalWorkerDeployment,
	reason string,
	eventMessage string,
	conditionMessage string,
) {
	r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, reason, eventMessage)
	r.setCondition(workerDeploy, temporaliov1alpha1.ConditionProgressing, metav1.ConditionFalse, reason, conditionMessage)
	r.setCondition(workerDeploy, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, reason, conditionMessage)
	// Deprecated: set TemporalConnectionHealthy=False for v1.3.x compat, but only for
	// reasons that actually indicate connection/auth issues. Plan generation and execution
	// failures are unrelated to connection health and should not trigger this condition.
	switch reason {
	case temporaliov1alpha1.ReasonTemporalConnectionNotFound,
		temporaliov1alpha1.ReasonAuthSecretInvalid,
		temporaliov1alpha1.ReasonTemporalClientCreationFailed,
		temporaliov1alpha1.ReasonTemporalStateFetchFailed:
		r.setCondition(workerDeploy, temporaliov1alpha1.ConditionTemporalConnectionHealthy, metav1.ConditionFalse, reason, conditionMessage) //nolint:staticcheck // backward compat
	}
	_ = r.Status().Update(ctx, workerDeploy)
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

	// Index WorkerResourceTemplate by spec.temporalWorkerDeploymentRef.name for efficient listing.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &temporaliov1alpha1.WorkerResourceTemplate{}, wrtWorkerRefKey, func(rawObj client.Object) []string {
		wrt, ok := rawObj.(*temporaliov1alpha1.WorkerResourceTemplate)
		if !ok {
			mgr.GetLogger().Error(errors.New("error indexing WorkerResourceTemplates"), "could not convert raw object", rawObj)
			return nil
		}
		return []string{wrt.Spec.TemporalWorkerDeploymentRef.Name}
	}); err != nil {
		return err
	}

	recoverPanic := !r.DisableRecoverPanic
	return ctrl.NewControllerManagedBy(mgr).
		For(&temporaliov1alpha1.TemporalWorkerDeployment{}).
		Owns(&appsv1.Deployment{}).
		Watches(&temporaliov1alpha1.TemporalConnection{}, handler.EnqueueRequestsFromMapFunc(r.findTWDsUsingConnection)).
		Watches(&temporaliov1alpha1.WorkerResourceTemplate{}, handler.EnqueueRequestsFromMapFunc(r.reconcileRequestForWRT)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100,
			RecoverPanic:            &recoverPanic,
		}).
		Complete(r)
}

// reconcileRequestForWRT returns a reconcile.Request to reconcile the TWD associated with the
// supplied WRT.
func (r *TemporalWorkerDeploymentReconciler) reconcileRequestForWRT(ctx context.Context, wrt client.Object) []reconcile.Request {
	wrtObj, ok := wrt.(*temporaliov1alpha1.WorkerResourceTemplate)
	if !ok {
		return nil
	}
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      wrtObj.Spec.TemporalWorkerDeploymentRef.Name,
				Namespace: wrt.GetNamespace(),
			},
		},
	}
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
		if twd.Spec.WorkerOptions.TemporalConnectionRef.Name == tc.GetName() {
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
