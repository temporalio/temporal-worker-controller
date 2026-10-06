//go:build integration

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

const (
	workflowsGroup  = "workflows"
	activitiesGroup = "activities"
)

func runWorkerGroupTests(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, namespace string) {
	t.Run("worker-groups-roll-out-and-sunset-together", func(t *testing.T) {
		testWorkerGroupsLifecycle(t, k8sClient, ts, namespace)
	})
}

// groupScenario is a WorkerDeployment whose workflows group polls a workflow queue named
// after it and whose activities group polls <name>-activities for activities only.
type groupScenario struct {
	t              *testing.T
	k8sClient      client.Client
	ts             *temporaltest.TestServer
	namespace      string
	name           string
	key            types.NamespacedName
	deploymentName string
}

// groupVersion holds the Deployments of one version and the stop functions of their workers.
type groupVersion struct {
	buildID    string
	def        appsv1.Deployment
	activities appsv1.Deployment
	stopDef    func()
	stopActs   func()
}

func (s *groupScenario) workerDeployment(image string) *temporaliov1alpha1.WorkerDeployment {
	tc := testhelpers.NewTestCase().WithInput(testhelpers.NewWorkerDeploymentBuilder().
		WithAllAtOnceStrategy().WithReplicas(1).WithTargetTemplate(image)).
		BuildWithValues(s.name, s.namespace, s.ts.GetDefaultNamespace())
	twd := tc.GetTWD()
	twd.Spec.SunsetStrategy = temporaliov1alpha1.SunsetStrategy{
		ScaledownDelay: &metav1.Duration{},
		DeleteDelay:    &metav1.Duration{},
	}
	workflows := *twd.Spec.Deployment
	activities := testhelpers.SetTaskQueue(workflows.Template, s.name+"-activities")
	activities = testhelpers.SetWorkerRole(activities, testhelpers.ActivityWorkerRole)
	twd.Spec.WorkerGroups = []temporaliov1alpha1.WorkerGroup{
		{Name: workflowsGroup, Deployment: workflows},
		{Name: activitiesGroup, Deployment: appsv1.DeploymentSpec{Replicas: workflows.Replicas, Template: activities}},
	}
	twd.Spec.Deployment = nil
	return twd
}

func (s *groupScenario) waitForDeployment(ctx context.Context, name string) appsv1.Deployment {
	s.t.Helper()
	var d appsv1.Deployment
	eventually(s.t, 30*time.Second, time.Second, func() error {
		return s.k8sClient.Get(ctx, types.NamespacedName{Namespace: s.namespace, Name: name}, &d)
	})
	return d
}

func (s *groupScenario) waitForVersionDeployments(ctx context.Context, buildID string) *groupVersion {
	s.t.Helper()
	return &groupVersion{
		buildID:    buildID,
		def:        s.waitForDeployment(ctx, k8s.ComputeWorkerGroupDeploymentName(s.name, workflowsGroup, buildID)),
		activities: s.waitForDeployment(ctx, k8s.ComputeWorkerGroupDeploymentName(s.name, activitiesGroup, buildID)),
	}
}

func (s *groupScenario) startWorkers(ctx context.Context, d appsv1.Deployment) func() {
	stops := applyDeployment(s.t, ctx, s.k8sClient, d.Name, s.namespace)
	return sync.OnceFunc(func() { handleStopFuncs(stops) })
}

func (s *groupScenario) waitForTarget(ctx context.Context, buildID string, status temporaliov1alpha1.VersionStatus) temporaliov1alpha1.WorkerDeployment {
	s.t.Helper()
	var wd temporaliov1alpha1.WorkerDeployment
	eventually(s.t, 60*time.Second, time.Second, func() error {
		if err := s.k8sClient.Get(ctx, s.key, &wd); err != nil {
			return err
		}
		if wd.Status.TargetVersion.BuildID != buildID || wd.Status.TargetVersion.Status != status {
			return fmt.Errorf("target is %s %s, want %s %s", wd.Status.TargetVersion.BuildID, wd.Status.TargetVersion.Status, buildID, status)
		}
		return nil
	})
	return wd
}

// runCrossGroupWorkflow starts a workflow pinned to buildID on the workflows group's queue and
// returns the build ID of the activities group worker that ran its activity.
func (s *groupScenario) runCrossGroupWorkflow(ctx context.Context, buildID string) string {
	s.t.Helper()
	run, err := s.ts.GetDefaultClient().ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
		ID:        fmt.Sprintf("%s-cross-group-%s", s.name, buildID),
		TaskQueue: s.name,
		VersioningOverride: &sdkclient.PinnedVersioningOverride{
			Version: sdkworker.WorkerDeploymentVersion{DeploymentName: s.deploymentName, BuildID: buildID},
		},
	}, testhelpers.CrossGroupWorkflowType, s.name+"-activities")
	if err != nil {
		s.t.Fatalf("failed to start cross-group workflow: %v", err)
	}
	var got string
	if err := run.Get(ctx, &got); err != nil {
		s.t.Fatalf("cross-group workflow failed: %v", err)
	}
	return got
}

