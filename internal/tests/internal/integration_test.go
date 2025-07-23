//go:build test_dep
// +build test_dep

package internal

import (
	"context"
	"os"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/server/common/dynamicconfig"
	"go.temporal.io/server/temporal"
	"go.temporal.io/server/temporaltest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testPollerHistoryTTL              = time.Second
	testDrainageVisibilityGracePeriod = time.Second
	testDrainageRefreshInterval       = time.Second
)

type testCase struct {
	// If starting from a particular state, specify that in input.Status
	input *temporaliov1alpha1.TemporalWorkerDeployment
	// TemporalWorkerDeploymentStatus only tracks the names of the Deployments for deprecated
	// versions, so for test scenarios that start with existing deprecated version Deployments,
	// specify the number of replicas for each deprecated build here.
	deprecatedBuildReplicas map[string]int32
	deprecatedBuildImages   map[string]string
	expectedStatus          *temporaliov1alpha1.TemporalWorkerDeploymentStatus
}

// TestIntegration runs integration tests for the Temporal Worker Controller
func TestIntegration(t *testing.T) {
	// Set faster reconcile interval for testing
	os.Setenv("RECONCILE_INTERVAL", "1s")

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

	testRampPercentStep1 := float32(5)

	// TODO(carlydf): add a test case that requires pre-reconcile-loop state creation
	// - can we create a drained version? validate that it's scaled down
	// - pollerTTL=0

	tests := map[string]testCase{
		"all-at-once-rollout-2-replicas": {
			input: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("all-at-once-rollout-2-replicas"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateAllAtOnce
				obj.Spec.Template = testhelpers.MakeHelloWorldPodSpec("v1")
				replicas := int32(2)
				obj.Spec.Replicas = &replicas
				obj.Spec.WorkerOptions = temporaliov1alpha1.WorkerOptions{
					TemporalConnection: "all-at-once-rollout-2-replicas",
					TemporalNamespace:  ts.GetDefaultNamespace(),
				}
				obj.ObjectMeta = metav1.ObjectMeta{
					Name:      "all-at-once-rollout-2-replicas",
					Namespace: testNamespace.Name,
					Labels:    map[string]string{"app": "test-worker"},
				}
				return obj
			}),
			deprecatedBuildReplicas: nil,
			expectedStatus: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion:        nil,
				CurrentVersion:       testhelpers.MakeCurrentVersion(testNamespace.Name, "all-at-once-rollout-2-replicas", "v1", true, false),
				RampingVersion:       nil,
				DeprecatedVersions:   nil,
				VersionConflictToken: nil,
				LastModifierIdentity: "",
			},
		},
		"progressive-rollout-expect-first-step": {
			input: testhelpers.ModifyObj(testhelpers.MakeTWDWithName("progressive-rollout-expect-first-step"), func(obj *temporaliov1alpha1.TemporalWorkerDeployment) *temporaliov1alpha1.TemporalWorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = []temporaliov1alpha1.RolloutStep{
					{RampPercentage: 5, PauseDuration: metav1.Duration{Duration: time.Hour}},
				}
				obj.Spec.Template = testhelpers.MakeHelloWorldPodSpec("v1")
				obj.Spec.WorkerOptions = temporaliov1alpha1.WorkerOptions{
					TemporalConnection: "progressive-rollout-expect-first-step",
					TemporalNamespace:  ts.GetDefaultNamespace(),
				}
				obj.ObjectMeta = metav1.ObjectMeta{
					Name:      "progressive-rollout-expect-first-step",
					Namespace: testNamespace.Name,
					Labels:    map[string]string{"app": "test-worker"},
				}
				obj.Status = temporaliov1alpha1.TemporalWorkerDeploymentStatus{
					TargetVersion:        nil,
					CurrentVersion:       testhelpers.MakeCurrentVersion(testNamespace.Name, "progressive-rollout-expect-first-step", "v0", true, true),
					RampingVersion:       nil,
					DeprecatedVersions:   nil,
					VersionConflictToken: nil,
					LastModifierIdentity: "",
				}
				return obj
			}),
			deprecatedBuildReplicas: map[string]int32{testhelpers.MakeBuildId("progressive-rollout-expect-first-step", "v0", nil): 1},
			deprecatedBuildImages:   map[string]string{testhelpers.MakeBuildId("progressive-rollout-expect-first-step", "v0", nil): "v0"},
			expectedStatus: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: testhelpers.MakeVersionId(testNamespace.Name, "progressive-rollout-expect-first-step", "v1"),
						Deployment: &corev1.ObjectReference{
							Namespace: testNamespace.Name,
							Name: k8s.ComputeVersionedDeploymentName(
								"progressive-rollout-expect-first-step",
								testhelpers.MakeBuildId("progressive-rollout-expect-first-step", "v1", nil),
							),
						},
					},
					TestWorkflows:      nil,
					RampPercentage:     &testRampPercentStep1,
					RampingSince:       nil, // not tested (for now at least)
					RampLastModifiedAt: nil, // not tested (for now at least)
				},
				CurrentVersion: nil,
				RampingVersion: &temporaliov1alpha1.TargetWorkerDeploymentVersion{
					BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{
						VersionID: testhelpers.MakeVersionId(testNamespace.Name, "progressive-rollout-expect-first-step", "v1"),
						Deployment: &corev1.ObjectReference{
							Namespace: testNamespace.Name,
							Name: k8s.ComputeVersionedDeploymentName(
								"progressive-rollout-expect-first-step",
								testhelpers.MakeBuildId("progressive-rollout-expect-first-step", "v1", nil),
							),
						},
					},
					TestWorkflows:      nil,
					RampPercentage:     &testRampPercentStep1,
					RampingSince:       nil, // not tested (for now at least)
					RampLastModifiedAt: nil, // not tested (for now at least)
				},
				DeprecatedVersions:   nil,
				VersionConflictToken: nil,
				LastModifierIdentity: "",
			},
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			ctx := context.Background()
			// TODO(carlydf): populate all fields in tc that are set to testName, so that the user does not need to specify
			testTemporalWorkerDeploymentCreation(ctx, t, k8sClient, ts, tc)
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
	// TODO(carlydf): handle Status.LastModifierIdentity
	if cv := twd.Status.CurrentVersion; cv != nil {
		t.Logf("Handling current version %v", cv.VersionID)
		if cv.Status != temporaliov1alpha1.VersionStatusCurrent {
			t.Errorf("Current Version's status must be Current")
		}
		if cv.Deployment != nil {
			t.Logf("Creating Deployment %s for Current Version", cv.Deployment.Name)
			createWorkerDeployment(ctx, t, k8sClient, twd, cv.VersionID, connection.Spec, replicas, images)
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
		}
	}

	if rv := twd.Status.RampingVersion; rv != nil {
		t.Logf("Handling ramping version %v", rv.VersionID)
		if rv.Status != temporaliov1alpha1.VersionStatusRamping {
			t.Errorf("Ramping Version's status must be Ramping")
		}
		if rv.Deployment != nil {
			t.Logf("Creating Deployment %s for Ramping Version", rv.Deployment.Name)
		}
		// TODO(carlydf): do this
	}
}

