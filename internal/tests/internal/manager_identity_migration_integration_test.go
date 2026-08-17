package internal

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	temporalClient "go.temporal.io/sdk/client"
)

// simulateIdentitySuffixUpgrade simulates a helm upgrade that changes the controller's
// identity suffix (the ns-UID -> SA-UID change). It claims the Worker Deployment's manager
// identity as the controller's pre-upgrade identity (prefix + legacySuffix), then sets the
// running reconciler's new primary suffix while retaining the previous suffix as the legacy
// suffix, exactly as the upgraded main() does.
func simulateIdentitySuffixUpgrade(legacySuffix, newSuffix string) func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
	return func(t *testing.T, ctx context.Context, tc testhelpers.TestCase, env testhelpers.TestEnv) {
		legacyIdentity := testControllerIdentityPrefix + "/" + legacySuffix
		workerDeploymentName := k8s.ComputeWorkerDeploymentName(tc.GetTWD())
		c, err := temporalClient.Dial(temporalClient.Options{
			HostPort:  env.Ts.GetFrontendHostPort(),
			Namespace: env.Ts.GetDefaultNamespace(),
			Identity:  legacyIdentity,
		})
		if err != nil {
			t.Fatalf("failed to dial temporal with identity %q: %v", legacyIdentity, err)
		}
		defer c.Close()

		if _, err := c.WorkerDeploymentClient().GetHandle(workerDeploymentName).SetManagerIdentity(ctx,
			temporalClient.WorkerDeploymentSetManagerIdentityOptions{Self: true}); err != nil {
			t.Fatalf("failed to claim manager identity %q: %v", legacyIdentity, err)
		}
		t.Logf("claimed manager identity as pre-upgrade identity %q", legacyIdentity)

		// Upgraded controller: new SA-UID primary suffix, previous ns-UID suffix retained as
		// the legacy suffix so the deployment can be re-claimed instead of deadlocking.
		t.Setenv(controller.IdentitySuffixEnvKey, newSuffix)
		t.Setenv(controller.LegacyIdentitySuffixEnvKey, legacySuffix)
		t.Logf("upgraded identity suffix %q -> %q (legacy suffix retained)", legacySuffix, newSuffix)
	}
}

// TestManagerIdentitySuffixMigration exercises a helm upgrade that changes the controller
// identity suffix, as the namespace-UID -> ServiceAccount-UID change does.
//
// A Worker Deployment already managed under the pre-upgrade identity should remain
// manageable after the suffix changes: the controller must be able to promote a new
// version. Without a migration path the controller does not re-claim the deployment and
// Temporal blocks routing changes from the new identity, so v1 is never promoted to
// current (the manager-identity deadlock raised in review).
//
// This test asserts the desired post-migration behaviour: after the suffix change the
// controller re-claims the deployment under its new identity and promotes v1 to current.
func TestManagerIdentitySuffixMigration(t *testing.T) {
	cfg, k8sClient, mgr, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	dc := dynamicconfig.NewMemoryClient()
	dc.OverrideValue(dynamicconfig.MakeKey("matching.wv.VersionDrainageStatusVisibilityGracePeriod"), testDrainageVisibilityGracePeriod)
	dc.OverrideValue(dynamicconfig.MakeKey("matching.wv.VersionDrainageStatusRefreshInterval"), testDrainageRefreshInterval)
	dc.OverrideValue(dynamicconfig.MakeKey("matching.maxVersionsInDeployment"), testMaxVersionsInDeployment)
	dc.OverrideValue(dynamicconfig.MakeKey("history.enableVersionReactivationSignals"), false)
	ts := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dc)),
	)

	// Before the upgrade the controller identity is testControllerIdentity
	// ("test-controller-identity/123"). Simulate an upgrade that changes the suffix.
	const newSuffix = "sa-uid"
	newIdentity := testControllerIdentityPrefix + "/" + newSuffix

	ctx := context.Background()
	builder := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewWorkerDeploymentBuilder().
				WithAllAtOnceStrategy().
				WithGate(true).
				WithTargetTemplate("v1").
				WithStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v0", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
						WithCurrentVersion("v0", true, true),
				),
		).
		WithExistingDeployments(
			testhelpers.NewDeploymentInfo("v0", 1),
		).
		WithSetupFunction(simulateIdentitySuffixUpgrade(testControllerIdentitySuffix, newSuffix)).
		WithWaitTime(5 * time.Second).
		// Desired: the controller re-claims under its new identity and promotes v1 to current,
		// deprecating the previously-current v0.
		WithExpectedStatus(
			testhelpers.NewStatusBuilder().
				WithTargetVersion("v1", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
				WithCurrentVersion("v1", true, false).
				WithDeprecatedVersions(
					testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
				),
		).
		WithExpectedDeployments(
			testhelpers.NewDeploymentInfo("v0", 1),
		).
		// The deployment is re-claimed under the post-upgrade identity.
		WithValidatorFunction(validateManagerIdentity(newIdentity))

	testWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts,
		builder.BuildWithValues("manager-identity-suffix-change", testNamespace.Name, ts.GetDefaultNamespace()))
}
