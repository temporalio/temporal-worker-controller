# Ownership Transfer in the Worker Controller

## Problem

If a worker controller is managing a Worker Deployment (ie. the controller is updating the RoutingConfig of the Worker
Deployment), but the user changes something via the CLI (ie. rolls back to the previous current version, or stops the
new target version from ramping because an issue was detected), the controller should not clobber what the human did.

At some point, after this human has handled their urgent rollback, they will want to let the controller know that it is
authorized to resume making changes to the Routing Config of the Worker Deployment.

## Solution

_Once it is available in OSS v1.29, the controller will be able to coordinate with other users via the `ManagerIdentity`
field of a Worker Deployment. This runbook will be updated when that is available and implemented by the controller._

In the meantime, the controller will watch the `LastModifierIdentity` field of a Worker Deployment to detect whether 
another user has made a change. If another user made a change to the Worker Deployment, the controller will not make
any more changes to ensure a human's change is not clobbered.

Once you are done making your own changes to the Worker Deployment's current and ramping versions, and you are ready
for the Worker Controller to take over, you can update the metadata to indicate that.

There is no Temporal server support for Worker Deployment Version-level metadata, so you'll have to set this value on
the Current Version of your Worker Deployment.

Note: The controller decodes this metadata value as a string. Be sure to set the value to the string "true" (not the boolean true).

```bash
temporal worker deployment update-metadata-version \
   --deployment-name $MY_DEPLOYMENT \
   --build-id $CURRENT_VERSION_BUILD_ID \
   --metadata 'temporal.io/ignore-last-modifier="true"'
```
Alternatively, if your CLI supports JSON input:
```bash
temporal worker deployment update-metadata-version \
   --deployment-name $MY_DEPLOYMENT \
   --build-id $CURRENT_VERSION_BUILD_ID \
   --metadata-json '{"temporal.io/ignore-last-modifier":"true"}'
```
In the rare case that you have a nil Current Version when you are passing back ownership, you should set it on your Ramping Version
```bash
temporal worker deployment update-metadata-version \
   --deployment-name $MY_DEPLOYMENT \
   --build-id $RAMPING_VERSION_BUILD_ID \
   --metadata 'temporal.io/ignore-last-modifier="true"'
```
Or with JSON:
```bash
temporal worker deployment update-metadata-version \
   --deployment-name $MY_DEPLOYMENT \
   --build-id $RAMPING_VERSION_BUILD_ID \
   --metadata-json '{"temporal.io/ignore-last-modifier":"true"}'
```

In the even rarer case that you have nil Current Version and nil Ramping Version, you'll need to use the CLI or SDK to
set a Current or Ramping Version and then do as instructed above.