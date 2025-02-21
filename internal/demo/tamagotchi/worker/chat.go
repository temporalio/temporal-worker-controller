package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ollama/ollama/api"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type TamaChatRequest struct {
	State   *TamagotchiState `json:"state"`
	Message string           `json:"message"`
}

type TamaChatResponse struct {
	Response string `json:"response"`
}

// TamaChat is a chatbot that can be used to interact with the tamagotchi.
type TamaChat struct {
	Client *api.Client
}

func NewTamaChat(endpoint string) (*TamaChat, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	c := api.NewClient(u, http.DefaultClient)
	fmt.Println("Created ollama client: ", endpoint)
	if err := c.Heartbeat(context.Background()); err != nil {
		return nil, err
	}

	resp, err := c.List(context.Background())
	if err != nil {
		return nil, err
	}
	for _, model := range resp.Models {
		fmt.Println("Ollama model: ", model.Name)
	}

	return &TamaChat{
		Client: c,
	}, nil
}

func generateSystemPrompt(state *TamagotchiState) string {
	return fmt.Sprintf(
		`You are a tamagotchi. You can be fed, bathed, and played with.
You will die if your hunger, fun, or clean levels reach 0 percent.
Keep responses extremely short, no more than 20 words. Communicate without using complex words or sentences.

If your hunger level is below 30 percent, you should be too hungry to respond.
If your happiness level is below 50 percent, your persona should be grumpy.
If your clean level is below 30 percent, you should be too distracted by how smelly you are to respond.

Respond to the user taking into account the following information about your current vitals:

Name: %s
Hunger: %d percent
Fun: %d percent
Clean: %d percent
Energy: %d percent`, state.Name, state.Hunger, state.Fun, state.Clean, state.Energy)
}

func (c *TamaChat) Chat(ctx context.Context, req *TamaChatRequest) (*TamaChatResponse, error) {
	var resp *api.ChatResponse
	if err := c.Client.Chat(ctx, &api.ChatRequest{
		Model: "llama3.2",
		Messages: []api.Message{
			{
				Role:    "system",
				Content: generateSystemPrompt(req.State),
			},
			{
				Role:    "user",
				Content: req.Message,
			},
		},
	}, func(r api.ChatResponse) error {
		resp = &r
		return nil
	}); err != nil {
		return nil, err
	}

	return &TamaChatResponse{
		Response: resp.Message.Content,
	}, nil
}

func (c *TamaChat) ExecuteActivityTamaChat(ctx workflow.Context, req *TamaChatRequest) (*TamaChatResponse, error) {
	var resp TamaChatResponse
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
	}), c.Chat, req).Get(ctx, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *TamaChat) RegisterActivities(registry worker.Registry) {
	registry.RegisterActivity(c.Chat)
}
