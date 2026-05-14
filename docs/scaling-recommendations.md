# Scaling Recommendations: HPA + prometheus-adapter vs KEDA

This document describes practical reactivity and reliability tradeoffs when scaling Temporal workers per worker-deployment-version on Kubernetes, and recommends which tool fits which workload pattern.

The `internal/demo/` example wires the HPA path described here. The KEDA path is mentioned for comparison and as a recommendation for workloads that cannot tolerate the HPA path's limits.

## TL;DR — Pick by workload pattern

| Workload pattern | Recommendation |
|------------------|----------------|
| Continuous traffic (task queue always loaded) | HPA + prometheus-adapter, scaling on slot utilization + backlog count |
| Idle periods >5 min between work; needs scale-from-zero | KEDA Temporal scaler |
| Required reactivity < ~60 s from first backlog | KEDA Temporal scaler |
| Required reactivity ~90 s typical, tolerant of occasional multi-minute stalls | HPA + prometheus-adapter is fine |
| Very large namespaces (N × M HPAs polling) | HPA + prometheus-adapter; KEDA hits the 50 RPS namespace rate limit |

## The reactivity model for HPA + prometheus-adapter

For a continuously-loaded task queue, the end-to-end delay from "backlog appears" to "HPA scales up" decomposes as:

```
backlog appears at T0
  └─ Temporal Cloud OpenMetrics emission cadence    +~60 s worst-case  (~1 sample/minute)
       └─ Prometheus scrape interval                 +~10 s
            └─ HPA poll interval                     +~15 s
                 └─ scale-up stabilization window    +~your config
                      └─ first replica added
```