func (s *groupScenario) assertGroupSelectors(v *groupVersion) {
	s.t.Helper()
	if got := v.def.Spec.Selector.MatchLabels[k8s.WorkerGroupLabel]; got != workflowsGroup {
		s.t.Errorf("workflows group selector has group label %q", got)
	}
	if got := v.activities.Spec.Selector.MatchLabels[k8s.WorkerGroupLabel]; got != activitiesGroup {
		s.t.Errorf("activities group selector has group label %q", got)
	}
}

func (s *groupScenario) assertWaitingForActivitiesGroup(ctx context.Context, buildID string) {
	s.t.Helper()
	s.waitForTarget(ctx, buildID, temporaliov1alpha1.VersionStatusInactive)
	eventually(s.t, 30*time.Second, time.Second, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := s.k8sClient.Get(ctx, s.key, &wd); err != nil {
			return err
		}
		cond := meta.FindStatusCondition(wd.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
		if cond == nil || cond.Reason != temporaliov1alpha1.ReasonWaitingForPollers || !strings.Contains(cond.Message, activitiesGroup) {
			return fmt.Errorf("progressing condition does not name the activities group: %+v", cond)
		}
		return nil
	})
	time.Sleep(3 * time.Second)
	if wd := s.waitForTarget(ctx, buildID, temporaliov1alpha1.VersionStatusInactive); wd.Status.CurrentVersion != nil {
		s.t.Fatalf("version %s was promoted before every group was available", buildID)
	}
}

func (s *groupScenario) assertTaskQueuesInOneVersion(ctx context.Context, buildID string) {
	s.t.Helper()
	desc, err := s.ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(s.deploymentName).
		DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: buildID})
	if err != nil {
		s.t.Fatal(err)
	}
	type queue struct {
		name string
		typ  sdkclient.TaskQueueType
	}
	queues := map[queue]bool{}
	for _, tq := range desc.Info.TaskQueuesInfos {
		queues[queue{tq.Name, tq.Type}] = true
	}
	activities := s.name + "-activities"
	if !queues[queue{s.name, sdkclient.TaskQueueTypeWorkflow}] || !queues[queue{activities, sdkclient.TaskQueueTypeActivity}] {
		s.t.Errorf("version task queues = %+v, want %s (workflow) and %s (activity)", queues, s.name, activities)
	}
	if queues[queue{activities, sdkclient.TaskQueueTypeWorkflow}] {
		s.t.Errorf("activity-only group registered %s as a workflow queue, so the gate would run there", activities)
	}
}

func (s *groupScenario) updateImage(ctx context.Context, image string) string {
	s.t.Helper()
	var buildID string
	eventually(s.t, 10*time.Second, time.Second, func() error {
		var latest temporaliov1alpha1.WorkerDeployment
		if err := s.k8sClient.Get(ctx, s.key, &latest); err != nil {
			return err
		}
		for i := range latest.Spec.WorkerGroups {
			latest.Spec.WorkerGroups[i].Deployment.Template.Spec.Containers[0].Image = image
		}
		buildID = k8s.ComputeBuildID(&latest)
		return s.k8sClient.Update(ctx, &latest)
	})
	return buildID
}

func (s *groupScenario) waitForDrained(ctx context.Context, buildID string) {
	s.t.Helper()
	eventually(s.t, 60*time.Second, time.Second, func() error {
		var wd temporaliov1alpha1.WorkerDeployment
		if err := s.k8sClient.Get(ctx, s.key, &wd); err != nil {
			return err
		}
		for _, v := range wd.Status.DeprecatedVersions {
			if v.BuildID == buildID && v.Status == temporaliov1alpha1.VersionStatusDrained {
				return nil
			}
		}
		return fmt.Errorf("version %s is not drained yet: %+v", buildID, wd.Status.DeprecatedVersions)
	})
}

func (s *groupScenario) assertSunsetTogether(ctx context.Context, v *groupVersion, hpaName string) {
	s.t.Helper()
	eventually(s.t, 90*time.Second, time.Second, func() error {
		for _, d := range []string{v.def.Name, v.activities.Name} {
			if err := s.k8sClient.Get(ctx, types.NamespacedName{Namespace: s.namespace, Name: d}, &appsv1.Deployment{}); err == nil {
				return fmt.Errorf("deployment %s still exists", d)
			} else if client.IgnoreNotFound(err) != nil {
				return err
			}
		}
		_, err := s.ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(s.deploymentName).
			DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: v.buildID})
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return fmt.Errorf("expected version %s to be deleted from Temporal, got %v", v.buildID, err)
		}
		return nil
	})
	eventually(s.t, 30*time.Second, time.Second, func() error {
		err := s.k8sClient.Get(ctx, types.NamespacedName{Namespace: s.namespace, Name: hpaName}, &autoscalingv2.HorizontalPodAutoscaler{})
		if err == nil {
			return errors.New("HPA of the sunset version still exists")
		}
		return client.IgnoreNotFound(err)
	})
}

