package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// wrtTestCases returns WRT integration tests as a slice of testCase entries that run through
// the standard testTemporalWorkerDeploymentCreation runner.
//
// Each entry:
//   - Sets up a fresh TWD (AllAtOnce v1.0 → Current) via the shared runner, which confirms the
//     TWD has reached its expected status via verifyTemporalWorkerDeploymentStatusEventually and
//     verifyTemporalStateMatchesStatusEventually.
//   - Uses WithValidatorFunction to exercise WRT-specific behaviour after those checks pass.
//
// Exception: "wrt-multiple-active-versions" uses a Progressive strategy with an existing v0
// Deployment so the runner leaves the TWD in a v1.0=Ramping + v0=Current state before the
// ValidatorFunction creates the WRT.
func wrtTestCases() []testCase {
	return []testCase{
		// WRT owner ref on per-Build-ID resource copy.
		// Verifies that each per-Build-ID HPA has the WorkerResourceTemplate (not the Deployment)
		// as its controller owner reference, so that k8s GC deletes the HPA when the WRT is removed.
		{
			name: "wrt-owner-ref",
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
					wrtName := "ownerref-hpa"

					wrt := makeHPAWRT(wrtName, twd.Namespace, twd.Name)
					if err := env.K8sClient.Create(ctx, wrt); err != nil {
						t.Fatalf("failed to create WRT: %v", err)
					}

					hpaName := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtName, buildID)
					waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, env.K8sClient, twd.Namespace, hpaName, depName, 30*time.Second)
					assertHPAOwnerRefToWRT(t, ctx, env.K8sClient, twd.Namespace, hpaName, wrtName)
				}),
		},

		// matchLabels auto-injection end-to-end.
		// Verifies that a PDB WRT with selector.matchLabels: {} gets the controller-managed
		// selector labels injected before the resource is applied to the API server.
		{
			name: "wrt-matchlabels-injection",
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
					wrtName := "matchlabels-pdb"

					wrt := makeWRTWithRaw(wrtName, twd.Namespace, twd.Name, []byte(`{
						"apiVersion": "policy/v1",
						"kind": "PodDisruptionBudget",
						"spec": {
							"minAvailable": 1,
							"selector": {
								"matchLabels": {}
							}
						}
					}`))
					if err := env.K8sClient.Create(ctx, wrt); err != nil {
						t.Fatalf("failed to create WRT: %v", err)
					}

					pdbName := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtName, buildID)
					waitForPDBWithInjectedMatchLabels(t, ctx, env.K8sClient, twd.Namespace, pdbName, twd.Name, buildID, 30*time.Second)
				}),
		},

		// Multiple WRTs on the same TWD — SSA field manager isolation.
		// Verifies that two WRTs (one HPA, one PDB) targeting the same TWD are applied
		// independently with no field manager conflict between them.
		{
			name: "wrt-multiple-wrts-same-twd",
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
					wrtHPAName := "multi-hpa"
					wrtPDBName := "multi-pdb"

					wrtHPA := makeHPAWRT(wrtHPAName, twd.Namespace, twd.Name)
					if err := env.K8sClient.Create(ctx, wrtHPA); err != nil {
						t.Fatalf("failed to create HPA WRT: %v", err)
					}

					wrtPDB := makeWRTWithRaw(wrtPDBName, twd.Namespace, twd.Name, []byte(`{
						"apiVersion": "policy/v1",
						"kind": "PodDisruptionBudget",
						"spec": {
							"minAvailable": 1,
							"selector": {
								"matchLabels": {}
							}
						}
					}`))
					if err := env.K8sClient.Create(ctx, wrtPDB); err != nil {
						t.Fatalf("failed to create PDB WRT: %v", err)
					}

					hpaName := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtHPAName, buildID)
					pdbName := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtPDBName, buildID)
					depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

					waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, env.K8sClient, twd.Namespace, hpaName, depName, 30*time.Second)
					waitForPDBWithInjectedMatchLabels(t, ctx, env.K8sClient, twd.Namespace, pdbName, twd.Name, buildID, 30*time.Second)
					waitForWRTStatusApplied(t, ctx, env.K8sClient, twd.Namespace, wrtHPAName, buildID, 30*time.Second)
					waitForWRTStatusApplied(t, ctx, env.K8sClient, twd.Namespace, wrtPDBName, buildID, 30*time.Second)
				}),
		},

		// Metric selector matchLabels injection end-to-end.
		// Verifies that the controller appends temporal_worker_deployment_name, temporal_worker_build_id,
		// and temporal_namespace to an External metric selector that has matchLabels present,
		// and that user-provided labels (task_type) coexist alongside the injected keys.
		{
			name: "wrt-metric-selector-injection",
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
					wrtName := "metric-sel-hpa"
					depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

					wrt := makeWRTWithRaw(wrtName, twd.Namespace, twd.Name, []byte(`{
						"apiVersion": "autoscaling/v2",
						"kind": "HorizontalPodAutoscaler",
						"spec": {
							"scaleTargetRef": {},
							"minReplicas": 2,
							"maxReplicas": 5,
							"metrics": [
								{
									"type": "External",
									"external": {
										"metric": {
											"name": "temporal_backlog_count_by_version",
											"selector": {
												"matchLabels": {
													"task_type": "Activity"
												}
											}
										},
										"target": {
											"type": "AverageValue",
											"averageValue": "10"
										}
									}
								}
							]
						}
					}`))
					if err := env.K8sClient.Create(ctx, wrt); err != nil {
						t.Fatalf("failed to create WRT: %v", err)
					}

					hpaName := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtName, buildID)
					waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, env.K8sClient, twd.Namespace, hpaName, depName, 30*time.Second)

					expectedMetricLabels := map[string]string{
						"task_type":                       "Activity",
						"temporal_worker_deployment_name": twd.Namespace + "_" + twd.Name,
						"temporal_worker_build_id":        buildID,
						"temporal_namespace":              twd.Spec.WorkerOptions.TemporalNamespace,
					}
					waitForHPAWithInjectedMetricSelector(t, ctx, env.K8sClient, twd.Namespace, hpaName, expectedMetricLabels, 30*time.Second)
				}),
		},

		// Multiple active versions (current + ramping) each get a WRT copy.
		// Uses a Progressive strategy with v0 pre-seeded as Current so the controller leaves
		// v1.0 in Ramping state. The ValidatorFunction then verifies that one HPA copy is
		// created per active Build ID (both v0 and v1.0).
		{
			name: "wrt-multiple-active-versions",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
						WithTargetTemplate("v1.0").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v0", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
								WithCurrentVersion("v0", true, true),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusRamping, 5, true, false).
						WithCurrentVersion("v0", true, true),
				).
				WithValidatorFunction(func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
					twd := tc.GetTWD()
					buildIDv1 := k8s.ComputeBuildID(twd)
					buildIDv0 := testhelpers.MakeBuildID(twd.Name, "v0", "", nil)
					wrtName := "multi-ver-hpa"

					wrt := makeHPAWRT(wrtName, twd.Namespace, twd.Name)
					if err := env.K8sClient.Create(ctx, wrt); err != nil {
						t.Fatalf("failed to create WRT: %v", err)
					}

					hpaNameV0 := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtName, buildIDv0)
					hpaNameV1 := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtName, buildIDv1)
					depNameV0 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv0)
					depNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)

					waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, env.K8sClient, twd.Namespace, hpaNameV0, depNameV0, 30*time.Second)
					waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, env.K8sClient, twd.Namespace, hpaNameV1, depNameV1, 30*time.Second)
					waitForWRTStatusApplied(t, ctx, env.K8sClient, twd.Namespace, wrtName, buildIDv0, 30*time.Second)
					waitForWRTStatusApplied(t, ctx, env.K8sClient, twd.Namespace, wrtName, buildIDv1, 30*time.Second)
				}),
		},

		// scaleTargetRef injection survives the k8s API server round-trip.
		//
		// Regression test for: scaleTargetRef: null is silently stripped by the Kubernetes API
		// server before storage, so null was never present in the stored WRT and the controller's
		// nil-check never fired, causing the HPA to be applied with an empty scaleTargetRef.
		//
		// The fix: accept {} as the opt-in sentinel (non-null, survives storage). This test
		// verifies the full path:
		//   1. WRT with scaleTargetRef:{} is accepted by the webhook (was previously rejected)
		//   2. {} survives the API server round-trip (null would have been stripped)
		//   3. The controller injects the correct Deployment reference into the HPA
		{
			name: "wrt-scaletargetref-empty-object-sentinel",
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
					wrtName := "empty-obj-sentinel-hpa"

					wrt := makeWRTWithRaw(wrtName, twd.Namespace, twd.Name, []byte(`{
						"apiVersion": "autoscaling/v2",
						"kind": "HorizontalPodAutoscaler",
						"spec": {
							"scaleTargetRef": {},
							"minReplicas": 1,
							"maxReplicas": 3,
							"metrics": []
						}
					}`))
					if err := env.K8sClient.Create(ctx, wrt); err != nil {
						t.Fatalf("webhook rejected WRT with scaleTargetRef:{} (regression: {} must be accepted as opt-in sentinel): %v", err)
					}

					// Read back the stored WRT and verify scaleTargetRef:{} survived storage.
					// This distinguishes {} (preserved) from null (stripped), and confirms the
					// controller will see the empty-map sentinel and inject the correct value.
					var storedWRT temporaliov1alpha1.WorkerResourceTemplate
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: wrtName, Namespace: twd.Namespace}, &storedWRT); err != nil {
						t.Fatalf("failed to read back WRT: %v", err)
					}
					var tmpl map[string]interface{}
					if err := json.Unmarshal(storedWRT.Spec.Template.Raw, &tmpl); err != nil {
						t.Fatalf("failed to unmarshal stored template: %v", err)
					}
					spec, _ := tmpl["spec"].(map[string]interface{})
					if spec == nil {
						t.Fatal("stored template has no spec")
					}
					storedRef, exists := spec["scaleTargetRef"]
					if !exists {
						t.Fatal("scaleTargetRef was stripped from stored WRT template (null sentinel was used instead of {}; use {} to survive API server storage)")
					}
					if m, ok := storedRef.(map[string]interface{}); !ok || len(m) != 0 {
						t.Fatalf("stored scaleTargetRef = %v, want empty map {}", storedRef)
					}

					hpaName := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtName, buildID)
					waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, env.K8sClient, twd.Namespace, hpaName, depName, 30*time.Second)
					waitForWRTStatusApplied(t, ctx, env.K8sClient, twd.Namespace, wrtName, buildID, 30*time.Second)
					t.Log("scaleTargetRef:{} survived API server round-trip and was correctly injected")
				}),
		},

		// Apply failure → status entry with non-empty Message and LastAppliedGeneration == 0.
		// Uses an unknown GVK that the API server cannot recognise. The controller's SSA apply
		// fails and the WRT status must reflect the failure for that Build ID.
		{
			name: "wrt-apply-failure",
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
					wrtName := "fail-resource"

					wrt := makeWRTWithRaw(wrtName, twd.Namespace, twd.Name, []byte(`{
						"apiVersion": "nonexistent.example.com/v1",
						"kind": "FakeResource",
						"spec": {
							"someField": "someValue"
						}
					}`))
					if err := env.K8sClient.Create(ctx, wrt); err != nil {
						t.Fatalf("failed to create WRT: %v", err)
					}

					waitForWRTStatusNotApplied(t, ctx, env.K8sClient, twd.Namespace, wrtName, buildID, 30*time.Second)
				}),
		},

		// SSA idempotency — multiple reconcile loops do not create duplicates.
		// Verifies that the HPA's resourceVersion and ownerReference count do not change after
		// additional reconcile loops are triggered by patching the TWD annotation.
		{
			name: "wrt-ssa-idempotency",
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
					wrtName := "idempotent-hpa"

					wrt := makeHPAWRT(wrtName, twd.Namespace, twd.Name)
					if err := env.K8sClient.Create(ctx, wrt); err != nil {
						t.Fatalf("failed to create WRT: %v", err)
					}

					hpaName := k8s.ComputeWorkerResourceTemplateName(twd.Name, wrtName, buildID)
					depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)
					waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, env.K8sClient, twd.Namespace, hpaName, depName, 30*time.Second)
					waitForWRTStatusApplied(t, ctx, env.K8sClient, twd.Namespace, wrtName, buildID, 30*time.Second)

					// Snapshot the HPA state before triggering extra reconciles.
					var hpaBefore autoscalingv2.HorizontalPodAutoscaler
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: twd.Namespace}, &hpaBefore); err != nil {
						t.Fatalf("failed to get HPA: %v", err)
					}
					rvBefore := hpaBefore.ResourceVersion
					ownerRefCountBefore := len(hpaBefore.OwnerReferences)
					t.Logf("HPA resourceVersion before extra reconciles: %s, ownerRefs: %d", rvBefore, ownerRefCountBefore)

					// Trigger extra reconcile loops by patching the TWD annotation.
					// With RECONCILE_INTERVAL=1s, waiting 4s gives ~4 additional loops.
					var currentTWD temporaliov1alpha1.TemporalWorkerDeployment
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &currentTWD); err != nil {
						t.Fatalf("failed to get TWD: %v", err)
					}
					patch := client.MergeFrom(currentTWD.DeepCopy())
					if currentTWD.Annotations == nil {
						currentTWD.Annotations = map[string]string{}
					}
					currentTWD.Annotations["test-reconcile-trigger"] = "1"
					if err := env.K8sClient.Patch(ctx, &currentTWD, patch); err != nil {
						t.Fatalf("failed to patch TWD annotation: %v", err)
					}
					time.Sleep(4 * time.Second)

					// HPA must be unchanged after additional reconcile loops.
					var hpaAfter autoscalingv2.HorizontalPodAutoscaler
					if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: twd.Namespace}, &hpaAfter); err != nil {
						t.Fatalf("failed to re-fetch HPA: %v", err)
					}
					if hpaAfter.ResourceVersion != rvBefore {
						t.Errorf("HPA resourceVersion changed from %s to %s after extra reconcile loops (SSA should be idempotent)",
							rvBefore, hpaAfter.ResourceVersion)
					}
					if len(hpaAfter.OwnerReferences) != ownerRefCountBefore {
						t.Errorf("HPA ownerReferences count changed from %d to %d — possible duplicate owner ref added",
							ownerRefCountBefore, len(hpaAfter.OwnerReferences))
					}

					var hpaList autoscalingv2.HorizontalPodAutoscalerList
					if err := env.K8sClient.List(ctx, &hpaList,
						client.InNamespace(twd.Namespace),
						client.MatchingLabels{k8s.BuildIDLabel: buildID},
					); err != nil {
						t.Fatalf("failed to list HPAs: %v", err)
					}
					if len(hpaList.Items) != 1 {
						t.Errorf("expected exactly 1 HPA with build ID %q, got %d", buildID, len(hpaList.Items))
					}
					t.Log("SSA idempotency confirmed: HPA unchanged after multiple reconcile loops")
				}),
		},
	}
}

