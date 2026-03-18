# CRD Management

## Why a Separate CRDs Chart?

Helm's `crds/` directory installs CRDs on `helm install` but **silently ignores them on `helm upgrade`** and does not delete them on `helm uninstall`. This means users have no supported mechanism to upgrade CRDs when upgrading the controller chart via the standard Helm workflow.

To provide an explicit, version-tracked CRD upgrade path, the temporal-worker-controller ships CRDs as a separate Helm chart: `temporal-worker-controller-crds`. This is the same pattern used by [Karpenter](https://karpenter.sh/docs/upgrading/upgrade-guide/) (`karpenter-crd`) and [prometheus-operator-crds](https://github.com/prometheus-community/helm-charts/tree/main/charts/prometheus-operator-crds).

Benefits:
- CRDs and the controller can be upgraded and rolled back independently
- Clear versioning: both charts always use the same version number
- CRDs won't be accidentally uninstalled via `helm uninstall` of `temporal-worker-controller` chart
- CRDs can be uninstalled separately with `helm uninstall temporal-worker-controller-crds`. WARNING: Uninstalling CRDs will delete all Custom Resources in your cluster that use those CRDs.

## Compatibility Commitment

> **CRD chart version N is forward-compatible with controller chart versions N and N−1.**

- CRD changes are **additive-only** within a minor version (no field removals, no type changes)
- Rolling back the controller one minor version while keeping the current CRDs is always safe
- Upgrading CRDs ahead of the controller (within one minor version) is always safe
- Structural CRD breaking changes (if ever needed) require a new API version (e.g., `v1beta1`) with a migration guide

### What the commitment requires of each release

- All new fields must be marked optional (`+optional`, `omitempty`) — no new required fields may be added within a minor version
- No existing fields may be removed or have their types changed within a minor version
- These rules apply to both spec and status fields

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

### CRD rollback and field pruning

**Recommendation:** When a controller rollback is needed, prefer keeping CRDs at the newer version and rolling back only the controller. Per the compatibility commitment this is always safe. Only roll back CRDs if you have a specific reason and have verified no objects are using the fields being removed from the schema (see below).

#### How Kubernetes handles rolled-back CRDs

When a CRD schema changes, Kubernetes does not retroactively re-validate or re-prune existing objects. Fields not present in the current schema are silently dropped ("pruned") only when an object is next written through the API server — any UPDATE or PATCH: `kubectl apply`, a GitOps reconciliation cycle, a user editing a field, or any tooling that touches the object.

#### The specific risk

If objects on the cluster have spec fields that were added in CRD version N+1 (e.g., `spec.newFeature: enabled`), and the CRD is rolled back to N (which does not define `spec.newFeature`):

- Those objects still show the field when you `kubectl get` them — the data is still in etcd.
- On the next write to any of those objects — even an unrelated change like updating `spec.replicas` — the API server silently drops `spec.newFeature`.
- This is **permanent data loss with no error or warning**.

#### Why the controller's write patterns affect the risk

The controller never writes back to TWD spec; it only writes to the status subresource and manages child Kubernetes Deployments. This means the controller itself will not directly trigger spec field pruning. However:

- GitOps tools (Flux, ArgoCD), manual `kubectl apply`, or any tooling that writes to the TWD object will trigger pruning on its next sync cycle.
- Status: the controller fully reconstitutes status from live cluster state on every reconcile. Status fields added in N+1 will disappear after one reconcile cycle regardless of CRD version. This is expected behavior and not meaningful data loss.

#### How to determine if CRD rollback is safe

Before rolling back CRDs, check whether any `TemporalWorkerDeployment` objects on the cluster are using fields that exist in the newer CRD version but not the older one:

```bash
# Replace <field-added-in-newer-version> with the field name(s) introduced in the version you are rolling back from
kubectl get temporalworkerdeployments -A -o yaml | grep <field-added-in-newer-version>
```

If the output is empty, no objects are using those fields and rollback is safe. If output is non-empty, rolling back the CRD will cause silent data loss on the next write to those objects.

## Migration Guide for Existing Users

If you are upgrading from a chart version that shipped CRDs in the `crds/` directory (Controller Helm Chart v0.12.0 and earlier), follow these steps.

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
