// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package internal

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	sdkworker "go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"go.temporal.io/server/temporaltest"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const issue550PinnedWorkflowType = "issue550PinnedWorkflow"

type versionSummaryFilteringClient struct {
	sdkclient.Client
	workflowService workflowservice.WorkflowServiceClient
}

func (c *versionSummaryFilteringClient) WorkflowService() workflowservice.WorkflowServiceClient {
	return c.workflowService
}

type versionSummaryFilteringWorkflowService struct {
	workflowservice.WorkflowServiceClient
	deploymentName string
	omitBuildID    string
	filteredCalls  atomic.Int32
}

func (s *versionSummaryFilteringWorkflowService) DescribeWorkerDeployment(
	ctx context.Context,
	req *workflowservice.DescribeWorkerDeploymentRequest,
	opts ...grpc.CallOption,
) (*workflowservice.DescribeWorkerDeploymentResponse, error) {
	resp, err := s.WorkflowServiceClient.DescribeWorkerDeployment(ctx, req, opts...)
	if err != nil || req.GetDeploymentName() != s.deploymentName {
		return resp, err
	}

	filtered := proto.Clone(resp).(*workflowservice.DescribeWorkerDeploymentResponse)
	info := filtered.GetWorkerDeploymentInfo()
	if info == nil {
		return filtered, nil
	}

	summaries := info.VersionSummaries[:0]
	omitted := false
	for _, summary := range info.VersionSummaries {
		if summary.GetDeploymentVersion().GetBuildId() == s.omitBuildID {
			omitted = true
			continue
		}
		summaries = append(summaries, summary)
	}
	info.VersionSummaries = summaries
	if omitted {
		s.filteredCalls.Add(1)
	}

	return filtered, nil
}

func runNotRegisteredVersionTests(
	t *testing.T,
	k8sClient client.Client,
	clientPool *clientpool.ClientPool,
	ts *temporaltest.TestServer,
	namespace string,
) {
	t.Run("missing-summary-version-is-described-before-deletion", func(t *testing.T) {
		testMissingSummaryVersionIsDescribedBeforeDeletion(t, k8sClient, clientPool, ts, namespace)
	})
}

