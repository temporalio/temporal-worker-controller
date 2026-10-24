// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// Grouped retirement waits for an application-owned workflow to certify that no
// new work can enter a build and no caller relies on implicit ramp selection.
// The controller can then initialize server-owned drainage with a zero-percent
// ramp. Only Temporal drainage permits pod shutdown; the application check does not.
package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	appsv1 "k8s.io/api/apps/v1"
)

type (
	// groupRetirement retains the observed members while execution checks the
	// saved retirement workflow. It exists only for this reconciliation.
	groupRetirement struct {
		buildID string
		saved   *k8s.GroupRevision
		actual  map[string]*appsv1.Deployment
		desired map[string]*appsv1.Deployment
		version *temporal.VersionInfo
	}
)

// retirementWorkflowID assigns one workflow identity to a saved build. Continuing
// as new keeps this identity, so each describe reads the latest execution.
func retirementWorkflowID(deploymentName, buildID string) string {
	return fmt.Sprintf("retire-%s:%s", deploymentName, buildID)
}

// checkTargetGroupRetirement rejects reselecting a build once its retirement
// workflow exists. The application may already have closed admission while that
// workflow waits for old work, so restoring routing would violate its promise.
func checkTargetGroupRetirement(ctx context.Context, c sdkclient.Client, w *v1alpha1.WorkerDeployment, p *plan) error {
	if p.targetGroup == nil {
		return nil
	}
	buildID := k8s.ComputeBuildID(w)
	id := retirementWorkflowID(p.WorkerDeploymentName, buildID)
	_, err := c.DescribeWorkflowExecution(ctx, id, "")
	var missing *serviceerror.NotFound
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check selected group retirement %q: %w", id, err)
	}
	return fmt.Errorf("grouped build %q has started retirement; use a new build ID", buildID)
}

