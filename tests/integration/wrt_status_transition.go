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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const wrtStatusTransitionPinnedWorkflowType = "wrtStatusTransitionPinnedWorkflow"

// testWRTVersionStatusesCurrentToDraining moves a version from Current to Draining and back,
// with one HPA template for [Current, Ramping] and one for [Draining, Drained]. At each step
// the version must have exactly the copy of the template that selects its status.
func testWRTVersionStatusesCurrentToDraining(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, namespace string) {
	ctx := context.Background()
	testName := "wrt-status-transition"

	tc := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewWorkerDeploymentBuilder().
				WithManualStrategy().
				WithReplicas(1).
				WithTargetTemplate("v1.0"),
		).
		BuildWithValues(testName, namespace, ts.GetDefaultNamespace())
	twd := tc.GetTWD()
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
	depNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)
	var deploymentV1 appsv1.Deployment
	eventually(t, 30*time.Second, time.Second, func() error {
		return k8sClient.Get(ctx, types.NamespacedName{Name: depNameV1, Namespace: namespace}, &deploymentV1)
	})

	v1Worker, stopV1, err := testhelpers.NewWorker(ctx, workerDeploymentName, buildIDv1, testName, ts.GetFrontendHostPort(), ts.GetDefaultNamespace(), true)
	if err != nil {
		t.Fatalf("failed to create v1 worker: %v", err)
	}
	defer stopV1()
	v1Worker.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
		return workflow.Await(ctx, func() bool { return false })
	}, workflow.RegisterOptions{Name: wrtStatusTransitionPinnedWorkflowType})
	if err := v1Worker.Start(); err != nil {
		t.Fatalf("failed to start v1 worker: %v", err)
	}
	setHealthyDeploymentStatus(t, ctx, k8sClient, deploymentV1)
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildIDv1)

	currentHPA := makeWRTWithRaw("current-hpa", namespace, twd.Name, []byte(`{
		"apiVersion": "autoscaling/v2",
		"kind": "HorizontalPodAutoscaler",
		"spec": {"scaleTargetRef": {}, "minReplicas": 3, "maxReplicas": 10, "metrics": []}
	}`))
	currentHPA.Spec.VersionStatuses = []temporaliov1alpha1.VersionStatus{
		temporaliov1alpha1.VersionStatusCurrent,
		temporaliov1alpha1.VersionStatusRamping,
	}
	pastHPA := makeWRTWithRaw("past-hpa", namespace, twd.Name, []byte(`{
		"apiVersion": "autoscaling/v2",
		"kind": "HorizontalPodAutoscaler",
		"spec": {"scaleTargetRef": {}, "minReplicas": 1, "maxReplicas": 2, "metrics": []}
	}`))
	pastHPA.Spec.VersionStatuses = []temporaliov1alpha1.VersionStatus{
		temporaliov1alpha1.VersionStatusDraining,
		temporaliov1alpha1.VersionStatusDrained,
	}
	for _, wrt := range []*temporaliov1alpha1.WorkerResourceTemplate{currentHPA, pastHPA} {
		if err := k8sClient.Create(ctx, wrt); err != nil {
			t.Fatalf("failed to create WRT %s: %v", wrt.Name, err)
		}
	}
	currentHPAv1 := k8s.ComputeWorkerResourceTemplateName(twd.Name, currentHPA.Name, buildIDv1)
	pastHPAv1 := k8s.ComputeWorkerResourceTemplateName(twd.Name, pastHPA.Name, buildIDv1)

	t.Log("Step 1: v1 is Current, so only current-hpa has a copy for v1")
	waitForHPAWithMinReplicas(ctx, t, k8sClient, namespace, currentHPAv1, depNameV1, 3, 30*time.Second)
	waitForWRTStatusApplied(t, ctx, k8sClient, namespace, currentHPA.Name, buildIDv1, 30*time.Second)
	waitForWRTCopyDeleted(ctx, t, k8sClient, namespace, pastHPA.Name, buildIDv1, pastHPAv1, &autoscalingv2.HorizontalPodAutoscaler{}, 30*time.Second)

	// The open pinned workflow keeps v1 Draining once v2 becomes Current.
	run, err := ts.GetDefaultClient().ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
		ID:        testName + "-pinned",
		TaskQueue: testName,
		VersioningOverride: &sdkclient.PinnedVersioningOverride{
			Version: sdkworker.WorkerDeploymentVersion{DeploymentName: workerDeploymentName, BuildID: buildIDv1},
		},
	}, wrtStatusTransitionPinnedWorkflowType)
	if err != nil {
		t.Fatalf("failed to start pinned workflow: %v", err)
	}
	defer func() {
		_ = ts.GetDefaultClient().TerminateWorkflow(context.Background(), run.GetID(), run.GetRunID(), "integration test cleanup")
	}()

	key := types.NamespacedName{Name: twd.Name, Namespace: namespace}
	var buildIDv2 string
	updateWorkerDeployment(ctx, t, k8sClient, key, func(w *temporaliov1alpha1.WorkerDeployment) {
		w.Spec.Deployment.Template.Spec.Containers[0].Image = "v2.0"
		buildIDv2 = k8s.ComputeBuildID(w)
	})
	depNameV2 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv2)
	eventually(t, 30*time.Second, time.Second, func() error {
		var deployment appsv1.Deployment
		return k8sClient.Get(ctx, types.NamespacedName{Name: depNameV2, Namespace: namespace}, &deployment)
	})
	stopV2 := applyDeployment(t, ctx, k8sClient, depNameV2, namespace)
	defer handleStopFuncs(stopV2)
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildIDv2)
	waitForVersionStatus(ctx, t, k8sClient, key, buildIDv1, temporaliov1alpha1.VersionStatusDraining, 60*time.Second)

	t.Log("Step 2: v1 is Draining, so current-hpa's copy is deleted and past-hpa's copy is created")
	waitForWRTCopyDeleted(ctx, t, k8sClient, namespace, currentHPA.Name, buildIDv1, currentHPAv1, &autoscalingv2.HorizontalPodAutoscaler{}, 30*time.Second)
	waitForHPAWithMinReplicas(ctx, t, k8sClient, namespace, pastHPAv1, depNameV1, 1, 30*time.Second)
	waitForWRTStatusApplied(t, ctx, k8sClient, namespace, pastHPA.Name, buildIDv1, 30*time.Second)
	currentHPAv2 := k8s.ComputeWorkerResourceTemplateName(twd.Name, currentHPA.Name, buildIDv2)
	waitForHPAWithMinReplicas(ctx, t, k8sClient, namespace, currentHPAv2, depNameV2, 3, 30*time.Second)

	// Roll back: v1 becomes Current again. Its current-hpa status entry was pruned when the
	// copy was deleted, so the copy must be re-applied rather than skipped on a stale hash.
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildIDv1)
	waitForVersionStatus(ctx, t, k8sClient, key, buildIDv1, temporaliov1alpha1.VersionStatusCurrent, 60*time.Second)

	t.Log("Step 3: v1 is Current again, so current-hpa's copy is recreated and past-hpa's copy is deleted")
	waitForHPAWithMinReplicas(ctx, t, k8sClient, namespace, currentHPAv1, depNameV1, 3, 30*time.Second)
	waitForWRTStatusApplied(t, ctx, k8sClient, namespace, currentHPA.Name, buildIDv1, 30*time.Second)
	waitForWRTCopyDeleted(ctx, t, k8sClient, namespace, pastHPA.Name, buildIDv1, pastHPAv1, &autoscalingv2.HorizontalPodAutoscaler{}, 30*time.Second)
}

