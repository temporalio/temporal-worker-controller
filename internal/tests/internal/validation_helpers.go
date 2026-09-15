package internal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	EmptyTargetVersion = temporaliov1alpha1.TargetWorkerDeploymentVersion{}
)

// waitForExpectedTargetDeployment waits for a deployment to be created
func waitForExpectedTargetDeployment(t *testing.T, twd *temporaliov1alpha1.WorkerDeployment, env testhelpers.TestEnv, timeout time.Duration) {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	deploymentName := k8s.ComputeVersionedDeploymentName(twd.Name, k8s.ComputeBuildID(twd))
	namespace := twd.Namespace
	ticks := 0

	for time.Now().Before(deadline) {
		var deployment appsv1.Deployment
		if err := env.K8sClient.Get(ctx, types.NamespacedName{
			Name:      deploymentName,
			Namespace: namespace,
		}, &deployment); err == nil {
			t.Logf("Found deployment %s in namespace %s", deployment.Name, namespace)
			expectedBuildID := k8s.ComputeBuildID(twd)
			expectedDeployment, err := k8s.NewDeploymentWithControllerRef(twd, expectedBuildID, env.Connection.Spec, env.Mgr.GetScheme())
			if err != nil {
				t.Fatalf("failed to create expected deployment: %v", err)
			}
			if !deploymentsEqual(*expectedDeployment, deployment) {
				t.Logf("deployment %s in namespace %s does not match expected deployment", deployment.Name, namespace)
			}
			t.Logf("Deployment %s with image '%s' matches expected deployment", deployment.Name, deployment.Spec.Template.Spec.Containers[0].Image)
			return
		}
		time.Sleep(1 * time.Second)
		// Cheap k8s-only snapshot while blocked, so a failure shows whether the
		// gating state was evolving or frozen. Diagnostic only (#542).
		if ticks++; ticks%10 == 0 {
			logRolloutDiagnostics(t, ctx, env, twd, fmt.Sprintf("waiting-%ds", ticks), false)
		}
	}
	logRolloutDiagnostics(t, ctx, env, twd, "timeout", true)
	postMortemDrainageWatch(t, ctx, env, twd, deploymentName, postMortemBudget, postMortemInterval)
	t.Fatalf("failed to wait for deployment: timeout waiting for deployment %s in namespace %s", deploymentName, namespace)
}

// deploymentsEqual returns true if the two deployments are equal in the fields we care about.
// Not doing a full hash comparison because I don't want to deal with timestamps.
// Only checks containers and images for now. Other pod spec changes are tested in unit tests.
func deploymentsEqual(expected, actual appsv1.Deployment) bool {
	if expected.Spec.MinReadySeconds != actual.Spec.MinReadySeconds {
		return false
	}
	expectedPodSpec := expected.Spec.Template.Spec
	actualPodSpec := actual.Spec.Template.Spec
	if len(expectedPodSpec.Containers) != len(actualPodSpec.Containers) {
		return false
	}
	for i := range expectedPodSpec.Containers {
		if expectedPodSpec.Containers[i].Name != actualPodSpec.Containers[i].Name {
			return false
		}
		if expectedPodSpec.Containers[i].Image != actualPodSpec.Containers[i].Image {
			return false
		}
		if len(expectedPodSpec.Containers[i].Env) != len(actualPodSpec.Containers[i].Env) {
			return false
		}
		for j := range expectedPodSpec.Containers[i].Env {
			if expectedPodSpec.Containers[i].Env[j].Name != actualPodSpec.Containers[i].Env[j].Name {
				return false
			}
			if expectedPodSpec.Containers[i].Env[j].Value != actualPodSpec.Containers[i].Env[j].Value {
				return false
			}
		}
	}
	return true
}

func waitForVersionRegistrationInDeployment(
	t *testing.T,
	ctx context.Context,
	ts *temporaltest.TestServer,
	version *worker.WorkerDeploymentVersion) {

	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(version.DeploymentName)

	eventually(t, 60*time.Second, time.Second, func() error {
		resp, err := deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return fmt.Errorf("unable to describe worker deployment %s: %w", version.DeploymentName, err)
		}
		for _, vs := range resp.Info.VersionSummaries {
			if vs.Version.DeploymentName == version.DeploymentName && vs.Version.BuildID == version.BuildID {
				return nil
			}
		}
		return fmt.Errorf("could not find version with build %s in worker deployment %s", version.BuildID, version.DeploymentName)
	})
	return
}

