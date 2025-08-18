// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"time"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Plan holds the actions to execute during reconciliation
type Plan struct {
	// Which actions to take
	DeleteDeployments      []*appsv1.Deployment
	ScaleDeployments       map[*corev1.ObjectReference]uint32
	UpdateDeployments      []*appsv1.Deployment
	ShouldCreateDeployment bool
	VersionConfig          *VersionConfig
	TestWorkflows          []WorkflowConfig
}

// VersionConfig defines version configuration for Temporal
type VersionConfig struct {
	// Token to use for conflict detection
	ConflictToken []byte
	// Deployment name and build ID for the new SDK structure
	DeploymentName string
	BuildID        string

	// One of RampPercentage OR SetCurrent must be set to a non-zero value.

	// Set this as the build ID for all new executions
	SetCurrent bool
	// Acceptable values [0,100]
	RampPercentage float32
}

// WorkflowConfig defines a workflow to be started
type WorkflowConfig struct {
	WorkflowType   string
	WorkflowID     string
	DeploymentName string
	BuildID        string
	TaskQueue      string
}

// Config holds the configuration for planning
type Config struct {
	// RolloutStrategy to use
	RolloutStrategy temporaliov1alpha1.RolloutStrategy
}

// GeneratePlan creates a plan for updating the worker deployment
func GeneratePlan(
	l logr.Logger,
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	temporalState *temporal.TemporalWorkerState,
	connection temporaliov1alpha1.TemporalConnectionSpec,
	config *Config,
	workerDeploymentName string,
) (*Plan, error) {
	plan := &Plan{
		ScaleDeployments: make(map[*corev1.ObjectReference]uint32),
	}

	// Add delete/scale operations based on version status
	plan.DeleteDeployments = getDeleteDeployments(k8sState, status, spec)
	plan.ScaleDeployments = getScaleDeployments(k8sState, status, spec)
	plan.ShouldCreateDeployment = shouldCreateDeployment(status, spec)
	plan.UpdateDeployments = getUpdateDeployments(k8sState, status, connection)

	// Determine if we need to start any test workflows
	plan.TestWorkflows = getTestWorkflows(status, config, workerDeploymentName)

	// Determine version config changes
	plan.VersionConfig = getVersionConfigDiff(l, status, temporalState, config, workerDeploymentName)

	// TODO(jlegrone): generate warnings/events on the TemporalWorkerDeployment resource when buildIDs are reachable
	//                 but have no corresponding Deployment.

	return plan, nil
}

// checkAndUpdateDeploymentConnectionSpec determines whether the Deployment for the given versionID is
// out-of-date with respect to the provided TemporalConnectionSpec. If an update is required, it mutates
// the existing Deployment in-place and returns a pointer to that Deployment. If no update is needed or
// the Deployment does not exist, it returns nil.
func checkAndUpdateDeploymentConnectionSpec(
	buildID string,
	k8sState *k8s.DeploymentState,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) *appsv1.Deployment {
	existingDeployment, exists := k8sState.Deployments[buildID]
	if !exists {
		return nil
	}

	// If the connection spec hash has changed, update the deployment
	currentHash := k8s.ComputeConnectionSpecHash(connection)
	if currentHash != existingDeployment.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] {

		// Update the deployment in-place with new connection info
		updateDeploymentWithConnection(existingDeployment, connection)
		return existingDeployment // Return the modified deployment
	}

	return nil
}

// updateDeploymentWithConnection updates an existing deployment with new TemporalConnectionSpec
func updateDeploymentWithConnection(deployment *appsv1.Deployment, connection temporaliov1alpha1.TemporalConnectionSpec) {
	// Update the connection spec hash annotation
	deployment.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] = k8s.ComputeConnectionSpecHash(connection)

	// Update secret volume if mTLS is enabled
	if connection.MutualTLSSecret != "" {
		for i, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == "temporal-tls" && volume.Secret != nil {
				deployment.Spec.Template.Spec.Volumes[i].Secret.SecretName = connection.MutualTLSSecret
				break
			}
		}
	}

	// Update any environment variables that reference the connection
	for i, container := range deployment.Spec.Template.Spec.Containers {
		for j, env := range container.Env {
			if env.Name == "TEMPORAL_HOST_PORT" {
				deployment.Spec.Template.Spec.Containers[i].Env[j].Value = connection.HostPort
			}
		}
	}
}

func getUpdateDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) []*appsv1.Deployment {
	var updateDeployments []*appsv1.Deployment

	// Check target version deployment if it has an expired connection spec hash
	if status.TargetVersion.BuildID != "" {
		if deployment := checkAndUpdateDeploymentConnectionSpec(status.TargetVersion.BuildID, k8sState, connection); deployment != nil {
			updateDeployments = append(updateDeployments, deployment)
		}
	}

	// Check current version deployment if it has an expired connection spec hash
	if status.CurrentVersion != nil && status.CurrentVersion.BuildID != "" {
		if deployment := checkAndUpdateDeploymentConnectionSpec(status.CurrentVersion.BuildID, k8sState, connection); deployment != nil {
			updateDeployments = append(updateDeployments, deployment)
		}
	}

	// Check deprecated versions for expired connection spec hashes
	for _, version := range status.DeprecatedVersions {
		if deployment := checkAndUpdateDeploymentConnectionSpec(version.BuildID, k8sState, connection); deployment != nil {
			updateDeployments = append(updateDeployments, deployment)
		}
	}

	return updateDeployments
}

// getDeleteDeployments determines which deployments should be deleted
func getDeleteDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
) []*appsv1.Deployment {
	var deleteDeployments []*appsv1.Deployment

	for _, version := range status.DeprecatedVersions {
		if version.Deployment == nil {
			continue
		}

		// Look up the deployment using buildID
		d, exists := k8sState.Deployments[version.BuildID]
		if !exists {
			continue
		}

		switch version.Status {
		case temporaliov1alpha1.VersionStatusDrained:
			// Deleting a deployment is only possible when:
			// 1. The deployment has been drained for deleteDelay + scaledownDelay.
			// 2. The deployment is scaled to 0 replicas.
			if (time.Since(version.DrainedSince.Time) > spec.SunsetStrategy.DeleteDelay.Duration+spec.SunsetStrategy.ScaledownDelay.Duration) &&
				*d.Spec.Replicas == 0 {
				deleteDeployments = append(deleteDeployments, d)
			}
		case temporaliov1alpha1.VersionStatusNotRegistered:
			// NotRegistered versions are versions that the server doesn't know about.
			// Only delete if it's not the target version.
			if status.TargetVersion.BuildID != version.BuildID {
				deleteDeployments = append(deleteDeployments, d)
			}
		}
	}

	return deleteDeployments
}

