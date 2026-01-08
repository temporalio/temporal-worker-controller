# Load Testing Quick Start

Quick guide for testing HPA with your helloworld demo worker.

## Your Current HPA Configuration

Your HPA is configured in `helm/helloworld/templates/hpa.yaml` with:
- **Min replicas**: 2
- **Max replicas**: 20
- **CPU target**: 75% utilization
- **Memory target**: 75% utilization
- **Scale-up**: 100% increase every 60s (doubles the pods)
- **Scale-down**: 25% decrease every 60s (gradual)

## Prerequisites

1. **Metrics Server** must be installed in your cluster:
```bash
kubectl get deployment metrics-server -n kube-system
```

2. **Worker pods must have resource requests**. Check your deployment's `values.yaml` or add:
```yaml
resources:
  requests:
    cpu: 500m      # HPA uses this as 100%
    memory: 512Mi  # HPA uses this as 100%
  limits:
    cpu: 2000m
    memory: 2Gi
```

3. **Temporal CLI** installed locally:
```bash
brew install temporal  # macOS
# or download from https://docs.temporal.io/cli
```

## Simple Test Scenarios

### Test 1: CPU Scaling (Recommended First Test)

```bash
# 1. Check baseline
kubectl get hpa
kubectl get pods -l 'app.kubernetes.io/name=helloworld'

# 2. Generate CPU load (20 workflows × 100% CPU × 5 minutes)
cd internal/demo/scripts
./loadtest.sh cpu 20

# 3. Watch HPA scale up
kubectl get hpa -w
# You should see CPU% rise above 75%, then REPLICAS increase

# 4. In another terminal, watch pods
kubectl get pods -l 'app.kubernetes.io/name=helloworld' -w

# 5. Check CPU metrics
kubectl top pods -l 'app.kubernetes.io/name=helloworld'
```

**Expected Timeline:**
- 0-30s: CPU rises as workflows start executing
- 30-60s: HPA detects high CPU (>75%)
- 60-90s: HPA scales up (100% increase = doubles pods)
- 90-120s: New pods come online and start processing
- 2-5min: CPU distributes across pods, utilization drops below 75%

### Test 2: Memory Scaling

```bash
# Generate memory pressure (15 workflows × 512MB × 5 minutes)
./loadtest.sh memory 15

# Watch memory utilization
kubectl top pods -l 'app.kubernetes.io/name=helloworld'

# Watch HPA
kubectl get hpa -w
```

### Test 3: Combined Load

```bash
# Generate both CPU and memory pressure
./loadtest.sh mixed 15

# Watch HPA respond to whichever metric is higher
kubectl get hpa -w
```

### Test 4: Backlog and Scale-Down

```bash
# 1. Create a large backlog with short workflows
./loadtest.sh backlog 50

# 2. HPA scales up to handle load
kubectl get hpa -w

# 3. Wait for workflows to complete (~60 seconds)
# Watch Temporal UI or:
temporal workflow list --query 'WorkflowType="LoadTestWorkflow"'

# 4. After ~5 minutes of low utilization, watch scale-down
kubectl get hpa -w
# Pods will gradually decrease (25% every 60s)
```

## Observing the Behavior

### Dashboard to Watch

Open 3 terminal windows:

**Terminal 1 - HPA Status:**
```bash
watch -n 2 'kubectl get hpa'
```

**Terminal 2 - Pod Metrics:**
```bash
watch -n 2 'kubectl top pods -l "app.kubernetes.io/name=helloworld"'
```

**Terminal 3 - Pod Count:**
```bash
watch -n 2 'kubectl get pods -l "app.kubernetes.io/name=helloworld" | grep Running | wc -l'
```

### Detailed HPA Info

```bash
# See HPA calculation details
kubectl describe hpa <your-release-name>-hpa

# Look for events like:
# - "New size: 4; reason: cpu resource utilization (percentage of request) above target"
# - "New size: 2; reason: All metrics below target"
```

## Understanding the Results

### Why didn't it scale?

1. **Resource requests not set**: HPA can't calculate percentage without requests
```bash
kubectl get pods <pod-name> -o jsonpath='{.spec.containers[0].resources}'
```

2. **Metrics not available**: Check metrics-server
```bash
kubectl get apiservice v1beta1.metrics.k8s.io
kubectl top nodes  # Should show metrics, not "error"
```

3. **Not enough load**: Verify workflows are running
```bash
temporal workflow list --query 'WorkflowType="LoadTestWorkflow" AND ExecutionStatus="Running"'
```

4. **Already at max replicas**: Check HPA maxReplicas
```bash
kubectl get hpa -o jsonpath='{.spec.maxReplicas}'
```

### Why is scaling slow?

- **Scale-up stabilization**: 60s window in your config
- **Metrics collection interval**: HPA checks every 15s by default
- **Pod startup time**: Includes image pull, init, worker connection
- **Scale-down stabilization**: 300s (5 minutes) in your config

To make scaling faster for testing, temporarily modify the HPA:
```bash
kubectl patch hpa <release-name>-hpa --patch '{"spec":{"behavior":{"scaleUp":{"stabilizationWindowSeconds":0}}}}'
```

## Custom Load Parameters

For fine-grained control:

```bash
# Low CPU load for gentle scaling
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id test-1 \
  --input '{"durationSeconds":180,"cpuIntensity":1,"memoryMB":100,"heartbeatEnabled":true}'

# High memory, low CPU
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id test-2 \
  --input '{"durationSeconds":180,"cpuIntensity":0,"memoryMB":800,"heartbeatEnabled":true}'

# Max pressure
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id test-3 \
  --input '{"durationSeconds":300,"cpuIntensity":3,"memoryMB":1024,"heartbeatEnabled":true}'
```

## Environment Variables

The load test script respects these environment variables:

```bash
export TEMPORAL_ADDRESS="your-namespace.tmprl.cloud:7233"
export TEMPORAL_NAMESPACE="your-namespace.your-account"
export TASK_QUEUE="hello-world"  # Must match your worker's task queue

./loadtest.sh cpu 10
```

## Cleanup

```bash
# List running load tests
temporal workflow list --query 'WorkflowId STARTS_WITH "load-test-"'

# Terminate all load tests
temporal workflow terminate \
  --query 'WorkflowId STARTS_WITH "load-test-"' \
  --reason "cleanup"

# Or wait for them to complete naturally
```

## Next: Custom Metrics

Once CPU/memory scaling is working, consider Temporal-specific metrics:

1. **Task Queue Backlog** (via Temporal API or Prometheus)
2. **Schedule-to-Start Latency** (worker metric)
3. **Slot Utilization** (worker metric)

These provide better scaling signals for Temporal workloads than CPU/memory alone.

See commented sections in your `hpa.yaml` for custom metrics configuration.

## Troubleshooting Checklist

- [ ] Metrics server is running
- [ ] Worker pods have resource requests defined
- [ ] Worker pods are actually running (not CrashLoopBackOff)
- [ ] Temporal connection is working
- [ ] Task queue name matches between worker and workflow start commands
- [ ] Workflows are actually starting (check Temporal UI)
- [ ] LoadTestWorkflow and LoadTestActivity are registered in worker
- [ ] Sufficient load to exceed 75% threshold

## Questions?

Check the full documentation: `LOADTEST.md`

