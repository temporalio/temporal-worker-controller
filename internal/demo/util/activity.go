// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

func SetActivityTimeout(ctx workflow.Context, d time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: d,
	})
}
