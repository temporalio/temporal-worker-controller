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
│   - Replicas                    - Task Queue name                │
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
│                    Your Worker Code                              │
│                                                                  │
│   worker := client.NewWorker(                                    │
│       taskQueue: "orders",  ◄──── YOU define this                │
│       options: {                                                 │
│         DeploymentOptions: {                                     │
│           UseVersioning: true,                                   │
│           Version: {                                             │
│             BuildId: os.Getenv("TEMPORAL_WORKER_BUILD_ID"),      │
│           },                                                     │
│         },                                                       │
│       },                                                         │
│   )                                                              │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

## Recommended Pattern: One TWD per Task Queue

If you have multiple Task Queues, create separate TemporalWorkerDeployment resources for each:

```yaml
# TWD for order processing
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: orders-worker
spec:
  replicas: 5
  template:
    spec:
      containers:
      - name: worker
        image: my-worker:v1.0
        env:
        - name: TASK_QUEUE
          value: "orders"
---
# TWD for notifications (different scaling needs)
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
        image: my-worker:v1.0
        env:
        - name: TASK_QUEUE
          value: "notifications"
```

Your worker code then reads `TASK_QUEUE` to determine which queue to poll.

### Benefits of One TWD per Task Queue

| Benefit | Description |
|---------|-------------|
| **Independent scaling** | Scale each Task Queue based on its workload characteristics |
| **Independent rollouts** | Deploy new versions to one queue without affecting others |
| **Clear versioning** | Each TWD tracks version state for its specific queue |
| **Isolated failures** | Issues in one queue don't impact others |

## When Multiple Task Queues per TWD Might Work

A single worker process can technically poll multiple Task Queues by creating multiple worker entities. This pattern may be acceptable when:

- Task Queues are tightly coupled (same workflows/activities)
- Scaling requirements are identical
- You always deploy all queues together
- Versioning behavior is the same across queues

However, you lose the benefits listed above. Use this pattern sparingly.

## FAQ

### Can I run multiple workers in one pod polling different Task Queues?

Yes, but the TWD manages them as a single unit. You cannot:
- Scale the queues independently
- Roll out versions to queues separately
- Have different replica counts per queue

### Should I use the same image for multiple TWDs?

Yes, this is common. The same image can serve different Task Queues - just pass the queue name via environment variable and have your code read it at startup.

### How do I share configuration across TWDs?

Use a shared `TemporalConnection` resource. Multiple TWDs can reference the same connection:

```yaml
apiVersion: temporal.io/v1alpha1
kind: TemporalConnection
metadata:
  name: prod-connection
spec:
  address: prod.temporal.example.com:7233
  namespace: prod
---
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: orders-worker
spec:
  workerOptions:
    temporalConnectionRef:
      name: prod-connection
  # ...
---
apiVersion: temporal.io/v1alpha1
kind: TemporalWorkerDeployment
metadata:
  name: notifications-worker
spec:
  workerOptions:
    temporalConnectionRef:
      name: prod-connection
  # ...
```
