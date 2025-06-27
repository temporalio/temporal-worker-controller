// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package tests

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestIntegration runs integration tests for the Temporal Worker Controller
func TestIntegration(t *testing.T) {
	// Set up test environment
	cfg, k8sClient, _, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Run the actual test
	testTemporalWorkerDeploymentCreation(t, cfg, k8sClient, testNamespace.Name, "v1")
}

// testTemporalWorkerDeploymentCreation tests the creation of a TemporalWorkerDeployment
func testTemporalWorkerDeploymentCreation(t *testing.T, cfg *rest.Config, k8sClient client.Client, namespace string, workerImage string) {
	ctx := context.Background()

	t.Log("Creating a TemporalConnection")
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-connection",
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: "0.0.0.0:7233", // Temporal dev server
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}

	t.Log("Creating a TemporalWorkerDeployment")
	replicas := int32(1)
	taskQueue := "hello_world"
	twd := &temporaliov1alpha1.TemporalWorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-worker",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test-worker"},
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "test-worker",
				},
			},
			Template: temporaliov1alpha1.MakePodSpec(
				[]corev1.Container{{Name: "worker", Image: workerImage}},
				map[string]string{"app": "test-worker"},
				taskQueue,
			),
			RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: "AllAtOnce",
			},
			SunsetStrategy: temporaliov1alpha1.SunsetStrategy{},
			WorkerOptions: temporaliov1alpha1.WorkerOptions{
				TemporalConnection: "test-connection",
				TemporalNamespace:  "default",
			},
		},
	}
	for _, c := range twd.Spec.Template.Spec.Containers {
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

	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TemporalWorkerDeployment: %v", err)
	}

	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))

	t.Log("Waiting for the controller to reconcile")
	waitForDeployment(t, k8sClient, expectedDeploymentName, twd.Namespace, 30*time.Second)
	verifyDeployment(t, ctx, k8sClient, expectedDeploymentName, twd.Namespace, taskQueue)
	applyDeployment(t, ctx, k8sClient, expectedDeploymentName, twd.Namespace)

	expectedStatus := &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		TargetVersion: nil,
		CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
				VersionID: k8s.ComputeVersionID(twd),
				Deployment: &corev1.ObjectReference{
					Namespace: twd.Namespace,
					Name:      expectedDeploymentName,
				},
			},
		},
		RampingVersion:       nil,
		DeprecatedVersions:   nil,
		VersionConflictToken: nil,
		LastModifierIdentity: "",
	}
	verifyTemporalWorkerDeploymentStatusEventually(t, ctx, k8sClient, twd.Name, twd.Namespace, expectedStatus, 60*time.Second, 10*time.Second)
}
