# Local Development Setup

This guide will help you set up and run the Temporal Worker Controller locally using Minikube.

### Prerequisites

- [Minikube](https://minikube.sigs.k8s.io/docs/start/)
- [Helm](https://helm.sh/docs/intro/install/) **v3.x** — the charts are tested against v3.14.3 (`HELM_VERSION` in the Makefile), and Skaffold shells out to whichever `helm` is on your `PATH`. Helm 4 is not tested here; if your package manager gave you Helm 4, run `make helm-dependency-build` and put the pinned binary first: `export PATH="$PWD/bin:$PATH"`.
- [Skaffold](https://skaffold.dev/docs/install/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
- A Temporal server, either:
  - a **Temporal Cloud** account with an API key or mTLS certificates, or
  - a **local dev server** — no account, no certificates. See [Option B](#option-b-local-dev-server-no-credentials) below.
- Understanding of [Worker Versioning concepts](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning) (Pinned and Auto-Upgrade versioning behaviors)
- cert-manager is required for the `WorkerResourceTemplate` validating webhook (TLS). The controller Helm chart installs it automatically as a subchart (`certmanager.install: true` is set in the Skaffold profile).
- The `jetstack` Helm repo must be registered **in the Helm configuration Skaffold sees**, or `skaffold run --profile worker-controller` fails with `building helm dependencies: exit status 1`. `make helm-dependency-build` deliberately writes to an isolated repo config under `bin/` so it never touches your global Helm setup, which means Skaffold does not inherit it. Either register it globally once:

  ```bash
  helm repo add jetstack https://charts.jetstack.io
  ```

  or point Skaffold at the isolated config:

  ```bash
  export HELM_REPOSITORY_CONFIG="$PWD/bin/helm-repositories.yaml"
  export HELM_REPOSITORY_CACHE="$PWD/bin/helm-repository-cache"
  ```

> **Note**: This demo specifically showcases **Pinned** workflow behavior. All workflows in the demo will remain on the worker version where they started, demonstrating how the controller safely manages multiple worker versions simultaneously during deployments.

### Running the Local Demo

1. Start a local Minikube cluster:
   ```bash
   minikube start
   ```

2. Create the `skaffold.env` file:
   ```bash
   cp skaffold.example.env skaffold.env
   ```

   Then fill it in using **either** Option A (Temporal Cloud) or Option B (local dev server).

#### Option A: Temporal Cloud

   Set `TEMPORAL_NAMESPACE` and `TEMPORAL_ADDRESS` in `skaffold.env` to match your namespace,
   then configure one of mTLS or API key authentication.

   **Using mTLS**
   - Create a `certs` directory in the project root
   - Save your Temporal Cloud mTLS client certificates as:
     - `certs/client.pem`
     - `certs/client.key`
   - Create the Kubernetes secret:
     ```bash
     make create-cloud-mtls-secret
     ```
   - In `skaffold.env`, set:
     ```env
     TEMPORAL_API_KEY_SECRET_NAME=""
     TEMPORAL_MTLS_SECRET_NAME=temporal-cloud-mtls-secret
     ```

   **Using API Keys**
   - Create a `certs` directory in the project root if not already present
   - Save your Temporal Cloud API key in a file (single line, no newline):
     ```bash
     echo -n "<YOUR_API_KEY>" > certs/api-key.txt
     ```
   - Create the Kubernetes Secret:
     ```bash
     make create-api-key-secret
     ```
   - In `skaffold.env`, set:
     ```env
     TEMPORAL_API_KEY_SECRET_NAME=temporal-api-key
     TEMPORAL_MTLS_SECRET_NAME=""
     ```
   - **Important**: When using API key authentication, you must use the regional endpoint instead of the namespace-specific endpoint. Set `TEMPORAL_ADDRESS` in `skaffold.env` to your region's endpoint, e.g.:
     ```env
     TEMPORAL_ADDRESS=us-east-1.aws.api.temporal.io:7233
     ```
     The namespace-specific endpoint (e.g. `<namespace>.tmprl.cloud:7233`) requires mTLS and will reject API key connections with a `tls: certificate required` error.
   - Note: Do not set both mTLS and API key for the same connection. If both present, the Connection Custom Resource
   Instance will not get installed in the k8s environment.

#### Option B: local dev server (no credentials)

   Runs the whole demo against `temporal server start-dev` on your host. No Temporal Cloud
   account, no certificates, nothing to rotate. Good for working on the controller itself.

   Start the server and leave it running in its own terminal:
   ```bash
   make start-temporal-server
   ```
   This binds `0.0.0.0` and enables the two dynamic configs the controller needs
   (`frontend.workerVersioningWorkflowAPIs`, `system.enableDeploymentVersions`).

   > **Make sure no other dev server is already running first.** `start-dev` does not fail if
   > port 7233 is taken: a server already bound to `127.0.0.1:7233` keeps that address, while
   > the new one binds the `*:7233` wildcard. Both then appear healthy, but the more specific
   > bind wins for anything connecting over loopback — including `host.minikube.internal` from
   > pods, which Docker Desktop routes to the host loopback. The result is a second server that
   > silently receives no traffic, and CLI output that disagrees with what the workers see.
   > Confirm there is exactly one listener:
   >
   > ```bash
   > lsof -nP -iTCP:7233 -sTCP:LISTEN
   > ```

   Then set `skaffold.env` to:
   ```env
   TEMPORAL_NAMESPACE=default
   TEMPORAL_ADDRESS=host.minikube.internal:7233
   TEMPORAL_MTLS_SECRET_NAME=""
   TEMPORAL_API_KEY_SECRET_NAME=""
   TEMPORAL_API_KEY_SECRET_KEY=""
   SKAFFOLD_KUBE_CONTEXT=minikube
   ```

   Leaving both secret names empty makes `ConnectionSpec.AuthMode()` resolve to
   `NO_CREDENTIALS` (see `api/v1alpha1/connection_types.go`), and the controller then injects
   no TLS or API-key environment variables into the worker pods. The worker builds its client
   with the Go SDK's `envconfig`, which defaults to plaintext, so it connects without further
   configuration.

   `host.minikube.internal` is how a pod reaches a process listening on your host. Verify it
   before going further — if this fails, nothing downstream will work:
   ```bash
   kubectl run nettest --rm -i --restart=Never --image=busybox:1.36 -- \
     nc -z -w 5 host.minikube.internal 7233 && echo REACHABLE
   ```

   > **Note**: the backlog metric comes from a different source locally — the dev server's own
   > `/metrics` endpoint rather than Temporal Cloud. The demo dashboard queries both, so the
   > "Task Backlog" panels work either way. See [Grafana Dashboard](#grafana-dashboard).

3. Build and deploy the Controller image to the local k8s cluster:
   ```bash
   skaffold run --profile worker-controller
   ```

   This installs cert-manager, the CRDs chart, and the controller. If it fails with
   `building helm dependencies: exit status 1`, the `jetstack` repo is not registered in the
   Helm config Skaffold sees — see [Prerequisites](#prerequisites).

### Testing Progressive Deployments

> **`WORKER_VERSION` is required** for every `skaffold run --profile helloworld-worker` invocation. It drives the image tag (and therefore the Temporal build ID), so each deploy must use a fresh value (`v1`, `v2`, …). If unset, skaffold silently falls back to tagging the image `:latest` while helm renders `image.tag` as `<no value>`, which deploys a broken pod.

4. **Deploy the v1 worker**:
   ```bash
   WORKER_VERSION=v1 skaffold run --profile helloworld-worker
   ```
   This deploys a WorkerDeployment and Connection Custom Resource using the **Progressive strategy**. Note that when there is no current version (as in an initial versioned worker deployment), the progressive steps are skipped and v1 becomes the current version immediately. All new workflow executions will now start on v1.
   
5. Watch the deployment status:
   ```bash
   watch kubectl get workerdeployment
   ```

6. **Apply load** to the v1 worker to simulate production traffic:
    ```bash
    make apply-load-sample-workflow          # Temporal Cloud (Option A)
    make apply-load-sample-workflow-local    # local dev server (Option B)
    ```

    > The non-`-local` targets read `TEMPORAL_ADDRESS` from `skaffold.env` and always pass
    > `--tls-cert-path certs/client.pem`, so they only work with Option A. The `-local`
    > variants talk to `127.0.0.1:7233` with no credentials. (`host.minikube.internal` from
    > `skaffold.env` resolves only inside the cluster, not on your host.)

#### **Progressive Rollout of v2** (Non-Replay-Safe Change)

7. **Deploy a non-replay-safe workflow change**:
   ```bash
   git apply internal/demo/helloworld/changes/no-version-gate.patch
   WORKER_VERSION=v2 skaffold run --profile helloworld-worker
   ```
   This applies a **non-replay-safe change** (switching an activity response type from string to a struct).

8. **Observe the progressive rollout managing incompatible versions**:
   - New workflow executions gradually shift from v1 to v2 following the configured rollout steps (25% → 50% → 75% → 100%, with a 120s pause at each step — see `internal/demo/helloworld/helm/helloworld/templates/deployment.yaml`)
   - **Both worker versions run simultaneously** - this is critical since the code changes are incompatible
   - v1 workers continue serving existing workflows (which would fail to replay on v2)
   - v2 workers handle new workflow executions with the updated code
   - This demonstrates how **Progressive rollout** safely handles breaking changes when you have existing traffic

### Monitoring 

You can monitor the controller's logs and the worker's status using:
```bash
# Output the controller pod's logs
kubectl logs -n temporal-system deployments/temporal-worker-controller-manager -f

# View WorkerDeployment status
kubectl get workerdeployment
```

### Testing WorkerResourceTemplate (per-version HPA)

`WorkerResourceTemplate` lets you attach Kubernetes resources — HPAs, PodDisruptionBudgets, etc. — to each worker version with running workers. The controller creates one copy per worker version with a running Deployment and wires it to the correct Deployment automatically.

The `WorkerResourceTemplate` validating webhook enforces that you have permission to create the embedded resource type yourself, and it requires TLS (provided by cert-manager, installed in step 3 above).

After deploying the helloworld worker (step 5), apply the example HPA:

```bash
kubectl apply -f examples/wrt-hpa.yaml
```

Watch the controller create an HPA for each worker version with running workers:

```bash
# See WorkerResourceTemplate status (Applied: true once the controller reconciles)
kubectl get WorkerResourceTemplate

# See the per-Build-ID HPAs
kubectl get hpa
```

You should see one HPA per worker version with running workers, with `scaleTargetRef` automatically pointing at the correct versioned Deployment.

When you deploy a new worker version (e.g., step 8), the controller creates a new HPA for the new Build ID and keeps the old one until that versioned Deployment is deleted during the sunset process.

See [docs/worker-resource-templates.md](../../docs/worker-resource-templates.md) for full documentation.

> **Note**: If you plan to continue to the Metric-Based HPA Scaling Demo below, delete this WRT before proceeding. Two WRTs targeting the same WorkerDeployment with the same resource kind will create conflicting HPAs.
> ```bash
> kubectl delete -f examples/wrt-hpa.yaml
> ```

---

### Grafana Dashboard

A pre-built Grafana dashboard is included at `internal/demo/k8s/grafana-dashboard.json`. It shows:
- HPA current vs desired replicas per version
- Activity slot utilization per version
- Workflow and activity task backlog per version
- Workflow and activity task dispatch rate per task queue and build ID
- Raw per-pod slot gauges (used vs available)

> **Install the monitoring stack first.** Grafana, Prometheus and kube-state-metrics all come
> from `kube-prometheus-stack` — see [Metric-Based HPA Scaling Demo → Prerequisites](#prerequisites-1)
> below, then come back here.

**Load the dashboard** as a ConfigMap. Grafana's sidecar watches for ConfigMaps labelled
`grafana_dashboard=1` and imports them automatically, so this survives Grafana restarts and
needs no clicking through the UI:

```bash
kubectl create configmap twc-hpa-scaling-dashboard -n monitoring \
  --from-file=twc-hpa-scaling.json=internal/demo/k8s/grafana-dashboard.json \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl label configmap twc-hpa-scaling-dashboard -n monitoring grafana_dashboard=1 --overwrite
```

**Open it:**

```bash
kubectl -n monitoring port-forward svc/prometheus-grafana 3000:80 &
```

Then go straight to <http://localhost:3000/d/twc-hpa-scaling>.

No login is required: `prometheus-stack-local-values.yaml` enables anonymous Admin access and
disables the login form. That is safe here only because the cluster is local and reachable
solely through `kubectl port-forward` — never use those Grafana settings anywhere else. If you
installed **without** that values file, Grafana will prompt for credentials; retrieve them with:

```bash
kubectl get secret --namespace monitoring -l app.kubernetes.io/component=admin-secret \
  -o jsonpath="{.items[0].data.admin-password}" | base64 --decode ; echo
```

The dashboard auto-refreshes every 10s and defaults to a 30-minute time window. Use it to tune HPA targets and observe per-version scaling behaviour during progressive rollouts.

> **The two "Task Backlog" panels read from a different source depending on your setup.**
> Backlog is not an SDK metric — no worker emits it — so it has to come from the server:
>
> | Setup | Series | Scraped from |
> |---|---|---|
> | Temporal Cloud | `temporal_cloud_v1_approximate_backlog_count` | `metrics.temporal.io` |
> | Local dev server | `approximate_backlog_count` | the server's own `/metrics` port |
>
> The label names differ too (`temporal_worker_deployment_name` vs `worker_deployment_name`),
> and the self-hosted series is split across task queue partitions, so it needs a `sum by`.
> Each panel therefore carries two queries — `A` for self-hosted, `B` for Cloud — and whichever
> source is present populates the panel while the other returns nothing.
>
> The local series is only exported if the dev server was started with a fixed metrics port;
> `make start-temporal-server` pins it to `LOCAL_TEMPORAL_METRICS_PORT` (default 7239) and
> `prometheus-stack-local-values.yaml` scrapes it. Without `--metrics-port`, `start-dev` picks a
> random free port on every start that no scrape config can target.
>
> A backlog of `0` is the expected steady state once the HPA has scaled out — the panel plots a
> real series at zero rather than showing "No data". To see it climb, raise the workflow rate or
> cap the HPA's `maxReplicas`.

> **The two "Task Dispatch Rate" panels split the same way**, and the units differ in a way that
> matters. They show tasks successfully matched to a poller, per second:
>
> | Setup | Series | Type | Query | Grouped by |
> |---|---|---|---|---|
> | Temporal Cloud | `temporal_cloud_v1_poll_success_count` | already a per-second rate | use directly | task queue only |
> | Local dev server | `poll_success` | counter | wrap in `rate(...[1m])` | task queue **and build ID** |
>
> Applying `rate()` to the Cloud series would be wrong — `temporal_cloud_v1_*` metrics are
> pre-computed rates. Applying it to the self-hosted counter is required. Each panel carries both
> queries, so only the one matching your setup returns data.
>
> **Per-build-ID breakdown is only available self-hosted.** `poll_success` carries
> `worker_build_id`, so the local query groups by it and you can watch dispatch shift between
> versions during a rollout. The Cloud metric does not: `temporal_worker_build_id` is an opt-in
> label on `temporal_cloud_v1_approximate_backlog_count`, but `poll_success_count` only exposes
> `operation`, `task_type` and `temporal_task_queue` — so query B stays grouped by task queue.
>
> Series with an empty build ID (system task queues, sticky queues, unversioned pollers) are
> relabelled `unversioned` via `label_replace` rather than dropped, so the panel totals stay
> honest.

---

### Metric-Based HPA Scaling Demo

This section demonstrates **per-version autoscaling** on real Temporal metrics: worker slot utilization (emitted by the worker pods) and approximate backlog count (from Temporal Cloud). The goal is a steady state of ~10 replicas per version, with each version's HPA responding independently during a progressive rollout.

The demo is structured in two phases so you can verify each layer before building on it.

> **Why the worker has only 5 activity slots per pod in this demo:** The Go SDK default is 1,000 slots per pod, which would require an impractically high workflow rate to saturate. The demo worker is configured with `MaxConcurrentActivityExecutionSize: 5` so that ~2 workflows/second drives 10 replicas at 70% utilization. Remove this limit in production.

#### Prerequisites

In addition to the main demo prerequisites, you need `kube-prometheus-stack` with `prometheus-adapter` as a subchart. This provides Prometheus (to scrape worker metrics and Temporal Cloud), a recording rule (to compute the utilization ratio), and the External Metrics API bridge that HPAs use.

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

# Temporal Cloud (Option A):
helm upgrade --install prometheus prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  -f internal/demo/k8s/prometheus-stack-values.yaml

# Local dev server (Option B) — adds the local overlay:
helm upgrade --install prometheus prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  -f internal/demo/k8s/prometheus-stack-values.yaml \
  -f internal/demo/k8s/prometheus-stack-local-values.yaml

helm upgrade --install prometheus-adapter prometheus-community/prometheus-adapter \
  -n monitoring \
  -f internal/demo/k8s/prometheus-adapter-values.yaml

kubectl apply -f internal/demo/k8s/servicemonitor.yaml
```

> **Option B users must pass `prometheus-stack-local-values.yaml`.**
> `prometheus-stack-values.yaml` mounts the secret `temporal-cloud-api-key` and adds a scrape
> job against `metrics.temporal.io`. Without that secret the Prometheus pod never leaves
> `ContainerCreating`. The overlay empties both, and additionally turns on anonymous Grafana
> access. It keeps the `temporal_slot_utilization` recording rule, which is computed from
> worker SDK metrics and needs no Temporal Cloud.

Wait for the stack to be ready:
```bash
kubectl -n monitoring rollout status deployment/prometheus-adapter
```

#### Phase 1: Scale on slot utilization

Slot utilization measures what fraction of each pod's activity task slots are in use. When workers are busy, the HPA adds replicas; when they drain, it removes them.

**Step 1 — Verify metrics are flowing.**

Port-forward Prometheus and confirm the recording rule is producing values:
```bash
kubectl -n monitoring port-forward svc/prometheus-kube-prometheus-prometheus 9090 &
# In a browser or with curl:
# http://localhost:9090/graph?g0.expr=temporal_slot_utilization
```

If `temporal_slot_utilization` returns no data, check the metric names a worker actually
emits. The worker image is distroless — it has no shell, `curl` or `wget` — so `kubectl exec`
will fail with `executable file not found in $PATH`. Port-forward to the pod instead:
```bash
kubectl port-forward -n default \
  $(kubectl get pods -n default -l temporal.io/deployment-name=helloworld -o name | head -1) \
  19090:9090 &
curl -s localhost:19090/metrics | grep -i slot
```

Also confirm Prometheus actually picked up the ServiceMonitor — it takes a reconcile plus a
config-reload cycle (up to ~1 minute) after `kubectl apply`, and an empty result before then is
expected:
```bash
kubectl -n monitoring port-forward svc/prometheus-kube-prometheus-prometheus 9090:9090 &
curl -s localhost:9090/api/v1/status/config | grep -c helloworld   # non-zero once picked up
```

Update the recording rule `expr` in `internal/demo/k8s/prometheus-stack-values.yaml` if the metric names differ, then run `helm upgrade prometheus ... -f internal/demo/k8s/prometheus-stack-values.yaml`.

**Step 2 — Apply the slot-utilization WRT.**
```bash
kubectl apply -f examples/wrt-hpa-slot-utilization.yaml
```

Confirm the HPA is reading the metric (not showing `<unknown>`):
```bash
kubectl get hpa -w
# TARGETS column should show e.g. "0/700m" within ~60 seconds
```

**Step 3 — Generate load.**
```bash
make apply-hpa-load         # Temporal Cloud (Option A); ~2 workflows/sec, Ctrl-C to stop
make apply-hpa-load-local   # local dev server (Option B)
```

Watch the pods scale up to ~10 replicas over the next few minutes:
```bash
kubectl get pods -l temporal.io/deployment-name=helloworld -w
```

Stop the load generator (`Ctrl-C`) and watch the HPA scale back down as in-flight activities complete.

#### Phase 2: Add approximate backlog count

[temporal_cloud_v1_approximate_backlog_count](https://docs.temporal.io/cloud/metrics/openmetrics/metrics-reference#temporal_cloud_v1_approximate_backlog_count) measures tasks queued in Temporal but not yet started on a worker. Adding it as a second HPA metric means the HPA scales up on *arriving* work even before slots are full — important for bursty traffic.
To ingest this metric into your cluster, you'll need to follow the instructions in the [Temporal OpenMetrics docs](https://docs.temporal.io/cloud/metrics/openmetrics) to set up a Temporal Cloud metrics API key. This is a separate credential from the namespace API key used for the worker connection.
You'll also need to [opt-in](https://docs.temporal.io/cloud/metrics/openmetrics/metrics-reference#opt-in-labels) to the `temporal_worker_deployment_name` and `temporal_worker_build_id` labels to enable per-version scaling.

This requires a **metrics API key** — a separate credential from the namespace API key used for the worker connection.

> **Picking a scaling tool for your workload:** This demo uses the HPA + prometheus-adapter path. It works well for continuously-loaded task queues and has a typical end-to-end reactivity of ~85 seconds (dominated by Temporal Cloud's ~1/minute OpenMetrics emission cadence). It cannot do scale-from-zero. For sub-60s reactivity or scale-from-zero, use the KEDA Temporal scaler. See [docs/scaling-recommendations.md](../../docs/scaling-recommendations.md) to understand the benefits and trade-offs of each approach.

**Step 1 — Create the Temporal Cloud metrics credentials secret.**

Once you have created a Temporal Cloud metrics API key at **Cloud UI → Settings → Observability → Generate API Key**, save the API key to `certs/metrics-api-key.txt`, then create the secret in the `monitoring` namespace:
```bash
kubectl create secret generic temporal-cloud-api-key \
  -n monitoring \
  --from-literal=api-key=<your-metrics-api-key>
```

> **Rotating an expired key:** If the key expires, generate a new one in the Cloud UI, then replace the secret and restart the Prometheus pod to remount it:
> ```bash
> kubectl delete secret temporal-cloud-api-key -n monitoring
> kubectl create secret generic temporal-cloud-api-key \
>   -n monitoring \
>   --from-literal=api-key=<your-new-metrics-api-key>
> kubectl delete pod -n monitoring prometheus-prometheus-kube-prometheus-prometheus-0
> ```

**Step 2 — Install or upgrade Prometheus and prometheus-adapter with the Temporal Cloud scrape config.**

```bash
helm upgrade --install prometheus prometheus-community/kube-prometheus-stack \
  -n monitoring -f internal/demo/k8s/prometheus-stack-values.yaml

helm upgrade --install prometheus-adapter prometheus-community/prometheus-adapter \
  -n monitoring -f internal/demo/k8s/prometheus-adapter-values.yaml
```

**Step 3 — Verify the backlog metric is flowing.**

```bash
kubectl -n monitoring port-forward svc/prometheus-kube-prometheus-prometheus 9092:9090 &
curl -s 'http://localhost:9092/api/v1/query?query=temporal_cloud_v1_approximate_backlog_count' \
  | jq '.data.result'
```

You should see results with `temporal_worker_deployment_name` and `temporal_worker_build_id` labels. If the result is empty, verify the Temporal Cloud metrics API key secret is correct and that scrape targets are healthy in the Prometheus UI.

**Step 4 — Apply the combined WRT.**
```bash
# Remove the Phase 1 WRT first to avoid two HPAs targeting the same Deployment
kubectl delete -f examples/wrt-hpa-slot-utilization.yaml
kubectl apply -f examples/wrt-hpa-backlog.yaml
```

#### Full progressive rollout demo

With load running, this demonstrates the core value proposition: v1 and v2 scale independently.

```bash
# Terminal 1: keep load running
make apply-hpa-load          # or: make apply-hpa-load-local  (Option B)

# Terminal 2: deploy v2 while v1 is under load
WORKER_VERSION=v2 skaffold run --profile helloworld-worker

# Terminal 3: watch the two HPAs
kubectl get hpa -w
# v1 HPA: replicas stay high while pinned workflows are running, then drop as they drain
# v2 HPA: replicas rise as new workflows are routed to v2 and its slots fill up
```

The progressive rollout steps (1% → 10% → 50% → 100%) gradually shift new workflow traffic to v2. The per-version HPAs respond to each version's actual load, not the aggregate — this is what makes the scaling correct during a deployment.

---

### Cleanup

To clean up the demo:
```bash
# Delete the demo worker and the controller
helm uninstall helloworld
helm uninstall temporal-worker-controller -n temporal-system

# Monitoring stack, if you installed it
helm uninstall prometheus-adapter -n monitoring
helm uninstall prometheus -n monitoring

# Stop Minikube
minikube stop
```

If you used Option B, also stop the `make start-temporal-server` process in its terminal
(Ctrl-C). Its state is in-memory, so everything it held disappears with it.

### Additional Operational commands

Complete cleanup (removes all clusters, cached images, and config):
```
minikube delete --all --purge
```

**What `minikube delete --all --purge` does:**
- `--all`: Deletes ALL minikube clusters (not just the default one)
- `--purge`: Completely removes all minikube data, cached images, and configuration files from your machine

This gives you a completely fresh start and frees up disk space used by minikube. 
