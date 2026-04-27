package internal

// Tests that deleting a TemporalWorkerDeployment CRD correctly cleans up
// Temporal server-side versioning data and handles edge cases like the
// TemporalConnection being deleted simultaneously by Helm.
//
// Covered:
//   - TWD deletion sets current version to unversioned on Temporal server
//   - TWD deletion removes finalizer from TemporalConnection when no other TWDs reference it
//   - TWD is fully deleted from K8s after cleanup (finalizer removed)
//   - TWD deletion with TemporalConnection deleted simultaneously (Helm race condition) still succeeds

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/api/serviceerror"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const deletionFinalizerName = "temporal.io/delete-protection"

func runDeletionTests(
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	testNamespace string,
) {
	t.Run("deletion-sets-current-to-unversioned", func(t *testing.T) {
		testDeletionSetsCurrentToUnversioned(t, k8sClient, ts, testNamespace)
	})

	t.Run("deletion-removes-connection-finalizer", func(t *testing.T) {
		testDeletionRemovesConnectionFinalizer(t, k8sClient, ts, testNamespace)
	})
}

// testDeletionSetsCurrentToUnversioned verifies the core fix: when a TWD is deleted,
// the controller sets the current version to unversioned so tasks route to unversioned workers,
// and the TWD is fully deleted from K8s.
func testDeletionSetsCurrentToUnversioned(
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	namespace string,
) {
	ctx := context.Background()
	testName := "del-cleanup"

	// Build a TWD using the standard builder pattern
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
		BuildWithValues(testName, namespace, ts.GetDefaultNamespace())

	twd := tc.GetTWD()

	// Create a TemporalConnection
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      twd.Spec.WorkerOptions.TemporalConnectionRef.Name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}

	// Create the TWD
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TWD: %v", err)
	}

	// Wait for the child deployment to be created by the controller
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(twd)
	buildID := k8s.ComputeBuildID(twd)
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, buildID)

	eventually(t, 30*time.Second, time.Second, func() error {
		var dep appsv1.Deployment
		return k8sClient.Get(ctx, types.NamespacedName{
			Name: expectedDeploymentName, Namespace: namespace,
		}, &dep)
	})

	// Start workers so the version registers on the Temporal server
	workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, namespace)
	defer handleStopFuncs(workerStopFuncs)

	// Wait until the version becomes current on the Temporal server
	deploymentHandle := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		resp, err := deploymentHandle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return err
		}
		if resp.Info.RoutingConfig.CurrentVersion == nil {
			return errors.New("current version not set yet")
		}
		return nil
	})
	t.Log("TWD is reconciled with a current version set")

	// Update the TWD to target v2.0, creating a second version to use as a ramping version.
	// This exercises the ramping-version clear path in handleDeletion.
	var twdForUpdate temporaliov1alpha1.TemporalWorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &twdForUpdate); err != nil {
		t.Fatalf("failed to get TWD for v2.0 update: %v", err)
	}
	twdForUpdate.Spec.Template.Spec.Containers[0].Image = "v2.0"
	buildIDv2 := k8s.ComputeBuildID(&twdForUpdate)
	deploymentNameV2 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv2)
	if err := k8sClient.Update(ctx, &twdForUpdate); err != nil {
		t.Fatalf("failed to update TWD to v2.0: %v", err)
	}

	// Wait for the controller to create the v2.0 K8s Deployment
	eventually(t, 30*time.Second, time.Second, func() error {
		var dep appsv1.Deployment
		return k8sClient.Get(ctx, types.NamespacedName{Name: deploymentNameV2, Namespace: namespace}, &dep)
	})

	// Start v2.0 workers so they register with the Temporal server
	workerStopFuncsV2 := applyDeployment(t, ctx, k8sClient, deploymentNameV2, namespace)
	defer handleStopFuncs(workerStopFuncsV2)

	// Wait for v2.0 to appear as a version in the Temporal deployment
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		resp, err := deploymentHandle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return err
		}
		for _, v := range resp.Info.VersionSummaries {
			if v.Version.BuildID == buildIDv2 {
				return nil
			}
		}
		return fmt.Errorf("v2.0 (buildID=%s) not yet visible in Temporal deployment", buildIDv2)
	})

	// Set v2.0 as the ramping version to exercise the clear-ramping path on deletion.
	// The AllAtOnce controller may have already promoted v2.0 to current by the time we get
	// here, so we first revert to v1.0 as current (pushing v2.0 back to inactive) then set
	// v2.0 as ramping.
	descResp, err := deploymentHandle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
	if err != nil {
		t.Fatalf("failed to describe deployment before setting ramping version: %v", err)
	}
	if descResp.Info.RoutingConfig.CurrentVersion != nil && descResp.Info.RoutingConfig.CurrentVersion.BuildID == buildIDv2 {
		// v2.0 is already current; revert to v1.0 so we can set v2.0 as ramping.
		t.Logf("v2.0 is already current; reverting to v1.0 as current before setting v2.0 as ramping")
		if _, err := deploymentHandle.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
			BuildID:                buildID,
			ConflictToken:          descResp.ConflictToken,
			IgnoreMissingTaskQueues: true,
		}); err != nil {
			t.Fatalf("failed to revert current version to v1.0: %v", err)
		}
		descResp, err = deploymentHandle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			t.Fatalf("failed to re-describe deployment after reverting current version: %v", err)
		}
	}
	if _, err := deploymentHandle.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
		BuildID:       buildIDv2,
		Percentage:    50,
		ConflictToken: descResp.ConflictToken,
		Identity:      "test",
	}); err != nil {
		t.Fatalf("failed to set v2.0 as ramping version: %v", err)
	}
	t.Logf("Set v2.0 (buildID=%s) as ramping version at 50%%", buildIDv2)

	// Verify the TWD has our finalizer
	var twdBeforeDelete temporaliov1alpha1.TemporalWorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &twdBeforeDelete); err != nil {
		t.Fatalf("failed to get TWD: %v", err)
	}
	hasFinalizer := false
	for _, f := range twdBeforeDelete.Finalizers {
		if f == deletionFinalizerName {
			hasFinalizer = true
			break
		}
	}
	if !hasFinalizer {
		t.Fatalf("TWD does not have expected finalizer %q", deletionFinalizerName)
	}

	// Delete the TWD
	t.Log("Deleting the TemporalWorkerDeployment")
	if err := k8sClient.Delete(ctx, &twdBeforeDelete); err != nil {
		t.Fatalf("failed to delete TWD: %v", err)
	}

	// Verify the TWD is eventually deleted (finalizer ran and was removed)
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		var check temporaliov1alpha1.TemporalWorkerDeployment
		err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &check)
		if err != nil {
			return nil // not found = deleted
		}
		return errors.New("TWD still exists, finalizer may not have completed")
	})
	t.Log("TWD deleted successfully (finalizer completed)")

	// Verify Temporal server-side state: current version should be unversioned
	resp, err := deploymentHandle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
	if err != nil {
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) {
			t.Log("Worker Deployment was fully deleted from Temporal server")
			return
		}
		t.Fatalf("failed to describe worker deployment after deletion: %v", err)
	}

	if resp.Info.RoutingConfig.CurrentVersion != nil {
		t.Errorf("expected current version to be nil (unversioned) after TWD deletion, got buildID=%q",
			resp.Info.RoutingConfig.CurrentVersion.BuildID)
	} else {
		t.Log("Verified: current version is unversioned after TWD deletion")
	}

	if resp.Info.RoutingConfig.RampingVersion != nil {
		t.Errorf("expected ramping version to be nil after TWD deletion, got buildID=%q",
			resp.Info.RoutingConfig.RampingVersion.BuildID)
	} else {
		t.Log("Verified: ramping version is cleared after TWD deletion")
	}
}

