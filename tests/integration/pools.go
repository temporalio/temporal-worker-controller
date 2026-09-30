//go:build integration
// +build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/api/serviceerror"
	sdkclient "go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const activitiesPool = "activities"

func runWorkerPoolTests(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, namespace string) {
	t.Run("worker-pools-roll-out-and-sunset-together", func(t *testing.T) {
		testWorkerPoolsLifecycle(t, k8sClient, ts, namespace)
	})
}

// pooledWorkerDeployment returns a WorkerDeployment whose default pool polls taskQueue for
// workflows and whose activities pool polls taskQueue-activities for activities only.
func pooledWorkerDeployment(name, namespace, temporalNamespace, image string) *temporaliov1alpha1.WorkerDeployment {
	tc := testhelpers.NewTestCase().WithInput(testhelpers.NewWorkerDeploymentBuilder().
		WithAllAtOnceStrategy().WithReplicas(1).WithTargetTemplate(image)).
		BuildWithValues(name, namespace, temporalNamespace)
	twd := tc.GetTWD()
	twd.Spec.SunsetStrategy = temporaliov1alpha1.SunsetStrategy{
		ScaledownDelay: &metav1.Duration{},
		DeleteDelay:    &metav1.Duration{},
	}
	activities := testhelpers.SetTaskQueue(*twd.Spec.Deployment.Template.DeepCopy(), name+"-activities")
	activities = testhelpers.SetWorkerRole(activities, testhelpers.ActivityWorkerRole)
	replicas := int32(1)
	twd.Spec.Pools = []temporaliov1alpha1.WorkerPool{{
		Name:       activitiesPool,
		Deployment: appsv1.DeploymentSpec{Replicas: &replicas, Template: activities},
	}}
	return twd
}

func waitForDeployment(t *testing.T, ctx context.Context, k8sClient client.Client, namespace, name string) appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	eventually(t, 30*time.Second, time.Second, func() error {
		return k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &d)
	})
	return d
}

func waitForTargetStatus(t *testing.T, ctx context.Context, k8sClient client.Client, key types.NamespacedName, buildID string, status temporaliov1alpha1.VersionStatus) temporaliov1alpha1.WorkerDeployment {
	t.Helper()
	var wd temporaliov1alpha1.WorkerDeployment
	eventually(t, 60*time.Second, time.Second, func() error {
		if err := k8sClient.Get(ctx, key, &wd); err != nil {
			return err
		}
		if wd.Status.TargetVersion.BuildID != buildID || wd.Status.TargetVersion.Status != status {
			return fmt.Errorf("target is %s %s, want %s %s", wd.Status.TargetVersion.BuildID, wd.Status.TargetVersion.Status, buildID, status)
		}
		return nil
	})
	return wd
}

// runCrossPoolWorkflow starts a workflow pinned to buildID on the default pool's queue and
// returns the build ID of the activities pool worker that ran its activity.
func runCrossPoolWorkflow(t *testing.T, ctx context.Context, ts *temporaltest.TestServer, deploymentName, taskQueue, buildID string) string {
	t.Helper()
	run, err := ts.GetDefaultClient().ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
		ID:        fmt.Sprintf("%s-cross-pool-%s", taskQueue, buildID),
		TaskQueue: taskQueue,
		VersioningOverride: &sdkclient.PinnedVersioningOverride{
			Version: sdkworker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: buildID},
		},
	}, testhelpers.CrossPoolWorkflowType, taskQueue+"-activities")
	if err != nil {
		t.Fatalf("failed to start cross-pool workflow: %v", err)
	}
	var got string
	if err := run.Get(ctx, &got); err != nil {
		t.Fatalf("cross-pool workflow failed: %v", err)
	}
	return got
}

