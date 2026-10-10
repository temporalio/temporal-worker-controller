# TWC install and upgrade concepts

## Two Helm charts

Document what the CRDs chart and controller chart own, why they have the same chart version, and why CRDs go first on install and upgrade. Use [Why a Separate CRDs Chart?](../../../../../docs/crd-management.md#why-a-separate-crds-chart) and [Compatibility Commitment](../../../../../docs/crd-management.md#compatibility-commitment).

## Webhook TLS

Explain the optional WorkerDeployment webhook and the always-on WorkerResourceTemplate webhook. The controller needs a TLS Secret even with `webhook.enabled: false`. Use [upgrade.md](../../../../../docs/upgrade.md#webhooks) and the [README](../../../../../README.md#webhook-tls-configuration).

## cert-manager relationship

Explain that the current controller chart can create an Issuer and Certificate, but cert-manager itself must be installed separately. Point upgrades from the old `certmanager.install: true` subchart to [subchart migration](cert-manager-subchart-migration.md).