// testDeletionRemovesConnectionFinalizer verifies that when a TWD is deleted,
// the controller removes its finalizer from the TemporalConnection, allowing
// the connection to be deleted by K8s. This tests the Helm race condition fix.
func testDeletionRemovesConnectionFinalizer(
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	namespace string,
) {
	ctx := context.Background()
	testName := "del-conn-finalizer"

	// Build a TWD with manual strategy (simpler, no need to reach current version)
	tc := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewTemporalWorkerDeploymentBuilder().
				WithManualStrategy().
				WithTargetTemplate("v1.0"),
		).
		WithExpectedStatus(
			testhelpers.NewStatusBuilder().
				WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
		).
		BuildWithValues(testName, namespace, ts.GetDefaultNamespace())

	twd := tc.GetTWD()

	// Create a TemporalConnection
	temporalConnection := &temporaliov1alpha1.TemporalConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      twd.Spec.WorkerOptions.TemporalConnectionRef.Name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create TemporalConnection: %v", err)
	}

	// Create the TWD
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TWD: %v", err)
	}

	// Wait for the finalizer to be added to both TWD and TemporalConnection
	eventually(t, 30*time.Second, time.Second, func() error {
		var check temporaliov1alpha1.TemporalWorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &check); err != nil {
			return err
		}
		for _, f := range check.Finalizers {
			if f == deletionFinalizerName {
				return nil
			}
		}
		return fmt.Errorf("TWD finalizer %q not yet added", deletionFinalizerName)
	})

	eventually(t, 30*time.Second, time.Second, func() error {
		var check temporaliov1alpha1.TemporalConnection
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: temporalConnection.Name, Namespace: namespace}, &check); err != nil {
			return err
		}
		for _, f := range check.Finalizers {
			if f == deletionFinalizerName {
				return nil
			}
		}
		return fmt.Errorf("TemporalConnection finalizer %q not yet added", deletionFinalizerName)
	})
	t.Log("Both finalizers are in place")

	// Simulate Helm deleting both resources simultaneously by deleting the
	// TemporalConnection first, then the TWD. The connection should be blocked
	// by the finalizer until the TWD cleanup removes it.
	if err := k8sClient.Delete(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to delete TemporalConnection: %v", err)
	}
	t.Log("TemporalConnection deletion requested (blocked by finalizer)")

	// Verify the connection is NOT yet deleted (finalizer holds it)
	var connCheck temporaliov1alpha1.TemporalConnection
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: temporalConnection.Name, Namespace: namespace}, &connCheck); err != nil {
		t.Fatalf("TemporalConnection should still exist (held by finalizer), but got: %v", err)
	}
	if connCheck.DeletionTimestamp.IsZero() {
		t.Fatal("TemporalConnection should have DeletionTimestamp set")
	}
	t.Log("Verified: TemporalConnection is in Terminating state (held by finalizer)")

	// Now delete the TWD
	var latestTwd temporaliov1alpha1.TemporalWorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &latestTwd); err != nil {
		t.Fatalf("failed to get TWD: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latestTwd); err != nil {
		t.Fatalf("failed to delete TWD: %v", err)
	}

	// Verify the TWD is eventually deleted
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		var check temporaliov1alpha1.TemporalWorkerDeployment
		err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &check)
		if err != nil {
			return nil // deleted
		}
		return errors.New("TWD still exists")
	})
	t.Log("TWD deleted successfully")

	// Verify the TemporalConnection is also eventually deleted
	// (controller removed the finalizer during TWD cleanup, K8s can now delete it)
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		var check temporaliov1alpha1.TemporalConnection
		err := k8sClient.Get(ctx, types.NamespacedName{Name: temporalConnection.Name, Namespace: namespace}, &check)
		if err != nil {
			return nil // deleted
		}
		return errors.New("TemporalConnection still exists after TWD cleanup")
	})
	t.Log("TemporalConnection deleted successfully (finalizer was removed by TWD cleanup)")
}
