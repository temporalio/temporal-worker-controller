# CRD Management

## Why a Separate CRDs Chart?

Helm's `crds/` directory installs CRDs on `helm install` but **silently ignores them on `helm upgrade`** and does not delete them on `helm uninstall`. This means users have no supported mechanism to upgrade CRDs when upgrading the controller chart via the standard Helm workflow.

To provide an explicit, version-tracked CRD upgrade path, the temporal-worker-controller ships CRDs as a separate Helm chart: `temporal-worker-controller-crds`. This is the same pattern used by [Karpenter](https://karpenter.sh/docs/upgrading/upgrade-guide/) (`karpenter-crd`), [prometheus-operator-crds](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-operator-crds), and [VictoriaMetrics](https://docs.victoriametrics.com/helm/victoria-metrics-operator-crds/) (`victoria-metrics-operator-crds`).

Benefits:
- CRDs survive accidental `helm uninstall` of either chart (`helm.sh/resource-policy: keep`)
- CRDs and the controller can be upgraded and rolled back independently
- Clear versioning: both charts always use the same version number

## Compatibility Commitment

> **CRD chart version N is forward-compatible with controller chart versions N and N−1.**

- CRD changes are **additive-only** within a minor version (no field removals, no type changes)
- Rolling back the controller one minor version while keeping the current CRDs is always safe
- Upgrading CRDs ahead of the controller (within one minor version) is always safe
- Structural CRD breaking changes (if ever needed) require a new API version (e.g., `v1beta1`) with a migration guide

## Initial Installation

Install the CRDs chart first, then the controller chart:

```bash
# 1. Install CRDs
helm install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <version> \
  --namespace temporal-system \
  --create-namespace

# 2. Install the controller
helm install temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version <version> \
  --namespace temporal-system
```

## Upgrading

Always upgrade the CRDs chart before the controller chart:

```bash
# 1. Upgrade CRDs first
helm upgrade temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <new-version> \
  --namespace temporal-system

# 2. Then upgrade the controller
helm upgrade temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version <new-version> \
  --namespace temporal-system
```

## Rollback

Roll back the controller first; CRDs can optionally be rolled back afterward (usually not needed):

```bash
# 1. Roll back the controller (CRDs remain at current version — safe per the compatibility commitment)
helm rollback temporal-worker-controller --namespace temporal-system

# 2. Optionally roll back CRDs
helm rollback temporal-worker-controller-crds --namespace temporal-system
```

## Migration Guide for Existing Users

If you are upgrading from a chart version that shipped CRDs in the `crds/` directory (v1.2.0 and earlier), follow these steps.

When upgrading to the new chart version, Helm will **not** delete the existing CRDs — they remain on the cluster untouched. The controller continues working normally. The CRDs become temporarily "orphaned" from Helm tracking, which is fine.

### One-Time Migration

```bash
# Step 1: Upgrade the main chart as usual (CRDs on the cluster are untouched)
helm upgrade temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version <new-version> \
  --namespace temporal-system

# Step 2: Install the CRDs chart to take Helm ownership of the existing CRDs
# kubectl apply reconciles any changes; same-version CRDs are a no-op on the cluster
helm install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version <new-version> \
  --namespace temporal-system
```

After this migration, follow the standard upgrade and rollback instructions above for all future releases.
