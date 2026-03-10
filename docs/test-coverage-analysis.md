# Test Coverage Analysis

This document catalogs what is and isn't covered by the current test suite, identifies gaps that could be filled in envtest, identifies gaps that require a real cluster, and then addresses the `approximate_backlog_count`→HPA autoscaling layer as a distinct testing concern.

---

## The Controller's Contract

Before analyzing coverage, it helps to be explicit about what the controller is actually promising. The contract can be grouped into five areas:

### 1. Deployment Lifecycle
- Creates a versioned `Deployment` for each unique Build ID derived from the TWD's pod template
- Rolls the pod template when the image or pod spec changes (rolling update)
- Rolls the pod template when the `TemporalConnection` spec changes (e.g., mTLS cert rotation)
- Scales drained versions to 0 replicas after `scaledownDelay`
- Deletes drained Deployments after `deleteDelay`
- Respects the max-ineligible-versions limit to prevent unbounded version accumulation
- Correctly sets TEMPORAL_* env vars so workers register the right Build ID with the right Temporal namespace

### 2. Traffic Routing (Temporal Version State)
- Registers new Build IDs with Temporal before routing traffic
- Sets a version as Current immediately (AllAtOnce) or after progressive ramp completion
- Blocks promotion when a gate workflow fails
- Blocks promotion when an external identity has modified the Temporal routing config
- Skips progressive promotion steps when unversioned pollers are NOT present and Current Version is unversioned.
- Re-grants controller authority when `temporal.io/ignore-last-modifier=true` is set
- Correctly tracks per-version drainage and status (Inactive → Ramping → Current → Draining → Drained)

### 3. TemporalWorkerOwnedResource (TWOR)
- Creates one copy of the attached resource per worker version with a running Deployment
- Auto-injects `scaleTargetRef` (when null) to point at the correct versioned Deployment
- Auto-injects `selector.matchLabels` (when null) with the correct per-version labels
- Applies via SSA with a per-TWOR field manager (stable across reconcile loops)
- Writes per-Build-ID apply status back to `TWOR.status.versions`
- Sets owner ref on the TWOR itself pointing to the TWD (GC if TWD deleted)
- Sets owner ref on each per-Build-ID resource copy pointing to the versioned Deployment (GC on sunset)

### 4. Webhook Admission Control (TWOR)
- Requires `apiVersion` and `kind` in `spec.object`
- Forbids `metadata.name` and `metadata.namespace` in `spec.object`
- Rejects banned resource kinds (Deployment, StatefulSet, Job, Pod, CronJob by default)
- Rejects `minReplicas: 0` in specs with a `minReplicas` field (such as HPA)
- Rejects non-null `scaleTargetRef` or `selector.matchLabels` (controller owns these)
- Enforces SAR: the requesting user must be able to create/update the embedded resource type
- Enforces SAR: the controller's service account must be able to create/update the embedded resource type
- Makes `workerRef.name` immutable

### 5. Kubernetes Correctness
- All controller-created resources have the TWD as their controller owner reference
- All per-Build-ID `TemporalWorkerOwnedResource` copies have the versioned Deployment as their controller owner reference
- Build ID naming is deterministic and DNS-safe
- `TemporalWorkerOwnedResource`-derived resource names are unique per `(twdName, tworName, buildID)` triple and ≤47 chars

---

## Current Test Environments

### Pure Unit Tests
No Kubernetes API server, no Temporal server. Tests call functions directly.

**Files:**
- `api/v1alpha1/temporalworker_webhook_test.go` — TWD webhook spec validation
- `api/v1alpha1/temporalworkerownedresource_webhook_test.go` — TWOR webhook spec validation (no SAR)
- `internal/k8s/deployments_test.go` — build ID computation, deployment spec generation, health detection
- `internal/k8s/ownedresources_test.go` — owned resource naming, auto-injection, template rendering
- `internal/planner/planner_test.go` — plan generation logic (create/scale/update/delete decisions)
- `internal/controller/state_mapper_test.go` — Temporal state → TWD status mapping
- `internal/temporal/worker_deployment_test.go` — workflow status mapping, test workflow ID generation
- `internal/controller/k8s.io/utils/utils_test.go` — hash utilities

### Envtest + Real Temporal Server (Integration Tests)
Full k8s fake API (envtest) with a real in-process Temporal server (`temporaltest.TestServer`). No real pods, no real controllers (HPA controller, GC controller, etc.), no webhook server registered, no cert-manager.

**File:** `internal/tests/internal/integration_test.go` (39 subtests)