// planGroupRetirements waits for the saved application approval and closed
// readiness workflows. It starts server-owned drainage for inactive builds, then
// retains every member until Temporal reports the build drained.
func planGroupRetirements(ctx context.Context, c sdkclient.Client, p *plan) error {
	var failures []error
	for _, group := range p.groupRetirements {
		if err := planGroupRetirement(ctx, c, p, group); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// planGroupRetirement keeps one obsolete group's saved pod counts until its
// application approval, readiness workflow closure, and Temporal drainage permit
// retirement. A failed check adds no member creation, scaling or deletion.
func planGroupRetirement(ctx context.Context, c sdkclient.Client, p *plan, group groupRetirement) error {
	approved, err := completedRetirement(ctx, c, p, group)
	if err != nil {
		return err
	}
	if !approved {
		planGroupMembers(p, group.actual, group.desired)
		return nil
	}
	closed, err := groupReadinessGatesClosed(ctx, c, p.WorkerDeploymentName, group)
	if err != nil {
		return err
	}
	if !closed {
		planGroupMembers(p, group.actual, group.desired)
		return nil
	}
	if group.version == nil || group.version.Status == v1alpha1.VersionStatusNotRegistered || group.version.Status == v1alpha1.VersionStatusInactive || group.version.Status == v1alpha1.VersionStatusCreated || group.version.Status == v1alpha1.VersionStatusRamping {
		if p.allowGroupedDrainage && p.UpdateVersionConfig == nil {
			if err := initializeGroupDrainage(ctx, c, p, group.buildID); err != nil {
				return err
			}
		}
		planGroupMembers(p, group.actual, group.desired)
		return nil
	}
	if group.version.Status != v1alpha1.VersionStatusDrained || group.version.DrainedSince == nil {
		planGroupMembers(p, group.actual, group.desired)
		return nil
	}
	elapsed := time.Since(*group.version.DrainedSince)
	if elapsed <= group.saved.Spec.SunsetStrategy.ScaledownDelay.Duration {
		planGroupMembers(p, group.actual, group.desired)
		return nil
	}
	deleting := elapsed > group.saved.Spec.SunsetStrategy.ScaledownDelay.Duration+group.saved.Spec.SunsetStrategy.DeleteDelay.Duration && groupStopped(group.actual, group.desired)
	for _, desired := range group.desired {
		zero := int32(0)
		desired.Spec.Replicas = &zero
	}
	planGroupMembers(p, group.actual, group.desired)
	if deleting {
		for _, actual := range group.actual {
			p.DeleteDeployments = append(p.DeleteDeployments, actual)
		}
		p.MarkRevisionsForDeletion = append(p.MarkRevisionsForDeletion, group.saved.Object)
	}
	return nil
}

// initializeGroupDrainage briefly assigns an approved obsolete build a zero
// percent ramp and clears that exact assignment. Each write uses a fresh owner
// token, so concurrent routing changes fail rather than being overwritten. A
// later reconcile can finish a zero percent assignment left by a stopped process.
func initializeGroupDrainage(ctx context.Context, c sdkclient.Client, p *plan, buildID string) error {
	handle := c.WorkerDeploymentClient().GetHandle(p.WorkerDeploymentName)
	description, err := handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
	if err != nil {
		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			return nil
		}
		return fmt.Errorf("describe routing before group retirement: %w", err)
	}
	routing := description.Info.RoutingConfig
	if routing.CurrentVersion != nil && routing.CurrentVersion.BuildID == buildID {
		return nil
	}
	if routing.RampingVersionPercentage != 0 || (routing.RampingVersion != nil && routing.RampingVersion.BuildID != buildID) {
		return nil
	}
	if len(description.ConflictToken) == 0 {
		return fmt.Errorf("group retirement requires a nonempty routing conflict token")
	}
	token := description.ConflictToken
	if routing.RampingVersion == nil {
		response, err := handle.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
			BuildID: buildID, Percentage: 0, ConflictToken: token,
			Identity: getControllerIdentity(), IgnoreMissingTaskQueues: true, AllowNoPollers: true,
		})
		if err != nil {
			return fmt.Errorf("initialize drainage for grouped build %q: %w", buildID, err)
		}
		token = response.ConflictToken
	}
	if len(token) == 0 {
		return fmt.Errorf("group retirement requires a nonempty updated routing conflict token")
	}
	_, err = handle.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
		BuildID: "", Percentage: 0, ConflictToken: token, Identity: getControllerIdentity(),
	})
	if err != nil {
		return fmt.Errorf("clear retirement ramp for grouped build %q: %w", buildID, err)
	}
	return nil
}

