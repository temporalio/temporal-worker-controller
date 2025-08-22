# Temporal Worker Controller Concepts

This document defines key concepts and terminology used throughout the Temporal Worker Controller documentation.

## Core Terminology

### Temporal Worker Deployment
A logical grouping in Temporal that represents a collection of workers that handle the same set of workflows and activities. Examples include "payment-processor", "notification-sender", or "data-pipeline-worker". This is a concept within Temporal itself, not specific to Kubernetes.

**Key characteristics:**
- Identified by a unique deployment name (e.g., "payment-processor")
- Can have multiple concurrent versions running simultaneously
- Versions are identified by Build IDs (e.g., "v1.5.1", "v1.5.2")
- Temporal routes workflow executions to appropriate versions based on compatibility rules

### `TemporalWorkerDeployment` CRD
The Kubernetes Custom Resource Definition that manages one Temporal Worker Deployment. This is the primary resource you interact with when using the Temporal Worker Controller.

**Key characteristics:**
- One CRD resource per Temporal Worker Deployment
- Manages the lifecycle of all versions for that deployment
- Defines rollout strategies, resource requirements, and connection details
- Controller creates and manages multiple Kubernetes `Deployment` resources based on this spec

### Kubernetes `Deployment`
The actual Kubernetes Deployment resources that run worker pods. The controller automatically creates these - you don't manage them directly.

**Key characteristics:**
- Multiple Kubernetes `Deployment` resources per `TemporalWorkerDeployment` CRD (one per version)
- Named with the pattern: `{deployment-name}-{build-id}` (e.g., `payment-processor-v1.5.1`)
- Managed entirely by the controller - created, updated, and deleted automatically
- Each runs a specific version of your worker code

### Key Relationship
**One `TemporalWorkerDeployment` CRD → Multiple Kubernetes `Deployment` resources (managed by controller)**

This is the fundamental architecture: you manage a single CRD resource, and the controller handles all the underlying Kubernetes `Deployment` resources for different versions.

## Version States

Worker deployment versions progress through various states during their lifecycle:

### NotRegistered
The version has been specified in the CRD but hasn't been registered with Temporal yet. This typically happens when:
- The worker pods are still starting up
- There are connectivity issues to Temporal
- The worker code has errors preventing registration

### Inactive
The version is registered with Temporal but isn't receiving any new workflow executions. This is the initial state for new versions when using Manual rollout strategy.

### Ramping
The version is receiving a percentage of new workflow executions as part of a Progressive rollout. The percentage gradually increases according to the configured rollout steps.

### Current
The version is receiving 100% of new workflow executions. This is the "production" version that handles all new work.

### Draining
The version is no longer receiving new workflow executions but may still be processing existing workflows. The controller waits for all workflows on this version to complete.

### Drained
All workflows on this version have completed. The version is ready for cleanup according to the sunset configuration.

## Rollout Strategies

### Manual Strategy
Requires explicit human intervention to promote versions. New versions remain in the `Inactive` state until manually promoted.

**Use cases:**
- During migration from manual deployment systems
- High-risk production environments requiring human approval
- Testing and validation scenarios

### AllAtOnce Strategy
Immediately routes 100% of new workflow executions to the new version once it's healthy and registered.

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
Configuration that defines how workers connect to Temporal:
- **connection**: Reference to a `TemporalConnection` resource
- **temporalNamespace**: The Temporal namespace to connect to
- **deploymentName**: The logical deployment name in Temporal (auto-generated if not specified)

### Cutover Configuration
Defines how new versions are promoted:
- **strategy**: Manual, AllAtOnce, or Progressive
- **steps**: For Progressive strategy, defines ramp percentages and pause durations
- **gate**: Optional workflow that must succeed before promotion continues

### Sunset Configuration
Defines how old versions are cleaned up:
- **scaledownDelay**: How long to wait after draining before scaling pods to zero
- **deleteDelay**: How long to wait after draining before deleting the Kubernetes `Deployment`

### Template
The pod template used for all versions of this deployment. Similar to a standard Kubernetes Deployment template but managed by the controller.

## Environment Variables

The controller automatically sets these environment variables for all worker pods:

### TEMPORAL_HOST_PORT
The host and port of the Temporal server, derived from the `TemporalConnection` resource.

### TEMPORAL_NAMESPACE
The Temporal namespace the worker should connect to, from `spec.workerOptions.temporalNamespace`.

### TEMPORAL_DEPLOYMENT_NAME
The deployment name in Temporal, either from `spec.workerOptions.deploymentName` or auto-generated from the CRD name.

### WORKER_BUILD_ID
The build ID for this specific version, derived from the container image tag or explicitly set.

## Resource Management Concepts

### Rainbow Deployments
The pattern of running multiple versions of the same worker simultaneously. This is essential for maintaining workflow determinism in Temporal, as running workflows must continue executing on the version they started with.

### Version Lifecycle Management
The automated process of:
1. Registering new versions with Temporal
2. Gradually routing traffic to new versions
3. Draining old versions once they're no longer needed
4. Cleaning up resources for drained versions

### Controller-Managed Resources
Resources that are created, updated, and deleted automatically by the controller:
- Kubernetes `Deployment` resources for each version
- ConfigMaps and Secrets as needed
- Service accounts and RBAC resources
- Labels and annotations for tracking and management

## Migration Concepts

### Import Process
The process of bringing existing manually-managed worker deployments under controller management. This involves:
1. Creating a `TemporalWorkerDeployment` CRD with Manual strategy
2. Sequentially updating the image spec to register each existing version
3. Cleaning up original manual Kubernetes `Deployment` resources
4. Enabling automated rollouts

### Single Resource Requirement
The critical principle that each Temporal Worker Deployment must be managed by exactly one `TemporalWorkerDeployment` CRD resource. You cannot split a single logical deployment across multiple CRD resources.

### Legacy Version Handling
The process of ensuring that existing worker versions continue running during migration, maintaining workflow determinism while transitioning to controller management.
