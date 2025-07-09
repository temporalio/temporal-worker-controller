// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package tests

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/temporaltest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type testCase struct {
	// If starting from a particular state, specify that in input.Status
	input *temporaliov1alpha1.TemporalWorkerDeployment
	// TemporalWorkerDeploymentStatus only tracks the names of the Deployments for deprecated
	// versions, so for test scenarios that start with existing deprecated version Deployments,
	// specify the number of replicas for each deprecated build here.
	deprecatedBuildReplicas map[string]int32
	expectedStatus          *temporaliov1alpha1.TemporalWorkerDeploymentStatus
}

// TestIntegration runs integration tests for the Temporal Worker Controller
func TestIntegration(t *testing.T) {
	// Set up test environment
	cfg, k8sClient, _, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Create test Temporal server and client
	ts := temporaltest.NewServer(temporaltest.WithT(t))

	testRampPercentStep1 := float32(5)

	// params:

	// example:
	// - can we create a drained version? validate that it's scaled down
	// - pollerTTL=0

	tests := map[string]testCase{
		"all-at-once-rollout-2-replicas": {
			input: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("all-at-once-rollout-2-replicas"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateAllAtOnce
				obj.Spec.Template = testhelpers.MakeHelloWorldPodSpec("v1")
				replicas := int32(2)
				obj.Spec.Replicas = &replicas
				obj.Spec.WorkerOptions = temporaliov1alpha1.WorkerOptions{
					TemporalConnection: "all-at-once-rollout-2-replicas",
					TemporalNamespace:  ts.GetDefaultNamespace(),
				}
				obj.ObjectMeta = metav1.ObjectMeta{
					Name:      "all-at-once-rollout-2-replicas",
					Namespace: testNamespace.Name,
					Labels:    map[string]string{"app": "test-worker"},
				}
				return obj
			}),
			deprecatedBuildReplicas: nil,
			expectedStatus: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: nil,
				CurrentVersion: &temporaliov1alpha1.CurrentWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: testhelpers.MakeVersionId(testNamespace.Name, "all-at-once-rollout-2-replicas", "v1"),
						Deployment: &corev1.ObjectReference{
							Namespace: testNamespace.Name,
							Name: k8s.ComputeVersionedDeploymentName(
								"all-at-once-rollout-2-replicas",
								testhelpers.MakeBuildId("all-at-once-rollout-2-replicas", "v1", nil),
							),
						},
					},
				},
				RampingVersion:       nil,
				DeprecatedVersions:   nil,
				VersionConflictToken: nil,
				LastModifierIdentity: "",
			},
		},
		"progressive-rollout-expect-first-step": {
			input: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("progressive-rollout-expect-first-step"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = []temporaliov1alpha1.RolloutStep{
					{5, metav1.Duration{time.Hour}},
				}
				obj.Spec.Template = testhelpers.MakeHelloWorldPodSpec("v1")
				obj.Spec.WorkerOptions = temporaliov1alpha1.WorkerOptions{
					TemporalConnection: "progressive-rollout-expect-first-step",
					TemporalNamespace:  ts.GetDefaultNamespace(),
				}
				obj.ObjectMeta = metav1.ObjectMeta{
					Name:      "progressive-rollout-expect-first-step",
					Namespace: testNamespace.Name,
					Labels:    map[string]string{"app": "test-worker"},
				}
				return obj
			}),
			deprecatedBuildReplicas: nil,
			expectedStatus: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: testhelpers.MakeVersionId(testNamespace.Name, "progressive-rollout-expect-first-step", "v1"),
						Deployment: &corev1.ObjectReference{
							Namespace: testNamespace.Name,
							Name: k8s.ComputeVersionedDeploymentName(
								"progressive-rollout-expect-first-step",
								testhelpers.MakeBuildId("progressive-rollout-expect-first-step", "v1", nil),
							),
						},
					},
					TestWorkflows:      nil,
					RampPercentage:     &testRampPercentStep1,
					RampingSince:       nil, // not tested (for now at least)
					RampLastModifiedAt: nil, // not tested (for now at least)
				},
				CurrentVersion: nil,
				RampingVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: testhelpers.MakeVersionId(testNamespace.Name, "progressive-rollout-expect-first-step", "v1"),
						Deployment: &corev1.ObjectReference{
							Namespace: testNamespace.Name,
							Name: k8s.ComputeVersionedDeploymentName(
								"progressive-rollout-expect-first-step",
								testhelpers.MakeBuildId("progressive-rollout-expect-first-step", "v1", nil),
							),
						},
					},
					TestWorkflows:      nil,
					RampPercentage:     &testRampPercentStep1,
					RampingSince:       nil, // not tested (for now at least)
					RampLastModifiedAt: nil, // not tested (for now at least)
				},
				DeprecatedVersions:   nil,
				VersionConflictToken: nil,
				LastModifierIdentity: "",
			},
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			// TODO(carlydf): create starting test env
			// - Use input.Status + deprecatedBuildReplicas to create (and maybe kill) pollers for deprecated versions in temporal
			// - also get routing config of the deployment into the starting state before running the test

			testTemporalWorkerDeploymentCreation(t, k8sClient, ts, tc.input, tc.expectedStatus)
		})

	}

}

// testTemporalWorkerDeploymentCreation tests the creation of a TemporalWorkerDeployment and waits for the expected status
func testTemporalWorkerDeploymentCreation(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, twd *temporaliov1alpha1.TemporalWorkerDeployment, expectedStatus *temporaliov1alpha1.TemporalWorkerDeploymentStatus) {
	ctx := context.Background()

	t.Log("Creating a TemporalConnection")
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      twd.Spec.WorkerOptions.TemporalConnection,
			Namespace: twd.Namespace,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}

	t.Log("Creating a TemporalWorkerDeployment")
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TemporalWorkerDeployment: %v", err)
	}

	t.Log("Waiting for the controller to reconcile")
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))
	waitForDeployment(t, k8sClient, expectedDeploymentName, twd.Namespace, 30*time.Second)
	workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, twd.Namespace)
	defer func() {
		for _, f := range workerStopFuncs {
			if f != nil {
				f()
			}
		}
	}()

	verifyTemporalWorkerDeploymentStatusEventually(t, ctx, k8sClient, twd.Name, twd.Namespace, expectedStatus, 60*time.Second, 10*time.Second)
}
