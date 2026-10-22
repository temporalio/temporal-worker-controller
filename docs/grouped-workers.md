# Grouped worker Deployments

One `WorkerDeployment` can run several separate Kubernetes Deployments under the
same Temporal build ID. Use this when workflow and activity workers need different
service accounts, secrets, resource requests, or replica counts, but must roll out
as one release. Each member has its own pod template; the controller does not put
these workers into a shared pod.

The existing `spec.deployment` configures the member named `primary`.
`spec.additionalDeployments` adds named members. Every member requires a positive,
fixed replica count. Grouped workers retain these independent counts while
Temporal drains the version, so `drainingDeployment` and external autoscalers are
not supported for grouped members. Single-Deployment configuration and autoscaling
behavior are unchanged.

```yaml
apiVersion: temporal.io/v1alpha1
kind: WorkerDeployment
metadata:
  name: grouped-workers
spec:
  workerOptions:
    connectionRef:
      name: temporal
    temporalNamespace: default
    unsafeCustomBuildID: release-1
  deployment:
    replicas: 2
    template:
      spec:
        serviceAccountName: workflow-workers
        containers:
          - name: workflow-worker
            image: example.invalid/workflow-worker:release-1
  additionalDeployments:
    - name: activities
      replicas: 3
      template:
        spec:
          serviceAccountName: activity-workers
          containers:
            - name: activity-worker
              image: example.invalid/activity-worker:release-1
              envFrom:
                - secretRef:
                    name: activity-credentials
              resources:
                requests:
                  cpu: 250m
                  memory: 256Mi
  expectedTaskQueues:
    - name: jobs
      kinds: [Workflow, Activity]
  rollout:
    strategy: AllAtOnce
  sunset:
    scaledownDelay: 1h
    deleteDelay: 24h
```

Member names must be unique DNS labels; `primary` is reserved. Queue entries have
unique names, are supported only for grouped workers, and declare the required `Workflow`, `Activity`, or both kinds.
Workflow and activity polling on the same queue name are separate requirements.
The controller injects the same Temporal deployment name and build ID into every
member using the existing worker environment variables. Configure each worker's
SDK to use those values and poll its intended queue kinds.

## Readiness and promotion

Before starting configured gate workflows, the controller waits for all members
to have their intended replica count observed, updated, ready, and available in
Kubernetes, and for Temporal to report every declared queue name and kind on that
exact build. A single available Deployment or a registered workflow queue cannot
make a missing activity member ready.

The existing `status.targetVersion.healthySince` represents this combined
readiness. `status.targetVersion.deployment` continues to reference the primary
Deployment. Each member also carries `temporal.io/member`, `temporal.io/build-id`,
and `temporal.io/deployment-name` labels for inspection.

Configured gate workflows must complete successfully before Temporal routing is
promoted, including when selecting a retained build for rollback. A gate runs once
per build and workflow queue; selecting the same build again reuses its existing
gate result. Use a new build ID when a release needs a fresh gate run. An application
that must stop accepting new requests on an older release must perform that
application-specific work itself, for example in its gate workflow. The controller
owns Temporal routing and worker lifecycle, not application request routing.

## Upgrade order

Install the updated CRDs and controller RBAC, then complete the controller upgrade
before applying grouped specifications. Older controller versions do not understand
separate members or their saved records. Do not downgrade the controller while
grouped builds remain. Existing single-Deployment resources require no data migration.

## Saved builds and recovery

The controller saves the complete `WorkerDeployment` specification in an
`apps/v1.ControllerRevision` before creating any member. Kubernetes protects the
saved record's `data` from updates. If creation stops partway through, a later
reconcile creates missing members from that saved specification. A newer release
cannot replace an older member's pod template, credentials, or replica count.
Removing a member from a newer release does not remove it from older builds that
still need it.

With an explicit `unsafeCustomBuildID`, changing saved group settings under that
same build ID is rejected. Give changed releases a new build ID. If the override
is omitted, the controller derives the group build ID from the entire
specification. The existing single-Deployment behavior that permits rolling
updates under an unchanged custom build ID is unchanged.

Connection resources remain shared: changes to the Temporal address or
connection credential references still update every live member. Those mutable
connection settings are not frozen in the saved group record. Secret references
inside each application pod template remain specific to that member.

Selecting a retained build with its original specification restores every saved
member and count, then waits for combined readiness and any configured gate before
routing workflows to it. Reverting only the primary image while keeping different
group settings is not a rollback of that saved build.

## Retirement and deletion

A grouped version keeps its saved member counts until Temporal reports it globally
`Drained`. After its saved `sunset.scaledownDelay`, the controller scales every
member to zero. After the additional saved `sunset.deleteDelay`, it waits for
Kubernetes to observe no remaining or terminating pods and starts cleanup.

The controller records cleanup intent in the saved revision's metadata before
removing the Temporal version. If cleanup is interrupted, later reconciles finish
deleting members instead of recreating workers. The saved record is deleted only
after every member Deployment object is gone. A build whose cleanup has already
started completes cleanup before it can be created again.

A version that never received routed traffic may remain `Inactive`, rather than
becoming `Drained`. Grouped workers retain that version; they do not infer global
drainage from a missing queue or one idle member.

Enabling grouping uses a new build ID. The controller rejects adoption of an
existing build that has no saved grouped settings. Whole-resource deletion keeps
the existing `WorkerDeployment` deletion behavior; it is a separate operation from
normal drained-version retirement. Check the migration and deletion behavior of
your existing deployment before replacing that resource.
