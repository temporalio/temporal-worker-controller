// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/temporalio/temporal-worker-controller/internal/demo/helloworld"
	"go.temporal.io/sdk/client"
)

var (
	temporalAddress   = flag.String("address", getEnv("TEMPORAL_ADDRESS", "localhost:7233"), "Temporal server address")
	namespace         = flag.String("namespace", getEnv("TEMPORAL_NAMESPACE", "default"), "Temporal namespace")
	taskQueue         = flag.String("task-queue", getEnv("TASK_QUEUE", "hello-world"), "Task queue name")
	count             = flag.Int("count", 10, "Number of workflows to start")
	duration          = flag.Int("duration", 300, "Duration in seconds for each workflow")
	cpuIntensity      = flag.Int("cpu", 2, "CPU intensity (0=none, 1=light, 2=medium, 3=heavy)")
	memoryMB          = flag.Int("memory", 256, "Memory to allocate in MB")
	heartbeat         = flag.Bool("heartbeat", true, "Enable activity heartbeats")
	workflowIDPrefix  = flag.String("workflow-id-prefix", fmt.Sprintf("load-test-%d", time.Now().Unix()), "Workflow ID prefix")
	stagger           = flag.Duration("stagger", 100*time.Millisecond, "Delay between starting workflows")
)

func main() {
	flag.Parse()

	// Create Temporal client
	c, err := client.Dial(client.Options{
		HostPort:  *temporalAddress,
		Namespace: *namespace,
	})
	if err != nil {
		log.Fatalf("Unable to create Temporal client: %v", err)
	}
	defer c.Close()

	ctx := context.Background()

	// Prepare workflow parameters
	params := helloworld.LoadTestParams{
		DurationSeconds:  *duration,
		CPUIntensity:     *cpuIntensity,
		MemoryMB:         *memoryMB,
		HeartbeatEnabled: *heartbeat,
	}

	// Print configuration
	fmt.Printf("🚀 Starting %d load test workflows\n", *count)
	fmt.Printf("Configuration:\n")
	fmt.Printf("  Temporal Address: %s\n", *temporalAddress)
	fmt.Printf("  Namespace: %s\n", *namespace)
	fmt.Printf("  Task Queue: %s\n", *taskQueue)
	fmt.Printf("  Duration: %ds\n", *duration)
	fmt.Printf("  CPU Intensity: %d\n", *cpuIntensity)
	fmt.Printf("  Memory: %dMB\n", *memoryMB)
	fmt.Printf("  Heartbeat: %v\n", *heartbeat)
	fmt.Printf("  Workflow ID Prefix: %s\n", *workflowIDPrefix)
	fmt.Printf("\n")

	// Start workflows
	successCount := 0
	for i := 1; i <= *count; i++ {
		workflowID := fmt.Sprintf("%s-%d", *workflowIDPrefix, i)

		options := client.StartWorkflowOptions{
			ID:        workflowID,
			TaskQueue: *taskQueue,
		}

		we, err := c.ExecuteWorkflow(ctx, options, helloworld.LoadTestWorkflow, params)
		if err != nil {
			log.Printf("❌ Failed to start workflow %d/%d (ID: %s): %v", i, *count, workflowID, err)
			continue
		}

		fmt.Printf("✅ Started workflow %d/%d\n", i, *count)
		fmt.Printf("   Workflow ID: %s\n", we.GetID())
		fmt.Printf("   Run ID: %s\n", we.GetRunID())

		successCount++

		// Stagger workflow starts to avoid overwhelming the system
		if i < *count {
			time.Sleep(*stagger)
		}
	}

	fmt.Printf("\n✨ Successfully started %d/%d workflows\n", successCount, *count)

	if successCount < *count {
		fmt.Printf("⚠️  %d workflows failed to start\n", *count-successCount)
		os.Exit(1)
	}

	// Print monitoring commands
	fmt.Printf("\n📊 Monitoring Commands:\n")
	fmt.Printf("  kubectl get hpa -w\n")
	fmt.Printf("  kubectl top pods\n")
	fmt.Printf("  temporal workflow list --namespace %s --query 'WorkflowType=\"LoadTestWorkflow\"'\n", *namespace)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

