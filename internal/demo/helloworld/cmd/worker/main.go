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
	w, stopFunc := util.NewVersionedWorker(worker.Options{})
	defer stopFunc()

	// Register activities and workflows
	w.RegisterWorkflow(helloworld.RolloutGate)
	w.RegisterWorkflow(helloworld.HelloWorld)
	w.RegisterWorkflow(helloworld.LoadTestWorkflow)
	w.RegisterActivity(helloworld.GetSubject)
	w.RegisterActivity(helloworld.LoadTestActivity)

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}