// completedRetirement starts the saved application check once and reads its
// latest run thereafter. Success authorizes the routing maintenance operation;
// Temporal must still prove that every pinned execution has finished.
func completedRetirement(ctx context.Context, c sdkclient.Client, p *plan, group groupRetirement) (bool, error) {
	deploymentName := p.WorkerDeploymentName
	config := group.saved.Spec.GroupRetirementWorkflow
	id := retirementWorkflowID(deploymentName, group.buildID)
	description, err := c.DescribeWorkflowExecution(ctx, id, "")
	var missing *serviceerror.NotFound
	if errors.As(err, &missing) {
		_, err = c.ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
			ID: id, TaskQueue: config.TaskQueue,
			WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		}, config.WorkflowType, deploymentName, group.buildID)
		var exists *serviceerror.WorkflowExecutionAlreadyStarted
		if err != nil && !errors.As(err, &exists) {
			return false, fmt.Errorf("start group retirement workflow %q: %w", id, err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("describe group retirement workflow %q: %w", id, err)
	}
	info := description.GetWorkflowExecutionInfo()
	if info.GetExecution().GetWorkflowId() != id || info.GetExecution().GetRunId() == "" || info.GetType().GetName() != config.WorkflowType || info.GetTaskQueue() != config.TaskQueue || !unversionedRetirement(info.GetVersioningInfo()) {
		return false, fmt.Errorf("group retirement workflow %q must match its saved type and unversioned management queue", id)
	}
	if info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED || info.GetCloseTime() == nil || info.GetCloseTime().CheckValid() != nil {
		return false, nil
	}
	// Request a small first history page for this exact run. Temporal can return
	// a whole stored event batch; validate its first event to prove which
	// deployment and build this execution received.
	history, err := c.WorkflowService().GetWorkflowExecutionHistory(ctx, &workflowservice.GetWorkflowExecutionHistoryRequest{
		Namespace:              p.TemporalNamespace,
		Execution:              &commonpb.WorkflowExecution{WorkflowId: id, RunId: info.GetExecution().GetRunId()},
		MaximumPageSize:        1,
		HistoryEventFilterType: enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT,
	})
	if err != nil {
		return false, fmt.Errorf("read group retirement input %q: %w", id, err)
	}
	events := history.GetHistory().GetEvents()
	if len(events) == 0 || events[0].GetEventId() != 1 || events[0].GetWorkflowExecutionStartedEventAttributes() == nil {
		return false, fmt.Errorf("group retirement workflow %q has no initial input event", id)
	}
	input := events[0].GetWorkflowExecutionStartedEventAttributes().GetInput()
	if len(input.GetPayloads()) != 2 {
		return false, fmt.Errorf("group retirement workflow %q requires exactly two string arguments", id)
	}
	var suppliedDeployment, suppliedBuild string
	if err := converter.GetDefaultDataConverter().FromPayloads(input, &suppliedDeployment, &suppliedBuild); err != nil {
		return false, fmt.Errorf("decode group retirement input %q: %w", id, err)
	}
	if suppliedDeployment != deploymentName || suppliedBuild != group.buildID {
		return false, fmt.Errorf("group retirement workflow %q input does not match its saved deployment and build", id)
	}
	return true, nil
}

// unversionedRetirement rejects effective deployment assignments as well as
// overrides, so a worker in the retiring group cannot certify its own shutdown.
func unversionedRetirement(info *workflowpb.WorkflowExecutionVersioningInfo) bool {
	//lint:ignore SA1019 Older servers expose deployment assignments in these fields.
	return info.GetBehavior() == enumspb.VERSIONING_BEHAVIOR_UNSPECIFIED && info.GetVersioningOverride() == nil && info.GetDeploymentVersion() == nil && info.GetVersionTransition() == nil && info.GetDeployment() == nil && info.GetVersion() == "" && info.GetDeploymentTransition() == nil
}

// groupReadinessGatesClosed reads the latest execution for each saved readiness
// gate after cancellation. An asynchronous cancellation request is not closure;
// a running or continuing gate keeps every grouped member alive.
func groupReadinessGatesClosed(ctx context.Context, c sdkclient.Client, deploymentName string, group groupRetirement) (bool, error) {
	if group.saved.Spec.RolloutStrategy.Gate == nil {
		return true, nil
	}
	for _, queue := range group.saved.Spec.ExpectedTaskQueues {
		if !slices.Contains(queue.Kinds, v1alpha1.TaskQueueWorkflow) {
			continue
		}
		id := temporal.GetTestWorkflowID(deploymentName, group.buildID, queue.Name)
		description, err := c.DescribeWorkflowExecution(ctx, id, "")
		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("confirm superseded readiness workflow %q closed: %w", id, err)
		}
		info := description.GetWorkflowExecutionInfo()
		if info.GetExecution().GetWorkflowId() != id || info.GetExecution().GetRunId() == "" || info.GetType().GetName() != group.saved.Spec.RolloutStrategy.Gate.WorkflowType || info.GetTaskQueue() != queue.Name || !gatePinnedToBuild(info.GetVersioningInfo().GetVersioningOverride(), deploymentName, group.buildID) {
			return false, fmt.Errorf("superseded readiness workflow %q does not match its saved settings", id)
		}
		switch info.GetStatus() {
		case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		default:
			return false, nil
		}
	}
	return true, nil
}
