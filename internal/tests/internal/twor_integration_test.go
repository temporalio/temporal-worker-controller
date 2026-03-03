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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// runTWORTests runs all TWOR gap integration tests as sub-tests.
// Called from TestIntegration so that all tests share the same envtest + Temporal setup.
func runTWORTests(
	t *testing.T,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	testNamespace *corev1.Namespace,
) {
	// Test 1: Deployment owner ref on per-Build-ID resource copy
	t.Run("twor-deployment-owner-ref", func(t *testing.T) {
		ctx := context.Background()
		twdName := "twor-owner-ref-test"
		tworName := "ownerref-hpa"

		twd, _, buildID, stopFuncs := setupTWORTestBase(t, ctx, k8sClient, mgr, ts, testNamespace, twdName)
		defer handleStopFuncs(stopFuncs)

		twor := makeHPATWOR(tworName, testNamespace.Name, twd.Name)
		if err := k8sClient.Create(ctx, twor); err != nil {
			t.Fatalf("failed to create TWOR: %v", err)
		}

		hpaName := k8s.ComputeOwnedResourceName(twd.Name, tworName, buildID)
		depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

		waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, testNamespace.Name, hpaName, depName, 30*time.Second)

		// Assert the HPA has the versioned Deployment (not the TWD) as its controller owner reference.
		assertHPAOwnerRefToDeployment(t, ctx, k8sClient, testNamespace.Name, hpaName, depName)
	})

	// Test 2: matchLabels auto-injection end-to-end
	t.Run("twor-matchlabels-injection", func(t *testing.T) {
		ctx := context.Background()
		twdName := "twor-matchlabels-test"
		tworName := "matchlabels-pdb"

		twd, _, buildID, stopFuncs := setupTWORTestBase(t, ctx, k8sClient, mgr, ts, testNamespace, twdName)
		defer handleStopFuncs(stopFuncs)

		// PDB with selector.matchLabels: null — controller should auto-inject the selector labels.
		twor := &temporaliov1alpha1.TemporalWorkerOwnedResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tworName,
				Namespace: testNamespace.Name,
			},
			Spec: temporaliov1alpha1.TemporalWorkerOwnedResourceSpec{
				WorkerRef: temporaliov1alpha1.WorkerDeploymentReference{Name: twd.Name},
				Object: runtime.RawExtension{Raw: []byte(`{
					"apiVersion": "policy/v1",
					"kind": "PodDisruptionBudget",
					"spec": {
						"minAvailable": 1,
						"selector": {
							"matchLabels": null
						}
					}
				}`)},
			},
		}
		if err := k8sClient.Create(ctx, twor); err != nil {
			t.Fatalf("failed to create TWOR: %v", err)
		}

		pdbName := k8s.ComputeOwnedResourceName(twd.Name, tworName, buildID)
		waitForPDBWithInjectedMatchLabels(t, ctx, k8sClient, testNamespace.Name, pdbName, twd.Name, buildID, 30*time.Second)
	})

	// Test 3: Multiple TWORs on the same TWD — SSA field manager isolation
	t.Run("twor-multiple-twors-same-twd", func(t *testing.T) {
		ctx := context.Background()
		twdName := "twor-multi-test"
		tworHPAName := "multi-hpa"
		tworPDBName := "multi-pdb"

		twd, _, buildID, stopFuncs := setupTWORTestBase(t, ctx, k8sClient, mgr, ts, testNamespace, twdName)
		defer handleStopFuncs(stopFuncs)

		tworHPA := makeHPATWOR(tworHPAName, testNamespace.Name, twd.Name)
		if err := k8sClient.Create(ctx, tworHPA); err != nil {
			t.Fatalf("failed to create HPA TWOR: %v", err)
		}

		tworPDB := &temporaliov1alpha1.TemporalWorkerOwnedResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tworPDBName,
				Namespace: testNamespace.Name,
			},
			Spec: temporaliov1alpha1.TemporalWorkerOwnedResourceSpec{
				WorkerRef: temporaliov1alpha1.WorkerDeploymentReference{Name: twd.Name},
				Object: runtime.RawExtension{Raw: []byte(`{
					"apiVersion": "policy/v1",
					"kind": "PodDisruptionBudget",
					"spec": {
						"minAvailable": 1,
						"selector": {
							"matchLabels": null
						}
					}
				}`)},
			},
		}
		if err := k8sClient.Create(ctx, tworPDB); err != nil {
			t.Fatalf("failed to create PDB TWOR: %v", err)
		}

		hpaName := k8s.ComputeOwnedResourceName(twd.Name, tworHPAName, buildID)
		pdbName := k8s.ComputeOwnedResourceName(twd.Name, tworPDBName, buildID)
		depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

		// Both resources should be created with no field manager conflict.
		waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, testNamespace.Name, hpaName, depName, 30*time.Second)
		waitForPDBWithInjectedMatchLabels(t, ctx, k8sClient, testNamespace.Name, pdbName, twd.Name, buildID, 30*time.Second)

		// Both TWORs should have Applied:true.
		waitForTWORStatusApplied(t, ctx, k8sClient, testNamespace.Name, tworHPAName, buildID, 30*time.Second)
		waitForTWORStatusApplied(t, ctx, k8sClient, testNamespace.Name, tworPDBName, buildID, 30*time.Second)
	})

	// Test 4: Template variable rendering end-to-end
	t.Run("twor-template-variable", func(t *testing.T) {
		ctx := context.Background()
		twdName := "twor-template-test"
		tworName := "template-hpa"

		twd, _, buildID, stopFuncs := setupTWORTestBase(t, ctx, k8sClient, mgr, ts, testNamespace, twdName)
		defer handleStopFuncs(stopFuncs)

		// HPA with a Go template expression in an annotation.
		// After rendering, the annotation should contain the actual versioned Deployment name.
		twor := &temporaliov1alpha1.TemporalWorkerOwnedResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tworName,
				Namespace: testNamespace.Name,
			},
			Spec: temporaliov1alpha1.TemporalWorkerOwnedResourceSpec{
				WorkerRef: temporaliov1alpha1.WorkerDeploymentReference{Name: twd.Name},
				Object: runtime.RawExtension{Raw: []byte(`{
					"apiVersion": "autoscaling/v2",
					"kind": "HorizontalPodAutoscaler",
					"metadata": {
						"annotations": {
							"my-deployment": "{{ .DeploymentName }}"
						}
					},
					"spec": {
						"scaleTargetRef": null,
						"minReplicas": 2,
						"maxReplicas": 5,
						"metrics": []
					}
				}`)},
			},
		}
		if err := k8sClient.Create(ctx, twor); err != nil {
			t.Fatalf("failed to create TWOR: %v", err)
		}

		hpaName := k8s.ComputeOwnedResourceName(twd.Name, tworName, buildID)
		expectedDepName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

		// Poll until the HPA appears and verify the annotation was rendered.
		waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, testNamespace.Name, hpaName, expectedDepName, 30*time.Second)

		eventually(t, 30*time.Second, time.Second, func() error {
			var hpa autoscalingv2.HorizontalPodAutoscaler
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: testNamespace.Name}, &hpa); err != nil {
				return err
			}
			got := hpa.Annotations["my-deployment"]
			if got != expectedDepName {
				return fmt.Errorf("annotation my-deployment = %q, want %q", got, expectedDepName)
			}
			return nil
		})
		t.Logf("Template variable {{ .DeploymentName }} correctly rendered to %q", expectedDepName)
	})

	// Test 5: Multiple active versions (current + ramping) each get a TWOR copy
	t.Run("twor-multiple-active-versions", func(t *testing.T) {
		ctx := context.Background()
		twdName := "twor-multi-ver-test"
		tworName := "multi-ver-hpa"

		// Set up: v0 as Current, then roll to v1 with progressive strategy (long pause → stays Ramping).
		tc := testhelpers.NewTestCase().
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

		env := testhelpers.TestEnv{
			K8sClient:                  k8sClient,
			Mgr:                        mgr,
			Ts:                         ts,
			Connection:                 temporalConnection,
			ExistingDeploymentReplicas: tc.GetExistingDeploymentReplicas(),
			ExistingDeploymentImages:   tc.GetExistingDeploymentImages(),
			ExpectedDeploymentReplicas: make(map[string]int32),
		}

		// Set up preliminary state (v0 as current in Temporal).
		makePreliminaryStatusTrue(ctx, t, env, twd)
		verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, twd.Status, 30*time.Second, 5*time.Second)

		if err := k8sClient.Create(ctx, twd); err != nil {
			t.Fatalf("failed to create TWD: %v", err)
		}

		// Wait for v1 deployment to be created.
		waitForExpectedTargetDeployment(t, twd, env, 30*time.Second)

		buildIDv1 := k8s.ComputeBuildID(twd)
		depNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)
		stopFuncsV1 := applyDeployment(t, ctx, k8sClient, depNameV1, testNamespace.Name)
		defer handleStopFuncs(stopFuncsV1)

		// Wait for v1 to be Ramping at 5% while v0 remains Current.
		buildIDv0 := testhelpers.MakeBuildId(twdName, "v0", "", nil)
		expectedStatus := testhelpers.NewStatusBuilder().
			WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusRamping, 5, true, false).
			WithCurrentVersion("v0", true, true).
			WithName(twdName).
			WithNamespace(testNamespace.Name).
			Build()
		verifyTemporalWorkerDeploymentStatusEventually(t, ctx, env, twd.Name, twd.Namespace, expectedStatus, 60*time.Second, 2*time.Second)

		// Create the TWOR — should create one copy per active build ID (v0 + v1).
		twor := makeHPATWOR(tworName, testNamespace.Name, twd.Name)
		if err := k8sClient.Create(ctx, twor); err != nil {
			t.Fatalf("failed to create TWOR: %v", err)
		}

		hpaNameV0 := k8s.ComputeOwnedResourceName(twd.Name, tworName, buildIDv0)
		hpaNameV1 := k8s.ComputeOwnedResourceName(twd.Name, tworName, buildIDv1)
		depNameV0 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv0)

		// Both per-Build-ID HPA copies should be created.
		waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, testNamespace.Name, hpaNameV0, depNameV0, 30*time.Second)
		waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, testNamespace.Name, hpaNameV1, depNameV1, 30*time.Second)

		// Both build IDs should appear in TWOR status as Applied.
		waitForTWORStatusApplied(t, ctx, k8sClient, testNamespace.Name, tworName, buildIDv0, 30*time.Second)
		waitForTWORStatusApplied(t, ctx, k8sClient, testNamespace.Name, tworName, buildIDv1, 30*time.Second)
	})

	// Test 6: Apply failure → status.Applied:false
	t.Run("twor-apply-failure", func(t *testing.T) {
		ctx := context.Background()
		twdName := "twor-apply-fail-test"
		tworName := "fail-resource"

		twd, _, buildID, stopFuncs := setupTWORTestBase(t, ctx, k8sClient, mgr, ts, testNamespace, twdName)
		defer handleStopFuncs(stopFuncs)

		// Use an unknown GVK that doesn't exist in the envtest API server.
		// The controller's SSA apply will fail because the API server can't find the resource type.
		twor := &temporaliov1alpha1.TemporalWorkerOwnedResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tworName,
				Namespace: testNamespace.Name,
			},
			Spec: temporaliov1alpha1.TemporalWorkerOwnedResourceSpec{
				WorkerRef: temporaliov1alpha1.WorkerDeploymentReference{Name: twd.Name},
				Object: runtime.RawExtension{Raw: []byte(`{
					"apiVersion": "nonexistent.example.com/v1",
					"kind": "FakeResource",
					"spec": {
						"someField": "someValue"
					}
				}`)},
			},
		}
		if err := k8sClient.Create(ctx, twor); err != nil {
			t.Fatalf("failed to create TWOR: %v", err)
		}

		// Wait for the TWOR status to show Applied:false for the build ID.
		waitForTWORStatusNotApplied(t, ctx, k8sClient, testNamespace.Name, tworName, buildID, 30*time.Second)
	})

	// Test 7: SSA idempotency — multiple reconcile loops do not create duplicates or trigger spurious updates
	t.Run("twor-ssa-idempotency", func(t *testing.T) {
		ctx := context.Background()
		twdName := "twor-idempotent-test"
		tworName := "idempotent-hpa"

		twd, _, buildID, stopFuncs := setupTWORTestBase(t, ctx, k8sClient, mgr, ts, testNamespace, twdName)
		defer handleStopFuncs(stopFuncs)

		twor := makeHPATWOR(tworName, testNamespace.Name, twd.Name)
		if err := k8sClient.Create(ctx, twor); err != nil {
			t.Fatalf("failed to create TWOR: %v", err)
		}

		hpaName := k8s.ComputeOwnedResourceName(twd.Name, tworName, buildID)
		depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

		waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, testNamespace.Name, hpaName, depName, 30*time.Second)
		waitForTWORStatusApplied(t, ctx, k8sClient, testNamespace.Name, tworName, buildID, 30*time.Second)

		// Record the HPA's resourceVersion before additional reconcile loops.
		var hpaBefore autoscalingv2.HorizontalPodAutoscaler
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: testNamespace.Name}, &hpaBefore); err != nil {
			t.Fatalf("failed to get HPA: %v", err)
		}
		rvBefore := hpaBefore.ResourceVersion
		ownerRefCountBefore := len(hpaBefore.OwnerReferences)
		t.Logf("HPA resourceVersion before extra reconciles: %s, ownerRefs: %d", rvBefore, ownerRefCountBefore)

		// Trigger extra reconcile loops by touching the TWD annotation.
		// With RECONCILE_INTERVAL=1s, waiting 4s gives ~4 additional reconcile loops.
		var currentTWD temporaliov1alpha1.TemporalWorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: testNamespace.Name}, &currentTWD); err != nil {
			t.Fatalf("failed to get TWD: %v", err)
		}
		patch := client.MergeFrom(currentTWD.DeepCopy())
		if currentTWD.Annotations == nil {
			currentTWD.Annotations = map[string]string{}
		}
		currentTWD.Annotations["test-reconcile-trigger"] = "1"
		if err := k8sClient.Patch(ctx, &currentTWD, patch); err != nil {
			t.Fatalf("failed to patch TWD annotation: %v", err)
		}
		time.Sleep(4 * time.Second)

		// Fetch the HPA again and verify nothing changed.
		var hpaAfter autoscalingv2.HorizontalPodAutoscaler
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: testNamespace.Name}, &hpaAfter); err != nil {
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

		// Verify there is still exactly one HPA with this name.
		var hpaList autoscalingv2.HorizontalPodAutoscalerList
		if err := k8sClient.List(ctx, &hpaList,
			client.InNamespace(testNamespace.Name),
			client.MatchingLabels{k8s.BuildIDLabel: buildID},
		); err != nil {
			t.Fatalf("failed to list HPAs: %v", err)
		}
		if len(hpaList.Items) != 1 {
			t.Errorf("expected exactly 1 HPA with build ID %q, got %d", buildID, len(hpaList.Items))
		}

		t.Log("SSA idempotency confirmed: HPA unchanged after multiple reconcile loops")
	})
}

