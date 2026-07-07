// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package temporal

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stretchr/testify/assert"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	enumspb "go.temporal.io/api/enums/v1"
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

func eventually(t *testing.T, timeout, interval time.Duration, check func() error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		err := check()
		if err == nil {
			return // Success!
		}
		lastErr = err
		time.Sleep(interval)
	}
	if lastErr != nil {
		t.Fatalf("eventually failed after %s: %v", timeout, lastErr)
	}
}

func TestRetryTransient(t *testing.T) {
	t.Run("retries transient errors until success", func(t *testing.T) {
		calls := 0
		err := retryTransient(context.Background(), 3, time.Millisecond, func() error {
			calls++
			if calls < 3 {
				return status.Error(codes.Unavailable, "transport blip")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("expected success after retries, got %v", err)
		}
		if calls != 3 {
			t.Fatalf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("does not retry non-transient errors", func(t *testing.T) {
		calls := 0
		err := retryTransient(context.Background(), 3, time.Millisecond, func() error {
			calls++
			return status.Error(codes.NotFound, "missing")
		})
		if status.Code(err) != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected 1 call, got %d", calls)
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		calls := 0
		err := retryTransient(context.Background(), 3, time.Millisecond, func() error {
			calls++
			return status.Error(codes.Unavailable, "still down")
		})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("expected Unavailable, got %v", err)
		}
		if calls != 3 {
			t.Fatalf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("stops on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := retryTransient(ctx, 3, time.Millisecond, func() error {
			return status.Error(codes.Unavailable, "down")
		})
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}
