//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	sdkclient "go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestGroupedRetirementIntegration runs a real Temporal server with reactivation
// disabled. The application approves retirement before a pinned activity finishes;
// every member must remain until Temporal drains the execution, then disappear
// automatically in response to the controller's Kubernetes replica changes.
func TestGroupedRetirementIntegration(t *testing.T) {
	cfg, kc, _, _, cleanup := setupTestEnvironment(t)
	defer cleanup()
	ns := createTestNamespace(t, kc)
	defer cleanupTestNamespace(t, cfg, kc, ns)
	ts := groupedRetirementTestServer(t)
	ctx := context.Background()
	testCase := testhelpers.NewTestCase().WithInput(testhelpers.NewWorkerDeploymentBuilder().WithManualStrategy().WithTargetTemplate("release-b")).BuildWithValues("grouped-retirement", ns.Name, ts.GetDefaultNamespace())
	twd := testCase.GetTWD()
	twd.Spec.WorkerOptions.UnsafeCustomBuildID = "build-b"
	twd.Spec.RolloutStrategy = v1alpha1.RolloutStrategy{Strategy: v1alpha1.UpdateManual, Gate: &v1alpha1.GateWorkflowConfig{WorkflowType: "CheckReadiness"}}
	one := int32(1)
	twd.Spec.Deployment.Replicas = &one
	for i := 1; i < 9; i++ {
		twd.Spec.AdditionalDeployments = append(twd.Spec.AdditionalDeployments, v1alpha1.AdditionalDeployment{Name: fmt.Sprintf("member-%d", i), Replicas: 1, Template: *twd.Spec.Deployment.Template.DeepCopy()})
	}
	twd.Spec.ExpectedTaskQueues = []v1alpha1.ExpectedTaskQueue{{Name: "grouped-jobs", Kinds: []v1alpha1.TaskQueueKind{v1alpha1.TaskQueueWorkflow}}}
	twd.Spec.GroupRetirementWorkflow = &v1alpha1.GroupRetirementWorkflowConfig{WorkflowType: "AuthorizeRetirement", TaskQueue: "grouped-management"}
	twd.Spec.SunsetStrategy = v1alpha1.SunsetStrategy{ScaledownDelay: &metav1.Duration{}, DeleteDelay: &metav1.Duration{}}
	connection := &v1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: twd.Spec.WorkerOptions.ConnectionRef.Name, Namespace: ns.Name}, Spec: v1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()}}
	require.NoError(t, kc.Create(ctx, connection))

	management := sdkworker.New(ts.GetDefaultClient(), "grouped-management", sdkworker.Options{})
	management.RegisterWorkflowWithOptions(func(workflow.Context, string, string) error { return nil }, workflow.RegisterOptions{Name: "AuthorizeRetirement"})
	require.NoError(t, management.Start())
	defer management.Stop()
	activityStarted := make(chan struct{})
	releaseActivity := make(chan struct{})
	var startedOnce sync.Once
	simctx, stopSimulation := context.WithCancel(ctx)
	simulationDone := make(chan error, 1)
	go func() {
		simulationDone <- simulateGroupedPods(simctx, kc, ts, twd, activityStarted, releaseActivity, &startedOnce)
	}()
	defer func() { stopSimulation(); require.NoError(t, <-simulationDone) }()
	require.NoError(t, kc.Create(ctx, twd))
	deploymentName := k8s.ComputeWorkerDeploymentName(twd)
	version := sdkworker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: "build-b"}
	eventually(t, 30*time.Second, time.Second, func() error {
		_, err := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName).DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: version.BuildID})
		return err
	})
	run, err := ts.GetDefaultClient().ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{ID: "accepted-job", TaskQueue: "grouped-jobs", VersioningOverride: &sdkclient.PinnedVersioningOverride{Version: version}}, "PinnedJob")
	require.NoError(t, err)
	select {
	case <-activityStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("pinned activity did not start")
	}
	handle := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName)
	before, err := handle.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: "build-b"})
	require.NoError(t, err)
	assert.True(t, before.Info.RampingSinceTime.IsZero())
	assert.True(t, before.Info.CurrentSinceTime.IsZero())
	eventually(t, 30*time.Second, time.Second, func() error {
		gate, err := ts.GetDefaultClient().DescribeWorkflowExecution(ctx, "test-"+deploymentName+":build-b-grouped-jobs", "")
		if err != nil {
			return err
		}
		if gate.GetWorkflowExecutionInfo().GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			return fmt.Errorf("old readiness workflow did not remain open")
		}
		return nil
	})
	key := types.NamespacedName{Namespace: ns.Name, Name: twd.Name}
	var next v1alpha1.WorkerDeployment
	require.NoError(t, kc.Get(ctx, key, &next))
	next.Spec.WorkerOptions.UnsafeCustomBuildID = "build-c"
	next.Spec.Deployment.Template.Spec.Containers[0].Image = "release-c"
	next.Spec.RolloutStrategy.Strategy = v1alpha1.UpdateAllAtOnce

	require.NoError(t, kc.Update(ctx, &next))

	// The application approves promptly, but the engine still owns a running
	// pinned activity. Approval alone must not stop a single member.
	eventually(t, 45*time.Second, time.Second, func() error {
		retirement, err := ts.GetDefaultClient().DescribeWorkflowExecution(ctx, "retire-"+deploymentName+":build-b", "")
		if err != nil {
			return err
		}
		if retirement.GetWorkflowExecutionInfo().GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED {
			return fmt.Errorf("retirement authorization still running")
		}
		if err := kc.Get(ctx, key, &next); err != nil {
			return err
		}
		for _, old := range next.Status.DeprecatedVersions {
			if old.BuildID == "build-b" && old.Status == v1alpha1.VersionStatusDraining {
				return groupedCurrentReady(ctx, kc, ts, key, deploymentName, "build-c")
			}
		}
		return fmt.Errorf("server has not started drainage")
	})
	for observation := 0; observation < 4; observation++ {
		var members appsv1.DeploymentList
		require.NoError(t, kc.List(ctx, &members, client.InNamespace(ns.Name), client.MatchingLabels{k8s.BuildIDLabel: "build-b"}))
		require.Len(t, members.Items, 9)
		for _, member := range members.Items {
			assert.EqualValues(t, 1, *member.Spec.Replicas)
		}
		time.Sleep(time.Second)
	}
	close(releaseActivity)
	require.NoError(t, run.Get(ctx, nil))
	eventually(t, 90*time.Second, time.Second, func() error {
		var members appsv1.DeploymentList
		if err := kc.List(ctx, &members, client.InNamespace(ns.Name), client.MatchingLabels{k8s.BuildIDLabel: "build-b"}); err != nil {
			return err
		}
		if len(members.Items) != 0 {
			return fmt.Errorf("%d old members remain", len(members.Items))
		}
		var saved appsv1.ControllerRevisionList
		if err := kc.List(ctx, &saved, client.InNamespace(ns.Name), client.MatchingLabels{k8s.BuildIDLabel: "build-b"}); err != nil {
			return err
		}
		if len(saved.Items) != 0 {
			return fmt.Errorf("saved obsolete build remains")
		}
		return nil
	})
	var active appsv1.DeploymentList
	require.NoError(t, kc.List(ctx, &active, client.InNamespace(ns.Name), client.MatchingLabels{k8s.BuildIDLabel: "build-c"}))
	require.Len(t, active.Items, 9)
	for _, member := range active.Items {
		assert.Positive(t, *member.Spec.Replicas)
	}
	eventually(t, 30*time.Second, time.Second, func() error {
		return groupedCurrentReady(ctx, kc, ts, key, deploymentName, "build-c")
	})
}

