package testhelpers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	corev1 "k8s.io/api/core/v1"
)

const (
	successTestWorkflowType = "successTestWorkflow"
	failTestWorkflowType    = "failTestWorkflow"
)

func getEnv(podTemplateSpec corev1.PodTemplateSpec, key string) (string, error) {
	for _, e := range podTemplateSpec.Spec.Containers[0].Env {
		if e.Name == key {
			return e.Value, nil
		}
	}
	return "", fmt.Errorf("environment variable %q must be set", key)
}

// Errors returned by this function are passed back to test output and fail.
func newVersionedWorker(ctx context.Context, podTemplateSpec corev1.PodTemplateSpec) (w worker.Worker, stopFunc func(), err error) {
	temporalDeploymentName, err := getEnv(podTemplateSpec, "TEMPORAL_DEPLOYMENT_NAME")
	if err != nil {
		return nil, nil, err
	}
	workerBuildId, err := getEnv(podTemplateSpec, "WORKER_BUILD_ID")
	if err != nil {
		return nil, nil, err
	}
	temporalTaskQueue, err := getEnv(podTemplateSpec, "TEMPORAL_TASK_QUEUE")
	if err != nil {
		return nil, nil, err
	}
	temporalHostPort, err := getEnv(podTemplateSpec, "TEMPORAL_HOST_PORT")
	if err != nil {
		return nil, nil, err
	}
	temporalNamespace, err := getEnv(podTemplateSpec, "TEMPORAL_NAMESPACE")
	if err != nil {
		return nil, nil, err
	}

	opts := worker.Options{
		DeploymentOptions: worker.DeploymentOptions{
			UseVersioning: true,
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: temporalDeploymentName,
				BuildId:        workerBuildId,
			},
			DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
		},
	}

	c, err := newClient(ctx, temporalHostPort, temporalNamespace)
	if err != nil {
		return nil, nil, err
	}

	w = worker.New(c, temporalTaskQueue, opts)

	return w, func() {
		w.Stop()
	}, nil
}

func newClient(ctx context.Context, hostPort, namespace string) (client.Client, error) {
	opts := client.Options{
		Identity:  "integration-tests",
		HostPort:  hostPort,
		Namespace: namespace,
		Logger:    nil,
	}
	c, err := client.Dial(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to dial server: %v", err)
	}

	if _, err := c.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		return nil, fmt.Errorf("failed to check health for server client: %v", err)
	}

	if _, err := c.ListWorkflow(ctx, &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: namespace,
	}); err != nil {
		return nil, fmt.Errorf("failed to list workflows with server client: %v", err)
	}

	return c, nil
}

// RunHelloWorldWorker runs one worker per replica in the pod spec. callback is a function that can be called multiple times.
func RunHelloWorldWorker(ctx context.Context, podTemplateSpec corev1.PodTemplateSpec, callback func(stopFunc func(), err error)) {
	w, stopFunc, err := newVersionedWorker(ctx, podTemplateSpec)
	defer func() {
		callback(stopFunc, err)
	}()
	if err != nil {
		return
	}

	// Register activities and workflows
	w.RegisterWorkflowWithOptions(successTestWorkflow, workflow.RegisterOptions{Name: successTestWorkflowType})
	w.RegisterWorkflowWithOptions(failTestWorkflow, workflow.RegisterOptions{Name: failTestWorkflowType})
	w.RegisterActivity(getSubjectTestActivity)
	w.RegisterActivity(sleepTestActivity)

	// Start the worker in a separate goroutine so that the stopFunc can be passed back to the caller via callback
	go func() {
		err = w.Start()
		if err != nil {
			callback(nil, err)
		}
	}()
}

func setActivityTimeout(ctx workflow.Context, d time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: d,
	})
}

func successTestWorkflow(ctx workflow.Context) (string, error) {
	workflow.GetLogger(ctx).Info("HelloWorld(success) workflow started")
	ctx = setActivityTimeout(ctx, 5*time.Minute)

	// Compute a subject
	var subject string
	if err := workflow.ExecuteActivity(ctx, getSubjectTestActivity).Get(ctx, &subject); err != nil {
		return "", err
	}

	// Sleep for a while
	if err := workflow.ExecuteActivity(ctx, sleepTestActivity, 5).Get(ctx, nil); err != nil {
		return "", err
	}

	// Return the greeting
	return fmt.Sprintf("Hello %s", subject), nil
}

func failTestWorkflow(ctx workflow.Context) (string, error) {
	workflow.GetLogger(ctx).Info("HelloWorld(fail) workflow started")
	ctx = setActivityTimeout(ctx, 5*time.Minute)

	// Compute a subject
	var subject string
	if err := workflow.ExecuteActivity(ctx, getSubjectTestActivity).Get(ctx, &subject); err != nil {
		return "", err
	}

	// Sleep for a while
	if err := workflow.ExecuteActivity(ctx, sleepTestActivity, 5).Get(ctx, nil); err != nil {
		return "", err
	}

	// Return the greeting
	return "", errors.New("this is a manufactured error to make the test fail")
}

func sleepTestActivity(ctx context.Context, seconds uint) error {
	time.Sleep(time.Duration(seconds) * time.Second)
	return nil
}

func getSubjectTestActivity(ctx context.Context) (string, error) {
	return "World10", nil
}
