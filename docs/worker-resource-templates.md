# WorkerResourceTemplate

`WorkerResourceTemplate` lets you attach arbitrary Kubernetes resources — HPAs, PodDisruptionBudgets, custom CRDs — to each worker version that has running workers. The controller creates one copy of the resource per worker version with a running Deployment, automatically wired to the correct versioned Deployment.

## Why you need this

The Temporal Worker Controller creates one Kubernetes `Deployment` per worker version (Build ID). If you attach an HPA directly to a single Deployment, it breaks as versions roll over — the old HPA still targets the old Deployment, the new Deployment has no HPA, and you have to manage cleanup yourself.

`WorkerResourceTemplate` solves this by treating the resource as a template. The controller renders one instance per worker version with running workers, injects the correct versioned Kubernetes Deployment name, injects per-version metrics tags where appropriate, and cleans up automatically when the versioned Deployment is deleted (e.g., during the sunset process after traffic has drained).

This is also the recommended mechanism for metric-based or backlog-based autoscaling: attach a standard HPA with custom metrics to your workers and the controller keeps one per running worker version, each pointing at the right Deployment and version-tagged metrics.

## How it works

1. You create a `WorkerResourceTemplate` that references a `WorkerDeployment` and contains the resource spec in `spec.template`.
2. The validating webhook checks that you have permission to manage that resource type yourself (SubjectAccessReview), and that the resource kind is on the allowed list (see below).
3. On each reconcile loop, the controller renders one copy of `spec.template` per worker version with a running Deployment, injects fields (see below), and applies it via Server-Side Apply. If you set `spec.versionStatuses`, only versions with a listed status get a copy (see [Selecting versions by status](#selecting-versions-by-status)).
4. Each per-version copy is deleted by the controller when its corresponding Kubernetes Deployment is deleted per the sunset policy, or when its version changes to a status that `spec.versionStatuses` does not list.
5. `WorkerResourceTemplate.status.versions` is updated with the applied/failed status for each version.

## Auto-injection

The controller auto-injects the fields below when you set them to `{}` (empty object) in `spec.template`. `{}` is the explicit opt-in sentinel:
- If you omit the field entirely, nothing is injected.
- If you set a non-empty value, the webhook rejects the `WorkerResourceTemplate` because the controller owns these fields.

| Field | Scope | Injected value                                                                                                              |
|-------|-------|-----------------------------------------------------------------------------------------------------------------------------|
| `scaleTargetRef` | Anywhere in `spec` (recursive) | `{apiVersion: apps/v1, kind: Deployment, name: <versioned-deployment-name>}`                                                |
| `spec.targetRef` | Only at this exact path | `{apiVersion: apps/v1, kind: Deployment, name: <versioned-deployment-name>}`                                                |
| `spec.selector.matchLabels` | Only at this exact path | `{temporal.io/build-id: <buildID>, temporal.io/deployment-name: <wdName>}`                                                  |
| `spec.metrics[*].external.metric.selector.matchLabels` | Each External metric entry where `matchLabels` is present | `{temporal_worker_deployment_name: <ns>_<wd-name>, temporal_worker_build_id: <buildID>, temporal_namespace: <temporal-ns>}` |

`scaleTargetRef` injection is recursive and covers HPAs, WPAs, and other autoscaler CRDs.

`spec.targetRef` covers VerticalPodAutoscalers, which name their target in `spec.targetRef` instead of `scaleTargetRef`.

`spec.selector.matchLabels` uses `{}` as the opt-in sentinel — absent means no injection; `{}` means inject pod selector labels.

`spec.metrics[*].external.metric.selector.matchLabels` appends the three Temporal metric identity labels (`temporal_worker_deployment_name`, `temporal_worker_build_id`, `temporal_namespace`) to any External metric selector where the `matchLabels` key is present (including `{}`). User labels like `task_type: "Activity"` coexist — the controller merges its keys alongside whatever you provide. If `matchLabels` is absent on a metric entry, no injection occurs for that entry.

For metrics backends that use Temporal Server's native label names, set the Helm value `workerResourceTemplate.hpaMatchLabelsStripTemporalPrefix: true` (or run the controller with `--wrt-hpa-match-labels-strip-temporal-prefix`). The controller will inject `worker_deployment_name`, `worker_build_id`, and `namespace` instead.

The webhook rejects any template that hardcodes `temporal_worker_deployment_name`, `temporal_worker_build_id`, or `temporal_namespace` in a metric selector — these are always controller-owned. When prefix stripping is enabled, it also rejects `worker_deployment_name`, `worker_build_id`, and `namespace`.

## Template variables

Any string in `spec.template` may contain these tokens. The controller replaces them on each rendered copy:

| Token | Value |
|-------|-------|
| `{{temporal_worker_deployment_name}}` | `<ns>_<wd-name>` |
| `{{temporal_worker_build_id}}` | `<buildID>` |
| `{{temporal_namespace}}` | `<temporal-ns>` |

The values are the same ones appended to `spec.metrics[*].external.metric.selector.matchLabels`. The controller replaces the exact token, the same way an empty `matchLabels: {}` or `""` opts in to injection.

When `workerResourceTemplate.hpaMatchLabelsStripTemporalPrefix` is enabled, the tokens are `{{worker_deployment_name}}`, `{{worker_build_id}}`, and `{{namespace}}`, matching the injected matchLabels.

Use the tokens in KEDA triggers whose query is a single string (`prometheus`, `datadog`, `dynatrace`, and others) so each ScaledObject filters metrics to one worker version. See [examples/wrt-keda-prometheus.yaml](../examples/wrt-keda-prometheus.yaml).

## Selecting versions by status

By default, a `WorkerResourceTemplate` renders a copy for every version with a running Deployment, whatever its status. A Current version and a version that drains for days behind pinned workflows then get the same policy.

Set `spec.versionStatuses` to render copies only for versions with one of the listed [version statuses](concepts.md#version-states): `NotRegistered`, `Created`, `Inactive`, `Ramping`, `Current`, `Draining`, or `Drained`. If you leave it unset, every status is selected.

Two templates for the same `WorkerDeployment` get different resource names, so you can split the statuses between them.

For example, a strict PodDisruptionBudget for versions that receive new workflows, and none for past versions, so that a Draining version at one replica does not block node drains:

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-pdb
  namespace: my-namespace
spec:
  workerDeploymentRef:
    name: my-worker
  versionStatuses: [Current, Ramping]
  template:
    apiVersion: policy/v1
    kind: PodDisruptionBudget
    spec:
      minAvailable: 1
      selector:
        matchLabels: {}
```

Or two HPAs: one that scales versions that receive new workflows on backlog, and one that keeps past versions small while they finish their pinned workflows:

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-hpa-current
  namespace: my-namespace
spec:
  workerDeploymentRef:
    name: my-worker
  versionStatuses: [Inactive, Ramping, Current]
  template:
    apiVersion: autoscaling/v2
    kind: HorizontalPodAutoscaler
    spec:
      scaleTargetRef: {}
      minReplicas: 3
      maxReplicas: 50
      metrics:
        - type: External
          external:
            metric:
              name: temporal_approximate_backlog_count
              selector:
                matchLabels: {}
            target:
              type: AverageValue
              averageValue: "10"
---
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-hpa-past
  namespace: my-namespace
spec:
  workerDeploymentRef:
    name: my-worker
  versionStatuses: [Draining, Drained]
  template:
    apiVersion: autoscaling/v2
    kind: HorizontalPodAutoscaler
    spec:
      scaleTargetRef: {}
      minReplicas: 1
      maxReplicas: 2
      metrics:
        - type: External
          external:
            metric:
              name: temporal_approximate_backlog_count
              selector:
                matchLabels: {}
            target:
              type: AverageValue
              averageValue: "10"
```

### How copies follow status changes

The controller reads the version statuses from Temporal on each reconcile, about every 10 seconds.

- When a version changes to a status that is not listed, the controller deletes its copy. When the version changes back, for example after a rollback, the controller renders the copy again.
- A new version gets no copy until it reaches a listed status. During a rollout, the new version is `NotRegistered` or `Inactive` before it becomes `Ramping` or `Current`.
- A copy follows a status change in the next reconcile, not at the same moment. After a promotion, it can take up to about 10 seconds before the new Current version gets its copy and the previous Current version loses its copy.
- In each reconcile, the controller deletes copies before it applies new ones. When a version moves from one template to the other, it loses the old copy before it gets the new one, so both copies do not exist at the same time. If a delete fails, both copies exist until the controller retries the delete and it succeeds.
- If Temporal returns no worker deployment state, the controller cannot tell the version statuses. Templates with `spec.versionStatuses` then keep their existing copies, and create no new ones, until the statuses are known again.
- A template that matches no live version has an empty `status.versions` and reports `Ready=True`.

### Splitting autoscalers by status

When an autoscaler such as an HPA or a KEDA ScaledObject manages replicas (`spec.deployment.replicas` is unset), follow these rules:

- **List `Inactive` in the template for new versions.** A new versioned Deployment starts at 1 replica. If its autoscaler copy only appears when the version is `Ramping` or `Current`, the version takes new workflows at 1 replica until the copy is applied and the autoscaler reacts. With `Inactive` listed, the copy exists before the promotion.
- **Cover `Draining` and `Drained` in another autoscaler template.** The controller does not scale Draining versions when an autoscaler manages replicas, and it ignores `spec.drainingDeployment.replicas`. A version that loses its autoscaler keeps its last replica count until the sunset scale-down.
- **Do not let two autoscaler templates select the same status.** Two autoscalers that target the same Deployment fight over its replica count.

Even when a template lists `Drained`, the controller removes the autoscaler copy of a Drained version when it scales that version to zero after `sunset.scaledownDelay`.

## Resource naming

Each per-Build-ID copy is given a unique, DNS-safe name derived from the `(wdName, wrtName, buildID)` triple. Names are capped at 47 characters to be safe for all Kubernetes resource types, including Deployment (which has pod-naming constraints that effectively limit Deployment names to ~47 characters). The name always ends with an 8-character hash of the full triple, so uniqueness is guaranteed even when the human-readable prefix is truncated.

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

If cert-manager is not already installed in your cluster, install it separately before installing the controller chart ([cert-manager installation docs](https://cert-manager.io/docs/installation/)).
## Example: HPA per worker version

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-hpa
  namespace: my-namespace
spec:
  # Reference the WorkerDeployment to attach to.
  workerDeploymentRef:
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
              name: temporal_approximate_backlog_count
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
  workerDeploymentRef:
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

## Example: VerticalPodAutoscaler per worker version

> **Note:** VerticalPodAutoscaler must be installed in the cluster separately before you add it to `allowedResources`. The Temporal Worker Controller does not install it. See the [VPA installation guide](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/installation.md).

VerticalPodAutoscaler is not in the default allowed list. Add it to `workerResourceTemplate.allowedResources` first:

```yaml
workerResourceTemplate:
  allowedResources:
    - kinds: ["HorizontalPodAutoscaler"]
      apiGroups: ["autoscaling"]
      resources: ["horizontalpodautoscalers"]
    - kinds: ["VerticalPodAutoscaler"]
      apiGroups: ["autoscaling.k8s.io"]
      resources: ["verticalpodautoscalers"]
```

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: my-worker-vpa
  namespace: my-namespace
spec:
  workerDeploymentRef:
    name: my-worker
  template:
    apiVersion: autoscaling.k8s.io/v1
    kind: VerticalPodAutoscaler
    spec:
      # {} tells the controller to auto-inject the versioned Deployment reference.
      targetRef: {}
      updatePolicy:
        # Initial sets requests only when a pod is created, so VPA never evicts a running worker.
        updateMode: "Initial"
```

See [Vertical scaling with VPA](scaling-recommendations.md#vertical-scaling-with-vpa) for the other update modes and which HPA and KEDA combinations are safe.

## Checking status

```bash
# See all WorkerResourceTemplates and which WorkerDeployment they reference
kubectl get WorkerResourceTemplate -n my-namespace

# See per-Build-ID apply status
kubectl get WorkerResourceTemplate my-worker-hpa -n my-namespace \
  -o jsonpath='{.status.versions}' | jq .

# See the created HPAs
kubectl get hpa -n my-namespace
```
