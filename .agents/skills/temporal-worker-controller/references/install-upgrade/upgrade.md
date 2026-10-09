# Upgrade and roll back TWC

Use [Before You Upgrade](../../../../../docs/upgrade.md#before-you-upgrade) for preflight and [Upgrade](../../../../../docs/upgrade.md#upgrade) for the normal chart sequence. Read [Version-Specific Notes](../../../../../docs/upgrade.md#version-specific-notes) only when crossing a version named there. Use [CRD compatibility](../../../../../docs/crd-management.md#compatibility-commitment) and the relevant [legacy migration](../../../../../docs/crd-management.md#migration-guide-for-existing-users) when applicable.

## Preflight

Read both Helm releases, versions, values, and manifests. Save the current values and manifests for recovery. Compare installed and target chart versions and review migrations crossed by that change. If the controller release has `certmanager.install: true` and the target removes the subchart, read [subchart migration](cert-manager-subchart-migration.md) before any upgrade. Confirm that webhook TLS will remain available.

## Upgrade

For the normal two-chart path, upgrade the CRDs chart first, then the controller chart to the matching version. Carry forward intentional values after checking the target chart defaults and removed values. Use the actual release names and namespace, not examples from the docs. Follow [existing-user CRD migration](../../../../../docs/crd-management.md#migration-guide-for-existing-users) when upgrading from legacy CRD packaging or names.

## Verify

Read back the deployed chart and image versions, controller readiness, webhook Secret and CA bundle, and Certificate readiness when cert-manager is used. See [Common Issues](../../../../../docs/upgrade.md#common-issues) if a check fails.

## Roll back

Roll back the controller first. Prefer keeping the newer CRDs within the documented compatibility window. Before rolling back CRDs, check for objects using fields that the older schema would prune; see [CRD rollback and field pruning](../../../../../docs/crd-management.md#crd-rollback-and-field-pruning). Check the older controller's TLS requirements before changing versions.
