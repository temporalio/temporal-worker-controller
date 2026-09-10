# CD Rollouts with the Temporal Worker Controller

This guide describes patterns for integrating the Temporal Worker Controller into a CD pipeline, intended as guidance once you are already using Worker Versioning in steady state.

> **Note:** The examples below illustrate common integration patterns but are not guaranteed to work verbatim with every version of each tool. API fields, configuration keys, and default behaviors change between releases. Always verify against the documentation for the specific tool you are using.

For migration help, see [migration-to-versioned.md](migration-to-versioned.md).

## Understanding the conditions

The `WorkerDeployment` resource exposes four standard conditions on `status.conditions` that CD tools and scripts can consume, in two pairs.

`Ready` and `Progressing` describe the rollout in the controller's own terms. They are the ones to read in a script, a dashboard, or `kubectl describe`, and their `reason` fields carry the detail.

`Stalled` and `Reconciling` say the same thing in the vocabulary [kstatus](https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus) understands — the library behind Helm 4 `--wait` and Flux health assessment. They follow kstatus's "abnormal-true" convention: each is present and `True` only while something unusual is happening, and absent otherwise. You rarely need to read them yourself; they exist so those tools reach the right verdict without a custom health check.

### `Ready`

`Ready=True` means the controller successfully reached Temporal **and** the target version is the current version in Temporal. This is the primary signal that a rollout has finished and the worker is fully operational.

`Ready=True` with reason `RolloutComplete` when the rollout has finished.

`Ready=False` while either condition is not met. The `reason` field tells you why:

| Reason | Meaning |
|---|---|
| `WaitingForPollers` | Target version's Deployment exists but workers haven't registered with Temporal yet |
| `WaitingForPromotion` | Workers are registered (Inactive) but not yet promoted to Current |
| `Ramping` | Progressive strategy is ramping traffic to the new version |
| Error reasons (see Progressing below) | A blocking error is preventing progress |

### `Progressing`

`Progressing=True` means a rollout is actively in-flight and the controller is making forward progress. `Progressing=False` means either the rollout is done (`Ready=True`) or a blocking error is preventing progress.

When `Progressing=False` due to an error, the `reason` field identifies what went wrong:

| Reason | Meaning |
|---|---|
| `RolloutComplete` | Not an error — the rollout finished successfully |
| `ConnectionNotFound` | The referenced `Connection` resource doesn't exist |
| `AuthSecretInvalid` | The credential secret is missing, malformed, or has an expired certificate |
| `TemporalClientCreationFailed` | The controller can't reach the Temporal server (dial/health-check failure) |
| `TemporalStateFetchFailed` | The controller reached the server but can't read the worker deployment state |
| `PlanGenerationFailed` | Internal error generating the reconciliation plan |
| `PlanExecutionFailed` | Internal error executing the plan (e.g., a Kubernetes API call failed) |

Once the underlying problem is fixed, the next successful reconcile will restore `Progressing` and `Ready` to the correct state.

### `Stalled` and `Reconciling`

`Reconciling=True` means the controller is still working toward the spec; kstatus-based tools report the resource as **in progress** and keep waiting. `Stalled=True` means reconciliation cannot proceed and waiting will not help; those tools report **failed** and stop. Both are absent once a rollout is complete, and only one is ever set at a time.

`Stalled` is set only for failures that are decidable from information already in hand, where nothing arriving later could change the answer:

| Reason | Condition set | Why |
|---|---|---|
| `InvalidSpec` | `Stalled` | Settled by the spec you just applied |
| `ClusterConnectionUnsupported` | `Stalled` | Settled by the spec plus how the controller was deployed |
| `ConnectionNotFound` | `Reconciling` | Waiting on another object — the `Connection` may not exist *yet* |
| `AuthSecretInvalid` | `Reconciling` | Same, and this reason also covers a credential Secret that is simply absent |
| `TemporalClientCreationFailed` | `Reconciling` | Server unreachable; retried |
| `TemporalStateFetchFailed` | `Reconciling` | Includes rate limiting; retried |
| `PlanGenerationFailed`, `PlanExecutionFailed` | `Reconciling` | Retried with backoff |

