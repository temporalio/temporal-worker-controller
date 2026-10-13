# Install TWC

Use this path for a new installation. If TWC releases or CRDs already exist, inspect ownership and use the [upgrade path](upgrade.md) instead; do not run a second install over existing resources.

## Helm prerequisite

Check `helm version --short`. TWC chart installation requires Helm v3.0 or newer. If Helm is missing, install a compatible CLI for the user's operating system using the [official Helm v3 installation instructions](https://helm.sh/docs/v3/intro/install/), then check again.

## Prepare webhook TLS

The controller always needs a TLS Secret for its WorkerResourceTemplate webhook. Choose one path before installing the controller. If cluster access is available, inspect the relevant prerequisite. Otherwise, tell the user what is required and ask for the check result.

- Default: independently install and verify cert-manager and its CRDs first; use [cert-manager](cert-manager.md). The TWC chart then creates the Issuer and Certificate.
- User-provided certificate: create the TLS Secret in the controller release namespace and configure `certmanager.enabled: false`, `webhook.certSecretName`, and `certmanager.caBundle`; use [Webhook TLS](webhook-tls.md).

## Install the charts

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

## Verify

Check both releases and their chart versions with `helm list -n <namespace>`. Check controller Pod readiness and that the configured webhook TLS Secret exists. When using cert-manager, also check the TWC Certificate is Ready. When using a supplied certificate, check the CA bundle on the validating webhook configurations. For failures, use the **Common issues** section of [upgrade](upgrade.md).
