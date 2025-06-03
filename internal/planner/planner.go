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
)

// Plan represents the actions to be taken to move the system to the desired state
type Plan struct {
	// Which actions to take
	DeleteDeployments      []*appsv1.Deployment
	ScaleDeployments       map[*v1.ObjectReference]uint32
	ShouldCreateDeployment bool
	VersionConfig          *VersionConfig
	TestWorkflows          []WorkflowConfig
}

// VersionConfig represents version routing configuration
type VersionConfig struct {
	// Token to use for conflict detection
	ConflictToken []byte
	// Version ID for which this config applies
	VersionID string

	// One of RampPercentage OR SetDefault must be set to a non-zero value.

	// Set this as the default build ID for all new executions
	SetDefault bool
	// Acceptable values [0,100]
	RampPercentage float32
}

// WorkflowConfig represents a workflow to be started
type WorkflowConfig struct {
	WorkflowType string
	WorkflowID   string
	VersionID    string
	TaskQueue    string
}

// Config holds the inputs needed to generate a plan
type Config struct {
	// Status of the TemporalWorkerDeployment
	Status *temporaliov1alpha1.TemporalWorkerDeploymentStatus
	// Spec of the TemporalWorkerDeployment
	Spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec
	// RolloutStrategy to use
	RolloutStrategy temporaliov1alpha1.RolloutStrategy
	// Desired version ID to deploy
	DesiredVersionID string
	// Number of replicas desired
	Replicas int32
	// Token to use for conflict detection
	ConflictToken []byte
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
	config *Config,
) (*Plan, error) {
	plan := &Plan{
		ScaleDeployments: make(map[*v1.ObjectReference]uint32),
	}

	// Add delete/scale operations based on version status
	plan.DeleteDeployments = getDeleteDeployments(k8sState, config)
	plan.ScaleDeployments = getScaleDeployments(k8sState, config)
	plan.ShouldCreateDeployment = shouldCreateDeployment(k8sState, config)

	// Determine if we need to start any test workflows
	plan.TestWorkflows = getTestWorkflows(config)

	// Determine version config changes
	plan.VersionConfig = getVersionConfigDiff(l, config.RolloutStrategy, config.Status, config.ConflictToken)

	return plan, nil
}

// getDeleteDeployments determines which deployments should be deleted
func getDeleteDeployments(
	k8sState *k8s.DeploymentState,
	config *Config,
) []*appsv1.Deployment {
	var deleteDeployments []*appsv1.Deployment

	for _, version := range config.Status.DeprecatedVersions {
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
			if (time.Since(version.DrainedSince.Time) > getDeleteDelay(config.Spec)+getScaledownDelay(config.Spec)) &&
				*d.Spec.Replicas == 0 {
				deleteDeployments = append(deleteDeployments, d)
			}
		case temporaliov1alpha1.VersionStatusNotRegistered:
			// NotRegistered versions are versions that the server doesn't know about.
			// Only delete if it's not the target version.
			if version != config.Status.TargetVersion {
				deleteDeployments = append(deleteDeployments, d)
			}
		}
	}

	// If the desired version ID has changed, delete the latest unregistered deployment
	if config.Status.TargetVersion != nil && config.Status.TargetVersion.Deployment != nil &&
		config.Status.TargetVersion.VersionID != config.DesiredVersionID {
		if d, exists := k8sState.Deployments[config.Status.TargetVersion.VersionID]; exists {
			deleteDeployments = append(deleteDeployments, d)
		}
	}

	return deleteDeployments
}

// getScaleDeployments determines which deployments should be scaled and to what size
func getScaleDeployments(
	k8sState *k8s.DeploymentState,
	config *Config,
) map[*v1.ObjectReference]uint32 {
	scaleDeployments := make(map[*v1.ObjectReference]uint32)

	// Scale the default version if needed
	if config.Status.DefaultVersion != nil && config.Status.DefaultVersion.Deployment != nil {
		ref := config.Status.DefaultVersion.Deployment
		if d, exists := k8sState.Deployments[config.Status.DefaultVersion.VersionID]; exists {
			if d.Spec.Replicas != nil && *d.Spec.Replicas != config.Replicas {
				scaleDeployments[ref] = uint32(config.Replicas)
			}
		}
	}

	// Scale other versions based on status
	for _, version := range config.Status.DeprecatedVersions {
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
			// Scale up these deployments
			if d.Spec.Replicas != nil && *d.Spec.Replicas != config.Replicas {
				scaleDeployments[version.Deployment] = uint32(config.Replicas)
			}
		case temporaliov1alpha1.VersionStatusDrained:
			if time.Since(version.DrainedSince.Time) > getScaledownDelay(config.Spec) {
				// Scale down drained deployments after delay
				if d.Spec.Replicas != nil && *d.Spec.Replicas != 0 {
					scaleDeployments[version.Deployment] = 0
				}
			}
		}
	}

	// Scale the target version if it exists
	if config.Status.TargetVersion != nil && config.Status.TargetVersion.Deployment != nil &&
		config.Status.TargetVersion.VersionID == config.DesiredVersionID {
		if d, exists := k8sState.Deployments[config.Status.TargetVersion.VersionID]; exists {
			if d.Spec.Replicas == nil || *d.Spec.Replicas != config.Replicas {
				scaleDeployments[config.Status.TargetVersion.Deployment] = uint32(config.Replicas)
			}
		}
	}

	return scaleDeployments
}

