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
				Status:         temporal.VersionStatusCurrent,
				RampPercentage: 100,
				TaskQueues: []temporal.TaskQueueInfo{
					{Name: "queue1"},
				},
				TestWorkflows: []temporal.WorkflowExecutionInfo{
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
				Status:         temporal.VersionStatusRamping,
				RampPercentage: 25.0,
				TaskQueues: []temporal.TaskQueueInfo{
					{Name: "queue1"},
				},
				TestWorkflows: []temporal.WorkflowExecutionInfo{
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
				Status:       temporal.VersionStatusDrained,
				DrainedSince: &drainedSince,
			},
		},
	}

	// Create state mapper
	mapper := NewStateMapper(k8sState, temporalState)

	// Map to status
	status := mapper.MapToStatus("worker.v2")

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

func TestMapVersionStatus(t *testing.T) {
	tests := []struct {
		name           string
		status         temporal.VersionStatus
		expectedStatus temporaliov1alpha1.VersionStatus
	}{
		{
			name:           "not registered",
			status:         temporal.VersionStatusNotRegistered,
			expectedStatus: temporaliov1alpha1.VersionStatusNotRegistered,
		},
		{
			name:           "inactive",
			status:         temporal.VersionStatusInactive,
			expectedStatus: temporaliov1alpha1.VersionStatusInactive,
		},
		{
			name:           "ramping",
			status:         temporal.VersionStatusRamping,
			expectedStatus: temporaliov1alpha1.VersionStatusRamping,
		},
		{
			name:           "current",
			status:         temporal.VersionStatusCurrent,
			expectedStatus: temporaliov1alpha1.VersionStatusCurrent,
		},
		{
			name:           "draining",
			status:         temporal.VersionStatusDraining,
			expectedStatus: temporaliov1alpha1.VersionStatusDraining,
		},
		{
			name:           "drained",
			status:         temporal.VersionStatusDrained,
			expectedStatus: temporaliov1alpha1.VersionStatusDrained,
		},
		{
			name:           "unknown",
			status:         temporal.VersionStatus("unknown"),
			expectedStatus: temporaliov1alpha1.VersionStatusNotRegistered,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := mapVersionStatus(tt.status)
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

func TestMapWorkerDeploymentVersion(t *testing.T) {
	// Set up test data
	now := time.Now()
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
		},
		DeploymentRefs: map[string]*v1.ObjectReference{
			"worker.v1": {
				Kind:      "Deployment",
				Name:      "worker-v1",
				Namespace: "default",
				UID:       types.UID("v1-uid"),
			},
		},
	}

	// Create Temporal state
	temporalState := &temporal.TemporalWorkerState{
		Versions: map[string]*temporal.VersionInfo{
			"worker.v1": {
				VersionID:      "worker.v1",
				Status:         temporal.VersionStatusCurrent,
				RampPercentage: 100,
				TaskQueues: []temporal.TaskQueueInfo{
					{Name: "queue1"},
				},
				TestWorkflows: []temporal.WorkflowExecutionInfo{
					{
						WorkflowID: "test-wf-1",
						RunID:      "run1",
						TaskQueue:  "queue1",
						Status:     temporaliov1alpha1.WorkflowExecutionStatusCompleted,
					},
				},
			},
			"worker.v2": {
				VersionID:    "worker.v2",
				Status:       temporal.VersionStatusDrained,
				DrainedSince: &drainedSince,
			},
		},
	}

	// Create state mapper
	mapper := NewStateMapper(k8sState, temporalState)

	// Test mapping existing version with deployment
	version := mapper.mapWorkerDeploymentVersion("worker.v1")
	assert.NotNil(t, version)
	assert.Equal(t, "worker.v1", version.VersionID)

	// Convert to string for comparison
	expectedStatus := string(temporaliov1alpha1.VersionStatusCurrent)
	actualStatus := string(version.Status)
	assert.Equal(t, expectedStatus, actualStatus)

	assert.NotNil(t, version.HealthySince)
	assert.Equal(t, healthySince.Time.Unix(), version.HealthySince.Time.Unix())
	assert.Equal(t, 1, len(version.TaskQueues))
	assert.Equal(t, "queue1", version.TaskQueues[0].Name)
	assert.Equal(t, 1, len(version.TestWorkflows))
	assert.Equal(t, "test-wf-1", version.TestWorkflows[0].WorkflowID)

	// Test mapping version without deployment
	version = mapper.mapWorkerDeploymentVersion("worker.v2")
	assert.NotNil(t, version)
	assert.Equal(t, "worker.v2", version.VersionID)

	// Convert to string for comparison
	expectedDrainedStatus := string(temporaliov1alpha1.VersionStatusDrained)
	actualDrainedStatus := string(version.Status)
	assert.Equal(t, expectedDrainedStatus, actualDrainedStatus)

	assert.Nil(t, version.HealthySince)
	assert.NotNil(t, version.DrainedSince)
	assert.Equal(t, drainedSince.Unix(), version.DrainedSince.Time.Unix())

	// Test mapping non-existent version
	version = mapper.mapWorkerDeploymentVersion("worker.v3")
	assert.NotNil(t, version)
	assert.Equal(t, "worker.v3", version.VersionID)

	// Convert to string for comparison
	expectedNotRegisteredStatus := string(temporaliov1alpha1.VersionStatusNotRegistered)
	actualNotRegisteredStatus := string(version.Status)
	assert.Equal(t, expectedNotRegisteredStatus, actualNotRegisteredStatus)

	// Test mapping empty version ID
	version = mapper.mapWorkerDeploymentVersion("")
	assert.Nil(t, version)
}
