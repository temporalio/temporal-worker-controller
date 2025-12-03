# Migrating from Versioned to Unversioned Workflows

This guide walks you through reverting from worker versioning back to unversioned Temporal Workflows. It assumes you have enabled worker versioning and currently have (or previously had) versioned workers polling the Temporal Server.

---

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

Deploy the updated worker so that unversioned pollers begin polling the system.

> **Note:** If using the Worker Controller, change the Rollout Strategy in your `TemporalWorkerDeployment` custom resource to `Manual`. This will prevent the controller from automatically setting a new current version.

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

---

## Cleanup and Garbage Collection

This section highlights the ways in which one can delete the corresponding Worker Versions or Worker Deployment resources that were created in Temporal.

### Deleting Kubernetes Deployments created with the Worker-Controller:

The Worker Controller will delete Kubernetes Deployments, which represent versioned workers, based on your configured Sunset Strategy. The Worker Controller deletes Kubernetes Deployments for a version once the time since it became drained exceeds the combined `Scaledown` and `Sunset` delay. Deleting these deployments stops the workers from polling the Temporal Server, which is a pre-requisite for deleting a Worker-Version.

### Deleting Worker Versions

A Worker Version can be deleted once it has been drained and has no active pollers.
Once it's drained and without active pollers, delete the Worker Version using:

```bash
temporal worker deployment delete-version \
    --deployment-name <your-deployment-name> \
    --build-id <your-build-id>
```

> **Tip:** To skip the drainage requirement, add the `--skip-drainage` flag:
>
> ```bash
> temporal worker deployment delete-version \
>     --deployment-name <your-deployment-name> \
>     --build-id <your-build-id> \
>     --skip-drainage
> ```

### Deleting the Worker Deployment

To delete a Worker Deployment:

1. First, ensure all its Worker Versions have been deleted.
2. Then run:

```bash
temporal worker deployment delete --name <your-deployment-name>
```