func testWorkerPoolsLifecycle(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, namespace string) {
	ctx := context.Background()
	const name = "pools"
	twd := pooledWorkerDeployment(name, namespace, ts.GetDefaultNamespace(), "v1.0")
	key := types.NamespacedName{Namespace: namespace, Name: name}
	deploymentName := k8s.ComputeWorkerDeploymentName(twd)
	v1 := k8s.ComputeBuildID(twd)

	connection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: twd.Spec.WorkerOptions.ConnectionRef.Name, Namespace: namespace},
		Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
	}
	if err := k8sClient.Create(ctx, connection); err != nil {
		t.Fatal(err)
	}
	wrt := makeHPAWRT(name+"-activities-hpa", namespace, name)
	wrt.Spec.Pool = activitiesPool
	if err := k8sClient.Create(ctx, wrt); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatal(err)
	}

	t.Log("Every pool of v1 gets its own Deployment with a pool selector")
	v1Default := waitForDeployment(t, ctx, k8sClient, namespace, k8s.ComputeVersionedDeploymentName(name, v1))
	v1Activities := waitForDeployment(t, ctx, k8sClient, namespace, k8s.ComputePoolDeploymentName(name, activitiesPool, v1))
	if got := v1Default.Spec.Selector.MatchLabels[k8s.PoolLabel]; got != temporaliov1alpha1.DefaultPoolName {
		t.Errorf("default pool selector has pool label %q", got)
	}
	if got := v1Activities.Spec.Selector.MatchLabels[k8s.PoolLabel]; got != activitiesPool {
		t.Errorf("activities pool selector has pool label %q", got)
	}

	t.Log("v1 is not promoted while its activities pool is unavailable")
	v1DefaultStops := applyDeployment(t, ctx, k8sClient, v1Default.Name, namespace)
	stopV1Default := sync.OnceFunc(func() { handleStopFuncs(v1DefaultStops) })
	defer stopV1Default()
	waitForTargetStatus(t, ctx, k8sClient, key, v1, temporaliov1alpha1.VersionStatusInactive)
	eventually(t, 30*time.Second, time.Second, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, key, &wd); err != nil {
			return err
		}
		cond := meta.FindStatusCondition(wd.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
		if cond == nil || cond.Reason != temporaliov1alpha1.ReasonWaitingForPollers || !strings.Contains(cond.Message, activitiesPool) {
			return fmt.Errorf("progressing condition does not name the activities pool: %+v", cond)
		}
		return nil
	})
	time.Sleep(3 * time.Second)
	if wd := waitForTargetStatus(t, ctx, k8sClient, key, v1, temporaliov1alpha1.VersionStatusInactive); wd.Status.CurrentVersion != nil {
		t.Fatalf("v1 was promoted before every pool was available")
	}

	t.Log("v1 is promoted once every pool is available")
	v1ActivitiesStops := applyDeployment(t, ctx, k8sClient, v1Activities.Name, namespace)
	stopV1Activities := sync.OnceFunc(func() { handleStopFuncs(v1ActivitiesStops) })
	defer stopV1Activities()
	wd := waitForTargetStatus(t, ctx, k8sClient, key, v1, temporaliov1alpha1.VersionStatusCurrent)
	if len(wd.Status.TargetVersion.Pools) != 2 {
		t.Errorf("target version pools = %+v, want default and activities", wd.Status.TargetVersion.Pools)
	}

	t.Log("Both pools' task queues are in one Temporal version")
	desc, err := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName).
		DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: v1})
	if err != nil {
		t.Fatal(err)
	}
	queues := map[string]sdkclient.TaskQueueType{}
	for _, tq := range desc.Info.TaskQueuesInfos {
		queues[tq.Name] = tq.Type
	}
	if queues[name] != sdkclient.TaskQueueTypeWorkflow || queues[name+"-activities"] != sdkclient.TaskQueueTypeActivity {
		t.Errorf("v1 task queues = %+v, want %s (workflow) and %s-activities (activity)", queues, name, name)
	}

	t.Log("The activities WRT targets only the activities pool's Deployment")
	waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, namespace,
		k8s.ComputeWorkerResourceTemplateName(name, wrt.Name, v1), v1Activities.Name, 30*time.Second)

	t.Log("A new image rolls out as v2 across both pools")
	eventually(t, 10*time.Second, time.Second, func() error {
		var latest temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, key, &latest); err != nil {
			return err
		}
		latest.Spec.Deployment.Template.Spec.Containers[0].Image = "v2.0"
		latest.Spec.Pools[0].Deployment.Template.Spec.Containers[0].Image = "v2.0"
		return k8sClient.Update(ctx, &latest)
	})
	var latest temporaliov1alpha1.WorkerDeployment
	if err := k8sClient.Get(ctx, key, &latest); err != nil {
		t.Fatal(err)
	}
	v2 := k8s.ComputeBuildID(&latest)
	v2Default := waitForDeployment(t, ctx, k8sClient, namespace, k8s.ComputeVersionedDeploymentName(name, v2))
	v2Activities := waitForDeployment(t, ctx, k8sClient, namespace, k8s.ComputePoolDeploymentName(name, activitiesPool, v2))
	v2DefaultStops := applyDeployment(t, ctx, k8sClient, v2Default.Name, namespace)
	defer handleStopFuncs(v2DefaultStops)
	v2ActivitiesStops := applyDeployment(t, ctx, k8sClient, v2Activities.Name, namespace)
	defer handleStopFuncs(v2ActivitiesStops)
	waitForTargetStatus(t, ctx, k8sClient, key, v2, temporaliov1alpha1.VersionStatusCurrent)

	t.Log("Activities on the activities pool run on the calling workflow's build")
	if got := runCrossPoolWorkflow(t, ctx, ts, deploymentName, name, v1); got != v1 {
		t.Errorf("activity for a workflow pinned to %s ran on %s", v1, got)
	}
	if got := runCrossPoolWorkflow(t, ctx, ts, deploymentName, name, v2); got != v2 {
		t.Errorf("activity for a workflow pinned to %s ran on %s", v2, got)
	}

	t.Log("v1 sunsets every pool together")
	eventually(t, 60*time.Second, time.Second, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, key, &wd); err != nil {
			return err
		}
		for _, v := range wd.Status.DeprecatedVersions {
			if v.BuildID == v1 && v.Status == temporaliov1alpha1.VersionStatusDrained {
				return nil
			}
		}
		return fmt.Errorf("v1 is not drained yet: %+v", wd.Status.DeprecatedVersions)
	})
	stopV1Default()
	stopV1Activities()
	scaleDeploymentToZero(t, ctx, k8sClient, v1Default.Name, namespace)
	scaleDeploymentToZero(t, ctx, k8sClient, v1Activities.Name, namespace)
	eventually(t, 90*time.Second, time.Second, func() error {
		for _, d := range []string{v1Default.Name, v1Activities.Name} {
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: d}, &appsv1.Deployment{}); err == nil {
				return fmt.Errorf("deployment %s still exists", d)
			} else if client.IgnoreNotFound(err) != nil {
				return err
			}
		}
		_, err := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName).
			DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: v1})
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return fmt.Errorf("expected v1 to be deleted from Temporal, got %v", err)
		}
		return nil
	})
	eventually(t, 30*time.Second, time.Second, func() error {
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: k8s.ComputeWorkerResourceTemplateName(name, wrt.Name, v1)}, &autoscalingv2.HorizontalPodAutoscaler{})
		if err == nil {
			return fmt.Errorf("v1 HPA still exists")
		}
		return client.IgnoreNotFound(err)
	})
	for _, d := range []string{v2Default.Name, v2Activities.Name} {
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: d}, &appsv1.Deployment{}); err != nil {
			t.Errorf("v2 deployment %s: %v", d, err)
		}
	}
}
