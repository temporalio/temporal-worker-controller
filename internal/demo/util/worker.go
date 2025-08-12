// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"net/http"
	"time"

	"go.temporal.io/sdk/worker"
)

func NewVersionedWorker(opts worker.Options) (w worker.Worker, stopFunc func()) {
	go func() {
		// Delay pod readiness by 5 seconds. During demos this provides time to talk through how
		// the controller waits for a safe point in the worker lifecycle before making a new
		// version the default.
		time.Sleep(5 * time.Second)

		if err := http.ListenAndServe("0.0.0.0:8080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})); err != nil {
			panic(err)
		}
	}()

	//opts.DeploymentOptions = worker.DeploymentOptions{
	//	UseVersioning: true,
	//	Version: worker.WorkerDeploymentVersion{
	//		DeploymentName: mustGetEnv("TEMPORAL_DEPLOYMENT_NAME"),
	//		BuildId:        mustGetEnv("WORKER_BUILD_ID"),
	//	},
	//	DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
	//}

	//c, stopClient := NewClient(mustGetEnv("WORKER_BUILD_ID"))
	c, stopClient := NewClient("")

	w = worker.New(c, temporalTaskQueue, opts)

	return w, func() {
		w.Stop()
		stopClient()
	}
}
