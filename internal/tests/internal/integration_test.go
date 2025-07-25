package internal

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/api/deployment/v1"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testPollerHistoryTTL              = time.Second
	testDrainageVisibilityGracePeriod = time.Second
	testDrainageRefreshInterval       = time.Second
)

// TestIntegration runs integration tests for the Temporal Worker Controller
func TestIntegration(t *testing.T) {
	// Set up test environment
	cfg, k8sClient, _, _, cleanup := setupTestEnvironment(t)
	defer cleanup()

	// Create test namespace
	testNamespace := createTestNamespace(t, k8sClient)
	defer cleanupTestNamespace(t, cfg, k8sClient, testNamespace)

	// Create test Temporal server and client
	dc := dynamicconfig.NewMemoryClient()
	// make versions eligible for deletion faster
	dc.OverrideValue("matching.PollerHistoryTTL", testPollerHistoryTTL)
	// make versions drain faster
	dc.OverrideValue("matching.wv.VersionDrainageStatusVisibilityGracePeriod", testDrainageVisibilityGracePeriod)
	dc.OverrideValue("matching.wv.VersionDrainageStatusRefreshInterval", testDrainageRefreshInterval)
	ts := temporaltest.NewServer(
		temporaltest.WithT(t),
		temporaltest.WithBaseServerOptions(temporal.WithDynamicConfigClient(dc)),
	)

	tests := map[string]*testhelpers.TestCaseBuilder{
		"all-at-once-rollout-2-replicas": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithAllAtOnceStrategy().
					WithReplicas(2).
					WithVersion("v1"),
			).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", -1, true, false).
					WithCurrentVersion("v1", true, false),
			),
		"progressive-rollout-expect-first-step": testhelpers.NewTestCase().
			WithInput(
				testhelpers.NewTemporalWorkerDeploymentBuilder().
					WithProgressiveStrategy(testhelpers.ProgressiveStep(5, time.Hour)).
					WithVersion("v1").
					WithTargetVersionStatus("v0", -1, true, true).
					WithCurrentVersionStatus("v0", true, true),
			).
			WithDeprecatedBuilds(
				testhelpers.NewDeprecatedVersionInfo("v0", 1),
			).
			WithExpectedStatus(
				testhelpers.NewStatusBuilder().
					WithTargetVersion("v1", 5, true, true),
			),
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			ctx := context.Background()
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, ts, tc.BuildWithValues(testName, testNamespace.Name, ts.GetDefaultNamespace()))
		})

	}

}

// Uses input.Status + deprecatedBuildReplicas to create (and maybe kill) pollers for deprecated versions in temporal
// also gets routing config of the deployment into the starting state before running the test.
// Does not set Status.VersionConflictToken, since that is only set internally by the server.
func makePreliminaryStatusTrue(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
	connection *temporaliov1alpha1.TemporalConnection,
	replicas map[string]int32,
	images map[string]string,
) {
	t.Logf("Creating starting test env based on input.Status")
	for _, dv := range twd.Status.DeprecatedVersions {
		t.Logf("Handling deprecated version %v", dv.VersionID)
		switch dv.Status {
		case temporaliov1alpha1.VersionStatusInactive:
			// start a poller -- is this included in deprecated versions list?
		case temporaliov1alpha1.VersionStatusRamping, temporaliov1alpha1.VersionStatusCurrent:
			// these won't be in deprecated versions
		case temporaliov1alpha1.VersionStatusDraining:
			// TODO(carlydf): start a poller, set ramp, start a wf on that version, then unset
		case temporaliov1alpha1.VersionStatusDrained:
			// TODO(carlydf): start a poller, set ramp, unset, wait for drainage status visibility grace period
		case temporaliov1alpha1.VersionStatusNotRegistered:
			// no-op, although I think this won't occur in deprecated versions either
		}
	}
	if tv := twd.Status.TargetVersion; tv.VersionID != "" {
		switch tv.Status {
		case temporaliov1alpha1.VersionStatusInactive:
		case temporaliov1alpha1.VersionStatusRamping:
		case temporaliov1alpha1.VersionStatusCurrent:
			t.Logf("Setting up current version %v", tv.VersionID)
			if tv.Deployment != nil && tv.Deployment.FieldPath == "create" {
				v := getVersion(t, tv.VersionID)
				cvTWD := recreateTWD(twd, images[v.BuildId], replicas[v.BuildId])
				createWorkerDeployment(ctx, t, k8sClient, cvTWD, v.BuildId, connection.Spec)
				expectedDeploymentName := k8s.ComputeVersionedDeploymentName(cvTWD.Name, k8s.ComputeBuildID(cvTWD))
				waitForDeployment(t, k8sClient, expectedDeploymentName, cvTWD.Namespace, 30*time.Second)
				workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, cvTWD.Namespace)
				defer func() {
					for _, f := range workerStopFuncs {
						if f != nil {
							f()
						}
					}
				}()
				setCurrentVersion(t, ctx, ts, v)
			}
		case temporaliov1alpha1.VersionStatusDraining:
		case temporaliov1alpha1.VersionStatusDrained:
		case temporaliov1alpha1.VersionStatusNotRegistered:
		}
	}
}

// Helper to handle unlikely error caused by invalid string split.
func getVersion(t *testing.T, versionId string) *deployment.WorkerDeploymentVersion {
	deploymentName, buildId, err := k8s.SplitVersionID(versionId)
	if err != nil {
		t.Error(err)
	}
	return &deployment.WorkerDeploymentVersion{
		DeploymentName: deploymentName,
		BuildId:        buildId,
	}
}

// recreateTWD returns a copy of the given TWD, but replaces the build-id-generating image name with the given one,
// and the Spec.Replicas with the given replica count.
// Panics if the twd spec is nil, or if it has no containers, but that should never be true for these integration tests.
func recreateTWD(twd *temporaliov1alpha1.TemporalWorkerDeployment, imageName string, replicas int32) *temporaliov1alpha1.TemporalWorkerDeployment {
	ret := twd.DeepCopy()
	ret.Spec.Template.Spec.Containers[0].Image = imageName
	ret.Spec.Replicas = &replicas
	return ret
}

func createWorkerDeployment(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
	buildId string,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) {
	dep := k8s.NewDeploymentWithOwnerRef(
		&twd.TypeMeta,
		&twd.ObjectMeta,
		&twd.Spec,
		k8s.ComputeWorkerDeploymentName(twd),
		buildId,
		connection,
	)
	t.Logf("Creating Deployment %s in namespace %s", dep.Name, dep.Namespace)

	if err := k8sClient.Create(ctx, dep); err != nil {
		t.Fatalf("failed to create Deployment: %v", err)
	}
}

// testTemporalWorkerDeploymentCreation tests the creation of a TemporalWorkerDeployment and waits for the expected status
func testTemporalWorkerDeploymentCreation(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	ts *temporaltest.TestServer,
	tc testhelpers.TestCase,
) {
	twd := tc.GetTWD()
	expectedStatus := tc.GetExpectedStatus()

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

	makePreliminaryStatusTrue(ctx, t, k8sClient, ts, twd, temporalConnection, tc.GetDeprecatedBuildReplicas(), tc.GetDeprecatedBuildImages())

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