// makeHPAWRT constructs a WorkerResourceTemplate with an HPA spec where
// scaleTargetRef is {} (triggering auto-injection by the controller).
// {} is used instead of null because the Kubernetes API server strips null values
// before storage, which would silently prevent injection.
func makeHPAWRT(name, namespace, workerDeploymentRefName string) *temporaliov1alpha1.WorkerResourceTemplate {
	return makeWRTWithRaw(name, namespace, workerDeploymentRefName, []byte(`{
		"apiVersion": "autoscaling/v2",
		"kind": "HorizontalPodAutoscaler",
		"spec": {
			"scaleTargetRef": {},
			"minReplicas": 2,
			"maxReplicas": 5,
			"metrics": []
		}
	}`))
}

// makeWRTWithRaw constructs a WorkerResourceTemplate with the given raw JSON object spec.
func makeWRTWithRaw(name, namespace, workerDeploymentRefName string, raw []byte) *temporaliov1alpha1.WorkerResourceTemplate {
	return &temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.WorkerResourceTemplateSpec{
			TemporalWorkerDeploymentRef: temporaliov1alpha1.TemporalWorkerDeploymentReference{Name: workerDeploymentRefName},
			Template:                    runtime.RawExtension{Raw: raw},
		},
	}
}

// assertHPAOwnerRefToWRT asserts that the named HPA has a controller owner reference
// pointing at the named WorkerResourceTemplate (not the Deployment or TWD).
func assertHPAOwnerRefToWRT(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, hpaName, expectedWRTName string,
) {
	t.Helper()
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: namespace}, &hpa); err != nil {
		t.Fatalf("failed to get HPA %s: %v", hpaName, err)
	}
	for _, ref := range hpa.OwnerReferences {
		if ref.Kind == "WorkerResourceTemplate" && ref.Name == expectedWRTName && ref.Controller != nil && *ref.Controller {
			t.Logf("HPA %s correctly has controller owner ref to WorkerResourceTemplate %s", hpaName, expectedWRTName)
			return
		}
	}
	t.Errorf("HPA %s/%s missing controller owner reference to WorkerResourceTemplate %s (ownerRefs: %+v)",
		namespace, hpaName, expectedWRTName, hpa.OwnerReferences)
}

