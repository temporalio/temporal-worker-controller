package internal

import (
	"context"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/server/api/deployment/v1"
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
	version *deployment.WorkerDeploymentVersion) {

	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(version.DeploymentName)

	eventually(t, 30*time.Second, time.Second, func() error {
		resp, err := deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
		if err != nil {
			return fmt.Errorf("unable to describe worker deployment %s: %w", version.DeploymentName, err)
		}
		for _, vs := range resp.Info.VersionSummaries {
			if vs.Version.BuildId == version.BuildId {
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
	version *deployment.WorkerDeploymentVersion,
) {
	waitForVersionRegistrationInDeployment(t, ctx, ts, version)
	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(version.DeploymentName)
	eventually(t, 30*time.Second, time.Second, func() error {
		_, err := deploymentHandler.SetCurrentVersion(ctx, sdkclient.WorkerDeploymentSetCurrentVersionOptions{
			BuildID:  version.BuildId,
			Identity: controller.ControllerIdentity,
		})
		if err != nil {
			return fmt.Errorf("unable to set current version %v: %w", version, err)
		}
		return nil
	})
	return
}

func setRampingVersion(
	t *testing.T,
	ctx context.Context,
	ts *temporaltest.TestServer,
	version *deployment.WorkerDeploymentVersion,
	rampPercentage float32,
) {
	waitForVersionRegistrationInDeployment(t, ctx, ts, version)
	deploymentHandler := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(version.DeploymentName)
	eventually(t, 30*time.Second, time.Second, func() error {
		var buildId string
		if version != nil {
			buildId = version.BuildId
		}

		_, err := deploymentHandler.SetRampingVersion(ctx, sdkclient.WorkerDeploymentSetRampingVersionOptions{
			BuildID:    buildId,
			Percentage: rampPercentage,
			Identity:   controller.ControllerIdentity,
		})
		if err != nil {
			return fmt.Errorf("unable to set current version %v: %w", version, err)
		}
		return nil
	})
	return
}

func verifyTemporalWorkerDeploymentStatusEventually(
	t *testing.T,
	ctx context.Context,
	k8sClient client.Client,
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
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      twdName,
			Namespace: namespace,
		}, &twd); err != nil {
			return fmt.Errorf("failed to get updated worker deployment: %v", err)
		}
		if expectedDeploymentStatus.CurrentVersion != nil {
			if twd.Status.CurrentVersion == nil {
				return fmt.Errorf("expected CurrentVersion to be set")
			}
			if twd.Status.CurrentVersion.VersionID != expectedDeploymentStatus.CurrentVersion.VersionID {
				return fmt.Errorf("expected current version id to be '%s', got '%s'",
					expectedDeploymentStatus.CurrentVersion.VersionID,
					twd.Status.CurrentVersion.VersionID)
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
		if expectedDeploymentStatus.TargetVersion.VersionID != "" {
			if twd.Status.TargetVersion.VersionID == "" {
				return fmt.Errorf("expected TargetVersion to be set")
			}
			if twd.Status.TargetVersion.VersionID != expectedDeploymentStatus.TargetVersion.VersionID {
				return fmt.Errorf("expected target version id to be '%s', got '%s'",
					expectedDeploymentStatus.TargetVersion.VersionID,
					twd.Status.TargetVersion.VersionID)
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
		return nil // All assertions passed!
	})
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
