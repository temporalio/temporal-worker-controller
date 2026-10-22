// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	commonpb "go.temporal.io/api/common/v1"
	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type (
	gateCancellationClient struct {
		sdkclient.Client
		description     *workflowservice.DescribeWorkflowExecutionResponse
		describeErr     error
		cancelErr       error
		describedIDs    []string
		describedRunIDs []string
		canceled        []v1alpha1.WorkflowExecution
	}
	gateStartClient struct {
		sdkclient.Client
		started []sdkclient.StartWorkflowOptions
	}
)

func (c *gateCancellationClient) DescribeWorkflowExecution(_ context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	c.describedIDs = append(c.describedIDs, workflowID)
	c.describedRunIDs = append(c.describedRunIDs, runID)
	return c.description, c.describeErr
}

func (c *gateCancellationClient) CancelWorkflow(_ context.Context, workflowID, runID string) error {
	c.canceled = append(c.canceled, v1alpha1.WorkflowExecution{WorkflowID: workflowID, RunID: runID})
	return c.cancelErr
}

func TestGateStartLifetimeMatchesDeploymentOwnership(t *testing.T) {
	for _, test := range []struct {
		name    string
		grouped bool
		input   []byte
		want    time.Duration
	}{
		{name: "single deployment", want: time.Hour},
		{name: "grouped build waits for dependencies", grouped: true},
		{name: "grouped build with gate input", grouped: true, input: []byte(`{"check":"ready"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := groupedWorker("build-b")
			if !test.grouped {
				w.Spec.AdditionalDeployments = nil
			}
			r, _ := newTestReconciler(nil)
			gateID := temporal.GetTestWorkflowID(w.Name, "build-b", "jobs")
			p := &plan{WorkerDeploymentName: w.Name, startTestWorkflows: []startWorkflowConfig{{
				workflowID: gateID, workflowType: "CheckRelease", buildID: "build-b", taskQueue: "jobs", input: test.input,
			}}}
			c := &gateStartClient{}
			require.NoError(t, r.startTestWorkflows(context.Background(), logr.Discard(), w, c, p))
			require.Len(t, c.started, 1)
			options := c.started[0]
			assert.Equal(t, test.want, options.WorkflowExecutionTimeout)
			assert.Zero(t, options.WorkflowRunTimeout)
			assert.Equal(t, gateID, options.ID)
			assert.Equal(t, "jobs", options.TaskQueue)
			assert.Equal(t, enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, options.WorkflowIDReusePolicy)
			assert.Equal(t, enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL, options.WorkflowIDConflictPolicy)
			pinned, ok := options.VersioningOverride.(*sdkclient.PinnedVersioningOverride)
			require.True(t, ok)
			assert.Equal(t, w.Name, pinned.Version.DeploymentName)
			assert.Equal(t, "build-b", pinned.Version.BuildID)
		})
	}
}

func TestGroupedSupersessionPlansOnlySavedGateWorkflowQueues(t *testing.T) {
	r, _ := newTestReconciler(nil)
	state := &k8s.DeploymentState{Revisions: make(map[string]*k8s.GroupRevision)}
	var desired *v1alpha1.WorkerDeployment
	for _, buildID := range []string{"build-a", "build-b", "build-c", "build-without-gate"} {
		w := groupedWorker(buildID)
		w.Spec.ExpectedTaskQueues = append(w.Spec.ExpectedTaskQueues, v1alpha1.ExpectedTaskQueue{Name: "activity-only", Kinds: []v1alpha1.TaskQueueKind{v1alpha1.TaskQueueActivity}})
		if buildID != "build-without-gate" {
			w.Spec.RolloutStrategy.Gate = &v1alpha1.GateWorkflowConfig{WorkflowType: "CheckRelease"}
		}
		saved, _ := savedGroup(t, r, w)
		state.Revisions[buildID] = saved
		if buildID == "build-c" {
			desired = w
		}
	}
	p := groupPlan()
	ts := &temporal.TemporalWorkerState{CurrentBuildID: "build-a", Versions: make(map[string]*temporal.VersionInfo)}
	require.NoError(t, r.planGroupedDeployments(desired, v1alpha1.ConnectionSpec{}, ts, state, p, false))
	require.Len(t, p.cancelTestWorkflows, 2)
	builds := []string{}
	for _, gate := range p.cancelTestWorkflows {
		builds = append(builds, gate.buildID)
		assert.Equal(t, "CheckRelease", gate.workflowType)
		assert.Equal(t, "jobs", gate.taskQueue)
		assert.Equal(t, temporal.GetTestWorkflowID(k8s.ComputeWorkerDeploymentName(desired), gate.buildID, "jobs"), gate.workflowID)
	}
	assert.ElementsMatch(t, []string{"build-a", "build-b"}, builds)
}

func TestGroupedGateStartsAndPromotionUseDeclaredWorkflowQueues(t *testing.T) {
	setup, _ := newTestReconciler(nil)
	w := groupedWorker("build-c")
	w.Spec.RolloutStrategy.Gate = &v1alpha1.GateWorkflowConfig{WorkflowType: "CheckRelease"}
	w.Spec.ExpectedTaskQueues = append(w.Spec.ExpectedTaskQueues, v1alpha1.ExpectedTaskQueue{Name: "activity-only", Kinds: []v1alpha1.TaskQueueKind{v1alpha1.TaskQueueActivity}})
	saved, members := savedGroup(t, setup, w)
	markGroupAvailable(members)
	w.Status.TargetVersion.BaseWorkerDeploymentVersion = baseVersion("build-c", members["primary"], v1alpha1.VersionStatusInactive)
	w.Status.TargetVersion.TaskQueues = []v1alpha1.TaskQueue{{Name: "jobs"}, {Name: "activity-only"}, {Name: "undeclared"}}
	objects := []client.Object{w, saved.Object}
	for _, member := range members {
		objects = append(objects, member)
	}
	r, _ := newTestReconciler(objects)
	ts := registeredGroup("build-c")
	ts.Versions["build-c"].RegisteredTaskQueues = w.Spec.ExpectedTaskQueues
	applyGroupedStatus(&w.Status, groupState("build-c", saved, members), ts)
	p, err := r.generatePlan(context.Background(), logr.Discard(), w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts)
	require.NoError(t, err)
	require.Len(t, p.startTestWorkflows, 1)
	assert.Equal(t, "jobs", p.startTestWorkflows[0].taskQueue)
	assert.Empty(t, p.cancelTestWorkflows)
	assert.Nil(t, p.UpdateVersionConfig)
	assert.Len(t, w.Status.TargetVersion.TaskQueues, 3)

	w.Status.TargetVersion.TestWorkflows = []v1alpha1.WorkflowExecution{{TaskQueue: "jobs", Status: v1alpha1.WorkflowExecutionStatusCompleted}}
	p, err = r.generatePlan(context.Background(), logr.Discard(), w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts)
	require.NoError(t, err)
	assert.Empty(t, p.startTestWorkflows)
	assert.NotNil(t, p.UpdateVersionConfig)
}

func TestCancelSupersededGatesChecksExactOwnershipAndRun(t *testing.T) {
	for _, test := range []struct {
		name       string
		change     func(*workflowpb.WorkflowExecutionInfo)
		wantError  bool
		wantCancel bool
	}{
		{name: "matching running gate", wantCancel: true},
		{name: "completed gate", change: func(i *workflowpb.WorkflowExecutionInfo) { i.Status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED }},
		{name: "canceled gate", change: func(i *workflowpb.WorkflowExecutionInfo) { i.Status = enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED }},
		{name: "different workflow ID", change: func(i *workflowpb.WorkflowExecutionInfo) { i.Execution.WorkflowId = "accepted-user-work" }, wantError: true},
		{name: "missing run ID", change: func(i *workflowpb.WorkflowExecutionInfo) { i.Execution.RunId = "" }, wantError: true},
		{name: "different workflow type", change: func(i *workflowpb.WorkflowExecutionInfo) { i.Type.Name = "ProcessJob" }, wantError: true},
		{name: "different queue", change: func(i *workflowpb.WorkflowExecutionInfo) { i.TaskQueue = "user-jobs" }, wantError: true},
		{name: "different build", change: func(i *workflowpb.WorkflowExecutionInfo) {
			i.VersioningInfo.VersioningOverride.GetPinned().Version.BuildId = "build-c"
		}, wantError: true},
		{name: "different deployment", change: func(i *workflowpb.WorkflowExecutionInfo) {
			i.VersioningInfo.VersioningOverride.GetPinned().Version.DeploymentName = "another-worker"
		}, wantError: true},
		{name: "missing version override", change: func(i *workflowpb.WorkflowExecutionInfo) { i.VersioningInfo.VersioningOverride = nil }, wantError: true},
		{name: "older pinned string", change: func(i *workflowpb.WorkflowExecutionInfo) {
			i.VersioningInfo.VersioningOverride = &workflowpb.VersioningOverride{Behavior: enumspb.VERSIONING_BEHAVIOR_PINNED, PinnedVersion: "workers.build-b"}
		}, wantCancel: true},
		{name: "older deployment fields", change: func(i *workflowpb.WorkflowExecutionInfo) {
			i.VersioningInfo.VersioningOverride = &workflowpb.VersioningOverride{Behavior: enumspb.VERSIONING_BEHAVIOR_PINNED, Deployment: &deploymentpb.Deployment{SeriesName: "workers", BuildId: "build-b"}}
		}, wantCancel: true},
		{name: "explicit auto upgrade ignores older pinned fields", change: func(i *workflowpb.WorkflowExecutionInfo) {
			i.VersioningInfo.VersioningOverride.Override = &workflowpb.VersioningOverride_AutoUpgrade{AutoUpgrade: true}
			i.VersioningInfo.VersioningOverride.Behavior = enumspb.VERSIONING_BEHAVIOR_PINNED
			i.VersioningInfo.VersioningOverride.PinnedVersion = "workers.build-b"
		}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate := cancelWorkflowConfig{workflowID: temporal.GetTestWorkflowID("workers", "build-b", "jobs"), workflowType: "CheckRelease", buildID: "build-b", taskQueue: "jobs"}
			info := &workflowpb.WorkflowExecutionInfo{
				Execution:      &commonpb.WorkflowExecution{WorkflowId: gate.workflowID, RunId: "gate-run-b"},
				Type:           &commonpb.WorkflowType{Name: "CheckRelease"},
				TaskQueue:      "jobs",
				Status:         enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
				VersioningInfo: &workflowpb.WorkflowExecutionVersioningInfo{VersioningOverride: &workflowpb.VersioningOverride{Override: &workflowpb.VersioningOverride_Pinned{Pinned: &workflowpb.VersioningOverride_PinnedOverride{Version: &deploymentpb.WorkerDeploymentVersion{DeploymentName: "workers", BuildId: "build-b"}}}}},
			}
			if test.change != nil {
				test.change(info)
			}
			c := &gateCancellationClient{description: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info}}
			err := cancelSupersededGates(context.Background(), c, &plan{WorkerDeploymentName: "workers", cancelTestWorkflows: []cancelWorkflowConfig{gate}})
			assert.Equal(t, test.wantError, err != nil)
			assert.Equal(t, []string{gate.workflowID}, c.describedIDs)
			if test.wantCancel {
				assert.Equal(t, []v1alpha1.WorkflowExecution{{WorkflowID: gate.workflowID, RunID: "gate-run-b"}}, c.canceled)
			} else {
				assert.Empty(t, c.canceled)
			}
		})
	}
}

func TestCancelSupersededGatesRetriesFailuresAndIgnoresTypedNotFound(t *testing.T) {
	gate := cancelWorkflowConfig{workflowID: "gate", workflowType: "CheckRelease", buildID: "build-b", taskQueue: "jobs"}
	p := &plan{WorkerDeploymentName: "workers", cancelTestWorkflows: []cancelWorkflowConfig{gate}}
	c := &gateCancellationClient{describeErr: &serviceerror.NotFound{Message: "missing"}}
	assert.NoError(t, cancelSupersededGates(context.Background(), c, p))
	assert.Empty(t, c.canceled)
	c.describeErr = errors.New("workflow not found text is not a typed absence")
	assert.ErrorContains(t, cancelSupersededGates(context.Background(), c, p), "describe superseded gate")
	c.describeErr = nil
	c.description = &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
		Execution: &commonpb.WorkflowExecution{WorkflowId: "gate", RunId: "run"}, Type: &commonpb.WorkflowType{Name: "CheckRelease"}, TaskQueue: "jobs", Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		VersioningInfo: &workflowpb.WorkflowExecutionVersioningInfo{VersioningOverride: &workflowpb.VersioningOverride{Override: &workflowpb.VersioningOverride_Pinned{Pinned: &workflowpb.VersioningOverride_PinnedOverride{Version: &deploymentpb.WorkerDeploymentVersion{DeploymentName: "workers", BuildId: "build-b"}}}}},
	}}
	c.cancelErr = &serviceerror.Unavailable{Message: "retry later"}
	assert.ErrorContains(t, cancelSupersededGates(context.Background(), c, p), "cancel superseded gate")
	c.cancelErr = nil
	assert.NoError(t, cancelSupersededGates(context.Background(), c, p))
	c.cancelErr = &serviceerror.NotFound{Message: "closed during cancellation"}
	assert.NoError(t, cancelSupersededGates(context.Background(), c, p))
	assert.Len(t, c.canceled, 3)
}

func TestCancelSupersededGateReadsLatestRunAfterContinuation(t *testing.T) {
	gate := cancelWorkflowConfig{workflowID: "gate", workflowType: "CheckRelease", buildID: "build-b", taskQueue: "jobs"}
	p := &plan{WorkerDeploymentName: "workers", cancelTestWorkflows: []cancelWorkflowConfig{gate}}
	info := &workflowpb.WorkflowExecutionInfo{
		Execution: &commonpb.WorkflowExecution{WorkflowId: "gate", RunId: "first-run"},
		Type:      &commonpb.WorkflowType{Name: "CheckRelease"}, TaskQueue: "jobs", Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		VersioningInfo: &workflowpb.WorkflowExecutionVersioningInfo{VersioningOverride: &workflowpb.VersioningOverride{Override: &workflowpb.VersioningOverride_Pinned{Pinned: &workflowpb.VersioningOverride_PinnedOverride{Version: &deploymentpb.WorkerDeploymentVersion{DeploymentName: "workers", BuildId: "build-b"}}}}},
	}
	c := &gateCancellationClient{
		description: &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info},
		cancelErr:   &serviceerror.NotFound{Message: "first run continued before cancellation"},
	}
	require.NoError(t, cancelSupersededGates(context.Background(), c, p))
	info.Execution.RunId = "continued-run"
	c.cancelErr = nil
	require.NoError(t, cancelSupersededGates(context.Background(), c, p))
	assert.Equal(t, []string{"", ""}, c.describedRunIDs)
	assert.Equal(t, []v1alpha1.WorkflowExecution{
		{WorkflowID: "gate", RunID: "first-run"},
		{WorkflowID: "gate", RunID: "continued-run"},
	}, c.canceled)
}

func (c *gateStartClient) ExecuteWorkflow(_ context.Context, options sdkclient.StartWorkflowOptions, _ any, _ ...any) (sdkclient.WorkflowRun, error) {
	c.started = append(c.started, options)
	return nil, nil
}
