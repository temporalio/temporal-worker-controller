# HPA Testing - Implementation Summary

## Overview

Your demo worker has been enhanced with comprehensive load testing capabilities for testing Horizontal Pod Autoscaler (HPA). You can now generate CPU pressure, memory pressure, and workflow backlogs to observe and validate HPA scaling behavior.

## What Was Added

### 1. Load Test Workflow and Activity (`worker.go`)

**New Types:**
- `LoadTestParams` - Configure duration, CPU intensity, memory allocation, and heartbeats
- `LoadTestResult` - Get feedback on actual execution metrics
- `LoadTestWorkflow` - Orchestrates load testing
- `LoadTestActivity` - Performs the actual load work

**CPU Intensity Levels:**
- **0 (None)**: No CPU work, just sleeps
- **1 (Light)**: Basic math operations (~10-20% CPU)
- **2 (Medium)**: Mathematical computations (~40-60% CPU)
- **3 (Heavy)**: SHA256 hashing loops (~100% CPU)

**Memory Allocation:**
- Allocates memory in 1MB chunks
- Writes to allocated memory to ensure OS actually allocates it
- Holds memory for the full duration
- Automatically releases when activity completes

### 2. Shell Script (`scripts/loadtest.sh`)

Easy-to-use shell script with pre-configured test scenarios:

```bash
./loadtest.sh cpu 20       # CPU pressure
./loadtest.sh memory 15    # Memory pressure
./loadtest.sh backlog 100  # Workflow backlog
./loadtest.sh mixed 10     # CPU + Memory
./loadtest.sh custom       # Interactive mode
```

**Features:**
- Pre-configured scenarios for common tests
- Interactive custom mode
- Helpful monitoring commands after execution
- Environment variable support
- Color-coded output

### 3. Go CLI Tool (`scripts/loadtest/`)

Programmatic load testing tool for automation and CI/CD:

```bash
cd internal/demo/scripts/loadtest
go build -o loadtest .
./loadtest -cpu 3 -memory 256 -count 20
```

**Advantages:**
- Cross-platform (Windows, Linux, macOS)
- No external dependencies (single binary)
- Type-safe parameters
- Better error handling
- Easy CI/CD integration
- Fast concurrent workflow starting

### 4. Documentation

- **`LOADTEST.md`** - Comprehensive guide with examples, HPA configuration, troubleshooting
- **`LOADTEST_QUICKSTART.md`** - Quick start specific to your setup
- **`scripts/loadtest/README.md`** - Go CLI tool documentation
- **`HPA_TESTING_SUMMARY.md`** (this file) - Implementation overview

## Quick Start

### Option 1: Shell Script (Easiest)

```bash
cd internal/demo/scripts
./loadtest.sh cpu 20
```

### Option 2: Go CLI Tool (Best for Automation)

```bash
cd internal/demo/scripts/loadtest
./loadtest -cpu 3 -memory 256 -count 20 -duration 300
```

### Option 3: Temporal CLI (Manual)

```bash
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id load-test-1 \
  --input '{"durationSeconds":300,"cpuIntensity":3,"memoryMB":256,"heartbeatEnabled":true}'
```

## Typical Test Workflow

### 1. Establish Baseline

```bash
# Check initial state
kubectl get hpa
kubectl get pods -l 'app.kubernetes.io/name=helloworld'
kubectl top pods -l 'app.kubernetes.io/name=helloworld'
```

### 2. Generate Load

```bash
cd internal/demo/scripts
./loadtest.sh cpu 20
```

### 3. Monitor Scaling

Open 3 terminals:

**Terminal 1 - HPA:**
```bash
watch -n 2 'kubectl get hpa'
```

**Terminal 2 - Metrics:**
```bash
watch -n 2 'kubectl top pods -l "app.kubernetes.io/name=helloworld"'
```

**Terminal 3 - Pod Count:**
```bash
watch -n 2 'kubectl get pods -l "app.kubernetes.io/name=helloworld" | grep Running | wc -l'
```

