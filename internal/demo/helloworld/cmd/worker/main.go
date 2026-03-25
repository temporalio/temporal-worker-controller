// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"log"

	"github.com/temporalio/temporal-worker-controller/internal/demo/helloworld"
	"github.com/temporalio/temporal-worker-controller/internal/demo/util"
	"go.temporal.io/sdk/worker"
)

func main() {
	// Limit activity slots to 5 per pod so the HPA demo reaches ~10 replicas at a
	// realistic load rate. Default (1000) would require thousands of concurrent workflows
	// to saturate even a single pod. Remove this limit in production.
	w, stopFunc := util.NewVersionedWorker(worker.Options{
		MaxConcurrentActivityExecutionSize: 5,
		MaxConcurrentActivityTaskPollers:   5,
		MaxConcurrentWorkflowTaskPollers:   2,
	})
	defer stopFunc()

	// Register activities and workflows
	w.RegisterWorkflow(helloworld.RolloutGate)
	w.RegisterWorkflow(helloworld.HelloWorld)
	w.RegisterActivity(helloworld.GetSubject)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}
