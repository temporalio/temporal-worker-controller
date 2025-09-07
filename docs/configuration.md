# Configuration Reference

This document provides comprehensive configuration options for the Temporal Worker Controller.

## Table of Contents

1. [Rollout Strategies](#rollout-strategies)
2. [Sunset Configuration](#sunset-configuration)
3. [Worker Options](#worker-options)
4. [Gate Configuration](#gate-configuration)
5. [Advanced Configuration](#advanced-configuration)

## Rollout Strategies

See the [Concepts](concepts.md) document for detailed explanations of rollout strategies. Here are the basic configuration patterns:

### Manual Strategy (Advanced Use Cases)

```yaml
rollout:
  strategy: Manual
# Requires manual intervention to promote versions
# Only recommended for special cases requiring full manual control
```

Use Manual strategy when you need complete control over version promotions, such as:
- Complex validation processes that require human approval
- Coordinated deployments across multiple services
- Special compliance or regulatory requirements

### AllAtOnce Strategy

```yaml
rollout:
  strategy: AllAtOnce
# Immediately routes 100% traffic to new version when healthy
```

Use AllAtOnce strategy for:
- Low-risk environments (development, staging)
- Services where fast deployment is more important than gradual rollout
- Background processing workers with minimal user impact

### Progressive Strategy (Recommended)

```yaml
rollout:
  strategy: Progressive
  steps:
    # Conservative initial migration settings
    - rampPercentage: 1
      pauseDuration: 10m
    - rampPercentage: 5  
      pauseDuration: 15m
    - rampPercentage: 25
      pauseDuration: 20m
    # Can be optimized to faster ramps after validation:
    # - rampPercentage: 10
    #   pauseDuration: 5m
    # - rampPercentage: 50
    #   pauseDuration: 10m
  gate:
    workflowType: "HealthCheck"  # Optional validation workflow
```

Progressive strategy is recommended for most production deployments because it:
- Minimizes risk by gradually increasing traffic to new versions
- Provides automatic pause points for validation
- Allows for quick rollback if issues are detected
- Can be tuned for different risk tolerances

#### Progressive Rollout Examples

**Conservative Production Rollout:**
```yaml
rollout:
  strategy: Progressive
  steps:
    - rampPercentage: 1
      pauseDuration: 15m
    - rampPercentage: 5
      pauseDuration: 30m
    - rampPercentage: 25
      pauseDuration: 45m
    - rampPercentage: 75
      pauseDuration: 30m
  gate:
    workflowType: "ProductionHealthCheck"
```

**Faster Development Environment:**
```yaml
rollout:
  strategy: Progressive
  steps:
    - rampPercentage: 25
      pauseDuration: 2m
    - rampPercentage: 75
      pauseDuration: 3m
```

**Canary-Style Rollout:**
```yaml
rollout:
  strategy: Progressive
  steps:
    - rampPercentage: 1
      pauseDuration: 30m  # Long canary period
    - rampPercentage: 100
      pauseDuration: 0s   # Full rollout after canary validation
```

## Sunset Configuration

Controls how old versions are scaled down and cleaned up after they're no longer receiving new traffic:

```yaml
sunset:
  scaledownDelay: 1h    # Wait 1 hour after draining before scaling to 0
  deleteDelay: 24h      # Wait 24 hours after draining before deleting
```

### Sunset Configuration Examples

**Conservative Cleanup (Recommended for Production):**
```yaml
sunset:
  scaledownDelay: 2h    # Allow time for workflows to complete
  deleteDelay: 48h      # Keep resources for debugging/rollback
```

**Aggressive Cleanup (Development/Staging):**
```yaml
sunset:
  scaledownDelay: 15m   # Quick scaledown
  deleteDelay: 2h       # Minimal retention
```

**Long-Running Workflow Environment:**
```yaml
sunset:
  scaledownDelay: 24h   # Long-running workflows need time
  deleteDelay: 168h     # 1 week retention for analysis
```

## Worker Options

Configure how workers connect to Temporal:

```yaml
workerOptions:
  connection: production-temporal     # Reference to TemporalConnection
  temporalNamespace: production      # Temporal namespace
  taskQueues:                        # Optional: explicit task queue list
    - order-processing
    - payment-processing
```

### Connection Configuration

Reference a `TemporalConnection` resource that defines server details:

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: production-temporal
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  mutualTLSSecret: temporal-cloud-mtls  # Optional: for mTLS
```

## Gate Configuration

Optional validation workflow that must succeed before proceeding with rollout:

```yaml
rollout:
  strategy: Progressive
  steps:
    - rampPercentage: 10
      pauseDuration: 5m
  gate:
    workflowType: "HealthCheck"
    input: |
      {
        "version": "{{.Version}}",
        "environment": "production"
      }
    timeout: 300s
```

### Gate Workflow Examples

**Simple Health Check:**
```yaml
gate:
  workflowType: "HealthCheck"
  timeout: 60s
```

**Complex Validation with Input:**
```yaml
gate:
  workflowType: "ValidationWorkflow"
  input: |
    {
      "deploymentName": "{{.DeploymentName}}",
      "buildId": "{{.BuildId}}",
      "rampPercentage": {{.RampPercentage}},
      "environment": "{{.Environment}}"
    }
  timeout: 600s
```

## Advanced Configuration

### Resource Limits and Requests

Configure Kubernetes resource limits for worker pods:

```yaml
template:
  spec:
    containers:
    - name: worker
      image: my-worker:latest
      resources:
        requests:
          memory: "512Mi"
          cpu: "250m"
        limits:
          memory: "1Gi"
          cpu: "500m"
```

### Environment-Specific Configurations

**Production Configuration:**
```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: order-processor
  namespace: production
spec:
  replicas: 5
  workerOptions:
    connection: production-temporal
    temporalNamespace: production
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 1
        pauseDuration: 15m
      - rampPercentage: 10
        pauseDuration: 30m
      - rampPercentage: 50
        pauseDuration: 45m
    gate:
      workflowType: "ProductionHealthCheck"
      timeout: 300s
  sunset:
    scaledownDelay: 2h
    deleteDelay: 48h
```

**Staging Configuration:**
```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: order-processor
  namespace: staging
spec:
  replicas: 2
  workerOptions:
    connection: staging-temporal
    temporalNamespace: staging
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 25
        pauseDuration: 5m
      - rampPercentage: 100
        pauseDuration: 0s
  sunset:
    scaledownDelay: 30m
    deleteDelay: 4h
```

### Labels and Annotations

Add custom labels and annotations to managed resources:

```yaml
template:
  metadata:
    labels:
      app: my-worker
      team: platform
      environment: production
    annotations:
      deployment.kubernetes.io/revision: "1"
  spec:
    # ... container spec
```

### Multiple Task Queues

Configure workers that handle multiple task queues:

```yaml
workerOptions:
  connection: production-temporal
  temporalNamespace: production
  taskQueues:
    - order-processing
    - payment-processing
    - notification-sending
```

## Configuration Validation

The controller validates configuration and will report errors in the resource status:

```bash
# Check for configuration errors
kubectl describe temporalworkerdeployment my-worker

# Look for validation errors in status
kubectl get temporalworkerdeployment my-worker -o yaml
```

Common validation errors:
- Invalid ramp percentages (must be 1-100)
- Invalid duration formats (use Go duration format: "5m", "1h", "30s")
- Missing required fields (connection, temporalNamespace)
- Invalid strategy combinations

For more examples and patterns, see the [Migration Guide](migration-guide.md) and [Concepts](concepts.md) documentation.
