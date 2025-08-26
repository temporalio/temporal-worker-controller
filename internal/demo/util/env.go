// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"fmt"
	"os"
)

var (
	// temporalTaskQueue is still needed for worker creation as it's not part of client config
	temporalTaskQueue = mustGetEnv("TEMPORAL_TASK_QUEUE")
)

func mustGetEnv(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	panic(fmt.Sprintf("environment variable %q must be set", key))
}