// cleanup deletes the scenario's objects and waits for them to be gone, so later tests on
// the same server and namespace start clean.
func (s *groupScenario) cleanup(ctx context.Context, objs ...client.Object) {
	s.t.Helper()
	for _, obj := range objs {
		if err := s.k8sClient.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
			s.t.Errorf("failed to delete %s: %v", obj.GetName(), err)
		}
	}
	eventually(s.t, 90*time.Second, time.Second, func() error {
		for _, obj := range objs {
			err := s.k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj.DeepCopyObject().(client.Object))
			if err == nil {
				return fmt.Errorf("%s still exists", obj.GetName())
			}
			if client.IgnoreNotFound(err) != nil {
				return err
			}
		}
		return nil
	})
}

func testWorkerGroupsLifecycle(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, namespace string) {
	ctx := context.Background()
	s := &groupScenario{t: t, k8sClient: k8sClient, ts: ts, namespace: namespace, name: "groups"}
	s.key = types.NamespacedName{Namespace: namespace, Name: s.name}
	twd := s.workerDeployment("v1.0")
	s.deploymentName = k8s.ComputeWorkerDeploymentName(twd)

	connection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: twd.Spec.WorkerOptions.ConnectionRef.Name, Namespace: namespace},
		Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
	}
	wrt := makeHPAWRT(s.name+"-activities-hpa", namespace, s.name)
	wrt.Spec.WorkerGroup = activitiesGroup
	for _, obj := range []client.Object{connection, wrt, twd} {
		if err := k8sClient.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	// Deferred first so it runs after every worker is stopped.
	defer s.cleanup(ctx, twd, wrt, connection)

	t.Log("Every group of v1 gets its own Deployment with a group selector")
	v1 := s.waitForVersionDeployments(ctx, k8s.ComputeBuildID(twd))
	s.assertGroupSelectors(v1)

	t.Log("v1 is not promoted while its activities group is unavailable")
	v1.stopDef = s.startWorkers(ctx, v1.def)
	defer v1.stopDef()
	s.assertWaitingForActivitiesGroup(ctx, v1.buildID)

	t.Log("v1 is promoted once every group is available")
	v1.stopActs = s.startWorkers(ctx, v1.activities)
	defer v1.stopActs()
	if wd := s.waitForTarget(ctx, v1.buildID, temporaliov1alpha1.VersionStatusCurrent); len(wd.Status.TargetVersion.WorkerGroups) != 2 {
		t.Errorf("target version groups = %+v, want workflows and activities", wd.Status.TargetVersion.WorkerGroups)
	}

	t.Log("Both groups' task queues are in one Temporal version")
	s.assertTaskQueuesInOneVersion(ctx, v1.buildID)

	t.Log("The activities WRT targets only the activities group's Deployment")
	v1HPA := k8s.ComputeWorkerResourceTemplateName(s.name, wrt.Name, v1.buildID)
	waitForOwnedHPAWithInjectedScaleTargetRef(t, ctx, k8sClient, namespace, v1HPA, v1.activities.Name, 30*time.Second)

	t.Log("A new image rolls out as v2 across both groups")
	v2 := s.waitForVersionDeployments(ctx, s.updateImage(ctx, "v2.0"))
	v2.stopDef = s.startWorkers(ctx, v2.def)
	defer v2.stopDef()
	v2.stopActs = s.startWorkers(ctx, v2.activities)
	defer v2.stopActs()
	s.waitForTarget(ctx, v2.buildID, temporaliov1alpha1.VersionStatusCurrent)

	t.Log("Activities on the activities group run on the calling workflow's build")
	for _, v := range []*groupVersion{v1, v2} {
		if got := s.runCrossGroupWorkflow(ctx, v.buildID); got != v.buildID {
			t.Errorf("activity for a workflow pinned to %s ran on %s", v.buildID, got)
		}
	}

	t.Log("v1 sunsets every group together")
	s.waitForDrained(ctx, v1.buildID)
	v1.stopDef()
	v1.stopActs()
	scaleDeploymentToZero(t, ctx, k8sClient, v1.def.Name, namespace)
	scaleDeploymentToZero(t, ctx, k8sClient, v1.activities.Name, namespace)
	s.assertSunsetTogether(ctx, v1, v1HPA)
	for _, d := range []string{v2.def.Name, v2.activities.Name} {
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: d}, &appsv1.Deployment{}); err != nil {
			t.Errorf("v2 deployment %s: %v", d, err)
		}
	}
}
