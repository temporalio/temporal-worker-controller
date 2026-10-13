# Upgrade and roll back TWC

Use this for upgrades and rollbacks of releases that already use the separate CRDs chart. Check version-specific transitions below before following the normal path. If upgrading from controller chart v0.12.0 or earlier, or crossing the CRD rename at v0.26.0, read [legacy CRD migrations](legacy-crd-migrations.md) first.

## Preflight

Run `helm list -A` to identify both releases, namespaces, and current chart versions. Save each release's values and manifest for recovery:

```bash
helm get values <release-name> -n <namespace> -o yaml > <release-name>-values.yaml
helm get manifest <release-name> -n <namespace> > <release-name>-manifest.yaml
```

Compare installed and target chart versions and values. Check whether the change crosses a transition below. If the controller release has `certmanager.install: true` and the target is chart v0.30.0 or newer, follow [subchart migration](cert-manager-subchart-migration.md) before the normal upgrade. Confirm that webhook TLS will remain available throughout.

## Upgrade

For the normal two-chart path, upgrade the CRDs chart first, then the controller chart to the **same** target version. Replace release names and namespace with the installed ones:

```bash
helm upgrade temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <target-version> \
  --namespace temporal-system

helm upgrade temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version <target-version> \
  --namespace temporal-system
```

With no values flags, Helm carries forward the release's user-supplied values and merges them with the target chart defaults. If adding `-f` or `--set`, review the saved user-supplied values against target defaults and removed keys, then pass the intended overrides explicitly; those flags change Helm's default value-merging behavior. Avoid blindly using `--reuse-values`, which can retain old chart defaults. Include the required webhook TLS settings for the target version; use [Webhook TLS](webhook-tls.md) when the certificate source changes.

## Verify

Read back deployed chart and image versions, controller Pod readiness, the webhook TLS Secret and CA bundle, and Certificate readiness when cert-manager is used. Investigate a failed check before proceeding.

## Roll back

Roll back the controller first: `helm rollback <controller-release> -n <namespace>`. Prefer keeping the newer CRDs when the rollback stays within the documented compatibility range: CRD chart version N is compatible with controller versions N and N−1. A rollback from controller chart v0.31.0 to v0.29.0 while retaining v0.31.0 CRDs falls outside that guarantee; plan a supported intermediate version instead of assuming it is safe. Before rolling back CRDs, compare old and new schemas and check whether any existing objects use fields absent from the older schema. Kubernetes prunes such fields on the object's next write, causing permanent data loss. Roll back the CRDs chart only when needed and after that check. Check the older controller's TLS requirements before changing versions.

## Version-specific transitions

- From app v1.4.0 to v1.5.2 or later: the webhook Secret mount became unconditional. With `certmanager.enabled: false`, create the Secret before upgrade or the controller Pod can remain in `ContainerCreating`.
- App v1.5.2 through v1.7.0: `webhook.enabled: true` installed the optional WorkerDeployment webhook Service but did not wire its handler. The handler works from v1.8.0 onward.
- At controller chart v0.26.0 (app v1.7.0), `TemporalWorkerDeployment` and `TemporalConnection` were renamed. Read [legacy CRD migrations](legacy-crd-migrations.md) when crossing this transition.
- At controller chart v0.30.0 (app v1.11.0), the cert-manager subchart was removed. A release with `certmanager.install: true` needs [subchart migration](cert-manager-subchart-migration.md).

## Common issues

| Symptom | Likely cause | Check or action |
| --- | --- | --- |
| Controller Pod stuck in `ContainerCreating` with `secret "webhook-server-cert" not found` | The always-on webhook needs a Secret; cert-manager is absent or a user-provided Secret was not created | Check `webhook.certSecretName`, Secret namespace, and certificate source. Install cert-manager or provide the Secret before retrying. |
| WorkerResourceTemplate create, update, or delete fails with a webhook error | The webhook uses `failurePolicy: Fail`; the controller is unavailable or TLS is invalid | Check controller Pod readiness, serving Secret, certificate validity, and validating webhook CA bundle. |
| Independent cert-manager install reports CRD `invalid ownership metadata` | CRDs are still owned by the old TWC subchart | Use [subchart migration](cert-manager-subchart-migration.md); transfer ownership before installing. |
| Certificate stops renewing after migration | Certificate or Issuer still has old Helm ownership | Check ownership annotations and complete the Certificate/Issuer transfer in [subchart migration](cert-manager-subchart-migration.md). |
