# Upgrade and roll back TWC

Use this for upgrades and rollbacks of releases that already use the separate CRDs chart. Check version-specific transitions below before following the normal path. If upgrading from controller chart v0.12.0 or earlier, or crossing the CRD rename at v0.26.0, read [legacy CRD migrations](legacy-crd-migrations.md) first.

## Preflight

Run `helm list -A` to identify both releases, namespaces, and current chart versions. Save each release's values and manifest for recovery:

```bash
helm get values <release-name> -n <namespace> -o yaml > <release-name>-values.yaml
helm get manifest <release-name> -n <namespace> > <release-name>-manifest.yaml
```

Compare installed and target chart versions and values. Check whether the change crosses a transition below. If the controller release has `certmanager.install: true` and the target is chart v0.30.0 or newer, use [subchart migration](cert-manager-subchart-migration.md) for the upgrade across v0.30.0. That procedure includes the controller upgrade; continue the normal path only for later versions. Confirm that webhook TLS will remain available throughout.

For a jump that skips chart minor versions (for example, v0.29.0 to v0.31.0), stage through published intermediate versions. First complete [legacy CRD adoption](legacy-crd-migrations.md) if the CRDs still come from an old controller chart's `crds/` directory; that path stages at v0.22.0. After adoption, at each version upgrade CRDs first, then the controller, and verify the result. The documented guarantee covers CRDs at version N with controller N or N−1; jumping CRDs directly to v0.31.0 while the controller is still at v0.29.0 falls outside it. If an intermediate chart is unavailable, use a wider jump only when release guidance covers that exact pairing or it has been tested on a representative non-production cluster. Complete each version-specific migration before moving past it.

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

Before choosing a rollback command, inspect `helm history <controller-release> -n <namespace>` and `helm history <crds-release> -n <namespace>`. Identify the controller revision to restore, both chart versions that would result, the older controller's TLS requirements, and any version-specific transition the rollback would cross. A Helm rollback can restore only a revision in that release's history.

CRD chart version N is documented as compatible with controller versions N and N−1. Keep the newer CRDs and roll back only the controller when the resulting pair is within that range. For example, v0.31.0 CRDs with a v0.29.0 controller are outside the guarantee. Do not issue that rollback or assume a v0.30.0 intermediate revision exists; inspect history and establish a supported path first. If no supported path is established, stop and ask for guidance before changing the cluster.

Check migration state before restoring an older revision. A rollback below chart v0.30.0 after migrating the cert-manager subchart can reintroduce `certmanager.install: true`; this skill does not provide a reversal procedure, so stop rather than run a plain rollback. For a rollback across the v0.26.0 CRD rename, follow [legacy CRD migrations](legacy-crd-migrations.md) and its version-specific downgrade procedure; do not use the normal path.

When these checks establish a supported controller rollback, run `helm rollback <controller-release> <revision> -n <namespace>`, then verify the controller and webhook TLS. Roll back the CRDs chart only if there is a specific need and a safe version sequence. First compare old and new schemas and check whether existing objects use fields absent from the older schema: Kubernetes prunes those fields on the object's next write, causing permanent data loss. Never use the normal CRD rollback path across the v0.26.0 rename.

## Version-specific transitions

- From app v1.4.0 to v1.5.2 or later: the webhook Secret mount became unconditional. With `certmanager.enabled: false`, create the Secret before upgrade or the controller Pod can remain in `ContainerCreating`.
- App v1.5.2 through v1.7.0: `webhook.enabled: true` installed the optional WorkerDeployment webhook Service but did not wire its handler. The handler works from v1.8.0 onward.
- At controller chart v0.26.0 (app v1.7.0), `TemporalWorkerDeployment` and `TemporalConnection` were renamed. Read [legacy CRD migrations](legacy-crd-migrations.md) when crossing this transition.
- At controller chart v0.30.0 (app v1.11.0), the cert-manager subchart was removed. A release with `certmanager.install: true` needs [subchart migration](cert-manager-subchart-migration.md).

## Common issues

| Symptom | Likely cause | Check or action |
| --- | --- | --- |
| Controller Pod stuck in `ContainerCreating` with `secret "webhook-server-cert" not found` | The always-on webhook needs a Secret; cert-manager is absent or a user-provided Secret was not created | Check `webhook.certSecretName`, Secret namespace, and certificate source. Install cert-manager or provide the Secret, then verify the existing controller release; do not run a second `helm install` over it. |
| WorkerResourceTemplate create, update, or delete fails with a webhook error | The webhook uses `failurePolicy: Fail`; the controller is unavailable or TLS is invalid | Check controller Pod readiness, serving Secret, certificate validity, and validating webhook CA bundle. |
| Independent cert-manager install reports CRD `invalid ownership metadata` | CRDs are still owned by the old TWC subchart | Use [subchart migration](cert-manager-subchart-migration.md); transfer ownership before installing. |
| Certificate stops renewing after migration | The handoff or cert-manager reconciliation may be incomplete | Check Certificate and Issuer status and events, cert-manager logs, and whether the ownership transfer in [subchart migration](cert-manager-subchart-migration.md) was completed. |
