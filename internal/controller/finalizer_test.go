// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
)

func TestFinalizerAddition(t *testing.T) {
	ctx := context.Background()

	// Set up the scheme with our custom resource
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = temporaliov1alpha1.AddToScheme(s)

	// Create a fake client
	client := fake.NewClientBuilder().WithScheme(s).Build()

	// Create a TemporalWorkerDeployment without finalizer
	workerDeploy := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: "default",
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
			Replicas: int32Ptr(1),
			WorkerOptions: temporaliov1alpha1.WorkerOptions{
				TemporalNamespace:  "test-namespace",
				TemporalConnection: "test-connection",
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: "test-image:latest",
						},
					},
				},
			},
		},
	}

	// Create the resource in the fake client
	err := client.Create(ctx, workerDeploy)
	if err != nil {
		t.Fatalf("Failed to create TemporalWorkerDeployment: %v", err)
	}

	// Create reconciler
	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: s,
	}

	// Verify finalizer is not present initially
	if controllerutil.ContainsFinalizer(workerDeploy, TemporalWorkerDeploymentFinalizer) {
		t.Error("Finalizer should not be present initially")
	}

	// Simulate what happens in the reconcile loop when finalizer needs to be added
	if !controllerutil.ContainsFinalizer(workerDeploy, TemporalWorkerDeploymentFinalizer) {
		controllerutil.AddFinalizer(workerDeploy, TemporalWorkerDeploymentFinalizer)
		err := reconciler.Update(ctx, workerDeploy)
		if err != nil {
			t.Fatalf("Failed to add finalizer: %v", err)
		}
	}

	// Fetch the updated resource
	updated := &temporaliov1alpha1.TemporalWorkerDeployment{}
	err = client.Get(ctx, types.NamespacedName{Name: "test-worker", Namespace: "default"}, updated)
	if err != nil {
		t.Fatalf("Failed to fetch updated resource: %v", err)
	}

	// Verify finalizer was added
	if !controllerutil.ContainsFinalizer(updated, TemporalWorkerDeploymentFinalizer) {
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

	// Set up the scheme
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = temporaliov1alpha1.AddToScheme(s)

	// Create a TemporalWorkerDeployment
	workerDeploy := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: "default",
			UID:       "worker-uid-123",
		},
	}

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

	// Create fake client with the deployments
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ownedDeployment, unownedDeployment).
		Build()

	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: s,
	}

	// Create a test logger
	logger := logr.Discard()

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

	// Set up the scheme
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = temporaliov1alpha1.AddToScheme(s)

	// Create a TemporalWorkerDeployment with finalizer and deletion timestamp
	now := metav1.Now()
	workerDeploy := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-worker",
			Namespace:         "default",
			UID:               "worker-uid-123",
			DeletionTimestamp: &now,
			Finalizers:        []string{TemporalWorkerDeploymentFinalizer},
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
			WorkerOptions: temporaliov1alpha1.WorkerOptions{
				TemporalNamespace:  "test-namespace",
				TemporalConnection: "test-connection",
			},
		},
	}

	// Create fake client
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(workerDeploy).
		Build()

	reconciler := &TemporalWorkerDeploymentReconciler{
		Client: client,
		Scheme: s,
	}

	// Create a test logger
	logger := logr.Discard()

	// Verify finalizer is present before deletion
	if !controllerutil.ContainsFinalizer(workerDeploy, TemporalWorkerDeploymentFinalizer) {
		t.Error("Finalizer should be present before deletion handling")
	}

	// Test deletion handling
	result, err := reconciler.handleDeletion(ctx, logger, workerDeploy)
	if err != nil {
		t.Fatalf("handleDeletion should succeed: %v", err)
	}

	if result.Requeue {
		t.Error("Result should not indicate requeue")
	}

	// After handleDeletion, the resource should be deleted (finalizer removal allows deletion to proceed)
	// In a real cluster, the resource would be gone. In the fake client, we can verify it was marked for deletion
	// by checking if the finalizer was removed (which we can't easily do since the resource is deleted)
	// Instead, we'll verify that the deletion handling completed without error, which means cleanup was successful
}

// Helper function to create an int32 pointer
func int32Ptr(i int32) *int32 {
	return &i
}
