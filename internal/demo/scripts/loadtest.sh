#!/bin/bash
# Script to trigger load test workflows for HPA testing
# Usage: ./loadtest.sh [cpu|memory|backlog|mixed] [count]

set -e

# Configuration
TEMPORAL_ADDRESS="${TEMPORAL_ADDRESS:-localhost:7233}"
NAMESPACE="${TEMPORAL_NAMESPACE:-default}"
TASK_QUEUE="${TASK_QUEUE:-hello-world}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

function print_usage() {
    echo "Usage: $0 [test-type] [count]"
    echo ""
    echo "Test Types:"
    echo "  cpu       - Generate CPU pressure (heavy computation)"
    echo "  memory    - Generate memory pressure (allocate memory)"
    echo "  backlog   - Generate workflow backlog (many quick workflows)"
    echo "  mixed     - Generate both CPU and memory pressure"
    echo "  custom    - Interactive mode to set custom parameters"
    echo ""
    echo "Count: Number of workflows to start (default: 10)"
    echo ""
    echo "Examples:"
    echo "  $0 cpu 20        # Start 20 CPU-intensive workflows"
    echo "  $0 memory 15     # Start 15 memory-intensive workflows"
    echo "  $0 backlog 100   # Start 100 workflows to create backlog"
    echo "  $0 mixed 10      # Start 10 workflows with CPU+memory pressure"
    echo ""
    echo "Environment Variables:"
    echo "  TEMPORAL_ADDRESS  - Temporal server address (default: localhost:7233)"
    echo "  TEMPORAL_NAMESPACE - Temporal namespace (default: default)"
    echo "  TASK_QUEUE        - Task queue name (default: hello-world)"
}

function start_workflow() {
    local params="$1"
    local workflow_id="$2"
    
    temporal workflow start \
        --address "$TEMPORAL_ADDRESS" \
        --namespace "$NAMESPACE" \
        --task-queue "$TASK_QUEUE" \
        --type LoadTestWorkflow \
        --workflow-id "$workflow_id" \
        --input "$params" \
        --tls-disable-host-verification 2>&1 | grep -E "(Started|WorkflowId)" || true
}

function generate_cpu_load() {
    local count="${1:-10}"
    echo -e "${GREEN}Starting $count CPU-intensive workflows...${NC}"
    
    for i in $(seq 1 "$count"); do
        local workflow_id="load-test-cpu-$(date +%s)-$i"
        local params='{"durationSeconds":300,"cpuIntensity":3,"memoryMB":0,"heartbeatEnabled":true}'
        echo -e "${YELLOW}Starting workflow $i/$count: $workflow_id${NC}"
        start_workflow "$params" "$workflow_id"
        sleep 0.1  # Small delay to avoid overwhelming the system
    done
    
    echo -e "${GREEN}✓ Started $count CPU-intensive workflows (300s duration, heavy CPU)${NC}"
}

function generate_memory_load() {
    local count="${1:-10}"
    echo -e "${GREEN}Starting $count memory-intensive workflows...${NC}"
    
    for i in $(seq 1 "$count"); do
        local workflow_id="load-test-memory-$(date +%s)-$i"
        # Allocate 512MB per workflow
        local params='{"durationSeconds":300,"cpuIntensity":0,"memoryMB":512,"heartbeatEnabled":true}'
        echo -e "${YELLOW}Starting workflow $i/$count: $workflow_id${NC}"
        start_workflow "$params" "$workflow_id"
        sleep 0.1
    done
    
    echo -e "${GREEN}✓ Started $count memory-intensive workflows (300s duration, 512MB each)${NC}"
}

function generate_backlog() {
    local count="${1:-100}"
    echo -e "${GREEN}Generating workflow backlog with $count workflows...${NC}"
    
    for i in $(seq 1 "$count"); do
        local workflow_id="load-test-backlog-$(date +%s)-$i"
        # Quick workflows with medium CPU to create backlog
        local params='{"durationSeconds":60,"cpuIntensity":2,"memoryMB":100,"heartbeatEnabled":false}'
        echo -e "${YELLOW}Starting workflow $i/$count: $workflow_id${NC}"
        start_workflow "$params" "$workflow_id"
        sleep 0.05  # Very small delay
    done
    
    echo -e "${GREEN}✓ Started $count workflows to generate backlog (60s duration each)${NC}"
}

