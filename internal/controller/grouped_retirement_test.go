package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	commonpb "go.temporal.io/api/common/v1"
	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type (
	retirementClient struct {
		sdkclient.Client
		info    map[string]*workflowpb.WorkflowExecutionInfo
		history *retirementHistoryClient
		started []sdkclient.StartWorkflowOptions
		args    [][]any
	}
	retirementHistoryClient struct {
		workflowservice.WorkflowServiceClient
		input      *commonpb.Payloads
		requests   []*workflowservice.GetWorkflowExecutionHistoryRequest
		err        error
		batched    bool
		inputs     map[string]*commonpb.Payloads
		deployment *workflowservice.DescribeWorkerDeploymentResponse
	}
	retirementRoutingHandle struct {
		sdkclient.WorkerDeploymentHandle
		description sdkclient.WorkerDeploymentDescribeResponse
		calls       []sdkclient.WorkerDeploymentSetRampingVersionOptions
		failAt      int
	}
)

func (c *retirementClient) DescribeWorkflowExecution(_ context.Context, id, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	info := c.info[id]
	if info == nil {
		return nil, &serviceerror.NotFound{}
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: info}, nil
}
func (c *retirementClient) WorkflowService() workflowservice.WorkflowServiceClient { return c.history }
func (c *retirementClient) ExecuteWorkflow(_ context.Context, opts sdkclient.StartWorkflowOptions, _ any, args ...any) (sdkclient.WorkflowRun, error) {
	c.started = append(c.started, opts)
	c.args = append(c.args, args)
	return nil, nil
}
func (c *retirementHistoryClient) GetWorkflowExecutionHistory(_ context.Context, req *workflowservice.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	c.requests = append(c.requests, req)
	input := c.input
	if c.inputs != nil {
		input = c.inputs[req.Execution.WorkflowId]
	}
	if c.batched {
		return &workflowservice.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{{EventId: 1, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Input: input}}}, {EventId: 2, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED}}}}, c.err
	}
	return &workflowservice.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{{EventId: 1, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Input: input}}}}}}, c.err
}
func (h *retirementRoutingHandle) Describe(context.Context, sdkclient.WorkerDeploymentDescribeOptions) (sdkclient.WorkerDeploymentDescribeResponse, error) {
	return h.description, nil
}
func (h *retirementRoutingHandle) SetRampingVersion(_ context.Context, opts sdkclient.WorkerDeploymentSetRampingVersionOptions) (sdkclient.WorkerDeploymentSetRampingVersionResponse, error) {
	h.calls = append(h.calls, opts)
	if len(h.calls) == h.failAt {
		return sdkclient.WorkerDeploymentSetRampingVersionResponse{}, &serviceerror.FailedPrecondition{Message: "routing changed"}
	}
	return sdkclient.WorkerDeploymentSetRampingVersionResponse{ConflictToken: []byte("after-assignment")}, nil
}
func approvedRetirements(t *testing.T, p *plan, base sdkclient.Client) *retirementClient {
	t.Helper()
	c := &retirementClient{Client: base, info: make(map[string]*workflowpb.WorkflowExecutionInfo), history: &retirementHistoryClient{}}
	for _, group := range p.groupRetirements {
		id := retirementWorkflowID(p.WorkerDeploymentName, group.buildID)
		c.info[id] = &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: id, RunId: "latest-run"}, Type: &commonpb.WorkflowType{Name: group.saved.Spec.GroupRetirementWorkflow.WorkflowType}, TaskQueue: group.saved.Spec.GroupRetirementWorkflow.TaskQueue, Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, CloseTime: timestamppb.New(time.Now().Add(-time.Hour))}
		input, err := converter.GetDefaultDataConverter().ToPayloads(p.WorkerDeploymentName, group.buildID)
		require.NoError(t, err)
		c.history.input = input
		if len(p.groupRetirements) > 1 {
			if c.history.inputs == nil {
				c.history.inputs = make(map[string]*commonpb.Payloads)
			}
			c.history.inputs[id] = input
		}
	}
	return c
}
func retirementPlan(t *testing.T, versionStatus v1alpha1.VersionStatus) (*plan, *retirementRoutingHandle) {
	t.Helper()
	r, _ := newTestReconciler(nil)
	w := groupedWorker("build-b")
	saved, members := savedGroup(t, r, w)
	p := groupPlan()
	p.WorkerDeploymentName = "test-workers"
	p.allowGroupedDrainage = true
	p.TemporalNamespace = "test-namespace"
	p.groupRetirements = []groupRetirement{{buildID: "build-b", saved: saved, actual: members, desired: members, version: &temporal.VersionInfo{BuildID: "build-b", Status: versionStatus}}}
	h := &retirementRoutingHandle{description: sdkclient.WorkerDeploymentDescribeResponse{ConflictToken: []byte("before"), Info: sdkclient.WorkerDeploymentInfo{RoutingConfig: sdkclient.WorkerDeploymentRoutingConfig{CurrentVersion: &worker.WorkerDeploymentVersion{DeploymentName: p.WorkerDeploymentName, BuildID: "build-c"}}}}}
	return p, h
}
func TestGroupedRetirementInitializesServerDrainageWithoutStoppingMembers(t *testing.T) {
	for _, state := range []v1alpha1.VersionStatus{v1alpha1.VersionStatusInactive, v1alpha1.VersionStatusNotRegistered, v1alpha1.VersionStatusCreated} {
		t.Run(string(state), func(t *testing.T) {
			p, h := retirementPlan(t, state)
			c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
			require.NoError(t, planGroupRetirements(context.Background(), c, p))
			require.Len(t, h.calls, 2)
			assert.Equal(t, "build-b", h.calls[0].BuildID)
			assert.Zero(t, h.calls[0].Percentage)
			assert.True(t, h.calls[0].AllowNoPollers)
			assert.True(t, h.calls[0].IgnoreMissingTaskQueues)
			assert.Equal(t, []byte("before"), h.calls[0].ConflictToken)
			assert.Empty(t, h.calls[1].BuildID)
			assert.Equal(t, []byte("after-assignment"), h.calls[1].ConflictToken)
			assert.False(t, h.calls[1].AllowNoPollers)
			assert.Empty(t, p.ScaleDeployments)
			assert.Empty(t, p.DeleteDeployments)
		})
	}
}
func TestGroupedRetirementRespectsReleaseRoutingAndRecoversInterruptedPair(t *testing.T) {
	for _, test := range []struct {
		name     string
		ramp     string
		percent  float32
		ordinary bool
		failAt   int
		want     int
	}{
		{"existing canary", "build-c", 25, false, 0, 0}, {"other zero ramp", "build-c", 0, false, 0, 0}, {"approved positive ramp", "build-b", 25, false, 0, 0}, {"release update wins", "", 0, true, 0, 0}, {"resume approved zero ramp", "build-b", 0, false, 0, 1}, {"first write conflict", "", 0, false, 1, 1}, {"clear conflict", "", 0, false, 2, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, h := retirementPlan(t, v1alpha1.VersionStatusInactive)
			if test.ramp != "" {
				h.description.Info.RoutingConfig.RampingVersion = &worker.WorkerDeploymentVersion{BuildID: test.ramp}
			}
			h.description.Info.RoutingConfig.RampingVersionPercentage = test.percent
			h.failAt = test.failAt
			if test.ordinary {
				p.UpdateVersionConfig = &planner.VersionConfig{BuildID: "build-c", SetCurrent: true}
			}
			c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
			err := planGroupRetirements(context.Background(), c, p)
			assert.Equal(t, test.failAt != 0, err != nil)
			assert.Len(t, h.calls, test.want)
			assert.Empty(t, p.ScaleDeployments)
		})
	}
}
func TestGroupedRetirementUsesCurrentRoutingAuthority(t *testing.T) {
	for _, test := range []struct {
		name           string
		saved, current v1alpha1.DefaultVersionUpdateStrategy
		writes         int
	}{
		{"saved manual current all at once", v1alpha1.UpdateManual, v1alpha1.UpdateAllAtOnce, 2},
		{"saved all at once current manual", v1alpha1.UpdateAllAtOnce, v1alpha1.UpdateManual, 0},
		{"saved all at once current progressive", v1alpha1.UpdateAllAtOnce, v1alpha1.UpdateProgressive, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, h := retirementPlan(t, v1alpha1.VersionStatusInactive)
			p.groupRetirements[0].saved.Spec.RolloutStrategy.Strategy = test.saved
			current := groupedWorker("build-c")
			current.Spec.RolloutStrategy.Strategy = test.current
			if test.current == v1alpha1.UpdateProgressive {
				current.Spec.RolloutStrategy.Steps = []v1alpha1.RolloutStep{{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: time.Minute}}}
			}
			r, _ := newTestReconciler(nil)
			generated, err := r.generatePlan(context.Background(), logr.Discard(), current, v1alpha1.ConnectionSpec{}, &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{}})
			require.NoError(t, err)
			p.allowGroupedDrainage = generated.allowGroupedDrainage
			c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
			require.NoError(t, planGroupRetirements(context.Background(), c, p))
			assert.Len(t, h.calls, test.writes)
			assert.Empty(t, p.ScaleDeployments)
		})
	}
}

