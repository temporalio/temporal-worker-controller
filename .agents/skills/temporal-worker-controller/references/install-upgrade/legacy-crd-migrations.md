# Legacy CRD migrations

Use only when the installed release predates the separate CRDs chart or the resource rename. Inspect installed chart version, CRDs, and objects before selecting a path. These are one-time transitions, not steps for a normal upgrade.

## Controller chart v0.12.0 or earlier: adopt CRDs into a separate chart

The old controller chart placed CRDs in `crds/`. Helm leaves them on the cluster during upgrade but does not track or upgrade them afterward. The old names in this transition are `temporalconnections.temporal.io` and `temporalworkerdeployments.temporal.io`.

1. Upgrade the controller chart to the intended version; the existing CRDs remain installed.
2. Check that those two CRDs exist and have no conflicting Helm ownership. Stamp `app.kubernetes.io/managed-by=Helm` and `meta.helm.sh/release-name=<crds-release>` plus `meta.helm.sh/release-namespace=<namespace>` on each CRD.
3. Install the separate `oci://docker.io/temporalio/temporal-worker-controller-crds` chart at the same target chart version and namespace to adopt them. Check the installed release and CRD schemas. Do not remove the CRDs: deleting them deletes their custom resources.

For example, after replacing the release and namespace:

```bash
kubectl label crd temporalconnections.temporal.io temporalworkerdeployments.temporal.io \
  app.kubernetes.io/managed-by=Helm --overwrite
kubectl annotate crd temporalconnections.temporal.io temporalworkerdeployments.temporal.io \
  meta.helm.sh/release-name=temporal-worker-controller-crds \
  meta.helm.sh/release-namespace=temporal-system --overwrite
helm install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <target-version> --namespace temporal-system
```

## Crossing chart v0.26.0 / app v1.7.0: renamed resources

`TemporalWorkerDeployment` became `WorkerDeployment`, `TemporalConnection` became `Connection`, and `WorkerResourceTemplate.spec.temporalWorkerDeploymentRef` became `spec.workerDeploymentRef`. New objects of the deprecated kinds cannot be created on v1.7, and existing old-kind objects stop being actively reconciled.
1. Upgrade the CRDs chart to v0.26.0, then the controller chart to v0.26.0. Confirm the controller is healthy.
2. Replace old kinds in manifests with the new kinds, keeping names, namespaces, and specs. Apply through the user's existing Helm, GitOps, or `kubectl` workflow. A migration guard finalizer holds old resources until matching new resources exist and ownership transfer completes. Wait for each new `WorkerDeployment` to become Ready before removing old objects.
3. Update WorkerResourceTemplate references to `spec.workerDeploymentRef`. Update scripts, alerts, and automation that use old kinds.
4. Verify new `WorkerDeployment` and `Connection` objects, child workload ownership, and removal of old objects. Do not force-remove the migration guard finalizer: it protects ownership transfer.

If downgrading from app v1.7.0 to v1.6 after this migration, inspect the live old/new objects and obtain the version-specific downgrade procedure before changing the controller; a normal Helm rollback alone does not reverse the resource rename.
