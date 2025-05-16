// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package temporal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.temporal.io/api/enums/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

func TestMapWorkflowStatus(t *testing.T) {
	tests := []struct {
		name           string
		status         enums.WorkflowExecutionStatus
		expectedStatus temporaliov1alpha1.WorkflowExecutionStatus
	}{
		{
			name:           "running",
			status:         enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusRunning,
		},
		{
			name:           "continued as new",
			status:         enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusRunning,
		},
		{
			name:           "completed",
			status:         enums.WORKFLOW_EXECUTION_STATUS_COMPLETED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusCompleted,
		},
		{
			name:           "failed",
			status:         enums.WORKFLOW_EXECUTION_STATUS_FAILED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusFailed,
		},
		{
			name:           "canceled",
			status:         enums.WORKFLOW_EXECUTION_STATUS_CANCELED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusCanceled,
		},
		{
			name:           "terminated",
			status:         enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusTerminated,
		},
		{
			name:           "timed out",
			status:         enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusTimedOut,
		},
		{
			name:           "unspecified",
			status:         enums.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusRunning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := mapWorkflowStatus(tt.status)
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

func TestGetTestWorkflowID(t *testing.T) {
	tests := []struct {
		name           string
		deploymentName string
		taskQueue      string
		versionID      string
		expected       string
	}{
		{
			name:           "basic test",
			deploymentName: "worker",
			taskQueue:      "queue1",
			versionID:      "worker.v1",
			expected:       "test-worker-queue1-worker.v1",
		},
		{
			name:           "with dots",
			deploymentName: "worker.app",
			taskQueue:      "queue.main",
			versionID:      "worker.app.v2",
			expected:       "test-worker.app-queue.main-worker.app.v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := getTestWorkflowID(tt.deploymentName, tt.taskQueue, tt.versionID)
			assert.Equal(t, tt.expected, id)
		})
	}
}

func TestVersionInfoManagement(t *testing.T) {
	// Create a test version info
	versionInfo := &VersionInfo{
		VersionID:      "worker.v1",
		Status:         VersionStatusCurrent,
		RampPercentage: 100,
		TaskQueues: []TaskQueueInfo{
			{Name: "queue1"},
			{Name: "queue2"},
		},
	}

	// Set drained since
	drainTime := time.Now().Add(-1 * time.Hour)
	versionInfo.DrainedSince = &drainTime

	// Add test workflows
	versionInfo.TestWorkflows = append(versionInfo.TestWorkflows, WorkflowExecutionInfo{
		WorkflowID: "test-wf-1",
		RunID:      "run1",
		TaskQueue:  "queue1",
		Status:     temporaliov1alpha1.WorkflowExecutionStatusCompleted,
	})

	// Verify fields
	assert.Equal(t, "worker.v1", versionInfo.VersionID)
	assert.Equal(t, VersionStatusCurrent, versionInfo.Status)
	assert.Equal(t, float32(100), versionInfo.RampPercentage)
	assert.Equal(t, 2, len(versionInfo.TaskQueues))
	assert.Equal(t, "queue1", versionInfo.TaskQueues[0].Name)
	assert.Equal(t, "queue2", versionInfo.TaskQueues[1].Name)
	assert.Equal(t, drainTime.Unix(), versionInfo.DrainedSince.Unix()) // Compare Unix timestamps to avoid sub-second differences
	assert.Equal(t, 1, len(versionInfo.TestWorkflows))
	assert.Equal(t, "test-wf-1", versionInfo.TestWorkflows[0].WorkflowID)
	assert.Equal(t, temporaliov1alpha1.WorkflowExecutionStatusCompleted, versionInfo.TestWorkflows[0].Status)
}

func TestTemporalWorkerState(t *testing.T) {
	// Create a test temporal worker state
	rampingSince := metav1.NewTime(time.Now().Add(-30 * time.Minute))

	state := &TemporalWorkerState{
		DefaultVersionID:     "worker.v1",
		RampingVersionID:     "worker.v2",
		RampPercentage:       25.0,
		RampingSince:         &rampingSince,
		LastModifierIdentity: "test-controller",
		VersionConflictToken: []byte("test-token"),
		Versions:             make(map[string]*VersionInfo),
	}

	// Add versions
	state.Versions["worker.v1"] = &VersionInfo{
		VersionID:      "worker.v1",
		Status:         VersionStatusCurrent,
		RampPercentage: 100,
	}

	state.Versions["worker.v2"] = &VersionInfo{
		VersionID:      "worker.v2",
		Status:         VersionStatusRamping,
		RampPercentage: 25.0,
	}

	// Verify state
	assert.Equal(t, "worker.v1", state.DefaultVersionID)
	assert.Equal(t, "worker.v2", state.RampingVersionID)
	assert.Equal(t, float32(25.0), state.RampPercentage)
	assert.Equal(t, rampingSince.Time.Unix(), state.RampingSince.Time.Unix())
	assert.Equal(t, "test-controller", state.LastModifierIdentity)
	assert.Equal(t, []byte("test-token"), state.VersionConflictToken)
	assert.Equal(t, 2, len(state.Versions))

	// Verify versions
	assert.Equal(t, VersionStatusCurrent, state.Versions["worker.v1"].Status)
	assert.Equal(t, VersionStatusRamping, state.Versions["worker.v2"].Status)
	assert.Equal(t, float32(25.0), state.Versions["worker.v2"].RampPercentage)
}
