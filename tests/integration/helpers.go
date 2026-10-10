//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalClient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
	"go.temporal.io/server/temporaltest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	testShortPollerHistoryTTL            = time.Second
	testDrainageVisibilityGracePeriod    = time.Second
	testDrainageRefreshInterval          = time.Second
	testMaxVersionsIneligibleForDeletion = 5
	testMaxVersionsInDeployment          = 6
	testControllerIdentityPrefix         = "test-controller-identity"
	testControllerIdentitySuffix         = "123"
	testControllerIdentity               = testControllerIdentityPrefix + "/" + testControllerIdentitySuffix
)

type testCase struct {
	name    string
	builder *testhelpers.TestCaseBuilder
}

// testWorkerDeploymentCreation tests the creation of a WorkerDeployment and waits for the expected status
func testWorkerDeploymentCreation(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	tc testhelpers.TestCase,
) {
	twd := tc.GetTWD()
	expectedStatus := tc.GetExpectedStatus()

	t.Log("Creating a Connection")
	temporalConnection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      twd.Spec.WorkerOptions.ConnectionRef.Name,
			Namespace: twd.Namespace,
		},
		Spec: temporaliov1alpha1.ConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create Connection: %v", err)
	}

	env := testhelpers.TestEnv{
		K8sClient:                  k8sClient,
		Mgr:                        mgr,
		Ts:                         ts,
		Connection:                 temporalConnection,
		ExistingDeploymentReplicas: tc.GetExistingDeploymentReplicas(),
		ExistingDeploymentImages:   tc.GetExistingDeploymentImages(),
		ExpectedDeploymentReplicas: tc.GetExpectedDeploymentReplicas(),
	}

	makePreliminaryStatusTrue(ctx, t, env, twd, tc.GetPreviouslyCurrentImages())

	// verify that temporal state matches the preliminary status, to confirm that makePreliminaryStatusTrue worked
	verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, twd.Status, 30*time.Second, 5*time.Second)

	// apply post-status setup function
	if f := tc.GetSetupFunc(); f != nil {
		f(t, ctx, tc, env)
	}

	// Apply any test-specific mutations to the TWD before it is created.
	if f := tc.GetTWDMutatorFunc(); f != nil {
		f(twd)
	}

	t.Log("Creating a WorkerDeployment")
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create WorkerDeployment: %v", err)
	}

	// k8sClient.Create strips the status subresource, so the TWD starts with an empty
	// status. Not guaranteed to precede the controller's first reconcile, but seeding it
	// here helps test cases with pre-existing target/deprecated versions converge faster
	// and avoid flaking against the eventually timeouts below.
	if twd.Status.TargetVersion.BuildID != "" {
		if err := k8sClient.Status().Update(ctx, twd); err != nil {
			t.Fatalf("failed to pre-apply TWD status: %v", err)
		}
	}

	// Hook: runs after TWD creation but before waiting for the target Deployment.
	// Use this to assert blocking behaviour and then unblock the rollout.
	if f := tc.GetPostTWDCreateFunc(); f != nil {
		f(t, ctx, tc, env)
	}

	t.Log("Waiting for the controller to reconcile")
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))

	// only wait for and create the deployment if it is expected
	if expectedStatus.TargetVersion.Status != temporaliov1alpha1.VersionStatusNotRegistered {
		waitForExpectedTargetDeployment(t, twd, env, 30*time.Second)
		workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, twd.Namespace)
		defer handleStopFuncs(workerStopFuncs)
	}

	if wait := tc.GetWaitTime(); wait != nil {
		time.Sleep(*wait)
	}
	verifyWorkerDeploymentStatusEventually(t, ctx, env, twd.Name, twd.Namespace, expectedStatus, 30*time.Second, 5*time.Second)
	verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, *expectedStatus, 30*time.Second, 5*time.Second)

	// apply post-expected-status validation function
	if f := tc.GetValidatorFunc(); f != nil {
		tc.GetValidatorFunc()(t, ctx, tc, env)
	}
}

