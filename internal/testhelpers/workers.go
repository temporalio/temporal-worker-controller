package testhelpers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	corev1 "k8s.io/api/core/v1"
)

const (
	successTestWorkflowType = "successTestWorkflow"
	failTestWorkflowType    = "failTestWorkflow"
	// CrossPoolWorkflowType runs reportBuildID on the task queue passed as its argument and
	// returns the build ID of the worker that ran it.
	CrossPoolWorkflowType     = "crossPoolWorkflow"
	reportBuildIDActivityType = "reportBuildID"

	workerRoleEnvKey = "TEMPORAL_TEST_WORKER_ROLE"
	// ActivityWorkerRole makes a test worker register only reportBuildID, so it polls its task
	// queue for activities only.
	ActivityWorkerRole = "activities"
)

// SetWorkerRole sets the test worker role in every container of the pod template.
func SetWorkerRole(podSpec corev1.PodTemplateSpec, role string) corev1.PodTemplateSpec {
	return setEnv(podSpec, workerRoleEnvKey, role)
}

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
	workerBuildID, err := getEnv(podTemplateSpec, "TEMPORAL_WORKER_BUILD_ID")
	if err != nil {
		return nil, nil, err
	}
	temporalTaskQueue, err := getEnv(podTemplateSpec, taskQueueEnvKey)
	if err != nil {
		return nil, nil, err
	}
	temporalHostPort, err := getEnv(podTemplateSpec, "TEMPORAL_ADDRESS")
	if err != nil {
		return nil, nil, err
	}
	temporalNamespace, err := getEnv(podTemplateSpec, "TEMPORAL_NAMESPACE")
	if err != nil {
		return nil, nil, err
	}
	role, _ := getEnv(podTemplateSpec, workerRoleEnvKey)
	opts := versionedWorkerOptions(temporalDeploymentName, workerBuildID)
	// Without this the SDK still polls for workflow tasks, which registers the queue as a
	// workflow queue in the version.
	opts.DisableWorkflowWorker = role == ActivityWorkerRole
	return newWorker(ctx, temporalTaskQueue, temporalHostPort, temporalNamespace, opts)
}

// StartVersionedWorker creates a versioned worker and registers a dummy workflow on it.
// This is used to register a build ID with a Temporal worker deployment to set LastCurrentTime.
// Returns a stop function that must be called when done.
func StartVersionedWorker(ctx context.Context, temporalDeploymentName, workerBuildID, temporalTaskQueue, temporalHostPort, temporalNamespace string) (stopFunc func(), err error) {
	w, stop, err := NewWorker(ctx, temporalDeploymentName, workerBuildID, temporalTaskQueue, temporalHostPort, temporalNamespace, true)
	if err != nil {
		return nil, err
	}
	w.RegisterWorkflow(func(workflow.Context) error { return nil })
	if err := w.Start(); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

func NewWorker(
	ctx context.Context,
	temporalDeploymentName, workerBuildID, temporalTaskQueue, temporalHostPort, temporalNamespace string,
	versioned bool,
) (w worker.Worker, stopFunc func(), err error) {
	opts := worker.Options{}
	if versioned {
		opts = versionedWorkerOptions(temporalDeploymentName, workerBuildID)
	}
	return newWorker(ctx, temporalTaskQueue, temporalHostPort, temporalNamespace, opts)
}

func versionedWorkerOptions(temporalDeploymentName, workerBuildID string) worker.Options {
	return worker.Options{
		DeploymentOptions: worker.DeploymentOptions{
			UseVersioning: true,
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: temporalDeploymentName,
				BuildID:        workerBuildID,
			},
			DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
		},
	}
}

func newWorker(ctx context.Context, temporalTaskQueue, temporalHostPort, temporalNamespace string, opts worker.Options) (worker.Worker, func(), error) {
	c, err := newClient(ctx, temporalHostPort, temporalNamespace)
	if err != nil {
		return nil, nil, err
	}

	w := worker.New(c, temporalTaskQueue, opts)

	return w, func() {
		w.Stop()
	}, nil
}

func newClient(ctx context.Context, hostPort, namespace string) (client.Client, error) {
	opts := client.Options{
		Identity:  "integration-tests",
		HostPort:  hostPort,
		Namespace: namespace,
		Logger: log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			AddSource:   false,
			Level:       slog.LevelWarn, // Set to warn level to reduce noise in tests
			ReplaceAttr: nil,
		}))),
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
	if err != nil {
		return
	}
	buildID, _ := getEnv(podTemplateSpec, "TEMPORAL_WORKER_BUILD_ID")
	reportBuildID := func(context.Context) (string, error) { return buildID, nil }

	if role, _ := getEnv(podTemplateSpec, workerRoleEnvKey); role == ActivityWorkerRole {
		w.RegisterActivityWithOptions(reportBuildID, activity.RegisterOptions{Name: reportBuildIDActivityType})
	} else {
		// Register activities and workflows
		w.RegisterWorkflowWithOptions(successTestWorkflow, workflow.RegisterOptions{Name: successTestWorkflowType})
		w.RegisterWorkflowWithOptions(failTestWorkflow, workflow.RegisterOptions{Name: failTestWorkflowType})
		w.RegisterWorkflowWithOptions(crossPoolWorkflow, workflow.RegisterOptions{Name: CrossPoolWorkflowType})
		w.RegisterActivity(getSubjectTestActivity)
		w.RegisterActivity(sleepTestActivity)
	}

	// Start the worker in a separate goroutine so that the stopFunc can be passed back to the caller via callback
	go func() {
		err = w.Start()
		if err != nil {
			callback(nil, err)
		} else {
			callback(stopFunc, nil)
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

func crossPoolWorkflow(ctx workflow.Context, activityTaskQueue string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           activityTaskQueue,
		StartToCloseTimeout: time.Minute,
	})
	var buildID string
	err := workflow.ExecuteActivity(ctx, reportBuildIDActivityType).Get(ctx, &buildID)
	return buildID, err
}

func sleepTestActivity(ctx context.Context, seconds uint) error {
	time.Sleep(time.Duration(seconds) * time.Second)
	return nil
}

func getSubjectTestActivity(ctx context.Context) (string, error) {
	return "World10", nil
}
