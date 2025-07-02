// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/k8s"
	"github.com/DataDog/temporal-worker-controller/internal/temporal"
)

// Plan holds the actions to execute during reconciliation
type Plan struct {
	// Which actions to take
	DeleteDeployments      []*appsv1.Deployment
	ScaleDeployments       map[*v1.ObjectReference]uint32
	ShouldCreateDeployment bool
	VersionConfig          *VersionConfig
	TestWorkflows          []WorkflowConfig
}

// VersionConfig defines version configuration for Temporal
type VersionConfig struct {
	// Token to use for conflict detection
	ConflictToken []byte
	// Version ID for which this config applies
	VersionID string

	// One of RampPercentage OR SetCurrent must be set to a non-zero value.

	// Set this as the build ID for all new executions
	SetCurrent bool
	// Acceptable values [0,100]
	RampPercentage float32
}

// WorkflowConfig defines a workflow to be started
type WorkflowConfig struct {
	WorkflowType string
	WorkflowID   string
	VersionID    string
	TaskQueue    string
}

// Config holds the configuration for planning
type Config struct {
	// RolloutStrategy to use
	RolloutStrategy temporaliov1alpha1.RolloutStrategy
}

// ScaledownDelay returns the scaledown delay from the sunset strategy
func getScaledownDelay(spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec) time.Duration {
	if spec.SunsetStrategy.ScaledownDelay == nil {
		return 0
	}
	return spec.SunsetStrategy.ScaledownDelay.Duration
}

// DeleteDelay returns the delete delay from the sunset strategy
func getDeleteDelay(spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec) time.Duration {
	if spec.SunsetStrategy.DeleteDelay == nil {
		return 0
	}
	return spec.SunsetStrategy.DeleteDelay.Duration
}

