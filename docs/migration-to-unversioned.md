# Migrating from Versioned to Unversioned Workflows

This guide walks you through reverting from versioned workers to unversioned workers. It assumes you have enabled worker versioning and currently have (or previously had) versioned workers polling the Temporal Server.

---

## Important Note

This guide uses specific terminology that is defined in the [Concepts](concepts.md) document. Please review the concepts document first to understand key terms like **Temporal Worker Deployment**, **`TemporalWorkerDeployment` CRD**, and **Kubernetes `Deployment`**, as well as the relationship between them.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Migration Steps](#migration-steps)
  - [Step 1: Update Worker Code](#step-1-update-worker-code)
  - [Step 2: Deploy the Unversioned Worker](#step-2-deploy-the-unversioned-worker)
  - [Step 3: Set Current Version to Unversioned](#step-3-set-current-version-to-unversioned)
- [Monitoring the Migration](#monitoring-the-migration)
- [Cleanup and Garbage Collection](#cleanup-and-garbage-collection)

---

## Prerequisites

Before starting the migration, ensure you have:

- ✅ **Temporal CLI** version >= 1.5.0

---

## Migration Steps

### Step 1: Update Worker Code

Update your worker initialization code to remove versioning configuration.

**Before (Versioned):**

```go
// Worker must use the build ID and deployment name from environment variables.
// These are set on the deployment by the controller.
buildID := os.Getenv("TEMPORAL_WORKER_BUILD_ID")
deploymentName := os.Getenv("TEMPORAL_DEPLOYMENT_NAME")
if buildID == "" || deploymentName == "" {
    // exit with an error
}

workerOptions := worker.Options{
    DeploymentOptions: worker.DeploymentOptions{
        UseVersioning: true,
        Version: worker.WorkerDeploymentVersion{
            DeploymentName: deploymentName,
            BuildID:        buildID,
        },
    },
}
worker := worker.New(client, "my-task-queue", workerOptions)
```

**After (Unversioned):**

```go
// Worker connects without versioning
worker := worker.New(client, "my-task-queue", worker.Options{})
```

### Step 2: Deploy the Unversioned Worker

Deploy your unversioned workers as you would without the Worker Controller (ie. as their own `Deployments` not connected to a `TemporalWorkerDeployment` resource) and ensure they are polling all of the Task Queues in your Worker Deployment. This can be done by verifying their presence on the Task Queues page (https://cloud.temporal.io/namespaces/<your-namespace>/task-queues/<your-task-queue>)

### Step 3: Set Current Version to Unversioned

Run the following Temporal CLI command to set the current version of the Worker Deployment to *unversioned*:

```bash
temporal worker deployment set-current-version \
    --deployment-name <your-deployment-name> \
    --build-id ""
```

---

## Monitoring the Migration

After completing the migration steps:

1. **Verify in the Temporal UI** that traffic is gradually shifting from versioned workers to unversioned workers.
2. **AutoUpgrade workflows** will eventually move onto the unversioned worker(s).
3. **Pinned workflows** that were started on versioned workers will continue and complete execution on those pinned workers.
4. **New executions** of workflows, regardless of them being *Pinned* or *AutoUpgrade*, will start on the deployed unversioned workers (unless they have a versioning-override set.)

---

## Cleanup and Garbage Collection

**NOTE**: The cleanup steps below are optional as it is not required to garbage collect the Worker Versions or Worker Deployment resources from the Temporal Server while migrating to unversioned workers. 

### Deleting Kubernetes Deployments created with the Worker-Controller:

The Worker Controller will delete Kubernetes Deployments, which represent versioned workers, based on your configured Sunset Strategy. Specifically, the Controller deletes Kubernetes Deployments for a version once the time since it became drained exceeds the combined `Scaledown` and `Delete` delay. Deleting these deployments stops the workers from polling the Temporal Server, which is a pre-requisite for deleting a Worker-Version.

### Deleting Worker Versions

A Worker Version can be deleted once it has been drained and has no active pollers.
Once it's drained and is without active pollers, delete the Worker Version using:

```bash
temporal worker deployment delete-version \
    --deployment-name <your-deployment-name> \
    --build-id <your-build-id>
```

### Deleting the Worker Deployment

To delete a Worker Deployment:

1. First, ensure all its Worker Versions have been deleted.
2. Then run:

```bash
temporal worker deployment delete --name <your-deployment-name>
```
