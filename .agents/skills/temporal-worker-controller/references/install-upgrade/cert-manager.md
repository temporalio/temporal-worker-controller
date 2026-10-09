# cert-manager prerequisite

Read this when TWC will use chart-managed webhook certificates. Skip it when the user supplies a certificate and CA bundle.

## Detect

Determine whether cert-manager is already installed and healthy. Check whether it belongs to an old TWC release; if `certmanager.install: true`, use [subchart migration](cert-manager-subchart-migration.md) before changing ownership.

## Install

For a fresh cluster, install cert-manager independently with its CRDs before installing the TWC controller chart. Follow the [README TLS section](../../../../../README.md#webhook-tls-configuration) and pin a compatible cert-manager version for the cluster.

## Verify

Check cert-manager pods, CRDs, and webhook readiness. After installing TWC, check that its Certificate becomes Ready and creates the expected Secret.
