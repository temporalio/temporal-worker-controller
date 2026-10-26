# Install and upgrade Temporal Worker Controller

Use this reference for TWC installation, upgrades, rollback, uninstallation, CRD migrations, cert-manager, and webhook TLS. Read the sections selected by `SKILL.md` and any related sections they link to.

## Check the current installation

For a request involving an existing cluster, use read-only checks before choosing a procedure:

1. Find the TWC and CRDs chart releases, namespaces, and versions with `helm list -A`.
2. Read the controller release's values with `helm get values <release> -n <namespace> --all` to check `certmanager.install`. Check its manifest if the values do not show whether the release owns cert-manager.
3. Check which `temporal.io` CRDs exist. Query old Kind resources only if their CRDs exist.

If cluster access is unavailable, ask for the relevant results. If the user requests a plan only, do not run cluster commands. For a fresh install, first check whether a TWC release or CRDs already exist, then follow [Install](#install).

## Before changing a cluster

Check the installed releases and affected resources first. Show the user the proposed commands, the resources they will change or delete, and how you will verify the result. Get explicit approval for that plan before running `helm install`, `helm upgrade`, `helm rollback`, `helm uninstall`, or mutating `kubectl` commands, unless the user has already approved those specific changes. Read-only checks do not need approval.

## Version scope

This reference can be used without a TWC repository checkout. The current-chart guidance was checked against chart v0.31.0; for a newer target, read its published chart values and templates before using version-specific instructions. Do not assume that example release names or namespaces match the user's cluster.

## Concepts

### Two Helm charts

`temporal-worker-controller-crds` owns the CustomResourceDefinitions. `temporal-worker-controller` owns the controller Deployment, RBAC, Services, and admission webhooks. The charts have matching version numbers. Install or upgrade the CRDs chart first, then the controller chart at the same target version. Helm does not upgrade CRDs placed in a chart's `crds/` directory during `helm upgrade`; the separate chart gives CRDs an explicit upgrade path. Uninstalling the CRDs chart deletes custom resources of those types.

CRD chart version N is compatible with controller chart versions N and N−1.

### TWC webhooks

TWC has two admission webhooks. The `WorkerDeployment` validating/defaulting webhook is controlled by `webhook.enabled` (default `false`). The `WorkerResourceTemplate` validating webhook is always enabled and cannot be disabled. The controller Pod therefore always mounts a webhook TLS Secret, even when `webhook.enabled: false`.

### cert-manager relationship

With `certmanager.enabled: true` (the default), the current controller chart creates an Issuer and Certificate, but cert-manager itself must already be installed independently. With `certmanager.enabled: false`, the operator supplies the TLS Secret and CA bundle. The older `certmanager.install: true` value installed cert-manager as a subchart; crossing controller chart v0.30.0 requires the separate [subchart migration](#cert-manager-subchart-migration).

## Install

Use this path for a new installation. If TWC releases or CRDs already exist, inspect ownership and use the [upgrade path](#upgrade-and-rollback) instead; do not run a second install over existing resources.

### Helm prerequisite

Check `helm version --short`. The OCI chart commands below require Helm v3.8 or newer, or Helm v4. If Helm is missing, install a compatible CLI for the user's operating system using the [official Helm installation instructions](https://helm.sh/docs/intro/install/), then check again.

### Prepare webhook TLS

Choose a certificate source before installing the controller:

- **cert-manager:** Install and verify cert-manager and its CRDs first; use [cert-manager](#cert-manager). The TWC chart then creates the Issuer and Certificate.
- **Your own certificate:** Create the TLS Secret in the controller release namespace and set `certmanager.enabled: false`, `webhook.certSecretName`, and `certmanager.caBundle`; use [Webhook TLS](#webhook-tls).

### Install the charts

Choose one target chart version for both charts. Replace the release names and namespace if the user uses different ones. Install CRDs first:

```bash
helm install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <version> \
  --namespace temporal-system \
  --create-namespace

helm install temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version <version> \
  --namespace temporal-system
```

Pass the selected TLS values to the controller install. The defaults use cert-manager (`certmanager.enabled: true`) and the Secret name `webhook-server-cert`.

### Verify

Check both releases and their chart versions with `helm list -n <namespace>`. Check controller Pod readiness and that the configured webhook TLS Secret exists. When using cert-manager, also check the TWC Certificate is Ready. When using a supplied certificate, check the CA bundle on the validating webhook configurations. For failures, use the **Common issues** section of [upgrade](#upgrade-and-rollback).

## Cert-manager

Read this when TWC uses chart-managed webhook certificates (`certmanager.enabled: true`). Skip it when the operator supplies the certificate and CA bundle.

### Determine ownership

Check whether cert-manager is already installed and healthy. On an existing TWC release, Read `helm get values <twc-release> -n <namespace> -o yaml`. If `certmanager.install: true`, cert-manager belongs to the old TWC subchart; use [subchart migration](#cert-manager-subchart-migration) when upgrading across v0.30.0. Do not install another cert-manager release over those resources.

### Independent installation

For a new installation, install cert-manager separately **before** the TWC controller chart. Choose a version compatible with the cluster. The cert-manager OCI chart needs no Helm repository setup:

```bash
helm install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --version <cert-manager-version> \
  --namespace cert-manager \
  --create-namespace \
  --set crds.enabled=true \
  --wait
```

If cert-manager already exists, do not reinstall it. With `certmanager.enabled: true`, TWC creates a namespaced Issuer and Certificate and cert-manager creates the webhook serving Secret. The default Secret name is `webhook-server-cert`; `webhook.certSecretName` changes it.

### Verify

Check cert-manager Pods and webhook readiness plus the cert-manager CRDs. After TWC installation, check the TWC Certificate is Ready and its target Secret exists in the controller namespace.

## Webhook TLS

The always-on WorkerResourceTemplate webhook requires a serving certificate even when the optional WorkerDeployment webhook has `webhook.enabled: false`. The controller mounts a TLS Secret in its release namespace; `webhook.certSecretName` selects it (default `webhook-server-cert`).

### cert-manager

With `certmanager.enabled: true` (default), install cert-manager independently first. The controller chart creates an Issuer and Certificate; cert-manager provisions the Secret and injects CA bundles into the validating webhook configurations. `webhook.certSecretName` can override the default Secret name. Do not set `certmanager.caBundle` for this path.

### Bring your own certificate

Create a `kubernetes.io/tls` Secret containing the serving certificate and key in the controller release namespace. The serving certificate needs DNS SANs for `<controller-release>-webhook-service.<namespace>.svc` and `<controller-release>-webhook-service.<namespace>.svc.cluster.local`. Choose the release name and namespace before issuing it; for chart versions newer than v0.31.0, verify the webhook Service name in that chart. Set `certmanager.enabled: false`, `webhook.certSecretName: <secret-name>`, and `certmanager.caBundle` to the base64-encoded PEM CA certificate that signed the serving certificate. The API server uses that CA bundle to verify the webhook. For example:

```bash
kubectl create secret tls my-webhook-cert \
  --cert=webhook.pem \
  --key=webhook-key.pem \
  --namespace temporal-system

# Add these to the controller Helm install/upgrade values:
# --set certmanager.enabled=false
# --set webhook.certSecretName=my-webhook-cert
# --set certmanager.caBundle=<base64-encoded-CA-PEM>
```

If an external PKI rotates the CA, the chart can use `certmanager.caInjectorJob.enabled: true` with `certmanager.caInjectorJob.secret.name` and `.secret.key` pointing to a Secret containing the CA. The Job copies the CA into the WorkerResourceTemplate validating webhook configuration and, when `webhook.enabled: true`, the WorkerDeployment configuration. It runs after Helm install or upgrade; changing the CA Secret alone does not update the webhook bundles. Plan a Helm upgrade after CA rotation and verify the bundles. This option applies only with `certmanager.enabled: false`; inspect the target chart's values before setting it.

### Verify

Check the Secret is mounted by the controller, its serving certificate is valid for the webhook Service, and the WorkerResourceTemplate validating webhook configuration's CA bundle matches the issuer. Check the WorkerDeployment configuration too only when `webhook.enabled: true`. With cert-manager, also check the TWC Certificate is Ready.

## Upgrade and rollback

Use this for upgrades and rollbacks of releases that already use the separate CRDs chart. Check version-specific transitions below before following the normal path. If upgrading from controller chart v0.12.0 or earlier, or crossing the CRD rename at v0.26.0, read [legacy CRD migrations](#legacy-crd-migrations) first.

### Preflight

Run `helm list -A` to identify both releases, namespaces, and current chart versions. Save each release's values and manifest for recovery:

```bash
helm get values <release-name> -n <namespace> -o yaml > <release-name>-values.yaml
helm get manifest <release-name> -n <namespace> > <release-name>-manifest.yaml
```

Compare installed and target chart versions and values. Check whether the change crosses a transition below. If the controller release has `certmanager.install: true` and the target is chart v0.30.0 or newer, use [subchart migration](#cert-manager-subchart-migration) for the upgrade across v0.30.0. That procedure includes the controller upgrade; continue the normal path only for later versions. Confirm that webhook TLS will remain available throughout.

For a jump that skips chart minor versions (for example, v0.29.0 to v0.31.0), stage through published intermediate versions. First complete [legacy CRD adoption](#legacy-crd-migrations) if the CRDs still come from an old controller chart's `crds/` directory; that path stages at v0.22.0. After adoption, at each version upgrade CRDs first, then the controller, and verify the result. The documented guarantee covers CRDs at version N with controller N or N−1; jumping CRDs directly to v0.31.0 while the controller is still at v0.29.0 falls outside it. If an intermediate chart is unavailable, use a wider jump only when release guidance covers that exact pairing or it has been tested on a representative non-production cluster. Complete each version-specific migration before moving past it.

### Upgrade

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

With no values flags, Helm carries forward the release's user-supplied values and merges them with the target chart defaults. If adding `-f` or `--set`, review the saved user-supplied values against target defaults and removed keys, then pass the intended overrides explicitly; those flags change Helm's default value-merging behavior. Avoid blindly using `--reuse-values`, which can retain old chart defaults. Include the required webhook TLS settings for the target version; use [Webhook TLS](#webhook-tls) when the certificate source changes.

### Verify

Read back deployed chart and image versions, controller Pod readiness, the webhook TLS Secret and CA bundle, and Certificate readiness when cert-manager is used. Investigate a failed check before proceeding.

### Roll back

Before choosing a rollback command, inspect `helm history <controller-release> -n <namespace>` and `helm history <crds-release> -n <namespace>`. Identify the controller revision to restore, both chart versions that would result, the older controller's TLS requirements, and any version-specific transition the rollback would cross. A Helm rollback can restore only a revision in that release's history.

CRD chart version N is documented as compatible with controller versions N and N−1. Keep the newer CRDs and roll back only the controller when the resulting pair is within that range. For example, v0.31.0 CRDs with a v0.29.0 controller are outside the guarantee. Do not issue that rollback or assume a v0.30.0 intermediate revision exists; inspect history and establish a supported path first. If no supported path is established, stop and ask for guidance before changing the cluster.

Check migration state before restoring an older revision. A rollback below chart v0.30.0 after migrating the cert-manager subchart can reintroduce `certmanager.install: true`; this skill does not provide a reversal procedure, so stop rather than run a plain rollback. For a rollback across the v0.26.0 CRD rename, follow [legacy CRD migrations](#legacy-crd-migrations) and its version-specific downgrade procedure; do not use the normal path.

When these checks establish a supported controller rollback, run `helm rollback <controller-release> <revision> -n <namespace>`, then verify the controller and webhook TLS. Roll back the CRDs chart only if there is a specific need and a safe version sequence. First compare old and new schemas and check whether existing objects use fields absent from the older schema: Kubernetes prunes those fields on the object's next write, causing permanent data loss. Never use the normal CRD rollback path across the v0.26.0 rename.

### Version-specific transitions

- From app v1.4.0 to v1.5.2 or later: the webhook Secret mount became unconditional. With `certmanager.enabled: false`, create the Secret before upgrade or the controller Pod can remain in `ContainerCreating`.
- App v1.5.2 through v1.7.0: `webhook.enabled: true` installed the optional WorkerDeployment webhook Service but did not wire its handler. The handler works from v1.8.0 onward.
- At controller chart v0.26.0 (app v1.7.0), `TemporalWorkerDeployment` and `TemporalConnection` were renamed. Read [legacy CRD migrations](#legacy-crd-migrations) when crossing this transition.
- At controller chart v0.30.0 (app v1.11.0), the cert-manager subchart was removed. A release with `certmanager.install: true` needs [subchart migration](#cert-manager-subchart-migration).

### Common issues

| Symptom | Likely cause | Check or action |
| --- | --- | --- |
| Controller Pod stuck in `ContainerCreating` with `secret "webhook-server-cert" not found` | The always-on webhook needs a Secret; cert-manager is absent or a user-provided Secret was not created | Check `webhook.certSecretName`, Secret namespace, and certificate source. Install cert-manager or provide the Secret, then verify the existing controller release; do not run a second `helm install` over it. |
| WorkerResourceTemplate create, update, or delete fails with a webhook error | The webhook uses `failurePolicy: Fail`; the controller is unavailable or TLS is invalid | Check controller Pod readiness, serving Secret, certificate validity, and validating webhook CA bundle. |
| Independent cert-manager install reports CRD `invalid ownership metadata` | CRDs are still owned by the old TWC subchart | Use [subchart migration](#cert-manager-subchart-migration); transfer ownership before installing. |
| Certificate stops renewing after migration | The handoff or cert-manager reconciliation may be incomplete | Check Certificate and Issuer status and events, cert-manager logs, and whether the ownership transfer in [subchart migration](#cert-manager-subchart-migration) was completed. |

## Legacy CRD migrations

Use only when the installed release predates the separate CRDs chart or the resource rename. Inspect installed chart version, CRDs, and objects before selecting a path. These are one-time transitions, not steps for a normal upgrade.

### Adopt CRDs from the old controller chart

Controller chart v0.12.0 and earlier installed `temporalconnections.temporal.io` and `temporalworkerdeployments.temporal.io` from `crds/`. Helm leaves them in place during upgrades but does not update them. Use this path when those CRDs exist without a separate CRDs release; check their live ownership rather than relying only on the controller version.

For a v0.12.0-or-earlier installation, stage the migration at chart v0.22.0, the first published version with matching controller and CRDs charts. Do not upgrade the controller straight to a later target while its old CRDs remain. The v0.22.0 CRD schemas retain both old kinds and add optional status fields compared with v0.12.0. This handoff briefly pairs v0.22.0 CRDs with an older controller, outside the documented N/N−1 compatibility commitment; test it on a representative nonproduction cluster before applying it. If the installed CRDs differ from v0.12.0, compare them with the v0.22.0 chart before adopting them.

1. Record the controller chart version and values. Check that both old CRDs exist, inspect their Helm ownership metadata, and list their custom resources. Do not delete either CRD.
2. Stamp `app.kubernetes.io/managed-by=Helm` and `meta.helm.sh/release-name=<crds-release>` plus `meta.helm.sh/release-namespace=<namespace>` on both CRDs. Install the v0.22.0 CRDs chart to adopt and update them. Verify the CRDs are Established and the existing custom resources remain accessible.
3. Upgrade the controller chart to v0.22.0 with the reviewed user values. Verify controller readiness and existing custom resources. Only then follow [upgrade](#upgrade-and-rollback) through later published versions, CRDs first and controller second at each step; handle the v0.26.0 rename separately.

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

### Crossing chart v0.26.0 / app v1.7.0: renamed resources

`TemporalWorkerDeployment` became `WorkerDeployment`, `TemporalConnection` became `Connection`, and `WorkerResourceTemplate.spec.temporalWorkerDeploymentRef` became `spec.workerDeploymentRef`. New objects of the deprecated kinds cannot be created on v1.7, and existing old-kind objects stop being actively reconciled.
1. Upgrade the CRDs chart to v0.26.0, then the controller chart to v0.26.0. Confirm the controller is healthy.
2. Replace old kinds in manifests with the new kinds, keeping names, namespaces, and specs. Apply through the user's existing Helm, GitOps, or `kubectl` workflow. A migration guard finalizer holds old resources until matching new resources exist and ownership transfer completes. Wait for each new `WorkerDeployment` to become Ready. If the user's tool does not delete the deprecated objects when manifests change (plain `kubectl apply` does not), delete those old objects after the matching new objects are ready and ownership transfer is complete; let the controller remove the finalizer.
3. Update WorkerResourceTemplate references to `spec.workerDeploymentRef`. Update scripts, alerts, and automation that use old kinds.
4. Verify new `WorkerDeployment` and `Connection` objects, child workload ownership, and removal of old objects. Do not force-remove the migration guard finalizer: it protects ownership transfer.

If downgrading from app v1.7.0 to v1.6 after this migration, inspect the live old/new objects and obtain the version-specific downgrade procedure before changing the controller; a normal Helm rollback alone does not reverse the resource rename.

## Cert-manager subchart migration

Use this only when the installed controller release set `certmanager.install: true` and the target removes the dependency at controller chart v0.30.0 (app v1.11.0). The old default was `false`; check the installed values rather than assuming migration applies. A direct Helm upgrade can delete cert-manager resources owned by the TWC release.

### Check the existing installation

Run `helm get values <twc-release> -n <namespace> -o yaml` and confirm that the saved values contain `certmanager.install: true`. If they do not, this migration does not apply. Save `helm get manifest <twc-release> -n <namespace>` to identify the cert-manager resources in the TWC release.

Run `helm get values <twc-release> -n <namespace> --all` to check `certmanager.enabled`, which controls whether TWC creates its Issuer and Certificate. Use this output only to check the current setting. It includes old chart defaults, so do not pass it to `helm upgrade` as a values file.

List the cert-manager resources owned by the TWC release: ServiceAccounts, Deployments, Services, Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, CRDs, and webhook configurations. Include any leader-election Role and RoleBinding in `kube-system`; their names depend on the cert-manager version. Also note the TWC Certificate, Issuer, and serving Secret. cert-manager creates the Secret, so it may not appear in the Helm manifest. Check whether other workloads use this cert-manager installation.

If the installed chart predates the last release with the cert-manager subchart, complete any [legacy CRD migration](#legacy-crd-migrations) first. Then use the [upgrade procedure](#upgrade-and-rollback) to move both TWC charts forward through published intermediate versions until they reach that last release. Keep the subchart installed during those upgrades. Save fresh values and manifests and list the current resources before migrating. Perform this migration when upgrading to the first published chart version without the subchart; continue to the requested target version only after the migration succeeds.

### Migration sequence

The migration chart version is the first published controller chart without the subchart (v0.30.0). Use the same version for both TWC charts at this stage, even if the requested final version is newer. Perform these stages in order; check the result of each stage before continuing:

1. Add `helm.sh/resource-policy=keep` to every cert-manager resource owned by the TWC release, including cluster-scoped and `kube-system` leader-election resources. Use the saved manifest to find each resource. Before annotating it, check that the live resource belongs to the TWC release. For example: `kubectl annotate deployment <name> -n <namespace> helm.sh/resource-policy=keep`. Skip resources absent from that cert-manager version.
2. Compare the controller values you set with the migration chart's values. Save the overrides you need to keep in a file. Do not use `helm get values --all` for this file; it includes old chart defaults.
3. Confirm the separate TWC CRDs release exists; complete [legacy CRD adoption](#legacy-crd-migrations) first if it does not. Upgrade the CRDs chart to the migration chart version, then upgrade the controller chart to the same version with `-f <reviewed-values-file> --set certmanager.install=false`. If TWC uses cert-manager for TLS, retain `certmanager.enabled=true`; if it uses a supplied certificate, retain `certmanager.enabled=false`, the Secret, and CA settings. Passing `--set` without the reviewed values file can discard other user overrides. Check that the controller and webhook Secret remain healthy; the old cert-manager Pods should still run because their resources were kept.
4. Transfer the six cert-manager CRDs to the independent release by setting `meta.helm.sh/release-name=cert-manager` and `meta.helm.sh/release-namespace=cert-manager` on `challenges.acme.cert-manager.io`, `orders.acme.cert-manager.io`, `certificaterequests.cert-manager.io`, `certificates.cert-manager.io`, `clusterissuers.cert-manager.io`, and `issuers.cert-manager.io`. Example: `kubectl annotate crd certificates.cert-manager.io meta.helm.sh/release-name=cert-manager meta.helm.sh/release-namespace=cert-manager --overwrite`. Verify the annotations before installation.
5. Apply the documented ownership transfer to the TWC Certificate `temporal-worker-controller-serving-cert` and Issuer `temporal-worker-controller-selfsigned-issuer`: set the same two annotations in the TWC namespace. Use actual names if the release differs.
6. Remove the **old subchart's** cert-manager Deployments, Services, ServiceAccounts, RBAC, and cert-manager webhook configurations before the independent install to avoid conflicts. Compare each object and label selector against the saved manifest before deletion; do not remove the TWC controller, its webhook configurations, or shared cert-manager resources.
7. Install cert-manager as an independent Helm release in the `cert-manager` namespace with its CRDs enabled and `--wait`; use the example in [cert-manager](#cert-manager). Choose a chart version compatible with the cluster. If an independent release already exists, inspect it instead of installing another.
8. Verify independent cert-manager Pods and webhook, absence of old cert-manager Pods in the TWC namespace, TWC controller readiness, the webhook serving Secret, and Certificate readiness. Check certificate renewal later, when it is due; it is not an immediate migration check.

### Verify and recover

If interrupted, inspect Helm ownership and live resources before retrying. Resume from the first incomplete stage rather than repeating annotation or deletion blindly. An `invalid ownership metadata` error on cert-manager installation suggests incomplete CRD ownership transfer. If a certificate stops renewing, inspect its status and events, then check whether the Certificate and Issuer ownership transfer was completed.

## Uninstall

Use this only when the user wants to remove an installation. Uninstalling the controller or CRDs can affect WorkerDeployments and their workloads.

### Inventory and decide what to remove

Identify the controller and CRDs Helm releases and their namespaces with `helm list -A`. Check which TWC CRDs exist with `kubectl get crd`. Inventory instances of each installed kind across all namespaces; include cluster-scoped ClusterConnections if that CRD exists. For example:

```bash
kubectl get workerdeployments,workerresourcetemplates,connections -A
kubectl get clusterconnections
```

Also list deprecated TemporalWorkerDeployments and TemporalConnections where those CRDs exist. Identify resources managed by GitOps or other tools and remove them through that source so they are not recreated. Decide whether the CRDs should be deleted or retained. Independently installed cert-manager may serve other workloads; leave it in place unless its removal is a separate, explicit task.

### Remove resources while the controller is running

Delete WorkerDeployments and deprecated TemporalWorkerDeployments first. Wait for their deletion to finish before stopping the controller: the controller handles their finalizers and cleans up Temporal server-side deployment state. Then remove WorkerResourceTemplates, Connections, deprecated TemporalConnections, and ClusterConnections. Verify that no TWC custom resources remain. If deprecated resources are held by a migration guard, follow the applicable [legacy CRD migration](#legacy-crd-migrations); do not force-remove finalizers as a routine uninstall step.

### Uninstall the Helm releases

After custom resources are gone, uninstall the controller release with `helm uninstall <controller-release> -n <namespace>`. Verify its Deployment and webhook configurations are gone. Before uninstalling the CRDs release, show the user the final cluster-wide custom-resource inventory and state that deleting a CRD deletes every custom resource of that type. Get explicit approval for CRD removal; approval to uninstall the controller alone does not include it. Only then run `helm uninstall <crds-release> -n <namespace>`. Verify the releases and, when removed, the CRDs are absent.
