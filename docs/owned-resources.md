# TemporalWorkerOwnedResource

`TemporalWorkerOwnedResource` (TWOR) lets you attach arbitrary Kubernetes resources — HPAs, PodDisruptionBudgets, custom CRDs — to each active versioned Deployment managed by a `TemporalWorkerDeployment`. The controller creates one copy of the resource per active Build ID, automatically wired to the correct versioned Deployment.

## Why you need this

The Temporal Worker Controller creates one Kubernetes `Deployment` per worker version (Build ID). If you attach an HPA directly to a single Deployment, it breaks as versions roll over — the old HPA still targets the old Deployment, the new Deployment has no HPA, and you have to manage cleanup yourself.

`TemporalWorkerOwnedResource` solves this by treating the attached resource as a template. The controller renders one instance per active Build ID, injects the correct versioned Deployment name, and cleans up automatically when a version is deleted (via Kubernetes owner reference garbage collection).

## How it works

1. You create a `TemporalWorkerOwnedResource` that references a `TemporalWorkerDeployment` and contains the resource spec in `spec.object`.
2. The validating webhook checks that you have permission to manage that resource type yourself (SubjectAccessReview), and that the resource kind isn't on the banned list.
3. On each reconcile loop, the controller renders one copy of `spec.object` per active Build ID, injects fields (see below), and applies it via Server-Side Apply.
4. Each copy is owned by the corresponding versioned `Deployment`, so it is garbage-collected automatically when that Deployment is deleted.
5. `TWOR.status.versions` is updated with the applied/failed status for each Build ID.

## Auto-injection

The controller auto-injects two fields when you set them to `null` in `spec.object`. Setting them to `null` is the explicit signal that you want injection — if you omit the field entirely, nothing is injected; if you set a non-null value, the webhook rejects the object.

| Field | Injected value |
|-------|---------------|
| `spec.scaleTargetRef` (HPA) | `{apiVersion: apps/v1, kind: Deployment, name: <versioned-deployment-name>}` |
| `spec.selector.matchLabels` (any) | `{temporal.io/build-id: <buildID>, temporal.io/deployment-name: <twdName>}` |

## Resource naming

Each per-Build-ID copy is named `<twdName>-<tworName>-<buildID>`, cleaned for DNS and truncated to 253 characters. Use `kubectl get hpa` (or whatever kind you attached) after a reconcile to see the created resources.

## RBAC

### What the webhook checks

When you create or update a TWOR, the webhook performs SubjectAccessReviews to verify:

1. **You** (the requesting user) can create/update the embedded resource type in that namespace.
2. **The controller's service account** can create/update the embedded resource type in that namespace.

If either check fails, the TWOR is rejected. This prevents privilege escalation — you cannot use TWOR to create resources you don't already have permission to create yourself.

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

Users who create TWORs also need RBAC permission to manage the embedded resource type directly. For example, to let a team create TWORs that embed HPAs, they need the standard `autoscaling` permissions in their namespace — there is nothing TWOR-specific to configure for this.

## Webhook TLS

The TWOR validating webhook requires TLS. The recommended approach is to install [cert-manager](https://cert-manager.io/docs/installation/) before deploying the controller — the Helm chart handles everything else automatically (`certmanager.enabled: true` is the default).

If cert-manager is not available in your cluster, set `certmanager.enabled: false` and provide:
1. A `kubernetes.io/tls` Secret named `webhook-server-cert` in the controller namespace, containing `tls.crt` and `tls.key` for the webhook server. The certificate must have DNS SANs:
   - `<release-name>-webhook-service.<namespace>.svc`
   - `<release-name>-webhook-service.<namespace>.svc.cluster.local`
2. The base64-encoded CA certificate that signed `tls.crt`, passed as `certmanager.caBundle` in Helm values.

```bash
helm install temporal-worker-controller oci://docker.io/temporalio/temporal-worker-controller \
  --namespace temporal-system \
  --set certmanager.enabled=false \
  --set certmanager.caBundle="$(base64 -w0 ca.crt)"
```

## Example: HPA per Build ID

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

  # The resource template. The controller creates one copy per active Build ID.
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

## Example: PodDisruptionBudget per Build ID

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
# See all TWORs and which TWD they reference
kubectl get twor -n my-namespace

# See per-Build-ID apply status
kubectl get twor my-worker-hpa -n my-namespace -o jsonpath='{.status.versions}' | jq .

# See the created HPAs
kubectl get hpa -n my-namespace
```
