// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/k8s"
	"github.com/DataDog/temporal-worker-controller/internal/temporal"
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
			"worker.v1": {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-v1",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:               appsv1.DeploymentAvailable,
							Status:             v1.ConditionTrue,
							LastTransitionTime: healthySince,
						},
					},
				},
			},
			"worker.v2": {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-v2",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:               appsv1.DeploymentAvailable,
							Status:             v1.ConditionTrue,
							LastTransitionTime: healthySince,
						},
					},
				},
			},
			"worker.v3": {
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-v3",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{},
				},
			},
		},
		DeploymentRefs: map[string]*v1.ObjectReference{
			"worker.v1": {
				Kind:      "Deployment",
				Name:      "worker-v1",
				Namespace: "default",
				UID:       types.UID("v1-uid"),
			},
			"worker.v2": {
				Kind:      "Deployment",
				Name:      "worker-v2",
				Namespace: "default",
				UID:       types.UID("v2-uid"),
			},
			"worker.v3": {
				Kind:      "Deployment",
				Name:      "worker-v3",
				Namespace: "default",
				UID:       types.UID("v3-uid"),
			},
		},
	}

	// Create Temporal state
	temporalState := &temporal.TemporalWorkerState{
		DefaultVersionID:     "worker.v1",
		RampingVersionID:     "worker.v2",
		RampPercentage:       25.0,
		RampingSince:         &rampingSince,
		VersionConflictToken: []byte("test-token"),
		Versions: map[string]*temporal.VersionInfo{
			"worker.v1": {
				VersionID:      "worker.v1",
				Status:         temporaliov1alpha1.VersionStatusCurrent,
				RampPercentage: 100,
				TaskQueues: []temporaliov1alpha1.TaskQueue{
					{Name: "queue1"},
				},
				TestWorkflows: []temporaliov1alpha1.WorkflowExecution{
					{
						WorkflowID: "test-wf-1",
						RunID:      "run1",
						TaskQueue:  "queue1",
						Status:     temporaliov1alpha1.WorkflowExecutionStatusCompleted,
					},
				},
			},
			"worker.v2": {
				VersionID:      "worker.v2",
				Status:         temporaliov1alpha1.VersionStatusRamping,
				RampPercentage: 25.0,
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
			"worker.v3": {
				VersionID:    "worker.v3",
				Status:       temporaliov1alpha1.VersionStatusDrained,
				DrainedSince: &drainedSince,
			},
		},
	}

	// Create state mapper
	mapper := newStateMapper(k8sState, temporalState)

	// Map to status
	status := mapper.mapToStatus("worker.v2")

	// Verify status
	assert.Equal(t, []byte("test-token"), status.VersionConflictToken)

	// Verify default version
	assert.NotNil(t, status.DefaultVersion)
	assert.Equal(t, "worker.v1", status.DefaultVersion.VersionID)

	// Convert to string for comparison
	expectedStatus := string(temporaliov1alpha1.VersionStatusCurrent)
	actualStatus := string(status.DefaultVersion.Status)
	assert.Equal(t, expectedStatus, actualStatus)

	assert.Equal(t, 1, len(status.DefaultVersion.TaskQueues))
	assert.Equal(t, "queue1", status.DefaultVersion.TaskQueues[0].Name)
	assert.Equal(t, 1, len(status.DefaultVersion.TestWorkflows))
	assert.Equal(t, "test-wf-1", status.DefaultVersion.TestWorkflows[0].WorkflowID)
	assert.Equal(t, temporaliov1alpha1.WorkflowExecutionStatusCompleted, status.DefaultVersion.TestWorkflows[0].Status)

	// Verify target version
	assert.NotNil(t, status.TargetVersion)
	assert.Equal(t, "worker.v2", status.TargetVersion.VersionID)

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
	assert.Equal(t, "worker.v3", status.DeprecatedVersions[0].VersionID)

	// Convert to string for comparison
	expectedDrainedStatus := string(temporaliov1alpha1.VersionStatusDrained)
	actualDrainedStatus := string(status.DeprecatedVersions[0].Status)
	assert.Equal(t, expectedDrainedStatus, actualDrainedStatus)

	assert.NotNil(t, status.DeprecatedVersions[0].DrainedSince)
	assert.Equal(t, drainedSince.Unix(), status.DeprecatedVersions[0].DrainedSince.Time.Unix())
}

func TestMapWorkerDeploymentVersion(t *testing.T) {
	// Set up test data
	now := time.Now()
	healthySince := metav1.NewTime(now.Add(-1 * time.Hour))
	drainedSince := now.Add(-30 * time.Minute)

	k8sState := &k8s.DeploymentState{
		Deployments: map[string]*appsv1.Deployment{
			"worker.v1": {
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:               appsv1.DeploymentAvailable,
							Status:             v1.ConditionTrue,
							LastTransitionTime: healthySince,
						},
					},
				},
			},
		},
		DeploymentRefs: map[string]*v1.ObjectReference{
			"worker.v1": {
				Kind:      "Deployment",
				Name:      "worker-v1",
				Namespace: "default",
				UID:       types.UID("test-uid"),
			},
		},
	}

	temporalState := &temporal.TemporalWorkerState{
		Versions: map[string]*temporal.VersionInfo{
			"worker.v1": {
				VersionID:      "worker.v1",
				Status:         temporaliov1alpha1.VersionStatusCurrent,
				RampPercentage: 100,
				DrainedSince:   &drainedSince,
			},
		},
	}

	mapper := newStateMapper(k8sState, temporalState)

	// Test with registered version
	version := mapper.mapWorkerDeploymentVersion("worker.v1")
	assert.NotNil(t, version)
	assert.Equal(t, "worker.v1", version.VersionID)
	assert.Equal(t, temporaliov1alpha1.VersionStatusCurrent, version.Status)
	assert.NotNil(t, version.HealthySince)
	assert.Equal(t, healthySince.Time.Unix(), version.HealthySince.Time.Unix())
	assert.NotNil(t, version.DrainedSince)
	assert.Equal(t, drainedSince.Unix(), version.DrainedSince.Time.Unix())
	assert.NotNil(t, version.Deployment)
	assert.Equal(t, "worker-v1", version.Deployment.Name)

	// Test with version that doesn't exist
	version = mapper.mapWorkerDeploymentVersion("nonexistent")
	assert.NotNil(t, version)
	assert.Equal(t, "nonexistent", version.VersionID)
	assert.Equal(t, temporaliov1alpha1.VersionStatusNotRegistered, version.Status)
	assert.Nil(t, version.HealthySince)
	assert.Nil(t, version.DrainedSince)
	assert.Nil(t, version.Deployment)
}
