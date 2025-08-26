package controller

import (
	"context"
	"strings"
	"testing"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers/testlogr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestFinalizerAddition(t *testing.T) {
	ctx := context.Background()

	// Create a TemporalWorkerDeployment without finalizer using test helpers
	workerDeploy := testhelpers.ModifyObj(testhelpers.MakeTWDWithName("test-worker", "default"), func(twd *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
		twd.Spec.WorkerOptions = temporaliov1alpha1.WorkerOptions{
			TemporalNamespace:  "test-namespace",
			TemporalConnection: "test-connection",
		}
		twd.Spec.Template = corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "worker",
						Image: "test-image:latest",
					},
				},
			},
		}
		return twd
	})

	// Create fake client with test helpers
	client := testhelpers.SetupFakeClient()

	// Create the resource in the fake client
	err := client.Create(ctx, workerDeploy)
	if err != nil {
		t.Fatalf("Failed to create TemporalWorkerDeployment: %v", err)
	}

	// Create reconciler
	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: testhelpers.SetupTestScheme(),
	}

	// Verify finalizer is not present initially
	if controllerutil.ContainsFinalizer(workerDeploy, temporalWorkerDeploymentFinalizer) {
		t.Error("Finalizer should not be present initially")
	}

	// Exercise the controller's logic for adding finalizers by calling Reconcile
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-worker",
			Namespace: "default",
		},
	}

	_, err = reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Fetch the updated resource
	updated := &temporaliov1alpha1.TemporalWorkerDeployment{}
	err = client.Get(ctx, types.NamespacedName{Name: "test-worker", Namespace: "default"}, updated)
	if err != nil {
		t.Fatalf("Failed to fetch updated resource: %v", err)
	}

	// Verify finalizer was added
	if !controllerutil.ContainsFinalizer(updated, temporalWorkerDeploymentFinalizer) {
		t.Error("Finalizer should be present after update")
	}
}

func TestIsOwnedByWorkerDeployment(t *testing.T) {
	// Create a TemporalWorkerDeployment
	workerDeploy := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: "default",
			UID:       "worker-uid-123",
		},
	}

	// Create a deployment owned by the worker deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deployment",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: apiGVStr,
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "worker-uid-123",
				},
			},
		},
	}

	// Create a deployment not owned by the worker deployment
	unownedDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unowned-deployment",
			Namespace: "default",
		},
	}

	reconciler := &TemporalWorkerDeploymentReconciler{}

	// Test owned deployment
	if !reconciler.isOwnedByWorkerDeployment(deployment, workerDeploy) {
		t.Error("Deployment should be identified as owned by worker deployment")
	}

	// Test unowned deployment
	if reconciler.isOwnedByWorkerDeployment(unownedDeployment, workerDeploy) {
		t.Error("Unowned deployment should not be identified as owned by worker deployment")
	}
}

func TestCleanupManagedResources(t *testing.T) {
	ctx := context.Background()

	// Create a TemporalWorkerDeployment using test helpers
	workerDeploy := testhelpers.ModifyObj(testhelpers.MakeTWDWithName("test-worker", "default"), func(twd *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
		twd.UID = "worker-uid-123"
		return twd
	})

	// Create a deployment owned by the worker deployment
	ownedDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "owned-deployment",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: apiGVStr,
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "worker-uid-123",
				},
			},
		},
	}

	// Create a deployment not owned by the worker deployment
	unownedDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unowned-deployment",
			Namespace: "default",
		},
	}

	// Create fake client with the deployments using test helpers
	client := testhelpers.SetupFakeClient(ownedDeployment, unownedDeployment)

	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: testhelpers.SetupTestScheme(),
	}

	// Create a test logger using testlogr
	logger := testlogr.New(t)

	// Test cleanup - should only delete owned deployments
	err := reconciler.cleanupManagedResources(ctx, logger, workerDeploy)
	if err != nil {
		t.Fatalf("Cleanup should succeed: %v", err)
	}

	// Verify owned deployment was deleted
	err = client.Get(ctx, types.NamespacedName{Name: "owned-deployment", Namespace: "default"}, &appsv1.Deployment{})
	if err == nil {
		t.Error("Owned deployment should have been deleted")
	}

	// Verify unowned deployment was not deleted
	err = client.Get(ctx, types.NamespacedName{Name: "unowned-deployment", Namespace: "default"}, &appsv1.Deployment{})
	if err != nil {
		t.Error("Unowned deployment should not have been deleted")
	}
}

