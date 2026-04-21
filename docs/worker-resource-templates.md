# WorkerResourceTemplate

`WorkerResourceTemplate` lets you attach arbitrary Kubernetes resources — HPAs, PodDisruptionBudgets, custom CRDs — to each worker version that has running workers. The controller creates one copy of the resource per worker version with a running Deployment, automatically wired to the correct versioned Deployment.

## Why you need this

The Temporal Worker Controller creates one Kubernetes `Deployment` per worker version (Build ID). If you attach an HPA directly to a single Deployment, it breaks as versions roll over — the old HPA still targets the old Deployment, the new Deployment has no HPA, and you have to manage cleanup yourself.

`WorkerResourceTemplate` solves this by treating the resource as a template. The controller renders one instance per worker version with running workers, injects the correct versioned Kubernetes Deployment name, injects per-version metrics tags where appropriate, and cleans up automatically when the versioned Deployment is deleted (e.g., during the sunset process after traffic has drained).

This is also the recommended mechanism for metric-based or backlog-based autoscaling: attach a standard HPA with custom metrics to your workers and the controller keeps one per running worker version, each pointing at the right Deployment and version-tagged metrics.

## How it works

1. You create a `WorkerResourceTemplate` that references a `TemporalWorkerDeployment` and contains the resource spec in `spec.template`.
2. The validating webhook checks that you have permission to manage that resource type yourself (SubjectAccessReview), and that the resource kind is on the allowed list (see below).
3. On each reconcile loop, the controller renders one copy of `spec.template` per worker version with a running Deployment, injects fields (see below), and applies it via Server-Side Apply.
4. Each per-version copy is deleted by the controller when its corresponding Kubernetes Deployment is deleted per the sunset policy.
5. `WorkerResourceTemplate.status.versions` is updated with the applied/failed status for each version.

## Auto-injection

The controller auto-injects two fields when you set them to `{}` (empty object) in `spec.template`. `{}` is the explicit opt-in sentinel:
- If you omit the field entirely, nothing is injected.
- If you set a non-empty value, the webhook rejects the `WorkerResourceTemplate` because the controller owns these fields.

| Field | Scope | Injected value |
|-------|-------|---------------|
| `scaleTargetRef` | Anywhere in `spec` (recursive) | `{apiVersion: apps/v1, kind: Deployment, name: <versioned-deployment-name>}` |
| `spec.selector.matchLabels` | Only at this exact path | `{temporal.io/build-id: <buildID>, temporal.io/deployment-name: <twdName>}` |
| `spec.metrics[*].external.metric.selector.matchLabels` | Each External metric entry where `matchLabels` is present | `{temporal_worker_deployment_name: <ns>_<twd-name>, temporal_worker_build_id: <buildID>, temporal_namespace: <temporal-ns>}` |

`scaleTargetRef` injection is recursive and covers HPAs, WPAs, and other autoscaler CRDs.

`spec.selector.matchLabels` uses `{}` as the opt-in sentinel — absent means no injection; `{}` means inject pod selector labels.

`spec.metrics[*].external.metric.selector.matchLabels` appends the three Temporal metric identity labels (`temporal_worker_deployment_name`, `temporal_worker_build_id`, `temporal_namespace`) to any External metric selector where the `matchLabels` key is present (including `{}`). User labels like `task_type: "Activity"` coexist — the controller merges its keys alongside whatever you provide. If `matchLabels` is absent on a metric entry, no injection occurs for that entry.

The webhook rejects any template that hardcodes `temporal_worker_deployment_name`, `temporal_worker_build_id`, or `temporal_namespace` in a metric selector — these are always controller-owned.

## Token substitution

For CRDs that don't have a structured `matchLabels` field (e.g. KEDA's `ScaledObject` Prometheus trigger, which takes a freeform PromQL `query` string), the controller performs token substitution over every string-valued leaf in `spec.template` at render time. The three tokens are:

| Token | Substituted value |
|-------|-------------------|
| `__TEMPORAL_WORKER_DEPLOYMENT_NAME__` | `<wrt-namespace>_<twdName>` |
| `__TEMPORAL_WORKER_BUILD_ID__` | the active Build ID |
| `__TEMPORAL_NAMESPACE__` | the Temporal namespace |

Tokens are opt-in: strings without any token are untouched. Unknown `__FOO__`-style tokens pass through unchanged — only the three tokens above are recognised.

See [examples/wrt-keda-prometheus.yaml](../examples/wrt-keda-prometheus.yaml) for a full KEDA example.

## Resource naming

Each per-Build-ID copy is given a unique, DNS-safe name derived from the `(twdName, wrtName, buildID)` triple. Names are capped at 47 characters to be safe for all Kubernetes resource types, including Deployment (which has pod-naming constraints that effectively limit Deployment names to ~47 characters). The name always ends with an 8-character hash of the full triple, so uniqueness is guaranteed even when the human-readable prefix is truncated.

Use `kubectl get <kind>` after a reconcile to see the created resources and their names.

## Allowed resource kinds and RBAC

`workerResourceTemplate.allowedResources` in Helm values serves two purposes at once: it defines which resource kinds the webhook will accept, and it drives the RBAC rules granted to the controller's `ClusterRole`. Only kinds listed here can be embedded in a `WorkerResourceTemplate`.

The default allows HPAs:

```yaml
workerResourceTemplate:
  allowedResources:
    - kinds: ["HorizontalPodAutoscaler"]
      apiGroups: ["autoscaling"]
      resources: ["horizontalpodautoscalers"]
```

To also allow PodDisruptionBudgets, add an entry:

```yaml
workerResourceTemplate:
  allowedResources:
    - kinds: ["HorizontalPodAutoscaler"]
      apiGroups: ["autoscaling"]
      resources: ["horizontalpodautoscalers"]
    - kinds: ["PodDisruptionBudget"]
      apiGroups: ["policy"]
      resources: ["poddisruptionbudgets"]
```

To also allow KEDA ScaledObjects, add an entry with the `keda.sh` API group:

```yaml
workerResourceTemplate:
  allowedResources:
    - kinds: ["HorizontalPodAutoscaler"]
      apiGroups: ["autoscaling"]
      resources: ["horizontalpodautoscalers"]
    - kinds: ["ScaledObject"]
      apiGroups: ["keda.sh"]
      resources: ["scaledobjects"]
```

Requires KEDA CRDs installed in the cluster. Users who create `WorkerResourceTemplate`s with `ScaledObject` need RBAC permission to manage `scaledobjects.keda.sh` in their namespace directly — the webhook's SubjectAccessReview rejects the request otherwise.

Each entry has three fields:
- `kinds` — kind names the webhook accepts (case-insensitive)
- `apiGroups` — API groups used to generate the controller's RBAC rules
- `resources` — resource names used to generate the controller's RBAC rules

### What the webhook checks

When you create or update a `WorkerResourceTemplate`, the webhook performs SubjectAccessReviews to verify:

1. **You** (the requesting user) can create/update the embedded resource type in that namespace.
2. **The controller's service account** can create/update the embedded resource type in that namespace.

If either check fails, the request is rejected. This prevents privilege escalation — you cannot use `WorkerResourceTemplate` to create resources you don't already have permission to create yourself.

Users who create `WorkerResourceTemplates` need RBAC permission to manage the embedded resource type directly. For example, to let a team create `WorkerResourceTemplates` that embed HPAs, they need the standard `autoscaling` permissions in their namespace.

## Webhook TLS

The `WorkerResourceTemplate` validating webhook requires TLS. The Helm chart uses cert-manager to provision the certificate (`certmanager.enabled: true` is the default).

If cert-manager is not already installed in your cluster, you can either install it separately ([cert-manager installation docs](https://cert-manager.io/docs/installation/)) or let the Helm chart install it as a subchart by setting `certmanager.install: true`.

## Example: HPA per worker version

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-hpa
  namespace: my-namespace
spec:
  # Reference the TemporalWorkerDeployment to attach to.
  temporalWorkerDeploymentRef:
    name: my-worker

  # The resource template. The controller creates one copy per worker version
  # with a running Deployment.
  template:
    apiVersion: autoscaling/v2
    kind: HorizontalPodAutoscaler
    spec:
      # {} tells the controller to auto-inject the versioned Deployment reference.
      # Do not set this to a real value — the webhook will reject it.
      scaleTargetRef: {}
      minReplicas: 2
      maxReplicas: 10
      metrics:
        - type: Resource
          resource:
            name: cpu
            target:
              type: Utilization
              averageUtilization: 70
        # For backlog-based scaling, add an External metric entry.
        # temporal_worker_deployment_name, build_id, and temporal_namespace are injected
        # automatically — do not set them here.
        - type: External
          external:
            metric:
              name: temporal_backlog_count_by_version
              selector:
                matchLabels:
                  task_type: "Activity"
            target:
              type: AverageValue
              averageValue: "10"
```

See [examples/wrt-hpa.yaml](../examples/wrt-hpa.yaml) for an example pre-configured for the helloworld demo.

## Example: PodDisruptionBudget per worker version

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-pdb
  namespace: my-namespace
spec:
  temporalWorkerDeploymentRef:
    name: my-worker
  template:
    apiVersion: policy/v1
    kind: PodDisruptionBudget
    spec:
      minAvailable: 1
      # {} tells the controller to auto-inject {temporal.io/build-id, temporal.io/deployment-name}.
      selector:
        matchLabels: {}
```

## Example: KEDA ScaledObject per worker version

For clusters where KEDA owns the external-metrics APIService, HPAs with `type: External` cannot resolve against other sources. Produce KEDA `ScaledObject`s directly:

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-keda
  namespace: my-namespace
spec:
  temporalWorkerDeploymentRef:
    name: my-worker
  template:
    apiVersion: keda.sh/v1alpha1
    kind: ScaledObject
    spec:
      # {} tells the controller to inject the versioned Deployment reference.
      # Do not set this to a real value — the webhook will reject it.
      scaleTargetRef: {}
      minReplicaCount: 1
      maxReplicaCount: 10
      triggers:
        - type: prometheus
          metadata:
            serverAddress: http://prometheus.monitoring.svc:9090
            threshold: "1"
            query: |
              sum(temporal_backlog_count_by_version{
                temporal_worker_deployment_name="__TEMPORAL_WORKER_DEPLOYMENT_NAME__",
                temporal_worker_build_id="__TEMPORAL_WORKER_BUILD_ID__",
                temporal_namespace="__TEMPORAL_NAMESPACE__"
              })
```

Scale-to-zero (`minReplicaCount: 0` or `idleReplicaCount: 0`) is rejected by the webhook for the same Temporal-side reason as HPA `minReplicas: 0`: `approximate_backlog_count` is not emitted when the task queue is idle with no pollers, so the autoscaler cannot detect new work from a cold start.

Requires KEDA installed in the cluster and `ScaledObject` added to `workerResourceTemplate.allowedResources` (see next section).

## Checking status

```bash
# See all WorkerResourceTemplates and which TWD they reference
kubectl get WorkerResourceTemplate -n my-namespace

# See per-Build-ID apply status
kubectl get WorkerResourceTemplate my-worker-hpa -n my-namespace \
  -o jsonpath='{.status.versions}' | jq .

# See the created HPAs
kubectl get hpa -n my-namespace
```
