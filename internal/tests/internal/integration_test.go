package internal

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/common/worker_versioning"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type testCase struct {
	name    string
	builder *testhelpers.TestCaseBuilder
}

// TestIntegration runs integration tests for the Temporal Worker Controller
func TestIntegration(t *testing.T) {
	// Set up test environment
	cfg, k8sClient, mgr, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Create test Temporal server and client
	dc := dynamicconfig.NewMemoryClient()
	// make versions drain faster
	dc.OverrideValue("matching.wv.VersionDrainageStatusVisibilityGracePeriod", testDrainageVisibilityGracePeriod)
	dc.OverrideValue("matching.wv.VersionDrainageStatusRefreshInterval", testDrainageRefreshInterval)
	dc.OverrideValue("matching.maxVersionsInDeployment", testMaxVersionsInDeployment)
	ts := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dc)),
	)

	// These tests are categorized by the three different rollout strategies the controller supports.

	// Manual strategy tests
	manualStrategyTestCases := []testCase{
		{
			name: "manual-rollout-expect-no-change",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						// Image name with banned characters (slash, colon) that will be replaced
						// with hyphens in the build ID. Dots and underscores are preserved.
						WithTargetTemplate("my.app/test/foo_bar:v1.0"),
				).
				WithWaitTime(5 * time.Second). // wait before checking to confirm no change
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("my.app/test/foo_bar:v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
				),
		},
		{
			name: "manual-rollout-with-failed-gate-expect-no-current",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						WithGate(false).
						WithReplicas(2).
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
				),
		},
		{
			name: "manual-rollout-with-success-gate-expect-no-current",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						WithGate(true).
						WithReplicas(2).
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
				),
		},
		{
			name: "manual-rollout-expect-deployments-scaled-up",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						WithReplicas(2).
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
				),
		},
		{
			name: "manual-rollout-scale-down-deprecated-versions",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						WithTargetTemplate("v2.0").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, true).
								WithDeprecatedVersions(
									testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
								),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1.0", 1),
				).
				WithWaitTime(5*time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v2.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false).
						WithDeprecatedVersions(
							testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v1.0", temporaliov1alpha1.VersionStatusInactive, true, false, true),
						),
				).
				WithExpectedDeployments( // note: right now this is only checked for deprecated versions, TODO(carlydf) add for non-deprecated too
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1.0", 0),
					testhelpers.NewDeploymentInfo("v2.0", 1),
				),
		},
		{
			name: "manual-rollout-yes-unversioned-pollers-expect-no-current",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						WithTargetTemplate("v1"),
				).
				WithSetupFunction(setupUnversionedPollers).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
				),
		},
		{
			name: "manual-rollout-blocked-at-max-versions-ineligible-for-deletion",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
						WithTargetTemplate("v5").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v4", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
								WithCurrentVersion("v4", true, true).
								WithDeprecatedVersions( // drained AND has no pollers -> eligible for deletion
									testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, true, true),
								),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
				).
				WithWaitTime(5*time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder(). // controller won't deploy v5, so it's not registered
						WithTargetVersion("v5", temporaliov1alpha1.VersionStatusNotRegistered, -1, false, false).
						WithCurrentVersion("v4", true, false).
						WithDeprecatedVersions( // drained but has pollers, so ineligible for deletion
							testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, false, true),
						),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
					testhelpers.NewDeploymentInfo("v5", 1),
				),
		},
		{
			name: "manual-rollout-blocked-by-modifier",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
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
				WithWaitTime(5 * time.Second).
				WithSetupFunction(setUnversionedCurrent).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false).
						WithCurrentVersion(worker_versioning.UnversionedVersionId, false, false).
						WithDeprecatedVersions(testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true)),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
				).
				WithValidatorFunction(validateIgnoreLastModifierMetadata(false)),
		},
		{
			name: "manual-rollout-unblocked-by-modifier-with-ignore",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithManualStrategy().
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
				WithSetupFunction(setCurrentAndSetIgnoreModifierMetadata).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false). // manual strategy, so controller should not promote v1 to current despite being unblocked
						WithCurrentVersion("v0", true, false),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
				).
				WithValidatorFunction(validateIgnoreLastModifierMetadata(true)), // Since this is a manual strategy, the current version at the end of the test is v0 which has the ignore last modifier set to true
		},
	}

	for _, tc := range manualStrategyTestCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts, tc.builder.BuildWithValues(tc.name, testNamespace.Name, ts.GetDefaultNamespace()))
		})
	}

	// All-at-once strategy tests
	allAtOnceStrategyTestCases := []testCase{
		{
			name: "all-at-once-with-success-gate-expect-current",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithGate(true).
						WithReplicas(2).
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v1.0", true, false),
				),
		},
		{
			name: "all-at-once-rollout-with-failed-gate-expect-no-current",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithGate(false).
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
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false),
				),
		},
		{
			name: "all-at-once-expect-scale-down-deprecated-versions",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v2.0").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, true).
								WithDeprecatedVersions(
									testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
								),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1.0", 1),
				).
				WithWaitTime(5*time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v2.0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithDeprecatedVersions(
							testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v1.0", temporaliov1alpha1.VersionStatusInactive, true, false, true),
						),
				).
				WithExpectedDeployments( // note: right now this is only checked for deprecated versions, TODO(carlydf) add for non-deprecated too
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1.0", 0),
					testhelpers.NewDeploymentInfo("v2.0", 1),
				),
		},
		{
			name: "all-at-once-yes-unversioned-pollers-expect-current",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v1"),
				).
				WithSetupFunction(setupUnversionedPollers).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusCurrent, -1, true, false),
				),
		},
		{
			name: "all-at-once-blocked-at-max-versions-ineligible-for-deletion",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v5").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v4", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
								WithCurrentVersion("v4", true, true).
								WithDeprecatedVersions( // drained AND has no pollers -> eligible for deletion
									testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, true, true),
								),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
				).
				WithWaitTime(5*time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder(). // controller won't deploy v5, so it's not registered
						WithTargetVersion("v5", temporaliov1alpha1.VersionStatusNotRegistered, -1, false, false).
						WithCurrentVersion("v4", true, false).
						WithDeprecatedVersions( // drained but has pollers, so ineligible for deletion
							testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, false, true),
						),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
					testhelpers.NewDeploymentInfo("v5", 1),
				),
		},
		{
			name: "all-at-once-rollout-blocked-by-modifier",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
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
				WithWaitTime(5 * time.Second).
				WithSetupFunction(setUnversionedCurrent).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false).
						WithCurrentVersion(worker_versioning.UnversionedVersionId, false, false).
						WithDeprecatedVersions(testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true)),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
				).
				WithValidatorFunction(validateIgnoreLastModifierMetadata(false)),
		},
		{
			name: "all-at-once-unblocked-by-modifier-with-ignore",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
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
				WithSetupFunction(setCurrentAndSetIgnoreModifierMetadata).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v1", true, false).
						WithDeprecatedVersions(testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true)),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
				).
				WithValidatorFunction(validateIgnoreLastModifierMetadata(false)),
		},
	}

	for _, tc := range allAtOnceStrategyTestCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts, tc.builder.BuildWithValues(tc.name, testNamespace.Name, ts.GetDefaultNamespace()))
		})
	}

	progressiveStrategyTestCases := []testCase{
		{
			name: "progressive-rollout-with-gate-no-current-expect-all-at-once",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(10, time.Hour)).
						WithReplicas(2).
						WithGate(true).
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v1.0", true, false),
				),
		},
		{
			name: "progressive-rollout-no-unversioned-pollers-expect-all-at-once",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
						WithTargetTemplate("v1.0"),
				).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusCurrent, -1, true, false).
						WithCurrentVersion("v1.0", true, false),
				),
		},
		{
			name: "progressive-rollout-yes-unversioned-pollers-expect-first-step",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
						WithTargetTemplate("v1"),
				).
				WithSetupFunction(setupUnversionedPollers).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusRamping, 5, true, false),
				),
		},
		{
			name: "progressive-rollout-expect-first-step",
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
				),
		},
		{
			name: "progressive-rollout-success-gate-expect-first-step",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
						WithGate(true).
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
				),
		},
		{
			name: "progressive-rollout-with-failed-gate",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
						WithGate(false).
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
				WithWaitTime(5 * time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1.0", temporaliov1alpha1.VersionStatusInactive, -1, true, false).
						WithCurrentVersion("v0", true, true),
				),
		},
		{
			name: "progressive-rollout-blocked-at-max-versions-ineligible-for-deletion",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
						WithTargetTemplate("v5").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v4", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
								WithCurrentVersion("v4", true, true).
								WithDeprecatedVersions( // drained AND has no pollers -> eligible for deletion
									testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, true, true),
								),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
				).
				WithWaitTime(5*time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder(). // controller won't deploy v5, so it's not registered
						WithTargetVersion("v5", temporaliov1alpha1.VersionStatusNotRegistered, -1, false, false).
						WithCurrentVersion("v4", true, false).
						WithDeprecatedVersions( // drained but has pollers, so ineligible for deletion
							testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, false, true),
						),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
					testhelpers.NewDeploymentInfo("v5", 1),
				),
		},
		{
			name: "progressive-rollout-blocked-by-modifier",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
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
				WithWaitTime(5 * time.Second).
				WithSetupFunction(setUnversionedCurrent).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusInactive, -1, true, false).
						WithCurrentVersion(worker_versioning.UnversionedVersionId, false, false).
						WithDeprecatedVersions(testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true)),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
				).
				WithValidatorFunction(validateIgnoreLastModifierMetadata(false)),
		},
		{
			name: "progressive-rollout-unblocked-by-modifier-with-ignore",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
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
				WithSetupFunction(setCurrentAndSetIgnoreModifierMetadata).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v1", temporaliov1alpha1.VersionStatusRamping, 5, true, false).
						WithCurrentVersion("v0", true, false),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 1),
				).
				WithValidatorFunction(validateIgnoreLastModifierMetadata(false)),
		},
	}

	for _, tc := range progressiveStrategyTestCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, ts, tc.builder.BuildWithValues(tc.name, testNamespace.Name, ts.GetDefaultNamespace()))
		})
	}

	// Create short TTL test Temporal server and client
	dcShortTTL := dynamicconfig.NewMemoryClient()
	// make versions eligible for deletion faster
	dcShortTTL.OverrideValue("matching.PollerHistoryTTL", testShortPollerHistoryTTL) // default is 5 minutes
	// make versions drain faster
	dcShortTTL.OverrideValue("matching.wv.VersionDrainageStatusVisibilityGracePeriod", testDrainageVisibilityGracePeriod)
	dcShortTTL.OverrideValue("matching.wv.VersionDrainageStatusRefreshInterval", testDrainageRefreshInterval)
	dcShortTTL.OverrideValue("matching.maxVersionsInDeployment", testMaxVersionsInDeployment)
	tsShortTTL := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dcShortTTL)),
	)
	testsShortPollerTTL := []testCase{
		// Note: Add tests that require pollers to expire quickly here
		{
			name: "6th-rollout-unblocked-after-pollers-die-versions-eligible-for-deletion",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v5").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v4", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
								WithCurrentVersion("v4", true, true).
								WithDeprecatedVersions( // drained AND has no pollers -> eligible for deletion
									testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, true, true),
								),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 0), // 0 replicas -> no pollers
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
				).
				WithWaitTime(5*time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v5", temporaliov1alpha1.VersionStatusCurrent, -1, false, false).
						WithCurrentVersion("v5", true, false).
						WithDeprecatedVersions( // drained AND has pollers -> eligible for deletion
							testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v4", temporaliov1alpha1.VersionStatusDrained, true, false, true),
						),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 0), // 0 replicas -> no pollers
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
					testhelpers.NewDeploymentInfo("v5", 1),
				),
		},
		{
			name: "7th-rollout-unblocked-after-pollers-die-version-deleted",
			builder: testhelpers.NewTestCase().
				WithInput(
					testhelpers.NewTemporalWorkerDeploymentBuilder().
						WithAllAtOnceStrategy().
						WithTargetTemplate("v6").
						WithStatus(
							testhelpers.NewStatusBuilder().
								WithTargetVersion("v5", temporaliov1alpha1.VersionStatusCurrent, -1, true, true).
								WithCurrentVersion("v5", true, true).
								WithDeprecatedVersions( // drained AND has no pollers -> eligible for deletion
									testhelpers.NewDeprecatedVersionInfo("v0", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, true, true),
									testhelpers.NewDeprecatedVersionInfo("v4", temporaliov1alpha1.VersionStatusDrained, true, true, true),
								),
						),
				).
				WithExistingDeployments(
					testhelpers.NewDeploymentInfo("v0", 0), // 0 replicas -> no pollers
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
					testhelpers.NewDeploymentInfo("v5", 1),
				).
				WithWaitTime(5*time.Second).
				WithExpectedStatus(
					testhelpers.NewStatusBuilder().
						WithTargetVersion("v6", temporaliov1alpha1.VersionStatusCurrent, -1, false, false).
						WithCurrentVersion("v6", true, false).
						WithDeprecatedVersions( // drained AND has pollers -> eligible for deletion
							testhelpers.NewDeprecatedVersionInfo("v1", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v2", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v3", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v4", temporaliov1alpha1.VersionStatusDrained, true, false, true),
							testhelpers.NewDeprecatedVersionInfo("v5", temporaliov1alpha1.VersionStatusDrained, true, false, true),
						),
				).
				WithExpectedDeployments(
					testhelpers.NewDeploymentInfo("v0", 0), // 0 replicas -> no pollers
					testhelpers.NewDeploymentInfo("v1", 1),
					testhelpers.NewDeploymentInfo("v2", 1),
					testhelpers.NewDeploymentInfo("v3", 1),
					testhelpers.NewDeploymentInfo("v4", 1),
					testhelpers.NewDeploymentInfo("v5", 1),
					testhelpers.NewDeploymentInfo("v6", 1),
				),
		},
	}

	for _, tc := range testsShortPollerTTL {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, mgr, tsShortTTL, tc.builder.BuildWithValues(tc.name, testNamespace.Name, tsShortTTL.GetDefaultNamespace()))
		})
	}

}

