// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package tests

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DataDog/temporal-worker-controller/internal/controller"
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

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/controller/clientpool"
)

// TestIntegration runs integration tests for the Temporal Worker Controller
func TestIntegration(t *testing.T) {
	// Set up test environment
	_, k8sClient, _, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, k8sClient, testNamespace)

	// Run the actual test
	testTemporalWorkerDeploymentCreation(t, k8sClient, testNamespace.Name)
}

// setupTestEnvironment sets up the test environment with envtest
func setupTestEnvironment(t *testing.T) (*rest.Config, client.Client, manager.Manager, *clientpool.ClientPool, func()) {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stdout), zap.UseDevMode(true)))

	t.Log("bootstrapping test environment")
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "helm", "temporal-worker-controller", "templates", "crds"),
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

	// Setup controller
	reconciler := &controller.TemporalWorkerDeploymentReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		TemporalClientPool: clientPool,
	}

	err = reconciler.SetupWithManager(mgr)
	if err != nil {
		t.Fatalf("failed to setup controller: %v", err)
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
func cleanupTestNamespace(t *testing.T, k8sClient client.Client, testNamespace *corev1.Namespace) {
	if testNamespace != nil {
		if err := k8sClient.Delete(context.Background(), testNamespace); err != nil {
			t.Errorf("failed to delete test namespace: %v", err)
		}
	}
}

// testTemporalWorkerDeploymentCreation tests the creation of a TemporalWorkerDeployment
func testTemporalWorkerDeploymentCreation(t *testing.T, k8sClient client.Client, namespace string) {
	ctx := context.Background()

	t.Log("Creating a TemporalConnection")
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-connection",
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: "localhost:7233", // Temporal dev server
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}

	t.Log("Creating a TemporalWorkerDeployment")
	replicas := int32(1)
	workerDeployment := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "test-worker",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "test-worker",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: "busybox:latest",
							Command: []string{
								"sleep",
								"3600",
							},
						},
					},
				},
			},
			RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: "AllAtOnce",
			},
			SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
				ScaledownDelay: &metav1.Duration{Duration: time.Hour},
				DeleteDelay:    &metav1.Duration{Duration: 24 * time.Hour},
			},
			WorkerOptions: temporaliov1alpha1.WorkerOptions{
				TemporalConnection: "test-connection",
				TemporalNamespace:  "default",
			},
		},
	}
	if err := k8sClient.Create(ctx, workerDeployment); err != nil {
		t.Fatalf("failed to create TemporalWorkerDeployment: %v", err)
	}

	t.Log("Waiting for the controller to reconcile")
	// Wait for the controller to process the resource
	if err := waitForDeployment(t, k8sClient, workerDeployment.Name, workerDeployment.Namespace, 30*time.Second); err != nil {
		t.Fatalf("failed to wait for deployment: %v", err)
	}

	t.Log("Verifying the deployment was created with correct labels")
	var deployment appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      workerDeployment.Name,
		Namespace: workerDeployment.Namespace,
	}, &deployment); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}

	if deployment.Labels["app"] != "test-worker" {
		t.Errorf("expected deployment label 'app' to be 'test-worker', got '%s'", deployment.Labels["app"])
	}

	if *deployment.Spec.Replicas != int32(1) {
		t.Errorf("expected deployment replicas to be 1, got %d", *deployment.Spec.Replicas)
	}

	t.Log("Verifying the worker deployment status is updated")
	if err := waitForStatusUpdate(t, k8sClient, workerDeployment.Name, workerDeployment.Namespace, 30*time.Second); err != nil {
		t.Fatalf("failed to wait for status update: %v", err)
	}

	t.Log("Verifying the status contains expected information")
	var updatedWorker temporaliov1alpha1.TemporalWorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      workerDeployment.Name,
		Namespace: workerDeployment.Namespace,
	}, &updatedWorker); err != nil {
		t.Fatalf("failed to get updated worker deployment: %v", err)
	}

	if updatedWorker.Status.CurrentVersion == nil {
		t.Error("expected CurrentVersion to be set")
	}

	if updatedWorker.Status.CurrentVersion.Deployment == nil {
		t.Error("expected CurrentVersion.Deployment to be set")
	}

	if updatedWorker.Status.CurrentVersion.Deployment.Name != workerDeployment.Name {
		t.Errorf("expected deployment name to be '%s', got '%s'", workerDeployment.Name, updatedWorker.Status.CurrentVersion.Deployment.Name)
	}
}

// waitForDeployment waits for a deployment to be created
func waitForDeployment(t *testing.T, k8sClient client.Client, name, namespace string, timeout time.Duration) error {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var deployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, &deployment); err == nil {
			return nil
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for deployment %s in namespace %s", name, namespace)
}

// waitForStatusUpdate waits for the worker deployment status to be updated
func waitForStatusUpdate(t *testing.T, k8sClient client.Client, name, namespace string, timeout time.Duration) error {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var updatedWorker temporaliov1alpha1.TemporalWorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, &updatedWorker); err == nil {
			if updatedWorker.Status.CurrentVersion != nil {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for status update for %s in namespace %s", name, namespace)
}