### Webhook Suite (Envtest + Webhook Server)
Real webhook server registered with TLS, but no Temporal server. Tests webhook HTTP responses directly.

**File:** `api/v1alpha1/temporalworkerownedresource_webhook_integration_test.go` (5 subtests via Ginkgo suite in `webhook_suite_test.go`)

---

## What Envtest Integration Tests Currently Cover

| Contract area | Covered? | Notes |
|---|---|---|
| Creates versioned Deployment for target Build ID | ✅ | All strategy subtests |
| Rolling update on pod spec change | ✅ | `manual-rollout-custom-build-expect-rolling-update` |
| Rolling update on connection spec change | ✅ | `connection-spec-change-rolling-update` |
| Scales deprecated versions to 0 after scaledownDelay | ✅ | `*-scale-down-deprecated-versions` |
| Deletes deprecated Deployments after deleteDelay | ✅ | `7th-rollout-unblocked-after-pollers-die-version-deleted` |
| Max-ineligible-versions blocks rollout | ✅ | `*-blocked-at-max-versions*` |
| TEMPORAL_* env vars correctly set | ✅ | Worker actually polls and registers with Temporal |
| AllAtOnce: promotes immediately | ✅ | `all-at-once-*` subtests |
| AllAtOnce: gate workflow blocks/unblocks | ✅ | `all-at-once-*-gate-*` subtests |
| Progressive: blocks on unversioned pollers | ✅ | `progressive-rollout-yes-unversioned-pollers-*` |
| Progressive: first ramp step | ✅ | `progressive-rollout-expect-first-step` |
| Progressive: full ramp progression | ✅ | `progressive-ramp-to-current` |
| Progressive: gate blocks ramping | ✅ | `progressive-rollout-with-failed-gate` |
| Progressive: gate success + pollers → ramp | ✅ | `progressive-rollout-success-gate-expect-first-step` |
| External modifier blocks controller | ✅ | `*-blocked-by-modifier` |
| IgnoreLastModifier unblocks controller | ✅ | `*-unblocked-by-modifier-with-ignore` |
| TTL-based cleanup when pollers die | ✅ | `6th-rollout-*` and `7th-rollout-*` |
| TWOR: creates HPA per worker version | ✅ | `twor-creates-hpa-per-build-id` |
| TWOR: scaleTargetRef auto-injected | ✅ | `twor-creates-hpa-per-build-id` |
| TWOR: status.versions Applied:true | ✅ | `twor-creates-hpa-per-build-id` |
| TWOR: TWD owner ref set on TWOR | ✅ | `twor-creates-hpa-per-build-id` |
| TWOR: versioned Deployment owner ref on resource copy | ✅ | `twor-deployment-owner-ref` |
| TWOR: matchLabels auto-injected | ✅ | `twor-matchlabels-injection` |
| TWOR: multiple TWORs on same TWD | ✅ | `twor-multiple-twors-same-twd` |
| TWOR: template variables ({{ .DeploymentName }}) | ✅ | `twor-template-variable` |
| TWOR: multiple active versions (current + ramping) | ✅ | `twor-multiple-active-versions` |
| TWOR: apply failure → status.Applied:false | ✅ | `twor-apply-failure` |
| TWOR: SSA idempotency across reconcile loops | ✅ | `twor-ssa-idempotency` |
| TWOR: cleanup on rollout (old version TWOR instances) | ❌ | GC not present in envtest |
| Gate input from ConfigMap | ✅ | `gate-input-from-configmap` |
| Gate input from Secret | ✅ | `gate-input-from-secret` |
| Multiple deprecated versions in status | ✅ | `multiple-deprecated-versions` |
| Webhook: spec validation (no API) | ✅ | Comprehensive unit tests |
| Webhook: SAR checks (end-to-end admission) | ✅ | `twor-webhook-*` tests in webhook suite |
| Webhook: cert-manager TLS | ❌ | No cert-manager in envtest |

---

## Test Cases That Could Be Added to Envtest

These are all exercisable within the current envtest + real Temporal framework. They validate real reconciler behavior without requiring real pods, real RBAC, or real cert-manager.

### TWOR Gaps (high value) — all implemented ✅

**1. TWOR: Deployment owner reference on per-Build-ID resource copy** ✅ `twor-deployment-owner-ref`

**2. TWOR: matchLabels auto-injection end-to-end** ✅ `twor-matchlabels-injection`

**3. TWOR: multiple TWORs on the same TWD** ✅ `twor-multiple-twors-same-twd`

**4. TWOR: template variable rendering** ✅ `twor-template-variable`

