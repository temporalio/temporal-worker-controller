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

type testEnv struct {
	k8sClient  client.Client
	ts         *temporaltest.TestServer
	connection *temporaliov1alpha1.TemporalConnection
	replicas   map[string]int32
	images     map[string]string
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
	env testEnv,
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
) {
	t.Logf("Creating starting test env based on input.Status")

	// Make a separate list of deferred functions, because calling defer in a for loop is not allowed.
	loopDefers := make([]func(), 0)
	defer handleStopFuncs(loopDefers)
	for _, dv := range twd.Status.DeprecatedVersions {
		t.Logf("Setting up deprecated version %v with status %v", dv.VersionID, dv.Status)
		workerStopFuncs := createStatus(ctx, t, env, twd, dv.BaseWorkerDeploymentVersion, nil)
		loopDefers = append(loopDefers, func() { handleStopFuncs(workerStopFuncs) })
	}

	if tv := twd.Status.TargetVersion; tv.VersionID != "" {
		t.Logf("Setting up target version %v with status %v", tv.VersionID, tv.Status)
		workerStopFuncs := createStatus(ctx, t, env, twd, tv.BaseWorkerDeploymentVersion, tv.RampPercentage)
		defer handleStopFuncs(workerStopFuncs)
	}
}

func handleStopFuncs(funcs []func()) {
	for _, f := range funcs {
		if f != nil {
			f()
		}
	}
}

// creates k8s deployment, pollers, and routing config state as needed.
func createStatus(
	ctx context.Context,
	t *testing.T,
	env testEnv,
	newTWD *temporaliov1alpha1.TemporalWorkerDeployment,
	prevVersion temporaliov1alpha1.BaseWorkerDeploymentVersion,
	rampPercentage *float32,
) (workerStopFuncs []func()) {
	if prevVersion.Deployment != nil && prevVersion.Deployment.FieldPath == "create" {
		v := getVersion(t, prevVersion.VersionID)
		prevTWD := recreateTWD(newTWD, env.images[v.BuildId], env.replicas[v.BuildId])
		createWorkerDeployment(ctx, t, env, prevTWD, v.BuildId)
		expectedDeploymentName := k8s.ComputeVersionedDeploymentName(prevTWD.Name, k8s.ComputeBuildID(prevTWD))
		waitForDeployment(t, env.k8sClient, expectedDeploymentName, prevTWD.Namespace, 30*time.Second)
		if prevVersion.Status != temporaliov1alpha1.VersionStatusNotRegistered {
			workerStopFuncs = applyDeployment(t, ctx, env.k8sClient, expectedDeploymentName, prevTWD.Namespace)
		}

		switch prevVersion.Status {
		case temporaliov1alpha1.VersionStatusInactive, temporaliov1alpha1.VersionStatusNotRegistered:
			// no-op
		case temporaliov1alpha1.VersionStatusRamping:
			setRampingVersion(t, ctx, env.ts, v, *rampPercentage) // rampPercentage won't be nil if the version is ramping
		case temporaliov1alpha1.VersionStatusCurrent:
			setCurrentVersion(t, ctx, env.ts, v)
		case temporaliov1alpha1.VersionStatusDraining:
			setRampingVersion(t, ctx, env.ts, v, 1)
			// TODO(carlydf): start a workflow on v that does not complete -> will never drain
			setRampingVersion(t, ctx, env.ts, nil, 0)
		case temporaliov1alpha1.VersionStatusDrained:
			setRampingVersion(t, ctx, env.ts, v, 1)
			setRampingVersion(t, ctx, env.ts, nil, 0)
		}
	}

	return workerStopFuncs
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
	env testEnv,
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
	buildId string,
) {
	dep := k8s.NewDeploymentWithOwnerRef(
		&twd.TypeMeta,
		&twd.ObjectMeta,
		&twd.Spec,
		k8s.ComputeWorkerDeploymentName(twd),
		buildId,
		env.connection.Spec,
	)
	t.Logf("Creating Deployment %s in namespace %s", dep.Name, dep.Namespace)

	if err := env.k8sClient.Create(ctx, dep); err != nil {
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

	env := testEnv{
		k8sClient:  k8sClient,
		ts:         ts,
		connection: temporalConnection,
		replicas:   tc.GetDeprecatedBuildReplicas(),
		images:     tc.GetDeprecatedBuildImages(),
	}

	makePreliminaryStatusTrue(ctx, t, env, twd)

	t.Log("Creating a TemporalWorkerDeployment")
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create TemporalWorkerDeployment: %v", err)
	}

	t.Log("Waiting for the controller to reconcile")
	expectedDeploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))
	waitForDeployment(t, k8sClient, expectedDeploymentName, twd.Namespace, 30*time.Second)
	workerStopFuncs := applyDeployment(t, ctx, k8sClient, expectedDeploymentName, twd.Namespace)
	defer handleStopFuncs(workerStopFuncs)

	verifyTemporalWorkerDeploymentStatusEventually(t, ctx, k8sClient, twd.Name, twd.Namespace, expectedStatus, 60*time.Second, 10*time.Second)
}
