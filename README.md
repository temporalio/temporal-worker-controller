# Temporal Worker Controller

> ⚠️ This project is 100% experimental. Please do not attempt to install the controller in any production and/or shared environment.

The goal of the Temporal Worker Controller is to make it easy to run workers on Kubernetes while leveraging
[Worker Versioning](https://docs.temporal.io/workers#worker-versioning).

## Why

Temporal's [deterministic constraints](https://docs.temporal.io/workflows#deterministic-constraints) can cause headaches
when rolling out or rolling back workflow code changes.

The traditional approach to workflow determinism is to gate new behavior behind
[versioning checks](https://docs.temporal.io/workflows#workflow-versioning). Over time these checks can become a
source of technical debt, as safely removing them from a codebase is a careful process that often involves querying all
running workflows.

Worker Versioning is an alternative approach which enables
workflow executions to be sticky to workers running a specific code revision. This allows a workflow author
to omit version checks in code and instead run multiple versions of their worker in parallel, relying on Temporal to
keep workflow executions pinned to workers running compatible code.

This project aims to provide automation which simplifies the bookkeeping around tracking which worker versions still
have active workflows, managing the lifecycle of versioned worker deployments, and calling Temporal APIs to update the
default version after a deployment.

## Features

- [x] Registration of new worker versions
- [x] Creation of versioned worker deployment resources
- [x] Deletion of drained worker deployments
- [x] Manual, Blue/Green, and Progressive rollouts of new worker versions
- [ ] Autoscaling of worker deployments
- [ ] Automated rollover to compatible worker versions
- [ ] Canary analysis of new worker versions
- [ ] Optional cancellation after timeout for workflows on old versions
- [ ] Passing `ContinueAsNew` signal to workflows on old versions

## Terminology
Note that in Temporal, "Worker Deployment" is sometimes referred to as "Deployment", but since the controller makes
significant references to Kubernetes Deployment resource, within this repository we will stick to these terms:
- **Worker Deployment Version**: A version of a deployment or service. It can have multiple Workers, but they all run the same build. Sometimes shortened to "version" or "deployment version."
- **Worker Deployment**: A deployment or service across multiple versions. In a rainbow deploy, a worker deployment can have multiple active deployment versions running at once.
- **Deployment**: A Kubernetes Deployment resource.

## Usage

In order to be compatible with this controller, workers need to be configured using these standard environment
variables:

- `TEMPORAL_HOST_PORT`: The host and port of the Temporal server, e.g. `default.foo.tmprl.cloud:7233`
- `TEMPORAL_NAMESPACE`: The Temporal namespace to connect to, e.g. `default`
- `TEMPORAL_DEPLOYMENT_NAME`: The name of the worker deployment. This must be unique to the worker deployment and should not
  change between versions.
- `WORKER_BUILD_ID`: The build ID of the worker. This should change with each new worker rollout.

Each of these will be automatically set by the controller, and must not be manually specified in the worker's pod template.

## How It Works

Note: These sequence diagrams have not been fully converted to versioning v0.31 terminology.

Every `TemporalWorkerDeployment` resource manages one or more standard `Deployment` resources. Each deployment manages pods
which in turn poll Temporal for tasks pinned to their respective versions.

```mermaid
flowchart TD
    wd[TemporalWorker]

    subgraph "Latest/default deployment version"
        d5["Deployment v5"]
        rs5["ReplicaSet v5"]
        p5a["Pod v5-a"]
        p5b["Pod v5-b"]
        p5c["Pod v5-c"]
        d5 --> rs5
        rs5 --> p5a
        rs5 --> p5b
        rs5 --> p5c
    end

    subgraph "Deprecated deployment versions"
        d1["Deployment v1"]
        rs1["ReplicaSet v1"]
        p1a["Pod v1-a"]
        p1b["Pod v1-b"]
        d1 --> rs1
        rs1 --> p1a
        rs1 --> p1b

        dN["Deployment ..."]
    end

    wd --> d1
    wd --> dN
    wd --> d5

    p1a -. "poll version v1" .-> server
    p1b -. "poll version v1" .-> server

    p5a -. "poll version v5" .-> server
    p5b -. "poll version v5" .-> server
    p5c -. "poll version v5" .-> server

    server["Temporal Server"]
```

### Worker Lifecycle

When a new worker deployment version is deployed, the worker controller automates the registration of a new default worker
deployment version in Temporal.

As older workflows finish executing and deprecated deployment versions are no longer needed, the worker controller also
frees up resources by deleting old deployment versions.

```mermaid
sequenceDiagram
    autonumber
    participant Dev as Developer
    participant K8s as Kubernetes
    participant Ctl as WorkerController
    participant T as Temporal

    Dev->>K8s: Create TemporalWorker "foo" (v1)
    K8s-->>Ctl: Notify TemporalWorker "foo" created
    Ctl->>K8s: Create Deployment "foo-v1"
    Ctl->>T: Register v1 as new default
    Dev->>K8s: Update TemporalWorker "foo" (v2)
    K8s-->>Ctl: Notify TemporalWorker "foo" updated
    Ctl->>K8s: Create Deployment "foo-v2"
    Ctl->>T: Register v2 as new default
    
    Ctl->>Ctl: Run breaking change detection between v1 and v2
    Ctl->>T: If versions compatible, merge v1 and v2.
    
    loop Poll Temporal API
        Ctl-->>T: Wait for v1 workflow executions to close
    end
    
    Ctl->>K8s: Delete Deployment "foo-v1"
```

## Contributing

This project is in very early stages; as such external code contributions are not yet being solicited.

Bug reports and feature requests are welcome! Please [file an issue](https://github.com/jlegrone/worker-controller/issues/new).

You may also reach out to `@jlegrone` on the [Temporal Slack](https://t.mp/slack) if you have questions, suggestions, or are
interested in making other contributions.