func setCurrentVersion(
	t *testing.T,
	ctx context.Context,
	ts *temporaltest.TestServer,
	workerDeploymentName, buildID string,
) {
	if buildID != "" {
		waitForVersionRegistrationInDeployment(t, ctx, ts, &worker.WorkerDeploymentVersion{
			DeploymentName: workerDeploymentName,
			BuildID:        buildID,
		})
	}
	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)
	eventually(t, 60*time.Second, time.Second, func() error {
		_, err := deploymentHandler.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
			BuildID:  buildID,
			Identity: testControllerIdentity,
		})
		if err != nil {
			return fmt.Errorf("unable to set build '%s' as current of worker deployment %s: %w", buildID, workerDeploymentName, err)
		}
		return nil
	})
	return
}

func setRampingVersion(
	t *testing.T,
	ctx context.Context,
	ts *temporaltest.TestServer,
	workerDeploymentName, buildID string,
	rampPercentage float32,
) {
	if buildID != "" {
		waitForVersionRegistrationInDeployment(t, ctx, ts, &worker.WorkerDeploymentVersion{
			DeploymentName: workerDeploymentName,
			BuildID:        buildID,
		})
	}
	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)
	eventually(t, 30*time.Second, time.Second, func() error {
		_, err := deploymentHandler.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
			BuildID:    buildID,
			Percentage: rampPercentage,
			Identity:   testControllerIdentity,
		})
		if err != nil {
			return fmt.Errorf("unable to set build '%s' as ramping of worker deployment %s: %w", buildID, workerDeploymentName, err)
		}
		return nil
	})
	return
}

