---
name: temporal-worker-controller
description: Operate Temporal Worker Controller (TWC) on Kubernetes. Use for installing, upgrading, rolling back, or uninstalling TWC and its CRDs chart; cert-manager migrations; webhook TLS; and related errors such as "webhook-server-cert not found" or "invalid ownership metadata".
---

# Temporal Worker Controller

Match the user's task to a row below. Read that reference's opening checks and the sections relevant to the request.

## Routing to reference files

| User intent | Reference file |
| --- | --- |
| Install, upgrade, roll back, or uninstall TWC; manage the controller and CRDs charts, CRD migrations, cert-manager, or webhook TLS; diagnose controller Pod startup or webhook errors during those tasks | [install-upgrade-controller.md](references/install-upgrade-controller.md) |

This skill does not yet cover other TWC tasks, such as diagnosing a stuck WorkerDeployment, image update automation, or autoscaling. For an uncovered request, say the skill has no procedure for it and suggest asking in the [Temporal Slack community](https://temporalio.slack.com/archives/C07MDJ6S3HP).
