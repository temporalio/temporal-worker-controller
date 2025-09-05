# Temporal Worker Controller Concepts

This document defines key concepts and terminology used throughout the Temporal Worker Controller documentation.

## Core Terminology

### Temporal Worker Deployment
A logical grouping in Temporal that represents a collection of workers that are deployed together and should be versioned together. Examples include "payment-processor", "notification-sender", or "data-pipeline-worker". This is a concept within Temporal itself, not specific to Kubernetes. See https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning for more details.

**Key characteristics:**
- Identified by a unique worker deployment name (e.g., "payment-processor/staging")
- Can have multiple concurrent worker versions running simultaneously
- Versions of a Worker Deployment are identified by Build IDs (e.g., "v1.5.1", "v1.5.2")
- Temporal routes workflow executions to appropriate worker versions based on the `RoutingConfig` of the Worker Deployment that the versions are in.
- Temporal routes workflow executions to appropriate versions based on compatibility rules

### `TemporalWorkerDeployment` CRD
The Kubernetes Custom Resource Definition that manages one Temporal Worker Deployment. This is the primary resource you interact with when using the Temporal Worker Controller.

**Key characteristics:**
- One `TemporalWorkerDeployment` Custom Resource per Temporal Worker Deployment
- Manages the lifecycle of all versions for that worker deployment
- Defines rollout strategies, resource requirements, and connection details
- Controller creates and manages multiple Kubernetes `Deployment` resources based on this spec

The actual Kubernetes `Deployment` resources that run worker pods. The controller automatically creates these - you don't manage them directly.
The actual Kubernetes Deployment resources that run worker pods. The controller automatically creates these - you don't manage them directly.

**Key characteristics:**
- Multiple Kubernetes `Deployment` resources per `TemporalWorkerDeployment` Custom Resource (one per version)
- Named with the pattern: `{worker-deployment-name}-{build-id}` (e.g., `payment-processor/staging-v1.5.1`)
- Managed entirely by the controller - created, updated, and deleted automatically
- Each runs a specific version of your worker code

### Key Relationship
**One `TemporalWorkerDeployment` Custom Resource → Multiple Kubernetes `Deployment` resources (managed by controller)**

make changes to the spec of your `TemporalWorkerDeployment` Custom Resource, and the controller handles all the underlying Kubernetes `Deployment` resources for different versions.

## Version States

Worker deployment versions progress through various states during their lifecycle:

### NotRegistered
The version has been specified in the `TemporalWorkerDeployment` custom resource but hasn't been registered with Temporal yet. This typically happens when:
- The worker pods are still starting up
- There are connectivity issues to Temporal
- The worker code has errors preventing registration

### Inactive
The version is registered with Temporal but isn't automatically receiving any new workflow executions through the Worker Deployment's `RoutingConfig`. This is the initial state for new versions before they are promoted via Versioning API calls. Inactive versions can receive workflow executions via `VersioningOverride` only.

### Ramping
The version is receiving a percentage of new workflow executions. If managed by a Progressive rollout, the percentage gradually increases according to the configured rollout steps. If the rollout is Manual, the user is responsible for setting the ramp percentage and ramping version.

### Current
The version is receiving 100% of new workflow executions. This is the "stable" version that handles all new workflows and all existing AutoUpgrade workflows running on the task queues in this Worker Deployment.

### Draining
The version is no longer receiving new workflow executions but may still be processing existing workflows.

### Drained
All Pinned workflows on this version have completed. The version is ready for cleanup according to the sunset configuration.

## Rollout Strategies

### Manual Strategy
Requires explicit human intervention to promote versions. New versions remain in the `Inactive` state until manually promoted.

**Use cases:**
- During migration from manual deployment systems
- Testing and validation scenarios

### AllAtOnce Strategy
Immediately routes 100% of new workflow executions to the target version once it's healthy and registered.

**Use cases:**
- Non-production environments
- Low-risk deployments
- When you want immediate cutover without gradual rollout

### Progressive Strategy
Gradually increases the percentage of new workflow executions routed to the new version according to configured steps.

**Use cases:**
- Production deployments where you want to validate new versions gradually
- When you want automated rollouts with built-in safety checks
- Deployments that benefit from canary analysis

## Configuration Concepts

### Worker Options
Configuration that tells the controller how to connect to the same Temporal cluster and namespace that the worker is connected to:
- **connection**: Reference to a `TemporalConnection` custom resource
- **temporalNamespace**: The Temporal namespace to connect to
- **deploymentName**: The logical deployment name in Temporal (auto-generated if not specified)

### Rollout Configuration
Defines how new versions are promoted:
- **strategy**: Manual, AllAtOnce, or Progressive
- **steps**: For Progressive strategy, defines ramp percentages and pause durations
- **gate**: Optional workflow that must succeed on all task queues in the target Worker Deployment Version before promotion continues

### Sunset Configuration
Defines how Drained versions are cleaned up:
- **scaledownDelay**: How long to wait after a version has been Drained before scaling pods to zero
- **deleteDelay**: How long to wait after a version has been Drained before deleting the Kubernetes `Deployment`

### Template
The pod template used for the target version of this worker deployment. Similar to a standard Kubernetes Deployment template but managed by the controller.

## Environment Variables

The controller automatically sets these environment variables for all worker pods:

### TEMPORAL_HOST_PORT
The host and port of the Temporal server, derived from the `TemporalConnection` custom resource.
The worker must connect to this Temporal endpoint, but since this is user provided and not controller generated, the user does not necessarily need to access this env var to get that endpoint if it already knows the endpoint another way.

### TEMPORAL_NAMESPACE
The Temporal namespace the worker should connect to, from `spec.workerOptions.temporalNamespace`.
The worker must connect to this Temporal namespace, but since this is user provided and not controller generated, the user does not necessarily need to access this env var to get that namespace if it already knows the namespace another way.

### TEMPORAL_DEPLOYMENT_NAME
The worker deployment name in Temporal, auto-generated from the `TemporalWorkerDeployment` name and Kubernetes namespace.
The worker *must* use this to configure its `worker.DeploymentOptions`.

### WORKER_BUILD_ID
The build ID for this specific version, derived from the container image tag and hash of the target pod template.
The worker *must* use this to configure its `worker.DeploymentOptions`.

## Resource Management Concepts

### Rainbow Deployments
The pattern of running multiple versions of the same service simultaneously. Running multiple versions of your workers simultaneously is essential for supporting Pinned workflows in Temporal, as Pinned workflows must continue executing on the worker version they started on.

### Version Lifecycle Management
The automated process of:
1. Registering new versions with Temporal
2. Gradually routing traffic to new versions
3. Cleaning up resources for drained versions

### Controller-Managed Resources
Resources that are created, updated, and deleted automatically by the controller:
- `TemporalWorkerDeployment` custom resources, to update their status
- Kubernetes `Deployment` resources for each version
- Labels and annotations for tracking and management
