# TWC install and upgrade concepts

## Two Helm charts

`temporal-worker-controller-crds` owns the CustomResourceDefinitions. `temporal-worker-controller` owns the controller Deployment, RBAC, Services, and admission webhooks. The charts have matching version numbers. Install or upgrade the CRDs chart first, then the controller chart at the same target version. Helm does not upgrade CRDs placed in a chart's `crds/` directory during `helm upgrade`; the separate chart gives CRDs an explicit upgrade path. Uninstalling the CRDs chart deletes custom resources of those types.

CRD chart version N is compatible with controller chart versions N and N−1.

## Webhook TLS

TWC has two admission webhooks. The `WorkerDeployment` validating/defaulting webhook is controlled by `webhook.enabled` (default `false`). The `WorkerResourceTemplate` validating webhook is always enabled and cannot be disabled. The controller Pod therefore always mounts a webhook TLS Secret, even when `webhook.enabled: false`.

## cert-manager relationship

With `certmanager.enabled: true` (the default), the current controller chart creates an Issuer and Certificate, but cert-manager itself must already be installed independently. With `certmanager.enabled: false`, the operator supplies the TLS Secret and CA bundle. The older `certmanager.install: true` value installed cert-manager as a subchart; crossing controller chart v0.30.0 requires the separate [subchart migration](cert-manager-subchart-migration.md).
