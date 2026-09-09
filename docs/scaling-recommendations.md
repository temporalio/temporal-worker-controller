# Scaling Recommendations

This document describes practical reactivity and reliability tradeoffs when scaling Temporal workers per worker deployment version on Kubernetes, and recommends which tool fits which workload pattern.

The `internal/demo/` example wires the HPA path described here. The KEDA path is a supported alternative that polls the Temporal API directly instead of going through a metrics pipeline. They can't both run in the same cluster, because Kubernetes allows only one provider for the `external.metrics.k8s.io` API and each of them claims it.

## TL;DR

We recommend choosing a scaler approach that aligns with the workload pattern your application exhibits.

| Workload pattern | Recommendation |
|------------------|----------------|
| Continuous traffic (task queue always loaded) | HPA + prometheus-adapter* |
| Idle periods >5 min between work AND needs scale-from-zero | KEDA Temporal scaler |
| Required reactivity < ~60 s from first backlog | KEDA Temporal scaler |
| Required reactivity ~90 s typical, tolerant of occasional multi-minute stalls | HPA + prometheus-adapter |
| 1000s of task queues and worker deployment versions  | HPA + prometheus-adapter |

\* We tested and are discussing prometheus in detail, but there are other
ways to pipe Cloud Metrics -> HPA that have similar caveats with slightly
different timing depending on configuration

We discuss the Prometheus Metrics Adapter in depth because it is free to
install so we have tested it with the Worker Controller end to end, but the
Temporal Cloud Metrics -> HPA method is expected to work with other metrics
providers as long as the metrics provider can ingest Temporal Cloud Metrics
from our OpenMetrics endpoint, and the metrics provider has a Kubernetes
adapter to pipe those metrics to HPA and uses the native HPA `matchLabels`
format to create a per-version metrics query. Using a different aggregation
layer could incur additional delays, depending on the configuration of that
layer.

Examples of potential combinations:

- **Prometheus** (detailed in this document)
 * Temporal Cloud Metrics integration (Temporal Cloud -> Prometheus)
 * HPA adapter (Prometheus -> HPA)
- **Datadog**
 * Temporal Cloud Metrics integration (Temporal Cloud -> DataDog)
 * HPA adapter (DataDog -> HPA)
- **New Relic**
 * Temporal Cloud Metrics integration (Temporal Cloud -> New Relic)
 * HPA adapter (New Relic -> HPA)
- **OpenTelemetry Collector**
 * Temporal Cloud Metrics integration (Temporal Cloud -> OpenTelemetry)
 * AWS CloudWatch OpenTelemetry integration (OpenTelemetry -> CloudWatch)
 * HPA adapter for AWS CloudWatch (CloudWatch -> HPA)