// GeneratePlan creates a plan for updating the worker deployment
func GeneratePlan(
	l logr.Logger,
	k8sState *k8s.DeploymentState,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	temporalState *temporal.TemporalWorkerState,
	config *Config,
) (*Plan, error) {
	plan := &Plan{
		ScaleDeployments: make(map[*v1.ObjectReference]uint32),
	}

	// Add delete/scale operations based on version status
	plan.DeleteDeployments = getDeleteDeployments(k8sState, status, spec)
	plan.ScaleDeployments = getScaleDeployments(k8sState, status, spec)
	plan.ShouldCreateDeployment = shouldCreateDeployment(status)

	// Determine if we need to start any test workflows
	plan.TestWorkflows = getTestWorkflows(status, config)

	// Determine version config changes
	plan.VersionConfig = getVersionConfigDiff(l, status, spec, temporalState, config)

	// TODO(jlegrone): generate warnings/events on the TemporalWorkerDeployment resource when buildIDs are reachable
	//                 but have no corresponding Deployment.

	return plan, nil
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

		// Look up the deployment
		d, exists := k8sState.Deployments[version.VersionID]
		if !exists {
			continue
		}

		switch version.Status {
		case temporaliov1alpha1.VersionStatusDrained:
			// Deleting a deployment is only possible when:
			// 1. The deployment has been drained for deleteDelay + scaledownDelay.
			// 2. The deployment is scaled to 0 replicas.
			if (time.Since(version.DrainedSince.Time) > getDeleteDelay(spec)+getScaledownDelay(spec)) &&
				*d.Spec.Replicas == 0 {
				deleteDeployments = append(deleteDeployments, d)
			}
		case temporaliov1alpha1.VersionStatusNotRegistered:
			// NotRegistered versions are versions that the server doesn't know about.
			// Only delete if it's not the target version.
			if status.TargetVersion == nil || status.TargetVersion.VersionID != version.VersionID {
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
) map[*v1.ObjectReference]uint32 {
	scaleDeployments := make(map[*v1.ObjectReference]uint32)
	replicas := *spec.Replicas

	// Scale the current version if needed
	if status.CurrentVersion != nil && status.CurrentVersion.Deployment != nil {
		ref := status.CurrentVersion.Deployment
		if d, exists := k8sState.Deployments[status.CurrentVersion.VersionID]; exists {
			if d.Spec.Replicas != nil && *d.Spec.Replicas != replicas {
				scaleDeployments[ref] = uint32(replicas)
			}
		}
	}

	// Scale the target version if it exists, and isn't current
	if (status.CurrentVersion == nil || status.CurrentVersion.VersionID != status.TargetVersion.VersionID) &&
		status.TargetVersion.Deployment != nil {
		if d, exists := k8sState.Deployments[status.TargetVersion.VersionID]; exists {
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

		d, exists := k8sState.Deployments[version.VersionID]
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
			if time.Since(version.DrainedSince.Time) > getScaledownDelay(spec) {
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
) bool {
	return status.TargetVersion.Deployment == nil
}

// getTestWorkflows determines which test workflows should be started
func getTestWorkflows(
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	config *Config,
) []WorkflowConfig {
	var testWorkflows []WorkflowConfig

	// Skip if there's no gate workflow defined or if the target version is already the current
	if config.RolloutStrategy.Gate == nil ||
		status.CurrentVersion == nil ||
		status.CurrentVersion.VersionID == status.TargetVersion.VersionID {
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
			testWorkflows = append(testWorkflows, WorkflowConfig{
				WorkflowType: config.RolloutStrategy.Gate.WorkflowType,
				WorkflowID:   getTestWorkflowID(tq.Name, targetVersion.VersionID),
				VersionID:    targetVersion.VersionID,
				TaskQueue:    tq.Name,
			})
		}
	}

	return testWorkflows
}

// getTestWorkflowID generates an ID for a test workflow
func getTestWorkflowID(taskQueue, versionID string) string {
	return "test-" + versionID + "-" + taskQueue
}

// getVersionConfigDiff determines the version configuration based on the rollout strategy
func getVersionConfigDiff(
	l logr.Logger,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	temporalState *temporal.TemporalWorkerState,
	config *Config,
) *VersionConfig {
	strategy := config.RolloutStrategy
	conflictToken := status.VersionConflictToken

	// Do nothing if target version's deployment is not healthy yet
	if status == nil || status.TargetVersion.HealthySince == nil {
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

	vcfg := &VersionConfig{
		ConflictToken: conflictToken,
		VersionID:     status.TargetVersion.VersionID,
	}

	// If there is no current version, set the target version as the current version
	if status.CurrentVersion == nil {
		vcfg.SetCurrent = true
		return vcfg
	}

	// If the current version is the target version
	if status.CurrentVersion.VersionID == status.TargetVersion.VersionID {
		// Reset ramp if needed, this would happen if a ramp has been rolled back before completing
		if temporalState.RampingVersionID != "" {
			vcfg.VersionID = ""
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
		// Determine the correct percentage ramp
		var (
			healthyDuration    time.Duration
			currentRamp        float32
			totalPauseDuration = healthyDuration
		)
		if status.TargetVersion.RampingSince != nil {
			healthyDuration = time.Since(status.TargetVersion.RampingSince.Time)
			// TODO(carlydf): Is it important that the version spends x time at each step % ?
			// Currently, if 1% ramp is set, and then multiple reconcile loops error so the next steps aren't set,
			// the version could skip straight from 1% to current if the error-ing period > totalPauseDuration
		}
		for _, s := range strategy.Steps {
			if s.RampPercentage != 0 { // TODO(carlydf): Support setting any ramp in [0,100]
				currentRamp = s.RampPercentage
			}
			totalPauseDuration += s.PauseDuration.Duration
			if healthyDuration < totalPauseDuration {
				// We're still in this step's pause duration

				// If this step's ramp percentage is the same as the target version's ramp percentage, do nothing
				if status.TargetVersion.RampPercentage != nil && currentRamp == *status.TargetVersion.RampPercentage {
					return nil
				}

				vcfg.RampPercentage = currentRamp
				return vcfg
			}
		}
		// We've progressed through all steps; it should now be safe to update the default version
		vcfg.SetCurrent = true
		return vcfg
	}

	return nil
}
