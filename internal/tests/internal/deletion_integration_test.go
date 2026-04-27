package internal

// Tests that deleting a WorkerDeployment CRD correctly cleans up
// Temporal server-side versioning data and handles edge cases like the
// Connection being deleted simultaneously by Helm.
//
// Covered:
//   - WD deletion sets current version to unversioned on Temporal server
//   - WD deletion removes finalizer from Connection when no other WDs reference it
//   - WD is fully deleted from K8s after cleanup (finalizer removed)
//   - WD deletion with Connection deleted simultaneously (Helm race condition) still succeeds

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

// testDeletionSetsCurrentToUnversioned verifies the core fix: when a WD is deleted,
// the controller sets the current version to unversioned so tasks route to unversioned workers,
// and the WD is fully deleted from K8s.
func testDeletionSetsCurrentToUnversioned(
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	namespace string,
) {
	ctx := context.Background()
	testName := "del-cleanup"

	// Use Manual strategy so the controller does not race to promote v2.0 to current
	// while the test is setting it up as a ramping version.
	tc := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewWorkerDeploymentBuilder().
				WithManualStrategy().
				WithTargetTemplate("v1.0"),
		).
		BuildWithValues(testName, namespace, ts.GetDefaultNamespace())

	WD := tc.GetTWD()

	// Create a Connection
	Connection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WD.Spec.WorkerOptions.ConnectionRef.Name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.ConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, Connection); err != nil {
		t.Fatalf("failed to create Connection: %v", err)
	}

	// Create the WD
	if err := k8sClient.Create(ctx, WD); err != nil {
		t.Fatalf("failed to create WD: %v", err)
	}

	// Wait for the child deployment to be created by the controller
	workerDeploymentName := k8s.ComputeWorkerDeploymentName(WD)
	buildID := k8s.ComputeBuildID(WD)
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(WD.Name, buildID)

	eventually(t, 30*time.Second, time.Second, func() error {
		var dep appsv1.Deployment
		return k8sClient.Get(ctx, types.NamespacedName{
			Name: expectedDeploymentName, Namespace: namespace,
		}, &dep)
	})

	// Start workers so the version registers on the Temporal server
	workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, namespace)
	defer handleStopFuncs(workerStopFuncs)

	// Manual strategy does not auto-promote, so set v1.0 as current explicitly.
	// setCurrentVersion uses defaults.ControllerIdentity so handleDeletion can later
	// update versioning state without a ManagerIdentity mismatch.
	deploymentHandle := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildID)
	t.Log("v1.0 set as current version")

	// Update the WD to target v2.0, creating a second version to use as a ramping version.
	// This exercises the ramping-version clear path in handleDeletion.
	var WDForUpdate temporaliov1alpha1.WorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: WD.Name, Namespace: namespace}, &WDForUpdate); err != nil {
		t.Fatalf("failed to get WD for v2.0 update: %v", err)
	}
	WDForUpdate.Spec.Template.Spec.Containers[0].Image = "v2.0"
	buildIDv2 := k8s.ComputeBuildID(&WDForUpdate)
	deploymentNameV2 := k8s.ComputeVersionedDeploymentName(WD.Name, buildIDv2)
	if err := k8sClient.Update(ctx, &WDForUpdate); err != nil {
		t.Fatalf("failed to update WD to v2.0: %v", err)
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
	setRampingVersion(t, ctx, ts, workerDeploymentName, buildIDv2, 50)
	t.Logf("Set v2.0 (buildID=%s) as ramping version at 50%%", buildIDv2)

	// Verify the WD has our finalizer
	var WDBeforeDelete temporaliov1alpha1.WorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: WD.Name, Namespace: namespace}, &WDBeforeDelete); err != nil {
		t.Fatalf("failed to get WD: %v", err)
	}
	hasFinalizer := false
	for _, f := range WDBeforeDelete.Finalizers {
		if f == deletionFinalizerName {
			hasFinalizer = true
			break
		}
	}
	if !hasFinalizer {
		t.Fatalf("WD does not have expected finalizer %q", deletionFinalizerName)
	}

	// Delete the WD
	t.Log("Deleting the WorkerDeployment")
	if err := k8sClient.Delete(ctx, &WDBeforeDelete); err != nil {
		t.Fatalf("failed to delete WD: %v", err)
	}

	// Verify the WD is eventually deleted (finalizer ran and was removed)
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		var check temporaliov1alpha1.WorkerDeployment
		err := k8sClient.Get(ctx, types.NamespacedName{Name: WD.Name, Namespace: namespace}, &check)
		if err != nil {
			return nil // not found = deleted
		}
		return errors.New("WD still exists, finalizer may not have completed")
	})
	t.Log("WD deleted successfully (finalizer completed)")

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
		t.Errorf("expected current version to be nil (unversioned) after WD deletion, got buildID=%q",
			resp.Info.RoutingConfig.CurrentVersion.BuildID)
	} else {
		t.Log("Verified: current version is unversioned after WD deletion")
	}

	if resp.Info.RoutingConfig.RampingVersion != nil {
		t.Errorf("expected ramping version to be nil after WD deletion, got buildID=%q",
			resp.Info.RoutingConfig.RampingVersion.BuildID)
	} else {
		t.Log("Verified: ramping version is cleared after WD deletion")
	}
}