### 4. Observe Results

Watch for:
- **CPU/Memory % rises** above 75% (your threshold)
- **REPLICAS increases** in HPA output
- **New pods appear** in Running state
- **Load distributes** across pods
- **Metrics decrease** as load spreads

### 5. Test Scale-Down

After load test completes (workflows finish):
- Wait 5-10 minutes (scale-down stabilization window is 300s)
- Observe HPA gradually reducing pod count
- Verify minimum replica count is respected (2 in your config)

## Your Current HPA Configuration

From `helm/helloworld/templates/hpa.yaml`:

```yaml
minReplicas: 2
maxReplicas: 20

metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 75
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 75

behavior:
  scaleUp:
    stabilizationWindowSeconds: 60
    policies:
      - type: Percent
        value: 100  # Doubles pods each cycle
        periodSeconds: 60
  scaleDown:
    stabilizationWindowSeconds: 300  # 5 minute cooldown
    policies:
      - type: Percent
        value: 25  # Reduces by 25% each cycle
        periodSeconds: 60
```

## Test Scenarios

### Scenario 1: CPU Scaling

**Goal:** Verify CPU-based scaling works

```bash
./loadtest.sh cpu 20
```

**Expected:**
1. CPU rises above 75%
2. HPA scales up
3. New pods process work
4. CPU drops below 75%

### Scenario 2: Memory Scaling

**Goal:** Verify memory-based scaling works

```bash
./loadtest.sh memory 15
```

**Expected:**
1. Memory usage rises above 75%
2. HPA scales up
3. Memory distributes across pods

### Scenario 3: Scale-Up Speed

**Goal:** Measure how fast HPA responds

```bash
# Start with sudden heavy load
./loadtest.sh mixed 30
```

**Measure:**
- Time from load start to first scale decision
- Time between scale-up events
- Total time to handle load

### Scenario 4: Scale-Down Behavior

**Goal:** Verify graceful scale-down

```bash
# Short duration workflows
./loadtest.sh cpu 5  # or manually set durationSeconds:60
```

**Observe:**
- 5+ minute delay before scale-down starts
- Gradual reduction (25% per cycle)
- Stops at minReplicas (2)

### Scenario 5: Max Capacity

**Goal:** Test maximum scale

```bash
# Generate enough load to hit maxReplicas (20)
./loadtest.sh mixed 100
```

**Verify:**
- Pods scale to 20
- HPA stops scaling at maxReplicas
- System remains stable

## Prerequisites for Successful Testing

### 1. Resource Requests Must Be Set

HPA cannot calculate % utilization without resource requests:

```yaml
# In your deployment values.yaml or template
resources:
  requests:
    cpu: 500m      # HPA uses this as 100%
    memory: 512Mi  # HPA uses this as 100%
  limits:
    cpu: 2000m
    memory: 2Gi
```

### 2. Metrics Server Must Be Running

```bash
kubectl get deployment metrics-server -n kube-system
kubectl top nodes  # Should show metrics, not error
```

If not installed, install it:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

### 3. Worker Pods Must Be Healthy

```bash
kubectl get pods -l 'app.kubernetes.io/name=helloworld'
# All should show Running, not CrashLoopBackOff
```

### 4. Temporal Connection Working

```bash
kubectl logs -l 'app.kubernetes.io/name=helloworld' --tail=20
# Should show worker connected, not connection errors
```

## Troubleshooting

### HPA Shows "unknown" Targets

**Cause:** Metrics not available

**Fix:**
```bash
# Check metrics-server
kubectl get apiservice v1beta1.metrics.k8s.io

# Check resource requests are set
kubectl get pods <pod-name> -o jsonpath='{.spec.containers[0].resources}'
```

### Pods Not Scaling

**Cause:** Various issues

**Debug:**
```bash
# Check HPA status
kubectl describe hpa <release-name>-hpa

# Check HPA events
kubectl get events --field-selector involvedObject.name=<release-name>-hpa

# Check current metrics
kubectl get hpa <release-name>-hpa -o yaml
```