func verifyTemporalStateMatchesStatusEventually(
	t *testing.T,
	ctx context.Context,
	ts *temporaltest.TestServer,
	twd *temporaliov1alpha1.WorkerDeployment,
	expectedDeploymentStatus temporaliov1alpha1.WorkerDeploymentStatus,
	timeout time.Duration,
	interval time.Duration,
) {
	if twd == nil {
		t.Fatalf("WorkerDeployment cannot be nil")
	}
	if expectedDeploymentStatus.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered ||
		expectedDeploymentStatus.TargetVersion.Status == "" {
		return // this is the first rollout, no Worker Deployment in temporal to describe
	}
	deploymentName := k8s.ComputeWorkerDeploymentName(twd)
	deploymentClient := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName)

	eventually(t, timeout, interval, func() error {
		resp, err := deploymentClient.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return fmt.Errorf("error describing worker deployment %s: %w (target version status: %v)", deploymentName, err, expectedDeploymentStatus.TargetVersion.Status)
		}
		rc := resp.Info.RoutingConfig

		if cv := expectedDeploymentStatus.CurrentVersion; cv != nil {
			if rc.CurrentVersion == nil {
				return errors.New("expected CurrentVersion to be set")
			}
			if rc.CurrentVersion.BuildID != expectedDeploymentStatus.CurrentVersion.BuildID {
				return fmt.Errorf("expected current build id to be '%s', got '%s'",
					expectedDeploymentStatus.CurrentVersion.BuildID,
					rc.CurrentVersion.BuildID)
			}
		}
		if tv := expectedDeploymentStatus.TargetVersion; tv.BuildID != "" {
			switch tv.Status {
			case temporaliov1alpha1.VersionStatusNotRegistered:
				for _, vs := range resp.Info.VersionSummaries {
					if vs.Version.BuildID == tv.BuildID {
						return fmt.Errorf("expected build id '%s' to not be registered, but found it", tv.BuildID)
					}
				}
			case temporaliov1alpha1.VersionStatusRamping:
				if rc.RampingVersion == nil {
					return fmt.Errorf("expected build id '%s' to be Ramping, but was nil was ramping instead", tv.BuildID)
				} else {
					if rc.RampingVersion.BuildID != tv.BuildID {
						return fmt.Errorf("expected build id '%s' to be Ramping, but was '%s' was ramping instead", tv.BuildID, rc.RampingVersion.BuildID)
					}
				}
				if tv.RampPercentage == nil {
					if rc.RampingVersionPercentage != 0 {
						return fmt.Errorf("expected RampPercentage to be nil, but was %v", rc.RampingVersionPercentage)
					}
				} else {
					expectedPercentage := *tv.RampPercentage
					if rc.RampingVersionPercentage != expectedPercentage {
						return fmt.Errorf("expected RampPercentage to be (%.2f%%), but temporal percentage was %.2f%%",
							expectedPercentage, rc.RampingVersionPercentage)
					}
				}
			case temporaliov1alpha1.VersionStatusCurrent:
				if rc.CurrentVersion == nil {
					return fmt.Errorf("expected build id '%s' to be Current, but was nil was current instead", tv.BuildID)
				} else {
					if rc.CurrentVersion.BuildID != tv.BuildID {
						return fmt.Errorf("expected build id '%s' to be Current, but was '%s' was Current instead", tv.BuildID, rc.CurrentVersion.BuildID)
					}
				}
			case temporaliov1alpha1.VersionStatusInactive, temporaliov1alpha1.VersionStatusDraining, temporaliov1alpha1.VersionStatusDrained:
				if rc.CurrentVersion != nil && rc.CurrentVersion.BuildID == tv.BuildID {
					return fmt.Errorf("expected build id '%s' to be %v, but was Current", tv.BuildID, tv.Status)
				}
				if rc.RampingVersion != nil && rc.RampingVersion.BuildID == tv.BuildID {
					return fmt.Errorf("expected build id '%s' to be %v, but was Ramping", tv.BuildID, tv.Status)
				}
				found := false
				for _, vs := range resp.Info.VersionSummaries {
					if vs.Version.BuildID == tv.BuildID {
						found = true
						switch tv.Status {
						case temporaliov1alpha1.VersionStatusInactive:
							if vs.DrainageStatus != sdkclient.WorkerDeploymentVersionDrainageStatusUnspecified {
								return fmt.Errorf("expected build id '%s' to be %v, but was %v", tv.BuildID, tv.Status, vs.DrainageStatus)
							}
						case temporaliov1alpha1.VersionStatusDraining:
							if vs.DrainageStatus != sdkclient.WorkerDeploymentVersionDrainageStatusDraining {
								return fmt.Errorf("expected build id '%s' to be %v, but was %v", tv.BuildID, tv.Status, vs.DrainageStatus)
							}
						case temporaliov1alpha1.VersionStatusDrained:
							if vs.DrainageStatus != sdkclient.WorkerDeploymentVersionDrainageStatusDrained {
								return fmt.Errorf("expected build id '%s' to be %v, but was %v", tv.BuildID, tv.Status, vs.DrainageStatus)
							}
						}
					}
				}
				if !found {
					return fmt.Errorf("expected build id '%s' to be %v, but was NotRegistered", tv.BuildID, tv.Status)
				}
			}
		}
		return nil // All assertions passed!
	})
}

