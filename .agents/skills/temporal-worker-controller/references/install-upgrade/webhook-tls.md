# Webhook TLS

The always-on WorkerResourceTemplate webhook requires a serving certificate even when the optional WorkerDeployment webhook has `webhook.enabled: false`. The controller mounts a TLS Secret in its release namespace; `webhook.certSecretName` selects it (default `webhook-server-cert`).

## cert-manager

With `certmanager.enabled: true` (default), install cert-manager independently first. The controller chart creates an Issuer and Certificate; cert-manager provisions the Secret and injects CA bundles into the validating webhook configurations. `webhook.certSecretName` can override the default Secret name. Do not set `certmanager.caBundle` for this path.

## Bring your own certificate

Create a `kubernetes.io/tls` Secret containing the serving certificate and key in the controller release namespace. Set `certmanager.enabled: false`, `webhook.certSecretName: <secret-name>`, and `certmanager.caBundle` to the base64-encoded PEM CA certificate that signed the serving certificate. The API server uses that CA bundle to verify the webhook. For example:

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

If an external PKI rotates the CA, the chart can instead use `certmanager.caInjectorJob.enabled: true` with `certmanager.caInjectorJob.secret.name` and `.secret.key` pointing to a Secret containing the CA. The post-install/post-upgrade Job copies its CA to both validating webhook configurations. This option applies only with `certmanager.enabled: false`; inspect the target chart's values before setting it.

## Verify

Check the Secret is mounted by the controller, its serving certificate is valid for the webhook Service, and the validating webhook configuration CA bundles match the issuer. With cert-manager, also check the TWC Certificate is Ready.