// setupKubebuilderAssets sets up the KUBEBUILDER_ASSETS environment variable if not already set
func setupKubebuilderAssets() error {
	if os.Getenv("KUBEBUILDER_ASSETS") != "" {
		return nil // Already set
	}

	// Get the repository root to find the setup-envtest binary
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return errors.New("failed to get current file path")
	}
	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "../.."))
	if err != nil {
		return fmt.Errorf("failed to get repository root: %v", err)
	}

	// Use the correct version and path that matches the Makefile
	setupEnvtestPath := filepath.Join(repoRoot, "bin", "setup-envtest")
	binDir := filepath.Join(repoRoot, "bin")
	cmd := exec.Command(setupEnvtestPath, "use", "1.27.1", "--bin-dir", binDir, "-p", "path")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to run setup-envtest: %v", err)
	}

	// The output with -p path flag is just the path, no need to parse
	assetsPath := strings.TrimSpace(string(output))
	if len(assetsPath) > 0 {
		os.Setenv("KUBEBUILDER_ASSETS", assetsPath)
	}

	return nil
}

func getRepoRoot(t *testing.T) string {
	// Get the current file's directory
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("failed to get current file path")
	}

	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "../.."))
	if err != nil {
		t.Fatalf("failed to get repository root: %v", err)
	}
	return repoRoot
}

// setupTestEnvironment sets up the test environment with envtest
func setupTestEnvironment(t *testing.T) (*rest.Config, client.Client, manager.Manager, *clientpool.ClientPool, func()) {
	// Set faster reconcile interval for testing
	t.Setenv("RECONCILE_INTERVAL", "1s")
	t.Setenv(controller.IdentityEnvKey, testControllerIdentityPrefix)
	t.Setenv(controller.IdentitySuffixEnvKey, testControllerIdentitySuffix)
	if kubeAssets := os.Getenv("KUBEBUILDER_ASSETS"); kubeAssets == "" {
		t.Skip("Skipping because KUBEBUILDER_ASSETS not set")
	}

	// set max versions value for testing
	t.Setenv(controller.MaxDeploymentVersionsIneligibleForDeletionEnvKey, fmt.Sprintf("%d", testMaxVersionsIneligibleForDeletion))

	// Setup kubebuilder assets for IDE testing
	if err := setupKubebuilderAssets(); err != nil {
		t.Logf("Warning: Could not setup kubebuilder assets automatically: %v", err)
		t.Logf("You may need to run 'make envtest' first or set KUBEBUILDER_ASSETS manually")
	}

	logf.SetLogger(zap.New(zap.WriteTo(os.Stdout), zap.UseDevMode(true)))

	t.Log("bootstrapping test environment")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(getRepoRoot(t), "helm", "temporal-worker-controller-crds", "templates"),
		},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("failed to start test environment: %v", err)
	}

	err = temporaliov1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatalf("failed to create k8s client: %v", err)
	}

	// Create manager
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Create client pool
	clientPool := clientpool.New(log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   false,
		Level:       nil,
		ReplaceAttr: nil,
	}))), k8sClient, nil)

	// Set up controller
	reconciler := &controller.WorkerDeploymentReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		TemporalClientPool:  clientPool,
		Recorder:            mgr.GetEventRecorderFor("temporal-worker-controller"),
		DisableRecoverPanic: true,
		MaxDeploymentVersionsIneligibleForDeletion: controller.GetControllerMaxDeploymentVersionsIneligibleForDeletion(),
		WRTHPAMatchLabelsStripTemporalPrefix:       controller.GetWRTHPAMatchLabelsStripTemporalPrefix(),
	}
	err = reconciler.SetupWithManager(mgr)
	if err != nil {
		t.Fatalf("failed to set up controller: %v", err)
	}

	// Start manager
	ctx, cancel := context.WithCancel(context.Background())
	managerStopped := make(chan struct{})
	go func() {
		defer close(managerStopped)
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("failed to start manager: %v", err)
		}
	}()

	// Return cleanup function
	cleanup := func() {
		cancel()
		<-managerStopped
		if err := testEnv.Stop(); err != nil {
			t.Errorf("failed to stop test environment: %v", err)
		}
	}

	return cfg, k8sClient, mgr, clientPool, cleanup
}