function generate_mixed_load() {
    local count="${1:-10}"
    echo -e "${GREEN}Starting $count mixed CPU+Memory workflows...${NC}"
    
    for i in $(seq 1 "$count"); do
        local workflow_id="load-test-mixed-$(date +%s)-$i"
        # Both CPU and memory pressure
        local params='{"durationSeconds":300,"cpuIntensity":3,"memoryMB":256,"heartbeatEnabled":true}'
        echo -e "${YELLOW}Starting workflow $i/$count: $workflow_id${NC}"
        start_workflow "$params" "$workflow_id"
        sleep 0.1
    done
    
    echo -e "${GREEN}✓ Started $count mixed workflows (300s duration, heavy CPU, 256MB memory)${NC}"
}

function custom_load() {
    echo -e "${GREEN}Custom Load Test Configuration${NC}"
    echo ""
    
    read -p "Duration in seconds (default: 300): " duration
    duration="${duration:-300}"
    
    read -p "CPU Intensity (0=none, 1=light, 2=medium, 3=heavy, default: 2): " cpu
    cpu="${cpu:-2}"
    
    read -p "Memory in MB (default: 256): " memory
    memory="${memory:-256}"
    
    read -p "Number of workflows (default: 10): " count
    count="${count:-10}"
    
    read -p "Enable heartbeat? (y/n, default: y): " heartbeat
    if [[ "$heartbeat" == "n" || "$heartbeat" == "N" ]]; then
        heartbeat="false"
    else
        heartbeat="true"
    fi
    
    echo ""
    echo -e "${YELLOW}Configuration:${NC}"
    echo "  Duration: ${duration}s"
    echo "  CPU Intensity: $cpu"
    echo "  Memory: ${memory}MB"
    echo "  Workflows: $count"
    echo "  Heartbeat: $heartbeat"
    echo ""
    read -p "Proceed? (y/n): " confirm
    
    if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
        echo "Cancelled."
        exit 0
    fi
    
    echo -e "${GREEN}Starting $count custom workflows...${NC}"
    
    for i in $(seq 1 "$count"); do
        local workflow_id="load-test-custom-$(date +%s)-$i"
        local params="{\"durationSeconds\":$duration,\"cpuIntensity\":$cpu,\"memoryMB\":$memory,\"heartbeatEnabled\":$heartbeat}"
        echo -e "${YELLOW}Starting workflow $i/$count: $workflow_id${NC}"
        start_workflow "$params" "$workflow_id"
        sleep 0.1
    done
    
    echo -e "${GREEN}✓ Started $count custom workflows${NC}"
}

function monitor_hint() {
    echo ""
    echo -e "${YELLOW}=== Monitoring Tips ===${NC}"
    echo ""
    echo "Watch HPA status:"
    echo "  kubectl get hpa -w"
    echo ""
    echo "Watch pod metrics:"
    echo "  kubectl top pods -l app=your-worker-app"
    echo ""
    echo "Watch pod count:"
    echo "  watch 'kubectl get pods | grep your-worker-app'"
    echo ""
    echo "View workflow executions:"
    echo "  temporal workflow list --address $TEMPORAL_ADDRESS --namespace $NAMESPACE"
    echo ""
}

# Main script
TEST_TYPE="${1:-}"
COUNT="${2:-10}"

if [[ -z "$TEST_TYPE" ]]; then
    print_usage
    exit 1
fi

# Check if temporal CLI is available
if ! command -v temporal &> /dev/null; then
    echo -e "${RED}Error: 'temporal' CLI not found. Please install it first.${NC}"
    echo "Visit: https://docs.temporal.io/cli"
    exit 1
fi

case "$TEST_TYPE" in
    cpu)
        generate_cpu_load "$COUNT"
        monitor_hint
        ;;
    memory)
        generate_memory_load "$COUNT"
        monitor_hint
        ;;
    backlog)
        generate_backlog "$COUNT"
        monitor_hint
        ;;
    mixed)
        generate_mixed_load "$COUNT"
        monitor_hint
        ;;
    custom)
        custom_load
        monitor_hint
        ;;
    help|--help|-h)
        print_usage
        ;;
    *)
        echo -e "${RED}Error: Unknown test type '$TEST_TYPE'${NC}"
        echo ""
        print_usage
        exit 1
        ;;
esac

