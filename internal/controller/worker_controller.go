// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"go.temporal.io/api/serviceerror"
	sdkclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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

	// wrtWorkerRefKey is the field index key for WorkerResourceTemplate by effective workerDeploymentRef name.
	wrtWorkerRefKey = ".spec.workerDeploymentRef.name"

	// finalizerName is the finalizer added to deprecated TemporalWorkerDeployment and TemporalConnection
	// resources to prevent deletion before cleanup actions are taken. On TWD resources, it
	// ensures Temporal server-side versioning data is cleaned up. On TemporalConnection
	// resources, it prevents deletion while any TWD still references the connection.
	finalizerName = "temporal.io/delete-protection"
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

func resolveAuthSecretName(tc *temporaliov1alpha1.Connection) (clientpool.AuthMode, string, error) {
	if tc.Spec.MutualTLSSecretRef != nil {
		name, err := getTLSSecretName(tc.Spec.MutualTLSSecretRef)
		return clientpool.AuthModeTLS, name, err
	} else if tc.Spec.APIKeySecretRef != nil {
		name, err := getAPIKeySecretName(tc.Spec.APIKeySecretRef)
		return clientpool.AuthModeAPIKey, name, err
	}
	return clientpool.AuthModeNoCredentials, "", nil
}

// WorkerDeploymentReconciler reconciles a WorkerDeployment object
type WorkerDeploymentReconciler struct {
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

// +kubebuilder:rbac:groups=temporal.io,resources=temporalconnections,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalconnections/finalizers,verbs=update
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=temporalworkerdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=temporal.io,resources=connections,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=connections/finalizers,verbs=update
// +kubebuilder:rbac:groups=temporal.io,resources=workerdeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=temporal.io,resources=workerdeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=workerdeployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get
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
//
//nolint:revive // cyclomatic complexity acceptable for a top-level reconcile loop
func (r *WorkerDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// TODO(Shivam): Monitor if the time taken for a successful reconciliation loop is closing in on 5 minutes. If so, we
	// may need to increase the timeout value.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	l := log.FromContext(ctx)

	// Fallback identity check for when the reconciler is used as a library and
	// main() is not in the call path. main() is kept as the primary check for
	// faster feedback in normal Helm-based deployments.
	if getControllerIdentity() == "" {
		return ctrl.Result{}, errors.New(fmt.Sprintf("%s and %s are not set",
			IdentityEnvKey, IdentitySuffixEnvKey))
	}

	l.V(1).Info("Running Reconcile loop")

	// Fetch the worker deployment
	var workerDeploy temporaliov1alpha1.WorkerDeployment
	if err := r.Get(ctx, req.NamespacedName, &workerDeploy); err != nil {
		if !apierrors.IsNotFound(err) {
			l.Error(err, "unable to fetch WorkerDeployment")
			return ctrl.Result{}, err
		}
		// WD not found: set Ready=False on any WRTs that reference it so users get a
		// clear signal rather than a silent no-op. No requeue for the not-found itself —
		// the next reconcile fires naturally when the WD is created. If the List or
		// status updates fail (transient API errors), return the error to requeue with backoff.
		return ctrl.Result{}, r.markWRTsWDNotFound(ctx, req.NamespacedName)
	}

	// Migration: if a deprecated TemporalWorkerDeployment with the same name/namespace
	// exists and has not yet been migrated, transfer ownership of its child Deployments
	// and WorkerResourceTemplates to this WorkerDeployment.
	if err := r.migrateFromDeprecatedTWD(ctx, l, &workerDeploy); err != nil {
		return ctrl.Result{}, err
	}

	// Handle deletion: clean up Temporal server-side versioning data before allowing
	// the CRD to be deleted. Without this, stale build ID routing persists in Temporal
	// and prevents unversioned workers from picking up tasks on the same task queue.
	if !workerDeploy.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&workerDeploy, finalizerName) {
			l.Info("WorkerDeployment is being deleted, running cleanup")
			if err := r.handleDeletion(ctx, l, &workerDeploy); err != nil {
				l.Error(err, "failed to clean up Temporal server-side deployment data, will retry")
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}

			// Remove our finalizer from the Connection if no other WDs reference it.
			if err := r.removeConnectionFinalizerIfUnused(ctx, l, &workerDeploy); err != nil {
				return ctrl.Result{}, err
			}

			// Cleanup succeeded, remove the finalizer so K8s can delete the resource
			controllerutil.RemoveFinalizer(&workerDeploy, finalizerName)
			if err := r.Update(ctx, &workerDeploy); err != nil {
				return ctrl.Result{}, err
			}
			l.Info("Temporal server-side cleanup complete, finalizer removed")
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present on non-deleted resources
	if !controllerutil.ContainsFinalizer(&workerDeploy, finalizerName) {
		controllerutil.AddFinalizer(&workerDeploy, finalizerName)
		if err := r.Update(ctx, &workerDeploy); err != nil {
			return ctrl.Result{}, err
		}
	}

	// TODO(jlegrone): Set defaults via webhook rather than manually
	if err := workerDeploy.Default(ctx, &workerDeploy); err != nil {
		l.Error(err, "WorkerDeployment defaulter failed")
		return ctrl.Result{}, err
	}

	// Fallback validation for spec constraints the CRD schema cannot enforce (rampPercentage
	// ordering, gate input/inputFrom exclusivity). When the optional TWD webhook is disabled
	// these checks would otherwise go unreported; this surfaces them as a condition and event.
	if _, err := workerDeploy.ValidateCreate(ctx, &workerDeploy); err != nil {
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonInvalidSpec,
			fmt.Sprintf("Invalid WorkerDeployment spec: %v", err),
			err.Error())
		return ctrl.Result{}, nil
	}

	// Note: ConnectionRef.Name is validated by webhook due to +kubebuilder:validation:Required

	// Fetch the connection parameters
	var connection temporaliov1alpha1.Connection
	if err := r.Get(ctx, types.NamespacedName{
		Name:      workerDeploy.Spec.WorkerOptions.ConnectionRef.Name,
		Namespace: workerDeploy.Namespace,
	}, &connection); err != nil {
		l.Error(err, "unable to fetch Connection")
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonConnectionNotFound,
			fmt.Sprintf("Unable to fetch Connection %q: %v", workerDeploy.Spec.WorkerOptions.ConnectionRef.Name, err),
			fmt.Sprintf("Connection %q not found: %v", workerDeploy.Spec.WorkerOptions.ConnectionRef.Name, err))
		return ctrl.Result{}, err
	}

	// Ensure our finalizer is on the Connection so it cannot be deleted
	// while this WD still references it. This guarantees the connection is available
	// during WD deletion cleanup.
	if err := r.ensureConnectionFinalizer(ctx, l, &connection); err != nil {
		return ctrl.Result{}, err
	}

	// Get the Auth Mode and Secret Name
	authMode, secretName, err := resolveAuthSecretName(&connection)
	if err != nil {
		l.Error(err, "unable to resolve auth secret name")
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonAuthSecretInvalid,
			fmt.Sprintf("Unable to resolve auth secret from Connection %q: %v", connection.Name, err),
			fmt.Sprintf("Unable to resolve auth secret: %v", err))
		return ctrl.Result{}, err
	}

	// Get or update temporal client for connection
	clientPoolKey := clientpool.ClientPoolKey{
		HostPort:   connection.Spec.HostPort,
		Namespace:  workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		SecretName: secretName,
		AuthMode:   authMode,
	}
	temporalClient, ok := r.TemporalClientPool.GetSDKClient(clientPoolKey)
	if !ok {
		clientOpts, key, clientAuth, err := r.TemporalClientPool.ParseClientSecret(ctx, secretName, authMode, clientpool.NewClientOptions{
			K8sNamespace:      workerDeploy.Namespace,
			TemporalNamespace: workerDeploy.Spec.WorkerOptions.TemporalNamespace,
			Spec:              connection.Spec,
			Identity:          getControllerIdentity(),
		})
		if err != nil {
			l.Error(err, "invalid Temporal auth secret")
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonAuthSecretInvalid,
				fmt.Sprintf("Invalid Temporal auth secret for %s:%s: %v", connection.Spec.HostPort, workerDeploy.Spec.WorkerOptions.TemporalNamespace, err),
				fmt.Sprintf("Invalid auth secret: %v", err))
			return ctrl.Result{}, err
		}

		c, err := r.TemporalClientPool.DialAndUpsertClient(*clientOpts, *key, *clientAuth)
		if err != nil {
			l.Error(err, "unable to create TemporalClient")
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonTemporalClientCreationFailed,
				fmt.Sprintf("Unable to create Temporal client for %s:%s: %v", connection.Spec.HostPort, workerDeploy.Spec.WorkerOptions.TemporalNamespace, err),
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
		if isAccessDeniedErr(err) {
			r.TemporalClientPool.EvictClient(clientPoolKey)
		}
		var rateLimitErr *serviceerror.ResourceExhausted
		if errors.As(err, &rateLimitErr) {
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonTemporalStateFetchFailed,
				fmt.Sprintf("Rate limited fetching worker deployment state: %v", err),
				fmt.Sprintf("Rate limited by Temporal server: %v", err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonTemporalStateFetchFailed,
			fmt.Sprintf("Unable to get worker deployment state: %v", err),
			fmt.Sprintf("Failed to query worker deployment state: %v", err))
		return ctrl.Result{}, fmt.Errorf("unable to get worker deployment state: %w", err)
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
	//  (defaults were already set above, but have to be set again after status update)
	if err := workerDeploy.Default(ctx, &workerDeploy); err != nil {
		l.Error(err, "WorkerDeployment defaulter failed")
		return ctrl.Result{}, err
	}

	// Generate a plan to get to desired spec from current status
	plan, err := r.generatePlan(ctx, l, &workerDeploy, connection.Spec, temporalState)
	if err != nil {
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			ReasonPlanGenerationFailed,
			fmt.Sprintf("Unable to generate reconciliation plan: %v", err),
			fmt.Sprintf("Plan generation failed: %v", err))
		return ctrl.Result{}, err
	}

	// Execute the plan, handling any errors
	if err := r.executePlan(ctx, l, &workerDeploy, temporalClient, plan); err != nil {
		if isAccessDeniedErr(err) {
			r.TemporalClientPool.EvictClient(clientPoolKey)
		}
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

// migrateFromDeprecatedTWD checks for a TemporalWorkerDeployment stub with the
// same name/namespace as wd. If one exists and has not yet been migrated, it
// transfers ownership of child Deployments and WorkerResourceTemplates from the
// deprecated TWD to wd, then labels the TWD as migrated.
func (r *WorkerDeploymentReconciler) migrateFromDeprecatedTWD(ctx context.Context, l logr.Logger, wd *temporaliov1alpha1.WorkerDeployment) error {
	var twd temporaliov1alpha1.TemporalWorkerDeployment
	if err := r.Get(ctx, types.NamespacedName{Name: wd.Name, Namespace: wd.Namespace}, &twd); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // no deprecated TWD, nothing to migrate
		}
		return err
	}
	if twd.Labels[deprecatedTWDMigratedLabel] == "true" {
		return nil // already migrated
	}

	l.Info("migrating ownership from deprecated TemporalWorkerDeployment")

	wdRef := metav1.OwnerReference{
		APIVersion:         temporaliov1alpha1.GroupVersion.String(),
		Kind:               "WorkerDeployment",
		Name:               wd.Name,
		UID:                wd.UID,
		Controller:         ptr(true),
		BlockOwnerDeletion: ptr(true),
	}

	// Patch ownerRefs on child Deployments owned by the deprecated TWD.
	var allDeploys appsv1.DeploymentList
	if err := r.List(ctx, &allDeploys, client.InNamespace(wd.Namespace)); err != nil {
		return err
	}
	for i := range allDeploys.Items {
		dep := &allDeploys.Items[i]
		if !hasTWDOwner(dep.OwnerReferences, twd.UID) {
			continue
		}
		patch := client.MergeFrom(dep.DeepCopy())
		dep.OwnerReferences = replaceOwnerRef(dep.OwnerReferences, twd.UID, wdRef)
		if err := r.Patch(ctx, dep, patch); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("patching Deployment %s ownerRef: %w", dep.Name, err)
		}
	}

	// Patch ownerRefs on WorkerResourceTemplates.
	var wrtList temporaliov1alpha1.WorkerResourceTemplateList
	if err := r.List(ctx, &wrtList, client.InNamespace(wd.Namespace)); err != nil {
		return err
	}
	for i := range wrtList.Items {
		wrt := &wrtList.Items[i]
		if !hasTWDOwner(wrt.OwnerReferences, twd.UID) {
			continue
		}
		patch := client.MergeFrom(wrt.DeepCopy())
		wrt.OwnerReferences = replaceOwnerRef(wrt.OwnerReferences, twd.UID, wdRef)
		if err := r.Patch(ctx, wrt, patch); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("patching WorkerResourceTemplate %s ownerRef: %w", wrt.Name, err)
		}
	}

	// Label the deprecated TWD as migrated.
	twdPatch := client.MergeFrom(twd.DeepCopy())
	if twd.Labels == nil {
		twd.Labels = make(map[string]string)
	}
	twd.Labels[deprecatedTWDMigratedLabel] = "true"
	if err := r.Patch(ctx, &twd, twdPatch); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("labelling deprecated TemporalWorkerDeployment as migrated: %w", err)
	}

	l.Info("migration from deprecated TemporalWorkerDeployment complete")
	return nil
}