// createTestNamespace creates a test namespace
func createTestNamespace(t *testing.T, k8sClient client.Client) *corev1.Namespace {
	testNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-integration-" + time.Now().Format("20060102150405"),
		},
	}

	if err := k8sClient.Create(context.Background(), testNamespace); err != nil {
		t.Fatalf("failed to create test namespace: %v", err)
	}

	return testNamespace
}

// cleanupTestNamespace cleans up the test namespace
func cleanupTestNamespace(t *testing.T, cfg *rest.Config, k8sClient client.Client, testNamespace *corev1.Namespace) {
	if testNamespace != nil {
		if err := k8sClient.Delete(context.Background(), testNamespace); err != nil {
			t.Errorf("failed to delete test namespace: %v", err)
		}
	}
}

func setupUnversionedPollers(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	w, _, err := testhelpers.NewWorker(ctx, "", "", tc.GetTWD().Name, env.Ts.GetFrontendHostPort(), env.Ts.GetDefaultNamespace(), false)
	if err != nil {
		t.Errorf("failed to setup worker: %v", err)
	}

	// Register a dummy workflow and activity so the worker has something to poll for
	w.RegisterWorkflowWithOptions(func(ctx workflow.Context) (string, error) { return "hi", nil }, workflow.RegisterOptions{Name: "dummyWorkflow"})
	w.RegisterActivity(func(ctx context.Context) (string, error) { return "hi", nil })

	err = w.Start()
	t.Log("started unversioned worker")
	if err != nil {
		t.Errorf("error starting unversioned worker %v", err)
	}
	eventually(t, 5*time.Second, 500*time.Millisecond, func() error {
		unversionedWorkflowPoller, err := hasUnversionedPoller(ctx, env.Ts.GetDefaultClient(), temporalClient.WorkerDeploymentTaskQueueInfo{
			Name: tc.GetTWD().Name,
			Type: temporalClient.TaskQueueTypeWorkflow,
		})
		if err != nil {
			return fmt.Errorf("error checking unversioned Workflow pollers %v", err)
		}
		unversionedActivityPoller, err := hasUnversionedPoller(ctx, env.Ts.GetDefaultClient(), temporalClient.WorkerDeploymentTaskQueueInfo{
			Name: tc.GetTWD().Name,
			Type: temporalClient.TaskQueueTypeActivity,
		})
		if err != nil {
			return fmt.Errorf("error checking unversioned Activity pollers %v", err)
		}
		if !unversionedWorkflowPoller {
			return errors.New("no workflow poller")
		}
		if !unversionedActivityPoller {
			return errors.New("no activity poller")
		}
		return nil
	})
	t.Logf("confirmed that task queue %v has unversioned workflow and activity pollers", tc.GetTWD().Name)
}

func hasUnversionedPoller(ctx context.Context,
	client temporalClient.Client,
	taskQueueInfo temporalClient.WorkerDeploymentTaskQueueInfo,
) (bool, error) {
	pollers, err := getPollers(ctx, client, taskQueueInfo)
	if err != nil {
		return false, fmt.Errorf("unable to confirm presence of unversioned poller: %w", err)
	}
	for _, p := range pollers {
		switch p.GetDeploymentOptions().GetWorkerVersioningMode() {
		case temporalClient.WorkerVersioningModeUnversioned, temporalClient.WorkerVersioningModeUnspecified:
			return true, nil
		case temporalClient.WorkerVersioningModeVersioned:
		}
	}
	return false, nil
}

