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
	"github.com/temporalio/temporal-worker-controller/internal/controller/connectionprovider"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"go.temporal.io/api/serviceerror"
	sdkclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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

// WorkerDeploymentReconciler reconciles a WorkerDeployment object
type WorkerDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Providers is the startup-built list of connection providers; the
	// reconciler never mutates it. A wrapper binary appends its own kinds to
	// the default slice from clientpool.NewDefaultProviders.
	Providers []connectionprovider.ConnectionProvider
	Recorder  events.EventRecorder

	// Disables panic recovery if true
	DisableRecoverPanic  bool
	DisableDeprecatedTWD bool

	// DisableClusterConnections drops ClusterConnection support. Set when the manager is
	// namespace-scoped: ClusterConnection is cluster-scoped, so a namespaced Role can never
	// authorize listing it and the watch would retry forever against a 403.
	DisableClusterConnections bool

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
// +kubebuilder:rbac:groups=temporal.io,resources=clusterconnections,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=temporal.io,resources=clusterconnections/finalizers,verbs=update
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

	// This is the status currently stored in the API server. It is diffed later against the
	// status computed by this reconcile.
	observedStatus := workerDeploy.Status.DeepCopy()

	// Migration: if a deprecated TemporalWorkerDeployment with the same name/namespace
	// exists and has not yet been migrated, transfer ownership of its child Deployments
	// and WorkerResourceTemplates to this WorkerDeployment.
	if !r.DisableDeprecatedTWD {
		if err := r.migrateFromDeprecatedTWD(ctx, l, &workerDeploy); err != nil {
			return ctrl.Result{}, err
		}
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
			if err := r.releaseConnectionFinalizerIfUnused(ctx, l, workerDeploy.Spec.WorkerOptions.ConnectionRef, workerDeploy.Namespace, workerDeploy.Name); err != nil {
				return ctrl.Result{}, err
			}

			// We use Patch here and not Update here to avoid conflicts during
			// apply where the field manager is different (say, helm versus
			// temporal-worker-controller).
			//
			// Ref: https://github.com/temporalio/temporal-worker-controller/issues/317
			patch := client.MergeFrom(workerDeploy.DeepCopy())

			// Cleanup succeeded, remove the finalizer so K8s can delete the resource
			controllerutil.RemoveFinalizer(&workerDeploy, finalizerName)
			if err := r.Patch(ctx, &workerDeploy, patch); err != nil {
				return ctrl.Result{}, err
			}
			l.Info("Temporal server-side cleanup complete, finalizer removed")
		}
		return ctrl.Result{}, nil
	}

	// ConnectionRef.Name is validated by webhook (+kubebuilder:validation:Required).
	// Resolve the connection provider before adding the WD finalizer, so an
	// unknown kind is reported on a deletable object instead of wedging deletion.
	connRef := workerDeploy.Spec.WorkerOptions.ConnectionRef
	connProv, err := connectionprovider.LookupProvider(r.Providers, connRef)
	if err != nil {
		l.Error(err, "unable to look up connection provider", "connectionRef", connRef)
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonUnknownConnectionKind,
			fmt.Sprintf("Unknown connection kind for %q: %v", connectionprovider.RefName(connRef), err),
			fmt.Sprintf("Unknown connection kind: %v", err))
		// No requeue: the user must correct the connectionRef.
		return ctrl.Result{}, nil
	}

	// Block cluster-scoped refs when the controller is namespace-scoped. Scope
	// comes from the resolved provider, so wrapper-registered cluster-scoped
	// kinds are blocked too, not just the built-in ClusterConnection.
	if r.DisableClusterConnections && connProv.IsClusterScoped() {
		msg := fmt.Sprintf("ClusterConnection %q cannot be used because the controller is namespace-scoped",
			connectionprovider.RefName(connRef))
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonClusterConnectionUnsupported, msg, msg)
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present on non-deleted resources
	if !controllerutil.ContainsFinalizer(&workerDeploy, finalizerName) {
		// We use Patch here and not Update here to avoid conflicts during
		// apply where the field manager is different (say, helm versus
		// temporal-worker-controller).
		//
		// Ref: https://github.com/temporalio/temporal-worker-controller/issues/317
		patch := client.MergeFrom(workerDeploy.DeepCopy())
		controllerutil.AddFinalizer(&workerDeploy, finalizerName)
		if err := r.Patch(ctx, &workerDeploy, patch); err != nil {
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

	// Fetch the referenced connection object; a missing one surfaces as
	// ReasonConnectionNotFound. (Unknown kinds were rejected above.)
	connection, err := connProv.Fetch(ctx, connRef, workerDeploy.Namespace)
	if err != nil {
		l.Error(err, "unable to fetch Connection")
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			temporaliov1alpha1.ReasonConnectionNotFound,
			fmt.Sprintf("Unable to fetch Connection %q: %v", connectionprovider.RefName(connRef), err),
			fmt.Sprintf("Connection %q not found: %v", connectionprovider.RefName(connRef), err))
		return ctrl.Result{}, err
	}

	// Ensure the finalizer is on the connection object so it cannot be deleted
	// while this WD still references it.
	if err := r.ensureConnectionFinalizer(ctx, l, connection.Object()); err != nil {
		return ctrl.Result{}, err
	}

	// If connectionRef changed since we last reconciled (name or kind), release the
	// finalizer from the previously-referenced connection if no other WD uses it.
	// The new connection is already protected by ensureConnectionFinalizer above,
	// so the WD is never left unprotected.
	if workerDeploy.Generation != workerDeploy.Status.ObservedGeneration {
		current := workerDeploy.Spec.WorkerOptions.ConnectionRef
		if observed := workerDeploy.Status.ObservedConnectionRef; observed != nil && !sameConnectionRef(*observed, current) {
			if err := r.releaseConnectionFinalizerIfUnused(ctx, l, *observed, workerDeploy.Namespace, workerDeploy.Name); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Get or create the cached SDK client for this connection. The provider owns
	// client construction. Auth/credential errors surface as
	// ReasonAuthSecretInvalid; dial/connectivity errors as
	// ReasonTemporalClientCreationFailed.
	temporalClient, err := connection.GetClient(ctx, workerDeploy.Spec.WorkerOptions.TemporalNamespace, workerDeploy.Namespace, getControllerIdentity())
	if err != nil {
		var authErr *connectionprovider.AuthError
		var dialErr *connectionprovider.DialError
		switch {
		case errors.As(err, &authErr):
			l.Error(err, "invalid Temporal auth secret")
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonAuthSecretInvalid,
				fmt.Sprintf("Invalid Temporal auth secret for %q: %v", connectionprovider.RefName(connRef), err),
				fmt.Sprintf("Invalid auth secret: %v", err))
		case errors.As(err, &dialErr):
			l.Error(err, "unable to create TemporalClient")
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonTemporalClientCreationFailed,
				fmt.Sprintf("Unable to create Temporal client for %q: %v", connectionprovider.RefName(connRef), err),
				fmt.Sprintf("Failed to connect to Temporal: %v", err))
		default:
			l.Error(err, "unable to create TemporalClient")
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonTemporalClientCreationFailed,
				fmt.Sprintf("Unable to create Temporal client for %q: %v", connectionprovider.RefName(connRef), err),
				fmt.Sprintf("Failed to connect to Temporal: %v", err))
		}
		return ctrl.Result{}, err
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
		l,
		temporalClient,
		workerDeploymentName,
		workerDeploy.Spec.WorkerOptions.TemporalNamespace,
		k8sState.Deployments,
		targetBuildID,
		workerDeploy.Spec.RolloutStrategy.Strategy,
		getControllerIdentity(),
	)
	if err != nil {
		if shouldEvictClient(err) {
			connection.Evict(workerDeploy.Spec.WorkerOptions.TemporalNamespace, workerDeploy.Namespace)
		}
		var rateLimitErr *serviceerror.ResourceExhausted
		if errors.As(err, &rateLimitErr) {
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				temporaliov1alpha1.ReasonTemporalStateFetchFailed,
				fmt.Sprintf("Got ResourceExhausted error fetching worker deployment state from Temporal server: %v", err),
				fmt.Sprintf("Got ResourceExhausted error fetching worker deployment state from Temporal server: %v", err))
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
	status.ObservedConnectionRef = workerDeploy.Spec.WorkerOptions.ConnectionRef.DeepCopy()
	status.ObservedGeneration = workerDeploy.Generation
	workerDeploy.Status = *status

	// TODO(jlegrone): Set defaults via webhook rather than manually
	//  (defaults were already set above, but have to be set again after status update)
	if err := workerDeploy.Default(ctx, &workerDeploy); err != nil {
		l.Error(err, "WorkerDeployment defaulter failed")
		return ctrl.Result{}, err
	}

	// Generate a plan to get to desired spec from current status
	plan, err := r.generatePlan(ctx, l, &workerDeploy, connection, temporalState)
	if err != nil {
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			ReasonPlanGenerationFailed,
			fmt.Sprintf("Unable to generate reconciliation plan: %v", err),
			fmt.Sprintf("Plan generation failed: %v", err))
		return ctrl.Result{}, err
	}

	// Execute the plan, handling any errors
	if err := r.executePlan(ctx, l, &workerDeploy, temporalClient, plan); err != nil {
		if shouldEvictClient(err) {
			connection.Evict(workerDeploy.Spec.WorkerOptions.TemporalNamespace, workerDeploy.Namespace)
		}
		var rateLimitErr *serviceerror.ResourceExhausted
		if errors.As(err, &rateLimitErr) {
			r.recordWarningAndSetBlocked(ctx, &workerDeploy,
				ReasonPlanExecutionFailed,
				fmt.Sprintf("Got ResourceExhausted error executing plan: %v", err),
				fmt.Sprintf("Got ResourceExhausted error executing plan: %v", err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.recordWarningAndSetBlocked(ctx, &workerDeploy,
			ReasonPlanExecutionFailed,
			fmt.Sprintf("Unable to execute reconciliation plan: %v", err),
			fmt.Sprintf("Plan execution failed: %v", err))
		return ctrl.Result{}, err
	}

	// Derive Ready/Progressing from rollout state before the final write. When the
	// target version has become current, this also factors in whether workers are
	// actively polling Temporal into ConditionProgressing (Ready itself remains
	// about rollout completion only).
	r.syncConditions(&workerDeploy, temporalState)

	// Single status write per reconcile: persists the generated status and
	// conditions set during this loop (Ready, Progressing). Do not send the update
	// when the status has not changed.
	if !equality.Semantic.DeepEqual(observedStatus, &workerDeploy.Status) {
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
//  3. Delete the k8s deployments so their pods stop polling
//  4. Delete all registered versions (with SkipDrainage since the WD is being removed entirely)
//  5. Delete the deployment record itself once all versions are gone
func (r *WorkerDeploymentReconciler) handleDeletion(
	ctx context.Context,
	l logr.Logger,
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
) (retErr error) {
	// Resolve Connection.
	// The Connection is guaranteed to exist because we hold a finalizer on it
	// that prevents deletion while any WD references it. An unknown kind,
	// however, never produced a Temporal client or placed a connection
	// finalizer (the reconcile path blocks before that), so there is nothing
	// to clean up server-side — treat it as success so the WD finalizer can be
	// removed instead of wedging deletion in a retry loop.
	connProv, err := connectionprovider.LookupProvider(r.Providers, workerDeploy.Spec.WorkerOptions.ConnectionRef)
	if err != nil {
		var unknown *connectionprovider.UnknownKindError
		if errors.As(err, &unknown) {
			l.Info("skipping deletion cleanup: connection kind has no provider",
				"connectionRef", workerDeploy.Spec.WorkerOptions.ConnectionRef)
			return nil
		}
		return fmt.Errorf("unable to look up connection provider: %w", err)
	}
	connection, err := connProv.Fetch(ctx, workerDeploy.Spec.WorkerOptions.ConnectionRef, workerDeploy.Namespace)
	if err != nil {
		return fmt.Errorf("unable to fetch Connection: %w", err)
	}

	temporalClient, err := connection.GetClient(ctx, workerDeploy.Spec.WorkerOptions.TemporalNamespace, workerDeploy.Namespace, getControllerIdentity())
	if err != nil {
		return fmt.Errorf("unable to create TemporalClient: %w", err)
	}

	// Evict cached SDK clients on failures that indicate the cached client may
	// no longer be usable. Domain responses such as NotFound are handled as
	// success below and should not churn otherwise healthy clients.
	defer func() {
		if shouldEvictClient(retErr) {
			connection.Evict(workerDeploy.Spec.WorkerOptions.TemporalNamespace, workerDeploy.Namespace)
		}
	}()

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

	// Step 3: Delete the k8s deployments so their pods stop polling. DeleteVersion is
	// rejected while a version still has active pollers.
	k8sState, err := k8s.GetDeploymentState(
		ctx,
		r.Client,
		workerDeploy.Namespace,
		workerDeploy.Name,
		workerDeploymentName)
	if err != nil {
		return fmt.Errorf("unable to list k8s deployments during deletion of worker deployment: %w", err)
	}
	for _, d := range k8sState.DeploymentsByTime {
		l.Info("Deleting k8s worker deployment during cleanup", "deployment", d.Name)
		if err := r.Delete(ctx, d); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("unable to delete k8s deployment %s during deletion of worker deployment (will retry): %w", d.Name, err)
		}
	}

	// Step 4: Delete every registered version. SkipDrainage lets DeleteVersion succeed on
	// versions that are still draining, which is acceptable here since the whole WD is
	// going away. If any version fails to delete, return an error so the reconciler requeues.
	// Pollers linger in the server's cache for matching.PollerHistoryTTL (dynamic config,
	// 5m by default) after the pods terminate, so a later attempt succeeds.
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

	// Step 5: Delete the worker deployment itself. This only succeeds if all versions are gone.
	l.Info("Attempting to delete worker deployment from Temporal server", "name", workerDeploymentName)
	if _, err := temporalClient.WorkerDeploymentClient().Delete(ctx, sdkclient.WorkerDeploymentDeleteOptions{
		Name:     workerDeploymentName,
		Identity: getControllerIdentity(),
	}); err != nil {
		return fmt.Errorf("unable to delete worker deployment %s (will retry): %w", workerDeploymentName, err)
	}

	return nil
}

// setCondition sets the given condition and reports whether it actually changed
// (differed in Status/Reason/Message/ObservedGeneration from what was already set).
func (r *WorkerDeploymentReconciler) setCondition(
	workerDeploy *temporaliov1alpha1.WorkerDeployment,
	conditionType string,
	status metav1.ConditionStatus,
	reason, message string,
) bool {
	return meta.SetStatusCondition(&workerDeploy.Status.Conditions, metav1.Condition{
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
func (r *WorkerDeploymentReconciler) syncConditions(
	twd *temporaliov1alpha1.WorkerDeployment,
	temporalState *temporal.TemporalWorkerState,
) {
	// Deprecated: set ConnectionHealthy=True on all successful reconciles for v1.3.x compat.
	r.setCondition(twd, temporaliov1alpha1.ConditionConnectionHealthy, //nolint:staticcheck // backward compat
		metav1.ConditionTrue, temporaliov1alpha1.ReasonConnectionHealthy, //nolint:staticcheck // backward compat
		"Connection is healthy and auth secret is resolved")

	switch twd.Status.TargetVersion.Status {
	case temporaliov1alpha1.VersionStatusCurrent:
		// Rollout itself has completed — Ready stays True regardless of poller
		// presence. Poller presence is surfaced on ConditionProgressing instead,
		// mirroring the existing ReasonWaitingForPollers (Progressing=True) /
		// ReasonActivePollers (Progressing=False) pair.
		r.setCondition(twd, temporaliov1alpha1.ConditionReady,
			metav1.ConditionTrue, temporaliov1alpha1.ReasonRolloutComplete,
			fmt.Sprintf("Rollout complete for buildID %s", twd.Status.TargetVersion.BuildID))

		r.setConditionProgressingForCurrent(twd, temporalState)

		// Deprecated: set RolloutComplete=True for v1.3.x compat. This deliberately
		// mirrors rollout completion only, not poller presence, matching its
		// pre-existing (Kubernetes-readiness-only) semantics for v1.3.x compat
		// consumers.
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
	r.Recorder.Eventf(workerDeploy, nil, corev1.EventTypeWarning, reason, reason, "%s", eventMessage)
	r.setCondition(workerDeploy, temporaliov1alpha1.ConditionProgressing, metav1.ConditionFalse, reason, conditionMessage)
	r.setCondition(workerDeploy, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, reason, conditionMessage)
	// Deprecated: set ConnectionHealthy=False for v1.3.x compat, but only for
	// reasons that actually indicate connection/auth issues. Plan generation and execution
	// failures are unrelated to connection health and should not trigger this condition.
	switch reason {
	case temporaliov1alpha1.ReasonConnectionNotFound,
		temporaliov1alpha1.ReasonClusterConnectionUnsupported,
		temporaliov1alpha1.ReasonUnknownConnectionKind,
		temporaliov1alpha1.ReasonAuthSecretInvalid,
		temporaliov1alpha1.ReasonTemporalClientCreationFailed,
		temporaliov1alpha1.ReasonTemporalStateFetchFailed:
		r.setCondition(workerDeploy, temporaliov1alpha1.ConditionConnectionHealthy, metav1.ConditionFalse, reason, conditionMessage) //nolint:staticcheck // backward compat
	}
	_ = r.Status().Update(ctx, workerDeploy)
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
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&temporaliov1alpha1.WorkerDeployment{}).
		Owns(&appsv1.Deployment{})
	// Watch every registered connection kind so a change to any referenced
	// connection re-enqueues the WorkerDeployments that use it. The mapper is
	// scoped by GroupKind: a namespaced kind lists WDs in the connection's own
	// namespace; a cluster-scoped kind lists across all namespaces. No concrete
	// kind name is hardcoded; a wrapper binary appends its kinds to the slice
	// and gets the watch for free. Skip cluster-scoped kinds when the controller
	// is namespace-scoped (DisableClusterConnections): a namespaced Role can
	// never authorize listing them and the watch would retry forever.
	for _, prov := range r.Providers {
		if prov.IsClusterScoped() && r.DisableClusterConnections {
			continue
		}
		builder = builder.Watches(prov.NewObject(), handler.EnqueueRequestsFromMapFunc(r.findTWDsUsingConnectionKind(prov.GroupKind(), prov.IsClusterScoped())))
	}
	builder = builder.Watches(&temporaliov1alpha1.WorkerResourceTemplate{}, handler.EnqueueRequestsFromMapFunc(r.reconcileRequestForWRT))
	if !r.DisableDeprecatedTWD {
		// Watch deprecated TemporalWorkerDeployments so that any modification to an existing TWD
		// (e.g. a status update before migration completes) triggers a reconcile of the matching WD
		// and causes migrateFromDeprecatedTWD to run.
		builder = builder.Watches(&temporaliov1alpha1.TemporalWorkerDeployment{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}}}
		}))
	}
	return builder.
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

// findTWDsUsingConnectionKind returns a mapper that enqueues every WorkerDeployment
// referencing a connection of the given GroupKind. clusterScoped controls the list
// scope: a namespaced kind lists WDs in the connection's own namespace, while a
// cluster-scoped kind lists across all namespaces. A WD matches only if BOTH the
// referenced name and the resolved GroupKind match, so a namespaced Connection
// "foo" and a cluster-scoped connection "foo" are distinct.
func (r *WorkerDeploymentReconciler) findTWDsUsingConnectionKind(gk schema.GroupKind, clusterScoped bool) func(context.Context, client.Object) []reconcile.Request {
	return func(ctx context.Context, conn client.Object) []reconcile.Request {
		var requests []reconcile.Request

		var listOpts []client.ListOption
		if !clusterScoped {
			listOpts = append(listOpts, client.InNamespace(conn.GetNamespace()))
		}

		var twds temporaliov1alpha1.WorkerDeploymentList
		if err := r.List(ctx, &twds, listOpts...); err != nil {
			return requests
		}

		for _, twd := range twds.Items {
			ref := twd.Spec.WorkerOptions.ConnectionRef
			if connectionprovider.RefName(ref) == conn.GetName() && connectionprovider.RefGroupKind(ref) == gk {
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
}

func isAccessDeniedErr(err error) bool {
	var permDenied *serviceerror.PermissionDenied
	if errors.As(err, &permDenied) {
		return true
	}
	return grpcstatus.Code(err) == codes.Unauthenticated
}

func shouldEvictClient(err error) bool {
	if err == nil {
		return false
	}
	if isAccessDeniedErr(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var unavailable *serviceerror.Unavailable
	return errors.As(err, &unavailable)
}