// waitForVersionStatus polls until the WorkerDeployment status reports buildID with the given status.
func waitForVersionStatus(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	key types.NamespacedName,
	buildID string,
	want temporaliov1alpha1.VersionStatus,
	timeout time.Duration,
) {
	t.Helper()
	eventually(t, timeout, time.Second, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, key, &wd); err != nil {
			return err
		}
		if wd.Status.CurrentVersion != nil && wd.Status.CurrentVersion.BuildID == buildID && wd.Status.CurrentVersion.Status == want {
			return nil
		}
		for _, v := range wd.Status.DeprecatedVersions {
			if v.BuildID == buildID && v.Status == want {
				return nil
			}
		}
		return fmt.Errorf("build ID %q is not yet %s in the WorkerDeployment status", buildID, want)
	})
}

// waitForHPAWithMinReplicas polls until the named HPA targets expectedDeploymentName and has
// the given minReplicas, which tells the copies of different templates apart.
func waitForHPAWithMinReplicas(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	namespace, hpaName, expectedDeploymentName string,
	minReplicas int32,
	timeout time.Duration,
) {
	t.Helper()
	eventually(t, timeout, time.Second, func() error {
		var hpa autoscalingv2.HorizontalPodAutoscaler
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: hpaName, Namespace: namespace}, &hpa); err != nil {
			return err
		}
		if hpa.Spec.ScaleTargetRef.Name != expectedDeploymentName {
			return fmt.Errorf("HPA %s targets %q, want %q", hpaName, hpa.Spec.ScaleTargetRef.Name, expectedDeploymentName)
		}
		if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != minReplicas {
			return fmt.Errorf("HPA %s has minReplicas %v, want %d", hpaName, hpa.Spec.MinReplicas, minReplicas)
		}
		return nil
	})
}

// waitForWRTCopyDeleted polls until the rendered copy of wrtName for buildID does not exist
// and the WRT status has no entry for buildID.
func waitForWRTCopyDeleted(
	ctx context.Context,
	t *testing.T,
	k8sClient client.Client,
	namespace, wrtName, buildID, resourceName string,
	obj client.Object,
	timeout time.Duration,
) {
	t.Helper()
	eventually(t, timeout, time.Second, func() error {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: namespace}, obj)
		if err == nil {
			return fmt.Errorf("rendered copy %s still exists", resourceName)
		}
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		var wrt temporaliov1alpha1.WorkerResourceTemplate
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: wrtName, Namespace: namespace}, &wrt); err != nil {
			return err
		}
		for _, v := range wrt.Status.Versions {
			if v.BuildID == buildID {
				return fmt.Errorf("WRT %s status still has an entry for build ID %q", wrtName, buildID)
			}
		}
		return nil
	})
}
