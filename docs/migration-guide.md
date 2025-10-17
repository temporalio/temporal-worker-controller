# Migrating from Unversioned to Versioned Workflows with Temporal Worker Controller

This guide helps teams migrate from unversioned Temporal workflows to versioned workflows using the Temporal Worker Controller. It assumes you are currently running workers without Temporal's Worker Versioning feature and want to adopt versioned worker deployments for safer, more controlled rollouts.

## Important Note

This guide uses specific terminology that is defined in the [Concepts](concepts.md) document. Please review the concepts document first to understand key terms like **Temporal Worker Deployment**, **`TemporalWorkerDeployment` CRD**, and **Kubernetes `Deployment`**, as well as the relationship between them.

## Table of Contents

1. [Why Migrate to Versioned Workflows](#why-migrate-to-versioned-workflows)
2. [Prerequisites](#prerequisites)
3. [Understanding the Differences](#understanding-the-differences)
4. [Migration Strategy](#migration-strategy)
5. [Step-by-Step Migration](#step-by-step-migration)
6. [Configuration Reference](#configuration-reference)
7. [Testing and Validation](#testing-and-validation)
8. [Common Migration Patterns](#common-migration-patterns)
9. [Troubleshooting](#troubleshooting)

For detailed configuration options, see the [Configuration Reference](configuration.md) document.

## Why Migrate to Versioned Workflows

If you're currently running unversioned Temporal workflows, you may be experiencing challenges with deployments. Versioned workflows with the Temporal Worker Controller can solve these problems. For details on the benefits of Worker Versioning, see the [Temporal documentation](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning).

## Prerequisites

Before starting the migration, ensure you have:

- ✅ **Unversioned Temporal workers**: Currently running workers without Worker Versioning
- ✅ **Kubernetes cluster**: Running Kubernetes 1.19+ with CustomResourceDefinition support
- ✅ **Basic worker configuration**: Workers connect to Temporal with namespace and task queue configuration
- ✅ **Administrative access**: Ability to install Custom Resource Definitions and controllers
- ✅ **Deployment pipeline**: Existing CI/CD system that can be updated to use new deployment method

### Current Environment Variables

Your workers are likely configured with basic environment variables like:

```bash
TEMPORAL_ADDRESS=your-temporal-namespace.tmprl.cloud:7233
TEMPORAL_NAMESPACE=your-temporal-namespace
# No TEMPORAL_DEPLOYMENT_NAME or TEMPORAL_WORKER_BUILD_ID yet
```

The controller will automatically add the versioning-related environment variables during migration.

## Understanding the Differences

### Before: Unversioned Workers

```yaml
# Single deployment, all workers run the same code
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-worker
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: worker
        image: my-worker:v1.2.3
        env:
        - name: TEMPORAL_ADDRESS
          value: "production.tmprl.cloud:7233"
        - name: TEMPORAL_NAMESPACE
          value: "production"
        - name: TEMPORAL_TLS_CLIENT_CERT_PATH
          value: "/path/to/temporal.cert"
        - name: TEMPORAL_TLS_CLIENT_KEY_PATH
          value: "/path/to/temporal.key"
        # No versioning environment variables
```

**Current Deployment Process:**
1. Build new worker image
2. Update Deployment with new image
3. Kubernetes rolls out new pods, terminating old ones
4. **Risk**: Running workflows may fail if code changes break compatibility

### After: Versioned Workers with Controller

```yaml
# Single Custom Resource manages multiple versions of a worker deployment automatically
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: my-worker
spec:
  replicas: 3
  workerOptions:
    connectionRef:
      name: production-temporal
    temporalNamespace: production
  rollout:
    strategy: Progressive  # Gradual rollout of new versions
    steps:
      - rampPercentage: 10
        pauseDuration: 5m
      - rampPercentage: 50
        pauseDuration: 10m
  sunset:
    scaledownDelay: 1h
    deleteDelay: 24h
  template:
    spec: # Any changes to this spec will trigger the controller to deploy a new version.
      containers:
      - name: worker
        image: my-worker:v1.2.4  # This is the most common value to change, as you roll out a new worker image.
        # Note: Controller automatically adds versioning environment variables:
        # TEMPORAL_ADDRESS, TEMPORAL_NAMESPACE, TEMPORAL_DEPLOYMENT_NAME, TEMPORAL_WORKER_BUILD_ID
```

**New Deployment Process:**
1. Build new worker image  
2. Update `TemporalWorkerDeployment` custom resource with new image
3. Controller creates new Kubernetes `Deployment` for the new version
4. Controller gradually routes new workflows and existing AutoUpgrade workflows to new version
5. Old version continues handling existing Pinned workflows until they complete
6. **Safety**: No disruption to running workflows, automated rollout control

**Key Benefits:**
- ✅ **Zero-disruption deployments** - Running workflows continue on original version
- ✅ **Automated version management** - Controller handles registration and routing
- ✅ **Progressive rollouts** - Gradual traffic shifting with automatic pause points
- ✅ **Easy rollbacks** - Instantly route new workflows back to previous version
- ✅ **Workflow continuity** - Deterministic execution preserved across deployments

## Migration Strategy

### Safe Migration Approach

The migration from unversioned to versioned workflows requires careful planning to avoid disrupting running workflows. The key is to transition gradually while maintaining workflow continuity.

**Key Principles:**
- **Start with Progressive strategy with conservative settings** - Experience the controller's main value while maintaining safety
- **Migrate one worker deployment at a time** - Reduces risk and allows learning
- **Test thoroughly in non-production** - Validate the approach before production migration
- **Preserve running workflows** - Ensure in-flight workflows complete successfully
- **Use very conservative ramp percentages initially** - Start with 1-5% ramps to minimize risk

### Migration Phases

#### Phase 1: Preparation
1. **Install the controller** in non-production environments
2. **Update worker code** to support versioning
3. **Test migration process** with non-critical workers
4. **Prepare CI/CD pipeline changes** for new deployment method

#### Phase 2: Initial Migration
1. **Choose lowest-risk worker** to migrate first
2. **Create `TemporalWorkerDeployment` custom resource** with a Progressive strategy (conservative intervals recommended)
3. **Validate controller management** works correctly
4. **Update deployment pipeline** for this worker

#### Phase 3: Gradual Rollout
1. **Migrate remaining workers** one at a time
2. **Monitor and tune** rollout configurations
3. **Train team** on new deployment process

### Recommended Migration Order

1. **Background/batch processing workers** - Lower risk, easier to validate
2. **Internal service workers** - Limited external impact
3. **Customer-facing workers** - Highest risk, migrate last with most care

## Step-by-Step Migration

### Step 1: Install the Temporal Worker Controller

```bash
# Install the controller using Helm
helm install -n temporal-system --create-namespace \
  temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller
  
```

### Step 2: Create TemporalConnection Resources

Define connection parameters to your Temporal server(s):

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: production-temporal
  namespace: default
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  mutualTLSSecretRef: 
    name: temporal-cloud-mtls  # Optional: for mTLS
---
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: staging-temporal
  namespace: default
spec:
  hostPort: "staging.abc123.tmprl.cloud:7233"
  mutualTLSSecretRef: 
    name: temporal-cloud-mtls
```

### Step 3: Prepare Your Worker Code

Update your worker initialization code to properly handle versioning:

**Before (Unversioned):**
```go
// Worker connects without versioning
worker := worker.New(client, "my-task-queue", worker.Options{})
```

**After (Versioned):**
```go
// Worker must use the build ID/deployment name from environment
// These are set on the deployment by the controller
buildID := os.Getenv("TEMPORAL_WORKER_BUILD_ID")
deploymentName := os.Getenv("TEMPORAL_DEPLOYMENT_NAME")
if buildID == "" || deploymentName == "" {
  // exit with an error
}
workerOptions := worker.Options{}
workerOptions.DeploymentOptions = worker.DeploymentOptions{
  UseVersioning: true,
  Version: worker.WorkerDeploymentVersion{
    DeploymentName: deploymentName,
    BuildId:        buildId,
  },
}
worker := worker.New(client, "my-task-queue", workerOptions)
```

### Step 4: Create Your First TemporalWorkerDeployment

Start with your lowest-risk worker. Make a copy of your existing unversioned Deployment and convert it to a `TemporalWorkerDeployment` custom resource:

**Existing Unversioned Deployment:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: payment-processor
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: worker
        image: payment-processor:v1.5.2
        env:
        - name: TEMPORAL_HOST_PORT
          value: "production.tmprl.cloud:7233"
        - name: TEMPORAL_NAMESPACE
          value: "production"
```

**New TemporalWorkerDeployment custom resource (IMPORTANT: Use Progressive strategy with conservative settings initially):**
```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: payment-processor
  labels:
    app: payment-processor
spec:
  replicas: 3
  workerOptions:
    connectionRef:
      name: production-temporal
    temporalNamespace: production
  # Start with Progressive strategy using conservative ramp percentages
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 1
        pauseDuration: 10m
      - rampPercentage: 5
        pauseDuration: 15m
      - rampPercentage: 25
        pauseDuration: 20m
  sunset:
    scaledownDelay: 30m
    deleteDelay: 2h
  template:
    spec:
      containers:
      - name: worker
        image: payment-processor:v1.5.2  # Same image as current deployment to ensure no breaking changes
        resources:
          requests:
            memory: "512Mi"
            cpu: "250m"
        # Note: Controller automatically adds TEMPORAL_* env vars
```

### Step 5: Deploy the TemporalWorkerDeployment

1. **Create the `TemporalWorkerDeployment` custom resource:**
   ```bash
   kubectl apply -f payment-processor-versioned.yaml
   ```

2. **Wait for the version to be registered:**
   ```bash
   # Monitor until status shows the version is registered and current
   kubectl get temporalworkerdeployment payment-processor -w
   ```
   
   You should see the controller create a new Kubernetes `Deployment` resource (e.g., `payment-processor-v1.5.2`) for this version.

3. **Verify the versioned deployment is working:**
   ```bash
   # Check that controller-managed deployment exists
   kubectl get deployments -l temporal.io/managed-by=temporal-worker-controller
   
   # Check pods are running
   kubectl get pods -l temporal.io/worker-deployment=payment-processor
   
   # Check worker logs to verify versioning is active
   kubectl logs -l temporal.io/worker-deployment=payment-processor
   ```

   You should see logs indicating the worker has registered with a Build ID.

4. **Verify in Temporal UI:**
   - Check the Workers page in Temporal UI
   - You should see your worker deployment with version information
   - New workflows should be routed to the versioned worker

### Step 6: Transition from Unversioned to Versioned

Now you need to carefully transition from your old unversioned deployment to the new versioned one:

1. **Ensure both deployments are running:**
   ```bash
   # Check your original unversioned deployment
   kubectl get deployment payment-processor
   
   # Check the new controller-managed versioned deployment  
   kubectl get deployments -l temporal.io/managed-by=temporal-worker-controller
   ```

2. **Monitor workflow routing:**
   - In Temporal UI, check that new workflows are being routed to the versioned worker
   - Existing workflows should continue on the unversioned worker until they complete

3. **Wait for existing workflows to complete:**
   ```bash
   # Monitor running workflows in Temporal UI
   # Or use Temporal CLI to check workflow status
   temporal workflow list --namespace production
   ```

4. **Scale down the original unversioned deployment:**
   ```bash
   # Scale down the original deployment
   kubectl scale deployment payment-processor --replicas=0
   ```

5. **Clean up the original deployment:**
   ```bash
   # Only after confirming all workflows are handled by versioned workers
   kubectl delete deployment payment-processor
   ```

### Step 7: Optimize Rollout Settings

Once the initial migration is complete and validated, you can optimize rollout settings for faster deployments:

```bash
# Update the TemporalWorkerDeployment custom resource to use faster Progressive rollout
kubectl patch temporalworkerdeployment payment-processor --type='merge' -p='{
  "spec": {
    "rollout": {
      "strategy": "Progressive",
      "steps": [
        {"rampPercentage": 10, "pauseDuration": "5m"},
        {"rampPercentage": 50, "pauseDuration": "10m"}
      ]
    }
  }
}'
```

### Step 8: Update Your CI/CD Pipeline

Modify your deployment pipeline to work with the new versioned approach:

**Before (Unversioned):**
```bash
# Old pipeline updated Deployment directly
kubectl set image deployment/payment-processor worker=payment-processor:v1.6.0
```

**After (Versioned):**
```bash
# New pipeline updates TemporalWorkerDeployment custom resource
kubectl patch temporalworkerdeployment payment-processor --type='merge' -p='{"spec":{"template":{"spec":{"containers":[{"name":"worker","image":"payment-processor:v1.6.0"}]}}}}'
```

**What happens next:**
1. Controller detects the image change
2. Creates a new Kubernetes `Deployment` (e.g., `payment-processor-v1.6.0`)
3. Registers the new version with Temporal
4. Gradually routes traffic according to Progressive strategy
5. Scales down and cleans up old version once new version is fully deployed

### Step 9: Test Your First Versioned Deployment

Deploy a new version to validate the entire flow:

1. **Make a small, safe change** to your worker code
2. **Build and push** a new container image
3. **Update the TemporalWorkerDeployment:**
   ```bash
   kubectl patch temporalworkerdeployment payment-processor --type='merge' -p='{"spec":{"template":{"spec":{"containers":[{"name":"worker","image":"payment-processor:v1.6.0"}]}}}}'
   ```
4. **Monitor the rollout:**
   ```bash
   # Watch the deployment progress
   kubectl get temporalworkerdeployment payment-processor -w
   
   # Check that new deployment is created
   kubectl get deployments -l temporal.io/managed-by=temporal-worker-controller
   ```
5. **Verify in Temporal UI** that traffic is gradually shifting to the new version

## Configuration Reference

For comprehensive configuration options including rollout strategies, sunset configuration, worker options, and advanced settings, see the [Configuration Reference](configuration.md) document.

Key configuration patterns for migration:

- **Progressive Strategy (Recommended)**: Start with conservative ramp percentages (1%, 5%, 25%) for initial migrations
- **AllAtOnce Strategy**: For development/staging environments where speed is preferred over gradual rollout  
- **Manual Strategy**: Only for advanced use cases requiring full manual control
- **Sunset Configuration**: Configure delays for scaling down and deleting old versions

See [Configuration Reference](configuration.md) for detailed examples and advanced configuration options.

## Testing and Validation

### Pre-Migration Testing

1. **Test in non-production environment:**
   ```bash
   # Create test TemporalWorkerDeployment
   kubectl apply -f test-worker.yaml
   
   # Verify worker registration
   kubectl logs -l temporal.io/worker-deployment=test-worker
   ```

2. **Validate environment variables:**
   ```bash
   kubectl exec -it deployment/test-worker-v1 -- env | grep TEMPORAL
   ```

### Post-Migration Validation

1. **Check version status:**
   ```bash
   kubectl get temporalworkerdeployment -o wide
   ```

2. **Monitor version transitions:**
   ```bash
   kubectl get events --field-selector involvedObject.kind=TemporalWorkerDeployment
   ```

3. **Validate workflow routing:**
   - Start test workflows
   - Verify they're routed to correct versions
   - Check Temporal UI for version distribution

## Common Migration Patterns

Here are common scenarios when migrating from unversioned to versioned workflows:

### Pattern 1: Microservices Architecture

If you have multiple services with their own workers, and each service is versioned and patched separately, migrate each service independently:

```yaml
# Payment service worker
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: payment-processor
  namespace: payments
spec:
  workerOptions:
    connection: production-temporal
    temporalNamespace: payments
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 5
        pauseDuration: 10m
      - rampPercentage: 25
        pauseDuration: 15m
  # ... rest of config
---
# Notification service worker (separate service, different risk profile)
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: notification-sender
  namespace: notifications
spec:
  workerOptions:
    connection: production-temporal
    temporalNamespace: notifications
  rollout:
    strategy: AllAtOnce  # Lower risk, faster rollouts desired
  # ... rest of config
```

### Pattern 2: Environment-Specific Strategies

Use different rollout strategies based on environment risk:

```yaml
# Production - Conservative rollout
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: order-processor
  namespace: production
spec:
  workerOptions:
    connection: production-temporal
    temporalNamespace: production
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 10
        pauseDuration: 15m
      - rampPercentage: 50
        pauseDuration: 30m
    gate:
      workflowType: "HealthCheck"  # Validate new version before proceeding
---
# Staging - Fast rollout for testing
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: order-processor
  namespace: staging
spec:
  workerOptions:
    connection: staging-temporal
    temporalNamespace: staging
  rollout:
    strategy: AllAtOnce  # Faster rollout
```

### Pattern 3: Gradual Team Migration

Migrate teams/services based on their readiness and risk tolerance:

**Phase 1: Low-Risk Services**
- Background processing workers
- Internal tooling workflows
- Non-customer-facing operations

**Phase 2: Medium-Risk Services**  
- Internal API workflows
- Data processing pipelines
- Administrative workflows

**Phase 3: High-Risk Services**
- Customer-facing workflows
- Payment processing
- Critical business operations

## Troubleshooting

### Common Issues

**1. Workers Not Registering with Temporal**

*Symptoms:*
```
status:
  targetVersion:
    status: NotRegistered
```

*Solutions:*
- Check worker logs for connection/initialization errors
- Verify TemporalConnection configuration  
- Ensure TLS secrets are properly configured
- Verify network connectivity to Temporal server
- Check that worker code properly handles `TEMPORAL_DEPLOYMENT_NAME` and `TEMPORAL_WORKER_BUILD_ID` environment variables

**2. New Workflows Still Going to Unversioned Workers**

*Symptoms:*
- Temporal UI shows workflows executing on unversioned workers
- Versioned workers appear idle

*Solutions:*
- Verify versioned workers are properly registered in Temporal UI
- Check that new workflows are starting on the correct task queue
- Ensure unversioned workers are scaled down gradually, not immediately
- Verify Temporal routing rules are working correctly

**3. Existing Workflows Failing During Migration**

*Symptoms:*
- Running workflows encounter errors during migration
- Workflow history shows non-deterministic errors

*Solutions:*
- Ensure unversioned workers remain running until workflows complete
- Don't force-terminate unversioned workers with running workflows
- Check that worker code changes are backward compatible
- Monitor workflow completion before scaling down old workers

**4. Version Stuck in Ramping State**

*Symptoms:*
```
status:
  targetVersion:
    status: Ramping
    rampPercentage: 10
```

*Solutions:*
- Check if gate workflow is configured and completing successfully
- Verify progressive rollout steps are reasonable
- Check controller logs for errors
- Ensure new version is healthy and processing workflows correctly

### Debugging Commands

```bash
# Check controller logs
kubectl logs -n temporal-worker-controller-system deployment/controller-manager

# Check worker status
kubectl describe temporalworkerdeployment my-worker

# Check managed deployments
kubectl get deployments -l temporal.io/managed-by=temporal-worker-controller

# Check worker logs
kubectl logs -l temporal.io/worker-deployment=my-worker

# Check controller events
kubectl get events --field-selector involvedObject.kind=TemporalWorkerDeployment
```

### Getting Help

1. **Check controller logs** for error messages
2. **Review TemporalWorkerDeployment status** for detailed state information
3. **Verify Temporal server connectivity** from worker pods
4. **File issues** at the project repository with logs and configuration

## Migration Summary

🎯 **Key principles for unversioned to versioned migration**: 
- **Start with Progressive strategy using conservative ramp percentages** to experience the controller's value while maintaining safety
- **Run both unversioned and versioned workers** during transition period
- **Wait for existing workflows to complete** before scaling down unversioned workers
- **Begin with very conservative ramp percentages (1-5%)** and optimize after validating the migration process
- **Migrate one service at a time** to reduce risk and enable learning

See the [Concepts](concepts.md) document for detailed explanations of the resource relationships and terminology.

This approach ensures a safe transition from unversioned to versioned workflows without disrupting running workflows or introducing deployment risks.

The Temporal Worker Controller should significantly improve your deployment safety and reduce the risk of workflow disruptions while providing automated rollout capabilities that weren't possible with unversioned workflows.