### Load Not Generating

**Cause:** Workflows not running

**Debug:**
```bash
# Check workflows
temporal workflow list --query 'WorkflowType="LoadTestWorkflow"'

# Check worker logs
kubectl logs -l 'app.kubernetes.io/name=helloworld' --tail=50

# Verify task queue
kubectl get pods <pod-name> -o yaml | grep -A 5 "env:"
```

### Workflows Starting But No Load

**Cause:** Workers not registered or task queue mismatch

**Fix:**
1. Verify `LoadTestWorkflow` and `LoadTestActivity` are registered in `cmd/worker/main.go`
2. Verify task queue name matches
3. Rebuild and redeploy worker

## Files Modified/Created

### Modified Files
- `internal/demo/helloworld/worker.go` - Added load test workflow and activities
- `internal/demo/helloworld/cmd/worker/main.go` - Registered new workflow/activity
- `go.work` - Added loadtest module to workspace
- `.gitignore` - Excluded loadtest binary

### New Files
- `internal/demo/scripts/loadtest.sh` - Shell script for easy load testing
- `internal/demo/scripts/loadtest/main.go` - Go CLI tool
- `internal/demo/scripts/loadtest/go.mod` - Go module for CLI tool
- `internal/demo/scripts/loadtest/README.md` - CLI tool documentation
- `internal/demo/LOADTEST.md` - Comprehensive load testing guide
- `internal/demo/LOADTEST_QUICKSTART.md` - Quick start guide
- `internal/demo/HPA_TESTING_SUMMARY.md` - This file

## Next Steps

### 1. Test Basic Functionality

```bash
# Simple test to verify everything works
cd internal/demo/scripts
./loadtest.sh cpu 5
```

### 2. Verify HPA Scaling

Watch HPA scale up and down with real load.

### 3. Tune HPA Parameters

Based on observations, adjust:
- `minReplicas` / `maxReplicas`
- CPU/Memory thresholds
- Scale-up/down policies
- Stabilization windows

### 4. Consider Custom Metrics

For production, consider Temporal-specific metrics:
- Task queue backlog depth (via Temporal API)
- Schedule-to-start latency (via worker metrics)
- Worker slot utilization (via worker metrics)

These provide better scaling signals than CPU/memory for Temporal workloads.

### 5. Add PodDisruptionBudget

To prevent too many pods being terminated during scale-down:

```yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: worker-pdb
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: helloworld
```

## Example Test Session

```bash
# 1. Build the worker with new code
cd internal/demo
docker build -t helloworld:latest .

# 2. Deploy with HPA
helm upgrade --install helloworld ./helm/helloworld \
  --set image.tag=latest \
  --set temporal.namespace=your-namespace \
  --set temporal.connectionName=your-connection

# 3. Wait for pods to be ready
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=helloworld --timeout=60s

# 4. Check baseline
kubectl get hpa
kubectl top pods

# 5. Generate load
cd scripts
./loadtest.sh cpu 20

# 6. Watch scaling (in separate terminals)
watch kubectl get hpa
watch kubectl top pods
watch 'kubectl get pods | grep helloworld'

# 7. Observe results
# - CPU usage rises
# - HPA scales up
# - Pods increase
# - Load distributes
# - Metrics stabilize

# 8. Wait for scale-down
# - Workflows complete (~5 minutes)
# - Wait for stabilization (~5 minutes)
# - Pods gradually reduce to minReplicas
```

## Additional Resources

- [Kubernetes HPA Documentation](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/)
- [HPA Walkthrough](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale-walkthrough/)
- [Temporal Worker Tuning Guide](https://docs.temporal.io/dev-guide/worker-performance)

## Support

If you encounter issues:
1. Check the troubleshooting section in `LOADTEST.md`
2. Review your HPA configuration
3. Verify prerequisites are met
4. Check worker logs for errors

Happy HPA testing! 🚀

