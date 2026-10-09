# Install TWC

Use this path for a new TWC installation. The [repository installation guide](../../../../../docs/crd-management.md#initial-installation) provides the chart commands.

## Helm prerequisite

Run `helm version --short` before the chart commands. The [TWC prerequisites](../../../../../README.md#prerequisites) require Helm v3.0 or newer. If Helm is missing, install a compatible Helm CLI using the [official Helm installation guide](https://helm.sh/docs/v3/intro/install/) for the user's operating system, then check the version again. Skip installation when Helm is already available.

## Prepare webhook TLS

Use cert-manager by default unless the user chooses to provide the webhook certificate. If the agent has cluster access, inspect the relevant prerequisite with read-only commands. Otherwise, tell the user what must be ready and ask them to confirm it or provide the check result.

- With cert-manager, check that it is installed and healthy before installing the controller; use [cert-manager](cert-manager.md) if setup is needed.
- With a user-provided certificate, confirm the TLS Secret and CA bundle are ready; use [Webhook TLS](webhook-tls.md) for the required values.

## Install

Install the CRDs chart, then the controller chart at the same chart version. Adapt the commands in [Initial Installation](../../../../../docs/crd-management.md#initial-installation) to the user's release names and namespace. Set the controller's TLS values for the chosen option.

## Verify

Check both Helm releases, controller readiness, and the webhook TLS Secret. If cert-manager supplies the certificate, check that the Certificate is Ready; otherwise check the configured CA bundle. See [Common Issues](../../../../../docs/upgrade.md#common-issues) if a check fails.