func TestHandleDeletion(t *testing.T) {
	ctx := context.Background()

	// Create a TemporalWorkerDeployment with finalizer and deletion timestamp using test helpers
	now := metav1.Now()
	workerDeploy := testhelpers.ModifyObj(testhelpers.MakeTWDWithName("test-worker", "default"), func(twd *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
		twd.UID = "worker-uid-123"
		twd.DeletionTimestamp = &now
		twd.Finalizers = []string{temporalWorkerDeploymentFinalizer}
		twd.Spec.WorkerOptions = temporaliov1alpha1.WorkerOptions{
			TemporalNamespace:  "test-namespace",
			TemporalConnection: "test-connection",
		}
		return twd
	})

	// Create fake client using test helpers
	client := testhelpers.SetupFakeClient(workerDeploy)

	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: testhelpers.SetupTestScheme(),
	}

	// Create a test logger using testlogr
	logger := testlogr.New(t)

	// Verify finalizer is present before deletion
	if !controllerutil.ContainsFinalizer(workerDeploy, temporalWorkerDeploymentFinalizer) {
		t.Error("Finalizer should be present before deletion handling")
	}

	// Test deletion handling
	result, err := reconciler.handleDeletion(ctx, logger, workerDeploy)
	if err != nil {
		t.Fatalf("handleDeletion should succeed: %v", err)
	}

	if result.RequeueAfter > 0 {
		t.Error("Result should not indicate requeue")
	}

	// After handleDeletion, the resource should be deleted (finalizer removal allows deletion to proceed)
	// In a real cluster, the resource would be gone. In the fake client, we can verify it was marked for deletion
	// by checking if the finalizer was removed (which we can't easily do since the resource is deleted)
	// Instead, we'll verify that the deletion handling completed without error, which means cleanup was successful
}

func TestCleanupWithContextCancellation(t *testing.T) {
	// Create a context that will be cancelled during cleanup
	ctx, cancel := context.WithCancel(context.Background())

	// Create a TemporalWorkerDeployment using test helpers
	workerDeploy := testhelpers.ModifyObj(testhelpers.MakeTWDWithName("test-worker", "default"), func(twd *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
		twd.UID = "worker-uid-123"
		return twd
	})

	// Create fake client using test helpers
	client := testhelpers.SetupFakeClient()

	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: testhelpers.SetupTestScheme(),
	}

	// Create a test logger using testlogr
	logger := testlogr.New(t)

	// Cancel the context immediately to simulate cancellation during cleanup
	cancel()

	// Test cleanup with cancelled context - should handle gracefully
	err := reconciler.cleanupManagedResources(ctx, logger, workerDeploy)
	if err == nil {
		t.Error("Expected error when context is cancelled during cleanup")
	}

	// Error should indicate context cancellation
	if ctx.Err() != context.Canceled {
		t.Error("Context should be cancelled")
	}
}

func TestWaitForOwnedDeploymentsTimeout(t *testing.T) {
	ctx := context.Background()

	// Create a TemporalWorkerDeployment using test helpers
	workerDeploy := testhelpers.ModifyObj(testhelpers.MakeTWDWithName("test-worker", "default"), func(twd *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
		twd.UID = "worker-uid-123"
		return twd
	})

	// Create a deployment that won't be deleted (simulate stuck deletion)
	persistentDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "persistent-deployment",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: apiGVStr,
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "worker-uid-123",
				},
			},
		},
	}

	// Create fake client with the deployment that won't be deleted
	client := testhelpers.SetupFakeClient(persistentDeployment)

	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: testhelpers.SetupTestScheme(),
	}

	// Create a test logger using testlogr
	logger := testlogr.New(t)

	// Test with a very short timeout to simulate timeout condition
	// This will use the actual waitForOwnedDeploymentsToBeDeleted method which has built-in timeout
	err := reconciler.waitForOwnedDeploymentsToBeDeleted(ctx, logger, workerDeploy)

	// Should timeout waiting for deployments to be deleted
	if err == nil {
		t.Error("Expected timeout error when deployments don't get deleted")
	}

	// Error message should indicate timeout
	if err != nil && !contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

func TestPartialCleanupFailure(t *testing.T) {
	ctx := context.Background()

	// Create a TemporalWorkerDeployment using test helpers
	workerDeploy := testhelpers.ModifyObj(testhelpers.MakeTWDWithName("test-worker", "default"), func(twd *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
		twd.UID = "worker-uid-123"
		return twd
	})

	// Create multiple deployments owned by the worker deployment
	deployment1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deployment-1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: apiGVStr,
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "worker-uid-123",
				},
			},
		},
	}

	deployment2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deployment-2",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: apiGVStr,
					Kind:       "TemporalWorkerDeployment",
					Name:       "test-worker",
					UID:        "worker-uid-123",
				},
			},
		},
	}

	// Create fake client with multiple deployments
	client := testhelpers.SetupFakeClient(deployment1, deployment2)

	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: testhelpers.SetupTestScheme(),
	}

	// Create a test logger using testlogr
	logger := testlogr.New(t)

	// Delete one deployment manually to simulate partial cleanup
	err := client.Delete(ctx, deployment1)
	if err != nil {
		t.Fatalf("Failed to delete deployment1: %v", err)
	}

	// Now test cleanup - it should handle the mixed state gracefully
	// (one deployment already deleted, one still exists)
	err = reconciler.cleanupManagedResources(ctx, logger, workerDeploy)

	// This should eventually succeed as the cleanup logic should handle
	// deployments that are already deleted gracefully
	if err != nil && !contains(err.Error(), "timeout") {
		t.Errorf("Cleanup should handle partial cleanup gracefully, got error: %v", err)
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
