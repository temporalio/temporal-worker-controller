# Architecture

This document provides a detailed technical overview of how the Temporal Worker Controller works, including architecture diagrams, worker lifecycle management, and the relationship between Kubernetes resources and Temporal Worker Deployments.

## How It Works

Every `TemporalWorkerDeployment` resource manages one or more standard `Deployment` resources. Each Deployment manages pods which in turn poll Temporal for tasks routed to their respective worker versions.

```mermaid
flowchart TD
    subgraph "K8s Namespace 'ns'"
      twd[TemporalWorkerDeployment 'foo']
      
      subgraph "Current/default version"
        d5["Deployment foo-v5, Version{DeploymentName: foo/ns, BuildId: v5}"]
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
        d1["Deployment foo-v1 Version{DeploymentName: foo/ns, BuildId: v1}"]
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

    p1a -. "poll version {foo/ns, v1}" .-> server
    p1b -. "poll version {foo/ns, v1}" .-> server

    p5a -. "poll version {foo/ns, v5}" .-> server
    p5b -. "poll version {foo/ns, v5}" .-> server
    p5c -. "poll version {foo/ns, v5}" .-> server

    server["Temporal Server"]
```

## Worker Lifecycle

When a new worker deployment version is deployed, the worker controller detects it and automatically begins the process of making that version the new **Current Version** of the worker deployment it is a part of. This could happen immediately if `rollout.strategy = AllAtOnce`, or gradually if `rollout.strategy = Progressive`.

As older pinned workflows finish executing and deprecated deployment versions become **Drained**, the worker controller frees up resources by scaling down and eventually deleting the `Deployment` resources running workers that poll those versions.

Here is an example of a progressive cut-over strategy gated on the success of the `HelloWorld` workflow:
```yaml
  rollout:
    strategy: Progressive
    steps:
      - rampPercentage: 1
        pauseDuration: 30s
      - rampPercentage: 10
        pauseDuration: 1m
    gate:
      workflowType: "HelloWorld"
```

## Deployment Flow Diagram

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
    Ctl->>T: Register build "v1" as new current version of "foo/ns"
    Dev->>K8s: Update TemporalWorker "foo" (v2)
    K8s-->>Ctl: Notify TemporalWorker "foo" updated
    Ctl->>K8s: Create Deployment "foo-v2"
    Ctl->>T: Register build "v2" as new current version of "foo/ns"
    
    loop Poll Temporal API
        Ctl-->>T: Wait for version {foo/ns, v1} to be drained (no open pinned wfs)
    end
    
    Ctl->>K8s: Delete Deployment "foo-v1"
```

## Resource Relationships

### TemporalWorkerDeployment (Custom Resource)
- **Purpose**: High-level configuration for a worker deployment across all versions
- **Manages**: Multiple Kubernetes `Deployment` resources (one per version)
- **Lifecycle**: Long-lived, persists across worker version changes

### Kubernetes Deployment (Managed by Controller)
- **Purpose**: Runs pods for a specific worker deployment version
- **Naming**: `{worker-deployment-name}-{build-id}`
- **Lifecycle**: Created when new version deployed, deleted when version drained

### TemporalConnection (Custom Resource)
- **Purpose**: Connection configuration to Temporal server(s)
- **Scope**: Can be shared across multiple worker deployments
- **Contains**: Server addresses, TLS configuration, authentication

## Version States and Rollout Strategies

For detailed information about version states (Current, Ramping, Deprecated, Drained) and rollout strategies (Manual, AllAtOnce, Progressive), see the [Concepts Guide](concepts.md).

For configuration examples and detailed rollout strategy options, see the [Configuration Reference](configuration.md).
