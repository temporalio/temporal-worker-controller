package api

import (
	"encoding/json"
	"net/http"
	"os"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/DataDog/temporal-worker-controller/internal/demo/tamagotchi/worker"
)

func NewHandler(c client.Client) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/hatch", func(w http.ResponseWriter, r *http.Request) {
		name := r.FormValue("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		wfr, err := c.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
			TaskQueue: os.Getenv("TEMPORAL_TASK_QUEUE"),
		}, "Tamagotchi", &worker.TamagotchiRequest{Name: name})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": wfr.GetID()})
	})

	mux.HandleFunc("/api/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var stats worker.TamagotchiState

		// Check if the tamagotchi is deceased (workflow completed)
		desc, err := c.DescribeWorkflowExecution(r.Context(), id, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if desc.GetWorkflowExecutionInfo().GetStatus() == enums.WORKFLOW_EXECUTION_STATUS_COMPLETED {
			if err := c.GetWorkflow(r.Context(), id, "").Get(r.Context(), &stats); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			resp, err := c.QueryWorkflow(r.Context(), id, "", "stats")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := resp.Get(&stats); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		if err := json.NewEncoder(w).Encode(stats); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	mux.HandleFunc("/api/{id}/feed", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		food := r.FormValue("food")
		if food == "" {
			http.Error(w, "food is required", http.StatusBadRequest)
			return
		}

		resp, err := c.UpdateWorkflow(r.Context(), client.UpdateWorkflowOptions{
			WorkflowID:   id,
			UpdateName:   "feed",
			Args:         []interface{}{&worker.FeedRequest{Food: food}},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var result worker.TamagotchiState
		if err := resp.Get(r.Context(), &result); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if err := json.NewEncoder(w).Encode(result); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	mux.HandleFunc("/api/{id}/play", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		activity := r.FormValue("activity")
		if activity == "" {
			http.Error(w, "activity is required", http.StatusBadRequest)
			return
		}

		resp, err := c.UpdateWorkflow(r.Context(), client.UpdateWorkflowOptions{
			WorkflowID:   id,
			UpdateName:   "play",
			Args:         []interface{}{&worker.PlayRequest{Activity: activity}},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var result worker.TamagotchiState
		if err := resp.Get(r.Context(), &result); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if err := json.NewEncoder(w).Encode(result); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	mux.HandleFunc("/api/{id}/sleep", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		resp, err := c.UpdateWorkflow(r.Context(), client.UpdateWorkflowOptions{
			WorkflowID:   id,
			UpdateName:   "sleep",
			WaitForStage: client.WorkflowUpdateStageAccepted,
			Args:         []interface{}{&worker.SleepRequest{}},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := resp.Get(r.Context(), nil); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if err := json.NewEncoder(w).Encode(map[string]string{}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	mux.HandleFunc("/api/{id}/bathe", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		resp, err := c.UpdateWorkflow(r.Context(), client.UpdateWorkflowOptions{
			WorkflowID:   id,
			UpdateName:   "bathe",
			Args:         []interface{}{&worker.BatheRequest{}},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var result worker.TamagotchiState
		if err := resp.Get(r.Context(), &result); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if err := json.NewEncoder(w).Encode(result); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	mux.HandleFunc("/api/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		message := r.FormValue("message")
		if message == "" {
			http.Error(w, "message is required", http.StatusBadRequest)
			return
		}

		resp, err := c.UpdateWorkflow(r.Context(), client.UpdateWorkflowOptions{
			WorkflowID:   id,
			UpdateName:   "chat",
			Args:         []interface{}{&worker.ChatRequest{Message: message}},
			WaitForStage: client.WorkflowUpdateStageAccepted,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var result worker.ChatResponse
		if err := resp.Get(r.Context(), &result); err != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		if err := json.NewEncoder(w).Encode(result); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	})

	return mux
}
