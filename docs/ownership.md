# Ownership Transfer in the Worker Controller

## Problem

If a worker controller is managing a Worker Deployment (ie. the controller is updating the RoutingConfig of the Worker 
Deployment), but the user changes something via the CLI (ie. rolls back to the previous current version, or stops the 
new target version from ramping because an issue was detected), the controller should not clobber what the human did.

After a human makes a change with the CLI, the `LastModifierIdentity` field of the Worker Deployment will change from 
“controller” to the client identity of the CLI user. The controller will see this and take that to imply that a human 
has made a change, and the controller should not make any more changes.

At some point, after this human has handled their urgent rollback, they will want to let the controller know that it is
authorized to resume making changes to the Routing Config of the Worker Deployment. The user can do this by updating
the metadata of the Worker Deployment.

## Solution

We've taken inspiration from how the `kubectl` tool allows for transferring ownership of resources between a user and a 
controller via [server-side apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/#transferring-ownership)

Currently, the only "field" of a Worker Deployment whose ownership is managed in this way is `routingConfig`, but more 
fields may be added in the future.

Essentially, to hand back control to the Worker Controller, the user should set the metadata of the Worker Deployment to
have a field `managedFields` that looks like this:
```go
"managedFields": [
  {
    "manager":"handover-to-worker-controller",
    "fieldsType":"v1",
    "v1":[
      "routingConfig"
    ]
  }
]
```

As in kubernetes, the “handover-to-controller” field manager will be cleared when the controller takes control.

Unlike in kubernetes, the user will not be blocked from making changes to the routing config when the controller is in 
charge of it. I think that the CLI should always work, even when a controller is running.

In the future, we could also use this mechanism to give exclusive ownership to the worker controller and block other 
clients from writing unless the `manager` of a field explicitly allows it, so let us know if you want that feature!

### How to actually do this?
Currently, the Temporal server supports Worker Deployment Version-level metadata, so you'll have to set this value on
the Current Version of your Worker Deployment.

```bash
temporal worker deployment update-metadata-version \
   --deployment-name $MY_DEPLOYMENT \
   --build-id $CURRENT_VERSION_BUILD_ID
   --metadata 'managedFields=[{"manager":"handover-to-controller", "fieldsType":"v1", "v1":["routingConfig"]}]`
```

In the rare case that you have a nil Current Version, you should set it on your Ramping Version
```bash
temporal worker deployment update-metadata-version \
   --deployment-name $MY_DEPLOYMENT \
   --build-id $RAMPING_VERSION_BUILD_ID
   --metadata 'managedFields=[{"manager":"handover-to-controller", "fieldsType":"v1", "v1":["routingConfig"]}]`
```

In the even rarer case that you have nil Current Version and nil Ramping Version, you'll need to use the CLI or SDK to
set a Current or Ramping Version and then do as instructed above.


### Future work
Once the Temporal server supports Worker Deployment-level metadata, this will just be one command:
```bash
temporal worker deployment update-metadata \
   --deployment-name $MY_DEPLOYMENT \
   --metadata 'managedFields=[{"manager":"handover-to-controller", "fieldsType":"v1", "v1":["routingConfig"]}]`
```