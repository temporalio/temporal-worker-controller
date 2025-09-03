package internal

import (
	"context"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	EmptyTargetVersion = temporaliov1alpha1.TargetWorkerDeploymentVersion{}
)

// waitForDeployment waits for a deployment to be created
func waitForDeployment(t *testing.T, k8sClient client.Client, deploymentName, namespace string, timeout time.Duration) {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		var deployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      deploymentName,
			Namespace: namespace,
		}, &deployment); err == nil {
			t.Logf("Found deployment %s in namespace %s", deployment.Name, namespace)
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("failed to wait for deployment: timeout waiting for deployment %s in namespace %s", deploymentName, namespace)
}

func waitForVersionRegistrationInDeployment(
	t *testing.T,
	ctx context.Context,
	ts *temporaltest.TestServer,
	version *worker.WorkerDeploymentVersion) {

	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(version.DeploymentName)

	eventually(t, 30*time.Second, time.Second, func() error {
		resp, err := deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return fmt.Errorf("unable to describe worker deployment %s: %w", version.DeploymentName, err)
		}
		for _, vs := range resp.Info.VersionSummaries {
			if vs.Version.DeploymentName == version.DeploymentName && vs.Version.BuildId == version.BuildId {
				return nil
			}
		}
		return fmt.Errorf("could not find version with build %s in worker deployment %s", version.BuildId, version.DeploymentName)
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
			BuildId:        buildID,
		})
	}
	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)
	eventually(t, 30*time.Second, time.Second, func() error {
		_, err := deploymentHandler.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
			BuildID:  buildID,
			Identity: controller.ControllerIdentity,
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
			BuildId:        buildID,
		})
	}
	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)
	eventually(t, 30*time.Second, time.Second, func() error {
		_, err := deploymentHandler.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
			BuildID:    buildID,
			Percentage: rampPercentage,
			Identity:   controller.ControllerIdentity,
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
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
	expectedDeploymentStatus temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	timeout time.Duration,
	interval time.Duration,
) {
	if twd == nil {
		t.Fatalf("TemporalWorkerDeployment cannot be nil")
	}
	if expectedDeploymentStatus.TargetVersion.Status == temporaliov1alpha1.VersionStatusNotRegistered ||
		expectedDeploymentStatus.TargetVersion.Status == "" {
		return // this is the first rollout, no Worker Deployment in temporal to describe
	}
	deploymentClient := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(k8s.ComputeWorkerDeploymentName(twd))

	eventually(t, timeout, interval, func() error {
		resp, err := deploymentClient.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return fmt.Errorf("error describing worker deployment: %w (target version status: %v)", err, expectedDeploymentStatus.TargetVersion.Status)
		}
		rc := resp.Info.RoutingConfig

		if cv := expectedDeploymentStatus.CurrentVersion; cv != nil {
			if rc.CurrentVersion == nil {
				return fmt.Errorf("expected CurrentVersion to be set")
			}
			if rc.CurrentVersion.BuildId != expectedDeploymentStatus.CurrentVersion.BuildID {
				return fmt.Errorf("expected current build id to be '%s', got '%s'",
					expectedDeploymentStatus.CurrentVersion.BuildID,
					rc.CurrentVersion.BuildId)
			}
		}
		if tv := expectedDeploymentStatus.TargetVersion; tv.BuildID != "" {
			switch tv.Status {
			case temporaliov1alpha1.VersionStatusNotRegistered:
				for _, vs := range resp.Info.VersionSummaries {
					if vs.Version.BuildId == tv.BuildID {
						return fmt.Errorf("expected build id '%s' to not be registered, but found it", tv.BuildID)
					}
				}
			case temporaliov1alpha1.VersionStatusRamping:
				if rc.RampingVersion == nil {
					return fmt.Errorf("expected build id '%s' to be Ramping, but was nil was ramping instead", tv.BuildID)
				} else {
					if rc.RampingVersion.BuildId != tv.BuildID {
						return fmt.Errorf("expected build id '%s' to be Ramping, but was '%s' was ramping instead", tv.BuildID, rc.RampingVersion.BuildId)
					}
				}
				if tv.RampPercentage == nil {
					if rc.RampingVersionPercentage != 0 {
						return fmt.Errorf("expected RampPercentage to be nil, but was %v", rc.RampingVersionPercentage)
					}
				} else {
					if rc.RampingVersionPercentage != *tv.RampPercentage {
						return fmt.Errorf("expected RampPercentage to be %v, but was %v", *tv.RampPercentage, rc.RampingVersionPercentage)
					}
				}
			case temporaliov1alpha1.VersionStatusCurrent:
				if rc.CurrentVersion == nil {
					return fmt.Errorf("expected build id '%s' to be Current, but was nil was current instead", tv.BuildID)
				} else {
					if rc.CurrentVersion.BuildId != tv.BuildID {
						return fmt.Errorf("expected build id '%s' to be Current, but was '%s' was Current instead", tv.BuildID, rc.CurrentVersion.BuildId)
					}
				}
			case temporaliov1alpha1.VersionStatusInactive, temporaliov1alpha1.VersionStatusDraining, temporaliov1alpha1.VersionStatusDrained:
				if rc.CurrentVersion != nil && rc.CurrentVersion.BuildId == tv.BuildID {
					return fmt.Errorf("expected build id '%s' to be %v, but was Current", tv.BuildID, tv.Status)
				}
				if rc.RampingVersion != nil && rc.RampingVersion.BuildId == tv.BuildID {
					return fmt.Errorf("expected build id '%s' to be %v, but was Ramping", tv.BuildID, tv.Status)
				}
				found := false
				for _, vs := range resp.Info.VersionSummaries {
					if vs.Version.BuildId == tv.BuildID {
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
func verifyTemporalWorkerDeploymentStatusEventually(
	t *testing.T,
	ctx context.Context,
	env testhelpers.TestEnv,
	twdName,
	namespace string,
	expectedDeploymentStatus *temporaliov1alpha1.TemporalWorkerDeploymentStatus,
	timeout time.Duration,
	interval time.Duration,
) {
	if expectedDeploymentStatus == nil {
		t.Fatalf("expected deployment status cannot be nil")
	}
	eventually(t, timeout, interval, func() error {
		var twd temporaliov1alpha1.TemporalWorkerDeployment
		if err := env.K8sClient.Get(ctx, types.NamespacedName{
			Name:      twdName,
			Namespace: namespace,
		}, &twd); err != nil {
			return fmt.Errorf("failed to get updated worker deployment: %v", err)
		}
		// validate current version
		if expectedDeploymentStatus.CurrentVersion != nil {
			if twd.Status.CurrentVersion == nil {
				return fmt.Errorf("expected CurrentVersion to be set")
			}
			if twd.Status.CurrentVersion.BuildID != expectedDeploymentStatus.CurrentVersion.BuildID {
				return fmt.Errorf("expected current build id to be '%s', got '%s'",
					expectedDeploymentStatus.CurrentVersion.BuildID,
					twd.Status.CurrentVersion.BuildID)
			}
			if twd.Status.CurrentVersion.Deployment == nil {
				return fmt.Errorf("expected CurrentVersion.Deployment to be set")
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
			expectedDV.BuildID, expectedDV.Status, expectedDV.Status)
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
		t.Fatalf("eventually failed after %s: %v", timeout, lastErr)
	}
}