func hasTWDOwner(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}

func replaceOwnerRef(refs []metav1.OwnerReference, oldUID types.UID, newRef metav1.OwnerReference) []metav1.OwnerReference {
	out := make([]metav1.OwnerReference, len(refs))
	copy(out, refs)
	for i, r := range out {
		if r.UID == oldUID {
			out[i] = newRef
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// markWRTsWDNotFound sets Ready=False/WDNotFound on all WorkerResourceTemplates that reference
// a WorkerDeployment that could not be found. This covers the case where the WRT is
// created before the WD exists, or where the WD was deleted before the controller set an owner
// reference on the WRT (which would otherwise cause Kubernetes GC to delete the WRT).
// Returns an error if the List or any status update fails so the caller can requeue.
func (r *WorkerDeploymentReconciler) markWRTsWDNotFound(ctx context.Context, wd types.NamespacedName) error {
	l := log.FromContext(ctx)
	var wrtList temporaliov1alpha1.WorkerResourceTemplateList
	if err := r.List(ctx, &wrtList,
		client.InNamespace(wd.Namespace),
		client.MatchingFields{wrtWorkerRefKey: wd.Name},
	); err != nil {
		return fmt.Errorf("list WorkerResourceTemplates referencing missing WorkerDeployment %q: %w", wd.Name, err)
	}
	var errs []error
	for i := range wrtList.Items {
		wrt := &wrtList.Items[i]
		meta.SetStatusCondition(&wrt.Status.Conditions, metav1.Condition{
			Type:               temporaliov1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             temporaliov1alpha1.ReasonWRTWDNotFound,
			Message:            fmt.Sprintf("WorkerDeployment %q not found", wd.Name),
			ObservedGeneration: wrt.Generation,
		})
		if err := r.Status().Update(ctx, wrt); err != nil {
			l.Error(err, "unable to update WorkerResourceTemplate status for missing WorkerDeployment",
				"WorkerResourceTemplate", wrt.Name, "WorkerDeployment", wd.Name)
			errs = append(errs, fmt.Errorf("update status for WorkerResourceTemplate %s/%s: %w", wrt.Namespace, wrt.Name, err))
		}
	}
	return errors.Join(errs...)
}

// handleDeletion cleans up Temporal server-side deployment versioning data when
// a WorkerDeployment CRD is deleted. This prevents stale build ID routing
// from blocking unversioned workers on the same task queue.
//
// The cleanup sequence:
//  1. Clear the ramping version (must happen first to avoid a split-traffic window)
//  2. Set the current version to "unversioned" (empty BuildID) so new tasks route to unversioned workers
//  3. Delete all registered versions (with SkipDrainage since the WD is being removed entirely)
//  4. Delete the deployment record itself once all versions are gone
func (r *WorkerDeploymentReconciler) handleDeletion(
	ctx context.Context,
	l logr.Logger,
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
) error {
	// Resolve Connection.
	// The Connection is guaranteed to exist because we hold a finalizer on it
	// that prevents deletion while any WD references it.
	var temporalConnection temporaliov1alpha1.Connection
	if err := r.Get(ctx, types.NamespacedName{
		Name:      workerDeploy.Spec.WorkerOptions.ConnectionRef.Name,
		Namespace: workerDeploy.Namespace,
	}, &temporalConnection); err != nil {
		return fmt.Errorf("unable to fetch Connection: %w", err)
	}

	authMode, secretName, err := resolveAuthSecretName(&temporalConnection)
	if err != nil {
		return fmt.Errorf("unable to resolve auth secret name: %w", err)
	}

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
			return fmt.Errorf("unable to parse Temporal auth secret: %w", err)
		}
		c, err := r.TemporalClientPool.DialAndUpsertClient(*clientOpts, *key, *clientAuth)
		if err != nil {
			return fmt.Errorf("unable to create TemporalClient: %w", err)
		}
		temporalClient = c
	}

	workerDeploymentName := k8s.ComputeWorkerDeploymentName(workerDeploy)
	deploymentHandler := temporalClient.WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// Describe the deployment to get current state
	resp, err := deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			l.Info("Worker Deployment not found on Temporal server, nothing to clean up")
			return nil
		}
		return fmt.Errorf("unable to describe worker deployment: %w", err)
	}

	routingConfig := resp.Info.RoutingConfig

	// Step 1: Clear the ramping version first. This must happen before setting
	// current to unversioned to avoid a window where traffic is split between
	// unversioned workers and the ramping version.
	if routingConfig.RampingVersion != nil {
		l.Info("Clearing ramping version", "buildID", routingConfig.RampingVersion.BuildID)
		if _, err := deploymentHandler.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
			BuildID:    "",
			Percentage: 0,
			Identity:   getControllerIdentity(),
		}); err != nil {
			return fmt.Errorf("unable to clear ramping version: %w", err)
		}
		l.Info("Successfully cleared ramping version")

		// Re-describe to get a fresh ConflictToken after the ramping change.
		resp, err = deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return fmt.Errorf("unable to re-describe worker deployment after clearing ramping version: %w", err)
		}
	} else {
		l.Info("No ramping version set, skipping clear ramping version")
	}

	// Step 2: Set current version to unversioned (empty BuildID) so tasks route to unversioned workers.
	// This is the critical step that unblocks task dispatch.
	if routingConfig.CurrentVersion != nil {
		l.Info("Setting current version to unversioned", "previousBuildID", routingConfig.CurrentVersion.BuildID)
		if _, err := deploymentHandler.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
			BuildID:                 "", // empty = unversioned
			ConflictToken:           resp.ConflictToken,
			Identity:                getControllerIdentity(),
			IgnoreMissingTaskQueues: true,
		}); err != nil {
			return fmt.Errorf("unable to set current version to unversioned: %w", err)
		}
		l.Info("Successfully set current version to unversioned")
	} else {
		l.Info("No current version set, skipping unversioned redirect")
	}

	// Step 3: Delete versions that are eligible. Versions that are still draining
	// are force-deleted with SkipDrainage since the WD is being removed entirely.
	// If any version fails to delete (e.g. active pollers), return an error so the
	// reconciler requeues. Pollers disappear once pods terminate and the next
	// reconciliation will succeed.
	for _, version := range resp.Info.VersionSummaries {
		buildID := version.Version.BuildID
		l.Info("Deleting worker deployment version", "buildID", buildID)
		if _, err := deploymentHandler.DeleteVersion(ctx, sdkclient.WorkerDeploymentDeleteVersionOptions{
			BuildID:      buildID,
			SkipDrainage: true,
			Identity:     getControllerIdentity(),
		}); err != nil {
			return fmt.Errorf("unable to delete version %s (will retry): %w", buildID, err)
		}
	}

	// Step 4: Delete the deployment itself. This only succeeds if all versions are gone.
	l.Info("Attempting to delete worker deployment from Temporal server", "name", workerDeploymentName)
	if _, err := temporalClient.WorkerDeploymentClient().Delete(ctx, sdkclient.WorkerDeploymentDeleteOptions{
		Name:     workerDeploymentName,
		Identity: getControllerIdentity(),
	}); err != nil {
		return fmt.Errorf("unable to delete worker deployment %s (will retry): %w", workerDeploymentName, err)
	}

	return nil
}

