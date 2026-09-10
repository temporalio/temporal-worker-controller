package internal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.temporal.io/server/temporaltest"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A superseded rollout that was never Current or Ramping stays Inactive forever.
// Exercise real version registration, visibility, poller expiry and DeleteVersion,
// including a pinned override on a version which has never received routed traffic.
func testInactiveVersionRetirement(t *testing.T, k8sClient client.Client, ts *temporaltest.TestServer, namespace string, pinned bool) {
	ctx := context.Background()
	name := fmt.Sprintf("inactive-retire-%t", pinned)
	tc := testhelpers.NewTestCase().WithInput(testhelpers.NewWorkerDeploymentBuilder().
		WithManualStrategy().WithTargetTemplate("v1.0")).
		BuildWithValues(name, namespace, ts.GetDefaultNamespace())
	twd := tc.GetTWD()
	connection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: twd.Spec.WorkerOptions.ConnectionRef.Name, Namespace: namespace},
		Spec:       temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
	}
	if err := k8sClient.Create(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatal(err)
	}
	deploymentName := k8s.ComputeWorkerDeploymentName(twd)
	buildID := k8s.ComputeBuildID(twd)
	oldKey := types.NamespacedName{Namespace: namespace, Name: k8s.ComputeVersionedDeploymentName(twd.Name, buildID)}
	var old appsv1.Deployment
	eventually(t, 30*time.Second, time.Second, func() error { return k8sClient.Get(ctx, oldKey, &old) })
	w, stop, err := testhelpers.NewWorker(ctx, deploymentName, buildID, name, ts.GetFrontendHostPort(), ts.GetDefaultNamespace(), true)
	if err != nil {
		t.Fatal(err)
	}
	stopOnce := sync.OnceFunc(stop)
	defer stopOnce()
	w.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
		return workflow.Await(ctx, func() bool { return false })
	}, workflow.RegisterOptions{Name: "inactivePinnedWorkflow"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	setHealthyDeploymentStatus(t, ctx, k8sClient, old)
	version := sdkworker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: buildID}
	waitForVersionRegistrationInDeployment(t, ctx, ts, &version)
	handle := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(deploymentName)

	var run sdkclient.WorkflowRun
	if pinned {
		run, err = ts.GetDefaultClient().ExecuteWorkflow(ctx, sdkclient.StartWorkflowOptions{
			ID: name, TaskQueue: name, VersioningOverride: &sdkclient.PinnedVersioningOverride{Version: version},
		}, "inactivePinnedWorkflow")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ts.GetDefaultClient().TerminateWorkflow(ctx, run.GetID(), run.GetRunID(), "test cleanup") }()
		// Wait for the actual visibility index, not just the start RPC, before retiring.
		eventually(t, 30*time.Second, time.Second, func() error {
			count, err := ts.GetDefaultClient().CountWorkflow(ctx, &workflowservice.CountWorkflowExecutionsRequest{
				Query: fmt.Sprintf("WorkflowId = '%s' AND TemporalWorkflowVersioningBehavior = 'Pinned' AND ExecutionStatus = 'Running'", name),
			})
			if err != nil {
				return err
			}
			if count.Count != 1 {
				return fmt.Errorf("pinned workflow not yet visible")
			}
			return nil
		})
	}

	// Replace the target before ever making v1 current or ramping.
	var next temporaliov1alpha1.WorkerDeployment
	key := types.NamespacedName{Name: twd.Name, Namespace: namespace}
	if err := k8sClient.Get(ctx, key, &next); err != nil {
		t.Fatal(err)
	}
	next.Spec.Template.Spec.Containers[0].Image = "v2.0"
	newBuildID := k8s.ComputeBuildID(&next)
	if err := k8sClient.Update(ctx, &next); err != nil {
		t.Fatal(err)
	}
	newKey := types.NamespacedName{Name: k8s.ComputeVersionedDeploymentName(twd.Name, newBuildID), Namespace: namespace}
	eventually(t, 30*time.Second, time.Second, func() error {
		var dep appsv1.Deployment
		return k8sClient.Get(ctx, newKey, &dep)
	})
	stops := applyDeployment(t, ctx, k8sClient, newKey.Name, namespace)
	defer handleStopFuncs(stops)
	setCurrentVersion(t, ctx, ts, deploymentName, newBuildID)
	eventually(t, 30*time.Second, time.Second, func() error {
		if err := k8sClient.Get(ctx, key, &next); err != nil {
			return err
		}
		for _, v := range next.Status.DeprecatedVersions {
			if v.BuildID == buildID && v.Status == temporaliov1alpha1.VersionStatusInactive && v.DrainedSince == nil {
				return nil
			}
		}
		return fmt.Errorf("superseded version is not yet Inactive")
	})
	stopOnce()
	scaleDeploymentToZero(t, ctx, k8sClient, oldKey.Name, namespace)
	if pinned {
		// Longer than both the poller TTL and several controller reconciliations.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
			if err := k8sClient.Get(ctx, oldKey, &old); err != nil {
				t.Fatalf("deleted a version with a pinned workflow: %v", err)
			}
			if _, err := handle.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: buildID}); err != nil {
				t.Fatal(err)
			}
		}
		if err := ts.GetDefaultClient().TerminateWorkflow(ctx, run.GetID(), run.GetRunID(), "release pinned version"); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 90*time.Second, time.Second, func() error {
		if err := k8sClient.Get(ctx, oldKey, &old); err == nil {
			return fmt.Errorf("superseded Inactive Deployment still exists")
		} else if client.IgnoreNotFound(err) != nil {
			return err
		}
		_, err := handle.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: buildID})
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return fmt.Errorf("expected retired Temporal version, got %v", err)
		}
		return nil
	})
	var current appsv1.Deployment
	if err := k8sClient.Get(ctx, newKey, &current); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: newBuildID}); err != nil {
		t.Fatal(err)
	}
}
