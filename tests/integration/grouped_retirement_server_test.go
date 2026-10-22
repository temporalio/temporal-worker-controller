//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TestGroupedRetirementServerInitialization verifies the server operations used
// by grouped retirement for absent and partly registered builds. It leaves the
// current workers and their pinned work running while the obsolete build drains;
// the separate Kubernetes scenario verifies automatic member shutdown and deletion.
func TestGroupedRetirementServerInitialization(t *testing.T) {
	server := groupedRetirementTestServer(t)
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent-build", true: "missing-current-queue"}[partial], func(t *testing.T) {
			ctx := context.Background()
			deploymentName := fmt.Sprintf("retirement-server-%t-%d", partial, time.Now().UnixNano())
			identity := "grouped-retirement-server-test"
			current := sdkworker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: "build-c"}
			obsolete := sdkworker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: "build-b"}
			sharedQueue := deploymentName + "-jobs"
			additionalQueue := deploymentName + "-new-jobs"
			for _, queue := range []string{sharedQueue, additionalQueue} {
				w := retirementServerWorker(server.GetDefaultClient(), queue, current)
				require.NoError(t, w.Start())
				defer w.Stop()
			}
			handle := server.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName)
			eventually(t, 30*time.Second, time.Second, func() error {
				description, err := handle.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: current.BuildID})
				if err != nil {
					return err
				}
				return retirementServerQueuePairs(description.Info.TaskQueuesInfos, []string{sharedQueue, additionalQueue})
			})
			description, err := handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
			require.NoError(t, err)
			_, err = handle.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{BuildID: current.BuildID, ConflictToken: description.ConflictToken, Identity: identity})
			require.NoError(t, err)

			// An execution on the additional current queue makes its recent work
			// visible to the server's missing-queue protection. It stays pinned to C
			// while the test initializes and observes B's drainage.
			run, err := server.GetDefaultClient().ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
				ID: deploymentName + "-accepted-current-work", TaskQueue: additionalQueue,
				VersioningOverride: &sdkclient.PinnedVersioningOverride{Version: current},
			}, "HoldCurrentWork")
			require.NoError(t, err)
			defer func() {
				err := server.GetDefaultClient().TerminateWorkflow(ctx, run.GetID(), run.GetRunID(), "isolated test cleanup")
				assert.NoError(t, err)
			}()
			eventually(t, 30*time.Second, time.Second, func() error {
				info, err := server.GetDefaultClient().DescribeWorkflowExecution(ctx, run.GetID(), run.GetRunID())
				if err != nil {
					return err
				}
				if info.GetWorkflowExecutionInfo().GetHistoryLength() < 4 {
					return fmt.Errorf("current pinned workflow has not completed its first task")
				}
				return nil
			})

			if partial {
				w := retirementServerWorker(server.GetDefaultClient(), sharedQueue, obsolete)
				require.NoError(t, w.Start())
				defer w.Stop()
				eventually(t, 30*time.Second, time.Second, func() error {
					info, err := handle.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: obsolete.BuildID})
					if err != nil {
						return err
					}
					return retirementServerQueuePairs(info.Info.TaskQueuesInfos, []string{sharedQueue})
				})
				description, err = handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
				require.NoError(t, err)
				_, err = handle.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
					BuildID: obsolete.BuildID, Percentage: 0, ConflictToken: description.ConflictToken, Identity: identity,
				})
				var missingQueues *serviceerror.FailedPrecondition
				require.True(t, errors.As(err, &missingQueues), "ordinary routing must reject B's missing current queue: %v", err)
			} else {
				_, err = handle.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: obsolete.BuildID})
				var absent *serviceerror.NotFound
				require.True(t, errors.As(err, &absent), "obsolete build must be absent before initialization: %v", err)
			}

			// These are the controller's exact maintenance options. No ordinary
			// traffic is assigned to B; the operation creates its server record if
			// necessary and then removes this same zero-percent assignment.
			description, err = handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
			require.NoError(t, err)
			require.Nil(t, description.Info.RoutingConfig.RampingVersion)
			require.NotEmpty(t, description.ConflictToken)
			assigned, err := handle.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
				BuildID: obsolete.BuildID, Percentage: 0, ConflictToken: description.ConflictToken,
				Identity: identity, AllowNoPollers: true, IgnoreMissingTaskQueues: true,
			})
			require.NoError(t, err)
			require.NotEmpty(t, assigned.ConflictToken)
			_, err = handle.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
				BuildID: "", Percentage: 0, ConflictToken: assigned.ConflictToken, Identity: identity,
			})
			require.NoError(t, err)
			eventually(t, 45*time.Second, time.Second, func() error {
				version, err := server.GetDefaultClient().WorkflowService().DescribeWorkerDeploymentVersion(ctx, &workflowservice.DescribeWorkerDeploymentVersionRequest{
					Namespace: server.GetDefaultNamespace(), DeploymentVersion: &deploymentpb.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildId: obsolete.BuildID},
				})
				if err != nil {
					return err
				}
				info := version.GetWorkerDeploymentVersionInfo()
				if info.GetStatus() != enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_DRAINED || info.GetDrainageInfo().GetLastChangedTime() == nil {
					return fmt.Errorf("obsolete build has not reached Drained with a server timestamp")
				}
				return nil
			})
			description, err = handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
			require.NoError(t, err)
			require.NotNil(t, description.Info.RoutingConfig.CurrentVersion)
			assert.Equal(t, current.BuildID, description.Info.RoutingConfig.CurrentVersion.BuildID)
			assert.Nil(t, description.Info.RoutingConfig.RampingVersion)
			currentWork, err := server.GetDefaultClient().DescribeWorkflowExecution(ctx, run.GetID(), run.GetRunID())
			require.NoError(t, err)
			assert.Equal(t, enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, currentWork.GetWorkflowExecutionInfo().GetStatus())
		})
	}
}