func getPollers(ctx context.Context,
	client temporalClient.Client,
	taskQueueInfo temporalClient.WorkerDeploymentTaskQueueInfo,
) ([]*taskqueue.PollerInfo, error) {
	var resp *workflowservice.DescribeTaskQueueResponse
	var err error
	switch taskQueueInfo.Type {
	case temporalClient.TaskQueueTypeWorkflow:
		resp, err = client.DescribeTaskQueue(ctx, taskQueueInfo.Name, temporalClient.TaskQueueTypeWorkflow)
	case temporalClient.TaskQueueTypeActivity:
		resp, err = client.DescribeTaskQueue(ctx, taskQueueInfo.Name, temporalClient.TaskQueueTypeActivity)
	}
	if err != nil {
		return nil, fmt.Errorf("unable to describe task queue %s: %w", taskQueueInfo.Name, err)
	}
	return resp.GetPollers(), nil
}

// setManagerIdentityToOther sets the Worker Deployment's ManagerIdentity to "some-other-cli-user"
// using a client with that identity. After this call, the controller (which has a different identity)
// will be blocked from making routing changes to this deployment.
func setManagerIdentityToOther(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(tc.GetTWD())
	c, err := temporalClient.Dial(temporalClient.Options{
		HostPort:  env.Ts.GetFrontendHostPort(),
		Namespace: env.Ts.GetDefaultNamespace(),
		Identity:  "some-other-cli-user",
	})
	if err != nil {
		t.Fatalf("failed to create temporal client with other identity: %v", err)
	}
	defer c.Close()

	deploymentHandle := c.WorkerDeploymentClient().GetHandle(workerDeploymentName)
	_, err = deploymentHandle.SetManagerIdentity(ctx, temporalClient.WorkerDeploymentSetManagerIdentityOptions{
		Self: true,
	})
	if err != nil {
		t.Errorf("error setting manager identity to other: %v", err)
	}
	t.Logf("set manager identity to 'some-other-cli-user'")
}

// setManagerIdentityBlockThenUnblock sets the Worker Deployment's ManagerIdentity to "some-other-cli-user"
// and then immediately clears it. This simulates a transient block: by the time the controller reconciles,
// the ManagerIdentity is empty and the controller can claim it normally.
func setManagerIdentityBlockThenUnblock(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(tc.GetTWD())
	c, err := temporalClient.Dial(temporalClient.Options{
		HostPort:  env.Ts.GetFrontendHostPort(),
		Namespace: env.Ts.GetDefaultNamespace(),
		Identity:  "some-other-cli-user",
	})
	if err != nil {
		t.Fatalf("failed to create temporal client with other identity: %v", err)
	}
	defer c.Close()

	deploymentHandle := c.WorkerDeploymentClient().GetHandle(workerDeploymentName)
	resp, err := deploymentHandle.SetManagerIdentity(ctx, temporalClient.WorkerDeploymentSetManagerIdentityOptions{
		Self: true,
	})
	if err != nil {
		t.Errorf("error setting manager identity to other: %v", err)
		return
	}
	t.Logf("set manager identity to 'some-other-cli-user'")

	_, err = deploymentHandle.SetManagerIdentity(ctx, temporalClient.WorkerDeploymentSetManagerIdentityOptions{
		ManagerIdentity: "", // clear
		ConflictToken:   resp.ConflictToken,
	})
	if err != nil {
		t.Errorf("error clearing manager identity: %v", err)
	}
	t.Logf("cleared manager identity")
}

// validateManagerIdentity checks that the Worker Deployment's ManagerIdentity matches expected.
func validateManagerIdentity(expected string) func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	return func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
		workerDeploymentName := k8s.ComputeWorkerDeploymentName(tc.GetTWD())
		deploymentHandle := env.Ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)

		desc, err := deploymentHandle.Describe(ctx, temporalClient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			t.Errorf("error describing worker deployment: %v", err)
			return
		}
		if desc.Info.ManagerIdentity != expected {
			t.Errorf("expected manager identity to be %q, got %q", expected, desc.Info.ManagerIdentity)
		}
	}
}
