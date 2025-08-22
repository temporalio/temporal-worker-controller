package internal

import (
	"context"
	"sync"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	"go.temporal.io/sdk/worker"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false // completed normally
	case <-time.After(timeout):
		return true // timed out
	}
}

func applyDeployment(t *testing.T, ctx context.Context, k8sClient client.Client, deploymentName, namespace string) []func() {
	var deployment appsv1.Deployment
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: namespace,
	}, &deployment); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}

	var wg sync.WaitGroup
	stopFuncs := make([]func(), *(deployment.Spec.Replicas))
	workerErrors := make([]error, *(deployment.Spec.Replicas))
	workerCallback := func(i int32) func(func(), error) {
		return func(stopFunc func(), err error) {
			if err == nil {
				stopFuncs[i] = stopFunc
				wg.Done()
			} else {
				workerErrors[i] = err
			}
		}
	}

	for i := int32(0); i < *(deployment.Spec.Replicas); i++ {
		wg.Add(1)
		go testhelpers.RunHelloWorldWorker(ctx, deployment.Spec.Template, workerCallback(i))
	}

	// wait 10s for all expected workers to be healthy
	timedOut := waitTimeout(&wg, 10*time.Second)

	if timedOut {
		t.Fatalf("could not start workers, errors were: %+v", workerErrors)
	} else {
		setHealthyDeploymentStatus(t, ctx, k8sClient, deployment)
	}

	return stopFuncs
}

// Set deployment status to `DeploymentAvailable` to simulate a healthy deployment
// This is necessary because envtest doesn't actually start pods
func setHealthyDeploymentStatus(t *testing.T, ctx context.Context, k8sClient client.Client, deployment appsv1.Deployment) {
	now := metav1.Now()
	deployment.Status = appsv1.DeploymentStatus{
		Replicas:            *deployment.Spec.Replicas,
		UpdatedReplicas:     *deployment.Spec.Replicas,
		ReadyReplicas:       *deployment.Spec.Replicas,
		AvailableReplicas:   *deployment.Spec.Replicas,
		UnavailableReplicas: 0,
		Conditions: []appsv1.DeploymentCondition{
			{
				Type:               appsv1.DeploymentAvailable,
				Status:             corev1.ConditionTrue,
				LastUpdateTime:     now,
				LastTransitionTime: now,
				Reason:             "MinimumReplicasAvailable",
				Message:            "Deployment has minimum availability.",
			},
			{
				Type:               appsv1.DeploymentProgressing,
				Status:             corev1.ConditionTrue,
				LastUpdateTime:     now,
				LastTransitionTime: now,
				Reason:             "NewReplicaSetAvailable",
				Message:            "ReplicaSet is available.",
			},
		},
	}
	t.Logf("started %d healthy workers, updating deployment status", *deployment.Spec.Replicas)
	if err := k8sClient.Status().Update(ctx, &deployment); err != nil {
		t.Fatalf("failed to update deployment status: %v", err)
	}
}

// Uses input.Status + existingDeploymentReplicas to create (and maybe kill) pollers for deprecated versions in temporal
// also gets routing config of the deployment into the starting state before running the test.
// Does not set Status.VersionConflictToken, since that is only set internally by the server.
func makePreliminaryStatusTrue(
	ctx context.Context,
	t *testing.T,
	env testhelpers.TestEnv,
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
) {
	t.Logf("Creating starting test env based on input.Status")

	// Make a separate list of deferred functions, because calling defer in a for loop is not allowed.
	loopDefers := make([]func(), 0)
	defer handleStopFuncs(loopDefers)
	for _, dv := range twd.Status.DeprecatedVersions {
		t.Logf("Setting up deprecated version %v with status %v", dv.BuildID, dv.Status)
		workerStopFuncs := createStatus(ctx, t, env, twd, dv.BaseWorkerDeploymentVersion, nil)
		loopDefers = append(loopDefers, func() { handleStopFuncs(workerStopFuncs) })
	}

	if tv := twd.Status.TargetVersion; tv.BuildID != "" {
		t.Logf("Setting up target version %v with status %v", tv.BuildID, tv.Status)
		workerStopFuncs := createStatus(ctx, t, env, twd, tv.BaseWorkerDeploymentVersion, tv.RampPercentage)
		defer handleStopFuncs(workerStopFuncs)
	}
}

