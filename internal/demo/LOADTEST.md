# Load Testing for HPA

This guide explains how to use the load testing features to test Horizontal Pod Autoscaler (HPA) with your Temporal workers.

## Overview

The demo worker now includes a `LoadTestWorkflow` and `LoadTestActivity` that can generate:
- **CPU pressure**: Intensive computational work
- **Memory pressure**: Memory allocation
- **Workflow backlog**: Many concurrent workflows to test queue depth scaling

## Quick Start

### 1. Using the Helper Script

The easiest way to generate load is using the provided script:

```bash
cd internal/demo/scripts

# Generate CPU pressure with 20 workflows
./loadtest.sh cpu 20

# Generate memory pressure with 15 workflows
./loadtest.sh memory 15

# Generate backlog with 100 workflows
./loadtest.sh backlog 100

# Generate mixed CPU + memory pressure
./loadtest.sh mixed 10

# Custom configuration (interactive)
./loadtest.sh custom
```

### 2. Manual Workflow Execution

You can also start workflows manually using the Temporal CLI:

```bash
# CPU-intensive load test
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id load-test-cpu-1 \
  --input '{"durationSeconds":300,"cpuIntensity":3,"memoryMB":0,"heartbeatEnabled":true}'

# Memory-intensive load test
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id load-test-memory-1 \
  --input '{"durationSeconds":300,"cpuIntensity":0,"memoryMB":512,"heartbeatEnabled":true}'

# Mixed load test
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id load-test-mixed-1 \
  --input '{"durationSeconds":300,"cpuIntensity":3,"memoryMB":256,"heartbeatEnabled":true}'
```

## Load Test Parameters

The `LoadTestParams` struct accepts the following parameters:

| Parameter | Type | Description |
|-----------|------|-------------|
| `durationSeconds` | int | How long the activity should run (in seconds) |
| `cpuIntensity` | int | CPU usage level: 0=none, 1=light, 2=medium, 3=heavy |
| `memoryMB` | int | Amount of memory to allocate in megabytes |
| `heartbeatEnabled` | bool | Whether to send activity heartbeats |

### CPU Intensity Levels

- **0 (None)**: No CPU work, just sleeps
- **1 (Light)**: Basic math operations, ~10-20% CPU per worker
- **2 (Medium)**: Mathematical computations, ~40-60% CPU per worker
- **3 (Heavy)**: SHA256 hashing in loops, ~100% CPU per worker

## Testing HPA Scenarios

### Scenario 1: CPU-Based Scaling

Test CPU-based HPA by generating high CPU load:

```bash
# Start with low CPU
./loadtest.sh cpu 5

# Monitor HPA
kubectl get hpa -w

# Increase CPU pressure
./loadtest.sh cpu 20

# Watch pods scale up
kubectl get pods -l app=your-worker-app -w
```

Expected behavior:
- CPU utilization increases
- HPA scales up pod count when CPU threshold is exceeded
- Additional pods come online and share the load
- CPU utilization decreases as load is distributed

### Scenario 2: Memory-Based Scaling

Test memory-based HPA:

```bash
# Start memory-intensive workflows
./loadtest.sh memory 10

# Each workflow allocates 512MB
# Monitor memory usage
kubectl top pods -l app=your-worker-app
```

Expected behavior:
- Memory utilization increases
- HPA scales up when memory threshold is exceeded
- New pods share the memory load

### Scenario 3: Backlog-Based Scaling

Test scaling based on workflow backlog (requires custom metrics):

```bash
# Create a large backlog
./loadtest.sh backlog 100

# Workflows will queue up if workers are busy
# Monitor queue depth in Temporal UI
```

Expected behavior:
- Workflows queue up waiting for available workers
- If HPA is configured with Temporal queue metrics, it scales up
- More workers come online to process the backlog
- Queue depth decreases

### Scenario 4: Scale Down Behavior

Test scale-down after load decreases:

```bash
# Generate short-duration load
temporal workflow start \
  --task-queue hello-world \
  --type LoadTestWorkflow \
  --workflow-id load-test-short \
  --input '{"durationSeconds":60,"cpuIntensity":3,"memoryMB":256,"heartbeatEnabled":true}'

# Wait for workflows to complete
# Monitor HPA scale-down (usually takes 5-10 minutes)
kubectl get hpa -w
```