func testMissingSummaryVersionIsDescribedBeforeDeletion(
	t *testing.T,
	k8sClient client.Client,
	clientPool *clientpool.ClientPool,
	ts *temporaltest.TestServer,
	namespace string,
) {
	ctx := context.Background()
	testName := "notregistered-describe"

	tc := testhelpers.NewTestCase().
		WithInput(
			testhelpers.NewWorkerDeploymentBuilder().
				WithManualStrategy().
				WithTargetTemplate("v1.0"),
		).
		BuildWithValues(testName, namespace, ts.GetDefaultNamespace())
	twd := tc.GetTWD()
	twd.Spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{Duration: time.Hour}
	twd.Spec.SunsetStrategy.DeleteDelay = &metav1.Duration{Duration: time.Hour}

	temporalConnection := &temporaliov1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      twd.Spec.WorkerOptions.ConnectionRef.Name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.ConnectionSpec{HostPort: ts.GetFrontendHostPort()},
	}
	if err := k8sClient.Create(ctx, temporalConnection); err != nil {
		t.Fatalf("failed to create Connection: %v", err)
	}
	if err := k8sClient.Create(ctx, twd); err != nil {
		t.Fatalf("failed to create WorkerDeployment: %v", err)
	}

	workerDeploymentName := k8s.ComputeWorkerDeploymentName(twd)
	buildIDv1 := k8s.ComputeBuildID(twd)
	deploymentNameV1 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv1)
	eventually(t, 30*time.Second, time.Second, func() error {
		var deployment appsv1.Deployment
		return k8sClient.Get(ctx, types.NamespacedName{Name: deploymentNameV1, Namespace: namespace}, &deployment)
	})

	v1Worker, stopV1, err := testhelpers.NewWorker(
		ctx,
		workerDeploymentName,
		buildIDv1,
		testName,
		ts.GetFrontendHostPort(),
		ts.GetDefaultNamespace(),
		true,
	)
	if err != nil {
		t.Fatalf("failed to create v1 worker: %v", err)
	}
	defer stopV1()
	v1Worker.RegisterWorkflowWithOptions(
		func(ctx workflow.Context) error {
			return workflow.Await(ctx, func() bool { return false })
		},
		workflow.RegisterOptions{Name: issue550PinnedWorkflowType},
	)
	if err := v1Worker.Start(); err != nil {
		t.Fatalf("failed to start v1 worker: %v", err)
	}

	var deploymentV1 appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentNameV1, Namespace: namespace}, &deploymentV1); err != nil {
		t.Fatalf("failed to get v1 Deployment: %v", err)
	}
	setHealthyDeploymentStatus(t, ctx, k8sClient, deploymentV1)
	waitForVersionRegistrationInDeployment(t, ctx, ts, &sdkworker.WorkerDeploymentVersion{
		DeploymentName: workerDeploymentName,
		BuildID:        buildIDv1,
	})
	deploymentHandle := ts.GetDefaultClient().WorkerDeploymentClient().GetHandle(workerDeploymentName)
	eventually(t, 60*time.Second, time.Second, func() error {
		desc, err := deploymentHandle.DescribeVersion(
			ctx,
			sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: buildIDv1},
		)
		if err != nil {
			return err
		}
		for _, taskQueue := range desc.Info.TaskQueuesInfos {
			if taskQueue.Name == testName && taskQueue.Type == sdkclient.TaskQueueTypeWorkflow {
				return nil
			}
		}
		return fmt.Errorf("v1 workflow task queue is not yet registered")
	})
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildIDv1)

	workflowRun, err := ts.GetDefaultClient().ExecuteWorkflow(
		ctx,
		sdkclient.StartWorkflowOptions{
			ID:        testName + "-pinned",
			TaskQueue: testName,
			VersioningOverride: &sdkclient.PinnedVersioningOverride{
				Version: sdkworker.WorkerDeploymentVersion{
					DeploymentName: workerDeploymentName,
					BuildID:        buildIDv1,
				},
			},
		},
		issue550PinnedWorkflowType,
	)
	if err != nil {
		t.Fatalf("failed to start pinned workflow: %v", err)
	}
	defer func() {
		_ = ts.GetDefaultClient().TerminateWorkflow(
			context.Background(),
			workflowRun.GetID(),
			workflowRun.GetRunID(),
			"integration test cleanup",
		)
	}()

	var twdV2 temporaliov1alpha1.WorkerDeployment
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &twdV2); err != nil {
		t.Fatalf("failed to get WorkerDeployment for v2 update: %v", err)
	}
	twdV2.Spec.Template.Spec.Containers[0].Image = "v2.0"
	buildIDv2 := k8s.ComputeBuildID(&twdV2)
	deploymentNameV2 := k8s.ComputeVersionedDeploymentName(twd.Name, buildIDv2)
	if err := k8sClient.Update(ctx, &twdV2); err != nil {
		t.Fatalf("failed to update WorkerDeployment to v2: %v", err)
	}
	eventually(t, 30*time.Second, time.Second, func() error {
		var deployment appsv1.Deployment
		return k8sClient.Get(ctx, types.NamespacedName{Name: deploymentNameV2, Namespace: namespace}, &deployment)
	})
	stopV2 := applyDeployment(t, ctx, k8sClient, deploymentNameV2, namespace)
	defer handleStopFuncs(stopV2)
	setCurrentVersion(t, ctx, ts, workerDeploymentName, buildIDv2)

	eventually(t, 60*time.Second, time.Second, func() error {
		desc, err := deploymentHandle.DescribeVersion(
			ctx,
			sdkclient.WorkerDeploymentDescribeVersionOptions{BuildID: buildIDv1},
		)
		if err != nil {
			return err
		}
		if desc.Info.DrainageInfo == nil ||
			desc.Info.DrainageInfo.DrainageStatus != sdkclient.WorkerDeploymentVersionDrainageStatusDraining {
			return fmt.Errorf("v1 version is not yet draining")
		}
		return nil
	})

	poolKey := clientpool.ClientPoolKey{
		HostPort:  temporalConnection.Spec.HostPort,
		Namespace: twd.Spec.WorkerOptions.TemporalNamespace,
		AuthMode:  temporaliov1alpha1.AuthModeNoCredentials,
	}
	originalClient, ok := clientPool.GetSDKClient(poolKey)
	if !ok {
		t.Fatal("controller Temporal client was not present in the client pool")
	}
	filteringService := &versionSummaryFilteringWorkflowService{
		WorkflowServiceClient: originalClient.WorkflowService(),
		deploymentName:        workerDeploymentName,
		omitBuildID:           buildIDv1,
	}
	clientPool.SetClientForTesting(poolKey, &versionSummaryFilteringClient{
		Client:          originalClient,
		workflowService: filteringService,
	})
	defer clientPool.SetClientForTesting(poolKey, originalClient)

	// Wait for multiple reconciliations against the filtered summary. Without the
	// DescribeVersion fallback, the first one classifies v1 as NotRegistered and
	// deletes its Kubernetes Deployment even though its pinned workflow is still open.
	eventually(t, 30*time.Second, time.Second, func() error {
		if filteringService.filteredCalls.Load() < 2 {
			return fmt.Errorf("waiting for reconciliations with the filtered version summary")
		}

		var deployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentNameV1, Namespace: namespace}, &deployment); err != nil {
			return fmt.Errorf("v1 Deployment was deleted: %w", err)
		}
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
			return fmt.Errorf("v1 Deployment replicas = %v, want 1", deployment.Spec.Replicas)
		}

		var current temporaliov1alpha1.WorkerDeployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: twd.Name, Namespace: namespace}, &current); err != nil {
			return err
		}
		for _, version := range current.Status.DeprecatedVersions {
			if version.BuildID == buildIDv1 {
				if version.Status != temporaliov1alpha1.VersionStatusDraining {
					return fmt.Errorf("v1 status = %s, want Draining", version.Status)
				}
				return nil
			}
		}
		return fmt.Errorf("v1 is missing from deprecated versions")
	})
}