The reason a missing `Connection` is not treated as terminal is ordering. Applying a `WorkerDeployment` alongside its `Connection` and credentials in one release gives no guarantee about which lands first, so a missing reference is frequently a normal gap of a few seconds rather than a mistake. Failing a deploy that was about to succeed is worse than waiting.

The trade-off is that a genuinely wrong `connectionRef` — a typo, or a `Connection` that was never created — keeps reporting *in progress* until your tool's timeout expires rather than failing immediately. Set timeouts you are willing to wait out, and read the `reason` on `Ready`/`Progressing` (or the resource's Kubernetes Events) to see what is actually blocking.

`WorkerResourceTemplate` follows the same pattern: a template that cannot render, or that the API server rejects outright, sets `Stalled`; one waiting for its `WorkerDeployment` to appear, or retrying a transient apply failure, sets `Reconciling`.

### `Connection` and `ClusterConnection`

`Connection` and `ClusterConnection` are configuration-only resources. They have no controller of their own and expose no conditions, so tools that assess health from conditions — Helm `--wait`, Flux, and anything else built on [kstatus](https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus) — treat them as healthy as soon as they exist. This is intentional: there is no reconcile loop behind them and therefore nothing to wait for. Kubernetes treats `ConfigMap` and `Secret` the same way.

A broken connection is still reported, just on the `WorkerDeployment` that references it rather than on the connection itself — see the `ConnectionNotFound` and `AuthSecretInvalid` reasons above. Gate your rollouts on the `WorkerDeployment`; waiting on a `Connection` tells you only that the object was accepted by the API server, not that the credentials in it work.

## Triggering a rollout

A rollout starts when you change the pod template in your `WorkerDeployment` spec — a changed pod spec produces a new Build ID, which the controller treats as a new version to roll out.

With Helm (image tag update):

```yaml
# values.yaml
image:
  repository: my-registry/my-worker
  tag: "v2.3.0"
```

```bash
helm upgrade my-worker ./chart --values values.yaml
```

With a plain manifest:

```yaml
# workerdeployment.yaml
spec:
  template:
    spec:
      containers:
        - name: worker
          image: my-registry/my-worker:v2.3.0
```

```bash
kubectl apply -f workerdeployment.yaml
```

The controller picks up the change on the next reconcile loop (within seconds) and begins the rollout.