// getScaleDeployments determines which deployments should be scaled and to what size
func getScaleDeployments(
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
) map[*corev1.ObjectReference]uint32 {
	scaleDeployments := make(map[*corev1.ObjectReference]uint32)
	replicas := *spec.Replicas

	// Scale the current version if needed
	if status.CurrentVersion != nil && status.CurrentVersion.Deployment != nil {
		ref := status.CurrentVersion.Deployment
		if d, exists := k8sState.Deployments[status.CurrentVersion.BuildID]; exists {
			if d.Spec.Replicas != nil && *d.Spec.Replicas != replicas {
				scaleDeployments[ref] = uint32(replicas)
			}
		}
	}

	// Scale the target version if it exists, and isn't current
	if (status.CurrentVersion == nil || status.CurrentVersion.BuildID != status.TargetVersion.BuildID) &&
		status.TargetVersion.Deployment != nil {
		if d, exists := k8sState.Deployments[status.TargetVersion.BuildID]; exists {
			if d.Spec.Replicas == nil || *d.Spec.Replicas != replicas {
				scaleDeployments[status.TargetVersion.Deployment] = uint32(replicas)
			}
		}
	}

	// Scale other versions based on status
	for _, version := range status.DeprecatedVersions {
		if version.Deployment == nil {
			continue
		}

		d, exists := k8sState.Deployments[version.BuildID]
		if !exists {
			continue
		}

		switch version.Status {
		case temporaliov1alpha1.VersionStatusInactive,
			temporaliov1alpha1.VersionStatusRamping,
			temporaliov1alpha1.VersionStatusCurrent:
			// TODO(carlydf): Consolidate scale up cases and verify that scale up is the correct action for inactive versions
			// Scale up these deployments
			if d.Spec.Replicas != nil && *d.Spec.Replicas != replicas {
				scaleDeployments[version.Deployment] = uint32(replicas)
			}
		case temporaliov1alpha1.VersionStatusDrained:
			if time.Since(version.DrainedSince.Time) > spec.SunsetStrategy.ScaledownDelay.Duration {
				// TODO(jlegrone): Compute scale based on load? Or percentage of replicas?
				// Scale down drained deployments after delay
				if d.Spec.Replicas != nil && *d.Spec.Replicas != 0 {
					scaleDeployments[version.Deployment] = 0
				}
			}
		}
	}

	return scaleDeployments
}

// shouldCreateDeployment determines if a new deployment needs to be created
func shouldCreateDeployment(
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
) bool {
	// Check if target version already has a deployment
	if status.TargetVersion.Deployment != nil {
		return false
	}

	// Check if we're at the version limit
	maxVersions := int32(75) // Default from defaults.MaxVersions
	if spec.MaxVersions != nil {
		maxVersions = *spec.MaxVersions
	}

	if status.VersionCount >= maxVersions {
		return false
	}

	return true
}

// getTestWorkflows determines which test workflows should be started
func getTestWorkflows(
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	config *Config,
	workerDeploymentName string,
) []WorkflowConfig {
	var testWorkflows []WorkflowConfig

	// Skip if there's no gate workflow defined, if the target version is already the current, or if the target
	// version is not yet registered in temporal
	if config.RolloutStrategy.Gate == nil ||
		status.CurrentVersion == nil ||
		status.CurrentVersion.BuildID == status.TargetVersion.BuildID ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered {
		return nil
	}

	targetVersion := status.TargetVersion

	// Create a map of task queues that already have running test workflows
	taskQueuesWithWorkflows := make(map[string]struct{})
	for _, wf := range targetVersion.TestWorkflows {
		taskQueuesWithWorkflows[wf.TaskQueue] = struct{}{}
	}

	// For each task queue without a running test workflow, create a config
	for _, tq := range targetVersion.TaskQueues {
		if _, ok := taskQueuesWithWorkflows[tq.Name]; !ok {
			// Construct version ID from deployment name and build ID for workflow ID generation
			versionID := workerDeploymentName + "." + targetVersion.BuildID
			testWorkflows = append(testWorkflows, WorkflowConfig{
				WorkflowType:   config.RolloutStrategy.Gate.WorkflowType,
				WorkflowID:     temporal.GetTestWorkflowID(versionID, tq.Name),
				DeploymentName: workerDeploymentName,
				BuildID:        targetVersion.BuildID,
				TaskQueue:      tq.Name,
			})
		}
	}

	return testWorkflows
}

