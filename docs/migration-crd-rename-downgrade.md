# Downgrade from CRD Rename Guide

Starting with Chart Version v0.26.0 (App Version v1.7.0), the Temporal Worker Controller renames its two primary CRDs and one field reference:

| Old name | New name |
|---|---|
| `TemporalWorkerDeployment` | `WorkerDeployment` |
| `TemporalConnection` | `Connection` |
| `WorkerResourceTemplate.spec.temporalWorkerDeploymentRef` | `WorkerResourceTemplate.spec.workerDeploymentRef` |

The upgrade path is straightforward. See the [upgrade guide](migration-crd-rename.md) for more details.

## Downgrading from v1.7 to v1.6

There are some important things to consider if you want to roll back
(downgrade) the installed version of Temporal Worker Controller after upgrading to v1.7.0.

> **Warning**: You **should not perform a rollback/downgrade of the Temporal
> Worker Controller CRDs Helm Chart**. Doing so is a potentially
> **destructive** operation that can cause your Temporal Worker Deployments to
> be deleted.
> 
> See [here][crd-pruning] for more details.

[crd-pruning]: https://github.com/temporalio/temporal-worker-controller/blob/main/docs/crd-management.md#crd-rollback-and-field-pruning

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

---
If you upgraded the Temporal Worker Controller to v1.7 -- i.e. you successfully
completed Step 2 above -- but **did not** complete Step 3 (migrating your
resources), execute the following `kubectl` command to remove the CRD rename
validation guard on the old `TemporalWorkerDeployment` and `TemporalConnection`
Custom Resource Definitions:

```bash
kubectl patch crd temporalworkerdeployments.temporal.io --type='json' -p='[{"op": "remove", "path": "/spec/versions/0/schema/openAPIV3Schema/properties/spec/x-kubernetes-validations"}]'
kubectl patch crd temporalconnections.temporal.io --type='json' -p='[{"op": "remove", "path": "/spec/versions/0/schema/openAPIV3Schema/properties/spec/x-kubernetes-validations"}]'
```
You will also need to manually remove the `migration-guard` finalizer that was added 
to your `TemporalWorkerDeployment` and `TemporalConnection` resources by the 1.7 controller:


Get a list of all the original `TemporalWorkerDeployment` object names and UIDs:

```bash
kubectl get -n <NAMESPACE> temporalworkerdeployments.temporal.io -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.uid}{"\n"}{end}'
```

For each of the `TemporalWorkerDeployments` listed above:

```bash
kubectl patch -n <NAMESPACE> temporalworkerdeployments/<TWD_NAME> --type=merge -p='{"metadata":{"finalizers":["temporal.io/delete-protection"]}}'
```

Get a list of all the original `TemporalConnection` object names and UIDs:

```bash
kubectl get -n <NAMESPACE> temporalworkerdeployments.temporal.io -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.uid}{"\n"}{end}'
```

For each of the `TemporalConnections` listed above:

```bash
kubectl patch -n <NAMESPACE>  temporalconnections/<TC_NAME> --type=merge -p='{"metadata":{"finalizers":[]}}'
```

---

If you upgraded the Temporal Worker Controller to v1.7 and completed Step 3
above (i.e. you successfully migrated your resources), you will need to
manually restore the OwnerReferences for your Kubernetes Deployments to point
at the original `TemporalWorkerDeployment` resources.

To do so, first, get a list of all the original `TemporalWorkerDeployment`
object names and UIDs:

```bash
kubectl get -n <NAMESPACE> temporalworkerdeployments.temporal.io -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.uid}{"\n"}{end}'
```

Then get a list of all the Kubernetes `Deployments` that are now owned by the new
`WorkerDeployment` resources:

```bash
kubectl get deployments -n <NAMESPACE> -o json | jq -r '
    .items[] | select(
      .metadata.ownerReferences // [] | any(.kind == "WorkerDeployment")
    ) | .metadata.name
  '
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
        "apiVersion": "temporal.io/v1alpha1",
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
earlier. It's important that the UID string is correct, because if Kubernetes GC
does not recognize the UID, it will treat those `Deployments` as
orphaned and delete them.

Confirm that your `Deployments` are now owned by the original `TemporalWorkerDeployment` resources:
```bash
kubectl get deployments -n <NAMESPACE> -o json | jq -r '
    .items[] | select(
      .metadata.ownerReferences // [] | any(.kind == "TemporalWorkerDeployment")
    ) | .metadata.name
  '
```

If you completed Step 4 above and modified `WorkerResourceTemplate` resources,
you will also need to reset the `OwnerReferences` for those resources as well.

```bash
kubectl get workerresourcetemplates -n <NAMESPACE> -o json | jq -r '
    .items[] | select(
      .metadata.ownerReferences // [] | any(.kind == "WorkerDeployment")
    ) | .metadata.name
  '
```

Then, for each of the `WorkerResourceTemplate` resources listed above, execute
the following `kubectl` command to reset the OwnerReferences of Kubernetes
Deployments back to the original `TemporalWorkerDeployment` custom resources:

```bash
kubectl patch -n <NAMESPACE> wrt <WRT_NAME> --type='merge' -p '
{
  "metadata": {
    "ownerReferences": [
      {
        "apiVersion": "temporal.io/v1alpha1",
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

Again, replace `<TWD_NAME>` and `<TWD_UID>` with the correct
`TemporalWorkerDeployment` custom resource's name and UID you printed out
earlier. It's important that the UID string is correct, because if Kubernetes GC 
does not recognize the UID, it will treat those `WorkerResourceTemplates` as 
orphaned and delete them.

Confirm that your `WorkerResourceTemplates` are now owned by the original `TemporalWorkerDeployment` resources:
```bash
kubectl get workerresourcetemplates -n <NAMESPACE> -o json | jq -r '
    .items[] | select(
      .metadata.ownerReferences // [] | any(.kind == "TemporalWorkerDeployment")
    ) | .metadata.name
  '
```

Now you can safely delete the `WorkerDeployment` and `Connection` resources without 
deleting any `Deployments` or `WorkerResourceTemplates`. Before deleting the `WorkerDeployment` 
resource, you will need to remove the `deletion-protection` finalizer that the v1.7 controller
added to it:

```bash
kubectl patch -n <NAMESPACE> workerdeployments/<WD_NAME> --type=merge -p='{"metadata":{"finalizers":[]}}'
```

You'll notice that because you did not roll back the CRD chart, there is still a 
deprecation warning on the `TemporalWorkerDeployment` and `TemporalConnection` resource. 
This can be safely ignored. If you have already safely migrated ownership away from all
`WorkerDeployment` resources, you could also roll back the CRD chart to v0.25.0. Rolling
the CRDs back earlier is very risky, because any `Deployments` and `WorkerResourceTemplates`
owned by the `WorkerDeployment` resources will be deleted when the `WorkerDeployment` resources
are deleted, and rolling back the CRDs will delete all `WorkerDeployment` and `Connection` instances.

To recap, here is how to confirm that no `WorkerDeployment` owns any `Deployments` or `WorkerResourceTemplates` in any namespace:
```bash
kubectl get deployments -A -o json | jq -r '
    .items[] | select(
      .metadata.ownerReferences // [] | any(.kind == "WorkerDeployment")
    ) | .metadata.name
  '
kubectl get workerresourcetemplates -A -o json | jq -r '
    .items[] | select(
      .metadata.ownerReferences // [] | any(.kind == "WorkerDeployment")
    ) | .metadata.name
  '
```

and here is how to confirm you no longer have any `WorkerDeployment` or `Connection` in any namespace:
```bash
kubectl get workerdeployments -A
kubectl get connections -A
```