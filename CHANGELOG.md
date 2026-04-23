# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Changed

- **Build ID hashing is now stable across Kubernetes API upgrades.** The controller previously used [spew](https://github.com/davecgh/go-spew) to serialize pod specs before hashing them. Spew serializes every struct field, including nil/zero-value fields, so when Kubernetes added new optional fields in v0.35 the serialized output changed and the computed build IDs changed with it. The controller now uses JSON marshaling, which respects `omitempty` tags and omits zero-value fields, so future Kubernetes API upgrades that only add optional fields will not affect build IDs.

  **One-time rollout on upgrade:** because the hash algorithm changed, the controller will compute a new build ID for each `TemporalWorkerDeployment` on its first reconcile after this upgrade. This triggers a normal, safe rollout — existing workers keep serving traffic while the new version starts up, exactly as they would during any image upgrade. No manual intervention is required. After this one-time rollout, build IDs will remain stable across future Kubernetes API upgrades.