# CRD Rename Migration Guide

Starting with Chart Version v0.26.0 (App Version v1.7.0), the Temporal Worker Controller renames its two primary CRDs and one field reference:

| Old name | New name |
|---|---|
| `TemporalWorkerDeployment` | `WorkerDeployment` |
| `TemporalConnection` | `Connection` |
| `WorkerResourceTemplate.spec.temporalWorkerDeploymentRef` | `WorkerResourceTemplate.spec.workerDeploymentRef` |

In App Version v1.7, the old CRD kinds are **not actively managed**: new objects of these kinds cannot be created, and existing objects will never become `Ready` or reconcile with Temporal Server state. The deprecated CRDs exist only to support migration of resources already in your cluster.

## Why the rename?

The `Temporal` prefix was redundant — all resources in the `temporal.io` API group are already scoped to Temporal. The shorter names are consistent with Kubernetes naming conventions and reduce verbosity in manifests and CLI commands.
After this release, the Worker Controller will be Generally Available (GA), which means no more breaking changes will be introduced. We wanted to make this transition before GA, and make it as clean as possible.

## Migration steps

> **Dev / non-production environments:** If you don't need to preserve any worker state, the simplest path is to delete all your `TemporalWorkerDeployment` and `TemporalConnection` resources while the v1.6 controller is still running. At that point no migration-guard finalizer has been added yet, so deletion completes after the v1.6 finalizer completes. Note that all related Worker Deployment state in the Temporal server will also be deleted. Then upgrade the controller and create fresh `WorkerDeployment` and `Connection` resources.

### Step 1: Upgrade the CRDs chart

```bash
helm upgrade temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version 0.26.0 \
  --namespace temporal-system
```

This installs the new `WorkerDeployment` and `Connection` CRDs and marks the old `TemporalWorkerDeployment` and `TemporalConnection` CRDs as deprecated. Existing resources and the running v1.6 controller are unaffected — the deprecation only blocks creating new resources of those kinds. Updates and deletes of existing resources continue to work normally.

### Step 2: Upgrade the controller

```bash
helm upgrade temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version 0.26.0 \
  --namespace temporal-system
```

When the v1.7 controller starts, it adds the `temporal.io/migration-guard` finalizer to all existing `TemporalWorkerDeployment` and `TemporalConnection` resources. See [Deletion protection](#deletion-protection) for what this means.

### Step 3: Migrate your resources

In your manifests (Helm values, GitOps configs, or raw YAML), replace the deprecated kind names:

- `kind: TemporalWorkerDeployment` → `kind: WorkerDeployment`
- `kind: TemporalConnection` → `kind: Connection`

No other spec changes are needed — the fields are identical.

> **The name must match exactly.** The controller derives the Temporal Worker Deployment name from `{namespace}/{name}`. A different name creates a new, distinct Temporal Worker Deployment.

Then apply your updated manifests (via `helm upgrade`, `kubectl apply`, or your GitOps toolchain). This simultaneously creates the new resources and marks the old ones for deletion. The migration-guard finalizer holds each deprecated resource in `Terminating` state until the controller confirms ownership transfer is complete, then removes the finalizer and lets the deletion finish. Workers are continuously managed throughout.

Wait for each `WorkerDeployment` to be ready:

```bash
kubectl wait workerdeployment/my-worker \
  --for=condition=Ready \
  --timeout=5m \
  --namespace my-namespace
```

> **Side-by-side alternative:** If you prefer to keep the old resources running while you create the new ones and verify them first, that is also safe. Create the new `Connection` and `WorkerDeployment` resources, wait for them to be ready, then delete the deprecated resources once you are satisfied.

### Step 4: Update WorkerResourceTemplate references (if applicable)

If you use `WorkerResourceTemplate`, update `spec.temporalWorkerDeploymentRef` to `spec.workerDeploymentRef`. Both fields are accepted in v1.7 and can be updated at any point after Step 2 without affecting running workers.
`spec.temporalWorkerDeploymentRef` is deprecated and will be removed in a future release.

```yaml
spec:
  workerDeploymentRef:       # was: temporalWorkerDeploymentRef
    name: my-worker
```

### Step 5: Update tooling and scripts

Update any scripts, CI/CD pipelines, runbooks, monitoring alerts, or other tooling that references the old CRD kind names (`TemporalWorkerDeployment`, `TemporalConnection`) or their short names (`twd`, `tconn`).

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

## Deletion protection

After upgrading to v1.7, the controller adds a `temporal.io/migration-guard` finalizer to every `TemporalWorkerDeployment` and `TemporalConnection`. This finalizer prevents the resource from being fully deleted until migration is confirmed:

- A `TemporalWorkerDeployment` can only be deleted once the controller has labeled it `temporal.io/migrated-to-wd: "true"` (set automatically after ownership transfer completes).
- A `TemporalConnection` can only be deleted once a `Connection` with the same name exists.

If you delete a deprecated resource before creating its replacement, the resource will enter `Terminating` state and remain there until you create the new resource. Once the matching `WorkerDeployment` or `Connection` is created and migration is confirmed, the finalizer is removed and deletion completes automatically. The condition during this wait will be:

```
Ready=False reason=DeletingPendingMigration
message: "This TemporalWorkerDeployment is marked for deletion. Create a WorkerDeployment with the same name and spec to complete migration; deletion will proceed automatically once migration is confirmed."
```
