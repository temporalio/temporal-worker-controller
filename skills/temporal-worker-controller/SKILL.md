---
name: temporal-worker-controller
description: Operate Temporal Worker Controller (TWC) on Kubernetes. Use for installing, upgrading, rolling back, or uninstalling TWC and its CRDs chart; cert-manager migrations; webhook TLS; and related errors such as "webhook-server-cert not found" or "invalid ownership metadata".
---

# Temporal Worker Controller

Match the user's task below. Read the reference's opening checks and the linked section; follow links to other sections only when the task requires them. The reference contains the install and upgrade guidance, so a TWC repository checkout is not required.

## Task routing

| User task | Start at |
| --- | --- |
| Understand the CRDs chart, controller chart, or webhooks | [Concepts](references/install-upgrade-controller.md#concepts) |
| Install TWC on a cluster | [Install](references/install-upgrade-controller.md#install) |
| Install or check cert-manager for TWC | [Cert-manager](references/install-upgrade-controller.md#cert-manager) |
| Configure webhook TLS or diagnose a missing serving Secret | [Webhook TLS](references/install-upgrade-controller.md#webhook-tls) |
| Upgrade or roll back TWC | [Upgrade and rollback](references/install-upgrade-controller.md#upgrade-and-rollback) |
| Diagnose an install or upgrade error, including invalid ownership metadata | [Common issues](references/install-upgrade-controller.md#common-issues) |
| Adopt CRDs from an old controller chart or cross the CRD rename | [Legacy CRD migrations](references/install-upgrade-controller.md#legacy-crd-migrations) |
| Migrate from the cert-manager subchart | [Cert-manager subchart migration](references/install-upgrade-controller.md#cert-manager-subchart-migration) |
| Uninstall TWC or its CRDs chart | [Uninstall](references/install-upgrade-controller.md#uninstall) |

This skill does not yet cover other TWC tasks, such as diagnosing a stuck WorkerDeployment, image update automation, or autoscaling. For an uncovered request, say the skill has no procedure for it and suggest asking in the [Temporal Slack community](https://temporalio.slack.com/archives/C07MDJ6S3HP).