// getVersionConfigDiff determines the version configuration based on the rollout strategy
func getVersionConfigDiff(
	l logr.Logger,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	temporalState *temporal.TemporalWorkerState,
	config *Config,
	workerDeploymentName string,
) *VersionConfig {
	strategy := config.RolloutStrategy
	conflictToken := status.VersionConflictToken

	// Do nothing if target version's deployment is not healthy yet, or if the version is not yet registered in temporal
	if status.TargetVersion.HealthySince == nil ||
		status.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered {
		return nil
	}

	// Do nothing if the test workflows have not completed successfully
	if strategy.Gate != nil {
		if len(status.TargetVersion.TaskQueues) == 0 {
			return nil
		}
		if len(status.TargetVersion.TestWorkflows) < len(status.TargetVersion.TaskQueues) {
			return nil
		}
		for _, wf := range status.TargetVersion.TestWorkflows {
			if wf.Status != temporaliov1alpha1.WorkflowExecutionStatusCompleted {
				return nil
			}
		}
	}

	// Use deployment name from parameter and build ID from status
	buildID := status.TargetVersion.BuildID

	vcfg := &VersionConfig{
		ConflictToken:  conflictToken,
		DeploymentName: workerDeploymentName,
		BuildID:        buildID,
	}

	// If there is no current version, set the target version as the current version
	if status.CurrentVersion == nil {
		vcfg.SetCurrent = true
		return vcfg
	}

	// If the current version is the target version
	if status.CurrentVersion.BuildID == status.TargetVersion.BuildID {
		// Reset ramp if needed, this would happen if a ramp has been rolled back before completing
		if temporalState.RampingBuildID != "" {
			vcfg.RampPercentage = 0
			return vcfg
		}
		// Otherwise, do nothing
		return nil
	}

	switch strategy.Strategy {
	case temporaliov1alpha1.UpdateManual:
		return nil
	case temporaliov1alpha1.UpdateAllAtOnce:
		// Set new current version immediately
		vcfg.SetCurrent = true
		return vcfg
	case temporaliov1alpha1.UpdateProgressive:
		return handleProgressiveRollout(strategy.Steps, time.Now(), status.TargetVersion.RampLastModifiedAt, status.TargetVersion.RampPercentage, vcfg)
	}

	return nil
}

// handleProgressiveRollout handles the progressive rollout strategy logic
func handleProgressiveRollout(
	steps []temporaliov1alpha1.RolloutStep,
	currentTime time.Time, // avoid calling time.Now() inside function to make it easier to test
	rampLastModifiedAt *metav1.Time,
	targetRampPercentage *float32,
	vcfg *VersionConfig,
) *VersionConfig {
	// Protect against modifying the current version right away if there are no steps.
	//
	// The validating admission webhook _should_ prevent creating rollouts with 0 steps,
	// but just in case validation is skipped we should go with the more conservative
	// behavior of not updating the current version from the controller.
	if len(steps) == 0 {
		return nil
	}

	// Get the currently active step
	i := getCurrentStepIndex(steps, targetRampPercentage)
	currentStep := steps[i]

	// If this is the first step and there is no ramp percentage set, set the ramp percentage
	// to the step's ramp percentage.
	if targetRampPercentage == nil {
		vcfg.RampPercentage = currentStep.RampPercentage
		return vcfg
	}

	// If the target ramp percentage doesn't match the current step's defined ramp, the ramp
	// is reset immediately. This might be considered overly conservative, but it guarantees that
	// rollouts resume from the earliest possible step, and that at least the last step is always
	// respected (both % and duration).
	if *targetRampPercentage != currentStep.RampPercentage {
		vcfg.RampPercentage = currentStep.RampPercentage
		return vcfg
	}

	// Move to the next step if it has been long enough since the last update
	if rampLastModifiedAt != nil {
		if rampLastModifiedAt.Add(currentStep.PauseDuration.Duration).Before(currentTime) {
			if i < len(steps)-1 {
				vcfg.RampPercentage = steps[i+1].RampPercentage
				return vcfg
			} else {
				vcfg.SetCurrent = true
				return vcfg
			}
		}
	}

	// In all other cases, do nothing
	return nil
}

func getCurrentStepIndex(steps []temporaliov1alpha1.RolloutStep, targetRampPercentage *float32) int {
	if targetRampPercentage == nil {
		return 0
	}

	var result int
	for i, s := range steps {
		// Break if ramp percentage is greater than current (use last index)
		if s.RampPercentage > *targetRampPercentage {
			break
		}
		result = i
	}

	return result
}
