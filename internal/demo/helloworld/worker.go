// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package helloworld

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/temporalio/temporal-worker-controller/internal/demo/util"
	"go.temporal.io/sdk/workflow"
)

func HelloWorld(ctx workflow.Context) (string, error) {
	ctx = util.SetActivityTimeout(ctx, 5*time.Minute)

	// Get a subject
	var subject GetSubjectResponse
	if err := workflow.ExecuteActivity(ctx, GetSubject).Get(ctx, &subject); err != nil {
		return "", err
	}

	// Return the greeting
	return fmt.Sprintf("Hello %s", subject.Name), nil
}

func GetSubject(ctx context.Context) (*GetSubjectResponse, error) {
	// Send heartbeats
	go util.AutoHeartbeat(ctx)

	// Get user via API
	subject, err := fetchUser(ctx, "https://jsonplaceholder.typicode.com")
	if err != nil {
		return nil, err
	}

	return &subject, nil
}

func RolloutGate(ctx workflow.Context) error {
	// Ensure that deploys fail fast rather than waiting forever if child workflow is blocked.
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowExecutionTimeout: time.Minute,
	})

	var greeting string
	if err := workflow.ExecuteChildWorkflow(ctx, HelloWorld).Get(ctx, &greeting); err != nil {
		return err
	}
	if !strings.HasPrefix(greeting, "Hello ") {
		return fmt.Errorf(`greeting does not have expect prefix "Hello "; got: %q`, greeting)
	}

	return nil
}
