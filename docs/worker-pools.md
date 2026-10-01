# Worker Pools

One `WorkerDeployment` can run several named worker pools in place of a single `spec.deployment`. Each pool gets its own Kubernetes `Deployment` in every version, with its own pod template, replicas, ServiceAccount, resources and placement. All pools share the `WorkerDeployment`'s Temporal deployment name and Build ID. So their task queues are in one Worker Deployment Version, and they ramp, promote, roll back and sunset together. Each pool still scales on its own.

## When to use pools

A Pinned workflow's activities and child workflows stay on its Build ID only when their task queue is in the same Worker Deployment Version. If each worker role is its own `WorkerDeployment`, each role is its own Temporal Worker Deployment. During a ramp, a workflow on the new build then often calls an activity or child on the old one (see [Worker Versioning](https://docs.temporal.io/worker-versioning)).

Use pools when roles release together but need different pod shapes. For example, parent workflows plus child workflows that need more memory, or activities that must run on GPU nodes. All pools are equal: any pool can run workflows, child workflows, activities, or a mix.

Use separate `WorkerDeployment` resources when roles should release and roll back on their own.

## Example

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerDeployment
metadata:
  name: documents
spec:
  pools:
    - name: workflows
      deployment:
        replicas: 2
        template:
          spec:
            containers:
              - name: worker
                image: registry.example.com/documents:v42
                env:
                  - name: WORKER_ROLE
                    value: workflows
    - name: parse
      deployment:
        # Omit replicas to let an autoscaler own this pool.
        template:
          spec:
            serviceAccountName: parser
            nodeSelector:
              gpu: "true"
            containers:
              - name: worker
                image: registry.example.com/documents:v42
                env:
                  - name: WORKER_ROLE
                    value: parse
                resources:
                  requests:
                    cpu: "1"
    - name: extract
      deployment:
        replicas: 4
        template:
          spec:
            containers:
              - name: worker
                image: registry.example.com/documents:v42
                env:
                  - name: WORKER_ROLE
                    value: extract
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 10
        pauseDuration: 10m
  sunset:
    scaledownDelay: 1h
    deleteDelay: 24h
  workerOptions:
    connectionRef:
      name: temporal
    temporalNamespace: documents
```

A `WorkerDeployment` sets exactly one of `spec.pools` and `spec.deployment`. Your workers decide which task queues each pool polls, here from `WORKER_ROLE`. The controller injects the same `TEMPORAL_DEPLOYMENT_NAME` and `TEMPORAL_WORKER_BUILD_ID` into every pool.

A full example is in [examples/worker-pools.yaml](../examples/worker-pools.yaml).

## How it works

- **One version for all pools.** The Build ID hashes every pool's name and pod template, in name order. Changing any pool's template starts one new version for all pools, and so does adding, removing or renaming a pool. Changing replicas or reordering pools does not. The Build ID starts with the image tag of the first pool by name.
- **One Deployment per pool per version.** Each is named `<WorkerDeployment.Name>-<pool>-<BuildID>-<hash>`, shortened to fit 47 characters. Each carries a `temporal.io/worker-pool` label in its selector, so selectors never overlap, and HPAs and PDBs only see one pool's pods.
- **Status.** `status.targetVersion.pools`, `status.currentVersion.pools` and each deprecated version list each pool's Deployment and when it became available.

## Rollouts

A target version with pools counts as healthy only when every pool in the spec has a Deployment that is Available with at least one available replica. A pool with `replicas: 0` is exempt. The gate workflow waits for that, and so do the promotions the controller makes under `AllAtOnce` and `Progressive`.

Make each worker's readiness probe pass only once the worker is polling Temporal. Otherwise a pod can be Ready before its task queues are in the version.

Temporal adds its own check. When the controller sets a new current or ramping version, the server rejects it if the new version is missing a task queue the current version still uses. So a pool whose pods have not polled yet blocks the promotion rather than splitting builds. The server skips this check on a WorkerDeployment's first rollout, and for task queues that are new in this version.

Once the target version is registered with Temporal but still waiting on pools, the `Progressing` condition has reason `WaitingForPollers` and names the pools that are not ready yet.

### Gate workflows

The gate runs one workflow on each workflow task queue in the target version. It therefore runs on every pool that polls workflow queues, and not on activity-only pools. If you use a gate, at least one pool must poll a workflow queue, and every pool that polls workflows must register the gate workflow type.

Turn off workflow polling in activity-only pools. In Go, set `worker.Options.DisableWorkflowWorker`. By default the SDK polls for workflow tasks even when no workflows are registered, which puts the pool's queue in the version as a workflow queue. The gate would then start a workflow there that no worker can run.

## Scaling

Set `replicas` on a pool to have the controller manage it, or omit it to let an autoscaler own that pool. This works the same as `spec.deployment.replicas`.

To autoscale a pool, set `pool` on a `WorkerResourceTemplate`. The controller then renders one copy per version for that pool's Deployment only. A `WorkerResourceTemplate` for a `WorkerDeployment` with pools must set `pool`.

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
metadata:
  name: documents-parse-hpa
spec:
  workerDeploymentRef:
    name: documents
  pool: parse
  template:
    apiVersion: autoscaling/v2
    kind: HorizontalPodAutoscaler
    spec:
      scaleTargetRef: {}
      minReplicas: 1
      maxReplicas: 20
      metrics:
        - type: Resource
          resource:
            name: cpu
            target:
              type: Utilization
              averageUtilization: 70
```

Notes:

- **Backlog metrics.** Backlog and slot metrics carry the deployment name and Build ID, not the pool. Add `task_queue` to the metric selector so each pool scales on its own queue.
- **KEDA.** Keep `minReplicaCount` at 1 or more for a pool. A pool at zero replicas never becomes healthy, so the target version can't be promoted. The controller also scales a new version's pool back to 1 when it finds it at zero.
- **Current-version conditions.** A pool scaled to zero on purpose shows up as a task queue without pollers in the `Progressing` condition.

## Changing pools

Without `unsafeCustomBuildID`, any change to the set of pools or their pod templates rolls out as a new version.

With `unsafeCustomBuildID` set and unchanged:

- Changing a pool's pod template rolls that pool's Deployment in place.
- Adding a pool creates its Deployment in the existing version.
- Removing a pool deletes its Deployment from that version. The version itself stays.
- Switching between `spec.deployment` and `spec.pools` is refused: the controller sets `Ready` and `Progressing` to `False` with reason `InvalidSpec` and keeps reconciling everything else. Change `unsafeCustomBuildID` to roll the change out as a new version.

## Limits

- 1 to 10 pools.
- Pool names are DNS labels of at most 24 characters. `default` is reserved.
- `spec.pools` can't be combined with `spec.deployment` or the deprecated `spec.template` fields.

## Downgrading the controller

A controller release without worker pools ignores `spec.pools`, so it can't run a `WorkerDeployment` that uses them. Move every such `WorkerDeployment` back to `spec.deployment` before you roll the controller back to that release.
