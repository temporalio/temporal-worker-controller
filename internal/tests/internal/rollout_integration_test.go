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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// rolloutTestCases returns rollout integration tests as a slice of testCase entries that run
// through the standard testTemporalWorkerDeploymentCreation runner.
//
// Multi-phase tests (progressive-ramp-to-current, multiple-deprecated-versions) express phase 1
// (initial Current state) through the builder so the shared runner verifies TWD status and
// Temporal state. Phase 2+ (image updates and subsequent Current promotions) are handled in
// ValidatorFunction using direct eventually checks — these phases are inherently sequential
// and the runner does not model them declaratively.
//
// The annotation-repair test (connection-spec-change-rolling-update) uses ValidatorFunction to
// corrupt and then verify repair of the ConnectionSpecHash annotation after the TWD is stable.
//
// The gate test (gate-input-from-configmap) uses WithTWDMutatorFunc to set
// gate.InputFrom.ConfigMapKeyRef before creation, and WithPostTWDCreateFunc to assert the
// rollout is blocked while the ConfigMap is absent, then creates it. The runner then proceeds
// to wait for the version to become Current.
func rolloutTestCases() []testCase {
	return []testCase{
		// Progressive rollout auto-promotes to Current after the pause expires.
		// Phase 1 (via runner): AllAtOnce v0 → Current.
		// Phase 2 (ValidatorFunction): switch to Progressive(5%, 30s) targeting v1; after
		// workers register and the 30s pause expires the controller promotes v1 to Current.
		{
			name: "progressive-ramp-to-current",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v0", true, false),
				).
				WithValidatorFunction(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
					twd := tc.GetTWD()

					var currentTWD temporaliov1alpha1.TemporalWorkerDeployment
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentTWD); err != nil {
						t.Fatalf("failed to get TWD for phase 2: %v", err)
					}
					updatedTWD := currentTWD.DeepCopy()
					updatedTWD.Spec.Template.Spec.Containers[0].Image = "v1"
					// 30s is the minimum allowed pause duration (webhook rejects anything shorter).
					updatedTWD.Spec.RolloutStrategy = temporaliov1alpha1.RolloutStrategy{
						Strategy: temporaliov1alpha1.UpdateProgressive,
						Steps:    []temporaliov1alpha1.RolloutStep{testhelpers.ProgressiveStep(5, 30*time.Second)},
					}
					if err := env.K8sClient.Update(ctx, updatedTWD); err != nil {
						t.Fatalf("failed to update TWD to v1 with progressive strategy: %v", err)
					}

					buildIDv1 := testhelpers.MakeBuildId(twd.Name, "v1", "", nil)
					depNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)

					eventually(t, 30*time.Second, time.Second, func() error {
						var dep appsv1.Deployment
						return env.K8sClient.Get(ctx, types.NamespacedName{Name: depNameV1, Namespace: twd.Namespace}, &dep)
					})
					stopFuncsV1 := applyDeployment(t, ctx, env.K8sClient, depNameV1, twd.Namespace)
					defer handleStopFuncs(stopFuncsV1)

					// After workers register for v1 and the 30s pause expires, the controller
					// auto-promotes v1 to Current.
					eventually(t, 90*time.Second, 3*time.Second, func() error {
						var tw temporaliov1alpha1.TemporalWorkerDeployment
						if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &tw); err != nil {
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
				}),
		},

		// The controller repairs a stale ConnectionSpecHash annotation on a Deployment.
		// The controller's UpdateDeployments pass compares ConnectionSpecHashAnnotation against
		// k8s.ComputeConnectionSpecHash(connection.Spec) on every reconcile and patches the
		// Deployment when they differ. We verify this by directly corrupting the annotation on
		// the live Deployment and asserting the controller restores the correct hash.
		{
			name: "connection-spec-change-rolling-update",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v1.0", true, false),
				).
				WithValidatorFunction(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
					twd := tc.GetTWD()
					buildID := k8s.ComputeBuildID(twd)
					depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

					// Compute the correct hash from the live TemporalConnection spec.
					var currentConn temporaliov1alpha1.TemporalConnection
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentConn); err != nil {
						t.Fatalf("failed to get TemporalConnection: %v", err)
					}
					expectedHash := k8s.ComputeConnectionSpecHash(currentConn.Spec)

					// Verify the annotation is already correct.
					var dep appsv1.Deployment
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: depName, Namespace: twd.Namespace}, &dep); err != nil {
						t.Fatalf("failed to get Deployment: %v", err)
					}
					actualHash := dep.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation]
					if actualHash != expectedHash {
						t.Fatalf("initial ConnectionSpecHashAnnotation mismatch: got %q, want %q", actualHash, expectedHash)
					}
					t.Logf("Verified initial connection spec hash: %s", actualHash)

					// Corrupt the annotation to simulate a stale annotation scenario.
					depPatch := client.MergeFrom(dep.DeepCopy())
					dep.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation] = "stale-hash-intentionally-wrong"
					if err := env.K8sClient.Patch(ctx, &dep, depPatch); err != nil {
						t.Fatalf("failed to corrupt ConnectionSpecHashAnnotation: %v", err)
					}
					t.Log("Corrupted ConnectionSpecHashAnnotation — waiting for controller to repair it")

					// The controller detects the stale annotation on the next reconcile and restores it.
					eventually(t, 30*time.Second, time.Second, func() error {
						var updatedDep appsv1.Deployment
						if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: depName, Namespace: twd.Namespace}, &updatedDep); err != nil {
							return err
						}
						restoredHash := updatedDep.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation]
						if restoredHash != expectedHash {
							return fmt.Errorf("ConnectionSpecHashAnnotation = %q, want %q (controller has not repaired it yet)", restoredHash, expectedHash)
						}
						t.Logf("ConnectionSpecHashAnnotation repaired: stale → %s", restoredHash)
						return nil
					})
				}),
		},

		// Gate input from ConfigMap — controller blocks deployment until ConfigMap exists.
		// WithTWDMutatorFunc sets gate.InputFrom.ConfigMapKeyRef on the TWD before creation.
		// WithPostTWDCreateFunc asserts no Deployment is created while the ConfigMap is absent,
		// then creates the ConfigMap. The runner then proceeds to create the Deployment and wait
		// for the gate workflow to succeed and the version to become Current.
		{
			name: "gate-input-from-configmap",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v1"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v1", true, false),
				).
				WithTWDMutatorFunc(func(twd *temporaliov1alpha1.TemporalWorkerDeployment) {
					configMapName := "gate-cm-gate-input-from-configmap"
					twd.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{
						WorkflowType: "successTestWorkflow",
						InputFrom: &temporaliov1alpha1.GateInputSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
								Key:                  "data",
							},
						},
					}
				}).
				WithPostTWDCreateFunc(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
					twd := tc.GetTWD()
					buildID := k8s.ComputeBuildID(twd)
					depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)
					configMapName := "gate-cm-gate-input-from-configmap"

					// Verify no versioned Deployment is created while the ConfigMap is absent.
					deadline := time.Now().Add(3 * time.Second)
					for time.Now().Before(deadline) {
						var dep appsv1.Deployment
						if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: depName, Namespace: twd.Namespace}, &dep); err == nil {
							t.Error("expected no Deployment to be created while gate ConfigMap is missing, but one was found")
							break
						}
						time.Sleep(500 * time.Millisecond)
					}
					t.Log("Confirmed: no versioned Deployment created while gate ConfigMap is absent")

					// Create the ConfigMap to unblock the rollout.
					cm := &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      configMapName,
							Namespace: twd.Namespace,
						},
						Data: map[string]string{"data": `{"proceed": true}`},
					}
					if err := env.K8sClient.Create(ctx, cm); err != nil {
						t.Fatalf("failed to create gate ConfigMap: %v", err)
					}
					t.Log("Created gate ConfigMap — controller should now proceed with the rollout")
				}),
		},

		// Three successive rollouts accumulate two deprecated versions in status.
		// Phase 1 (via runner): AllAtOnce v0 → Current.
		// Phases 2–3 (ValidatorFunction): live image updates v0→v1→v2; asserts that after v2
		// is Current, both v0 and v1 appear in DeprecatedVersions.
		{
			name: "multiple-deprecated-versions",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v0", true, false),
				).
				WithValidatorFunction(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
					twd := tc.GetTWD()
					buildIDv0 := k8s.ComputeBuildID(twd)

					// --- Phase 2: roll to v1 by updating the image ---
					buildIDv1 := testhelpers.MakeBuildId(twd.Name, "v1", "", nil)

					var currentTWD temporaliov1alpha1.TemporalWorkerDeployment
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentTWD); err != nil {
						t.Fatalf("failed to get TWD (phase 2): %v", err)
					}
					updatedTWD := currentTWD.DeepCopy()
					updatedTWD.Spec.Template.Spec.Containers[0].Image = "v1"
					if err := env.K8sClient.Update(ctx, updatedTWD); err != nil {
						t.Fatalf("failed to update TWD to v1 (phase 2): %v", err)
					}

					depNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)
					eventually(t, 30*time.Second, time.Second, func() error {
						var dep appsv1.Deployment
						return env.K8sClient.Get(ctx, types.NamespacedName{Name: depNameV1, Namespace: twd.Namespace}, &dep)
					})
					stopFuncsV1 := applyDeployment(t, ctx, env.K8sClient, depNameV1, twd.Namespace)
					defer handleStopFuncs(stopFuncsV1)

					eventually(t, 60*time.Second, 3*time.Second, func() error {
						var tw temporaliov1alpha1.TemporalWorkerDeployment
						if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &tw); err != nil {
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
					buildIDv2 := testhelpers.MakeBuildId(twd.Name, "v2", "", nil)

					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentTWD); err != nil {
						t.Fatalf("failed to get TWD (phase 3): %v", err)
					}
					updatedTWD = currentTWD.DeepCopy()
					updatedTWD.Spec.Template.Spec.Containers[0].Image = "v2"
					if err := env.K8sClient.Update(ctx, updatedTWD); err != nil {
						t.Fatalf("failed to update TWD to v2 (phase 3): %v", err)
					}

					depNameV2 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv2)
					eventually(t, 30*time.Second, time.Second, func() error {
						var dep appsv1.Deployment
						return env.K8sClient.Get(ctx, types.NamespacedName{Name: depNameV2, Namespace: twd.Namespace}, &dep)
					})
					stopFuncsV2 := applyDeployment(t, ctx, env.K8sClient, depNameV2, twd.Namespace)
					defer handleStopFuncs(stopFuncsV2)

					eventually(t, 90*time.Second, 3*time.Second, func() error {
						var tw temporaliov1alpha1.TemporalWorkerDeployment
						if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &tw); err != nil {
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
				}),
		},
	}
}