// TODO(carlydf): check version task queues and reduce code repetition
func verifyWorkerDeploymentStatusEventually(
	t *testing.T,
	ctx context.Context,
	env testhelpers.TestEnv,
	twdName,
	namespace string,
	expectedDeploymentStatus *temporaliov1alpha1.WorkerDeploymentStatus,
	timeout time.Duration,
	interval time.Duration,
) {
	if expectedDeploymentStatus == nil {
		t.Fatalf("expected deployment status cannot be nil")
	}
	eventually(t, timeout, interval, func() error {
		var twd temporaliov1alpha1.WorkerDeployment
		if err := env.K8sClient.Get(ctx, types.NamespacedName{
			Name:      twdName,
			Namespace: namespace,
		}, &twd); err != nil {
			return fmt.Errorf("failed to get updated worker deployment: %v", err)
		}
		// validate current version
		if expectedDeploymentStatus.CurrentVersion != nil {
			if twd.Status.CurrentVersion == nil {
				return errors.New("expected CurrentVersion to be set")
			}
			if twd.Status.CurrentVersion.BuildID != expectedDeploymentStatus.CurrentVersion.BuildID {
				return fmt.Errorf("expected current build id to be '%s', got '%s'",
					expectedDeploymentStatus.CurrentVersion.BuildID,
					twd.Status.CurrentVersion.BuildID)
			}
			if twd.Status.CurrentVersion.Deployment == nil {
				return errors.New("expected CurrentVersion.Deployment to be set")
			}
			if twd.Status.CurrentVersion.Deployment.Name != expectedDeploymentStatus.CurrentVersion.Deployment.Name {
				return fmt.Errorf("expected deployment name to be '%s', got '%s'",
					expectedDeploymentStatus.CurrentVersion.Deployment.Name,
					twd.Status.CurrentVersion.Deployment.Name)
			}
		}
		// validate target version
		if expectedDeploymentStatus.TargetVersion.BuildID != "" {
			if twd.Status.TargetVersion.BuildID != expectedDeploymentStatus.TargetVersion.BuildID {
				return fmt.Errorf("expected target build id to be '%s', got '%s'",
					expectedDeploymentStatus.TargetVersion.BuildID,
					twd.Status.TargetVersion.BuildID)
			}
			if twd.Status.TargetVersion.Status != expectedDeploymentStatus.TargetVersion.Status {
				return fmt.Errorf("expected target version status to be '%s', got '%s'",
					expectedDeploymentStatus.TargetVersion.Status,
					twd.Status.TargetVersion.Status)
			}
			if expectedDeploymentStatus.TargetVersion.RampPercentage != nil {
				if twd.Status.TargetVersion.RampPercentage == nil {
					return fmt.Errorf("expected ramp percentage to be '%v', got nil",
						*expectedDeploymentStatus.TargetVersion.RampPercentage)
				}
				if *twd.Status.TargetVersion.RampPercentage != *expectedDeploymentStatus.TargetVersion.RampPercentage {
					return fmt.Errorf("expected ramp percentage to be '%v', got '%v'",
						*expectedDeploymentStatus.TargetVersion.RampPercentage,
						*twd.Status.TargetVersion.RampPercentage)
				}
			} else {
				if twd.Status.TargetVersion.RampPercentage != nil {
					return fmt.Errorf("expected ramp percentage to be nil, got '%v'",
						*twd.Status.TargetVersion.RampPercentage)
				}
			}
			waitForExpectedTargetDeployment(t, &twd, env, 30*time.Second)
		}
		// validate deprecated version(s)
		if len(expectedDeploymentStatus.DeprecatedVersions) != len(twd.Status.DeprecatedVersions) {
			return fmt.Errorf("expected deprecated versions count to be '%v', got '%v'",
				len(expectedDeploymentStatus.DeprecatedVersions), len(twd.Status.DeprecatedVersions))
		}
		for _, expectedDV := range expectedDeploymentStatus.DeprecatedVersions {
			found := false
			for _, actualDV := range twd.Status.DeprecatedVersions {
				if expectedDV.BuildID == actualDV.BuildID {
					found = true
					if err := validateDeprecatedVersion(ctx, env, expectedDV, actualDV); err != nil {
						return fmt.Errorf("expected deprecated version did not match actual: %w", err)
					}
				}
			}
			if !found {
				return fmt.Errorf("expected to find deprecated build '%s', but did not find it", expectedDV.BuildID)
			}
		}
		for _, actualDV := range twd.Status.DeprecatedVersions {
			found := false
			for _, expectedDV := range expectedDeploymentStatus.DeprecatedVersions {
				if expectedDV.BuildID == actualDV.BuildID {
					found = true
					if err := validateDeprecatedVersion(ctx, env, expectedDV, actualDV); err != nil {
						return fmt.Errorf("expected deprecated version did not match actual: %w", err)
					}
				}
			}
			if !found {
				return fmt.Errorf("did not expect to find actual build '%s', but did find it", actualDV.BuildID)
			}
		}
		return nil // All assertions passed!
	})
}