func handleStopFuncs(funcs []func()) {
	for _, f := range funcs {
		if f != nil {
			f()
		}
	}
}

// creates k8s deployment, pollers, and routing config state as needed.
func createStatus(
	ctx context.Context,
	t *testing.T,
	env testhelpers.TestEnv,
	newTWD *temporaliov1alpha1.TemporalWorkerDeployment,
	prevVersion temporaliov1alpha1.BaseWorkerDeploymentVersion,
	rampPercentage *float32,
) (workerStopFuncs []func()) {
	if prevVersion.Deployment != nil && prevVersion.Deployment.FieldPath == "create" {
		deploymentName := k8s.ComputeWorkerDeploymentName(newTWD)
		v := &worker.WorkerDeploymentVersion{
			DeploymentName: deploymentName,
			BuildId:        prevVersion.BuildID,
		}
		prevTWD := recreateTWD(newTWD, env.ExistingDeploymentImages[v.BuildId], env.ExistingDeploymentReplicas[v.BuildId])
		createWorkerDeployment(ctx, t, env, prevTWD, v.BuildId)
		expectedDeploymentName := k8s.ComputeVersionedDeploymentName(prevTWD.Name, k8s.ComputeBuildID(prevTWD))
		waitForDeployment(t, env.K8sClient, expectedDeploymentName, prevTWD.Namespace, 30*time.Second)
		if prevVersion.Status != temporaliov1alpha1.VersionStatusNotRegistered {
			workerStopFuncs = applyDeployment(t, ctx, env.K8sClient, expectedDeploymentName, prevTWD.Namespace)
		}

		switch prevVersion.Status {
		case temporaliov1alpha1.VersionStatusInactive, temporaliov1alpha1.VersionStatusNotRegistered:
			// no-op
		case temporaliov1alpha1.VersionStatusRamping:
			setRampingVersion(t, ctx, env.Ts, v.DeploymentName, v.BuildId, *rampPercentage) // rampPercentage won't be nil if the version is ramping
		case temporaliov1alpha1.VersionStatusCurrent:
			setCurrentVersion(t, ctx, env.Ts, v.DeploymentName, v.BuildId)
		case temporaliov1alpha1.VersionStatusDraining:
			setRampingVersion(t, ctx, env.Ts, v.DeploymentName, v.BuildId, 1)
			// TODO(carlydf): start a workflow on v that does not complete -> will never drain
			setRampingVersion(t, ctx, env.Ts, v.DeploymentName, "", 0)
		case temporaliov1alpha1.VersionStatusDrained:
			setCurrentVersion(t, ctx, env.Ts, v.DeploymentName, v.BuildId)
			setCurrentVersion(t, ctx, env.Ts, v.DeploymentName, "")
		}
	}

	return workerStopFuncs
}

// recreateTWD returns a copy of the given TWD, but replaces the build-id-generating image name with the given one,
// and the Spec.Replicas with the given replica count.
// Panics if the twd spec is nil, or if it has no containers, but that should never be true for these integration tests.
func recreateTWD(twd *temporaliov1alpha1.TemporalWorkerDeployment, imageName string, replicas int32) *temporaliov1alpha1.TemporalWorkerDeployment {
	ret := twd.DeepCopy()
	ret.Spec.Template.Spec.Containers[0].Image = imageName
	ret.Spec.Replicas = &replicas
	return ret
}

func createWorkerDeployment(
	ctx context.Context,
	t *testing.T,
	env testhelpers.TestEnv,
	twd *temporaliov1alpha1.TemporalWorkerDeployment,
	buildId string,
) {
	dep, err := k8s.NewDeploymentWithControllerRef(twd, buildId, env.Connection.Spec, env.Mgr.GetScheme())
	if err != nil {
		t.Fatalf("error creating Deployment spec: %v", err.Error())
	}

	t.Logf("Creating Deployment %s in namespace %s", dep.Name, dep.Namespace)

	if err := env.K8sClient.Create(ctx, dep); err != nil {
		t.Fatalf("failed to create Deployment: %v", err)
	}
}
