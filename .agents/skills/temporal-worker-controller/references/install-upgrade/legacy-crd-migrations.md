# Legacy CRD migrations

Use only when the installed release predates the separate CRDs chart or the resource rename. Inspect installed chart version, CRDs, and objects before selecting a path. These are one-time transitions, not steps for a normal upgrade.

## Adopt CRDs from the old controller chart

Controller chart v0.12.0 and earlier installed `temporalconnections.temporal.io` and `temporalworkerdeployments.temporal.io` from `crds/`. Helm leaves them in place during upgrades but does not update them. Use this path when those CRDs exist without a separate CRDs release; check their live ownership rather than relying only on the controller version.

For a v0.12.0-or-earlier installation, stage the migration at chart v0.22.0, the first published version with matching controller and CRDs charts. Do not upgrade the controller straight to a later target while its old CRDs remain. The v0.22.0 CRD schemas retain both old kinds and add optional status fields compared with v0.12.0. This handoff briefly pairs v0.22.0 CRDs with an older controller, outside the documented N/N−1 compatibility commitment; test it on a representative nonproduction cluster before applying it. If the installed CRDs differ from v0.12.0, compare them with the v0.22.0 chart before adopting them.

1. Record the controller chart version and values. Check that both old CRDs exist, inspect their Helm ownership metadata, and list their custom resources. Do not delete either CRD.
2. Stamp `app.kubernetes.io/managed-by=Helm` and `meta.helm.sh/release-name=<crds-release>` plus `meta.helm.sh/release-namespace=<namespace>` on both CRDs. Install the v0.22.0 CRDs chart to adopt and update them. Verify the CRDs are Established and the existing custom resources remain accessible.
3. Upgrade the controller chart to v0.22.0 with the reviewed user values. Verify controller readiness and existing custom resources. Only then follow [upgrade](upgrade.md) through later published versions, CRDs first and controller second at each step; handle the v0.26.0 rename separately.

For the default release names and namespace, the adoption commands are:

```bash
kubectl label crd temporalconnections.temporal.io temporalworkerdeployments.temporal.io \
  app.kubernetes.io/managed-by=Helm --overwrite
kubectl annotate crd temporalconnections.temporal.io temporalworkerdeployments.temporal.io \
  meta.helm.sh/release-name=temporal-worker-controller-crds \
  meta.helm.sh/release-namespace=temporal-system --overwrite
helm install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version 0.22.0 --namespace temporal-system
helm upgrade temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version 0.22.0 --namespace temporal-system
```

## Crossing chart v0.26.0 / app v1.7.0: renamed resources

`TemporalWorkerDeployment` became `WorkerDeployment`, `TemporalConnection` became `Connection`, and `WorkerResourceTemplate.spec.temporalWorkerDeploymentRef` became `spec.workerDeploymentRef`. New objects of the deprecated kinds cannot be created on v1.7, and existing old-kind objects stop being actively reconciled.
1. Upgrade the CRDs chart to v0.26.0, then the controller chart to v0.26.0. Confirm the controller is healthy.
2. Replace old kinds in manifests with the new kinds, keeping names, namespaces, and specs. Apply through the user's existing Helm, GitOps, or `kubectl` workflow. A migration guard finalizer holds old resources until matching new resources exist and ownership transfer completes. Wait for each new `WorkerDeployment` to become Ready. If the user's tool does not delete the deprecated objects when manifests change (plain `kubectl apply` does not), delete those old objects after the matching new objects are ready and ownership transfer is complete; let the controller remove the finalizer.
3. Update WorkerResourceTemplate references to `spec.workerDeploymentRef`. Update scripts, alerts, and automation that use old kinds.
4. Verify new `WorkerDeployment` and `Connection` objects, child workload ownership, and removal of old objects. Do not force-remove the migration guard finalizer: it protects ownership transfer.

If downgrading from app v1.7.0 to v1.6 after this migration, inspect the live old/new objects and obtain the version-specific downgrade procedure before changing the controller; a normal Helm rollback alone does not reverse the resource rename.
