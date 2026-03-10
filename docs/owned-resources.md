# TemporalWorkerOwnedResource

`TemporalWorkerOwnedResource` lets you attach arbitrary Kubernetes resources — HPAs, PodDisruptionBudgets, KEDA ScaledObjects, custom CRDs — to each worker version that has running workers. The controller creates one copy of the resource per worker version with a running Deployment, automatically wired to the correct versioned Deployment.

## Why you need this

The Temporal Worker Controller creates one Kubernetes `Deployment` per worker version (Build ID). If you attach an HPA directly to a single Deployment, it breaks as versions roll over — the old HPA still targets the old Deployment, the new Deployment has no HPA, and you have to manage cleanup yourself.

`TemporalWorkerOwnedResource` solves this by treating the attached resource as a template. The controller renders one instance per worker version with running workers, injects the correct versioned Deployment name, and cleans up automatically when the versioned Deployment is deleted (e.g., during the sunset process after traffic has drained).

This is also the recommended mechanism for metric-based or backlog-based autoscaling: attach a KEDA `ScaledObject` (or a standard HPA with custom metrics) to your workers and the controller keeps one per running worker version, each pointing at the right Deployment.

## How it works

1. You create a `TemporalWorkerOwnedResource` that references a `TemporalWorkerDeployment` and contains the resource spec in `spec.object`.
2. The validating webhook checks that you have permission to manage that resource type yourself (SubjectAccessReview), and that the resource kind isn't on the banned list.
3. On each reconcile loop, the controller renders one copy of `spec.object` per worker version with a running Deployment, injects fields (see below), and applies it via Server-Side Apply.
4. Each copy is owned by the corresponding versioned `Deployment`, so it is garbage-collected automatically when that Deployment is deleted.
5. `TemporalWorkerOwnedResource.status.versions` is updated with the applied/failed status for each Build ID.

## Auto-injection

The controller auto-injects two fields when you set them to `null` in `spec.object`. Setting them to `null` is the explicit signal that you want injection:
- If you omit the field entirely, nothing is injected.
- If you set a non-null value, the webhook rejects the object because the controller owns these fields.

| Field | Injected value |
|-------|---------------|
| `spec.scaleTargetRef` (any resource with this field) | `{apiVersion: apps/v1, kind: Deployment, name: <versioned-deployment-name>}` |
| `spec.selector.matchLabels` (any resource with this field) | `{temporal.io/build-id: <buildID>, temporal.io/deployment-name: <twdName>}` |

The `scaleTargetRef` injection applies to any resource type that has a `scaleTargetRef` field — not just HPAs. KEDA `ScaledObjects` and other autoscaler CRDs use the same field and benefit from the same injection.

## Resource naming

Each per-Build-ID copy is given a unique, DNS-safe name derived from the `(twdName, tworName, buildID)` triple. Names are capped at 47 characters to be safe for all Kubernetes resource types, including Deployment (which has pod-naming constraints that effectively limit deployment names to ~47 characters). The name always ends with an 8-character hash of the full triple, so uniqueness is guaranteed even when the human-readable prefix is truncated.

Use `kubectl get <kind>` after a reconcile to see the created resources and their names.

## Banned resource kinds

Certain resource kinds are blocked by default to prevent misuse (e.g., using `TemporalWorkerOwnedResource` to spin up arbitrary workloads). The default banned list is:

```
Deployment, StatefulSet, Job, Pod, CronJob
```

The banned list is configured via `ownedResourceConfig.bannedKinds` in Helm values and is visible as the `BANNED_KINDS` environment variable on the controller pod:

```bash
kubectl get pod -n <controller-namespace> -l app.kubernetes.io/name=temporal-worker-controller \
  -o jsonpath='{.items[0].spec.containers[0].env[?(@.name=="BANNED_KINDS")].value}'
```

## RBAC

### What the webhook checks

When you create or update a `TemporalWorkerOwnedResource`, the webhook performs SubjectAccessReviews to verify:

1. **You** (the requesting user) can create/update the embedded resource type in that namespace.
2. **The controller's service account** can create/update the embedded resource type in that namespace.

If either check fails, the request is rejected. This prevents privilege escalation — you cannot use `TemporalWorkerOwnedResource` to create resources you don't already have permission to create yourself.

### What to configure in Helm

`ownedResourceConfig.rbac.rules` controls what resource types the controller's ClusterRole permits it to manage. The defaults cover HPAs and PodDisruptionBudgets:

```yaml
ownedResourceConfig:
  rbac:
    rules:
      - apiGroups: ["autoscaling"]
        resources: ["horizontalpodautoscalers"]
      - apiGroups: ["policy"]
        resources: ["poddisruptionbudgets"]
```

Add entries for any other resource types you want to attach (e.g., KEDA `ScaledObjects`). For development clusters you can set `rbac.wildcard: true` to grant access to all resource types, but this is not recommended for production.

### What to configure for your users

Users who create `TemporalWorkerOwnedResources` also need RBAC permission to manage the embedded resource type directly. For example, to let a team create `TemporalWorkerOwnedResources` that embed HPAs, they need the standard `autoscaling` permissions in their namespace — there is nothing `TemporalWorkerOwnedResource`-specific to configure for this.

## Webhook TLS

The `TemporalWorkerOwnedResource` validating webhook requires TLS. Install [cert-manager](https://cert-manager.io/docs/installation/) before deploying the controller — the Helm chart handles everything else automatically (`certmanager.enabled: true` is the default).

## Example: HPA per worker version

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerOwnedResource
metadata:
  name: my-worker-hpa
  namespace: my-namespace
spec:
  # Reference the TemporalWorkerDeployment to attach to.
  workerRef:
    name: my-worker

  # The resource template. The controller creates one copy per worker version
  # with a running Deployment.
  object:
    apiVersion: autoscaling/v2
    kind: HorizontalPodAutoscaler
    spec:
      # null tells the controller to auto-inject the versioned Deployment reference.
      # Do not set this to a real value — the webhook will reject it.
      scaleTargetRef: null
      minReplicas: 2
      maxReplicas: 10
      metrics:
        - type: Resource
          resource:
            name: cpu
            target:
              type: Utilization
              averageUtilization: 70
```

See [examples/twor-hpa.yaml](../examples/twor-hpa.yaml) for an example pre-configured for the helloworld demo.

## Example: PodDisruptionBudget per worker version

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerOwnedResource
metadata:
  name: my-worker-pdb
  namespace: my-namespace
spec:
  workerRef:
    name: my-worker
  object:
    apiVersion: policy/v1
    kind: PodDisruptionBudget
    spec:
      minAvailable: 1
      # null tells the controller to auto-inject {temporal.io/build-id, temporal.io/deployment-name}.
      selector:
        matchLabels: null
```

## Checking status

```bash
# See all TemporalWorkerOwnedResources and which TWD they reference
kubectl get temporalworkerownedresource -n my-namespace

# See per-Build-ID apply status
kubectl get temporalworkerownedresource my-worker-hpa -n my-namespace \
  -o jsonpath='{.status.versions}' | jq .

# See the created HPAs
kubectl get hpa -n my-namespace
```
