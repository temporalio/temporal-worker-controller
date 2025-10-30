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
  connectionRef:
    name: production-temporal     # Reference to TemporalConnection
  temporalNamespace: production      # Temporal namespace
  taskQueues:                        # Optional: explicit task queue list
    - order-processing
    - payment-processing
```

### Connection Configuration

Reference a `TemporalConnection` resource that defines server details. You can use either mutual TLS (mTLS) or API key authentication, but not both.

**Using mTLS Authentication:**

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: production-temporal
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  mutualTLSSecretRef: 
    name: temporal-cloud-mtls  # Optional: for mTLS
```
**Creating an mTLS Secret:**

The mTLS secret must be of type `kubernetes.io/tls` and contain `tls.crt` (certificate) and `tls.key` (private key) keys.

<details>
<summary>Show detailed creation steps</summary>

**Option 1: Using kubectl from existing files:**

If you already have your certificate and key files:

```bash
kubectl create secret tls temporal-cloud-mtls \
  --cert=/path/to/certificate.pem \
  --key=/path/to/private-key.key \
  --namespace=your-namespace
```

**Option 2: Using kubectl with literal base64-encoded values:**

```bash
kubectl create secret tls temporal-cloud-mtls \
  --cert=<(echo -n "$CERTIFICATE_CONTENT") \
  --key=<(echo -n "$KEY_CONTENT") \
  --namespace=your-namespace
```

**Option 3: Creating via YAML manifest:**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: temporal-cloud-mtls
  namespace: your-namespace
type: kubernetes.io/tls
data:
  # Base64-encoded certificate
  tls.crt: LS0tLS1CRUdJTi...
  # Base64-encoded private key
  tls.key: LS0tLS1CRUdJTi...
```

To generate the base64-encoded values:

```bash
# For tls.crt
cat certificate.pem | base64 -w 0

# For tls.key
cat private-key.key | base64 -w 0
```

</details>

**Using API Key Authentication:**

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: production-temporal
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  apiKeySecretRef:
    name: temporal-api-key  # Name of the Secret
    key: api-key            # Key within the Secret containing the API key token
```

**Creating an API Key Secret:**

The API key secret must be of type `kubernetes.io/opaque` (or you can omit the type field). The API key token should be stored under a key of your choice.

<details>
<summary>Show detailed creation steps</summary>

**Option 1: Using kubectl with literal value:**

```bash
kubectl create secret generic temporal-api-key \
  --from-literal=api-key=your-api-key-token-here \
  --namespace=your-namespace
```

**Option 2: Using kubectl from a file:**

If your API key is stored in a file:

```bash
kubectl create secret generic temporal-api-key \
  --from-file=api-key=/path/to/api-key-file.txt \
  --namespace=your-namespace
```

**Option 3: Creating via YAML manifest:**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: temporal-api-key
  namespace: your-namespace
type: kubernetes.io/opaque
data:
  # Base64-encoded API key token
  # The key name here must match the 'key' field in apiKeySecretRef
  api-key: eW91ci1hcGkta2V5LXRva2VuLWhlcmU=
```

To generate the base64-encoded value:

```bash
echo -n "your-api-key-token-here" | base64
```

</details>

**Important Notes:**
- Both secrets must be created in the same Kubernetes namespace as the `TemporalConnection` resource
- Only one authentication method can be specified per `TemporalConnection` (either `mutualTLSSecretRef` or `apiKeySecretRef`)
- The secret name and key in `apiKeySecretRef` must match the actual Secret resource and data key
- For mTLS secrets, the keys must be named exactly `tls.crt` and `tls.key`

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
    # Optionally provide input to the gate workflow:
    # 1) Inline arbitrary JSON:
    # input:
    #   thresholds:
    #     errorRate: 0.01
    #     p95LatencyMs: 250
    # 2) Or reference a key from a ConfigMap or Secret containing JSON:
    # inputFrom:
    #   configMapKeyRef:
    #     name: gate-input
    #     key: payload.json
    # inputFrom:
    #   secretKeyRef:
    #     name: gate-input
    #     key: payload.json
```

Gate workflow input details:
- Exactly one of `input` or `inputFrom` may be set.
- `input` accepts any JSON object and is passed as the first parameter to the gate workflow.
- `inputFrom` reads a JSON document from the specified `ConfigMap` or `Secret` key.
- The target workflow should declare a single argument matching the JSON shape (e.g., a struct or `json.RawMessage`).

## Advanced Configuration

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
    connectionRef:
      name: production-temporal
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
    connectionRef:
      name: staging-temporal
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

### Multiple Task Queues

Configure workers that handle multiple task queues:

```yaml
workerOptions:
  connectionRef:
    name: production-temporal
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
