# CRD Rename Migration Guide

Starting with v1.7, the Temporal Worker Controller renames its two primary CRDs and one field reference:

| Old name | New name |
|---|---|
| `TemporalWorkerDeployment` | `WorkerDeployment` |
| `TemporalConnection` | `Connection` |
| `WorkerResourceTemplate.spec.temporalWorkerDeploymentRef` | `WorkerResourceTemplate.spec.workerDeploymentRef` |

The old CRDs and field remain fully functional in v1.7. They will be removed in v1.8.

## Why the rename?

The `Temporal` prefix was redundant — all resources in the `temporal.io` API group are already scoped to Temporal. The shorter names are consistent with Kubernetes naming conventions and reduce verbosity in manifests and CLI commands.

## What happens to existing resources?

The controller watches both old and new CRD kinds simultaneously. Existing `TemporalWorkerDeployment` and `TemporalConnection` objects continue to work without any manual intervention in v1.7.

The deprecated resources will have a `Ready=False` status condition set by a migration helper controller:

```
TemporalWorkerDeployment foo:
  Ready=False reason=Deprecated
  message: "TemporalWorkerDeployment is deprecated. Create a WorkerDeployment with the same name and spec to migrate."
```

Once a corresponding `WorkerDeployment` with the same name exists in the same namespace, the condition updates to:

```
  Ready=False reason=MigratedToWorkerDeployment
  message: "Migration complete. Delete this TemporalWorkerDeployment."
```

The same pattern applies to `TemporalConnection` → `Connection`.

## Migration steps

### Step 1: Migrate TemporalConnection resources

For each `TemporalConnection` in your cluster, create a corresponding `Connection` with the same name, namespace, and spec:

**Before:**
```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: production-temporal
  namespace: my-namespace
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  apiKeySecretRef:
    name: temporal-api-key
    key: api-key
```

**After (same namespace, same name, same spec):**
```yaml
apiVersion: temporal.io/v1alpha1
kind: Connection
metadata:
  name: production-temporal
  namespace: my-namespace
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  apiKeySecretRef:
    name: temporal-api-key
    key: api-key
```

Apply the new `Connection` resource. You can run both the old `TemporalConnection` and new `Connection` simultaneously — the controller uses whichever the `WorkerDeployment` (or `TemporalWorkerDeployment`) references.

Once the `Connection` is in place and referenced by all `WorkerDeployment` resources, delete the old `TemporalConnection`:

```bash
kubectl delete temporalconnection production-temporal -n my-namespace
```

### Step 2: Migrate TemporalWorkerDeployment resources

For each `TemporalWorkerDeployment`, create a corresponding `WorkerDeployment` with the same name and spec, updating the `connectionRef` to use the new `Connection` kind:

**Before:**
```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: my-worker
  namespace: my-namespace
spec:
  workerOptions:
    connectionRef:
      name: production-temporal
    temporalNamespace: production
  rollout:
    strategy: AllAtOnce
  template:
    spec:
      containers:
        - name: worker
          image: my-worker:v1.2.3
```

**After:**
```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerDeployment
metadata:
  name: my-worker
  namespace: my-namespace
spec:
  workerOptions:
    connectionRef:
      name: production-temporal
    temporalNamespace: production
  rollout:
    strategy: AllAtOnce
  template:
    spec:
      containers:
        - name: worker
          image: my-worker:v1.2.3
```

> **Important**: The new `WorkerDeployment` must have the **same name** as the `TemporalWorkerDeployment` it replaces. The controller uses `{namespace}/{name}` as the Temporal Worker Deployment name; changing the name would create a distinct Temporal Worker Deployment.

Apply the new `WorkerDeployment`. The controller will immediately begin managing it. Because the name is the same, it connects to the same Temporal Worker Deployment that was already managed by the old resource.

Once the new `WorkerDeployment` is healthy and you have verified the controller is reconciling it correctly (check `status.conditions`), delete the old `TemporalWorkerDeployment`:

```bash
kubectl delete temporalworkerdeployment my-worker -n my-namespace
```

### Step 3: Migrate WorkerResourceTemplate references

If you use `WorkerResourceTemplate`, update the `spec.temporalWorkerDeploymentRef` field to `spec.workerDeploymentRef`:

**Before:**
```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-hpa
  namespace: my-namespace
spec:
  temporalWorkerDeploymentRef:
    name: my-worker
  template:
    ...
```

**After:**
```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-hpa
  namespace: my-namespace
spec:
  workerDeploymentRef:
    name: my-worker
  template:
    ...
```

Both `temporalWorkerDeploymentRef` and `workerDeploymentRef` are accepted in v1.7. The controller uses whichever is set. If both are set, `workerDeploymentRef` takes precedence.

### Step 4: Update manifests and tooling

Update any manifests, Helm charts, GitOps configs, or scripts that reference the old CRD kinds or field names:

```bash
# Find all manifests still using old kind names
grep -r "kind: TemporalWorkerDeployment" .
grep -r "kind: TemporalConnection" .
grep -r "temporalWorkerDeploymentRef" .

# Find kubectl invocations using old resource names
grep -r "temporalworkerdeployment" .
grep -r "temporalconnection" .
```

Common kubectl shorthand updates:

| Old | New |
|---|---|
| `kubectl get temporalworkerdeployment` | `kubectl get workerdeployment` |
| `kubectl get temporalconnection` | `kubectl get connection` |
| `kubectl describe temporalworkerdeployment <name>` | `kubectl describe workerdeployment <name>` |

## Timeline

| Version | Status |
|---|---|
| v1.6 and earlier | Only `TemporalWorkerDeployment` and `TemporalConnection` exist |
| **v1.7** | `WorkerDeployment` and `Connection` introduced; deprecated resources still work with migration guidance via status conditions |
| v1.8 (planned) | `TemporalWorkerDeployment` and `TemporalConnection` CRDs removed; `temporalWorkerDeploymentRef` field removed |

Complete the migration before upgrading to v1.8.