// testDeletionRemovesConnectionFinalizer verifies that when a WD is deleted,
// the controller removes its finalizer from the Connection, allowing
// the connection to be deleted by K8s. This tests the Helm race condition fix.
func testDeletionRemovesConnectionFinalizer(
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	namespace string,
) {
	ctx := context.Background()
	testName := "del-conn-finalizer"

	// Build a WD with manual strategy (simpler, no need to reach current version)
	tc := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewWorkerDeploymentBuilder().
				WithManualStrategy().
				WithTargetTemplate("v1.0"),
		).
		WithExpectedStatus(
			testhelpers.NewStatusBuilder().
				WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
		).
		BuildWithValues(testName, namespace, ts.GetDefaultNamespace())

	WD := tc.GetTWD()

	// Create a Connection
	Connection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WD.Spec.WorkerOptions.ConnectionRef.Name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.ConnectionSpec{
			HostPort: ts.GetFrontendHostPort(),
		},
	}
	if err := k8sClient.Create(ctx, Connection); err != nil {
		t.Fatalf("failed to create Connection: %v", err)
	}

	// Create the WD
	if err := k8sClient.Create(ctx, WD); err != nil {
		t.Fatalf("failed to create WD: %v", err)
	}

	// Wait for the finalizer to be added to both WD and Connection
	eventually(t, 30*time.Second, time.Second, func() error {
		var check temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: WD.Name, Namespace: namespace}, &check); err != nil {
			return err
		}
		for _, f := range check.Finalizers {
			if f == deletionFinalizerName {
				return nil
			}
		}
		return fmt.Errorf("WD finalizer %q not yet added", deletionFinalizerName)
	})

	eventually(t, 30*time.Second, time.Second, func() error {
		var check temporaliov1alpha1.Connection
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: Connection.Name, Namespace: namespace}, &check); err != nil {
			return err
		}
		for _, f := range check.Finalizers {
			if f == deletionFinalizerName {
				return nil
			}
		}
		return fmt.Errorf("Connection finalizer %q not yet added", deletionFinalizerName)
	})
	t.Log("Both finalizers are in place")

	// Simulate Helm deleting both resources simultaneously by deleting the
	// Connection first, then the WD. The connection should be blocked
	// by the finalizer until the WD cleanup removes it.
	if err := k8sClient.Delete(ctx, Connection); err != nil {
		t.Fatalf("failed to delete Connection: %v", err)
	}
	t.Log("Connection deletion requested (blocked by finalizer)")

	// Verify the connection is NOT yet deleted (finalizer holds it)
	var connCheck temporaliov1alpha1.Connection
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: Connection.Name, Namespace: namespace}, &connCheck); err != nil {
		t.Fatalf("Connection should still exist (held by finalizer), but got: %v", err)
	}
	if connCheck.DeletionTimestamp.IsZero() {
		t.Fatal("Connection should have DeletionTimestamp set")
	}
	t.Log("Verified: Connection is in Terminating state (held by finalizer)")

	// Now delete the WD
	var latestWD temporaliov1alpha1.WorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: WD.Name, Namespace: namespace}, &latestWD); err != nil {
		t.Fatalf("failed to get WD: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latestWD); err != nil {
		t.Fatalf("failed to delete WD: %v", err)
	}

	// Verify the WD is eventually deleted
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		var check temporaliov1alpha1.WorkerDeployment
		err := k8sClient.Get(ctx, types.NamespacedName{Name: WD.Name, Namespace: namespace}, &check)
		if err != nil {
			return nil // deleted
		}
		return errors.New("WD still exists")
	})
	t.Log("WD deleted successfully")

	// Verify the Connection is also eventually deleted
	// (controller removed the finalizer during WD cleanup, K8s can now delete it)
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		var check temporaliov1alpha1.Connection
		err := k8sClient.Get(ctx, types.NamespacedName{Name: Connection.Name, Namespace: namespace}, &check)
		if err != nil {
			return nil // deleted
		}
		return errors.New("Connection still exists after WD cleanup")
	})
	t.Log("Connection deleted successfully (finalizer was removed by WD cleanup)")
}