**5. TWOR: multiple active versions (current + ramping)** ✅ `twor-multiple-active-versions`

**6. TWOR: apply failure → status.Applied:false** ✅ `twor-apply-failure`

**7. TWOR: SSA idempotency** ✅ `twor-ssa-idempotency`

### Rollout Gaps (medium value)

**8. Progressive rollout: full ramp progression to Current** ✅ `progressive-ramp-to-current`

**9. Connection spec change triggers rolling update** ✅ `connection-spec-change-rolling-update`
The test corrupts the `ConnectionSpecHashAnnotation` directly on the live Deployment and asserts the controller repairs it, verifying the hash-comparison path in UpdateDeployments.

**10. Gate input from ConfigMap** ✅ `gate-input-from-configmap`

**10b. Gate input from Secret** ✅ `gate-input-from-secret`

**11. TWD reconciles correctly after namespace-scoped controller restart**
Delete the controller pod mid-reconcile (or pause/resume the manager) and verify the next reconcile loop converges to the expected state without creating duplicate resources. (Can be simulated by stopping and restarting the manager goroutine in tests.)

### Status/State Mapping Gaps (lower value but useful)

**12. Status accurately reflects replica counts**
Assert that `TWD.status.targetVersion.replicas` (if reported) matches what's actually in the Deployment spec.

**13. Multiple DeprecatedVersions status entries** ✅ `multiple-deprecated-versions`

### Webhook / Admission Control (envtest-capable) — all implemented ✅

**14. Webhook: invalid TWOR spec is rejected (end-to-end admission call)** ✅

**15. Webhook: SAR pass — user with permission can create TWOR embedding HPA** ✅

**16. Webhook: SAR fail — user without permission is rejected** ✅

**17. Webhook: SAR fail — controller SA lacks permission** ✅

**18. Webhook: workerRef.name immutability enforced via real API admission** ✅

---

## Test Cases That Cannot Be Handled by Envtest

These require a real Kubernetes cluster with additional components.

### Webhook & Admission Control

**W1. cert-manager TLS provisioning end-to-end**
envtest handles webhook TLS itself (generates its own CA/cert). What cannot be tested in envtest is the cert-manager-specific mechanism — that the `cert-manager.io/inject-ca-from` annotation causes cert-manager to populate the `caBundle` field, and that the resulting cert is valid for the webhook service DNS name. This requires cert-manager running in a real cluster.

### Kubernetes Garbage Collection

**G1. Per-Build-ID TWOR resource copies deleted when versioned Deployment is deleted**
The owner reference on each TWOR copy points to the versioned Deployment. When a version sunsets and the Deployment is deleted, k8s GC should delete the corresponding TWOR copies (e.g., the per-version HPA). envtest has no GC controller; this must be tested in a real cluster.

**G2. TWOR itself deleted (and all copies) when TWD is deleted**
The TWOR has the TWD as its owner reference. Deleting the TWD should cascade to the TWOR and its resource copies. GC chain: TWD deletion → TWOR deletion → per-version resource copies deletion.

**G3. All TWD-owned Deployments deleted when TWD is deleted**
Similar GC chain for the Deployments themselves.

### HPA & Autoscaling Behavior

**H1. HPA actually reacts to CPU/memory metrics**
Requires metrics-server in the cluster, real pods with real CPU load. The HPA controller (absent from envtest) must read metrics and update `DesiredReplicas`. This is really testing Kubernetes HPA behavior, not the controller's behavior — but validating the integration matters.

**H2. KEDA ScaledObject reacts to custom metrics**
If users attach KEDA ScaledObjects via TWOR, KEDA must be installed, the scaler must be configured, and KEDA must successfully scale the versioned Deployment.

**H3. Per-version HPA scaleTargetRef correctly isolated**
In a multi-version rollout (current + ramping), each versioned Deployment has its own HPA. The v1 HPA should scale only v1 pods; the v2 HPA should scale only v2 pods. This requires real pods and HPA controller to verify.

### Real Worker & Temporal Integration

**T1. Real workers poll correct task queues and register Build IDs**
The integration tests fake worker readiness — workers don't actually run. A real cluster test with actual Temporal workers running in pods verifies that the env var injection is correct end-to-end (workers register with the Temporal server using the Build ID the controller computed).

**T2. Workflows execute correctly during a rolling rollout**
Workflows started on v1 continue to run on v1 workers while v2 workers serve new workflows. This is the core user-facing promise — it requires real workflows, real workers, and a real Temporal server.

