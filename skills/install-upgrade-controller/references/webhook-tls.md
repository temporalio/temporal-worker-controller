# Webhook TLS

The always-on WorkerResourceTemplate webhook requires a serving certificate even when the optional WorkerDeployment webhook has `webhook.enabled: false`. The controller mounts a TLS Secret in its release namespace; `webhook.certSecretName` selects it (default `webhook-server-cert`).

## cert-manager

With `certmanager.enabled: true` (default), install cert-manager independently first. The controller chart creates an Issuer and Certificate; cert-manager provisions the Secret and injects CA bundles into the validating webhook configurations. `webhook.certSecretName` can override the default Secret name. Do not set `certmanager.caBundle` for this path.

## Bring your own certificate

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

## Verify

Check the Secret is mounted by the controller, its serving certificate is valid for the webhook Service, and the WorkerResourceTemplate validating webhook configuration's CA bundle matches the issuer. Check the WorkerDeployment configuration too only when `webhook.enabled: true`. With cert-manager, also check the TWC Certificate is Ready.