// simulateGroupedPods supplies the pod execution that envtest intentionally omits.
// It starts real workers only for requested replicas and stops them only after
// the controller requests zero, then reports the observed pod count to Kubernetes.
func simulateGroupedPods(ctx context.Context, kc client.Client, ts groupedRetirementServer, owner *v1alpha1.WorkerDeployment, activityStarted chan struct{}, releaseActivity <-chan struct{}, startedOnce *sync.Once) error {
	workers := make(map[string]sdkworker.Worker)
	defer func() {
		for _, w := range workers {
			w.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
		var deployments appsv1.DeploymentList
		if err := kc.List(ctx, &deployments, client.InNamespace(owner.Namespace), client.MatchingLabels{k8s.WorkerDeploymentNameLabel: owner.Name}); err != nil {
			return err
		}
		for _, deployment := range deployments.Items {
			if deployment.Spec.Replicas == nil {
				return fmt.Errorf("missing fixed member replica count")
			}
			count := *deployment.Spec.Replicas
			if count > 0 && workers[deployment.Name] == nil {
				build := deployment.Labels[k8s.BuildIDLabel]
				w := sdkworker.New(ts.GetDefaultClient(), "grouped-jobs", sdkworker.Options{DeploymentOptions: sdkworker.DeploymentOptions{UseVersioning: true, Version: sdkworker.WorkerDeploymentVersion{DeploymentName: k8s.ComputeWorkerDeploymentName(owner), BuildID: build}, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}})
				w.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
					if build == "build-b" {
						return workflow.Await(ctx, func() bool { return false })
					}
					return nil
				}, workflow.RegisterOptions{Name: "CheckReadiness"})
				w.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
					ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
					return workflow.ExecuteActivity(ctx, "PublishResult").Get(ctx, nil)
				}, workflow.RegisterOptions{Name: "PinnedJob"})
				w.RegisterActivityWithOptions(func(ctx context.Context) error {
					startedOnce.Do(func() { close(activityStarted) })
					select {
					case <-releaseActivity:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, activity.RegisterOptions{Name: "PublishResult"})
				if err := w.Start(); err != nil {
					return err
				}
				workers[deployment.Name] = w
			}
			if count == 0 && workers[deployment.Name] != nil {
				workers[deployment.Name].Stop()
				delete(workers, deployment.Name)
			}
			if deployment.Status.ObservedGeneration == deployment.Generation && deployment.Status.Replicas == count {
				continue
			}
			deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: deployment.Generation, Replicas: count, UpdatedReplicas: count, ReadyReplicas: count, AvailableReplicas: count, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}}
			if err := kc.Status().Update(ctx, &deployment); err != nil && !apierrors.IsConflict(err) {
				return err
			}
		}
	}
}

