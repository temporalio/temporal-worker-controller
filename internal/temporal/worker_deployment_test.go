// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package temporal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
)

func TestVersionStatusMap(t *testing.T) {
	tests := map[enumspb.WorkerDeploymentVersionStatus]temporaliov1alpha1.VersionStatus{
		enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_UNSPECIFIED: temporaliov1alpha1.VersionStatusNotRegistered,
		enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_CREATED:     temporaliov1alpha1.VersionStatusCreated,
		enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_INACTIVE:    temporaliov1alpha1.VersionStatusInactive,
		enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_RAMPING:     temporaliov1alpha1.VersionStatusRamping,
		enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_CURRENT:     temporaliov1alpha1.VersionStatusCurrent,
		enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_DRAINING:    temporaliov1alpha1.VersionStatusDraining,
		enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_DRAINED:     temporaliov1alpha1.VersionStatusDrained,
	}

	assert.Equal(t, tests, versionStatusMap)
}

func TestVersionInfoFromVersionSummaryLogsRoutingConfigStatusConflicts(t *testing.T) {
	tests := []struct {
		name                  string
		summaryStatus         enumspb.WorkerDeploymentVersionStatus
		routingConfig         *deploymentpb.RoutingConfig
		reportedStatus        temporaliov1alpha1.VersionStatus
		expectedLogMessage    string
		expectedConfigBuildID string
	}{
		{
			name:          "ramping",
			summaryStatus: enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_RAMPING,
			routingConfig: &deploymentpb.RoutingConfig{
				RampingDeploymentVersion: &deploymentpb.WorkerDeploymentVersion{
					DeploymentName: "workers",
					BuildId:        "build-b",
				},
			},
			reportedStatus:        temporaliov1alpha1.VersionStatusRamping,
			expectedLogMessage:    "version reports Ramping but routing config identifies a different Ramping version; trusting routing config",
			expectedConfigBuildID: "build-b",
		},
		{
			name:          "current",
			summaryStatus: enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_CURRENT,
			routingConfig: &deploymentpb.RoutingConfig{
				CurrentDeploymentVersion: &deploymentpb.WorkerDeploymentVersion{
					DeploymentName: "workers",
					BuildId:        "build-b",
				},
			},
			reportedStatus:        temporaliov1alpha1.VersionStatusCurrent,
			expectedLogMessage:    "version reports Current but routing config identifies a different Current version; trusting routing config",
			expectedConfigBuildID: "build-b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logLines []string
			logger := funcr.New(func(prefix, args string) {
				logLines = append(logLines, prefix+" "+args)
			}, funcr.Options{})
			summary := &deploymentpb.WorkerDeploymentInfo_WorkerDeploymentVersionSummary{
				DeploymentVersion: &deploymentpb.WorkerDeploymentVersion{
					DeploymentName: "workers",
					BuildId:        "build-a",
				},
				Status: tt.summaryStatus,
			}

			info := versionInfoFromVersionSummary(
				context.Background(), logger, nil, "", "", nil, tt.routingConfig, summary,
			)

			assert.Equal(t, tt.reportedStatus, info.Status)
			logs := strings.Join(logLines, "\n")
			assert.Contains(t, logs, tt.expectedLogMessage)
			assert.Contains(t, logs, "build-a")
			assert.Contains(t, logs, tt.expectedConfigBuildID)
			assert.NotContains(t, logs, "workers:")
		})
	}
}

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

// describeTaskQueueStub is a minimal temporalclient.Client that only implements
// DescribeTaskQueue, recording which (name, type) pairs it was called with and
// returning a canned response keyed by type. Embedding the interface (left nil)
// satisfies every other method without needing a full mock.
type describeTaskQueueStub struct {
	temporalclient.Client
	responses map[enumspb.TaskQueueType]*workflowservice.DescribeTaskQueueResponse
	calls     []enumspb.TaskQueueType
}

func (s *describeTaskQueueStub) DescribeTaskQueue(_ context.Context, _ string, taskqueueType enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
	s.calls = append(s.calls, taskqueueType)
	return s.responses[taskqueueType], nil
}

func TestGetPollersCoversWorkflowActivityAndNexusTaskQueues(t *testing.T) {
	for _, tc := range []struct {
		name          string
		queueType     temporalclient.TaskQueueType
		protoType     enumspb.TaskQueueType
		wantIdentity  string
		wantCallCount int
	}{
		{
			name:          "workflow task queue",
			queueType:     temporalclient.TaskQueueTypeWorkflow,
			protoType:     enumspb.TASK_QUEUE_TYPE_WORKFLOW,
			wantIdentity:  "workflow-poller",
			wantCallCount: 1,
		},
		{
			name:          "activity task queue",
			queueType:     temporalclient.TaskQueueTypeActivity,
			protoType:     enumspb.TASK_QUEUE_TYPE_ACTIVITY,
			wantIdentity:  "activity-poller",
			wantCallCount: 1,
		},
		{
			name:          "nexus task queue",
			queueType:     temporalclient.TaskQueueTypeNexus,
			protoType:     enumspb.TASK_QUEUE_TYPE_NEXUS,
			wantIdentity:  "nexus-poller",
			wantCallCount: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &describeTaskQueueStub{
				responses: map[enumspb.TaskQueueType]*workflowservice.DescribeTaskQueueResponse{
					tc.protoType: {Pollers: []*taskqueuepb.PollerInfo{{Identity: tc.wantIdentity}}},
				},
			}
			pollers, err := getPollers(context.Background(), stub, temporalclient.WorkerDeploymentTaskQueueInfo{
				Name: "tq", Type: tc.queueType,
			})
			require.NoError(t, err)
			require.Len(t, pollers, 1)
			assert.Equal(t, tc.wantIdentity, pollers[0].GetIdentity())
			assert.Equal(t, []enumspb.TaskQueueType{tc.protoType}, stub.calls)
		})
	}
}

// TestGetTaskQueuesWithNoPollersDoesNotFalsePositiveOnNexus is a regression test for the bug
// where getPollers had no case for TaskQueueTypeNexus: the switch fell through with a nil
// DescribeTaskQueueResponse, GetPollers() on nil returned an empty slice, and every Nexus task
// queue was reported as having no active poller even when one was actively polling it.
func TestGetTaskQueuesWithNoPollersDoesNotFalsePositiveOnNexus(t *testing.T) {
	stub := &describeTaskQueueStub{
		responses: map[enumspb.TaskQueueType]*workflowservice.DescribeTaskQueueResponse{
			enumspb.TASK_QUEUE_TYPE_NEXUS: {Pollers: []*taskqueuepb.PollerInfo{{Identity: "nexus-poller"}}},
		},
	}
	withoutPollers, err := getTaskQueuesWithNoPollers(context.Background(), stub, []temporalclient.WorkerDeploymentTaskQueueInfo{
		{Name: "nexus-tq", Type: temporalclient.TaskQueueTypeNexus},
	})
	require.NoError(t, err)
	assert.Empty(t, withoutPollers, "a Nexus task queue with an active poller must not be reported as having no poller")
}
