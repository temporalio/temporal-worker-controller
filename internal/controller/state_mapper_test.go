// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMapToStatus(t *testing.T) {
	// Set up test data
	now := time.Now()
	rampingSince := metav1.NewTime(now.Add(-10 * time.Minute))
	drainedSince := now.Add(-1 * time.Hour)
	healthySince := metav1.NewTime(now.Add(-2 * time.Hour))

	// Create Kubernetes state
	k8sState := &k8s.DeploymentState{
		Deployments: map[string]*appsv1.Deployment{
			"v1": {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-v1",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:               appsv1.DeploymentAvailable,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: healthySince,
						},
					},
				},
			},
			"v2": {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-v2",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:               appsv1.DeploymentAvailable,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: healthySince,
						},
					},
				},
			},
			"v3": {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-v3",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{},
				},
			},
		},
		DeploymentRefs: map[string]*corev1.ObjectReference{
			"v1": {
				Kind:      "Deployment",
				Name:      "worker-v1",
				Namespace: "default",
				UID:       types.UID("v1-uid"),
			},
			"v2": {
				Kind:      "Deployment",
				Name:      "worker-v2",
				Namespace: "default",
				UID:       types.UID("v2-uid"),
			},
			"v3": {
				Kind:      "Deployment",
				Name:      "worker-v3",
				Namespace: "default",
				UID:       types.UID("v3-uid"),
			},
		},
	}

	// Create Temporal state
	temporalState := &temporal.TemporalWorkerState{
		CurrentBuildID:       "v1",
		RampingBuildID:       "v2",
		RampPercentage:       25.0,
		RampingSince:         &rampingSince,
		VersionConflictToken: []byte("test-token"),
		Versions: map[string]*temporal.VersionInfo{
			"v1": {
				DeploymentName: "worker",
				BuildID:        "v1",
				Status:         temporaliov1alpha1.VersionStatusCurrent,
				TaskQueues: []temporaliov1alpha1.TaskQueue{
					{Name: "queue1"},
				},
			},
			"v2": {
				DeploymentName: "worker",
				BuildID:        "v2",
				Status:         temporaliov1alpha1.VersionStatusRamping,
				TaskQueues: []temporaliov1alpha1.TaskQueue{
					{Name: "queue1"},
				},
				TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
					{
						WorkflowID: "test-wf-2",
						RunID:      "run2",
						TaskQueue:  "queue1",
						Status:     temporaliov1alpha1.WorkflowExecutionStatusRunning,
					},
				},
			},
			"v3": {
				DeploymentName: "worker",
				BuildID:        "v3",
				Status:         temporaliov1alpha1.VersionStatusDrained,
				DrainedSince:   &drainedSince,
			},
		},
	}

	// Create state mapper
	mapper := newStateMapper(k8sState, temporalState, "worker")

	// Map to status (now takes buildID instead of versionID)
	status := mapper.mapToStatus("v2")

	// Verify status
	assert.Equal(t, []byte("test-token"), status.VersionConflictToken)

	// Verify default version
	assert.NotNil(t, status.CurrentVersion)
	assert.Equal(t, "v1", status.CurrentVersion.BuildID)

	// Convert to string for comparison
	expectedStatus := string(temporaliov1alpha1.VersionStatusCurrent)
	actualStatus := string(status.CurrentVersion.Status)
	assert.Equal(t, expectedStatus, actualStatus)

	assert.Equal(t, 1, len(status.CurrentVersion.TaskQueues))
	assert.Equal(t, "queue1", status.CurrentVersion.TaskQueues[0].Name)

	// Verify target version
	assert.NotNil(t, status.TargetVersion)
	assert.Equal(t, "v2", status.TargetVersion.BuildID)

	// Convert to string for comparison
	expectedRampingStatus := string(temporaliov1alpha1.VersionStatusRamping)
	actualRampingStatus := string(status.TargetVersion.Status)
	assert.Equal(t, expectedRampingStatus, actualRampingStatus)

	assert.NotNil(t, status.TargetVersion.RampPercentage)
	assert.Equal(t, float32(25.0), *status.TargetVersion.RampPercentage)
	assert.Equal(t, rampingSince.Time.Unix(), status.TargetVersion.RampingSince.Time.Unix())
	assert.Equal(t, 1, len(status.TargetVersion.TaskQueues))
	assert.Equal(t, "queue1", status.TargetVersion.TaskQueues[0].Name)
	assert.Equal(t, 1, len(status.TargetVersion.TestWorkflows))
	assert.Equal(t, "test-wf-2", status.TargetVersion.TestWorkflows[0].WorkflowID)
	assert.Equal(t, temporaliov1alpha1.WorkflowExecutionStatusRunning, status.TargetVersion.TestWorkflows[0].Status)

	// Verify deprecated versions
	assert.Equal(t, 1, len(status.DeprecatedVersions))
	assert.Equal(t, "v3", status.DeprecatedVersions[0].BuildID)

	// Convert to string for comparison
	expectedDrainedStatus := string(temporaliov1alpha1.VersionStatusDrained)
	actualDrainedStatus := string(status.DeprecatedVersions[0].Status)
	assert.Equal(t, expectedDrainedStatus, actualDrainedStatus)

	assert.NotNil(t, status.DeprecatedVersions[0].DrainedSince)
	assert.Equal(t, drainedSince.Unix(), status.DeprecatedVersions[0].DrainedSince.Time.Unix())

	// Verify version count is set correctly from VersionSummaries (via Versions map)
	// Should count: worker.v1, worker.v2, worker.v3 (3 versions total)
	assert.Equal(t, int32(3), status.VersionCount)
}

