# Migrating to Temporal Worker Controller

This guide helps teams migrate from their existing versioned worker deployment systems to the Temporal Worker Controller. It assumes you are already running versioned workers using Temporal's Worker Versioning feature and want to automate the management of these deployments.

## Important Terminology

To avoid confusion, this guide uses specific terminology:

- **Temporal Worker Deployment**: A logical grouping in Temporal (e.g., "payment-processor", "notification-sender")
- **`TemporalWorkerDeployment` CRD**: The Kubernetes custom resource that manages one Temporal Worker Deployment
- **Kubernetes `Deployment`**: The actual k8s Deployment resources that run worker pods (multiple per Temporal Worker Deployment, one per version)

**Key Relationship**: One `TemporalWorkerDeployment` CRD → Multiple Kubernetes `Deployment` resources (managed by controller)

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Understanding the Differences](#understanding-the-differences)
3. [Migration Strategy](#migration-strategy)
4. [Step-by-Step Migration](#step-by-step-migration)
5. [Configuration Mapping](#configuration-mapping)
6. [Testing and Validation](#testing-and-validation)
7. [Common Migration Patterns](#common-migration-patterns)
8. [Troubleshooting](#troubleshooting)

## Prerequisites

Before starting the migration, ensure you have:

- ✅ **Existing versioned workers**: Your workers are already using Temporal's Worker Versioning feature
- ✅ **Kubernetes cluster**: Running Kubernetes 1.19+ with CustomResourceDefinition support
- ✅ **Worker configuration**: Workers are configured with deployment names and build IDs
- ✅ **Rainbow deployments**: Currently managing multiple concurrent versions manually
- ✅ **Administrative access**: Ability to install Custom Resource Definitions and controllers

### Environment Variables Your Workers Should Already Use

Your workers should already be configured with these standard environment variables:

```bash
TEMPORAL_HOST_PORT=your-namespace.tmprl.cloud:7233
TEMPORAL_NAMESPACE=your-temporal-namespace
TEMPORAL_DEPLOYMENT_NAME=your-deployment-name
WORKER_BUILD_ID=your-build-id
```

If you're using different variable names, you'll need to update your worker code during migration.

## Understanding the Differences

### Before: Manual Version Management

```yaml
# You currently manage multiple deployments manually
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-worker-v1
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: worker
        image: my-worker:v1.2.3
        env:
        - name: WORKER_BUILD_ID
          value: "v1.2.3"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-worker-v2
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: worker
        image: my-worker:v1.2.4
        env:
        - name: WORKER_BUILD_ID
          value: "v1.2.4"
```

### After: Controller-Managed Versions

```yaml
# Single resource manages all versions automatically
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: my-worker
spec:
  replicas: 3
  workerOptions:
    connection: production-temporal
    temporalNamespace: production
  cutover:
    strategy: Manual  # Use Manual during migration, then switch to Progressive
  sunset:
    scaledownDelay: 1h
    deleteDelay: 24h
  template:
    spec:
      containers:
      - name: worker
        image: my-worker:v1.2.4  # Update this image to import/deploy versions
```

**Key Differences:**
- ✅ **Single CRD resource** manages multiple versions instead of multiple manual Kubernetes Deployments
- ✅ **Controller creates Kubernetes Deployments** automatically (one per version)
- ✅ **Automated lifecycle** handles registration, routing, and cleanup
- ✅ **Version history** tracked in the CRD resource status
- ✅ **Rollout strategies** automate traffic shifting between versions

## Migration Strategy

### The Single Resource Requirement

⚠️ **CRITICAL**: For each **Temporal Worker Deployment**, you MUST create exactly **ONE** `TemporalWorkerDeployment` CRD resource and import all existing versions into it. This is not optional - it's how the system works.

**Terminology Clarification:**
- **Temporal Worker Deployment**: A logical grouping in Temporal (e.g., "payment-processor")
- **`TemporalWorkerDeployment` CRD**: One Kubernetes custom resource per Temporal Worker Deployment
- **Kubernetes `Deployment`**: Multiple k8s Deployments (one per version) managed by the controller

**Key Principles:**
- **One CRD resource per Temporal Worker Deployment** - Don't create multiple `TemporalWorkerDeployment` resources for the same logical worker
- **Use Manual strategy during import** - Prevents unwanted automatic promotions
- **Import versions sequentially** - Update the same CRD resource to import each existing version
- **Enable automation last** - Switch to Progressive strategy only after migration is complete

### Recommended Migration Steps

1. **Install the controller** in your cluster
2. **Choose a non-critical worker** to migrate first
3. **Create a single TemporalWorkerDeployment** with Manual strategy
4. **Import existing versions one by one** by updating the image spec
5. **Clean up manual deployments** after confirming controller management
6. **Enable automated rollouts** by switching to Progressive strategy
7. **Repeat for remaining workers** one deployment at a time

## Step-by-Step Migration

### Step 1: Install the Temporal Worker Controller

```bash
# Install the controller using Helm
helm install --repo FIXME temporal-worker-controller temporal-worker-controller
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
  mutualTLSSecret: temporal-cloud-mtls  # If using mTLS
---
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: staging-temporal
  namespace: default
spec:
  hostPort: "staging.abc123.tmprl.cloud:7233"
  mutualTLSSecret: temporal-cloud-mtls
```

### Step 3: Map Your Current Configuration

For each existing **Temporal Worker Deployment** (logical grouping), identify all currently running versions. You'll create a single `TemporalWorkerDeployment` CRD resource and import each version one by one.

**Current Manual Kubernetes Deployments:**
```yaml
# Version 1 (older, still has running workflows)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: payment-processor-v1
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: worker
        image: payment-processor:v1.5.1
        env:
        - name: WORKER_BUILD_ID
          value: "v1.5.1"
---
# Version 2 (current production version)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: payment-processor-v2
spec:
  replicas: 5
  template:
    spec:
      containers:
      - name: worker
        image: payment-processor:v1.5.2
        env:
        - name: WORKER_BUILD_ID
          value: "v1.5.2"
```

**Single `TemporalWorkerDeployment` CRD Resource (CRITICAL: Use Manual strategy):**
```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: payment-processor
  labels:
    app: payment-processor
spec:
  replicas: 5
  workerOptions:
    connection: production-temporal
    temporalNamespace: production
  # IMPORTANT: Use Manual during migration to prevent unwanted promotions
  cutover:
    strategy: Manual
  sunset:
    scaledownDelay: 30m
    deleteDelay: 2h
  template:
    metadata:
      annotations:
        prometheus.io/scrape: "true"
    spec:
      containers:
      - name: worker
        image: payment-processor:v1.5.1  # Start with oldest version
        resources:
          requests:
            memory: "512Mi"
            cpu: "250m"
        # Note: TEMPORAL_* env vars are set automatically by controller
```

### Step 4: Import Existing Versions One by One

⚠️ **CRITICAL**: Use a single `TemporalWorkerDeployment` CRD resource with `strategy: Manual` to import all existing versions.

1. **Create the `TemporalWorkerDeployment` CRD with the oldest version:**
   ```bash
   kubectl apply -f payment-processor-migration.yaml
   ```

2. **Wait for the first version to be registered:**
   ```bash
   # Monitor until status shows the version is registered and current
   kubectl get temporalworkerdeployment payment-processor -w
   ```
   
   The controller will create a Kubernetes `Deployment` resource (e.g., `payment-processor-v1.5.1`) for this version.

3. **Import the next version by updating the CRD resource:**
   ```bash
   # Edit the TemporalWorkerDeployment CRD to use the next image version
   kubectl patch temporalworkerdeployment payment-processor --type='merge' -p='{"spec":{"template":{"spec":{"containers":[{"name":"worker","image":"payment-processor:v1.5.2"}]}}}}'
   ```

4. **Wait for the new version to be registered:**
   ```bash
   # Monitor until the new version appears in status.targetVersion
   kubectl get temporalworkerdeployment payment-processor -o yaml
   ```

   You should see:
   ```yaml
   status:
     currentVersion:
       versionID: "payment-processor.v1.5.1"
       status: Current
     targetVersion:
       versionID: "payment-processor.v1.5.2"
       status: Inactive  # Will be Inactive because strategy is Manual
   ```
   
   The controller will create another Kubernetes `Deployment` resource (e.g., `payment-processor-v1.5.2`) for this new version.

5. **Repeat for all existing versions** until all are imported under controller management.
   
   Each version update will result in a new Kubernetes `Deployment` being created by the controller.

### Step 5: Validate All Versions Are Imported

Confirm all your existing versions are now managed by the controller:

```bash
# Check all versions are registered in the CRD status
kubectl get temporalworkerdeployment payment-processor -o yaml

# Verify controller-managed Kubernetes Deployments exist for all versions
kubectl get deployments -l temporal.io/managed-by=temporal-worker-controller

# Check pods are running for all versions
kubectl get pods -l temporal.io/worker-deployment=payment-processor
```

You should see multiple Kubernetes `Deployment` resources created by the controller, one for each version you imported.

### Step 6: Clean Up Manual Kubernetes Deployments

⚠️ **Only after confirming all versions are imported and working:**

```bash
# Scale down your original manual Kubernetes Deployments
kubectl scale deployment payment-processor-v1 --replicas=0
kubectl scale deployment payment-processor-v2 --replicas=0

# Monitor that the controller-managed Kubernetes Deployments are handling all traffic
# Check Temporal UI to verify workflows are running on controller-managed versions

# Delete the original manual Kubernetes Deployments
kubectl delete deployment payment-processor-v1 payment-processor-v2
```

At this point, you should only have controller-managed Kubernetes `Deployment` resources running your workers.

### Step 7: Enable Automated Rollouts

Once migration is complete and all legacy versions are imported, update your deployment system to use automated rollouts:

```bash
# Update the TemporalWorkerDeployment CRD to use Progressive strategy for new deployments
kubectl patch temporalworkerdeployment payment-processor --type='merge' -p='{
  "spec": {
    "cutover": {
      "strategy": "Progressive",
      "steps": [
        {"rampPercentage": 5, "pauseDuration": "5m"},
        {"rampPercentage": 25, "pauseDuration": "10m"}, 
        {"rampPercentage": 50, "pauseDuration": "15m"}
      ]
    }
  }
}'
```

From this point forward:
- **New worker versions** will automatically follow the Progressive rollout strategy
- **Your CI/CD system** should update the `spec.template.spec.containers[0].image` field in the `TemporalWorkerDeployment` CRD to deploy new versions
- **The controller** will automatically create new Kubernetes `Deployment` resources, handle version registration, traffic routing, and cleanup

Example CI/CD update command:
```bash
# Your deployment pipeline should update the CRD image field like this:
kubectl patch temporalworkerdeployment payment-processor --type='merge' -p='{"spec":{"template":{"spec":{"containers":[{"name":"worker","image":"payment-processor:v1.6.0"}]}}}}'
```

The controller will automatically create a new Kubernetes `Deployment` (e.g., `payment-processor-v1.6.0`) and manage the rollout.

## Configuration Mapping

### Rollout Strategies

**Manual Strategy (Default Behavior):**
```yaml
cutover:
  strategy: Manual
# Requires manual intervention to promote versions
```

**Immediate Cutover:**
```yaml
cutover:
  strategy: AllAtOnce
# Immediately routes 100% traffic to new version when healthy
```

**Progressive Rollout:**
```yaml
cutover:
  strategy: Progressive
  steps:
    - rampPercentage: 1
      pauseDuration: 5m
    - rampPercentage: 10  
      pauseDuration: 10m
    - rampPercentage: 50
      pauseDuration: 15m
  gate:
    workflowType: "HealthCheck"  # Optional validation workflow
```

### Sunset Configuration

```yaml
sunset:
  scaledownDelay: 1h    # Wait 1 hour after draining before scaling to 0
  deleteDelay: 24h      # Wait 24 hours after draining before deleting
```

### Resource Management

**CPU and Memory:**
```yaml
template:
  spec:
    containers:
    - name: worker
      resources:
        requests:
          memory: "1Gi"
          cpu: "500m"
        limits:
          memory: "2Gi" 
          cpu: "1"
```

**Pod Annotations and Labels:**
```yaml
template:
  metadata:
    annotations:
      prometheus.io/scrape: "true"
      prometheus.io/port: "9090"
    labels:
      team: payments
      environment: production
```

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

⚠️ **Remember**: Each pattern below represents **separate Temporal Worker Deployments**. Each gets exactly **one** `TemporalWorkerDeployment` CRD resource.

### Pattern 1: Microservices with Multiple Workers

```yaml
# payment-service workers (ONE CRD for this Temporal Worker Deployment)
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: payment-processor
spec:
  workerOptions:
    connection: production-temporal
    temporalNamespace: payments
  cutover:
    strategy: Manual  # Use Manual during migration
  # ... rest of config
---
# notification-service workers (separate Temporal Worker Deployment = separate CRD)
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: notification-sender
spec:
  workerOptions:
    connection: production-temporal
    temporalNamespace: notifications
  cutover:
    strategy: Manual  # Use Manual during migration
  # ... rest of config
```

### Pattern 2: Environment-Specific Deployments

```yaml
# Production (use Manual during migration, then switch to Progressive)
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: my-worker
  namespace: production
spec:
  workerOptions:
    connection: production-temporal
    temporalNamespace: production
  cutover:
    strategy: Manual  # Use Manual during migration, then switch to Progressive
---
# Staging (use Manual during migration, then switch to AllAtOnce)
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: my-worker
  namespace: staging
spec:
  workerOptions:
    connection: staging-temporal
    temporalNamespace: staging
  cutover:
    strategy: Manual  # Use Manual during migration, then switch to AllAtOnce
```



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
- Check worker logs for connection errors
- Verify TemporalConnection configuration
- Ensure TLS secrets are properly configured
- Verify network connectivity to Temporal server

**2. Version Stuck in Ramping State**

*Symptoms:*
```
status:
  targetVersion:
    status: Ramping
    rampPercentage: 5
```

*Solutions:*
- Check if gate workflow is configured and completing successfully
- Verify progressive rollout steps are reasonable
- Check controller logs for errors

**3. Old Versions Not Being Cleaned Up**

*Symptoms:*
- Multiple old Deployments still exist
- Deprecated versions not transitioning to Drained

*Solutions:*
- Check if workflows are still running on old versions
- Verify sunset configuration is reasonable
- Check Temporal UI for workflow status

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

🎯 **Remember the core principle**: 
- **One `TemporalWorkerDeployment` CRD per Temporal Worker Deployment**
- **Manual strategy during migration** to prevent unwanted promotions
- **Import existing versions sequentially** by updating the same CRD resource
- **Enable automation only after migration is complete**

**Resource Relationship:**
- **Before**: Multiple manual Kubernetes `Deployment` resources per worker
- **After**: One `TemporalWorkerDeployment` CRD → Controller creates multiple Kubernetes `Deployment` resources (one per version)

This approach ensures a smooth transition from manual version management to controller automation without disrupting running workflows.

## Next Steps

After successful migration:

1. **Set up monitoring** for your TemporalWorkerDeployment resources
2. **Update CI/CD pipelines** to patch TemporalWorkerDeployment image specs instead of managing Deployments directly
3. **Configure alerting** on version transition failures
4. **Train your team** on the new deployment process (single resource updates vs multiple Deployment management)
5. **Document your specific configuration** patterns for future reference

The Temporal Worker Controller should significantly reduce the operational overhead of managing versioned worker deployments while providing better automation and safety for your workflow deployments. 