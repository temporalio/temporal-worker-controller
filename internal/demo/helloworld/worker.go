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
	workflow.GetLogger(ctx).Info("HelloWorld workflow started")
	ctx = util.SetActivityTimeout(ctx, 5*time.Minute)

	// Compute a subject
	var subject string
	if err := workflow.ExecuteActivity(ctx, GetSubject).Get(ctx, &subject); err != nil {
		return "", err
	}

	// Sleep for a while
	if err := workflow.Sleep(ctx, time.Minute); err != nil {
		return "", err
	}

	// Return the greeting
	return fmt.Sprintf("Hello %s", subject), nil
}

type GetSubjectResponse struct {
	Name string
}

func GetSubject(ctx context.Context) (GetSubjectResponse, error) {
	return GetSubjectResponse{
		Name: "World",
	}, nil
}
