// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"log"

	"go.temporal.io/sdk/worker"
)

func main() {
	var (
		buildID           = mustGetEnv("WORKER_BUILD_ID")
		temporalHostPort  = mustGetEnv("TEMPORAL_HOST_PORT")
		temporalNamespace = mustGetEnv("TEMPORAL_NAMESPACE")
		temporalTaskQueue = mustGetEnv("TEMPORAL_TASK_QUEUE")
	)

	c, stopFunc := newClient(temporalHostPort, temporalNamespace, buildID)
	defer stopFunc()

	w := worker.New(c, temporalTaskQueue, worker.Options{
		BuildID:                 buildID,
		UseBuildIDForVersioning: true,
	})
	defer w.Stop()

	// Register activities and workflows
	w.RegisterWorkflow(HelloWorld)
	w.RegisterActivity(GetSubject)
	w.RegisterActivity(Sleep)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}