func createWorkerDeployment(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
	versionID string,
	connection temporaliov1alpha1.TemporalConnectionSpec,
	replicas map[string]int32,
	images map[string]string,
) {
	t.Log("Creating a Deployment")
	_, buildId, err := k8s.SplitVersionID(versionID)
	if err != nil {
		t.Error(err)
	}

	prevImageName := twd.Spec.Template.Spec.Containers[0].Image
	prevReplicas := twd.Spec.Replicas
	// temporarily replace it
	twd.Spec.Template.Spec.Containers[0].Image = images[buildId]
	newReplicas := replicas[buildId]
	twd.Spec.Replicas = &newReplicas
	defer func() {
		twd.Spec.Template.Spec.Containers[0].Image = prevImageName
		twd.Spec.Replicas = prevReplicas
	}()

	dep := k8s.NewDeploymentWithOwnerRef(
		&twd.TypeMeta,
		&twd.ObjectMeta,
		&twd.Spec,
		k8s.ComputeWorkerDeploymentName(twd),
		buildId,
		connection,
	)

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
	tc testCase,
) {
	twd := tc.input
	expectedStatus := tc.expectedStatus

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

	makePreliminaryStatusTrue(ctx, t, k8sClient, ts, twd, temporalConnection, tc.deprecatedBuildReplicas, tc.deprecatedBuildImages)

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
