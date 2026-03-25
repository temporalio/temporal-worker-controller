# CD Rollouts with the Temporal Worker Controller

This guide describes patterns for integrating the Temporal Worker Controller into a CD pipeline, intended as guidance once you are already using Worker Versioning in steady state.

> **Note:** The examples below illustrate common integration patterns but are not guaranteed to work verbatim with every version of each tool. API fields, configuration keys, and default behaviors change between releases. Always verify against the documentation for the specific tool you are using.

For migration help, see [migration-to-versioned.md](migration-to-versioned.md).

## Understanding the conditions

The `TemporalWorkerDeployment` resource exposes two standard conditions on `status.conditions` that CD tools and scripts can consume.

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
| `TemporalConnectionNotFound` | The referenced `TemporalConnection` resource doesn't exist |
| `AuthSecretInvalid` | The credential secret is missing, malformed, or has an expired certificate |
| `TemporalClientCreationFailed` | The controller can't reach the Temporal server (dial/health-check failure) |
| `TemporalStateFetchFailed` | The controller reached the server but can't read the worker deployment state |
| `PlanGenerationFailed` | Internal error generating the reconciliation plan |
| `PlanExecutionFailed` | Internal error executing the plan (e.g., a Kubernetes API call failed) |

Once the underlying problem is fixed, the next successful reconcile will restore `Progressing` and `Ready` to the correct state.

## Triggering a rollout

A rollout starts when you change the pod template in your `TemporalWorkerDeployment` spec — a changed pod spec produces a new Build ID, which the controller treats as a new version to roll out.

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
# twd.yaml
spec:
  template:
    spec:
      containers:
        - name: worker
          image: my-registry/my-worker:v2.3.0
```

```bash
kubectl apply -f twd.yaml
```

The controller picks up the change on the next reconcile loop (within seconds) and begins the rollout.

## kubectl

`kubectl wait` can block a pipeline script until `Ready=True`:

```bash
kubectl apply -f twd.yaml
kubectl wait temporalworkerdeployment/my-worker \
  --for=condition=Ready \
  --timeout=10m
```

Set `--timeout` to exceed the longest expected rollout time — for progressive strategies this is the sum of all `pauseDuration` values plus the time for workers to start and register. `kubectl wait` exits non-zero on timeout, which you can use to fail the pipeline.

## Helm

### Helm 4

Helm 4 uses [kstatus](https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus) for its `--wait` implementation ([HIP-0022](https://helm.sh/community/hips/hip-0022/)). kstatus understands the standard Kubernetes conditions contract and should block until `Ready=True` on your `TemporalWorkerDeployment`:

```bash
helm upgrade my-worker ./chart --values values.yaml --wait --timeout 10m
```

> **Verify:** Check your Helm 4 release notes — kstatus behavior and the `--wait` flag semantics have evolved across point releases.

### Helm 3

Helm 3's `--wait` only covers a hardcoded set of native resource types (Deployments, StatefulSets, DaemonSets, Jobs, Pods) and does not inspect conditions on custom resources. A separate `kubectl wait` step is one approach:

```bash
helm upgrade my-worker ./chart --values values.yaml
kubectl wait temporalworkerdeployment/my-worker \
  --for=condition=Ready \
  --timeout=10m \
  --namespace my-namespace
```

## ArgoCD

ArgoCD does not have a generic fallback that automatically checks `status.conditions` on unknown CRD types. For any resource whose group (`temporal.io`) is not in ArgoCD's built-in health check registry, ArgoCD silently skips that resource when computing application health. A [custom Lua health check](https://argo-cd.readthedocs.io/en/stable/operator-manual/health/) is the standard mechanism for teaching ArgoCD how to assess a CRD's health.

The two standard conditions (`Ready`, `Progressing`) keep the Lua simple — it only needs to read the condition type and status, not any controller-specific status fields. The following script is a starting point; adapt it to your ArgoCD version and any site-specific requirements:

```yaml
# In your argocd-cm ConfigMap
data:
  resource.customizations.health.temporal.io_TemporalWorkerDeployment: |
    local ready = nil
    local progressing = nil
    if obj.status ~= nil and obj.status.conditions ~= nil then
      for _, c in ipairs(obj.status.conditions) do
        if c.type == "Ready" then ready = c end
        if c.type == "Progressing" then progressing = c end
      end
    end
    if ready ~= nil and ready.status == "True" then
      return {status = "Healthy", message = ready.message}
    end
    if progressing ~= nil then
      if progressing.status == "True" then
        return {status = "Progressing", message = progressing.message}
      else
        return {status = "Degraded", message = progressing.message}
      end
    end
    return {status = "Progressing", message = "Waiting for conditions"}
```

With a health check like this in place:

- ArgoCD shows **Healthy** once `Ready=True`.
- ArgoCD shows **Progressing** while a rollout is in-flight (`Progressing=True`).
- ArgoCD shows **Degraded** when progress is blocked (`Progressing=False` with an error reason).

If you use [sync waves](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-waves/) and workers must be fully rolled out before a dependent service is updated, place the `TemporalWorkerDeployment` in an earlier wave.

> **Verify:** ArgoCD's health customization API and Lua runtime have changed across versions. Test your health check script in a non-production environment before relying on it to gate sync waves.

## Flux

### Kustomization

Flux's `Kustomization` controller uses kstatus to assess resource health. Because `TemporalWorkerDeployment` emits a standard `Ready` condition, Flux should treat it as healthy when `Ready=True`. Adding an explicit `healthChecks` entry makes the dependency visible and ensures Flux waits on the `TemporalWorkerDeployment` before marking the Kustomization as ready:

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
      kind: TemporalWorkerDeployment
      name: my-worker
      namespace: my-namespace
  timeout: 10m
```

Set `timeout` to exceed the longest expected rollout duration.

### HelmRelease

Flux's `helm-controller` uses kstatus by default for post-install/post-upgrade health assessment, so a `HelmRelease` deploying your worker chart should automatically wait for `Ready=True` on any `TemporalWorkerDeployment` resources in the release:

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