// retirementServerWorker starts versioned polling when the caller invokes Start.
// Its registered workflow stays open so the test can prove current work survives
// the obsolete version's maintenance assignment and drainage.
func retirementServerWorker(c sdkclient.Client, queue string, version sdkworker.WorkerDeploymentVersion) sdkworker.Worker {
	w := sdkworker.New(c, queue, sdkworker.Options{DeploymentOptions: sdkworker.DeploymentOptions{
		UseVersioning: true, Version: version, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
	}})
	w.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
		return workflow.Await(ctx, func() bool { return false })
	}, workflow.RegisterOptions{Name: "HoldCurrentWork"})
	return w
}

// retirementServerQueuePairs verifies the workflow and activity entries produced
// by each normal SDK worker. Failure includes the exact observed names and kinds
// so a registration problem cannot be mistaken for a later retirement failure.
func retirementServerQueuePairs(observed []sdkclient.WorkerDeploymentTaskQueueInfo, names []string) error {
	expected := make(map[sdkclient.WorkerDeploymentTaskQueueInfo]bool)
	for _, name := range names {
		for _, kind := range []sdkclient.TaskQueueType{sdkclient.TaskQueueTypeWorkflow, sdkclient.TaskQueueTypeActivity} {
			expected[sdkclient.WorkerDeploymentTaskQueueInfo{Name: name, Type: kind}] = false
		}
	}
	actual := make([]string, 0, len(observed))
	matches := len(observed) == len(expected)
	for _, queue := range observed {
		actual = append(actual, fmt.Sprintf("%s:%s", queue.Name, enumspb.TaskQueueType(queue.Type).String()))
		seen, exists := expected[queue]
		if !exists || seen {
			matches = false
		}
		expected[queue] = true
	}
	if !matches {
		return fmt.Errorf("expected workflow and activity registration for queues %v; observed %v", names, actual)
	}
	return nil
}
