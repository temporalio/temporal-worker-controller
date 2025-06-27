package tests

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	corev1 "k8s.io/api/core/v1"
)

func mustGetEnv(podTemplateSpec corev1.PodTemplateSpec, key string) string {
	for _, e := range podTemplateSpec.Spec.Containers[0].Env {
		if e.Name == key {
			return e.Value
		}
	}
	panic(fmt.Errorf("environment variable %q must be set", key))
}

func newVersionedWorker(ctx context.Context, podTemplateSpec corev1.PodTemplateSpec) (w worker.Worker, stopFunc func()) {
	temporalDeploymentName := mustGetEnv(podTemplateSpec, "TEMPORAL_DEPLOYMENT_NAME")
	workerBuildId := mustGetEnv(podTemplateSpec, "WORKER_BUILD_ID")
	temporalTaskQueue := mustGetEnv(podTemplateSpec, "TEMPORAL_TASK_QUEUE")
	temporalHostPort := mustGetEnv(podTemplateSpec, "TEMPORAL_HOST_PORT")
	temporalNamespace := mustGetEnv(podTemplateSpec, "TEMPORAL_NAMESPACE")

	opts := worker.Options{
		DeploymentOptions: worker.DeploymentOptions{
			UseVersioning:             true,
			Version:                   temporalDeploymentName + "." + workerBuildId,
			DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
		},
	}

	c, stopClient := newClient(ctx, temporalHostPort, temporalNamespace)

	w = worker.New(c, temporalTaskQueue, opts)

	return w, func() {
		w.Stop()
		stopClient()
	}
}

func newClient(ctx context.Context, hostPort, namespace string) (c client.Client, stopFunc func()) {
	opts := client.Options{
		Identity:  "integration-tests",
		HostPort:  hostPort,
		Namespace: namespace,
		Logger:    nil, // do we want a test-related logger?
	}
	c, err := client.Dial(opts)
	if err != nil {
		panic(fmt.Errorf("failed to dial server: %v", err))
	}

	if _, err := c.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		panic(fmt.Errorf("failed to check health for server client: %v", err))
	}

	if _, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: namespace,
	}); err != nil {
		panic(fmt.Errorf("failed to list workflows with server client: %v", err))
	}

	return c, stopFunc
}

func runHelloWorldWorker(ctx context.Context, podTemplateSpec corev1.PodTemplateSpec) {
	w, stopFunc := newVersionedWorker(ctx, podTemplateSpec)
	defer stopFunc()

	// todo: periodically check Deployment, and if replicas go down or up, or are deleted, scale these

	getSubject := func(ctx context.Context) (string, error) {
		return "World10", nil
	}

	sleep := func(ctx context.Context, seconds uint) error {
		time.Sleep(time.Duration(seconds) * time.Second)
		return nil
		//return temporal.NewNonRetryableApplicationError("oops", "", nil)
	}

	helloWorld := func(ctx workflow.Context) (string, error) {
		workflow.GetLogger(ctx).Info("HelloWorld workflow started")
		ctx = setActivityTimeout(ctx, 5*time.Minute)

		// Compute a subject
		var subject string
		if err := workflow.ExecuteActivity(ctx, getSubject).Get(ctx, &subject); err != nil {
			return "", err
		}

		// Sleep for a while
		if err := workflow.ExecuteActivity(ctx, sleep, 60).Get(ctx, nil); err != nil {
			return "", err
		}

		// Return the greeting
		return fmt.Sprintf("Hello %s", subject), nil
	}

	// Register activities and workflows
	w.RegisterWorkflow(helloWorld)
	w.RegisterActivity(getSubject)
	w.RegisterActivity(sleep)

	if err := w.Run(worker.InterruptCh()); err != nil {
		panic(fmt.Errorf("error running worker: %v", err))
	}
}

func setActivityTimeout(ctx workflow.Context, d time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: d,
	})
}