**T3. Unversioned poller detection with real workers**
If a worker pod is deployed without Worker Versioning (legacy mode), the controller must detect it and block promotion. Real cluster test with a deliberately unversioned worker pod.

**T4. mTLS cert rotation triggers rolling update**
Update the `TemporalConnection`'s `mutualTLSSecretRef` Secret (rotate the cert) and verify the controller detects the connection spec change and rolls all versioned Deployments. Requires real pods and a real TLS-terminated Temporal endpoint.

### Infrastructure & Operations

**I1. Helm chart installation produces working controller**
`helm install` → controller pod starts → CRDs registered → RBAC correct → webhook functional. This is the primary per-release smoke test.

**I2. Helm chart upgrade is non-disruptive**
`helm upgrade` → controller rolling update → no reconcile loop gaps → no duplicate resource creation during the upgrade.

**I3. Multi-replica controller with leader election**
Two controller replicas — one holds the lease, one stands by. Kill the leader → standby promotes within expected time and resumes reconciliation. Requires real pods with real leader election.

**I4. Controller pod restart mid-reconcile is idempotent**
Kill the controller pod while it's partway through an apply loop. The next reconcile from the new pod should converge to the same state without creating duplicates or losing TWOR apply records.

---

## The `approximate_backlog_count` → HPA Autoscaling Layer

This is a distinct testing concern from the controller's own reconciliation logic. The question is: can Temporal's `approximate_backlog_count` metric be used as a reliable HPA scaling signal, and how do we define and observe "reliable" in an automated test?

### What the metric is

`approximate_backlog_count` is a Temporal server metric (exposed via Prometheus) that reports the approximate number of tasks waiting in a task queue at a point in time. It is approximate because Temporal's task queue implementation uses multiple internal partitions and the count is a snapshot.

The metric carries a `worker_version` label with the format `<worker_deployment_name>:<build_id>`. This means per-version backlog isolation is possible via label filtering. Example PromQL:

```
approximate_backlog_count{
  namespace="my-temporal-ns",
  task_queue="my-task-queue",
  worker_version="my-worker:v1-0-abcd"
}
```

Each versioned HPA queries a different `worker_version` label value, giving independent scaling signals for each active Build ID.

**Note on minReplicas**: The TWOR webhook rejects `minReplicas: 0` because `approximate_backlog_count` stops being emitted when the task queue goes idle with no pollers. If all pods for a version were scaled to zero, the metric would disappear and the HPA could never detect new backlog to scale back up. The `minReplicas ≥ 1` constraint is intentional and necessary for metric-driven autoscaling to work correctly.

### Recommended autoscaling architecture

**Do not use the KEDA Temporal scaler for versioned worker autoscaling.** The KEDA Temporal scaler queries the Temporal API directly but does not scope to a specific `worker_version`. It sees the total queue backlog across all versions, which would cause all per-version HPAs to react to all traffic regardless of which version it targets.

The correct architecture is:

1. **Temporal server** exposes `approximate_backlog_count` (with `worker_version` label) via Prometheus metrics endpoint
2. **Prometheus** scrapes the Temporal server and stores the metric
3. **Prometheus Adapter** exposes `approximate_backlog_count` to the Kubernetes custom metrics API, with label selectors preserved
4. **Standard HPA** (not KEDA) with `external` metric type queries `approximate_backlog_count{worker_version="my-worker:v1-0-abcd"}` for each versioned Deployment

Each TWOR-created HPA is scoped to a specific `worker_version` value, providing true per-version scaling isolation.

### Defining "accurate"

A metric is accurate if:

1. **Sign is correct**: When backlog is growing, the metric increases. When backlog is draining, it decreases. A metric that trends in the wrong direction would cause inverse scaling (scale down under load, scale up at idle).
2. **Magnitude is proportional**: The metric value should be proportional to the actual queue depth within a reasonable factor (say, 2×). Gross under- or over-reporting would result in too-slow or too-aggressive scaling.
3. **Lag is bounded**: The Prometheus scrape interval + HPA poll interval + metric server cache adds latency. "Accurate" requires this latency to be bounded and predictable — if the lag exceeds the HPA's stabilization window, scaling decisions will be based on stale data and thrashing can occur.
4. **Build-ID scoping is correct**: The metric for `worker_version="my-worker:v1-0-abcd"` must not bleed into `worker_version="my-worker:v2-0-efgh"`. The `worker_version` label provides this isolation natively.

### Defining "successful" autoscaling

A scaling action is successful if:

1. **Scale-up timeliness**: After backlog crosses the target threshold, new pods are scheduled and ready within a defined SLO (e.g., `p90 < 3 minutes` from metric threshold breach to new pod ready).
2. **Scale-down correctness**: After backlog drains below the threshold (accounting for the HPA stabilization window), replicas return to `minReplicas` within the stabilization window + 1 scrape interval.
3. **No thrashing**: Replica count should not oscillate more than once per stabilization window under steady-state load.
4. **No over-scaling**: Replicas should not exceed `maxReplicas` regardless of backlog depth.
5. **Per-version isolation**: Scaling of v1 workers does not affect v2 worker replica count.

### What an automated test would look like

This test cannot run in envtest. It requires a real cluster with:
- Temporal server with Prometheus metrics exposed (including `approximate_backlog_count` with `worker_version` label)
- Prometheus scraping the Temporal server
- Prometheus Adapter configured to expose `approximate_backlog_count` to the k8s custom metrics API
- The temporal-worker-controller installed with a running TWD
- `TemporalWorkerOwnedResource` with an HPA using `external` metric type applied
- A workflow client that can generate controlled bursts of tasks
- Access to the k8s metrics API and/or Prometheus query API

**Test outline:**

```
Phase 1 — Baseline
  - Assert: backlog metric = 0, replicas = minReplicas
  - Wait: 1 stabilization window

Phase 2 — Load spike
  - Action: enqueue N tasks without any workers polling
    (ensures backlog builds; set N = maxReplicas × targetMetricValue × 2)
  - Assert: backlog metric rises within 2 scrape intervals (accuracy check)
  - Assert: HPA DesiredReplicas = maxReplicas within 1 poll cycle
  - Assert: actual replicas = maxReplicas within scale-up SLO
  - Observe: replica count timeline (detect thrashing)

Phase 3 — Drain
  - Action: start workers, drain the queue
  - Assert: backlog metric falls to 0 within 2 scrape intervals
  - Assert: replicas return to minReplicas within stabilization window + buffer

Phase 4 — Per-version isolation (requires two active versions)
  - Setup: progressive rollout with v1 current + v2 ramping; each version has its own
    HPA querying approximate_backlog_count{worker_version="<name>:<buildID>"}
  - Action: enqueue tasks targeting v1 only
  - Assert: v1 replicas scale up, v2 replicas stay at minReplicas
  - Action: enqueue tasks targeting v2 only
  - Assert: v2 replicas scale up, v1 replicas stay at minReplicas

Phase 5 — Metric accuracy spot-check
  - Action: enqueue exactly K tasks without polling
  - Assert: backlog metric value is within [K/2, 2K] (proportionality bound)
  - Assert: after polling K tasks, metric returns to 0 within 2 scrape intervals
```

**Observable success criteria for CI:**
- All `Assert` steps pass within their time bounds
- No replica oscillation (more than 1 direction change per phase)
- Metric lag < 2× scrape interval for any transition
- Per-version scaling delta = 0 in isolation phases (v2 replicas unchanged when only v1 queue has load)

**Open questions to resolve before implementing:**

1. **Stabilization parameters**: What HPA stabilization window and scale-up/scale-down policies should be recommended? These directly affect the timeliness SLO and thrashing behavior.

2. **Approximation error**: What is the documented approximation error of `approximate_backlog_count`? The Temporal server uses multiple partitions and the count is eventually consistent. The accuracy bounds in the test above (factor of 2) should be calibrated against actual observed behavior.

3. **CI infrastructure**: What cluster will run these tests? They need Prometheus, Prometheus Adapter, real Temporal (Cloud or self-hosted), and enough nodes to actually schedule pods. The current CI runs envtest only.

---

## Priority Recommendations

**Add to envtest next (high value, low cost):**
All high-value envtest gaps (TWOR 1–7, rollout 8–10, 13, webhook 14–18) are now implemented.
Remaining envtest candidates: rollout 11 (manager restart idempotency), rollout 12 (replica count in status).

**Add to per-release real-cluster CI (must-haves before 1.0):**
1. Helm install smoke test (controller starts, CRDs registered, webhook functional)
2. TWOR webhook: cert-manager TLS provisioning end-to-end (W1)
3. GC: TWOR copies deleted when versioned Deployment sunset
4. Real worker polling and Build ID registration
5. Workflow execution continuity during rollout

**`approximate_backlog_count` autoscaling (deferred, needs infrastructure):**
1. Set up Prometheus + Prometheus Adapter in CI cluster (KEDA Temporal scaler not suitable for per-version use)
2. Define HPA stabilization parameters before writing assertions
3. Implement as a dedicated test job in the per-release pipeline