func validateDeprecatedVersion(ctx context.Context, env testhelpers.TestEnv, expectedDV, actualDV *temporaliov1alpha1.DeprecatedWorkerDeploymentVersion) error {
	// status
	if expectedDV.Status != actualDV.Status {
		return fmt.Errorf("expected status of deprecated build '%s' to be '%v', got '%v'",
			expectedDV.BuildID, expectedDV.Status, actualDV.Status)
	}
	// deployment
	if expectedDV.Deployment == nil {
		if actualDV.Deployment != nil {
			return fmt.Errorf("expected Deployment for deprecated build '%s' to be nil, but was %v",
				expectedDV.BuildID, *actualDV.Deployment)
		}
	} else {
		if expectedDV.Deployment == nil {
			return fmt.Errorf("expected Deployment for deprecated build '%s' to be %v, but was nil",
				expectedDV.BuildID, *expectedDV.Deployment)
		}
		if expectedDV.Deployment.Name != actualDV.Deployment.Name {
			return fmt.Errorf("expected Deployment for deprecated build '%s' to be named '%s, but was '%s'",
				expectedDV.BuildID, expectedDV.Deployment.Name, actualDV.Deployment.Name)
		}
		var deployment appsv1.Deployment
		if err := env.K8sClient.Get(ctx, types.NamespacedName{
			Name:      expectedDV.Deployment.Name,
			Namespace: expectedDV.Deployment.Namespace,
		}, &deployment); err != nil {
			return fmt.Errorf("error getting expected Deployment: %w", err)
		}
		if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas != env.ExpectedDeploymentReplicas[expectedDV.BuildID] {
			return fmt.Errorf("expected Deployment for build '%s' to have %v replicas, but had %v",
				expectedDV.BuildID, env.ExpectedDeploymentReplicas[expectedDV.BuildID], *deployment.Spec.Replicas)
		}
	}
	// drainage status
	if (expectedDV.DrainedSince == nil) != (actualDV.DrainedSince == nil) { // TODO: test actual time values someday
		return fmt.Errorf("expected DrainedSince for deprecated build '%s' to be %v, but was %v",
			expectedDV.BuildID, expectedDV.DrainedSince, actualDV.DrainedSince)
	}
	return nil
}

// waitForCondition polls until the named condition on the TWD matches the expected
// status and reason, or fatals on timeout.
func waitForCondition(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	twdName, namespace, condType string,
	expectedStatus metav1.ConditionStatus,
	expectedReason string,
	timeout, interval time.Duration,
) {
	t.Helper()
	eventually(t, timeout, interval, func() error {
		var twd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twdName, Namespace: namespace}, &twd); err != nil {
			return fmt.Errorf("failed to get TWD: %w", err)
		}
		for _, c := range twd.Status.Conditions {
			if c.Type == condType {
				if c.Status != expectedStatus {
					return fmt.Errorf("condition %q: expected status %q, got %q", condType, expectedStatus, c.Status)
				}
				if c.Reason != expectedReason {
					return fmt.Errorf("condition %q: expected reason %q, got %q", condType, expectedReason, c.Reason)
				}
				return nil
			}
		}
		return fmt.Errorf("condition %q not found on TWD %s/%s", condType, namespace, twdName)
	})
}

// waitForEvent polls until at least one Kubernetes Event for the named TWD exists
// with the given reason, or fatals on timeout.
func waitForEvent(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	twdName, namespace, reason string,
	timeout, interval time.Duration,
) {
	t.Helper()
	eventually(t, timeout, interval, func() error {
		var eventList corev1.EventList
		if err := k8sClient.List(ctx, &eventList, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("failed to list events: %w", err)
		}
		for _, e := range eventList.Items {
			if e.InvolvedObject.Name == twdName && e.Reason == reason {
				return nil
			}
		}
		return fmt.Errorf("no event with reason %q found for TWD %s/%s", reason, namespace, twdName)
	})
}

// diagOnFailure ...
var diagOnFailure func(label string)

func eventually(t *testing.T, timeout, interval time.Duration, check func() error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := check(); err == nil {
			return // Success!
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	if lastErr != nil {
		if diagOnFailure != nil {
			diagOnFailure("eventually-failed")
		}
		t.Fatalf("eventually failed after %s: %v", timeout, lastErr)
	}
}

// waitForOwnedHPAWithInjectedScaleTargetRef polls until the named HPA exists in namespace and
// verifies that the controller auto-injected scaleTargetRef to point at expectedDeploymentName.
func waitForOwnedHPAWithInjectedScaleTargetRef(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, hpaName, expectedDeploymentName string,
	timeout time.Duration,
) {
	t.Helper()
	t.Logf("Waiting for HPA %q to be created in namespace %q", hpaName, namespace)
	var hpa autoscalingv2.HorizontalPodAutoscaler
	eventually(t, timeout, time.Second, func() error {
		return k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: namespace}, &hpa)
	})
	if hpa.Spec.ScaleTargetRef.Name != expectedDeploymentName {
		t.Errorf("HPA scaleTargetRef.name = %q, want %q", hpa.Spec.ScaleTargetRef.Name, expectedDeploymentName)
	}
	if hpa.Spec.ScaleTargetRef.Kind != "Deployment" {
		t.Errorf("HPA scaleTargetRef.kind = %q, want %q", hpa.Spec.ScaleTargetRef.Kind, "Deployment")
	}
	if hpa.Spec.ScaleTargetRef.APIVersion != "apps/v1" {
		t.Errorf("HPA scaleTargetRef.apiVersion = %q, want %q", hpa.Spec.ScaleTargetRef.APIVersion, "apps/v1")
	}
	t.Logf("HPA scaleTargetRef correctly injected: %s/%s %s",
		hpa.Spec.ScaleTargetRef.APIVersion, hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name)
}