This provider flexibility applies to the HPA path only. KEDA has triggers for several of these providers, but per-version scaling with KEDA works with the `temporal` trigger alone; see [KEDA limitations](#keda-limitations).

## HPA scaling signal

This section describes the signal used by HPA + prometheus adapter to adjust the count of workers in a Kubernetes deployment managed by Temporal Worker Controller.

There are two metric data points that are scraped by HPA + prometheus adapter.

`temporal_cloud_v1_approximate_backlog_count` (or just "backlog") is a measurement of the number of pending tasks on a particular task queue that are waiting for a poller (a worker) to pull that task and process it. This is a metric provided by [Temporal Cloud's OpenMetrics aggregation service][tc-openmetrics].

`temporal_slot_utilization` (or just "slot util") is emitted directly by Workers (no Temporal Cloud aggregation), scraped at the Prometheus `ServiceMonitor` interval (~10–30 s), and reflects the current state of a particular Worker. This metric rises *before* backlog accumulates. In other words, slots on the Worker saturate first, then queueing starts.

For a continuously-loaded task queue, important events from "backlog appears" to "HPA scales up" can be visualized like so:

```
backlog appears at T0
  └─ Temporal Cloud OpenMetrics emission cadence     + ~60s worst-case  (~1 sample/minute)
       └─ Prometheus scrape interval                 + ~10s
            └─ HPA poll interval                     + ~15s
                 └─ scale-up stabilization window    + taken from HPA configuration
                      └─ first replica added
```

Per-Worker tunable configuration options is outside the scope of this document.

Please refer to the [documentation](worker-perf) for recommendations on when to use different [slot allocation strategies](slot-alloc-strat) for different workloads.

Briefly, however, follow this advice:

> Scenarios with tasks that have variable, or very high, per-task resource
> needs should rely on fixed-size suppliers and manual tuning rather than
> resource-based suppliers.

[tc-openmetrics]: https://docs.temporal.io/cloud/metrics/openmetrics
[worker-perf]: https://docs.temporal.io/develop/worker-performance
[slot-alloc-strat]: https://docs.temporal.io/develop/worker-performance#choosing-slot-supplier-types

## HPA strengths

Because HPA uses a single OpenMetrics scrape to gather all series for the namespace in a single HTTP request, the HPA approach scales independently of namespace count. The single HTTP request for OpenMetrics more efficient than KEDA's Temporal API-based approach, and will not run into Temporal API rate limiting problems (see section below on [KEDA limitations](#keda-limitations)).

HPA + prometheus adapter can be configured to look at both slot utilization and backlog provides fast scale-up via slot util and a backlog-driven backstop to prevent overly reactive replica count adjustment. Slot utilization can be used to prevent overly reactive scale-down when backlog is zero but the workers are well-utilized and replica count is right-sized for the workload.

## HPA limitations

This section describes two known limitations for HPA + prometheus adapter.

Temporal Cloud's OpenMetrics endpoint may sometimes return the same embedded timestamps on repeated scrapes for each series across the account simultaneously — backlog series, action counts, error counts, every queue, every namespace. This delay in returning fresh metrics data can impact the speed to which HPA + prometheus adapter scales out or in the replica count for a worker deployment version. This means that HPA + prometheus adapter may not be a good solution if your workload cannot tolerate occasional multi-minute scaling pauses.

> **Warning**: There is an [up to 3 minute potential delay][om-delay] before exported metrics are available in the Temporal Cloud OpenMetrics endpoint for new task queues.

> **Note**: This is why `metricsRelistInterval: 5m` is the recommended setting: the discovery window must comfortably exceed the longest expected delay so the metric does not deregister, otherwise re-registration waits up to one more relist cycle after delivery resumes.

HPA cannot scale your Worker Deployment from zero because the signal for scaling does not yet exist. The signal for scaling is the backlog metric for the task queue associated with the workers in the Worker Deployment. This metric will not exist until there is at least one worker polling the task queue.

In addition to the "first worker start" problem, for customers using Temporal Cloud, if there are no polling workers for a task queue for more than 5 minutes, Temporal Cloud will unload the task queue from memory. Unloaded task queues do not emit metrics, and therefore the signal that HPA uses to scale up will not be present.

Submitting a workflow does load the task queue back into memory, but the metric still won't reach the HPA until the next OpenMetrics emission cycle (~1 minute). By the time the HPA reacts, you've already had ~1+ minute of unprovisioned work.

[om-delay]: https://docs.temporal.io/cloud/metrics/openmetrics#overview

## HPA example configuration

Here is an example HPA + prometheus adapter configuration (snipped for brevity).

**Scrape config** (`internal/demo/k8s/prometheus-stack-values.yaml`):
```yaml
- job_name: temporal_cloud
  scrape_interval: 10s
  honor_timestamps: true
  metrics_path: /v1/metrics
  params:
    labels:
      - temporal_worker_deployment_name
      - temporal_worker_build_id
```

**prometheus-adapter rule** (`internal/demo/k8s/prometheus-adapter-values.yaml`):
```yaml
metricsRelistInterval: 5m   # must accommodate Cloud's ~3-min embedded-timestamp lag
rules:
  external:
    - seriesQuery: 'temporal_cloud_v1_approximate_backlog_count{temporal_worker_build_id!="__unversioned__"}'
      metricsQuery: 'sum(<<.Series>>{<<.LabelMatchers>>})'
      name:
        as: "temporal_cloud_v1_approximate_backlog_count"
      resources:
        namespaced: false
```

The `seriesQuery` filter excludes `__unversioned__` series. Without it, accounts with many unversioned namespaces produce 5000+ series in the discovery response, which slows or breaks adapter discovery. The filter scopes discovery to versioned workloads.

**HPA template** (`examples/wrt-hpa-backlog.yaml`): two metrics — slot utilization (fast leading signal, scale-up gate) and backlog count (confirming signal, AverageValue target).

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
spec:
  scaleTargetRef: {}
  minReplicas: 1
  maxReplicas: 30
  metrics:
    - type: External
      external:
        metric:
          name: temporal_slot_utilization
          selector:
            matchLabels:
              worker_type: "ActivityWorker"
        target:
          type: AverageValue
          value: "0.75"

    - type: External
      external:
        metric:
          name: temporal_cloud_v1_approximate_backlog_count
          selector:
            matchLabels:
              temporal_task_queue: "default_helloworld"
              task_type: "Activity"
        target:
          type: AverageValue
          averageValue: "1"
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 30
      policies:
        - type: Percent
          value: 10
          periodSeconds: 10
      selectPolicy: Max

    scaleDown:
      stabilizationWindowSeconds: 120
      policies:
      - type: Percent
        value: 10
        periodSeconds: 10
      selectPolicy: Max
```

## KEDA scaling signal

This section describes the signal used by KEDA's Temporal scaler to adjust the count of workers in a Kubernetes deployment managed by Temporal Worker Controller.

KEDA calls `DescribeWorkerDeploymentVersion` over gRPC directly against the Temporal server, reading the approximate backlog count for one specific Worker Deployment Version, with nothing scraped, aggregated, or relabelled along the way.

Currently, backlog is the only signal available on this path, which has consequences for scale-down; see [KEDA limitations](#keda-limitations).

Three fields do most of the tuning:

| Field | Default | Effect |
|-------|---------|--------|
| `pollingInterval` | 30s | how often KEDA queries Temporal; each poll costs ~1 API call per ScaledObject |
| `targetQueueSize` | 5 | target backlog per replica; the HPA adds replicas when the backlog per active replica exceeds it |
| `cooldownPeriod` | 300s | applies only when scaling to zero and has no effect when `minReplicaCount` is 1 or higher |

## KEDA strengths

KEDA needs no metrics pipeline and has fewer moving parts.

Because there is no pipeline in front of the scaler, how quickly it reacts comes down mostly to `pollingInterval`, which you set yourself. On the HPA path the largest delay is Temporal Cloud's metrics emission cadence, which you have no control over — see [HPA scaling signal](#hpa-scaling-signal).

Per-version scoping also needs no Temporal Cloud configuration. The HPA path requires opting in to the `temporal_worker_deployment_name` and `temporal_worker_build_id` OpenMetrics labels, plus adapter rules to expose them; on the KEDA path the controller injects the version identifiers into trigger metadata directly.

## KEDA limitations

Only the `temporal` trigger can be scoped to a single version. Triggers like `prometheus`, `datadog`, and `dynatrace` take their query as a single string rather than a structured selector, so there is no field for the controller to inject a Build ID into, and no templating syntax to do it with today ([#355](https://github.com/temporalio/temporal-worker-controller/issues/355)). You can scale per version on backlog, but not on arbitrary cluster metrics such as slot utilization.

Backlog is a good signal for scaling up, but a poor one for scaling down, i.e. an idle fleet and a busy fleet that is keeping up will both report zero. The HPA path specifically guards against that scenario by pairing backlog with the `temporal_slot_utilization` metric. Note that slot utilization is not available per version with KEDA. A conservative `scaleDown` stabilization window on the ScaledObject's HPA behavior mitigates this, but it delays scale-down rather than detecting busy workers. If your workload cannot tolerate scaling down while workers are still busy, it is recommended to go the HPA path.

Querying Temporal directly has its own cost. Every poll is an API call, and those calls share a per-namespace rate limit:

```
FrontendGlobalWorkerDeploymentReadRPS = 50  # per namespace, evenly distributed across frontend instances
```

For a namespace with N task queues × M worker-deployment-versions = K HPAs, each KEDA poll uses ~1 API call. The polling budget:

| HPA count | Poll every 30s | Poll every 10s | Poll every 5s |
|-----------|----------------|----------------|---------------|
| 50        | 1.7 RPS (3%)   | 5 RPS (10%)    | 10 RPS (20%)  |
| 250       | 8 RPS (17%)    | 25 RPS (50%)   | 50 RPS (100%) |
| 1500      | 50 RPS (100%)  | exceeds limit  | exceeds limit |


If you are using KEDA with Temporal Cloud and hitting the API rate limit described above, you will need to contact your Temporal Cloud account team to discuss increasing the rate limits.

## KEDA example configuration

Per-version scaling with KEDA requires KEDA >= 2.20.0, which added the `workerDeploymentName` and `workerDeploymentBuildId` trigger metadata ([kedacore/keda#7672](https://github.com/kedacore/keda/pull/7672)), and Temporal Worker Controller >= v1.8.0, which auto-injects them. Earlier KEDA releases can only query a task queue in aggregate across all versions.

> **Warning**: Do not install KEDA into a cluster that already runs prometheus-adapter. KEDA's metrics-apiserver takes over the `external.metrics.k8s.io` APIService.

Here is an example KEDA ScaledObject configuration (snipped for brevity).

**Controller Helm values**: `ScaledObject` must be added to the allow-list, which lists `HorizontalPodAutoscaler` only by default.
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

This entry serves two purposes: it is the validating webhook's allow-list and generates the RBAC the controller needs to create `ScaledObject`s.

**TriggerAuthentication**: KEDA takes the Temporal Cloud API key as a parameter named `apiKey`, never as trigger metadata.
```yaml
apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata:
  name: keda-trigger-auth-temporal
  namespace: default
spec:
  secretTargetRef:
    - parameter: apiKey
      name: temporal-cloud-api-key
      key: api-key
```

This maps `apiKey` onto an existing secret whose key is `api-key`, so the same credential can serve the workers, the controller, and the scaler.

**WorkerResourceTemplate** (`examples/wrt-keda.yaml`): the empty string is the opt-in sentinel for the three identifiers the controller owns.
```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerResourceTemplate
spec:
  workerDeploymentRef:
    name: helloworld
  template:
    apiVersion: keda.sh/v1alpha1
    kind: ScaledObject
    spec:
      scaleTargetRef: {}
      minReplicaCount: 1
      maxReplicaCount: 3
      pollingInterval: 10
      triggers:
        - type: temporal
          authenticationRef:
            name: keda-trigger-auth-temporal
          metadata:
            endpoint: us-east-1.aws.api.temporal.io:7233
            taskQueue: default/helloworld
            queueTypes: activity
            targetQueueSize: "5"
            namespace: ""
            workerDeploymentName: ""
            workerDeploymentBuildId: ""
```

A non-empty value for `namespace`, `workerDeploymentName`, or `workerDeploymentBuildId` is rejected by the validating webhook. `minReplicaCount` must stay at 1 or higher: a version whose workers are not polling never registers with Temporal, and its rollout will not progress.

## References

- [Temporal Cloud OpenMetrics](https://docs.temporal.io/cloud/metrics/openmetrics) — endpoint and opt-in labels
* [Temporal Worker Performance Tuning](https://docs.temporal.io/develop/worker-performance) — explanation of tunable Worker performance knobs
- [prometheus-adapter README](https://github.com/kubernetes-sigs/prometheus-adapter/blob/master/README.md) — `metrics-relist-interval` and discovery window semantics
- [prometheus-adapter externalmetrics.md](https://github.com/kubernetes-sigs/prometheus-adapter/blob/master/docs/externalmetrics.md) — external rules, `namespaced: false` for cluster-scoped metrics
- [Prometheus HTTP API: `/api/v1/series`](https://prometheus.io/docs/prometheus/latest/querying/api/#finding-series-by-label-matchers) — series discovery semantics
- [Prometheus scrape config: `honor_timestamps`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#scrape_config) — preserving source timestamps
- [KEDA Temporal scaler](https://keda.sh/docs/latest/scalers/temporal/) — trigger metadata and authentication parameters
- [KEDA ScaledObject specification](https://keda.sh/docs/latest/reference/scaledobject-spec/) — `pollingInterval`, `cooldownPeriod`, and HPA `behavior` overrides
- [kedacore/keda#7672](https://github.com/kedacore/keda/pull/7672) — Worker Deployment Version support in the Temporal scaler, released in KEDA 2.20.0
- [Temporal Cloud service regions](https://docs.temporal.io/cloud/regions) — regional gRPC endpoints