// waitForHPAWithInjectedMetricSelector polls until the named HPA has all expectedLabels
// present in the first External metric entry's selector.matchLabels. Extra labels on the
// HPA are allowed — the controller merges its labels into whatever the user provided.
func waitForHPAWithInjectedMetricSelector(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, hpaName string,
	expectedLabels map[string]string,
	timeout time.Duration,
) {
	t.Helper()
	t.Logf("Waiting for HPA %q with injected metric selector labels in namespace %q", hpaName, namespace)
	eventually(t, timeout, time.Second, func() error {
		var hpa autoscalingv2.HorizontalPodAutoscaler
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: namespace}, &hpa); err != nil {
			return err
		}
		if len(hpa.Spec.Metrics) == 0 {
			return fmt.Errorf("HPA %s has no metrics", hpaName)
		}
		ext := hpa.Spec.Metrics[0].External
		if ext == nil || ext.Metric.Selector == nil {
			return fmt.Errorf("HPA %s first metric has no external selector", hpaName)
		}
		ml := ext.Metric.Selector.MatchLabels
		for k, want := range expectedLabels {
			if got := ml[k]; got != want {
				return fmt.Errorf("HPA metric selector matchLabels[%q] = %q, want %q", k, got, want)
			}
		}
		return nil
	})
	t.Logf("HPA %q has correctly injected metric selector labels", hpaName)
}

