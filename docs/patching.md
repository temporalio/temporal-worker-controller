# Patching

This is a runbook for how to deploy a patch build and move workflows to it without making it the Current/Ramping version
of your Worker Deployment.

We plan to support this more natively in future releases, but in the meantime if you want to use the worker controller
to deploy a patch build of a worker, but you don't want new workflows to go to it (or at least not yet), you can follow
these instructions.

Let's say workflows pinned to Build ID `v1.2.0-dsf7` in Worker Deployment `my-worker/prod` have some bug that 
you want to fix.
1. You make your patch code change, and build and push an image named `my-worker:v1.2.1`.
2. If you were doing trunk-style development, you would normally edit the manifest for `TemporalWorkerDeployment` called
   `my-worker` and deploy it according to your usual rollout strategy. However, in this case you only want workflows that
   are stuck on the broken `my-worker:v1.2.0` image to run on your patch version. To do this, you need to edit your manifest
   to have the `Manual` rollout strategy. This means the controller will create your worker pods but will not promote the
   build.
3. The controller will now create a new Build ID `v1.2.1-4543` based on your image and the hash of the pod spec. It will
   also create the Deployment needed to run your new worker and the worker will start polling.
4. Now, to move the broken pinned workflows to your new version, set a batch versioning override:
```bash
% temporal workflow update-options \
  --versioning-override-behavior pinned \
  --versioning-override-build-id v1.2.1-4543 \
  --versioning-override-deployment-name my-worker/prod \
  --query 'TemporalWorkerDeploymentVersion = "my-worker/prod:v1.2.0-dsf7" and TemporalWorkerVersioningBehavior = "Pinned" and Status = "Running"'
```


PROBLEM:

This version will never become Active, so when it is no longer the Target Version, the controller will scale it to zero
workers, because the drainage status is not set.

One fix for this is that on the server, we could trigger "draining" when a versioning override is set on a workflow...