## Monitoring

### Watch HPA Status

```bash
kubectl get hpa -w
```

Example output:
```
NAME              REFERENCE                    TARGETS         MINPODS   MAXPODS   REPLICAS
worker-hpa        Deployment/worker            45%/80%         2         10        3
```

### Monitor Pod Metrics

```bash
kubectl top pods -l app=your-worker-app
```

Example output:
```
NAME                      CPU(cores)   MEMORY(bytes)
worker-6d4b8c9f5d-abc12   850m         512Mi
worker-6d4b8c9f5d-def34   920m         480Mi
```

### View Workflow Executions

```bash
temporal workflow list --query 'WorkflowType="LoadTestWorkflow"'
```

### Watch Pod Count Changes

```bash
watch 'kubectl get pods -l app=your-worker-app | grep Running | wc -l'
```

## HPA Configuration Examples

### CPU-Based HPA

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: worker-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: your-worker-deployment
  minReplicas: 2
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 80
```

### Memory-Based HPA

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: worker-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: your-worker-deployment
  minReplicas: 2
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
```

### Combined CPU + Memory HPA

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: worker-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: your-worker-deployment
  minReplicas: 2
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 80
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 0
      policies:
      - type: Percent
        value: 100
        periodSeconds: 15
    scaleDown:
      stabilizationWindowSeconds: 300
      policies:
      - type: Percent
        value: 50
        periodSeconds: 15
```

## Tips and Best Practices

### 1. Set Resource Requests and Limits

Make sure your worker pods have resource requests set:

```yaml
resources:
  requests:
    cpu: 500m
    memory: 512Mi
  limits:
    cpu: 2000m
    memory: 2Gi
```

Without requests, HPA cannot calculate utilization percentages.

### 2. Start Small

Begin with a small number of workflows to understand baseline behavior:

```bash
./loadtest.sh cpu 5
```

### 3. Gradual Scaling

Test HPA's response to gradual load increases:

```bash
./loadtest.sh cpu 5
sleep 120
./loadtest.sh cpu 5
sleep 120
./loadtest.sh cpu 10
```

### 4. Monitor Temporal Worker Slots

Workers have limited activity/workflow slots. Check worker configuration:

```bash
# View worker logs
kubectl logs -l app=your-worker-app --tail=100
```

### 5. Scale-Down Testing

HPA has default cooldown periods for scale-down. Test with:
- Short duration workflows (30-60 seconds)
- Wait 5-10 minutes to observe scale-down
- Customize scale-down behavior in HPA spec

### 6. Metrics Server

Ensure metrics-server is running for resource metrics:

```bash
kubectl get deployment metrics-server -n kube-system
```

## Troubleshooting

### HPA shows "unknown" targets

```bash
# Check if metrics-server is running
kubectl get apiservice v1beta1.metrics.k8s.io

# Check pod resource requests are set
kubectl describe deployment your-worker-deployment
```

### Pods not scaling

```bash
# Check HPA status
kubectl describe hpa worker-hpa

# Check HPA events
kubectl get events --field-selector involvedObject.name=worker-hpa

# Verify pods have resource requests
kubectl get pods -o jsonpath='{.items[*].spec.containers[*].resources}'
```

### Load not generating

```bash
# Check if workflows are running
temporal workflow list

# Check worker logs
kubectl logs -l app=your-worker-app --tail=50

# Verify task queue name matches
kubectl get pods -l app=your-worker-app -o yaml | grep TASK_QUEUE
```

## Cleanup

Stop all load test workflows:

```bash
# List running load test workflows
temporal workflow list --query 'WorkflowId STARTS_WITH "load-test-"'

# Terminate all load test workflows
temporal workflow terminate --query 'WorkflowId STARTS_WITH "load-test-"' --reason "cleanup"
```

Or wait for them to complete naturally (based on `durationSeconds`).

## Next Steps: Custom Metrics

For production use, consider scaling based on Temporal-specific metrics:
- Task queue backlog depth
- Task age
- Worker slot utilization

These require setting up custom metrics using Prometheus and a metrics adapter.

