# Migrate from the cert-manager subchart

Use this only when the installed controller release set `certmanager.install: true` and the target removes the dependency at controller chart v0.30.0 (app v1.11.0). The old default was `false`; check the installed values rather than assuming migration applies. A direct Helm upgrade can delete cert-manager resources owned by the TWC release.

## Check the existing installation

Run `helm get values <twc-release> -n <namespace> -o yaml` and confirm that the saved values contain `certmanager.install: true`. If they do not, this migration does not apply. Save `helm get manifest <twc-release> -n <namespace>` to identify the cert-manager resources in the TWC release.

Run `helm get values <twc-release> -n <namespace> --all` to check `certmanager.enabled`, which controls whether TWC creates its Issuer and Certificate. Use this output only to check the current setting. It includes old chart defaults, so do not pass it to `helm upgrade` as a values file.

List the cert-manager resources owned by the TWC release: ServiceAccounts, Deployments, Services, Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, CRDs, and webhook configurations. Include any leader-election Role and RoleBinding in `kube-system`; their names depend on the cert-manager version. Also note the TWC Certificate, Issuer, and serving Secret. cert-manager creates the Secret, so it may not appear in the Helm manifest. Check whether other workloads use this cert-manager installation.

If the installed chart predates the last release with the cert-manager subchart, complete any [legacy CRD migration](legacy-crd-migrations.md) first. Then use the [upgrade procedure](upgrade.md) to move both TWC charts forward through published intermediate versions until they reach that last release. Keep the subchart installed during those upgrades. Save fresh values and manifests and list the current resources before migrating. Perform this migration when upgrading to the first published chart version without the subchart; continue to the requested target version only after the migration succeeds.

## Migration sequence

The migration chart version is the first published controller chart without the subchart (v0.30.0). Use the same version for both TWC charts at this stage, even if the requested final version is newer. Perform these stages in order; check the result of each stage before continuing:

1. Add `helm.sh/resource-policy=keep` to every cert-manager resource owned by the TWC release, including cluster-scoped and `kube-system` leader-election resources. Use the saved manifest to find each resource. Before annotating it, check that the live resource belongs to the TWC release. For example: `kubectl annotate deployment <name> -n <namespace> helm.sh/resource-policy=keep`. Skip resources absent from that cert-manager version.
2. Compare the controller values you set with the migration chart's values. Save the overrides you need to keep in a file. Do not use `helm get values --all` for this file; it includes old chart defaults.
3. Confirm the separate TWC CRDs release exists; complete [legacy CRD adoption](legacy-crd-migrations.md) first if it does not. Upgrade the CRDs chart to the migration chart version, then upgrade the controller chart to the same version with `-f <reviewed-values-file> --set certmanager.install=false`. If TWC uses cert-manager for TLS, retain `certmanager.enabled=true`; if it uses a supplied certificate, retain `certmanager.enabled=false`, the Secret, and CA settings. Passing `--set` without the reviewed values file can discard other user overrides. Check that the controller and webhook Secret remain healthy; the old cert-manager Pods should still run because their resources were kept.
4. Transfer the six cert-manager CRDs to the independent release by setting `meta.helm.sh/release-name=cert-manager` and `meta.helm.sh/release-namespace=cert-manager` on `challenges.acme.cert-manager.io`, `orders.acme.cert-manager.io`, `certificaterequests.cert-manager.io`, `certificates.cert-manager.io`, `clusterissuers.cert-manager.io`, and `issuers.cert-manager.io`. Example: `kubectl annotate crd certificates.cert-manager.io meta.helm.sh/release-name=cert-manager meta.helm.sh/release-namespace=cert-manager --overwrite`. Verify the annotations before installation.
5. Apply the documented ownership transfer to the TWC Certificate `temporal-worker-controller-serving-cert` and Issuer `temporal-worker-controller-selfsigned-issuer`: set the same two annotations in the TWC namespace. Use actual names if the release differs.
6. Remove the **old subchart's** cert-manager Deployments, Services, ServiceAccounts, RBAC, and cert-manager webhook configurations before the independent install to avoid conflicts. Compare each object and label selector against the saved manifest before deletion; do not remove the TWC controller, its webhook configurations, or shared cert-manager resources.
7. Install cert-manager as an independent Helm release in the `cert-manager` namespace with its CRDs enabled and `--wait`; use the example in [cert-manager](cert-manager.md). Choose a chart version compatible with the cluster. If an independent release already exists, inspect it instead of installing another.
8. Verify independent cert-manager Pods and webhook, absence of old cert-manager Pods in the TWC namespace, TWC controller readiness, the webhook serving Secret, and Certificate readiness. Check certificate renewal later, when it is due; it is not an immediate migration check.

## Verify and recover

If interrupted, inspect Helm ownership and live resources before retrying. Resume from the first incomplete stage rather than repeating annotation or deletion blindly. An `invalid ownership metadata` error on cert-manager installation suggests incomplete CRD ownership transfer. If a certificate stops renewing, inspect its status and events, then check whether the Certificate and Issuer ownership transfer was completed.
