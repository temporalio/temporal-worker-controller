## What's Changed

#### Upgrade Note
> **Migration required.** v1.7.0 renames the two primary CRDs: `TemporalWorkerDeployment` → `WorkerDeployment` and `TemporalConnection` → `Connection`. Existing resources are not reconciled until migrated. A zero-downtime migration path is available — see [docs/migration-crd-rename.md](docs/migration-crd-rename.md). If you need to roll back after upgrading, see [docs/migration-crd-rename-downgrade.md](docs/migration-crd-rename-downgrade.md).

---

- **CRD rename: `TemporalWorkerDeployment` → `WorkerDeployment`, `TemporalConnection` → `Connection`** ([#294](https://github.com/temporalio/temporal-worker-controller/pull/294)): The `Temporal` prefix was redundant given the `temporal.io` API group. This is the last breaking change before GA. The deprecated CRD kinds remain installed with migration-guard finalizers and status conditions to guide migration.
- **Cluster UID in `CONTROLLER_IDENTITY`** ([#309](https://github.com/temporalio/temporal-worker-controller/pull/309)): Completes the two-release migration started in v1.6.0. The controller identity now includes the cluster namespace UID (`{identity}/{namespaceUID}`), preventing cross-cluster conflicts when two controllers share the same base identity. Existing Worker Deployments are reclaimed transparently on upgrade.
- **Fix CEL rule to actually block deprecated resource create** ([#313](https://github.com/temporalio/temporal-worker-controller/pull/313)): The deprecation CEL rule was silently skipped on create (not just update). Fixed with `optionalOldSelf: true` and `hasValue()`.
- **Downgrade guide for CRD rename migration** ([#312](https://github.com/temporalio/temporal-worker-controller/pull/312)): New doc covering rollback procedures for both pre- and post-migration scenarios.

**Full Changelog**: https://github.com/temporalio/temporal-worker-controller/compare/v1.6.0...v1.7.0
