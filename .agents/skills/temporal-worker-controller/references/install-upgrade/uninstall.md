# Uninstall TWC

Use this only when the user wants to remove an installation. Uninstalling the controller or CRDs can affect WorkerDeployments and their workloads.

## Inventory and decide what to remove

Identify the controller and CRDs Helm releases and their namespaces with `helm list -A`. Check which TWC CRDs exist with `kubectl get crd`. Inventory instances of each installed kind across all namespaces; include cluster-scoped ClusterConnections if that CRD exists. For example:

```bash
kubectl get workerdeployments,workerresourcetemplates,connections -A
kubectl get clusterconnections
```

Also list deprecated TemporalWorkerDeployments and TemporalConnections where those CRDs exist. Identify resources managed by GitOps or other tools and remove them through that source so they are not recreated. Decide whether the CRDs should be deleted or retained. Independently installed cert-manager may serve other workloads; leave it in place unless its removal is a separate, explicit task.

## Remove resources while the controller is running

Delete WorkerDeployments and deprecated TemporalWorkerDeployments first. Wait for their deletion to finish before stopping the controller: the controller handles their finalizers and cleans up Temporal server-side deployment state. Then remove WorkerResourceTemplates, Connections, deprecated TemporalConnections, and ClusterConnections. Verify that no TWC custom resources remain. If deprecated resources are held by a migration guard, follow the applicable [legacy CRD migration](legacy-crd-migrations.md); do not force-remove finalizers as a routine uninstall step.

## Uninstall the Helm releases

After custom resources are gone, uninstall the controller release with `helm uninstall <controller-release> -n <namespace>`. Verify its Deployment and webhook configurations are gone. Before uninstalling the CRDs release, show the user the final cluster-wide custom-resource inventory and state that deleting a CRD deletes every custom resource of that type. Get explicit approval for CRD removal; approval to uninstall the controller alone does not include it. Only then run `helm uninstall <crds-release> -n <namespace>`. Verify the releases and, when removed, the CRDs are absent.
