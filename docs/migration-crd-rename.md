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

## Downgrading from v1.7 to v1.6

There are some important things to consider if you want to roll back
(downgrade) an installed version of Temporal Worker Controller.

> **Warning**: You **should not perform a rollback/downgrade of the Temporal
> Worker Controller CRDs Helm Chart**. Doing so is a potentially
> **destructive** operation that can cause your Temporal Worker Deployments to
> be deleted.

To downgrade the Temporal Worker Controller itself, do:

```bash
helm rollback <RELEASE_NAME> <REVISION_NUMBER>
```

Where `<RELEASE_NAME>` is the Helm Release associated with the Temporal Worker
Controller Helm Chart (**not** the CRDs Chart) and `<REVISION_NUMBER>` is the
Helm release revision number to roll back to. You can get this information by
doing:

```bash
helm history -n <TWC_NAMESPACE> <TWC_RELEASE_NAME>
```

Where `<TWC_NAMESPACE>` is the Kubernetes Namespace you installed Temporal
Worker Controller in and `<TWC_RELEASE>` is the name of the Helm Release
associated with the Temporal Worker Controller Helm Chart.

Once you have downgraded the Temporal Worker Controller, you will need to take
some corrective actions depending on how far down the migration path you went
when upgrading to the v1.7 Temporal Worker Controller release.

If you upgraded the Temporal Worker Controller to v1.7 -- i.e. you successfully
completed Step 2 above -- but **did not** complete Step 3 (migrating your
resources), execute the following `kubectl` command to remove the CRD rename
validation guard on the old `TemporalWorkerDeployment` Custom Resource
Definition:

```bash
kubectl patch crd temporalworkerdeployments.temporal.io --type='json' -p='[{"op": "remove", "path": "/spec/versions/0/schema/openAPIV3Schema/properties/spec/x-kubernetes-validations"}]'
```

If you upgraded the Temporal Worker Controller to v1.7 and completed Step 3
above (i.e. you successfully migrated your resources), you will need to
manually restore the OwnerReferences for your Kubernetes Deployments to point
at the original `TemporalWorkerDeployment` resources.

To do so, first, get a list of all the original `TemporalWorkerDeployment`
object names and UIDs:

```bash
kubectl get -n <NAMESPACE> temporalworkerdeployments.temporal.io -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.uid}{"\n"}{end}'
```

Then get a list of all the Kubernetes Deployments that are now owned by the new
`WorkerDeployment` resources:

```bash
kubectl get -n <NAMESPACE> deployments -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.ownerReferences[0].kind}{"/"}{.metadata.ownerReferences[0].name}{"\n"}{end}'
```

Then, for each of the Kubernetes Deployments listed above, execute the
following `kubectl` command to reset the OwnerReferences of Kubernetes
Deployments back to the original `TemporalWorkerDeployment` custom resources:

```bash
kubectl patch -n <NAMESPACE> deployment <DEPLOYMENT_NAME> --type='merge' -p '
{
  "metadata": {
    "ownerReferences": [
      {
        "apiVersion": "v1alpha1",
        "kind": "TemporalWorkerDeployment",
        "name": "<TWD_NAME>",
        "uid": "<TWD_UID>",
        "controller": true,
        "blockOwnerDeletion": true
      }
    ]
  }
}'
```

Replace `<TWD_NAME>` and `<TWD_UID>` with the correct
`TemporalWorkerDeployment` custom resource's name and UID you printed out
earlier.
