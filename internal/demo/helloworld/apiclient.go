// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package helloworld

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"time"

	"github.com/temporalio/temporal-worker-controller/internal/demo/util"
)

type GetSubjectResponse struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

func fetchUser(ctx context.Context, apiEndpoint string) (GetSubjectResponse, error) {
	// Pick a random user ID (API has 10 users with IDs 1-10)
	userID := rand.Intn(10) + 1

	url := fmt.Sprintf("%s/users/%d", apiEndpoint, userID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return GetSubjectResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	// Use custom HTTP client with random network latency up to 30s
	client := util.NewHTTPClient(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return GetSubjectResponse{}, fmt.Errorf("failed to fetch user: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return GetSubjectResponse{}, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var user GetSubjectResponse
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return GetSubjectResponse{}, fmt.Errorf("failed to decode response: %w", err)
	}

	return user, nil
}