// testTemporalWorkerDeploymentCreation tests the creation of a TemporalWorkerDeployment and waits for the expected status
func testTemporalWorkerDeploymentCreation(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	mgr manager.Manager,
	ts *temporaltest.TestServer,
	tc testhelpers.TestCase,
) {
	twd := tc.GetTWD()
	expectedStatus := tc.GetExpectedStatus()

	t.Log("Creating a TemporalConnection")
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
		ExpectedDeploymentReplicas: tc.GetExpectedDeploymentReplicas(),
	}

	makePreliminaryStatusTrue(ctx, t, env, twd)

	// verify that temporal state matches the preliminary status, to confirm that makePreliminaryStatusTrue worked
	verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, twd.Status, 30*time.Second, 5*time.Second)

	// apply post-status setup function
	if f := tc.GetSetupFunc(); f != nil {
		f(t, ctx, tc, env)
	}

	t.Log("Creating a TemporalWorkerDeployment")
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TemporalWorkerDeployment: %v", err)
	}

	t.Log("Waiting for the controller to reconcile")
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))

	// only wait for and create the deployment if it is expected
	if expectedStatus.TargetVersion.Status != temporaliov1alpha1.VersionStatusNotRegistered {
		waitForDeployment(t, k8sClient, expectedDeploymentName, twd.Namespace, 30*time.Second)
		workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, twd.Namespace)
		defer handleStopFuncs(workerStopFuncs)
	}

	if wait := tc.GetWaitTime(); wait != nil {
		time.Sleep(*wait)
	}
	verifyTemporalWorkerDeploymentStatusEventually(t, ctx, env, twd.Name, twd.Namespace, expectedStatus, 30*time.Second, 5*time.Second)
	verifyTemporalStateMatchesStatusEventually(t, ctx, ts, twd, *expectedStatus, 30*time.Second, 5*time.Second)

	// apply post-expected-status validation function
	if f := tc.GetValidatorFunc(); f != nil {
		tc.GetValidatorFunc()(t, ctx, tc, env)
	}
}