// setCondition sets a condition on the WorkerDeployment status.
func (r *WorkerDeploymentReconciler) setCondition(
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
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
func (r *WorkerDeploymentReconciler) syncConditions(twd *temporaliov1alpha1.WorkerDeployment) {
	// Deprecated: set ConnectionHealthy=True on all successful reconciles for v1.3.x compat.
	r.setCondition(twd, temporaliov1alpha1.ConditionConnectionHealthy, //nolint:staticcheck // backward compat
		metav1.ConditionTrue, temporaliov1alpha1.ReasonConnectionHealthy, //nolint:staticcheck // backward compat
		"Connection is healthy and auth secret is resolved")

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
func (r *WorkerDeploymentReconciler) recordWarningAndSetBlocked(
	ctx context.Context,
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
	reason string,
	eventMessage string,
	conditionMessage string,
) {
	r.Recorder.Eventf(workerDeploy, corev1.EventTypeWarning, reason, "%s", eventMessage)
	r.setCondition(workerDeploy, temporaliov1alpha1.ConditionProgressing, metav1.ConditionFalse, reason, conditionMessage)
	r.setCondition(workerDeploy, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, reason, conditionMessage)
	// Deprecated: set ConnectionHealthy=False for v1.3.x compat, but only for
	// reasons that actually indicate connection/auth issues. Plan generation and execution
	// failures are unrelated to connection health and should not trigger this condition.
	switch reason {
	case temporaliov1alpha1.ReasonConnectionNotFound,
		temporaliov1alpha1.ReasonAuthSecretInvalid,
		temporaliov1alpha1.ReasonTemporalClientCreationFailed,
		temporaliov1alpha1.ReasonTemporalStateFetchFailed:
		r.setCondition(workerDeploy, temporaliov1alpha1.ConditionConnectionHealthy, metav1.ConditionFalse, reason, conditionMessage) //nolint:staticcheck // backward compat
	}
	_ = r.Status().Update(ctx, workerDeploy)
}

// ensureConnectionFinalizer adds our finalizer to the Connection so it
// cannot be deleted while this WD still needs it for cleanup.
func (r *WorkerDeploymentReconciler) ensureConnectionFinalizer(
	ctx context.Context,
	l logr.Logger,
	tc *temporaliov1alpha1.Connection,
) error {
	if !controllerutil.ContainsFinalizer(tc, finalizerName) {
		l.Info("Adding finalizer to Connection", "connection", tc.Name)
		controllerutil.AddFinalizer(tc, finalizerName)
		if err := r.Update(ctx, tc); err != nil {
			return fmt.Errorf("unable to add finalizer to Connection %q: %w", tc.Name, err)
		}
	}
	return nil
}

// removeConnectionFinalizerIfUnused removes our finalizer from the Connection
// if no other WDs (besides the one being deleted) still reference it.
func (r *WorkerDeploymentReconciler) removeConnectionFinalizerIfUnused(
	ctx context.Context,
	l logr.Logger,
	deletingWD *temporaliov1alpha1.WorkerDeployment,
) error {
	connectionName := deletingWD.Spec.WorkerOptions.ConnectionRef.Name

	// List all WDs in the same namespace
	var wds temporaliov1alpha1.WorkerDeploymentList
	if err := r.List(ctx, &wds, client.InNamespace(deletingWD.Namespace)); err != nil {
		return fmt.Errorf("unable to list WDs: %w", err)
	}

	// Check if any other WD (not the one being deleted) references this connection
	for i := range wds.Items {
		wd := &wds.Items[i]
		if wd.Name == deletingWD.Name {
			continue
		}
		if wd.Spec.WorkerOptions.ConnectionRef.Name == connectionName {
			l.Info("Connection still referenced by another WD, keeping finalizer",
				"connection", connectionName, "referencedBy", wd.Name)
			return nil
		}
	}

	// No other WDs reference this connection, remove the finalizer
	var tc temporaliov1alpha1.Connection
	if err := r.Get(ctx, types.NamespacedName{
		Name:      connectionName,
		Namespace: deletingWD.Namespace,
	}, &tc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // already gone
		}
		return fmt.Errorf("unable to fetch Connection %q: %w", connectionName, err)
	}

	if controllerutil.ContainsFinalizer(&tc, finalizerName) {
		l.Info("Removing finalizer from Connection", "connection", connectionName)
		controllerutil.RemoveFinalizer(&tc, finalizerName)
		if err := r.Update(ctx, &tc); err != nil {
			return fmt.Errorf("unable to remove finalizer from Connection %q: %w", connectionName, err)
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkerDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &appsv1.Deployment{}, deployOwnerKey, func(rawObj client.Object) []string {
		// grab the job object, extract the owner...
		deploy := rawObj.(*appsv1.Deployment)
		owner := metav1.GetControllerOf(deploy)

		if owner == nil {
			return nil
		}
		// ...make sure it's a WorkerDeployment...
		// TODO(jlegrone): double check apiGVStr has the correct value
		if owner.APIVersion != apiGVStr || owner.Kind != "WorkerDeployment" {
			return nil
		}

		// ...and if so, return it
		return []string{owner.Name}
	}); err != nil {
		return err
	}

	// Index WorkerResourceTemplate by EffectiveWorkerDeploymentName() for efficient listing.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &temporaliov1alpha1.WorkerResourceTemplate{}, wrtWorkerRefKey, func(rawObj client.Object) []string {
		wrt, ok := rawObj.(*temporaliov1alpha1.WorkerResourceTemplate)
		if !ok {
			mgr.GetLogger().Error(errors.New("error indexing WorkerResourceTemplates"), "could not convert raw object", rawObj)
			return nil
		}
		return []string{wrt.Spec.EffectiveWorkerDeploymentName()}
	}); err != nil {
		return err
	}

	recoverPanic := !r.DisableRecoverPanic
	return ctrl.NewControllerManagedBy(mgr).
		For(&temporaliov1alpha1.WorkerDeployment{}).
		Owns(&appsv1.Deployment{}).
		Watches(&temporaliov1alpha1.Connection{}, handler.EnqueueRequestsFromMapFunc(r.findTWDsUsingConnection)).
		Watches(&temporaliov1alpha1.WorkerResourceTemplate{}, handler.EnqueueRequestsFromMapFunc(r.reconcileRequestForWRT)).
		// Watch deprecated TemporalWorkerDeployments so that any modification to an existing TWD
		// (e.g. a status update before migration completes) triggers a reconcile of the matching WD
		// and causes migrateFromDeprecatedTWD to run.
		Watches(&temporaliov1alpha1.TemporalWorkerDeployment{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}}}
		})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100,
			RecoverPanic:            &recoverPanic,
		}).
		Complete(r)
}