// waitForWRTStatusApplied polls until WRT.Status.Versions contains an entry for buildID
// with a non-zero LastAppliedGeneration (meaning at least one successful apply has occurred).
func waitForWRTStatusApplied(
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
			if v.BuildID == buildID && v.LastAppliedGeneration > 0 {
				return nil
			}
		}
		return fmt.Errorf("WRT status not yet updated for build ID %q (current versions: %+v)", buildID, wrt.Status.Versions)
	})
	t.Log("WRT status shows LastAppliedGeneration > 0 for build ID")
}

// assertWRTControllerOwnerRef asserts that the named WRT has a controller owner reference
// pointing to the WorkerDeployment named twdName.
func assertWRTControllerOwnerRef(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
	namespace, wrtName, twdName string,
) {
	t.Helper()
	var wrt temporaliov1alpha1.WorkerResourceTemplate
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: wrtName, Namespace: namespace}, &wrt); err != nil {
		t.Fatalf("failed to re-fetch WRT: %v", err)
	}
	for _, ref := range wrt.OwnerReferences {
		if ref.Kind == "WorkerDeployment" && ref.Name == twdName && ref.Controller != nil && *ref.Controller {
			t.Logf("WRT correctly has controller owner reference to TWD %q", twdName)
			return
		}
	}
	t.Errorf("WRT %s/%s missing controller owner reference to TWD %s (refs: %+v)",
		namespace, wrtName, twdName, wrt.OwnerReferences)
}

// logRolloutDiagnostics dumps the state
func logRolloutDiagnostics(
	t *testing.T,
	ctx context.Context,
	env testhelpers.TestEnv,
	twd *temporaliov1alpha1.WorkerDeployment,
	label string,
	describeTemporal bool,
) {
	t.Helper()

	if describeTemporal {
		wdName := k8s.ComputeWorkerDeploymentName(twd)
		handle := env.Ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(wdName)
		resp, err := handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			t.Logf("DIAG[%s] temporal describe %s failed: %v", label, wdName, err)
		} else {
			rc := resp.Info.RoutingConfig
			cur, ramp := "<none>", "<none>"
			if rc.CurrentVersion != nil {
				cur = rc.CurrentVersion.BuildID
			}
			if rc.RampingVersion != nil {
				ramp = rc.RampingVersion.BuildID
			}
			t.Logf("DIAG[%s] temporal %s: current=%s ramping=%s versions=%d",
				label, wdName, cur, ramp, len(resp.Info.VersionSummaries))
			for _, vs := range resp.Info.VersionSummaries {
				t.Logf("DIAG[%s]   temporal version %-14s drainage=%v", label, vs.Version.BuildID, vs.DrainageStatus)
			}
		}
	}

	var live temporaliov1alpha1.WorkerDeployment
	if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &live); client.IgnoreNotFound(err) == nil && err != nil {
		t.Logf("DIAG[%s] TWD not created yet", label)
	} else if err != nil {
		t.Logf("DIAG[%s] get TWD %s failed: %v", label, twd.Name, err)
	} else {
		ineligible := 0
		for _, dv := range live.Status.DeprecatedVersions {
			if !dv.EligibleForDeletion {
				ineligible++
			}
		}
		cur := "<none>"
		if live.Status.CurrentVersion != nil {
			cur = live.Status.CurrentVersion.BuildID
		}
		t.Logf("DIAG[%s] TWD status: current=%s target=%s(%s) versionCount=%d deprecated=%d ineligible=%d (cap=%d, blocked=%v)",
			label, cur, live.Status.TargetVersion.BuildID, live.Status.TargetVersion.Status,
			live.Status.VersionCount, len(live.Status.DeprecatedVersions), ineligible,
			testMaxVersionsIneligibleForDeletion, ineligible >= testMaxVersionsIneligibleForDeletion)
		for _, dv := range live.Status.DeprecatedVersions {
			t.Logf("DIAG[%s]   deprecated %-14s status=%-14s eligibleForDeletion=%v", label, dv.BuildID, dv.Status, dv.EligibleForDeletion)
		}
	}

	var deps appsv1.DeploymentList
	if err := env.K8sClient.List(ctx, &deps, client.InNamespace(twd.Namespace)); err != nil {
		t.Logf("DIAG[%s] list deployments failed: %v", label, err)
		return
	}
	for _, d := range deps.Items {
		owned := false
		for _, or := range d.OwnerReferences {
			if or.UID == twd.UID {
				owned = true
			}
		}
		if !owned {
			continue
		}
		var specReplicas int32
		if d.Spec.Replicas != nil {
			specReplicas = *d.Spec.Replicas
		}
		t.Logf("DIAG[%s]   k8s deployment %-42s buildID=%-14s spec.replicas=%d status.replicas=%d",
			label, d.Name, d.Labels[k8s.BuildIDLabel], specReplicas, d.Status.Replicas)
	}
}

