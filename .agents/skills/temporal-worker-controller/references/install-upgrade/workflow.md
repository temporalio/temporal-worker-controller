# Install and upgrade workflow

Use this area for TWC installation, upgrades, rollbacks, uninstallation, cert-manager setup or migration, webhook TLS, and failures that may come up during those operations.

## Discover the installed state

For a request involving an existing cluster, use read only checks before choosing the task reference:

1. Find the TWC and CRDs chart releases, namespaces, and versions with `helm list -A`.
2. Read the controller release's values with `helm get values <release> -n <namespace> --all` to check `certmanager.install`. Check its manifest if the values do not show whether the release owns cert-manager.
3. Check which `temporal.io` CRDs exist. Query old Kind resources only if their CRDs exist.
4. Use the findings to select the references below; more than one may apply.

If cluster access is unavailable or the user requests a plan without commands, ask for the relevant results rather than running checks. For a fresh install, first check whether a TWC release or CRDs already exist, then follow the prerequisites in [Install](install.md).

## Before changing a cluster

Inspect the installed releases and affected resources first. Show the user the proposed commands, the resources they will change or delete, and how you will verify the result. Get explicit approval for that plan before running `helm install`, `helm upgrade`, `helm rollback`, `helm uninstall`, or mutating `kubectl` commands, unless the user has already approved those specific changes. Read only checks do not need approval.

## Route within this area

| Task | Read |
| --- | --- |
| Understand the chart and webhook relationships | [Concepts](concepts.md) |
| Install TWC on a cluster | [Install](install.md), then the relevant TLS path |
| Install or verify cert-manager for TWC | [cert-manager](cert-manager.md) |
| Upgrade or roll back TWC | [Upgrade](upgrade.md) |
| Uninstall TWC or its CRDs chart | [Uninstall](uninstall.md) |
| Upgrade from old `crds/` packaging or cross the CRD rename | [Legacy CRD migrations](legacy-crd-migrations.md) for those transitions |
| Upgrade from a release with `certmanager.install: true` across v0.30.0 | [Subchart migration](cert-manager-subchart-migration.md) for that upgrade |
| Configure or change webhook TLS | [Webhook TLS](webhook-tls.md) |

## Using these references

These references cover the procedures described here without a TWC repository checkout. Read another reference only when the request crosses into its topic. The current-chart guidance was checked against chart v0.31.0; for a newer target, read its published chart values and templates before using version specific instructions. Do not assume that example release names or namespaces match the user's cluster.