// setupTWORTestBase creates a TWD + TemporalConnection, waits for the target Deployment,
// starts workers, and waits for VersionStatusCurrent. Returns the TWD, connection, buildID,
// and stop functions for the workers (caller must defer handleStopFuncs(stopFuncs)).
func setupTWORTestBase(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	testNamespace *corev1.Namespace,
	twdName string,
) (*temporaliov1alpha1.TemporalWorkerDeployment, *temporaliov1alpha1.TemporalConnection, string, []func()) {
	t.Helper()

	tc := testhelpers.NewTestCase().
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
		t.Fatalf("failed to create TWD: %v", err)
	}

	waitForExpectedTargetDeployment(t, twd, env, 30*time.Second)

	buildID := k8s.ComputeBuildID(twd)
	depName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)
	stopFuncs := applyDeployment(t, ctx, k8sClient, depName, testNamespace.Name)

	verifyTemporalWorkerDeploymentStatusEventually(t, ctx, env, twd.Name, twd.Namespace, tc.GetExpectedStatus(), 30*time.Second, 5*time.Second)

	return twd, temporalConnection, buildID, stopFuncs
}

// makeHPATWOR constructs a TemporalWorkerOwnedResource with an HPA spec where
// scaleTargetRef is null (triggering auto-injection by the controller).
func makeHPATWOR(name, namespace, workerRefName string) *temporaliov1alpha1.TemporalWorkerOwnedResource {
	return &temporaliov1alpha1.TemporalWorkerOwnedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.TemporalWorkerOwnedResourceSpec{
			WorkerRef: temporaliov1alpha1.WorkerDeploymentReference{Name: workerRefName},
			Object: runtime.RawExtension{Raw: []byte(`{
				"apiVersion": "autoscaling/v2",
				"kind": "HorizontalPodAutoscaler",
				"spec": {
					"scaleTargetRef": null,
					"minReplicas": 2,
					"maxReplicas": 5,
					"metrics": []
				}
			}`)},
		},
	}
}

