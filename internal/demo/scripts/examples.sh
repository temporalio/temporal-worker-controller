#!/bin/bash
# Example load test commands for different scenarios
# These are ready-to-run examples you can copy and paste

echo "=== HPA Load Testing Examples ==="
echo ""
echo "Make sure you're in the scripts directory:"
echo "  cd internal/demo/scripts"
echo ""

cat << 'EOF'

# ============================================================
# Example 1: Quick CPU Test (Good First Test)
# ============================================================
# What it does: Starts 10 CPU-intensive workflows
# Expected: CPU usage rises, HPA scales up pods
# Duration: 5 minutes per workflow

./loadtest.sh cpu 10


# ============================================================
# Example 2: Memory Pressure Test
# ============================================================
# What it does: Each workflow allocates 512MB of memory
# Expected: Memory usage rises, HPA scales up pods
# Duration: 5 minutes per workflow

./loadtest.sh memory 15


# ============================================================
# Example 3: Large Backlog Test
# ============================================================
# What it does: Creates 100 workflows to queue up
# Expected: Workflows queue, HPA scales up to process backlog
# Duration: 1 minute per workflow (processes faster)

./loadtest.sh backlog 100


# ============================================================
# Example 4: Mixed Load (CPU + Memory)
# ============================================================
# What it does: Heavy CPU AND memory pressure
# Expected: Both metrics rise, HPA scales aggressively
# Duration: 5 minutes per workflow

./loadtest.sh mixed 20


# ============================================================
# Example 5: Gradual Load Increase
# ============================================================
# What it does: Gradually increases load to see scaling response
# Expected: HPA scales up in stages

./loadtest.sh cpu 5
sleep 120
./loadtest.sh cpu 10
sleep 120
./loadtest.sh cpu 20


# ============================================================
# Example 6: Scale-Down Test
# ============================================================
# What it does: Short workflows to test scale-down behavior
# Expected: Pods scale up, then gradually scale down after 5-10min

# Use temporal CLI for custom duration
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id scale-down-test \
  --input '{"durationSeconds":60,"cpuIntensity":3,"memoryMB":256,"heartbeatEnabled":true}'

# Start 20 of these
for i in {1..20}; do
  temporal workflow start \
    --task-queue hello-world \
    --type LoadTestWorkflow \
    --workflow-id "scale-down-test-$i" \
    --input '{"durationSeconds":60,"cpuIntensity":3,"memoryMB":256,"heartbeatEnabled":true}'
  sleep 0.1
done


# ============================================================
# Example 7: Custom Interactive Test
# ============================================================
# What it does: Prompts you for parameters
# Expected: Whatever you configure

./loadtest.sh custom


# ============================================================
# Example 8: Using the Go CLI Tool
# ============================================================
# What it does: Same as shell script but cross-platform
# Expected: More control, better for automation

cd loadtest
./loadtest -cpu 3 -memory 256 -count 20 -duration 300


# ============================================================
# Example 9: Light Load (Won't Trigger Scaling)
# ============================================================
# What it does: Low intensity to verify baseline
# Expected: Metrics rise but stay below threshold (no scaling)

temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id baseline-test \
  --input '{"durationSeconds":180,"cpuIntensity":1,"memoryMB":100,"heartbeatEnabled":true}'


# ============================================================
# Example 10: Stress Test (Maximum Load)
# ============================================================
# What it does: Generate enough load to hit maxReplicas (20)
# Expected: Scales to maximum, stays there, system stable

./loadtest.sh cpu 50
# Or
./loadtest.sh mixed 80


# ============================================================
# Monitoring Commands (Run in Separate Terminals)
# ============================================================

# Terminal 1: Watch HPA
watch -n 2 'kubectl get hpa'

# Terminal 2: Watch Pod Metrics
watch -n 2 'kubectl top pods -l "app.kubernetes.io/name=helloworld"'

# Terminal 3: Watch Pod Count
watch -n 2 'kubectl get pods -l "app.kubernetes.io/name=helloworld" | grep Running | wc -l'

# Terminal 4: Watch Workflows
watch -n 5 'temporal workflow list --query "WorkflowType=\"LoadTestWorkflow\" AND ExecutionStatus=\"Running\"" | head -20'


# ============================================================
# Cleanup Commands
# ============================================================

# List all running load tests
temporal workflow list --query 'WorkflowId STARTS_WITH "load-test-" AND ExecutionStatus="Running"'

# Terminate all load tests
temporal workflow terminate \
  --query 'WorkflowId STARTS_WITH "load-test-"' \
  --reason "cleanup"

# Or wait for them to complete naturally (they'll finish after their duration)

EOF

