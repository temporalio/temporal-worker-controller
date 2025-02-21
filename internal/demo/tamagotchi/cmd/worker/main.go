// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"log"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/DataDog/temporal-worker-controller/internal/demo/tamagotchi/nexus"
	tama "github.com/DataDog/temporal-worker-controller/internal/demo/tamagotchi/worker"
	"github.com/DataDog/temporal-worker-controller/internal/demo/util"
)

func main() {
	w, stopFunc := util.NewVersionedWorker(worker.Options{})
	defer stopFunc()

	// chat, err := tama.NewTamaChat(os.Getenv("OLLAMA_ENDPOINT"))
	// if err != nil {
	// 	log.Fatal(err)
	// }

	// Register activities and workflows
	w.RegisterWorkflow(tama.Tamagotchi)
	w.RegisterWorkflow(DeploymentGate)
	// chat.RegisterActivities(w)
	w.RegisterNexusService(nexus.NewTamagatchiService())

	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatal(err)
	}
}

func DeploymentGate(ctx workflow.Context) error {
	// Ensure that child workflows start on the current build ID (the one being tested).
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		VersioningIntent: temporal.VersioningIntentInheritBuildID,
	})

	// Start the Tamagotchi workflow
	tt := workflow.ExecuteChildWorkflow(ctx, tama.Tamagotchi, &tama.TamagotchiRequest{
		Name: "TamaTest",
	})
	var wf workflow.Execution
	if err := tt.GetChildWorkflowExecution().Get(ctx, &wf); err != nil {
		return err
	}

	// Send a feed update to the child workflow
	// TODO(jlegrone): Is there a way to ensure that the nexus operation is evaluated on the current build ID?
	if err := workflow.NewNexusClient("tamagotchi", "game").ExecuteOperation(ctx, "feed", &nexus.FeedOperationRequest{
		Entity:  &nexus.TamagotchiEntity{ID: wf.ID},
		Request: &tama.FeedRequest{Food: "apple"},
	}, workflow.NexusOperationOptions{
		ScheduleToCloseTimeout: time.Minute,
	}).Get(ctx, nil); err != nil {
		return err
	}

	// Wait for the child workflow to complete and return the final result
	if err := tt.Get(ctx, nil); err != nil {
		return err
	}

	return nil
}
