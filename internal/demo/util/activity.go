// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func SetActivityTimeout(ctx workflow.Context, startToClose time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: startToClose,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			BackoffCoefficient: 1, // avoid long backoff -- helps demonstrate recovery quickly in demos.
		},
	})
}

// AutoHeartbeat sends activity heartbeats in a loop until the activity completes. It is
// meant to be run in a background goroutine. Example usage:
//
//	func MyActivity(ctx context.Context) error {
//	    go AutoHeartbeat(ctx)
//	    // ...
//	    return nil
//	}
func AutoHeartbeat(ctx context.Context) {
	timeout := activity.GetInfo(ctx).HeartbeatTimeout
	// Return early if there is no heartbeat timeout
	if timeout == 0 {
		return
	}

	ticker := time.NewTicker(timeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context is cancelled or deadline exceeded, exit the loop
			return
		case <-ticker.C:
			// Send heartbeat every 10 seconds
			activity.RecordHeartbeat(ctx)
		}
	}
}
