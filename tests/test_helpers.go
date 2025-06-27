package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// waitForDeployment waits for a deployment to be created
func waitForDeployment(t *testing.T, k8sClient client.Client, deploymentName, namespace string, timeout time.Duration) {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var deployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      deploymentName,
			Namespace: namespace,
		}, &deployment); err == nil {
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("failed to wait for deployment: timeout waiting for deployment %s in namespace %s", deploymentName, namespace)
}

func verifyDeployment(t *testing.T, ctx context.Context, k8sClient client.Client, deploymentName, namespace, taskQueue string) {
	t.Log("Verifying the deployment was created with correct labels")
	var deployment appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: namespace,
	}, &deployment); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}

	if deployment.Labels["app"] != "test-worker" {
		t.Errorf("expected deployment label 'app' to be 'test-worker', got '%s'", deployment.Labels["app"])
	}

	if *deployment.Spec.Replicas != int32(1) {
		t.Errorf("expected deployment replicas to be 1, got %d", *deployment.Spec.Replicas)
	}

	for _, c := range deployment.Spec.Template.Spec.Containers {
		found := false
		for _, e := range c.Env {
			if e.Name == "TEMPORAL_TASK_QUEUE" {
				found = true
				if e.Value != taskQueue {
					t.Errorf("expected deployment to have `TEMPORAL_TASK_QUEUE=%s` in pod spec but was %s", taskQueue, e.Value)
				}
			}
		}
		if !found {
			t.Errorf("expected deployment to have `TEMPORAL_TASK_QUEUE` in pod spec but did not find it")
		}
	}
}

func verifyTemporalWorkerDeploymentStatusEventually(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	twdName,
	namespace string,
	expectedDeploymentStatus *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	timeout time.Duration,
	interval time.Duration,
) {
	eventually(t, timeout, interval, func() error {
		var twd temporaliov1alpha1.TemporalWorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      twdName,
			Namespace: namespace,
		}, &twd); err != nil {
			return fmt.Errorf("failed to get updated worker deployment: %v", err)
		}
		if twd.Status.CurrentVersion == nil {
			return fmt.Errorf("expected CurrentVersion to be set")
		}
		if twd.Status.CurrentVersion.Deployment == nil {
			return fmt.Errorf("expected CurrentVersion.Deployment to be set")
		}
		if twd.Status.CurrentVersion.VersionID != expectedDeploymentStatus.CurrentVersion.VersionID {
			return fmt.Errorf("expected current version id to be '%s', got '%s'",
				expectedDeploymentStatus.CurrentVersion.VersionID,
				twd.Status.CurrentVersion.VersionID)
		}
		if twd.Status.CurrentVersion.Deployment.Name != expectedDeploymentStatus.CurrentVersion.Deployment.Name {
			return fmt.Errorf("expected deployment name to be '%s', got '%s'",
				expectedDeploymentStatus.CurrentVersion.Deployment.Name,
				twd.Status.CurrentVersion.Deployment.Name)
		}
		return nil // All assertions passed!
	})
}

func eventually(t *testing.T, timeout, interval time.Duration, check func() error) {
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