// waitForPDBWithInjectedMatchLabels polls until the named PDB exists and has the
// expected selector.matchLabels injected by the controller.
func waitForPDBWithInjectedMatchLabels(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, pdbName, twdName, buildID string,
	timeout time.Duration,
) {
	t.Helper()
	t.Logf("Waiting for PDB %q with injected matchLabels in namespace %q", pdbName, namespace)
	eventually(t, timeout, time.Second, func() error {
		var pdb policyv1.PodDisruptionBudget
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: pdbName, Namespace: namespace}, &pdb); err != nil {
			return err
		}
		if pdb.Spec.Selector == nil {
			return fmt.Errorf("PDB %s has nil selector", pdbName)
		}
		ml := pdb.Spec.Selector.MatchLabels
		expected := k8s.ComputeSelectorLabels(twdName, buildID)
		for k, v := range expected {
			if ml[k] != v {
				return fmt.Errorf("PDB matchLabels[%q] = %q, want %q", k, ml[k], v)
			}
		}
		return nil
	})
	t.Logf("PDB %q has correctly injected matchLabels", pdbName)
}

// waitForWRTStatusNotApplied polls until the WRT status contains an entry for buildID
// with a non-empty Message and LastAppliedGeneration == 0 (indicating the SSA apply
// failed and no successful apply has ever occurred for this Build ID).
func waitForWRTStatusNotApplied(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, wrtName, buildID string,
	timeout time.Duration,
) {
	t.Helper()
	eventually(t, timeout, time.Second, func() error {
		var wrt temporaliov1alpha1.WorkerResourceTemplate
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: wrtName, Namespace: namespace}, &wrt); err != nil {
			return err
		}
		for _, v := range wrt.Status.Versions {
			if v.BuildID == buildID {
				if v.ApplyError != "" && v.LastAppliedGeneration == 0 {
					t.Logf("WRT status correctly shows apply failure for build ID %q, message: %s", buildID, v.ApplyError)
					return nil
				}
				return fmt.Errorf("WRT status for build ID %q: lastAppliedGeneration=%d message=%q (want failure with gen=0)", buildID, v.LastAppliedGeneration, v.ApplyError)
			}
		}
		return fmt.Errorf("WRT status has no entry for build ID %q (current: %+v)", buildID, wrt.Status.Versions)
	})
}
