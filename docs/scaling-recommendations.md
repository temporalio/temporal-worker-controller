# Scaling Recommendations

This document describes practical reactivity and reliability tradeoffs when scaling Temporal workers per worker deployment version on Kubernetes, and recommends which tool fits which workload pattern.

The `internal/demo/` example wires the HPA path described here. The KEDA path is mentioned for comparison and as a recommendation for workloads that cannot tolerate the HPA path's limits.

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

## KEDA strengths

KEDA's Temporal scaler calls `DescribeTaskQueue(stats=true)` (or `DescribeWorkerDeploymentVersion`), which loads the queue synchronously and returns the backlog directly. This allows KEDA to scale Temporal workers from zero.

## KEDA limitations

As of June 2026 and the KEDA 2.20 release, the KEDA Temporal Scaler uses backlog count *only*. Releases of Temporal Worker Controller before v1.8.0 do not support per-version metrics queries in KEDA Temporal Scaler configuration of Worker Resource Templates.

KEDA bypasses the metric pipeline but uses Temporal API calls, which are subject to a per-namespace rate limit:

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

## References

- [Temporal Cloud OpenMetrics](https://docs.temporal.io/cloud/metrics/openmetrics) — endpoint and opt-in labels
* [Temporal Worker Performance Tuning](https://docs.temporal.io/develop/worker-performance) — explanation of tunable Worker performance knobs
- [prometheus-adapter README](https://github.com/kubernetes-sigs/prometheus-adapter/blob/master/README.md) — `metrics-relist-interval` and discovery window semantics
- [prometheus-adapter externalmetrics.md](https://github.com/kubernetes-sigs/prometheus-adapter/blob/master/docs/externalmetrics.md) — external rules, `namespaced: false` for cluster-scoped metrics
- [Prometheus HTTP API: `/api/v1/series`](https://prometheus.io/docs/prometheus/latest/querying/api/#finding-series-by-label-matchers) — series discovery semantics
- [Prometheus scrape config: `honor_timestamps`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#scrape_config) — preserving source timestamps
- [KEDA Temporal scaler](https://keda.sh/docs/latest/scalers/temporal/) — direct API polling alternative