**Typical end-to-end reactivity is ≈ 85 seconds + your stabilization window.** Empirically, sample age in Prometheus for a single series follows a sawtooth between 0 and 60 seconds (matching the gateway's ~1/min emission cadence). p50 sample age ≈ 30s, p95 ≈ 50s. The 60-second emission cadence is the inherent floor — smaller scrape intervals, tighter `metricsRelistInterval`, or recording rules cannot improve it because they all consume the same upstream cadence.

### Caveat: gateway-wide stalls

During our investigation we observed one period of several minutes during which Temporal Cloud's OpenMetrics endpoint returned frozen timestamps for *every* series across the account — backlog series, action counts, error counts, every queue, every namespace, all showing the exact same staleness simultaneously (e.g. all ~30 visible series reading 239s old, identical to the second). The Prometheus scrape continued to succeed (`up{job="temporal_cloud"}` stayed 1, HTTP 200 responses) — only the embedded timestamps in the response body did not advance. During such a period, HPAs see the last known value (via Prometheus staleness lookback) until either (a) fresh samples resume, or (b) the staleness window (5 min default) expires and the metric disappears entirely.

We have only directly characterized this once, so frequency and typical duration are not yet known. The behavior is open with Temporal's Observability team. If your workload cannot tolerate occasional multi-minute scaling pauses, prefer KEDA.

This is also why `metricsRelistInterval: 5m` is the recommended setting: the discovery window must comfortably exceed the longest expected gap so the metric does not deregister, otherwise re-registration waits up to one more relist cycle after data flows again.

### Slot utilization is a much faster leading signal

`temporal_slot_utilization` is emitted directly by worker pods (no Temporal Cloud aggregation), scraped at the ServiceMonitor interval (~10–30 s), and reflects current state. It also rises *before* backlog accumulates — slots saturate first, then queueing starts. So a two-metric HPA with both slot util and backlog gives you fast scale-up via slot util and a backlog-driven backstop.

The demo HPA uses both. For production scaling we recommend keeping both as well.

## When backlog metric goes silent

Two distinct failure modes that look similar in HPA events but have different meanings:

### Mode 1: adapter-level deregistration (rare)
- Trigger: prometheus-adapter pod restart, or *no* series matching the rule's `seriesQuery` exist in Prometheus.
- Symptom in HPA events: `the server could not find the metric ...`.
- Recovery: up to one `metricsRelistInterval` after data flows again.

prometheus-adapter periodically asks Prometheus "what series exist in the last `metricsRelistInterval`?" — see the [prometheus-adapter README](https://github.com/kubernetes-sigs/prometheus-adapter/blob/master/README.md). If the discovery window is shorter than the longest gateway-wide stall, the discovery returns empty and the metric name disappears from the External Metrics API. The `metricsRelistInterval: 5m` setting buys margin: comfortably longer than typical sample age (~30s p50, ~50s p95) and longer than observed multi-minute gateway stalls so far.

### Mode 2: series-level silence (common in low-traffic workloads)
- Trigger: a task queue with no polls or new tasks for >5 minutes. Temporal unloads it from memory and stops emitting `temporal_cloud_v1_approximate_backlog_count` for that specific `(task_queue, build_id, ...)` labelset. Other queues' series continue to emit.
- Symptom in HPA events: `no metrics returned from external metrics API`. The metric *name* is still registered; the HPA's specific label selector just matches zero rows now.
- Recovery: traffic resumes → queue reloads → next emission cycle (~1 min) + 3-min aggregation lag → HPA can read value again.

In a two-metric HPA configured with slot utilization, this is mostly fine: the HPA reports `ScalingActive=True` based on slot utilization while backlog is unavailable, and rejoins backlog scaling once it returns. We've confirmed this empirically in this demo cluster — the HPA continued scaling correctly on slot utilization through 1000+ backlog `FailedGetExternalMetric` events.

## Why this demo does not use a backlog recording rule

A prior version of this demo wrapped the raw Cloud series in a Prometheus recording rule:

```yaml
- record: temporal_approximate_backlog_count
  expr: sum by (...) (temporal_cloud_v1_approximate_backlog_count)
```

The rule was originally added to work around a label-formatting issue in an older Temporal Cloud release. With native per-version labels (`temporal_worker_deployment_name`, `temporal_worker_build_id`) now opt-in, the rule no longer earns its keep:

- **It doesn't reduce reactivity.** The HPA reactivity floor is the upstream OpenMetrics emission cadence (~60s), not anything the rule could fix.
- **It duplicates the cardinality bill.** Per-`(task_queue, build_id)` labels are already opt-in at the OpenMetrics level *because* of cardinality. Adding a recording rule on top means storing the same high-cardinality series twice.
- **It hides a `sum(...)` that the adapter already does.** prometheus-adapter's `metricsQuery: sum(<<.Series>>{<<.LabelMatchers>>})` performs the same collapsing at query time. Pedagogically, "the adapter does the sum" is cleaner than "a recording rule sums first, then the adapter sums again."
- **It does not solve series-level silence.** When the source goes silent (task queue unloaded), the rule output also goes silent eventually (once Prometheus's staleness lookback expires).

What the recording rule *does* buy is registration stability after operational events: when the source series is sparse-by-timestamp, the rule produces a dense 10-second sample stream that lets the adapter discover with a tight `metricsRelistInterval`. If you find yourself fighting registration flicker on every adapter restart and would rather pay the cardinality cost than tune `metricsRelistInterval`, a recording rule is a reasonable choice. Otherwise, prefer the raw metric.

In this demo we set `metricsRelistInterval: 5m` and consume the raw metric directly.

## Why prometheus-adapter cannot do scale-from-zero

Scale-from-zero on backlog through the metric path requires the metric to exist while there are zero workers. It does not:

1. Zero workers means no polls.
2. No polls for >5 minutes means the task queue is unloaded from Temporal Cloud's memory.
3. An unloaded queue emits no metric.
4. Adapter discovery returns no series, or HPA queries return no rows.
5. HPA cannot scale up because there's no signal to scale on.

Submitting a workflow does load the task queue back into memory, but the metric still won't reach the HPA until the next OpenMetrics emission cycle (~1 minute) plus scrape and HPA poll. By the time the HPA reacts, you've already had ~1+ minute of unprovisioned work.

KEDA's Temporal scaler calls `DescribeTaskQueue(stats=true)` (or `DescribeWorkerDeploymentVersion`), which loads the queue synchronously and returns the backlog directly. No metric pipeline involved. Scale-from-zero in seconds.

## When KEDA hits its own limits

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

prometheus-adapter has no equivalent per-namespace bottleneck — one OpenMetrics scrape returns all series for the namespace in a single HTTP request, scaling independently of HPA count.

So for very large namespaces (hundreds–thousands of HPAs) needing fast reactivity, neither path is great: KEDA hits the API rate limit, and the metric path has the 3-min aggregation floor. In practice this is a "talk to your account team" situation.

## Recommended configuration for the HPA + prometheus-adapter path

This demo's configuration represents the recommendation, in compact form:

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

The `seriesQuery` filter excludes `__unversioned__` series. Without it, accounts with many unversioned namespaces produce 5000+ series in the discovery response, which slows or breaks adapter discovery. The filter scopes discovery to versioned workloads — exactly the ones HPAs need.

**HPA template** (`examples/wrt-hpa-backlog.yaml`): two metrics — slot utilization (fast leading signal, scale-up gate) and backlog count (confirming signal, AverageValue target).

## References

- [Temporal Cloud OpenMetrics](https://docs.temporal.io/cloud/metrics/openmetrics) — endpoint and opt-in labels
- [prometheus-adapter README](https://github.com/kubernetes-sigs/prometheus-adapter/blob/master/README.md) — `metrics-relist-interval` and discovery window semantics
- [prometheus-adapter externalmetrics.md](https://github.com/kubernetes-sigs/prometheus-adapter/blob/master/docs/externalmetrics.md) — external rules, `namespaced: false` for cluster-scoped metrics
- [Prometheus HTTP API: `/api/v1/series`](https://prometheus.io/docs/prometheus/latest/querying/api/#finding-series-by-label-matchers) — series discovery semantics
- [Prometheus scrape config: `honor_timestamps`](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#scrape_config) — preserving source timestamps
- [KEDA Temporal scaler](https://keda.sh/docs/latest/scalers/temporal/) — direct API polling alternative
