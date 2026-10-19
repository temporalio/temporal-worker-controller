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

The controller never writes back to `WorkerDeployment` spec; it only writes to the status subresource and manages child Kubernetes Deployments. This means the controller itself will not directly trigger spec field pruning. However:

- GitOps tools (Flux, ArgoCD), manual `kubectl apply`, or any tooling that writes to the WorkerDeployment object will trigger pruning on its next sync cycle.
- Status: the controller fully reconstitutes status from live cluster state on every reconcile. Status fields added in N+1 will disappear after one reconcile cycle regardless of CRD version. This is expected behavior and not meaningful data loss.

#### How to determine if CRD rollback is safe

Before rolling back CRDs, check whether any `WorkerDeployment` objects on the cluster are using fields that exist in the newer CRD version but not the older one:

```bash
# Replace <field-added-in-newer-version> with the field name(s) introduced in the version you are rolling back from
kubectl get workerdeployments -A -o yaml | grep <field-added-in-newer-version>
```

If the output is empty, no objects are using those fields and rollback is safe. If output is non-empty, rolling back the CRD will cause silent data loss on the next write to those objects.

## Migration Guide for Existing Users

### Migrating from the old `crds/` directory

Controller chart v0.12.0 and earlier installed CRDs from its `crds/` directory. Helm leaves those CRDs on the cluster during a controller upgrade but does not update them. Check the live CRDs and their ownership if the separate CRDs release is missing; do not infer ownership from the controller version alone.

For a v0.12.0-or-earlier installation, first bring both charts to v0.22.0, the first published matching controller and CRDs chart version. Do not upgrade the controller straight to a later target while its old CRDs remain. The v0.22.0 CRD chart retains both old kinds and adds optional status fields compared with v0.12.0. Installing it before upgrading the controller briefly pairs v0.22.0 CRDs with an older controller, outside the N/N−1 compatibility commitment. Test this handoff on a representative nonproduction cluster before using it in production; compare schemas first if the installed CRDs differ from v0.12.0.

#### One-time adoption at v0.22.0

Save the controller release's values and manifest, and inventory any `TemporalWorkerDeployment` and `TemporalConnection` objects. Confirm that the existing CRDs have no conflicting Helm ownership. Do not delete them: deleting a CRD deletes its custom resources.

```bash
# Step 1: Stamp Helm ownership labels/annotations onto the existing CRDs.
# CRDs installed via the old `crds/` directory have no Helm tracking metadata.
# Without this step, `helm install` in step 2 fails with "cannot be imported into
# the current release: invalid ownership metadata".
kubectl label crd \
  temporalconnections.temporal.io \
  temporalworkerdeployments.temporal.io \
  app.kubernetes.io/managed-by=Helm --overwrite

kubectl annotate crd \
  temporalconnections.temporal.io \
  temporalworkerdeployments.temporal.io \
  meta.helm.sh/release-name=temporal-worker-controller-crds \
  meta.helm.sh/release-namespace=temporal-system --overwrite

# Step 2: Adopt and update the CRDs before upgrading the controller.
helm install temporal-worker-controller-crds \
  oci://docker.io/temporalio/temporal-worker-controller-crds \
  --version 0.22.0 \
  --namespace temporal-system

# Step 3: After checking the CRDs and existing objects, upgrade the controller.
helm upgrade temporal-worker-controller \
  oci://docker.io/temporalio/temporal-worker-controller \
  --version 0.22.0 \
  --namespace temporal-system
```

Verify both chart versions, controller readiness, and existing custom resources before continuing. For later versions, follow the standard CRDs-first upgrade order, one published chart version at a time. Complete the [CRD rename migration](migration-crd-rename.md) when crossing v0.26.0.

### Migrating from TemporalWorkerDeployment/TemporalConnection CRDs (Chart Version v0.25.0 and earlier)

See the [CRD rename migration guide](migration-crd-rename.md) for step-by-step instructions to migrate from the old CRD names (`TemporalWorkerDeployment`, `TemporalConnection`) to the new names (`WorkerDeployment`, `Connection`) introduced in Chart Version v0.26.0 (App Version v1.7.0).
