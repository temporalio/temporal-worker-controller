# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Changed

- **Build ID hashing is now stable across Kubernetes API upgrades.** Upgrading the Temporal server pulled in [`go.temporal.io/auto-scaled-workers`](https://github.com/temporalio/auto-scaled-workers) as a transitive dependency. That package depends directly on `k8s.io/client-go` and `k8s.io/apimachinery`, which caused the Kubernetes API version used by this controller to be upgraded from v0.34 to v0.35.

  Kubernetes v0.35 added new optional fields to core pod types — notably `RestartPolicyRules` on `Container` and `HostnameOverride` and `WorkloadRef` on `PodSpec`. The previous hash implementation used [spew](https://github.com/davecgh/go-spew) to serialize the pod spec, which includes every struct field regardless of whether it is set. Adding new zero-value fields to the struct therefore changed the hash output even when the actual pod spec was unchanged.

  The controller now uses `json.Marshal` to serialize pod specs before hashing. Kubernetes types use `omitempty` on all optional fields, so new zero-value fields introduced in future API versions are omitted from the JSON output and do not affect the hash.

### Added

- `CHANGELOG.md` to track notable changes and serve as a basis for release notes.

---

## Upgrade notes for the next release

**One-time Build ID rollout.** Because the hash algorithm changed, each `TemporalWorkerDeployment` will be assigned a new build ID on its first reconcile after upgrading the controller. This causes a normal, safe rollout: existing workers continue serving traffic while pods on the new build ID start up, exactly as happens during any image upgrade. No manual intervention is required, and tracking of existing builds and their Temporal routing state is not affected. After this one-time rollout, build IDs will remain stable across future Kubernetes API upgrades.
