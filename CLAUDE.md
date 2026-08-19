# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Kubernetes controller (kubebuilder / controller-runtime) that automates Temporal
[Worker Versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning).
One `WorkerDeployment` custom resource maps to **one Temporal Worker Deployment** and **N Kubernetes
`Deployment`s** — one per Build ID (version). The controller registers versions with Temporal, shifts
routing (current / ramping) per the rollout strategy, and scales down + deletes versioned Deployments
once Temporal reports them Drained.

Read `docs/concepts.md` then `docs/architecture.md` before making non-trivial changes; the terminology
(target vs. current vs. deprecated version, Inactive/Ramping/Current/Draining/Drained) is used
verbatim throughout the code.

## Three Go modules

| Path             | Module                                              | Notes |
|------------------|-----------------------------------------------------|-------|
| `.`              | `github.com/temporalio/temporal-worker-controller`  | controller, API types, planner |
| `internal/tests` | `.../temporal-worker-controller/tests`              | integration tests (embedded Temporal server) |
| `internal/demo`  | `.../temporal-worker-controller/internal/demo`      | sample `helloworld` worker for the local demo |

`go test ./...` from the root **does not** reach `internal/tests`. Anything touching that directory
needs a Go workspace, which is gitignored and must be created locally (CI does the same):

```bash
go work init && go work use -r .
```

`make tidy` runs `go mod tidy` in all three modules; CI fails if it produces a diff.

## Commands

```bash
make build                 # manifests + generate + fmt + vet, then build bin/manager
make run                   # run the controller against your current kubecontext

make manifests             # regenerate CRDs into helm/temporal-worker-controller-crds/templates
                           #   + RBAC into the helm chart (via hack/sync-rbac-rules.py)
make generate              # regenerate zz_generated.deepcopy.go

make test-unit             # go test ./... (provisions envtest binaries + helm chart deps first)
make test-integration      # TestIntegration in internal/tests (needs the go workspace above)
make test-all              # manifests + generate, then go test -tags test_dep ./...

make lint                  # golangci-lint + actionlint
make fmt-imports           # gci; CI fails if this leaves the tree dirty
make tidy                  # go mod tidy in every module; CI fails if dirty
```

Always run `make manifests generate` after editing anything in `api/v1alpha1/` or any
`+kubebuilder:` marker (including the RBAC markers on the reconcilers) and commit the result.

### Running a single test

```bash
# pure unit test — no setup needed
go test ./internal/planner -run TestGetVersionConfigDiff

# envtest-backed suites (internal/controller, api/v1alpha1) silently SKIP without this:
make envtest
KUBEBUILDER_ASSETS=$(./bin/setup-envtest use 1.29.0 --bin-dir ./bin -p path) \
  go test ./internal/controller -run TestFoo

# one integration case (requires go.work + KUBEBUILDER_ASSETS)
go test -v -tags test_dep ./internal/tests/internal -run 'TestIntegration/manual-rollout-expect-no-change'
```

The webhook suite in `api/v1alpha1` shells out to `helm template`, so it needs the chart dependencies
fetched (`make helm-dependency-build`, which `make test-unit` does for you) or `HELM=<path>` set.

Integration tests boot a real Temporal server in-process (`temporaltest`) plus envtest, and fake the
kubelet: `internal/tests/internal/deployment_controller.go` starts real SDK workers for the Deployments
the controller creates so that versions actually register and drain. Test cases are declared with the
fluent builders in `internal/testhelpers`.

### Local demo

minikube + skaffold against a real Temporal Cloud namespace — see `internal/demo/README.md`.
`WORKER_VERSION` is required on every `skaffold run --profile helloworld-worker`.

## Architecture: the reconcile pipeline

`Reconcile` (`internal/controller/worker_controller.go`) is a strict **observe → status → plan → execute**
loop, requeued every 10s:

1. **Observe.** `k8s.GetDeploymentState` (child Deployments indexed by the `temporal.io/build-id` label)
   and `temporal.GetWorkerDeploymentState` (`DescribeWorkerDeployment` → routing config + version
   summaries) produce the two halves of observed state.
2. **Status.** `genstatus.go` + `state_mapper.go` merge those into `WorkerDeploymentStatus`
   (target / current / deprecated versions). Status is written **once** at the end of the loop.
3. **Plan.** `genplan.go` resolves side inputs (gate input from ConfigMap/Secret, the list of
   `WorkerResourceTemplate`s) and calls `planner.GeneratePlan`.
4. **Execute.** `execplan.go` is the **only** place that mutates Kubernetes or calls Temporal write APIs.