func TestMapWorkerDeploymentVersion(t *testing.T) {
	// Set up test data
	now := time.Now()
	healthySince := metav1.NewTime(now.Add(-1 * time.Hour))
	drainedSince := now.Add(-30 * time.Minute)

	k8sState := &k8s.DeploymentState{
		Deployments: map[string]*appsv1.Deployment{
			"v1": {
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:               appsv1.DeploymentAvailable,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: healthySince,
						},
					},
				},
			},
		},
		DeploymentRefs: map[string]*corev1.ObjectReference{
			"v1": {
				Kind:      "Deployment",
				Name:      "worker-v1",
				Namespace: "default",
				UID:       types.UID("test-uid"),
			},
		},
	}

	temporalState := &temporal.TemporalWorkerState{
		Versions: map[string]*temporal.VersionInfo{
			"v1": {
				DeploymentName: "worker",
				BuildID:        "v1",
				Status:         temporaliov1alpha1.VersionStatusCurrent,
				DrainedSince:   &drainedSince,
			},
		},
	}

	mapper := newStateMapper(k8sState, temporalState, "worker")

	// Test current version mapping
	currentVersion := mapper.mapCurrentWorkerDeploymentVersionByBuildID("v1")
	assert.NotNil(t, currentVersion)
	assert.Equal(t, "v1", currentVersion.BuildID)
	assert.Equal(t, temporaliov1alpha1.VersionStatusCurrent, currentVersion.Status)
	assert.NotNil(t, currentVersion.HealthySince)
	assert.Equal(t, healthySince.Time.Unix(), currentVersion.HealthySince.Time.Unix())
	assert.NotNil(t, currentVersion.Deployment)
	assert.Equal(t, "worker-v1", currentVersion.Deployment.Name)

	// Test target version mapping
	targetVersion := mapper.mapTargetWorkerDeploymentVersionByBuildID("v1")
	assert.Equal(t, "v1", targetVersion.BuildID)
	assert.Equal(t, temporaliov1alpha1.VersionStatusCurrent, targetVersion.Status)
	assert.NotNil(t, targetVersion.HealthySince)
	assert.Equal(t, healthySince.Time.Unix(), targetVersion.HealthySince.Time.Unix())
	assert.NotNil(t, targetVersion.Deployment)
	assert.Equal(t, "worker-v1", targetVersion.Deployment.Name)

	// Test deprecated version mapping
	deprecatedVersion := mapper.mapDeprecatedWorkerDeploymentVersionByBuildID("v1")
	assert.NotNil(t, deprecatedVersion)
	assert.Equal(t, "v1", deprecatedVersion.BuildID)
	assert.Equal(t, temporaliov1alpha1.VersionStatusCurrent, deprecatedVersion.Status)
	assert.NotNil(t, deprecatedVersion.HealthySince)
	assert.Equal(t, healthySince.Time.Unix(), deprecatedVersion.HealthySince.Time.Unix())
	assert.NotNil(t, deprecatedVersion.DrainedSince)
	assert.Equal(t, drainedSince.Unix(), deprecatedVersion.DrainedSince.Time.Unix())
	assert.NotNil(t, deprecatedVersion.Deployment)
	assert.Equal(t, "worker-v1", deprecatedVersion.Deployment.Name)

	// Test with version that doesn't exist
	currentVersion = mapper.mapCurrentWorkerDeploymentVersionByBuildID("nonexistent")
	assert.NotNil(t, currentVersion)
	assert.Equal(t, "nonexistent", currentVersion.BuildID)
	assert.Equal(t, temporaliov1alpha1.VersionStatusNotRegistered, currentVersion.Status)
	assert.Nil(t, currentVersion.HealthySince)
	assert.Nil(t, currentVersion.Deployment)
}
