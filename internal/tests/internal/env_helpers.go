//go:build test_dep
// +build test_dep

package internal

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"go.temporal.io/sdk/log"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

	// Try to find the assets using setup-envtest
	cmd := exec.Command("setup-envtest", "use", "1.28.0", "--bin-dir", "bin")
	output, err := cmd.Output()
	if err != nil {
		return err
	}

	// Parse the output to get the path
	// The output format is typically: "export KUBEBUILDER_ASSETS=/path/to/assets"
	// We need to extract just the path
	assetsPath := string(output)
	if len(assetsPath) > 0 {
		// Remove any trailing newlines
		assetsPath = assetsPath[:len(assetsPath)-1]
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
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		TemporalClientPool: clientPool,
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

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}

func applyDeployment(t *testing.T, ctx context.Context, k8sClient client.Client, deploymentName, namespace string) []func() {
	var deployment appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: namespace,
	}, &deployment); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}

	var wg sync.WaitGroup
	stopFuncs := make([]func(), *(deployment.Spec.Replicas))
	workerErrors := make([]error, *(deployment.Spec.Replicas))
	workerCallback := func(i int32) func(func(), error) {
		return func(stopFunc func(), err error) {
			if err == nil {
				stopFuncs[i] = stopFunc
				wg.Done()
			} else {
				workerErrors[i] = err
			}
		}
	}

	for i := int32(0); i < *(deployment.Spec.Replicas); i++ {
		wg.Add(1)
		go runHelloWorldWorker(ctx, deployment.Spec.Template, workerCallback(i))
	}

	// wait 10s for all expected workers to be healthy
	timedOut := waitTimeout(&wg, 10*time.Second)

	if timedOut {
		t.Fatalf("could not start workers, errors were: %+v", workerErrors)
	} else {
		setHealthyDeploymentStatus(t, ctx, k8sClient, deployment)
	}

	return stopFuncs
}

// Set deployment status to `DeploymentAvailable` to simulate a healthy deployment
// This is necessary because envtest doesn't actually start pods
func setHealthyDeploymentStatus(t *testing.T, ctx context.Context, k8sClient client.Client, deployment appsv1.Deployment) {
	now := metav1.Now()
	deployment.Status = appsv1.DeploymentStatus{
		Replicas:            *deployment.Spec.Replicas,
		UpdatedReplicas:     *deployment.Spec.Replicas,
		ReadyReplicas:       *deployment.Spec.Replicas,
		AvailableReplicas:   *deployment.Spec.Replicas,
		UnavailableReplicas: 0,
		Conditions: []appsv1.DeploymentCondition{
			{
				Type:               appsv1.DeploymentAvailable,
				Status:             corev1.ConditionTrue,
				LastUpdateTime:     now,
				LastTransitionTime: now,
				Reason:             "MinimumReplicasAvailable",
				Message:            "Deployment has minimum availability.",
			},
			{
				Type:               appsv1.DeploymentProgressing,
				Status:             corev1.ConditionTrue,
				LastUpdateTime:     now,
				LastTransitionTime: now,
				Reason:             "NewReplicaSetAvailable",
				Message:            "ReplicaSet is available.",
			},
		},
	}
	t.Logf("started %d healthy workers, updating deployment status", *deployment.Spec.Replicas)
	if err := k8sClient.Status().Update(ctx, &deployment); err != nil {
		t.Fatalf("failed to update deployment status: %v", err)
	}
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