// postMortemDrainageWatch keeps watching after the test has already given up
func postMortemDrainageWatch(
	t *testing.T,
	ctx context.Context,
	env testhelpers.TestEnv,
	twd *temporaliov1alpha1.WorkerDeployment,
	deploymentName string,
	budget time.Duration,
	interval time.Duration,
) {
	t.Helper()

	wdName := k8s.ComputeWorkerDeploymentName(twd)
	handle := env.Ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(wdName)

	if probe, err := handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{}); err == nil {
		stuck := false
		for _, vs := range probe.Info.VersionSummaries {
			if vs.DrainageStatus == sdkclient.WorkerDeploymentVersionDrainageStatusDraining {
				stuck = true
			}
		}
		if !stuck {
			t.Logf("DIAG[post-mortem] no version is in Draining; nothing to watch")
			return
		}
	}

	start := time.Now()
	deadline := start.Add(budget)
	lastDrainage := map[string]sdkclient.WorkerDeploymentVersionDrainageStatus{}
	deploymentSeen := false
	var firstFlip, deploymentAt time.Duration
	sawFlip := false

	t.Logf("DIAG[post-mortem] watching for up to %s at %s intervals (test has already failed)", budget, interval)

	for time.Now().Before(deadline) {
		elapsed := time.Since(start).Truncate(time.Second)

		resp, err := handle.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			t.Logf("DIAG[post-mortem] t=+%s describe failed: %v", elapsed, err)
		} else {
			for _, vs := range resp.Info.VersionSummaries {
				prev, seen := lastDrainage[vs.Version.BuildID]
				if !seen {
					t.Logf("DIAG[post-mortem] t=+%s %-14s drainage=%v", elapsed, vs.Version.BuildID, vs.DrainageStatus)
				} else if prev != vs.DrainageStatus {
					t.Logf("DIAG[post-mortem] t=+%s %-14s drainage %v -> %v  <-- TRANSITION", elapsed, vs.Version.BuildID, prev, vs.DrainageStatus)
					if !sawFlip && vs.DrainageStatus == sdkclient.WorkerDeploymentVersionDrainageStatusDrained {
						firstFlip, sawFlip = elapsed, true
					}
				}
				lastDrainage[vs.Version.BuildID] = vs.DrainageStatus
			}
		}

		if !deploymentSeen {
			var deployment appsv1.Deployment
			if err := env.K8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: twd.Namespace}, &deployment); err == nil {
				deploymentSeen, deploymentAt = true, elapsed
				t.Logf("DIAG[post-mortem] t=+%s deployment %s CREATED  <-- controller unblocked", elapsed, deploymentName)
			}
		}

		if sawFlip && deploymentSeen {
			break // both questions answered; no reason to keep the run alive
		}
		time.Sleep(interval)
	}

	switch {
	case sawFlip && deploymentSeen:
		t.Logf("DIAG[post-mortem] VERDICT: backoff, not latched -- drained at +%s, controller created the deployment at +%s", firstFlip, deploymentAt)
	case sawFlip:
		t.Logf("DIAG[post-mortem] VERDICT: drainage recovered at +%s but the deployment never appeared within %s", firstFlip, budget)
	default:
		t.Logf("DIAG[post-mortem] VERDICT: latched -- no version reached Drained within %s; drainage evaluation appears to have stopped", budget)
	}
}
