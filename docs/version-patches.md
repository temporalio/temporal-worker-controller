# Version-Specific Configuration Patches

The Temporal Worker Controller supports version-specific configuration overrides through the `TemporalWorkerDeploymentPatch` custom resource. This allows fine-grained control over individual worker deployment versions without modifying the main `TemporalWorkerDeployment` resource.

## Overview

Version patches enable you to:

- **Scale specific versions independently**: Adjust replica counts for individual versions based on their workload or importance
- **Customize sunset strategies per version**: Apply different cleanup policies to versions based on their lifecycle needs
- **Maintain operational flexibility**: Make per-version adjustments without affecting the overall deployment strategy

## Use Cases

### 1. Scaling Down Deprecated Versions

When a version is deprecated but still processing workflows, you might want to reduce its resource consumption:

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeploymentPatch
metadata:
  name: scale-down-deprecated-v1
spec:
  temporalWorkerDeploymentName: my-worker-deployment
  versionID: "my-worker-deployment.v1.0.0"
  replicas: 1  # Reduce from default 3 to 1
  sunsetStrategy:
    scaledownDelay: 30m  # Faster scaledown
    deleteDelay: 2h      # Quicker cleanup
```

### 2. Scaling Up Critical Versions

For versions handling critical workflows that require higher availability:

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeploymentPatch
metadata:
  name: scale-up-critical-v2
spec:
  temporalWorkerDeploymentName: my-worker-deployment
  versionID: "my-worker-deployment.v2.1.0"
  replicas: 10  # Increase from default 3 to 10
  sunsetStrategy:
    scaledownDelay: 24h  # Keep running longer
    deleteDelay: 7d      # Preserve for a week
```



## Patch Status

The controller automatically updates patch status to indicate whether patches are successfully applied:

### Status Types

- **Active**: The patch is applied to an existing version
- **Orphaned**: The referenced version no longer exists
- **Invalid**: The referenced TemporalWorkerDeployment doesn't exist

### Status Fields

```yaml
status:
  status: Active
  appliedAt: "2024-01-15T10:30:00Z"
  message: "Patch successfully applied to active version"
  observedGeneration: 1
```

## Controller Behavior

### Reconciliation

1. **Patch Discovery**: The controller watches for changes to `TemporalWorkerDeploymentPatch` resources
2. **Status Updates**: Patch statuses are updated during each reconciliation loop
3. **Spec Application**: When creating or updating deployments, patches are applied to compute the effective configuration
4. **Event Propagation**: Changes to patches trigger reconciliation of the target `TemporalWorkerDeployment`

### Conflict Resolution

- If multiple patches target the same version and field, the last applied patch wins
- Patches are applied in alphabetical order by name for deterministic behavior
- Invalid patches are marked as such and do not affect deployment behavior

## Best Practices

### Naming Conventions

Use descriptive names that indicate the purpose and target:

```yaml
metadata:
  name: scale-down-v1-deprecated
  # or
  name: extend-sunset-critical-v2
```

### Lifecycle Management

1. **Create patches proactively** for versions you know will need special handling
2. **Monitor patch statuses** to identify orphaned patches that can be cleaned up
3. **Use labels** to group related patches for easier management:

```yaml
metadata:
  labels:
    temporal.io/version-class: deprecated
    temporal.io/scaling-policy: conservative
```

### Resource Management

- **Clean up orphaned patches** regularly to avoid resource accumulation
- **Co-locate patches** with their target deployments in the same namespace
- **Set appropriate RBAC** to control who can create/modify patches

## Limitations

1. **Namespace Scope**: Patches must be in the same namespace as their target `TemporalWorkerDeployment`
2. **Supported Fields**: Currently only `replicas` and `sunsetStrategy` can be overridden
3. **Version Scope**: Patches apply to specific version IDs, not version patterns
4. **Runtime Changes**: Patches are applied during deployment creation/update, not to existing deployments

## Monitoring

### Kubectl Commands

```bash
# List all patches
kubectl get temporalworkerdeploymentpatches

# Show patch details
kubectl describe twdpatch scale-down-old-version

# Check patch status
kubectl get twdpatch -o custom-columns=NAME:.metadata.name,STATUS:.status.status,VERSION:.spec.versionID
```

### Metrics

The controller provides metrics for:
- Number of active/orphaned/invalid patches
- Patch application success/failure rates
- Time since last patch status update

This enables monitoring and alerting on patch health and effectiveness. 