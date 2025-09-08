// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stateMapper maps between Kubernetes and Temporal states
type stateMapper struct {
	k8sState             *k8s.DeploymentState
	temporalState        *temporal.TemporalWorkerState
	workerDeploymentName string
}

// newStateMapper creates a new state mapper
func newStateMapper(k8sState *k8s.DeploymentState, temporalState *temporal.TemporalWorkerState, workerDeploymentName string) *stateMapper {
	return &stateMapper{
		k8sState:             k8sState,
		temporalState:        temporalState,
		workerDeploymentName: workerDeploymentName,
	}
}

// mapToStatus converts the states to a CRD status
func (m *stateMapper) mapToStatus(targetBuildID string) *v1alpha1.TemporalWorkerDeploymentStatus {
	status := &v1alpha1.TemporalWorkerDeploymentStatus{
		VersionConflictToken: m.temporalState.VersionConflictToken,
	}

	status.LastModifierIdentity = m.temporalState.LastModifierIdentity

	// Get build IDs directly from temporal state
	currentBuildID := m.temporalState.CurrentBuildID
	rampingBuildID := m.temporalState.RampingBuildID

	// Set current version
	status.CurrentVersion = m.mapCurrentWorkerDeploymentVersionByBuildID(currentBuildID)

	// Set target version (desired version)
	status.TargetVersion = m.mapTargetWorkerDeploymentVersionByBuildID(targetBuildID)
	if rampingBuildID == targetBuildID {
		status.TargetVersion.RampingSince = m.temporalState.RampingSince
		status.TargetVersion.RampLastModifiedAt = m.temporalState.RampLastModifiedAt
		rampPercentageBasisPoints := int32(m.temporalState.RampPercentage * 100)
		status.TargetVersion.RampPercentageBasisPoints = &rampPercentageBasisPoints
	}

	// Add deprecated versions
	var deprecatedVersions []*v1alpha1.DeprecatedWorkerDeploymentVersion
	for buildID := range m.k8sState.Deployments {
		// Skip current and target versions
		if buildID == currentBuildID || buildID == targetBuildID {
			continue
		}

		versionStatus := m.mapDeprecatedWorkerDeploymentVersionByBuildID(buildID)
		if versionStatus != nil {
			deprecatedVersions = append(deprecatedVersions, versionStatus)
		}
	}
	status.DeprecatedVersions = deprecatedVersions

	// Set version count from temporal state (directly from VersionSummaries via Versions map)
	status.VersionCount = int32(len(m.temporalState.Versions))

	return status
}

// mapCurrentWorkerDeploymentVersionByBuildID creates a current version status from the states using buildID
func (m *stateMapper) mapCurrentWorkerDeploymentVersionByBuildID(buildID string) *v1alpha1.CurrentWorkerDeploymentVersion {
	if buildID == "" {
		return nil
	}

	version := &v1alpha1.CurrentWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{
			BuildID: buildID,
			Status:  v1alpha1.VersionStatusNotRegistered,
		},
	}

	// Set deployment reference if it exists
	if deployment, exists := m.k8sState.Deployments[buildID]; exists {
		version.Deployment = m.k8sState.DeploymentRefs[buildID]

		// Check deployment health
		healthy, healthySince := k8s.IsDeploymentHealthy(deployment)
		if healthy {
			version.HealthySince = healthySince
		}
	}

	// Set version status from temporal state
	if temporalVersion, exists := m.temporalState.Versions[buildID]; exists {
		version.Status = temporalVersion.Status

		// Set task queues
		version.TaskQueues = append(version.TaskQueues, temporalVersion.TaskQueues...)
	}

	return version
}

// mapTargetWorkerDeploymentVersionByBuildID creates a target version status from the states using buildID
func (m *stateMapper) mapTargetWorkerDeploymentVersionByBuildID(buildID string) v1alpha1.TargetWorkerDeploymentVersion {
	version := v1alpha1.TargetWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{
			BuildID: buildID,
			Status:  v1alpha1.VersionStatusNotRegistered,
		},
	}

	if buildID == "" {
		return version
	}

	// Set deployment reference if it exists
	if deployment, exists := m.k8sState.Deployments[buildID]; exists {
		version.Deployment = m.k8sState.DeploymentRefs[buildID]

		// Check deployment health
		healthy, healthySince := k8s.IsDeploymentHealthy(deployment)
		if healthy {
			version.HealthySince = healthySince
		}
	}

	// Set version status from temporal state
	if temporalVersion, exists := m.temporalState.Versions[buildID]; exists {
		version.Status = temporalVersion.Status

		// Set ramp percentage if this is a ramping version
		if temporalVersion.Status == v1alpha1.VersionStatusRamping && m.temporalState.RampPercentage > 0 {
			rampPercentageBasisPoints := int32(m.temporalState.RampPercentage * 100)
			version.RampPercentageBasisPoints = &rampPercentageBasisPoints
		}

		// Set task queues
		version.TaskQueues = append(version.TaskQueues, temporalVersion.TaskQueues...)

		// Set test workflows
		version.TestWorkflows = append(version.TestWorkflows, temporalVersion.TestWorkflows...)
	}

	return version
}

// mapDeprecatedWorkerDeploymentVersionByBuildID creates a deprecated version status from the states using buildID
func (m *stateMapper) mapDeprecatedWorkerDeploymentVersionByBuildID(buildID string) *v1alpha1.DeprecatedWorkerDeploymentVersion {
	if buildID == "" {
		return nil
	}

	eligibleForDeletion := false
	if vInfo, exists := m.temporalState.Versions[buildID]; exists {
		eligibleForDeletion = vInfo.Status == v1alpha1.VersionStatusDrained && vInfo.NoTaskQueuesHaveVersionedPoller
	}

	version := &v1alpha1.DeprecatedWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{
			BuildID: buildID,
			Status:  v1alpha1.VersionStatusNotRegistered,
		},
		EligibleForDeletion: eligibleForDeletion,
	}

	// Set deployment reference if it exists
	if deployment, exists := m.k8sState.Deployments[buildID]; exists {
		version.Deployment = m.k8sState.DeploymentRefs[buildID]

		// Check deployment health
		healthy, healthySince := k8s.IsDeploymentHealthy(deployment)
		if healthy {
			version.HealthySince = healthySince
		}
	}

	// Set version status from temporal state
	if temporalVersion, exists := m.temporalState.Versions[buildID]; exists {
		version.Status = temporalVersion.Status

		// Set drained since if available
		if temporalVersion.DrainedSince != nil {
			drainedSince := metav1.NewTime(*temporalVersion.DrainedSince)
			version.DrainedSince = &drainedSince
		}

		// Set task queues
		version.TaskQueues = append(version.TaskQueues, temporalVersion.TaskQueues...)
	}

	return version
}
