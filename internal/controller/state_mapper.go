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

// stateMapper maps between Kubernetes and Temporal states
type stateMapper struct {
	k8sState      *k8s.DeploymentState
	temporalState *temporal.TemporalWorkerState
}

// newStateMapper creates a new state mapper
func newStateMapper(k8sState *k8s.DeploymentState, temporalState *temporal.TemporalWorkerState) *stateMapper {
	return &stateMapper{
		k8sState:      k8sState,
		temporalState: temporalState,
	}
}

// mapToStatus converts the states to a CRD status
func (m *stateMapper) mapToStatus(desiredVersionID string) *v1alpha1.TemporalWorkerDeploymentStatus {
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
func (m *stateMapper) mapWorkerDeploymentVersion(versionID string) *v1alpha1.WorkerDeploymentVersion {
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
		version.Status = temporalVersion.Status

		// Set drained since if available
		if temporalVersion.DrainedSince != nil {
			drainedSince := metav1.NewTime(*temporalVersion.DrainedSince)
			version.DrainedSince = &drainedSince
		}

		// Set ramp percentage if this is a ramping version
		// TODO(carlydf): Support setting any ramp in [0,100]
		// NOTE(rob): We are now setting any ramp > 0, is that correct?
		if temporalVersion.Status == v1alpha1.VersionStatusRamping && temporalVersion.RampPercentage > 0 {
			version.RampPercentage = &temporalVersion.RampPercentage
		}

		// Set task queues
		version.TaskQueues = append(version.TaskQueues, temporalVersion.TaskQueues...)

		// Set test workflows
		version.TestWorkflows = append(version.TestWorkflows, temporalVersion.TestWorkflows...)
	}

	return version
}