**`internal/planner/planner.go` is pure** — it takes observed state plus spec and returns a `Plan`
struct, with no clients and no I/O. All rollout decisions live there (`getVersionConfigDiff`,
`handleProgressiveRollout`, `isRollbackScenario`, `getScaleDeployments`, `getDeleteDeployments`,
`shouldCreateDeployment`). Keep it that way: new decision logic goes in the planner with a table test
in `planner_test.go`; new I/O goes in `genplan.go` (reads) or `execplan.go` (writes).

## Invariants that are easy to break

- **Build ID** is derived, never user-set in the normal path: `k8s.ComputeBuildID` = image tag prefix +
  hash of `spec.template`. Any pod template change ⇒ new Build ID ⇒ new versioned Deployment. Setting
  `workerOptions.unsafeCustomBuildID` pins it, and drift is then detected via the
  `temporal.io/pod-template-spec-hash` annotation and applied as a rolling update instead.
- **Temporal worker deployment name** is always `<k8s namespace>/<WorkerDeployment name>`
  (`k8s.ComputeWorkerDeploymentName`) and is deliberately not configurable. Versioned Deployment names
  are capped at 47 chars with a hash suffix (`ComputeVersionedDeploymentName`).
- **ManagerIdentity**: the controller claims `ManagerIdentity` on the Temporal Worker Deployment before
  its first routing change, and the server then rejects routing writes from anyone else. A human taking
  manual control clears/steals it and the controller backs off. See `docs/manager-identity.md`.
- **Deletion order matters** (`handleDeletion`): clear ramping → set current to unversioned → delete k8s
  Deployments → `DeleteVersion(SkipDrainage)` for each version → delete the deployment record. Pollers
  linger server-side for `matching.PollerHistoryTTL`, so failures here are expected and retried.
- **Finalizers**: `temporal.io/delete-protection` sits on both `WorkerDeployment` (server-side cleanup)
  and the referenced `Connection` (so it outlives the WDs using it). `temporal.io/migration-guard` is
  owned solely by the deprecated reconcilers.
- **`WorkerResourceTemplate` copies are owned by the WRT, not the Deployment**, so Kubernetes GC will not
  remove them when a version is sunset — the controller deletes them explicitly and only prunes the WRT
  status entry after a confirmed delete. Applies are Server-Side Apply, skipped when the rendered hash
  matches `status.versions[].lastAppliedHash`.
- **Deprecated CRDs**: `TemporalWorkerDeployment` / `TemporalConnection` were renamed to
  `WorkerDeployment` / `Connection`. The deprecated reconcilers (`deprecated_*.go`) must **never** call
  Temporal APIs or manage Deployments — they would race the real reconciler over the same Temporal
  deployment name. Whether they run at all is decided at startup by `DetectDeprecatedCRDWatches`.
- The controller requires `CONTROLLER_IDENTITY` in the environment; `main.go` appends the namespace UID
  as `CONTROLLER_IDENTITY_SUFFIX`, and `Reconcile` errors out if the combined identity is empty.

## Generated / synced files — do not hand-edit

- `helm/temporal-worker-controller-crds/templates/*.yaml` — from `make manifests`
- the `# GENERATED RULES` blocks in `helm/temporal-worker-controller/templates/rbac.yaml` — from
  `hack/sync-rbac-rules.py`, sourced from the `+kubebuilder:rbac` markers via a scratch `config/` dir
  (gitignored)
- `api/v1alpha1/zz_generated.deepcopy.go` — from `make generate`
- `CHANGELOG.md` — regenerated by the `update-changelog` GitHub Action on release

## Conventions and lint gotchas

- `golangci-lint` runs with `revive` **enable-all-rules** (cyclomatic and cognitive complexity capped at
  25) plus `errcheck`, `staticcheck`, `exhaustive`, `godox`, `forbidigo`, `importas`. Locally `make
  lint-code` only checks the diff vs. `main` and auto-fixes; CI runs it with `--fix=false`.
- `godox` treats **`FIXME` as a build failure** (`TODO` is fine). `forbidigo` bans `panic` in non-test
  code and `time.Sleep` outside tests. `importas` enforces `...pb` / `...spb` aliases for Temporal
  protobuf packages.
- Most files under `api/`, `internal/`, `cmd/` open with the two-line MIT/Datadog copyright header;
  match the neighbouring files.
- Event reason strings in `internal/controller/util.go` are explicitly **not** API — they may change.
  CRD `status.conditions` (`Ready`, `Progressing`) are the stable surface.
- Two independent release trains: application (`vX.Y.Z`, the controller image) and chart
  (`helm-vX.Y.Z`, where the major version tracks the CRD APIVersion). See `docs/release.md`.