func TestGroupedRetirementApprovalDoesNotReplaceTemporalDrainage(t *testing.T) {
	for _, state := range []v1alpha1.VersionStatus{v1alpha1.VersionStatusDraining, v1alpha1.VersionStatusDrained} {
		t.Run(string(state), func(t *testing.T) {
			p, h := retirementPlan(t, state)
			drained := time.Now().Add(-time.Hour)
			p.groupRetirements[0].version.DrainedSince = &drained
			c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
			require.NoError(t, planGroupRetirements(context.Background(), c, p))
			assert.Equal(t, state == v1alpha1.VersionStatusDrained, len(p.ScaleDeployments) == 2)
			assert.Empty(t, h.calls)
		})
	}
}
func TestGroupedRetirementStartsWithoutDeadlinesAndBindsLatestRunInput(t *testing.T) {
	p, _ := retirementPlan(t, v1alpha1.VersionStatusInactive)
	c := &retirementClient{info: map[string]*workflowpb.WorkflowExecutionInfo{}}
	approved, err := completedRetirement(context.Background(), c, p, p.groupRetirements[0])
	require.NoError(t, err)
	assert.False(t, approved)
	require.Len(t, c.started, 1)
	assert.Zero(t, c.started[0].WorkflowExecutionTimeout)
	assert.Zero(t, c.started[0].WorkflowRunTimeout)
	assert.Nil(t, c.started[0].VersioningOverride)
	assert.Equal(t, []any{p.WorkerDeploymentName, "build-b"}, c.args[0])
	c = approvedRetirements(t, p, nil)
	c.history.batched = true
	approved, err = completedRetirement(context.Background(), c, p, p.groupRetirements[0])
	require.NoError(t, err)
	assert.True(t, approved)
	require.Len(t, c.history.requests, 1)
	assert.Equal(t, int32(1), c.history.requests[0].MaximumPageSize)
	assert.Equal(t, "latest-run", c.history.requests[0].Execution.RunId)
	c.history.input, err = converter.GetDefaultDataConverter().ToPayloads(p.WorkerDeploymentName, "build-c")
	require.NoError(t, err)
	_, err = completedRetirement(context.Background(), c, p, p.groupRetirements[0])
	assert.ErrorContains(t, err, "does not match")
	c.history.err = errors.New("unavailable")
	_, err = completedRetirement(context.Background(), c, p, p.groupRetirements[0])
	assert.ErrorContains(t, err, "read group retirement input")
}
func TestGroupedRetirementRejectsVersionedExecutionAndReselection(t *testing.T) {
	p, _ := retirementPlan(t, v1alpha1.VersionStatusInactive)
	c := approvedRetirements(t, p, nil)
	id := retirementWorkflowID(p.WorkerDeploymentName, "build-b")
	c.info[id].VersioningInfo = &workflowpb.WorkflowExecutionVersioningInfo{Behavior: enumspb.VERSIONING_BEHAVIOR_PINNED}
	_, err := completedRetirement(context.Background(), c, p, p.groupRetirements[0])
	assert.ErrorContains(t, err, "unversioned")
	p.targetGroup = p.groupRetirements[0].saved
	w := groupedWorker("build-b")
	assert.ErrorContains(t, checkTargetGroupRetirement(context.Background(), c, w, p), "new build ID")
	for _, info := range c.info {
		info.Status = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
	}
	assert.ErrorContains(t, checkTargetGroupRetirement(context.Background(), c, w, p), "new build ID")
	delete(c.info, id)
	assert.NoError(t, checkTargetGroupRetirement(context.Background(), c, w, p))
}
func TestGroupedDrainedDelayDoesNotRestoreAlreadyStoppedMembers(t *testing.T) {
	p, h := retirementPlan(t, v1alpha1.VersionStatusDrained)
	g := &p.groupRetirements[0]
	drained := time.Now().Add(-time.Minute)
	g.version.DrainedSince = &drained
	g.saved.Spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{}
	g.saved.Spec.SunsetStrategy.DeleteDelay = &metav1.Duration{Duration: time.Hour}
	for _, member := range g.actual {
		zero := int32(0)
		member.Spec.Replicas = &zero
		p.ScaleDeployments[k8s.NewObjectRef(member)] = 3
	}
	c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
	require.NoError(t, planGroupRetirements(context.Background(), c, p))
	assert.Empty(t, p.ScaleDeployments)
}
func TestGroupedRetirementIncompleteOrFailedCheckRetainsMembers(t *testing.T) {
	for _, state := range []enumspb.WorkflowExecutionStatus{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW} {
		t.Run(strings.TrimPrefix(state.String(), "WORKFLOW_EXECUTION_STATUS_"), func(t *testing.T) {
			p, h := retirementPlan(t, v1alpha1.VersionStatusDrained)
			c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
			for _, info := range c.info {
				info.Status = state
			}
			require.NoError(t, planGroupRetirements(context.Background(), c, p))
			assert.Empty(t, p.ScaleDeployments)
			assert.Empty(t, h.calls)
		})
	}
}

