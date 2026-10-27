---
name: temporal-worker-controller
description: Operate Temporal Worker Controller (TWC) on Kubernetes. Use for installing, upgrading, rolling back, or uninstalling TWC and its CRDs chart; cert-manager migrations; webhook TLS; and related errors such as "webhook-server-cert not found" or "invalid ownership metadata".
---

# Temporal Worker Controller

Match the user's task below. Read the reference's opening checks, then the linked section and any sections it points to.

## Routing to reference files

| User intent | Start at |
| --- | --- |
| Understand the controller chart, CRDs chart, or webhooks | [Concepts](references/install-upgrade-controller.md#concepts) |
| Install TWC | [Install](references/install-upgrade-controller.md#install) |
| Install or check cert-manager for TWC | [Cert-manager](references/install-upgrade-controller.md#cert-manager) |
| Configure webhook TLS or diagnose a missing serving Secret | [Webhook TLS](references/install-upgrade-controller.md#webhook-tls) |
| Upgrade or roll back TWC | [Upgrade and rollback](references/install-upgrade-controller.md#upgrade-and-rollback) |
| Migrate legacy CRDs or the cert-manager subchart | [Legacy CRD migrations](references/install-upgrade-controller.md#legacy-crd-migrations) or [cert-manager subchart migration](references/install-upgrade-controller.md#cert-manager-subchart-migration) |
| Uninstall TWC or its CRDs chart | [Uninstall](references/install-upgrade-controller.md#uninstall) |
| Diagnose an install, upgrade, or webhook error | [Common issues](references/install-upgrade-controller.md#common-issues) |
