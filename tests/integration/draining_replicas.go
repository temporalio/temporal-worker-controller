//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	sdkclient "go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const drainingReplicasPinnedWorkflowType = "drainingReplicasPinnedWorkflow"

// testDrainingDeploymentReplicas keeps v1 draining with an open pinned
// workflow, promotes v2, and checks that v1 runs at drainingDeployment.replicas
// while v2 keeps deployment.replicas.
func testDrainingDeploymentReplicas(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, namespace string) {
	ctx := context.Background()
	testName := "draining-replicas"
	deploymentReplicas, drainingReplicas := int32(2), int32(1)

	tc := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewWorkerDeploymentBuilder().
				WithManualStrategy().
				WithReplicas(deploymentReplicas).
				WithTargetTemplate("v1.0"),
		).
		BuildWithValues(testName, namespace, ts.GetDefaultNamespace())
	twd := tc.GetTWD()
	twd.Spec.DrainingDeployment = &temporaliov1alpha1.DrainingDeploymentSpec{Replicas: &drainingReplicas}
	twd.Spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{Duration: time.Hour}
	twd.Spec.SunsetStrategy.DeleteDelay = &metav1.Duration{Duration: time.Hour}

	connection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: twd.Spec.WorkerOptions.ConnectionRef.Name, Namespace: namespace},
		Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
	}
	if err := k8sClient.Create(ctx, connection); err != nil {
		t.Fatalf("failed to create Connection: %v", err)
	}
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create WorkerDeployment: %v", err)
	}

	workerDeploymentName := k8s.ComputeWorkerDeploymentName(twd)
	buildIDv1 := k8s.ComputeBuildID(twd)
	keyV1 := types.NamespacedName{Name: k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1), Namespace: namespace}
	var deploymentV1 appsv1.Deployment
	eventually(t, 30*time.Second, time.Second, func() error { return k8sClient.Get(ctx, keyV1, &deploymentV1) })

	v1Worker, stopV1, err := testhelpers.NewWorker(ctx, workerDeploymentName, buildIDv1, testName, ts.GetFrontendHostPort(), ts.GetDefaultNamespace(), true)
	if err != nil {
		t.Fatalf("failed to create v1 worker: %v", err)
	}
	defer stopV1()
	v1Worker.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
		return workflow.Await(ctx, func() bool { return false })
	}, workflow.RegisterOptions{Name: drainingReplicasPinnedWorkflowType})
	if err := v1Worker.Start(); err != nil {
		t.Fatalf("failed to start v1 worker: %v", err)
	}
	setHealthyDeploymentStatus(t, ctx, k8sClient, deploymentV1)
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildIDv1)

	// The open pinned workflow keeps v1 draining once v2 becomes current.
	run, err := ts.GetDefaultClient().ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
		ID:        testName + "-pinned",
		TaskQueue: testName,
		VersioningOverride: &sdkclient.PinnedVersioningOverride{
			Version: sdkworker.WorkerDeploymentVersion{DeploymentName: workerDeploymentName, BuildID: buildIDv1},
		},
	}, drainingReplicasPinnedWorkflowType)
	if err != nil {
		t.Fatalf("failed to start pinned workflow: %v", err)
	}
	defer func() {
		_ = ts.GetDefaultClient().TerminateWorkflow(context.Background(), run.GetID(), run.GetRunID(), "integration test cleanup")
	}()

	key := types.NamespacedName{Name: twd.Name, Namespace: namespace}
	var buildIDv2 string
	next := updateWorkerDeployment(ctx, t, k8sClient, key, func(w *temporaliov1alpha1.WorkerDeployment) {
		w.Spec.Deployment.Template.Spec.Containers[0].Image = "v2.0"
		buildIDv2 = k8s.ComputeBuildID(w)
	})
	keyV2 := types.NamespacedName{Name: k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv2), Namespace: namespace}
	eventually(t, 30*time.Second, time.Second, func() error {
		var deployment appsv1.Deployment
		return k8sClient.Get(ctx, keyV2, &deployment)
	})
	stopV2 := applyDeployment(t, ctx, k8sClient, keyV2.Name, namespace)
	defer handleStopFuncs(stopV2)
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildIDv2)

	eventually(t, 60*time.Second, time.Second, func() error {
		if err := k8sClient.Get(ctx, key, &next); err != nil {
			return err
		}
		draining := false
		for _, v := range next.Status.DeprecatedVersions {
			if v.BuildID == buildIDv1 && v.Status == temporaliov1alpha1.VersionStatusDraining {
				draining = true
			}
		}
		if !draining {
			return fmt.Errorf("v1 is not yet Draining in the WorkerDeployment status")
		}
		if err := k8sClient.Get(ctx, keyV1, &deploymentV1); err != nil {
			return err
		}
		if got := *deploymentV1.Spec.Replicas; got != drainingReplicas {
			return fmt.Errorf("draining v1 Deployment has %d replicas, want %d", got, drainingReplicas)
		}
		var deploymentV2 appsv1.Deployment
		if err := k8sClient.Get(ctx, keyV2, &deploymentV2); err != nil {
			return err
		}
		if got := *deploymentV2.Spec.Replicas; got != deploymentReplicas {
			return fmt.Errorf("current v2 Deployment has %d replicas, want %d", got, deploymentReplicas)
		}
		return nil
	})
}
