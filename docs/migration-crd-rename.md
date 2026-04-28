# CRD Rename Migration Guide

Starting with Chart Version v0.27.0 (App Version v1.7.0), the Temporal Worker Controller renames its two primary CRDs and one field reference:

| Old name | New name |
|---|---|
| `TemporalWorkerDeployment` | `WorkerDeployment` |
| `TemporalConnection` | `Connection` |
| `WorkerResourceTemplate.spec.temporalWorkerDeploymentRef` | `WorkerResourceTemplate.spec.workerDeploymentRef` |

In App Version v1.7, the old CRD kinds are **not actively managed**: new objects of these kinds cannot be created, and existing objects will never become `Ready` or reconcile with Temporal Server state. The deprecated CRDs exist only to support migration of resources already in your cluster.

## Why the rename?

The `Temporal` prefix was redundant — all resources in the `temporal.io` API group are already scoped to Temporal. The shorter names are consistent with Kubernetes naming conventions and reduce verbosity in manifests and CLI commands.
After this release, the Worker Controller will be Generally Available (GA), which means no more breaking changes will be introduced. We wanted to make this transition before GA, and make it as clean as possible.

## What happens to deprecated resources in App Version v1.7?
Existing `TemporalWorkerDeployment` and `TemporalConnection` objects remain in your cluster but are no longer reconciled against Temporal server state. The controller will not manage worker versions, route traffic, or connect to Temporal on behalf of these resources. New resources of these kinds cannot be created.

The deprecated resources will have a `Ready=False` status condition set by a migration helper controller. The condition progresses through three states:

**Before migration** — if no `WorkerDeployment` with the same name exists:
```
Ready=False reason=Deprecated
message: "TemporalWorkerDeployment is deprecated. Create a WorkerDeployment with the same name and spec to migrate."
```

**Migration in progress** — a `WorkerDeployment` with the same name exists, and the controller is transferring ownership of child `Deployments` and `WorkerResourceTemplates` from the old owner to the new one:
```
Ready=False reason=WorkerDeploymentExists
message: "A WorkerDeployment with the same name exists. Migration is in progress."
```

**Migration complete** — ownership transfer is done (the controller labels the `TemporalWorkerDeployment` with `temporal.io/migrated-to-wd: "true"` once all child resources have been re-owned):
```
Ready=False reason=MigratedToWorkerDeployment
message: "Migration complete. Delete this TemporalWorkerDeployment."
```

For `TemporalConnection`, the same `Deprecated` → `MigratedToConnection` pattern applies (there is no ownership transfer step, so there is no intermediate state).

## Migration steps

The order below matters for zero downtime. The key principle is that new `WorkerDeployment` and `Connection` resources must exist on the cluster **before** upgrading the controller. When the controller starts, it immediately picks up the new resources and transfers ownership of running `Deployments` from the old resources to the new ones so that workers are never unmanaged.

### Step 1: Upgrade the CRDs chart

```bash
helm upgrade temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <new-version> \
  --namespace temporal-system
```

This installs the new `WorkerDeployment` and `Connection` CRDs and marks the old `TemporalWorkerDeployment` and `TemporalConnection` CRDs as deprecated. Existing resources and the running v1.6 controller are unaffected — the deprecation only blocks creating new resources of those kinds. Updates and deletes of existing resources continue to work normally.

### Step 2: Create Connection resources

For each `TemporalConnection` in your cluster, create a `Connection` with the same name, namespace, and spec:

```yaml
apiVersion: temporal.io/v1alpha1
kind: Connection
metadata:
  name: production-temporal      # same name
  namespace: my-namespace        # same namespace
spec:
  hostPort: "production.abc123.tmprl.cloud:7233"
  apiKeySecretRef:
    name: temporal-api-key
    key: api-key
```

The old `TemporalConnection` and new `Connection` coexist safely at this point. Do not delete the old one yet, the running controller still references it.

### Step 3: Create WorkerDeployment resources

For each `TemporalWorkerDeployment`, create a `WorkerDeployment` with the same name, namespace, and spec:

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerDeployment
metadata:
  name: my-worker                # same name as the TemporalWorkerDeployment
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

> **The name must match exactly.** The controller derives the Temporal Worker Deployment name from `{namespace}/{name}`. A different name creates a new, distinct Temporal Worker Deployment.

The v1.6 controller ignores these new resources. No pods are started or stopped at this point.

### Step 4: Upgrade the controller

```bash
helm upgrade temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version <new-version> \
  --namespace temporal-system
```

When the v1.7 controller starts, it:

1. Finds each `WorkerDeployment` and the `TemporalWorkerDeployment` with the same name
2. Transfers ownership of the existing running Kubernetes `Deployment` resources from the old owner to the new one (no pods are restarted)
3. Begins managing the `WorkerDeployment` normally
4. Sets migration conditions on the `TemporalWorkerDeployment` (see [What happens to existing resources?](#what-happens-to-existing-resources) above)

Workers are continuously managed throughout.

### Step 5: Verify and delete old resources

Wait for each `WorkerDeployment` to be ready:

```bash
kubectl wait workerdeployment/my-worker \
  --for=condition=Ready \
  --timeout=5m \
  --namespace my-namespace
```

Confirm the old `TemporalWorkerDeployment` shows `MigratedToWorkerDeployment`:

```bash
kubectl get temporalworkerdeployment my-worker -n my-namespace -o jsonpath='{.status.conditions}'
```

Then delete the old resources:

```bash
kubectl delete temporalworkerdeployment my-worker -n my-namespace
kubectl delete temporalconnection production-temporal -n my-namespace
```

### Step 6: Update WorkerResourceTemplate references (if applicable)

If you use `WorkerResourceTemplate`, update `spec.temporalWorkerDeploymentRef` to `spec.workerDeploymentRef`. Both fields are accepted in v1.7 and can be updated at any point after Step 4 without affecting running workers.
`spec.temporalWorkerDeploymentRef` is deprecated and will be removed in a future release.

```yaml
spec:
  workerDeploymentRef:       # was: temporalWorkerDeploymentRef
    name: my-worker
```

### Step 7: Update manifests and tooling

Update any manifests, Helm charts, GitOps configs, or scripts that reference the old CRD kinds or field names.