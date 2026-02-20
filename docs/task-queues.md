# Task Queues and TemporalWorkerDeployment

This document explains how Task Queues relate to TemporalWorkerDeployment resources and provides guidance on structuring your deployments.

## Key Concept: Task Queue is Defined in Your Code

The Task Queue is **not** configured in the TemporalWorkerDeployment spec. Instead:

1. The controller injects environment variables into your pods:
   - `TEMPORAL_ADDRESS`
   - `TEMPORAL_NAMESPACE`
   - `TEMPORAL_DEPLOYMENT_NAME`
   - `TEMPORAL_WORKER_BUILD_ID`

2. Your worker code reads these variables and specifies which Task Queue to poll.

```
┌──────────────────────────────────────────────────────────────────┐
│              TemporalWorkerDeployment                            │
│                                                                  │
│   Manages:                      Does NOT manage:                 │
│   - Replicas                    - Task Queue name(s)             │
│   - Rollout strategy            - Workflows/Activities           │
│   - Version lifecycle           - Worker business logic          │
│   - K8s Deployments                                              │
│   - Env var injection                                            │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
                         │
                         │ Creates pods with env vars
                         ▼
┌──────────────────────────────────────────────────────────────────┐
│                           Pod                                    │
│                                                                  │
│  ┌────────────────────┐  ┌────────────────────┐                  │
│  │  Container 1       │  │  Container 2       │                  │
│  │  (orders worker)   │  │  (payments worker) │   ...            │
│  │                    │  │                    │                  │
│  │  Polls: "orders"   │  │  Polls: "payments" │                  │
│  └────────────────────┘  └────────────────────┘                  │
│                                                                  │
│  Each container runs a worker process polling ONE Task Queue     │
│  All containers share the same TEMPORAL_WORKER_BUILD_ID          │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

> **Note:** Each worker process polls exactly one Task Queue. To handle multiple queues, run multiple containers.

## Grouping Multiple Task Queues in a Single TWD

A single worker codebase can process tasks for multiple Task Queues. This can be done by bundling multiple containers (each running a worker process polling its own Task Queue) into the same pod, managed by a single TemporalWorkerDeployment:

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: order-service-workers
spec:
  replicas: 5
  template:
    spec:
      containers:
      - name: orders-worker
        image: order-service:v1.0
        env:
        - name: TASK_QUEUE
          value: "orders"
      - name: payments-worker
        image: order-service:v1.0
        env:
        - name: TASK_QUEUE
          value: "payments"
      - name: notifications-worker
        image: order-service:v1.0
        env:
        - name: TASK_QUEUE
          value: "notifications"
```

Each container reads `TASK_QUEUE` to determine which queue to poll:

```go
func main() {
    taskQueue := os.Getenv("TASK_QUEUE")

    opts := worker.Options{
        DeploymentOptions: worker.DeploymentOptions{
            UseVersioning: true,
            Version: worker.Version{
                BuildId: os.Getenv("TEMPORAL_WORKER_BUILD_ID"),
            },
        },
    }

    w := client.NewWorker(taskQueue, opts)
    w.Start()
}
```

This approach can work well when:

- Task Queues are part of the same logical service
- You want to deploy and version workers for all queues together
- The queues have similar resource and scaling requirements

## When to Split Task Queues into Separate TWDs

Consider creating separate TemporalWorkerDeployment resources when Task Queues have:

**Different scaling requirements** - One queue may need 10 replicas while another needs 2:

```yaml
# High-volume order processing
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: orders-worker
spec:
  replicas: 10
  template:
    spec:
      containers:
      - name: worker
        image: order-service:v1.0
        env:
        - name: TASK_QUEUE
          value: "orders"
---
# Low-volume notifications
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: notifications-worker
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: worker
        image: order-service:v1.0
        env:
        - name: TASK_QUEUE
          value: "notifications"
```

**Different deployment cadences** - You want to roll out changes to one queue without affecting others, or test changes on a low-risk queue before rolling to critical queues.

**Different resource profiles** - One queue runs CPU-intensive activities while another is I/O-bound.

### Trade-offs of Splitting

| Benefit | Cost |
|---------|------|
| Independent scaling per queue | More Kubernetes resources to manage |
| Independent rollouts | More TWD manifests to maintain |
| Isolated failures | Coordination overhead for shared changes |
| Queue-specific resource tuning | Potential duplication if queues are similar |
