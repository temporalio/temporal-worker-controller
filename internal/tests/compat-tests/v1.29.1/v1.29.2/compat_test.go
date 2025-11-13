package compat_v1_29_1

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

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	shared "github.com/temporalio/temporal-worker-controller/tests/compat/shared"
	"go.temporal.io/sdk/log"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
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
	ServerVersion                        = "v1.29.1"
	testDrainageVisibilityGracePeriod    = time.Second
	testDrainageRefreshInterval          = time.Second
	testMaxVersionsIneligibleForDeletion = 5
	testMaxVersionsInDeployment          = 6
)

// TestCompatibility runs compatibility tests against Temporal server v1.29.1
func TestCompatibility(t *testing.T) {
	shared.LogCompatibilityTest(t, "current", ServerVersion, "Core Compatibility Suite")

	// Set up test environment
	cfg, k8sClient, mgr, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Create Temporal server with v1.29.1
	dc := dynamicconfig.NewMemoryClient()
	dc.OverrideValue("matching.wv.VersionDrainageStatusVisibilityGracePeriod", testDrainageVisibilityGracePeriod)
	dc.OverrideValue("matching.wv.VersionDrainageStatusRefreshInterval", testDrainageRefreshInterval)
	dc.OverrideValue("matching.maxVersionsInDeployment", testMaxVersionsInDeployment)
	ts := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dc)),
	)

	// Run the core compatibility test suite
	suite := shared.GetCoreCompatibilityTestSuite()
	shared.RunCompatibilityTests(t, k8sClient, mgr, ts, testNamespace.Name, suite)
}

func setupKubebuilderAssets() error {
	if os.Getenv("KUBEBUILDER_ASSETS") != "" {
		return nil
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("failed to get current file path")
	}
	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "../../../../.."))
	if err != nil {
		return fmt.Errorf("failed to get repository root: %v", err)
	}

	setupEnvtestPath := filepath.Join(repoRoot, "bin", "setup-envtest")
	binDir := filepath.Join(repoRoot, "bin")
	cmd := exec.Command(setupEnvtestPath, "use", "1.27.1", "--bin-dir", binDir, "-p", "path")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to run setup-envtest: %v", err)
	}

	assetsPath := strings.TrimSpace(string(output))
	if len(assetsPath) > 0 {
		os.Setenv("KUBEBUILDER_ASSETS", assetsPath)
	}

	return nil
}

func getRepoRoot(t *testing.T) string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("failed to get current file path")
	}

	repoRoot, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "../../../../.."))
	if err != nil {
		t.Fatalf("failed to get repository root: %v", err)
	}
	return repoRoot
}

func setupTestEnvironment(t *testing.T) (*rest.Config, client.Client, manager.Manager, *clientpool.ClientPool, func()) {
	t.Setenv("RECONCILE_INTERVAL", "1s")
	if kubeAssets := os.Getenv("KUBEBUILDER_ASSETS"); kubeAssets == "" {
		t.Skip("Skipping because KUBEBUILDER_ASSETS not set")
	}

	t.Setenv(controller.ControllerMaxDeploymentVersionsIneligibleForDeletionEnvKey, fmt.Sprintf("%d", testMaxVersionsIneligibleForDeletion))

	if err := setupKubebuilderAssets(); err != nil {
		t.Logf("Warning: Could not setup kubebuilder assets automatically: %v", err)
		t.Logf("You may need to run 'make envtest' first or set KUBEBUILDER_ASSETS manually")
	}

	logf.SetLogger(zap.New(zap.WriteTo(os.Stdout), zap.UseDevMode(true)))

	t.Log("bootstrapping test environment for v1.29.1 compatibility tests")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join(getRepoRoot(t), "helm", "temporal-worker-controller", "crds"),
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

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	clientPool := clientpool.New(log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   false,
		Level:       nil,
		ReplaceAttr: nil,
	}))), k8sClient)

	reconciler := &controller.TemporalWorkerDeploymentReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		TemporalClientPool:  clientPool,
		DisableRecoverPanic: true,
		MaxDeploymentVersionsIneligibleForDeletion: controller.GetControllerMaxDeploymentVersionsIneligibleForDeletion(),
	}
	err = reconciler.SetupWithManager(mgr)
	if err != nil {
		t.Fatalf("failed to set up controller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("failed to start manager: %v", err)
		}
	}()

	cleanup := func() {
		cancel()
		if err := testEnv.Stop(); err != nil {
			t.Errorf("failed to stop test environment: %v", err)
		}
	}

	return cfg, k8sClient, mgr, clientPool, cleanup
}

func createTestNamespace(t *testing.T, k8sClient client.Client) *corev1.Namespace {
	testNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "compat-v1-29-1-" + time.Now().Format("20060102150405"),
		},
	}

	if err := k8sClient.Create(context.Background(), testNamespace); err != nil {
		t.Fatalf("failed to create test namespace: %v", err)
	}

	return testNamespace
}

func cleanupTestNamespace(t *testing.T, cfg *rest.Config, k8sClient client.Client, testNamespace *corev1.Namespace) {
	if testNamespace != nil {
		if err := k8sClient.Delete(context.Background(), testNamespace); err != nil {
			t.Errorf("failed to delete test namespace: %v", err)
		}
	}
}
