# Migrate from the cert-manager subchart

Use this only when the installed controller release set `certmanager.install: true` and the target chart removes the dependency at controller chart `v0.30.0` (app `v1.11.0`). A direct Helm upgrade can delete cert-manager resources owned by that release.

## Identify affected resources

Read the installed values and release manifest. Match the resource names and namespaces to the actual release; [Migrating from the cert-manager Subchart](../../../../../docs/upgrade.md#migrating-from-the-cert-manager-subchart) identifies the affected releases. Account for other workloads that depend on this cert-manager installation.

## Migration sequence

Read the relevant subsection for each step, then check it before continuing:

1. [Annotate cert-manager resources](../../../../../docs/upgrade.md#annotate-cert-manager-resources) to keep.
2. [Upgrade TWC with the subchart disabled](../../../../../docs/upgrade.md#upgrade-temporal-worker-controller-with-the-subchart-disabled).
3. [Transfer cert-manager CRD ownership](../../../../../docs/upgrade.md#transfer-cert-manager-crd-ownership).
4. [Transfer Certificate and Issuer ownership](../../../../../docs/upgrade.md#transfer-certificate-and-issuer-ownership).
5. [Clean up old cert-manager resources](../../../../../docs/upgrade.md#clean-up-old-cert-manager-resources).
6. [Install cert-manager independently](../../../../../docs/upgrade.md#install-cert-manager-independently).
7. [Verify](../../../../../docs/upgrade.md#verify).

Do not use the guide's delete commands until their selectors and ownership match the inspected cluster.

## Verify and recover

Use the migration guide's [Verify](../../../../../docs/upgrade.md#verify) subsection to confirm the independent cert-manager pods, TWC webhook Secret, controller readiness, and certificate renewal. If interrupted, inspect Helm ownership and live resources before retrying; resume from the first incomplete step rather than repeating deletion.
