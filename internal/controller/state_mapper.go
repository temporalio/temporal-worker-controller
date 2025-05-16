// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/k8s"
	"github.com/DataDog/temporal-worker-controller/internal/temporal"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StateMapper maps between Kubernetes and Temporal states
type StateMapper struct {
	k8sState      *k8s.DeploymentState
	temporalState *temporal.TemporalWorkerState
}

// NewStateMapper creates a new state mapper
func NewStateMapper(k8sState *k8s.DeploymentState, temporalState *temporal.TemporalWorkerState) *StateMapper {
	return &StateMapper{
		k8sState:      k8sState,
		temporalState: temporalState,
	}
}

// MapToStatus converts the states to a CRD status
func (m *StateMapper) MapToStatus(desiredVersionID string) *v1alpha1.TemporalWorkerDeploymentStatus {
	status := &v1alpha1.TemporalWorkerDeploymentStatus{
		VersionConflictToken: m.temporalState.VersionConflictToken,
	}

	// Set default version
	defaultVersionID := m.temporalState.DefaultVersionID
	if defaultVersionID != "" {
		status.DefaultVersion = m.mapWorkerDeploymentVersion(defaultVersionID)
	}

	// Set target version (desired version)
	status.TargetVersion = m.mapWorkerDeploymentVersion(desiredVersionID)
	if status.TargetVersion != nil && m.temporalState.RampingVersionID == desiredVersionID {
		status.TargetVersion.RampingSince = m.temporalState.RampingSince
		rampPercentage := m.temporalState.RampPercentage
		status.TargetVersion.RampPercentage = &rampPercentage
	}

	// Add deprecated versions
	var deprecatedVersions []*v1alpha1.WorkerDeploymentVersion
	for versionID := range m.k8sState.Deployments {
		// Skip default and target versions
		if versionID == defaultVersionID || versionID == desiredVersionID {
			continue
		}

		versionStatus := m.mapWorkerDeploymentVersion(versionID)
		if versionStatus != nil {
			deprecatedVersions = append(deprecatedVersions, versionStatus)
		}
	}
	status.DeprecatedVersions = deprecatedVersions

	return status
}

// mapWorkerDeploymentVersion creates a version status from the states
func (m *StateMapper) mapWorkerDeploymentVersion(versionID string) *v1alpha1.WorkerDeploymentVersion {
	if versionID == "" {
		return nil
	}

	version := &v1alpha1.WorkerDeploymentVersion{
		VersionID: versionID,
		Status:    v1alpha1.VersionStatusNotRegistered,
	}

	// Set deployment reference if it exists
	if deployment, exists := m.k8sState.Deployments[versionID]; exists {
		version.Deployment = m.k8sState.DeploymentRefs[versionID]

		// Check deployment health
		healthy, healthySince := k8s.IsDeploymentHealthy(deployment)
		if healthy {
			version.HealthySince = healthySince
		}
	}

	// Set version status from temporal state
	if temporalVersion, exists := m.temporalState.Versions[versionID]; exists {
		version.Status = mapVersionStatus(temporalVersion.Status)

		// Set drained since if available
		if temporalVersion.DrainedSince != nil {
			drainedSince := metav1.NewTime(*temporalVersion.DrainedSince)
			version.DrainedSince = &drainedSince
		}

		// Set ramp percentage if this is a ramping version
		// TODO(carlydf): Support setting any ramp in [0,100]
		// NOTE(rob): We are now setting any ramp > 0, is that correct?
		if temporalVersion.Status == temporal.VersionStatusRamping && temporalVersion.RampPercentage > 0 {
			version.RampPercentage = &temporalVersion.RampPercentage
		}

		// Set task queues
		for _, tq := range temporalVersion.TaskQueues {
			version.TaskQueues = append(version.TaskQueues, v1alpha1.TaskQueue{
				Name: tq.Name,
			})
		}

		// Set test workflows
		for _, wf := range temporalVersion.TestWorkflows {
			version.TestWorkflows = append(version.TestWorkflows, v1alpha1.WorkflowExecution{
				WorkflowID: wf.WorkflowID,
				RunID:      wf.RunID,
				TaskQueue:  wf.TaskQueue,
				Status:     wf.Status,
			})
		}
	}

	return version
}

// mapVersionStatus converts temporal version status to CRD version status
func mapVersionStatus(status temporal.VersionStatus) v1alpha1.VersionStatus {
	switch status {
	case temporal.VersionStatusNotRegistered:
		return v1alpha1.VersionStatusNotRegistered
	case temporal.VersionStatusInactive:
		return v1alpha1.VersionStatusInactive
	case temporal.VersionStatusRamping:
		return v1alpha1.VersionStatusRamping
	case temporal.VersionStatusCurrent:
		return v1alpha1.VersionStatusCurrent
	case temporal.VersionStatusDraining:
		return v1alpha1.VersionStatusDraining
	case temporal.VersionStatusDrained:
		return v1alpha1.VersionStatusDrained
	default:
		return v1alpha1.VersionStatusNotRegistered
	}
}