type (
	retirementReconcileHandle struct{ retirementRoutingHandle }
	retirementReconcileClient struct{ *retirementClient }
)

func (h *retirementReconcileHandle) DescribeVersion(_ context.Context, opts sdkclient.WorkerDeploymentDescribeVersionOptions) (sdkclient.WorkerDeploymentVersionDescription, error) {
	return sdkclient.WorkerDeploymentVersionDescription{Info: sdkclient.WorkerDeploymentVersionInfo{Version: worker.WorkerDeploymentVersion{BuildID: opts.BuildID}, TaskQueuesInfos: []sdkclient.WorkerDeploymentTaskQueueInfo{{Name: "jobs", Type: sdkclient.TaskQueueTypeWorkflow}, {Name: "jobs", Type: sdkclient.TaskQueueTypeActivity}}}}, nil
}
func (c *retirementReconcileClient) DescribeTaskQueue(context.Context, string, enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
	return &workflowservice.DescribeTaskQueueResponse{Pollers: []*taskqueuepb.PollerInfo{{Identity: "healthy-worker"}}}, nil
}
func (c *retirementHistoryClient) DescribeWorkerDeployment(context.Context, *workflowservice.DescribeWorkerDeploymentRequest, ...grpc.CallOption) (*workflowservice.DescribeWorkerDeploymentResponse, error) {
	return c.deployment, nil
}