// reconcileRequestForWRT returns a reconcile.Request to reconcile the TWD associated with the
// supplied WRT.
func (r *WorkerDeploymentReconciler) reconcileRequestForWRT(ctx context.Context, wrt client.Object) []reconcile.Request {
	wrtObj, ok := wrt.(*temporaliov1alpha1.WorkerResourceTemplate)
	if !ok {
		return nil
	}
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      wrtObj.Spec.EffectiveWorkerDeploymentName(),
				Namespace: wrt.GetNamespace(),
			},
		},
	}
}

func (r *WorkerDeploymentReconciler) findTWDsUsingConnection(ctx context.Context, tc client.Object) []reconcile.Request {
	var requests []reconcile.Request

	// Find all TWDs in same namespace that reference this TC
	var twds temporaliov1alpha1.WorkerDeploymentList
	if err := r.List(ctx, &twds, client.InNamespace(tc.GetNamespace())); err != nil {
		return requests
	}

	// Filter to ones using this connection
	for _, twd := range twds.Items {
		if twd.Spec.WorkerOptions.ConnectionRef.Name == tc.GetName() {
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

func isAccessDeniedErr(err error) bool {
	var permDenied *serviceerror.PermissionDenied
	if errors.As(err, &permDenied) {
		return true
	}
	return grpcstatus.Code(err) == codes.Unauthenticated
}
