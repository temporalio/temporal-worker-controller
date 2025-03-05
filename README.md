# Temporal Worker Controller

> ⚠️ This project is 100% experimental. Please do not attempt to install the controller in any production and/or shared environment.

The goal of the Temporal Worker Controller is to make it easy to run workers on Kubernetes while leveraging
[Worker Versioning](https://docs.temporal.io/workers#worker-versioning).

## Why

Temporal's [deterministic constraints](https://docs.temporal.io/workflows#deterministic-constraints) can cause headaches
when rolling out or rolling back workflow code changes.

The traditional approach to workflow determinism is to gate new behavior behind
[versioning checks](https://docs.temporal.io/workflows#workflow-versioning), otherwise known as the Patching API. Over time these checks can become a
source of technical debt, as safely removing them from a codebase is a careful process that often involves querying all
running workflows.

**Worker Versioning** is a Temporal feature that allows you to pin Workflows to individual versions of your workers, which 
are called **Worker Deployment Versions**. Using pinning, you’ll no longer need to patch most Workflows as part of routine 
deploys! With this guarantee, you can freely make changes that would have previously caused non-determinism errors had 
you done them without patching. And provided your Activities and Workflows are running in the same worker deployment version, 
you also do not need to ensure interface compatibility across versions.

This greatly simplifies Workflow upgrades, but the cost is that your deployment system must support multiple versions 
running simultaneously and allow you to control when they are sunsetted. This is typically known as a [rainbow deploy](https://release.com/blog/rainbow-deployment-why-and-how-to-do-it) 
(of which a **blue-green deploy** is a special case) and contrasts to a **rolling deploy** in which your Workers are upgraded in
place without the ability to keep old versions around.

This project aims to provide automation to enable rainbow deployments of your workers by simplifying the bookkeeping around 
tracking which versions still have active workflows, managing the lifecycle of versioned worker deployments, and calling 
Temporal APIs to update the routing config of Temporal Worker Deployments to route workflow traffic to new versions.

## Terminology
Note that in Temporal, **Worker Deployment** is sometimes referred to as **Deployment**, but since the controller makes
significant references to Kubernetes Deployment resource, within this repository we will stick to these terms:
- **Worker Deployment Version**: A version of a deployment or service. It can have multiple Workers, but they all run the same build. Sometimes shortened to "version" or "deployment version."
- **Worker Deployment**: A deployment or service across multiple versions. In a rainbow deploy, a worker deployment can have multiple active deployment versions running at once.
- **Deployment**: A Kubernetes Deployment resource. A Deployment is "versioned" if it is running versioned Temporal workers/pollers.

## Features

- [x] Registration of new Temporal Worker Deployment Versions
- [x] Creation of versioned Deployment resources (that manage the Pods that run your Temporal pollers)
- [x] Deletion of resources associated with drained Worker Deployment Versions
- [x] `Manual`, `AllAtOnce`, and `Progressive` rollouts of new versions
- [x] Ability to specify a "gate" workflow that must succeed on the new version before routing real traffic to that version
- [ ] Autoscaling of versioned Deployments
- [ ] Canary analysis of new worker versions
- [ ] Optional cancellation after timeout for workflows on old versions
- [ ] Passing `ContinueAsNew` signal to workflows on old versions


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
which in turn poll Temporal for tasks routed to their respective versions.

```mermaid
flowchart TD
    subgraph "K8s Namespace 'ns'"
      twd[TemporalWorkerDeployment 'foo']
      
      subgraph "Current/default version"
        d5["Deployment foo-v5 (Version foo/ns.v5)"]
        rs5["ReplicaSet foo-v5"]
        p5a["Pod foo-v5-a"]
        p5b["Pod foo-v5-b"]
        p5c["Pod foo-v5-c"]
        d5 --> rs5
        rs5 --> p5a
        rs5 --> p5b
        rs5 --> p5c
      end

      subgraph "Deprecated versions"
        d1["Deployment foo-v1 (Version foo/ns.v1)"]
        rs1["ReplicaSet foo-v1"]
        p1a["Pod foo-v1-a"]
        p1b["Pod foo-v1-b"]
        d1 --> rs1
        rs1 --> p1a
        rs1 --> p1b

        dN["Deployment ..."]
      end
    end  

    twd --> d1
    twd --> dN
    twd --> d5

    p1a -. "poll version foo/ns.v1" .-> server
    p1b -. "poll version foo/ns.v1" .-> server

    p5a -. "poll version foo/ns.v5" .-> server
    p5b -. "poll version foo/ns.v5" .-> server
    p5c -. "poll version foo/ns.v5" .-> server

    server["Temporal Server"]
```

### Worker Lifecycle

When a new worker deployment version is deployed, the worker controller detects it and automatically begins the process
of making that version the new **current** (aka default) version of the worker deployment it is a part of. This could happen
immediately if `cutover.strategy = AllAtOnce`, or gradually if `cutover.strategy = Progressive`.

As older pinned workflows finish executing and deprecated deployment versions become drained, the worker controller also
frees up resources by sunsetting the `Deployment` resources polling those versions.

Here is an example of a progressive cut-over strategy gated on the success of the `HelloWorld` workflow:
```yaml
  cutover:
    strategy: Progressive
    steps:
      - rampPercentage: 1
        pauseDuration: 30s
      - rampPercentage: 10
        pauseDuration: 1m
    gate:
      workflowType: "HelloWorld"
```

```mermaid
sequenceDiagram
    autonumber
    participant Dev as Developer
    participant K8s as Kubernetes
    participant Ctl as WorkerController
    participant T as Temporal

    Dev->>K8s: Create TemporalWorkerDeployment "foo" (v1)
    K8s-->>Ctl: Notify TemporalWorkerDeployment "foo" created
    Ctl->>K8s: Create Deployment "foo-v1"
    Ctl->>T: Register "foo/ns.v1" as new current version of "foo/ns"
    Dev->>K8s: Update TemporalWorker "foo" (v2)
    K8s-->>Ctl: Notify TemporalWorker "foo" updated
    Ctl->>K8s: Create Deployment "foo-v2"
    Ctl->>T: Register "foo/ns.v2" as new current version of "foo/ns"
    
    loop Poll Temporal API
        Ctl-->>T: Wait for "foo/ns.v1" to be drained (no open pinned wfs)
    end
    
    Ctl->>K8s: Delete Deployment "foo-v1"
```

## Contributing

This project is in very early stages; as such external code contributions are not yet being solicited.

Bug reports and feature requests are welcome! Please [file an issue](https://github.com/jlegrone/worker-controller/issues/new).

You may also reach out to `@jlegrone` on the [Temporal Slack](https://t.mp/slack) if you have questions, suggestions, or are
interested in making other contributions.