// shouldCreateDeployment determines if a new deployment needs to be created
func shouldCreateDeployment(
	k8sState *k8s.DeploymentState,
	config *Config,
) bool {
	if config.Status.TargetVersion == nil {
		return true
	}

	if config.Status.TargetVersion.Deployment == nil {
		return true
	}

	// If the desired version already has a deployment, we don't need to create another one
	if config.Status.TargetVersion.VersionID == config.DesiredVersionID {
		if _, exists := k8sState.Deployments[config.DesiredVersionID]; exists {
			return false
		}
	}

	return true
}

// getTestWorkflows determines which test workflows should be started
func getTestWorkflows(
	config *Config,
) []WorkflowConfig {
	var testWorkflows []WorkflowConfig

	// Skip if there's no gate workflow defined or if the target version is already the default
	if config.RolloutStrategy.Gate == nil || config.Status.TargetVersion == nil ||
		config.Status.DefaultVersion == nil ||
		config.Status.DefaultVersion.VersionID == config.Status.TargetVersion.VersionID {
		return nil
	}

	targetVersion := config.Status.TargetVersion

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
				WorkflowID:   getTestWorkflowID(config, tq.Name, targetVersion.VersionID),
				VersionID:    targetVersion.VersionID,
				TaskQueue:    tq.Name,
			})
		}
	}

	return testWorkflows
}

// getTestWorkflowID generates an ID for a test workflow
func getTestWorkflowID(config *Config, taskQueue, versionID string) string {
	return "test-" + versionID + "-" + taskQueue
}

// getVersionConfigDiff determines if version config needs to be updated
func getVersionConfigDiff(
	l logr.Logger,
	strategy temporaliov1alpha1.RolloutStrategy,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	conflictToken []byte,
) *VersionConfig {
	vcfg := getVersionConfig(l, strategy, status)
	if vcfg == nil {
		return nil
	}

	// Check if target version is nil
	if status == nil || status.TargetVersion == nil {
		return nil
	}

	vcfg.VersionID = status.TargetVersion.VersionID
	vcfg.ConflictToken = conflictToken

	// Set default version if there isn't one yet
	if status.DefaultVersion == nil {
		vcfg.SetDefault = true
		vcfg.RampPercentage = 0
		return vcfg
	}

	// Don't make updates if desired default is already the default
	if vcfg.VersionID == status.DefaultVersion.VersionID {
		return nil
	}

	// Don't make updates if desired ramping version is already the target, and ramp percentage is correct
	if !vcfg.SetDefault &&
		vcfg.VersionID == status.TargetVersion.VersionID &&
		status.TargetVersion.RampPercentage != nil &&
		vcfg.RampPercentage == *status.TargetVersion.RampPercentage {
		return nil
	}

	return vcfg
}

// getVersionConfig determines the version configuration based on the rollout strategy
func getVersionConfig(
	l logr.Logger,
	strategy temporaliov1alpha1.RolloutStrategy,
	status *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
) *VersionConfig {
	// Do nothing if target version's deployment is not healthy yet
	if status == nil || status.TargetVersion == nil || status.TargetVersion.HealthySince == nil {
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

	switch strategy.Strategy {
	case temporaliov1alpha1.UpdateManual:
		return nil
	case temporaliov1alpha1.UpdateAllAtOnce:
		// Set new default version immediately
		return &VersionConfig{
			SetDefault: true,
		}
	case temporaliov1alpha1.UpdateProgressive:
		// Determine the correct percentage ramp
		var (
			healthyDuration    time.Duration
			currentRamp        float32
			totalPauseDuration = healthyDuration
		)
		if status.TargetVersion.RampingSince != nil {
			healthyDuration = time.Since(status.TargetVersion.RampingSince.Time)
		}
		for _, s := range strategy.Steps {
			if s.RampPercentage != 0 {
				currentRamp = s.RampPercentage
			}
			totalPauseDuration += s.PauseDuration.Duration
			if healthyDuration < totalPauseDuration {
				// We're still in this step's pause duration
				return &VersionConfig{
					RampPercentage: currentRamp,
				}
			}
		}
		// We've progressed through all steps; it should now be safe to update the default version
		return &VersionConfig{
			SetDefault: true,
		}
	}

	return nil
}