func TestGroupedRetirementFailureDoesNotBlockOtherObsoleteGroups(t *testing.T) {
	p, h := retirementPlan(t, v1alpha1.VersionStatusDrained)
	second, _ := retirementPlan(t, v1alpha1.VersionStatusDrained)
	second.groupRetirements[0].buildID = "build-a"
	for _, member := range second.groupRetirements[0].actual {
		member.Name += "-other"
	}
	drained := time.Now().Add(-time.Hour)
	p.groupRetirements[0].version.DrainedSince = &drained
	second.groupRetirements[0].version.DrainedSince = &drained
	p.groupRetirements = append(p.groupRetirements, second.groupRetirements[0])
	c := approvedRetirements(t, p, newStubTemporalClientWithHandle(h))
	c.info[retirementWorkflowID(p.WorkerDeploymentName, "build-b")].TaskQueue = "wrong-management-queue"
	assert.Error(t, planGroupRetirements(context.Background(), c, p))
	assert.Len(t, p.ScaleDeployments, 2)
	for ref := range p.ScaleDeployments {
		assert.Contains(t, ref.Name, "-other")
	}
}

func TestReconcileGroupedRetirementFailureKeepsHealthyCurrentReleaseReady(t *testing.T) {
	ctx := context.Background()
	t.Setenv(IdentityEnvKey, "test-controller")
	t.Setenv(IdentitySuffixEnvKey, "retirement")
	current := groupedWorker("build-c")
	old := groupedWorker("build-b")
	setup, _ := newTestReconciler(nil)
	savedCurrent, membersCurrent := savedGroup(t, setup, current)
	savedOld, membersOld := savedGroup(t, setup, old)
	markGroupAvailable(membersCurrent)
	markGroupAvailable(membersOld)
	connection := makeNoCredsConnection(current.Spec.WorkerOptions.ConnectionRef.Name, current.Namespace, "test:7233")
	objects := []client.Object{current, connection, savedCurrent.Object, savedOld.Object}
	for _, members := range []map[string]*appsv1.Deployment{membersCurrent, membersOld} {
		for _, member := range members {
			objects = append(objects, member)
		}
	}
	r, recorder := newTestReconciler(objects)
	deploymentName := k8s.ComputeWorkerDeploymentName(current)
	h := &retirementReconcileHandle{}
	base := newStubTemporalClientWithHandle(h)
	p := groupPlan()
	p.WorkerDeploymentName = deploymentName
	p.groupRetirements = []groupRetirement{{buildID: "build-b", saved: savedOld}}
	c := &retirementReconcileClient{retirementClient: approvedRetirements(t, p, base)}
	c.info[retirementWorkflowID(deploymentName, "build-b")].TaskQueue = "wrong-management-queue"
	versionC := &deploymentpb.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildId: "build-c"}
	versionB := &deploymentpb.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildId: "build-b"}
	c.history.deployment = &workflowservice.DescribeWorkerDeploymentResponse{ConflictToken: []byte("stable"), WorkerDeploymentInfo: &deploymentpb.WorkerDeploymentInfo{RoutingConfig: &deploymentpb.RoutingConfig{CurrentDeploymentVersion: versionC}, LastModifierIdentity: getControllerIdentity(), ManagerIdentity: getControllerIdentity(), VersionSummaries: []*deploymentpb.WorkerDeploymentInfo_WorkerDeploymentVersionSummary{{DeploymentVersion: versionC, Status: enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_CURRENT}, {DeploymentVersion: versionB, Status: enumspb.WORKER_DEPLOYMENT_VERSION_STATUS_INACTIVE}}}}
	r.TemporalClientPool.SetClientForTesting(noCredsPoolKey(connection.Spec.HostPort, current.Spec.WorkerOptions.TemporalNamespace), c)
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: current.Name, Namespace: current.Namespace}})
	require.NoError(t, err)
	assert.Positive(t, result.RequeueAfter)
	var observed v1alpha1.WorkerDeployment
	require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(current), &observed))
	ready := meta.FindStatusCondition(observed.Status.Conditions, v1alpha1.ConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, v1alpha1.ReasonRolloutComplete, ready.Reason)
	assert.NotNil(t, observed.Status.TargetVersion.HealthySince)
	foundWarning := false
	for len(recorder.Events) > 0 {
		event := <-recorder.Events
		if strings.Contains(event, ReasonGroupRetirementFailed) {
			foundWarning = true
		}
	}
	assert.True(t, foundWarning)
	for _, oldMember := range membersOld {
		var retained appsv1.Deployment
		require.NoError(t, r.Get(ctx, client.ObjectKeyFromObject(oldMember), &retained))
		assert.Positive(t, *retained.Spec.Replicas)
	}
}
