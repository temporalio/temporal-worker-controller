package internal

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	"go.temporal.io/sdk/worker"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	temporalClient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
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

// setupKubebuilderAssets sets up the KUBEBUILDER_ASSETS environment variable if not already set
func setupKubebuilderAssets() error {
	if os.Getenv("KUBEBUILDER_ASSETS") != "" {
		return nil // Already set
	}

	// Get the repository root to find the setup-envtest binary
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("failed to get current file path")
	}
	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "../../.."))
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

	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "../../.."))
	if err != nil {
		t.Fatalf("failed to get repository root: %v", err)
	}
	return repoRoot
}

// setupTestEnvironment sets up the test environment with envtest
func setupTestEnvironment(t *testing.T) (*rest.Config, client.Client, manager.Manager, *clientpool.ClientPool, func()) {
	// Set faster reconcile interval for testing
	t.Setenv("RECONCILE_INTERVAL", "1s")
	if kubeAssets := os.Getenv("KUBEBUILDER_ASSETS"); kubeAssets == "" {
		t.Skip("Skipping because KUBEBUILDER_ASSETS not set")
	}

	// Setup kubebuilder assets for IDE testing
	if err := setupKubebuilderAssets(); err != nil {
		t.Logf("Warning: Could not setup kubebuilder assets automatically: %v", err)
		t.Logf("You may need to run 'make envtest' first or set KUBEBUILDER_ASSETS manually")
	}

	logf.SetLogger(zap.New(zap.WriteTo(os.Stdout), zap.UseDevMode(true)))

	t.Log("bootstrapping test environment")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(getRepoRoot(t), "helm", "temporal-worker-controller", "templates", "crds"),
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
	}))), k8sClient)

	// Set up controller
	reconciler := &controller.TemporalWorkerDeploymentReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		TemporalClientPool:  clientPool,
		DisableRecoverPanic: true,
	}
	err = reconciler.SetupWithManager(mgr)
	if err != nil {
		t.Fatalf("failed to set up controller: %v", err)
	}

	// Start manager
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("failed to start manager: %v", err)
		}
	}()

	// Return cleanup function
	cleanup := func() {
		cancel()
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

func setupUnversionedPoller(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	w, _, err := testhelpers.NewWorker(ctx, "", "", tc.GetTWD().Name, env.Ts.GetFrontendHostPort(), env.Ts.GetDefaultNamespace(), false)

	// Register a dummy workflow and activity so the worker has something to poll for
	w.RegisterWorkflowWithOptions(func(ctx workflow.Context) (string, error) { return "hi", nil }, workflow.RegisterOptions{Name: "dummyWorkflow"})
	w.RegisterActivity(func(ctx context.Context) (string, error) { return "hi", nil })

	err = w.Start()
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
			return fmt.Errorf("no workflow poller")
		}
		if !unversionedActivityPoller {
			return fmt.Errorf("no activity poller")
		}
		return nil
	})
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

func setUnversionedCurrent(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(tc.GetTWD())
	deploymentHandle := env.Ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)

	_, err := deploymentHandle.SetCurrentVersion(ctx, temporalClient.WorkerDeploymentSetCurrentVersionOptions{
		BuildID:                 "",
		IgnoreMissingTaskQueues: true,
	})
	if err != nil {
		t.Errorf("error setting unversioned current version to spook controller into manual mode: %v", err)
	}
	t.Logf("set current version to unversioned with non-controller identity")

}

func setCurrentAndSetIgnoreModifierMetadata(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(tc.GetTWD())
	deploymentHandle := env.Ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)

	// change current version arbitrarily so we can be the last modifier
	resp, err := deploymentHandle.SetCurrentVersion(ctx, temporalClient.WorkerDeploymentSetCurrentVersionOptions{
		BuildID:                 "",
		IgnoreMissingTaskQueues: true,
	})
	if err != nil {
		t.Errorf("error setting unversioned current version to spook controller into manual mode: %v", err)
	}
	t.Logf("set current version to unversioned with non-controller identity")

	// set it back to what it was so that it's non-nil
	_, err = deploymentHandle.SetCurrentVersion(ctx, temporalClient.WorkerDeploymentSetCurrentVersionOptions{
		BuildID: resp.PreviousVersion.BuildId,
	})
	if err != nil {
		t.Errorf("error restoring current version: %v", err)
	}
	t.Logf("set current version to build %v with non-controller identity", resp.PreviousVersion.BuildId)

	// set the IgnoreLastModifier metadata
	_, err = deploymentHandle.UpdateVersionMetadata(ctx, temporalClient.WorkerDeploymentUpdateVersionMetadataOptions{
		Version: worker.WorkerDeploymentVersion{
			DeploymentName: workerDeploymentName,
			BuildId:        resp.PreviousVersion.BuildId,
		},
		MetadataUpdate: temporalClient.WorkerDeploymentMetadataUpdate{
			UpsertEntries: map[string]interface{}{
				temporal.IgnoreLastModifierKey: "true",
			},
		},
	})
	if err != nil {
		t.Errorf("error updating version metadata: %v", err)
	}
	t.Log("set current version's metadata to have \"temporal.io/ignore-last-modifier\"=\"true\"")
}
