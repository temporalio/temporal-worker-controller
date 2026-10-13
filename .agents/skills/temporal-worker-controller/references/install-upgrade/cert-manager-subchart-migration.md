# Migrate from the cert-manager subchart

Use this only when the installed controller release set `certmanager.install: true` and the target removes the dependency at controller chart v0.30.0 (app v1.11.0). The old default was `false`; check the installed values rather than assuming migration applies. A direct Helm upgrade can delete cert-manager resources owned by the TWC release.

## Identify affected resources

Read the installed values and manifest (`helm get values <twc-release> -n <namespace> -o yaml` and `helm get manifest <twc-release> -n <namespace>`). Confirm `certmanager.install: true`; inventory cert-manager ServiceAccounts, Deployments, Services, Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, CRDs, and webhook configurations owned by this release. Include leader-election Roles/RoleBindings in `kube-system`; names vary with cert-manager version. Record the TWC Certificate, Issuer, and serving Secret. Check whether other workloads depend on this cert-manager installation. Back up values and manifest before changing ownership.

## Migration sequence

Perform these stages in order; check the result of each stage before continuing:

1. Add `helm.sh/resource-policy=keep` to every cert-manager resource owned by the TWC release, including cluster-scoped resources and leader-election resources in `kube-system`. Derive names from the installed release manifest and verify each object before annotating it. Example: `kubectl annotate deployment <name> -n <namespace> helm.sh/resource-policy=keep`. Resources absent in that cert-manager version need no annotation.
2. If the separate TWC CRDs release exists, upgrade it to the target chart version first. Review the saved **user-supplied** controller values against the target chart and put the values to retain in a file; do not use `helm get values --all` as that file. Upgrade the controller chart to the same version with `-f <reviewed-values-file> --set certmanager.install=false --set certmanager.enabled=true`. Supplying `--set` without the reviewed values file can discard other user overrides. Keep the webhook Secret and controller healthy. The old cert-manager Pods should still run because their resources were kept.
3. Transfer the six cert-manager CRDs to the independent release by setting `meta.helm.sh/release-name=cert-manager` and `meta.helm.sh/release-namespace=cert-manager` on `challenges.acme.cert-manager.io`, `orders.acme.cert-manager.io`, `certificaterequests.cert-manager.io`, `certificates.cert-manager.io`, `clusterissuers.cert-manager.io`, and `issuers.cert-manager.io`. Example: `kubectl annotate crd certificates.cert-manager.io meta.helm.sh/release-name=cert-manager meta.helm.sh/release-namespace=cert-manager --overwrite`. Verify the ownership metadata before installation.
4. Set those two ownership annotations on the TWC Certificate `temporal-worker-controller-serving-cert` and Issuer `temporal-worker-controller-selfsigned-issuer` in the TWC namespace. Use actual names if the release differs. This step is required for certificate renewal after migration.
5. Remove the **old subchart's** cert-manager Deployments, Services, ServiceAccounts, RBAC, and cert-manager webhook configurations before the independent install to avoid conflicts. Compare every object and label selector against the saved release manifest before deletion; do not remove the TWC controller, its webhook configurations, or shared cert-manager resources.
6. Install cert-manager as an independent Helm release in the `cert-manager` namespace with its CRDs enabled and `--wait`; use the example in [cert-manager](cert-manager.md). Choose the appropriate chart version and source for the cluster. A preexisting independent release changes this stage: inspect it and avoid a second install.
7. Verify independent cert-manager Pods and webhook, absence of old cert-manager Pods in the TWC namespace, TWC controller readiness, webhook serving Secret, Certificate readiness, and subsequent certificate renewal.

## Verify and recover

If interrupted, inspect Helm ownership and live resources before retrying. Resume from the first incomplete stage rather than repeating annotation or deletion blindly. An `invalid ownership metadata` error on cert-manager installation points to incomplete CRD ownership transfer; a certificate that stops renewing points to incomplete Certificate or Issuer transfer.
