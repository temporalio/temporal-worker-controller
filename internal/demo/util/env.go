// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"fmt"
	"os"
)

var (
	temporalHostPort  = mustGetEnv("TEMPORAL_HOST_PORT")
	temporalNamespace = mustGetEnv("TEMPORAL_NAMESPACE")
	temporalTaskQueue = mustGetEnv("TEMPORAL_TASK_QUEUE")
	tlsKeyFilePath    = mustGetEnv("TEMPORAL_TLS_KEY_PATH")
	tlsCertFilePath   = mustGetEnv("TEMPORAL_TLS_CERT_PATH")
)

func mustGetEnv(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	panic(fmt.Sprintf("environment variable %q must be set", key))
}
