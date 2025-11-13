package internal

import (
	"context"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These helper functions are designed to work with the existing test infrastructure
// and provide compatibility-specific utilities

// WaitForDeployment waits for a deployment to be created
func WaitForDeployment(t *testing.T, k8sClient client.Client, deploymentName, namespace string, timeout time.Duration) {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var deployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      deploymentName,
			Namespace: namespace,
		}, &deployment); err == nil {
			t.Logf("Found deployment %s in namespace %s", deployment.Name, namespace)
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("failed to wait for deployment: timeout waiting for deployment %s in namespace %s", deploymentName, namespace)
}

// Eventually retries a check function until it succeeds or times out
func Eventually(t *testing.T, timeout, interval time.Duration, check func() error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := check(); err == nil {
			return // Success!
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	if lastErr != nil {
		t.Fatalf("eventually failed after %s: %v", timeout, lastErr)
	}
}

// VerifyBasicStatus performs basic status verification for compatibility testing
func VerifyBasicStatus(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	twdName, namespace string,
	expectedStatus *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	timeout, interval time.Duration,
) {
	Eventually(t, timeout, interval, func() error {
		var twd temporaliov1alpha1.TemporalWorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      twdName,
			Namespace: namespace,
		}, &twd); err != nil {
			return fmt.Errorf("failed to get updated worker deployment: %v", err)
		}

		// Validate target version
		if expectedStatus.TargetVersion.BuildID != "" {
			if twd.Status.TargetVersion.BuildID != expectedStatus.TargetVersion.BuildID {
				return fmt.Errorf("expected target build id to be '%s', got '%s'",
					expectedStatus.TargetVersion.BuildID,
					twd.Status.TargetVersion.BuildID)
			}
			if twd.Status.TargetVersion.Status != expectedStatus.TargetVersion.Status {
				return fmt.Errorf("expected target version status to be '%s', got '%s'",
					expectedStatus.TargetVersion.Status,
					twd.Status.TargetVersion.Status)
			}
		}

		// Validate current version if expected
		if expectedStatus.CurrentVersion != nil {
			if twd.Status.CurrentVersion == nil {
				return fmt.Errorf("expected CurrentVersion to be set")
			}
			if twd.Status.CurrentVersion.BuildID != expectedStatus.CurrentVersion.BuildID {
				return fmt.Errorf("expected current build id to be '%s', got '%s'",
					expectedStatus.CurrentVersion.BuildID,
					twd.Status.CurrentVersion.BuildID)
			}
		}

		return nil
	})
}

// CreateTemporalConnection creates a TemporalConnection resource
func CreateTemporalConnection(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	name, namespace string,
) *temporaliov1alpha1.TemporalConnection {
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: k8s.NewObjectMeta(name, namespace, nil),
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}
	return temporalConnection
}

// CleanupResource deletes a Kubernetes resource
func CleanupResource(ctx context.Context, t *testing.T, k8sClient client.Client, obj client.Object) {
	if err := k8sClient.Delete(ctx, obj); err != nil {
		t.Logf("Warning: failed to delete resource %s: %v", obj.GetName(), err)
	}
}

// GetServerVersion returns a simplified server version string for logging
func GetServerVersion() string {
	// This will be set differently in each version-specific test module
	return "unknown"
}

// LogCompatibilityTest logs compatibility test information
func LogCompatibilityTest(t *testing.T, controllerVersion, serverVersion, testName string) {
	t.Logf("=== Compatibility Test ===")
	t.Logf("Controller: %s", controllerVersion)
	t.Logf("Server: %s", serverVersion)
	t.Logf("Test: %s", testName)
	t.Logf("========================")
}

// SetupTestEnv creates a test environment for compatibility testing
func SetupTestEnv(
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	tc testhelpers.TestCase,
	connection *temporaliov1alpha1.TemporalConnection,
) testhelpers.TestEnv {
	return testhelpers.TestEnv{
		K8sClient:                  k8sClient,
		Ts:                         ts,
		Connection:                 connection,
		ExistingDeploymentReplicas: tc.GetExistingDeploymentReplicas(),
		ExistingDeploymentImages:   tc.GetExistingDeploymentImages(),
		ExpectedDeploymentReplicas: tc.GetExpectedDeploymentReplicas(),
	}
}

