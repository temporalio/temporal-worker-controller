// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package temporal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	enumspb "go.temporal.io/api/enums/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/server/temporaltest"
)

func TestMapWorkflowStatus(t *testing.T) {
	tests := []struct {
		name           string
		status         enumspb.WorkflowExecutionStatus
		expectedStatus temporaliov1alpha1.WorkflowExecutionStatus
	}{
		{
			name:           "running",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusRunning,
		},
		{
			name:           "continued as new",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusRunning,
		},
		{
			name:           "completed",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusCompleted,
		},
		{
			name:           "failed",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusFailed,
		},
		{
			name:           "canceled",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusCanceled,
		},
		{
			name:           "terminated",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusTerminated,
		},
		{
			name:           "timed out",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
			expectedStatus: temporaliov1alpha1.WorkflowExecutionStatusTimedOut,
		},
		{
			name:           "unspecified",
			status:         enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED,
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
		buildID        string
		taskQueue      string
		expected       string
	}{
		{
			name:           "basic test",
			deploymentName: "worker",
			buildID:        "v1",
			taskQueue:      "queue1",
			expected:       "test-worker:v1-queue1",
		},
		{
			name:           "with dots",
			deploymentName: "worker.app",
			buildID:        "v2",
			taskQueue:      "queue.main",
			expected:       "test-worker.app:v2-queue.main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := GetTestWorkflowID(tt.deploymentName, tt.buildID, tt.taskQueue)
			assert.Equal(t, tt.expected, id)
		})
	}
}

func TestGetIgnoreLastModifier(t *testing.T) {
	ctx := context.Background()
	deploymentName := "test-dep"
	buildId := "v1"
	tq := "test-tq"

	ts := temporaltest.NewServer(temporaltest.WithT(t))

	// create version
	w, stopFunc, err := testhelpers.NewWorker(ctx, deploymentName, buildId, tq, ts.GetFrontendHostPort(), ts.GetDefaultNamespace(), true)
	if err != nil {
		t.Error(err)
	}
	if err = w.Start(); err != nil {
		t.Errorf("error starting unversioned worker %v", err)
	}
	defer stopFunc()

	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName)

	eventually(t, 5*time.Second, 100*time.Millisecond, func() error {
		_, err := deploymentHandler.UpdateVersionMetadata(ctx, sdkclient.WorkerDeploymentUpdateVersionMetadataOptions{
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: deploymentName,
				BuildId:        buildId,
			},
			MetadataUpdate: sdkclient.WorkerDeploymentMetadataUpdate{
				UpsertEntries: map[string]interface{}{
					IgnoreLastModifierKey: "true",
				},
			},
		})
		return err
	})

	shouldIgnore, err := getShouldIgnoreLastModifier(ctx, deploymentHandler, buildId)
	if err != nil {
		t.Error(err)
	}
	if !shouldIgnore {
		t.Error("expected true, got false")
	}

}

func eventually(t *testing.T, timeout, interval time.Duration, check func() error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := check(); err == nil {
			return // Success!
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	if lastErr != nil {
		t.Fatalf("eventually failed after %s: %v", timeout, lastErr)
	}
}
