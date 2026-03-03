// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// runRolloutTests runs all rollout gap integration tests as sub-tests.
// Called from TestIntegration so that all tests share the same envtest + Temporal setup.
func runRolloutTests(
	t *testing.T,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	testNamespace *corev1.Namespace,
) {
	// Test 8: Progressive rollout auto-promotes to Current after a short pause expires.
	// Uses a live two-phase rollout:
	//   Phase 1: AllAtOnce targeting "v0" → get v0 to Current
	//   Phase 2: switch to Progressive(5%, 1ms) targeting "v1" → controller auto-promotes v1
	t.Run("progressive-ramp-to-current", func(t *testing.T) {
		ctx := context.Background()
		twdName := "prog-ramp-to-current"

		// --- Phase 1: Create TWD with AllAtOnce strategy targeting "v0", wait for Current. ---
		tc := testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithTargetTemplate("v0"),
			).
			BuildWithValues(twdName, testNamespace.Name, ts.GetDefaultNamespace())
		twd := tc.GetTWD()

		temporalConnection := &temporaliov1alpha1.TemporalConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name:      twd.Spec.WorkerOptions.TemporalConnectionRef.Name,
				Namespace: twd.Namespace,
			},
			Spec: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort: ts.GetFrontendHostPort(),
			},
		}
		if err := k8sClient.Create(ctx, temporalConnection); err != nil {
			t.Fatalf("failed to create TemporalConnection: %v", err)
		}
		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create TWD (phase 1): %v", err)
		}

		buildIDv0 := k8s.ComputeBuildID(twd)
		depNameV0 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv0)

		eventually(t, 30*time.Second, time.Second, func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{Name: depNameV0, Namespace: testNamespace.Name}, &dep)
		})
		stopFuncsV0 := applyDeployment(t, ctx, k8sClient, depNameV0, testNamespace.Name)
		defer handleStopFuncs(stopFuncsV0)

		eventually(t, 30*time.Second, 3*time.Second, func() error {
			var tw temporaliov1alpha1.TemporalWorkerDeployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &tw); err != nil {
				return err
			}
			if tw.Status.TargetVersion.Status != temporaliov1alpha1.VersionStatusCurrent {
				return fmt.Errorf("phase 1: v0 not Current (status=%s)", tw.Status.TargetVersion.Status)
			}
			return nil
		})
		t.Logf("Phase 1 complete: v0 (%s) is Current", buildIDv0)

		// --- Phase 2: switch to Progressive(5%, 1ms) targeting "v1". ---
		// With a 1ms pause, the controller promotes v1 to Current as soon as it is Ramping.
		var currentTWD temporaliov1alpha1.TemporalWorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentTWD); err != nil {
			t.Fatalf("failed to get TWD for phase 2: %v", err)
		}
		updatedTWD := currentTWD.DeepCopy()
		updatedTWD.Spec.Template.Spec.Containers[0].Image = "v1"
		// 30s is the minimum allowed pause duration (webhook rejects anything shorter).
		// The controller auto-promotes once the pause expires and workers are healthy.
		updatedTWD.Spec.RolloutStrategy = temporaliov1alpha1.RolloutStrategy{
			Strategy: temporaliov1alpha1.UpdateProgressive,
			Steps: []temporaliov1alpha1.RolloutStep{
				testhelpers.ProgressiveStep(5, 30*time.Second),
			},
		}
		if err := k8sClient.Update(ctx, updatedTWD); err != nil {
			t.Fatalf("failed to update TWD to v1 with progressive strategy: %v", err)
		}

		buildIDv1 := testhelpers.MakeBuildId(twdName, "v1", "", nil)
		depNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)

		eventually(t, 30*time.Second, time.Second, func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{Name: depNameV1, Namespace: testNamespace.Name}, &dep)
		})
		stopFuncsV1 := applyDeployment(t, ctx, k8sClient, depNameV1, testNamespace.Name)
		defer handleStopFuncs(stopFuncsV1)

		// After workers register for v1, the controller sets the version to Ramping.
		// Once the 30s pause expires, it auto-promotes to Current.
		eventually(t, 90*time.Second, 3*time.Second, func() error {
			var tw temporaliov1alpha1.TemporalWorkerDeployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &tw); err != nil {
				return err
			}
			if tw.Status.CurrentVersion == nil {
				return fmt.Errorf("CurrentVersion not set yet")
			}
			if tw.Status.CurrentVersion.BuildID != buildIDv1 {
				return fmt.Errorf("CurrentVersion BuildID = %q, want %q", tw.Status.CurrentVersion.BuildID, buildIDv1)
			}
			if tw.Status.TargetVersion.Status != temporaliov1alpha1.VersionStatusCurrent {
				return fmt.Errorf("TargetVersion Status = %q, want %q", tw.Status.TargetVersion.Status, temporaliov1alpha1.VersionStatusCurrent)
			}
			return nil
		})
		t.Logf("Progressive ramp correctly promoted v1 (%s) to Current after 30s pause", buildIDv1)
	})

	// Test 9: The controller repairs a stale ConnectionSpecHash annotation on a versioned Deployment.
	// Rationale: the controller's UpdateDeployments pass compares the deployment's
	// ConnectionSpecHashAnnotation against k8s.ComputeConnectionSpecHash(connection.Spec) on every
	// reconcile and patches the deployment when they differ. We verify this by directly corrupting
	// the annotation on the live Deployment and asserting the controller restores the correct hash.
	//
	// Note: we do NOT change TemporalConnection.Spec.HostPort here. Changing HostPort to an
	// unreachable address causes UpsertClient to fail eagerly (worker_controller.go:176), which
	// short-circuits the reconcile before plan generation runs and the annotation would never update.
	t.Run("connection-spec-change-rolling-update", func(t *testing.T) {
		ctx := context.Background()
		twdName := "connection-update-test"

		// Use the standard TWOR test base: creates TWD, starts workers, waits for Current.
		twd, temporalConnection, buildID, stopFuncs := setupTWORTestBase(t, ctx, k8sClient, mgr, ts, testNamespace, twdName)
		defer handleStopFuncs(stopFuncs)

		depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

		// Compute the correct hash from the live TemporalConnection spec.
		var currentConn temporaliov1alpha1.TemporalConnection
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: temporalConnection.Name, Namespace: testNamespace.Name}, &currentConn); err != nil {
			t.Fatalf("failed to get TemporalConnection: %v", err)
		}
		expectedHash := k8s.ComputeConnectionSpecHash(currentConn.Spec)

		// Record the current annotation on the Deployment and verify it is already correct.
		var dep appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: depName, Namespace: testNamespace.Name}, &dep); err != nil {
			t.Fatalf("failed to get Deployment: %v", err)
		}
		actualHash := dep.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation]
		if actualHash != expectedHash {
			t.Fatalf("initial ConnectionSpecHashAnnotation mismatch: got %q, want %q", actualHash, expectedHash)
		}
		t.Logf("Verified initial connection spec hash: %s", actualHash)

		// Corrupt the annotation directly — simulate what would happen if the annotation
		// got out of sync (e.g. manual edit, bug, or controller restart mid-update).
		depPatch := client.MergeFrom(dep.DeepCopy())
		dep.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] = "stale-hash-intentionally-wrong"
		if err := k8sClient.Patch(ctx, &dep, depPatch); err != nil {
			t.Fatalf("failed to corrupt ConnectionSpecHashAnnotation: %v", err)
		}
		t.Log("Corrupted ConnectionSpecHashAnnotation — waiting for controller to repair it")

		// The controller detects the stale annotation on the next reconcile and restores the
		// correct hash. We assert it converges back to expectedHash.
		eventually(t, 30*time.Second, time.Second, func() error {
			var updatedDep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: depName, Namespace: testNamespace.Name}, &updatedDep); err != nil {
				return err
			}
			restoredHash := updatedDep.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation]
			if restoredHash != expectedHash {
				return fmt.Errorf("ConnectionSpecHashAnnotation = %q, want %q (controller has not repaired it yet)", restoredHash, expectedHash)
			}
			t.Logf("ConnectionSpecHashAnnotation repaired: stale → %s", restoredHash)
			return nil
		})
	})

	// Test 10: Gate input from ConfigMap — controller blocks deployment until ConfigMap exists.
	// Validates that when rollout.gate.inputFrom.configMapKeyRef references a missing ConfigMap,
	// the controller does not create the versioned Deployment. Once the ConfigMap is created,
	// the rollout proceeds normally.
	t.Run("gate-input-from-configmap", func(t *testing.T) {
		ctx := context.Background()
		twdName := "gate-configmap-test"
		configMapName := "gate-cm-" + twdName

		// Build TWD with AllAtOnce strategy and a gate workflow that reads input from a ConfigMap.
		tc := testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithTargetTemplate("v1"),
			).
			BuildWithValues(twdName, testNamespace.Name, ts.GetDefaultNamespace())
		twd := tc.GetTWD()

		// "successTestWorkflow" is the gate workflow registered by RunHelloWorldWorker.
		// The InputFrom instructs the controller to read gate input from a ConfigMap.
		twd.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{
			WorkflowType: "successTestWorkflow",
			InputFrom: &temporaliov1alpha1.GateInputSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
					Key:                  "data",
				},
			},
		}

		temporalConnection := &temporaliov1alpha1.TemporalConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name:      twd.Spec.WorkerOptions.TemporalConnectionRef.Name,
				Namespace: twd.Namespace,
			},
			Spec: temporaliov1alpha1.TemporalConnectionSpec{
				HostPort: ts.GetFrontendHostPort(),
			},
		}
		if err := k8sClient.Create(ctx, temporalConnection); err != nil {
			t.Fatalf("failed to create TemporalConnection: %v", err)
		}

		// Create TWD without the ConfigMap. The controller's generatePlan will error
		// ("failed to get ConfigMap") and requeue without creating a Deployment.
		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create TWD: %v", err)
		}

		buildID := k8s.ComputeBuildID(twd)
		depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

		// Verify no versioned Deployment is created while the ConfigMap is absent.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			var dep appsv1.Deployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: depName, Namespace: testNamespace.Name}, &dep); err == nil {
				t.Error("expected no Deployment to be created while gate ConfigMap is missing, but one was found")
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Log("Confirmed: no versioned Deployment created while gate ConfigMap is absent")

		// Create the ConfigMap. The controller should now be able to resolve the gate input
		// and proceed with the rollout.
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: testNamespace.Name,
			},
			Data: map[string]string{"data": `{"proceed": true}`},
		}
		if err := k8sClient.Create(ctx, cm); err != nil {
			t.Fatalf("failed to create gate ConfigMap: %v", err)
		}
		t.Log("Created gate ConfigMap — controller should now proceed with the rollout")

		env := testhelpers.TestEnv{
			K8sClient:                  k8sClient,
			Mgr:                        mgr,
			Ts:                         ts,
			Connection:                 temporalConnection,
			ExistingDeploymentReplicas: make(map[string]int32),
			ExistingDeploymentImages:   make(map[string]string),
			ExpectedDeploymentReplicas: make(map[string]int32),
		}

		// Wait for the Deployment to be created now that the ConfigMap is available.
		waitForExpectedTargetDeployment(t, twd, env, 30*time.Second)

		stopFuncs := applyDeployment(t, ctx, k8sClient, depName, testNamespace.Name)
		defer handleStopFuncs(stopFuncs)

		// Gate workflow succeeds; wait for the version to become Current.
		eventually(t, 60*time.Second, 3*time.Second, func() error {
			var updatedTWD temporaliov1alpha1.TemporalWorkerDeployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updatedTWD); err != nil {
				return err
			}
			if updatedTWD.Status.TargetVersion.Status != temporaliov1alpha1.VersionStatusCurrent {
				return fmt.Errorf("TargetVersion Status = %q, want %q", updatedTWD.Status.TargetVersion.Status, temporaliov1alpha1.VersionStatusCurrent)
			}
			return nil
		})
		t.Logf("Gate + ConfigMap: version %s became Current after ConfigMap was created", buildID)
	})

	// Test 13: Three successive rollouts accumulate two deprecated versions in status.
	// Validates that the controller correctly tracks multiple deprecated versions when
	// a TWD undergoes v0 → v1 → v2 live rollouts.
	t.Run("multiple-deprecated-versions", func(t *testing.T) {
		ctx := context.Background()
		twdName := "multi-dep-test"

		// Create TemporalConnection once; reused across all three rollout phases.
		temporalConnection := &temporaliov1alpha1.TemporalConnection{
			ObjectMeta: metav1.ObjectMeta{Name: twdName, Namespace: testNamespace.Name},
			Spec:       temporaliov1alpha1.TemporalConnectionSpec{HostPort: ts.GetFrontendHostPort()},
		}
		if err := k8sClient.Create(ctx, temporalConnection); err != nil {
			t.Fatalf("failed to create TemporalConnection: %v", err)
		}

		// --- Phase 1: deploy v0 and wait for it to become Current ---
		tc0 := testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithTargetTemplate("v0"),
			).
			BuildWithValues(twdName, testNamespace.Name, ts.GetDefaultNamespace())
		twd := tc0.GetTWD()

		env := testhelpers.TestEnv{
			K8sClient:                  k8sClient,
			Mgr:                        mgr,
			Ts:                         ts,
			Connection:                 temporalConnection,
			ExistingDeploymentReplicas: make(map[string]int32),
			ExistingDeploymentImages:   make(map[string]string),
			ExpectedDeploymentReplicas: make(map[string]int32),
		}

		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create TWD (phase 1): %v", err)
		}

		buildIDv0 := k8s.ComputeBuildID(twd)
		waitForExpectedTargetDeployment(t, twd, env, 30*time.Second)
		stopFuncsV0 := applyDeployment(t, ctx, k8sClient, k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv0), testNamespace.Name)
		defer handleStopFuncs(stopFuncsV0)

		eventually(t, 30*time.Second, 3*time.Second, func() error {
			var updatedTWD temporaliov1alpha1.TemporalWorkerDeployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updatedTWD); err != nil {
				return err
			}
			if updatedTWD.Status.TargetVersion.Status != temporaliov1alpha1.VersionStatusCurrent {
				return fmt.Errorf("phase 1: v0 not Current (status=%s)", updatedTWD.Status.TargetVersion.Status)
			}
			return nil
		})
		t.Logf("Phase 1 complete: v0 (%s) is Current", buildIDv0)

		// --- Phase 2: roll to v1 by updating the image ---
		buildIDv1 := testhelpers.MakeBuildId(twdName, "v1", "", nil)

		var currentTWD temporaliov1alpha1.TemporalWorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentTWD); err != nil {
			t.Fatalf("failed to get TWD (phase 2): %v", err)
		}
		updatedTWD := currentTWD.DeepCopy()
		updatedTWD.Spec.Template.Spec.Containers[0].Image = "v1"
		if err := k8sClient.Update(ctx, updatedTWD); err != nil {
			t.Fatalf("failed to update TWD to v1 (phase 2): %v", err)
		}

		// Wait for the v1 Deployment to appear.
		depNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)
		eventually(t, 30*time.Second, time.Second, func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{Name: depNameV1, Namespace: testNamespace.Name}, &dep)
		})
		stopFuncsV1 := applyDeployment(t, ctx, k8sClient, depNameV1, testNamespace.Name)
		defer handleStopFuncs(stopFuncsV1)

		// Wait for v1 to become Current and v0 to appear in DeprecatedVersions.
		eventually(t, 60*time.Second, 3*time.Second, func() error {
			var tw temporaliov1alpha1.TemporalWorkerDeployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &tw); err != nil {
				return err
			}
			if tw.Status.CurrentVersion == nil || tw.Status.CurrentVersion.BuildID != buildIDv1 {
				return fmt.Errorf("phase 2: v1 not Current yet (currentVersion=%v)", tw.Status.CurrentVersion)
			}
			if len(tw.Status.DeprecatedVersions) < 1 {
				return fmt.Errorf("phase 2: v0 not yet in DeprecatedVersions")
			}
			return nil
		})
		t.Logf("Phase 2 complete: v1 (%s) is Current, v0 (%s) is Deprecated", buildIDv1, buildIDv0)

		// --- Phase 3: roll to v2 by updating the image again ---
		buildIDv2 := testhelpers.MakeBuildId(twdName, "v2", "", nil)

		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentTWD); err != nil {
			t.Fatalf("failed to get TWD (phase 3): %v", err)
		}
		updatedTWD = currentTWD.DeepCopy()
		updatedTWD.Spec.Template.Spec.Containers[0].Image = "v2"
		if err := k8sClient.Update(ctx, updatedTWD); err != nil {
			t.Fatalf("failed to update TWD to v2 (phase 3): %v", err)
		}

		depNameV2 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv2)
		eventually(t, 30*time.Second, time.Second, func() error {
			var dep appsv1.Deployment
			return k8sClient.Get(ctx, types.NamespacedName{Name: depNameV2, Namespace: testNamespace.Name}, &dep)
		})
		stopFuncsV2 := applyDeployment(t, ctx, k8sClient, depNameV2, testNamespace.Name)
		defer handleStopFuncs(stopFuncsV2)

		// Wait for v2 Current and BOTH v0 and v1 in DeprecatedVersions.
		eventually(t, 90*time.Second, 3*time.Second, func() error {
			var tw temporaliov1alpha1.TemporalWorkerDeployment
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &tw); err != nil {
				return err
			}
			if tw.Status.CurrentVersion == nil || tw.Status.CurrentVersion.BuildID != buildIDv2 {
				return fmt.Errorf("phase 3: v2 not Current yet (currentVersion=%v)", tw.Status.CurrentVersion)
			}
			if len(tw.Status.DeprecatedVersions) < 2 {
				return fmt.Errorf("phase 3: expected ≥2 deprecated versions, got %d", len(tw.Status.DeprecatedVersions))
			}
			depBuildIDs := make(map[string]bool)
			for _, dv := range tw.Status.DeprecatedVersions {
				depBuildIDs[dv.BuildID] = true
			}
			if !depBuildIDs[buildIDv0] {
				return fmt.Errorf("phase 3: v0 (%s) not in DeprecatedVersions (got: %v)", buildIDv0, tw.Status.DeprecatedVersions)
			}
			if !depBuildIDs[buildIDv1] {
				return fmt.Errorf("phase 3: v1 (%s) not in DeprecatedVersions (got: %v)", buildIDv1, tw.Status.DeprecatedVersions)
			}
			return nil
		})
		t.Logf("Multiple deprecated versions confirmed: v2 (%s) Current, v0 (%s) and v1 (%s) both Deprecated",
			buildIDv2, buildIDv0, buildIDv1)
	})
}