> **Rollback via image tag:** If the image tag you set matches a version that was current within the last hour, the controller treats it as a rollback rather than a normal rollout: it routes 100% of traffic back to that version immediately (AllAtOnce), regardless of the configured rollout strategy. So a `helm rollback` will trigger an immediate rollback rather than a gradual re-rollout. See [Rollback Strategy](configuration.md#rollback-strategy) for details.

## kubectl

`kubectl wait` can block a pipeline script until `Ready=True`:

```bash
kubectl apply -f workerdeployment.yaml
kubectl wait workerdeployment/my-worker \
  --for=condition=Ready \
  --timeout=10m
```

Set `--timeout` to exceed the longest expected rollout time — for progressive strategies this is the sum of all `pauseDuration` values plus the time for workers to start and register. `kubectl wait` exits non-zero on timeout, which you can use to fail the pipeline.

## Helm

### Helm 4

Helm 4 uses [kstatus](https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus) for its `--wait` implementation ([HIP-0022](https://helm.sh/community/hips/hip-0022/)). kstatus understands the standard Kubernetes conditions contract and should block until `Ready=True` on your `WorkerDeployment`. Because the controller also emits `Stalled` (see above), a rollout blocked by an invalid spec fails the release immediately instead of waiting out the timeout:

```bash
helm upgrade my-worker ./chart --values values.yaml --wait --timeout 10m
```

> **Verify:** Check your Helm 4 release notes — kstatus behavior and the `--wait` flag semantics have evolved across point releases.

### Helm 3

Helm 3's `--wait` only covers a hardcoded set of native resource types (Deployments, StatefulSets, DaemonSets, Jobs, Pods) and does not inspect conditions on custom resources. A separate `kubectl wait` step is one approach:

```bash
helm upgrade my-worker ./chart --values values.yaml
kubectl wait workerdeployment/my-worker \
  --for=condition=Ready \
  --timeout=10m \
  --namespace my-namespace
```

## ArgoCD

ArgoCD does not have a generic fallback that automatically checks `status.conditions` on unknown CRD types. For any resource whose group (`temporal.io`) is not in ArgoCD's built-in health check registry, ArgoCD silently skips that resource when computing application health. A [custom Lua health check](https://argo-cd.readthedocs.io/en/stable/operator-manual/health/) is the standard mechanism for teaching ArgoCD how to assess a CRD's health.

The standard conditions keep the Lua simple — it only needs to read condition types and statuses, not any controller-specific status fields. Reading `Stalled` and `Reconciling` rather than `Progressing` also makes ArgoCD agree with Helm and Flux about what counts as a failure, instead of showing **Degraded** for a `Connection` that is a second away from existing. The following script is a starting point; adapt it to your ArgoCD version and any site-specific requirements:

```yaml
# In your argocd-cm ConfigMap
data:
  resource.customizations.health.temporal.io_WorkerDeployment: |
    local ready = nil
    local stalled = nil
    local reconciling = nil
    if obj.status ~= nil and obj.status.conditions ~= nil then
      for _, c in ipairs(obj.status.conditions) do
        if c.type == "Ready" then ready = c end
        if c.type == "Stalled" then stalled = c end
        if c.type == "Reconciling" then reconciling = c end
      end
    end
    -- Check Stalled first: it is the only condition that means waiting will not help.
    if stalled ~= nil and stalled.status == "True" then
      return {status = "Degraded", message = stalled.message}
    end
    if ready ~= nil and ready.status == "True" then
      return {status = "Healthy", message = ready.message}
    end
    if reconciling ~= nil and reconciling.status == "True" then
      return {status = "Progressing", message = reconciling.message}
    end
    return {status = "Progressing", message = "Waiting for conditions"}
```

With a health check like this in place:

- ArgoCD shows **Degraded** when reconciliation is stalled (`Stalled=True`) — an invalid spec, or a connection kind this controller cannot read.
- ArgoCD shows **Healthy** once `Ready=True`.
- ArgoCD shows **Progressing** while a rollout is in-flight, and also while the controller is retrying a recoverable problem such as a `Connection` that does not exist yet. Read the `reason` on `Ready` to tell those apart.

The same script works for `WorkerResourceTemplate` — register it under `resource.customizations.health.temporal.io_WorkerResourceTemplate` as well, since it emits the same three conditions.

If you use [sync waves](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-waves/) and workers must be fully rolled out before a dependent service is updated, place the `WorkerDeployment` in an earlier wave.

> **Verify:** ArgoCD's health customization API and Lua runtime have changed across versions. Test your health check script in a non-production environment before relying on it to gate sync waves.

## Flux

### Kustomization

Flux's `Kustomization` controller uses kstatus to assess resource health. Because `WorkerDeployment` emits the standard `Ready`, `Reconciling`, and `Stalled` conditions, Flux should treat it as healthy when `Ready=True`, keep waiting while `Reconciling=True`, and fail the health check rather than wait out the timeout when `Stalled=True`. Adding an explicit `healthChecks` entry makes the dependency visible and ensures Flux waits on the `WorkerDeployment` before marking the Kustomization as ready:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-workers
  namespace: flux-system
spec:
  interval: 5m
  path: ./workers
  prune: true
  sourceRef:
    kind: GitRepository
    name: my-repo
  healthChecks:
    - apiVersion: temporal.io/v1alpha1
      kind: WorkerDeployment
      name: my-worker
      namespace: my-namespace
  timeout: 10m
```

Set `timeout` to exceed the longest expected rollout duration.

### HelmRelease

Flux's `helm-controller` uses kstatus by default for post-install/post-upgrade health assessment, so a `HelmRelease` deploying your worker chart should automatically wait for `Ready=True` on any `WorkerDeployment` resources in the release:

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: my-worker
  namespace: flux-system
spec:
  interval: 5m
  timeout: 10m   # should exceed the longest expected rollout
  chart:
    spec:
      chart: ./chart
      sourceRef:
        kind: GitRepository
        name: my-repo
```

> **Verify:** kstatus integration details and the `healthChecks` API have evolved across Flux releases. Check the Flux documentation for your version.