// assertHPAOwnerRefToDeployment asserts that the named HPA has a controller owner reference
// pointing at the named versioned Deployment (not the TWD).
func assertHPAOwnerRefToDeployment(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, hpaName, expectedDepName string,
) {
	t.Helper()
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: namespace}, &hpa); err != nil {
		t.Fatalf("failed to get HPA %s: %v", hpaName, err)
	}
	for _, ref := range hpa.OwnerReferences {
		if ref.Kind == "Deployment" && ref.Name == expectedDepName && ref.Controller != nil && *ref.Controller {
			t.Logf("HPA %s correctly has controller owner ref to Deployment %s", hpaName, expectedDepName)
			return
		}
	}
	t.Errorf("HPA %s/%s missing controller owner reference to Deployment %s (ownerRefs: %+v)",
		namespace, hpaName, expectedDepName, hpa.OwnerReferences)
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

// waitForTWORStatusNotApplied polls until the TWOR status contains an entry for buildID
// with Applied:false (and optionally a non-empty message indicating the error).
func waitForTWORStatusNotApplied(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, tworName, buildID string,
	timeout time.Duration,
) {
	t.Helper()
	eventually(t, timeout, time.Second, func() error {
		var twor temporaliov1alpha1.TemporalWorkerOwnedResource
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: tworName, Namespace: namespace}, &twor); err != nil {
			return err
		}
		for _, v := range twor.Status.Versions {
			if v.BuildID == buildID {
				if !v.Applied {
					t.Logf("TWOR status correctly shows Applied:false for build ID %q, message: %s", buildID, v.Message)
					return nil
				}
				return fmt.Errorf("TWOR status shows Applied:true for build ID %q, expected false", buildID)
			}
		}
		return fmt.Errorf("TWOR status has no entry for build ID %q (current: %+v)", buildID, twor.Status.Versions)
	})
}
