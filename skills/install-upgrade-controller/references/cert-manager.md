# cert-manager prerequisite

Read this when TWC uses chart-managed webhook certificates (`certmanager.enabled: true`). Skip it when the operator supplies the certificate and CA bundle.

## Determine ownership

Check whether cert-manager is already installed and healthy. On an existing TWC release, Read `helm get values <twc-release> -n <namespace> -o yaml`. If `certmanager.install: true`, cert-manager belongs to the old TWC subchart; use [subchart migration](cert-manager-subchart-migration.md) when upgrading across v0.30.0. Do not install another cert-manager release over those resources.

## Independent installation

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

## Verify

Check cert-manager Pods and webhook readiness plus the cert-manager CRDs. After TWC installation, check the TWC Certificate is Ready and its target Secret exists in the controller namespace.
