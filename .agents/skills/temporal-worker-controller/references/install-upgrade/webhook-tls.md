# Webhook TLS

The WorkerResourceTemplate webhook requires a serving certificate on every TWC installation. Choose the certificate source before installing or upgrading; see the [README TLS options](../../../../../README.md#webhook-tls-configuration) and chart [values](../../../../../helm/temporal-worker-controller/values.yaml).

## cert-manager

With `certmanager.enabled: true`, install cert-manager separately. The TWC chart creates the Issuer and Certificate. `webhook.certSecretName` sets the Secret mounted by the controller.

## Bring your own certificate

With `certmanager.enabled: false`, create or provide the TLS Secret in the TWC namespace and set `webhook.certSecretName` and `certmanager.caBundle` to the serving certificate's CA. Account for CA rotation; inspect `certmanager.caInjectorJob` in the target chart when a static bundle is unsuitable.

## Verify

Check the mounted Secret, serving certificate validity, and CA bundles in the TWC validating webhook configurations. When cert-manager is used, also check Certificate readiness.