type (
	// groupedRetirementServer lets this test use the server library or a separately
	// built historical server without changing the controller's SDK dependencies.
	groupedRetirementServer interface {
		GetDefaultClient() sdkclient.Client
		GetDefaultNamespace() string
		GetFrontendHostPort() string
	}
	groupedExternalServer struct {
		client             sdkclient.Client
		namespace, address string
	}
)

func (s *groupedExternalServer) GetDefaultClient() sdkclient.Client { return s.client }
func (s *groupedExternalServer) GetDefaultNamespace() string        { return s.namespace }
func (s *groupedExternalServer) GetFrontendHostPort() string        { return s.address }

// groupedRetirementTestServer accepts an external server only at a literal
// loopback address and in an explicitly named test namespace. With no address it
// creates the standard in-process server with reactivation disabled.
func groupedRetirementTestServer(t *testing.T) groupedRetirementServer {
	t.Helper()
	address := os.Getenv("TEMPORAL_GROUP_RETIREMENT_TEST_ADDRESS")
	namespace := os.Getenv("TEMPORAL_GROUP_RETIREMENT_TEST_NAMESPACE")
	var server groupedRetirementServer
	if address != "" || namespace != "" {
		host, _, err := net.SplitHostPort(address)
		require.NoError(t, err)
		ip := net.ParseIP(host)
		require.NotNil(t, ip, "external test server requires a literal loopback IP")
		require.True(t, ip.IsLoopback(), "external test server must be loopback")
		require.True(t, strings.HasPrefix(namespace, "temporaltest-"), "external server requires an isolated temporaltest- namespace")
		c, err := sdkclient.Dial(sdkclient.Options{HostPort: address, Namespace: namespace})
		require.NoError(t, err)
		t.Cleanup(c.Close)
		server = &groupedExternalServer{client: c, namespace: namespace, address: address}
	} else {
		dc := dynamicconfig.NewMemoryClient()
		dc.OverrideValue(dynamicconfig.MakeKey("history.enableVersionReactivationSignals"), false)
		// A live long poll must refresh its record before the test cache expires it.
		dc.OverrideValue(dynamicconfig.MakeKey("matching.PollerHistoryTTL"), 5*time.Second)
		dc.OverrideValue(dynamicconfig.MakeKey("matching.longPollExpirationInterval"), time.Second)
		dc.OverrideValue(dynamicconfig.MakeKey("matching.wv.VersionDrainageStatusVisibilityGracePeriod"), time.Second)
		dc.OverrideValue(dynamicconfig.MakeKey("matching.wv.VersionDrainageStatusRefreshInterval"), time.Second)
		server = temporaltest.NewServer(temporaltest.WithT(t), temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dc)))
	}
	cluster, err := server.GetDefaultClient().WorkflowService().GetClusterInfo(context.Background(), &workflowservice.GetClusterInfoRequest{})
	require.NoError(t, err)
	t.Logf("observed Temporal server version: %s", cluster.GetServerVersion())
	if expected := os.Getenv("TEMPORAL_GROUP_RETIREMENT_TEST_VERSION"); expected != "" {
		require.Equal(t, expected, cluster.GetServerVersion())
	}
	return server
}

// groupedCurrentReady checks both routing in Temporal and rollout completion in
// Kubernetes, so surviving Deployment objects alone cannot make the test pass.
func groupedCurrentReady(ctx context.Context, kc client.Client, ts groupedRetirementServer, key types.NamespacedName, deploymentName, buildID string) error {
	description, err := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName).Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
	if err != nil {
		return err
	}
	current := description.Info.RoutingConfig.CurrentVersion
	if current == nil || current.BuildID != buildID {
		return fmt.Errorf("Temporal has not made build %s current", buildID)
	}
	var resource v1alpha1.WorkerDeployment
	if err := kc.Get(ctx, key, &resource); err != nil {
		return err
	}
	ready := meta.FindStatusCondition(resource.Status.Conditions, v1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != v1alpha1.ReasonRolloutComplete {
		return fmt.Errorf("current grouped release is not Ready with RolloutComplete")
	}
	return nil
}
