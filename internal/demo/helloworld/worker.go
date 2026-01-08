// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package helloworld

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/temporalio/temporal-worker-controller/internal/demo/util"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

func HelloWorld(ctx workflow.Context) (string, error) {
	ctx = util.SetActivityTimeout(ctx, 5*time.Minute)

	// Get a subject
	var subject string
	if err := workflow.ExecuteActivity(ctx, GetSubject).Get(ctx, &subject); err != nil {
		return "", err
	}

	// Return the greeting
	return fmt.Sprintf("Hello %s", subject), nil
}

func GetSubject(ctx context.Context) (string, error) {
	// Simulate activity execution latency
	time.Sleep(time.Duration(rand.Intn(30)) * time.Second)
	// Return a hardcoded subject
	return "World", nil
}

func RolloutGate(ctx workflow.Context) error {
	// Ensure that deploys fail fast rather than waiting forever if child workflow is blocked.
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowExecutionTimeout: time.Minute,
	})

	var greeting string
	if err := workflow.ExecuteChildWorkflow(ctx, HelloWorld).Get(ctx, &greeting); err != nil {
		return err
	}
	if !strings.HasPrefix(greeting, "Hello ") {
		return fmt.Errorf(`greeting does not have expect prefix "Hello "; got: %q`, greeting)
	}

	return nil
}

// LoadTestParams defines the parameters for load testing
type LoadTestParams struct {
	// DurationSeconds is how long the activity should run
	DurationSeconds int `json:"durationSeconds"`
	// CPUIntensity controls CPU usage (0=none, 1=light, 2=medium, 3=heavy)
	CPUIntensity int `json:"cpuIntensity"`
	// MemoryMB is the amount of memory to allocate in MB
	MemoryMB int `json:"memoryMB"`
	// HeartbeatEnabled sends heartbeats during execution
	HeartbeatEnabled bool `json:"heartbeatEnabled"`
}

// LoadTestResult contains the result of a load test
type LoadTestResult struct {
	Success         bool          `json:"success"`
	ActualDuration  time.Duration `json:"actualDuration"`
	MemoryAllocated int           `json:"memoryAllocated"`
	CPUWorkDone     int64         `json:"cpuWorkDone"`
}

// LoadTestWorkflow orchestrates a load test
func LoadTestWorkflow(ctx workflow.Context, params LoadTestParams) (*LoadTestResult, error) {
	ctx = util.SetActivityTimeout(ctx, time.Duration(params.DurationSeconds+60)*time.Second)

	var result LoadTestResult
	if err := workflow.ExecuteActivity(ctx, LoadTestActivity, params).Get(ctx, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// LoadTestActivity performs the actual load testing work
func LoadTestActivity(ctx context.Context, params LoadTestParams) (*LoadTestResult, error) {
	startTime := time.Now()
	
	// Start heartbeating if enabled
	if params.HeartbeatEnabled {
		go util.AutoHeartbeat(ctx)
	}

	result := &LoadTestResult{
		Success: true,
	}

	// Allocate memory if requested
	var memoryBallast [][]byte
	if params.MemoryMB > 0 {
		activity.GetLogger(ctx).Info("Allocating memory", "mb", params.MemoryMB)
		// Allocate in 1MB chunks to ensure it's actually allocated
		for i := 0; i < params.MemoryMB; i++ {
			chunk := make([]byte, 1024*1024)
			// Write to the memory to ensure it's actually allocated
			for j := 0; j < len(chunk); j += 4096 {
				chunk[j] = byte(i)
			}
			memoryBallast = append(memoryBallast, chunk)
		}
		result.MemoryAllocated = params.MemoryMB
	}

	// Calculate end time
	endTime := startTime.Add(time.Duration(params.DurationSeconds) * time.Second)
	
	// Perform CPU work until duration expires
	cpuWorkCounter := int64(0)
	for time.Now().Before(endTime) {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			result.Success = false
			result.ActualDuration = time.Since(startTime)
			result.CPUWorkDone = cpuWorkCounter
			return result, ctx.Err()
		default:
		}

		// Do CPU work based on intensity
		switch params.CPUIntensity {
		case 0: // No CPU work, just sleep
			time.Sleep(100 * time.Millisecond)
		case 1: // Light CPU work
			cpuWorkCounter += lightCPUWork()
			time.Sleep(50 * time.Millisecond)
		case 2: // Medium CPU work
			cpuWorkCounter += mediumCPUWork()
			time.Sleep(10 * time.Millisecond)
		case 3: // Heavy CPU work
			cpuWorkCounter += heavyCPUWork()
			// No sleep, continuous work
		default:
			cpuWorkCounter += mediumCPUWork()
			time.Sleep(10 * time.Millisecond)
		}
	}

	result.ActualDuration = time.Since(startTime)
	result.CPUWorkDone = cpuWorkCounter

	// Keep memory allocated until the end
	if len(memoryBallast) > 0 {
		activity.GetLogger(ctx).Info("Releasing memory", "mb", len(memoryBallast))
	}

	return result, nil
}

// lightCPUWork performs a small amount of CPU work
func lightCPUWork() int64 {
	counter := int64(0)
	for i := 0; i < 1000; i++ {
		counter += int64(math.Sqrt(float64(i)))
	}
	return counter
}

// mediumCPUWork performs moderate CPU work
func mediumCPUWork() int64 {
	counter := int64(0)
	for i := 0; i < 10000; i++ {
		counter += int64(math.Sqrt(float64(i)) * math.Sin(float64(i)))
	}
	return counter
}

// heavyCPUWork performs intensive CPU work
func heavyCPUWork() int64 {
	counter := int64(0)
	// Compute multiple hash operations
	data := []byte(fmt.Sprintf("heavy-cpu-work-%d", time.Now().UnixNano()))
	for i := 0; i < 100; i++ {
		hash := sha256.Sum256(data)
		data = hash[:]
		for _, b := range data {
			counter += int64(b)
		}
	}
	return counter
}
