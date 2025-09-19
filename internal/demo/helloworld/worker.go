// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package helloworld

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/temporalio/temporal-worker-controller/internal/demo/util"
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
