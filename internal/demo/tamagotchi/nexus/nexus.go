package nexus

import (
	"context"
	"os"
	"time"

	"github.com/DataDog/temporal-worker-controller/internal/demo/tamagotchi/worker"
	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporalnexus"
)

type HatchRequest struct {
	Name string `json:"name"`
}

type TamagotchiEntity struct {
	ID string `json:"id"`
}

type FeedOperationRequest struct {
	Entity  *TamagotchiEntity   `json:"entity"`
	Request *worker.FeedRequest `json:"request"`
}

type PlayOperationRequest struct {
	Entity  *TamagotchiEntity   `json:"entity"`
	Request *worker.PlayRequest `json:"request"`
}

var HatchOperation = temporalnexus.NewWorkflowRunOperation("hatch", worker.Tamagotchi, func(ctx context.Context, input *worker.TamagotchiRequest, options nexus.StartOperationOptions) (client.StartWorkflowOptions, error) {
	return client.StartWorkflowOptions{
		ID:                       options.RequestID,
		TaskQueue:                os.Getenv("TEMPORAL_TASK_QUEUE"),
		WorkflowExecutionTimeout: time.Hour,
	}, nil
})

var GetStatsOperation = temporalnexus.NewSyncOperation("get-stats", func(ctx context.Context, c client.Client, input *TamagotchiEntity, opts nexus.StartOperationOptions) (*worker.TamagotchiState, error) {
	res, err := c.QueryWorkflow(ctx, input.ID, "", "stats", nil)
	if err != nil {
		return nil, err
	}

	var s worker.TamagotchiState
	if err := res.Get(&s); err != nil {
		return nil, err
	}
	return &s, nil
})

var FeedOperation = temporalnexus.NewSyncOperation("feed", func(ctx context.Context, c client.Client, input *FeedOperationRequest, opts nexus.StartOperationOptions) (*worker.TamagotchiState, error) {
	res, err := c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   input.Entity.ID,
		UpdateName:   "feed",
		UpdateID:     opts.RequestID,
		Args:         []interface{}{input.Request},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return nil, err
	}

	var s worker.TamagotchiState
	if err := res.Get(ctx, &s); err != nil {
		return nil, err
	}
	return &s, nil
})

var PlayOperation = temporalnexus.NewSyncOperation("play", func(ctx context.Context, c client.Client, input *PlayOperationRequest, opts nexus.StartOperationOptions) (*worker.TamagotchiState, error) {
	res, err := c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID:   input.Entity.ID,
		UpdateName:   "play",
		UpdateID:     opts.RequestID,
		Args:         []interface{}{input.Request},
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return nil, err
	}

	var s worker.TamagotchiState
	if err := res.Get(ctx, &s); err != nil {
		return nil, err
	}
	return &s, nil
})

func NewTamagatchiService() *nexus.Service {
	svc := nexus.NewService("game")
	if err := svc.Register(
		HatchOperation,
		GetStatsOperation,
		FeedOperation,
		PlayOperation,
	); err != nil {
		panic(err)
	}

	return svc
}
