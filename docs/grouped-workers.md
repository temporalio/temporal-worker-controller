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
  groupRetirementWorkflow:
    workflowType: AuthorizeRetirement
    taskQueue: release-management
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

## Member resource names

New grouped member Deployments start with up to 25 characters of the member name,
then up to 10 DNS-safe characters of the build ID, then a 10-character hash of the
complete WorkerDeployment name, member name, and build ID. Names stay within the
controller's existing 47-character Deployment allowance. For example, an
`activities` member with build `release-1` starts with `activities-release-1-`.
The hash distinguishes groups and builds whose readable prefixes are the same.

A controller upgrade preserves existing Deployment names, UIDs, and pods. The
controller finds members by their owner and build/member labels, so old and new
name formats can coexist in one retained build. If a member Deployment is
missing, the controller restores its saved settings using the new readable name.
Generated resource names are not a stable caller contract; use the labels and
observed status references described below. Single-Deployment naming is unchanged.

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
gate result. Gates run only on declared `Workflow` queues, never on activity-only
queues. Grouped gates have no controller-imposed execution deadline: they may wait
for dependencies or authorization to recover without permanently blocking the
build. Gate workflows should continue as new when needed to bound their history;
the same workflow identity and pinned build are retained. Single-Deployment gates
keep their existing one-hour execution deadline. When another build becomes
desired, the controller requests cancellation of still-running gates for the
superseded build after checking their saved type,
queue, and pinned build. It does not cancel application workflows. Cancellation
is asynchronous: workers remain until those workflows close and Temporal reports
global drainage. A canceled
gate cannot approve a later rollback of the same build. Use a new build ID when a
release needs a fresh gate run. An application that must stop accepting new
requests on an older release must perform that application-specific work itself,
for example in its gate workflow. The controller
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

Before its retirement workflow starts, selecting a retained build with its
original specification restores every saved member and count, then waits for combined readiness and any configured gate before
routing workflows to it. Reverting only the primary image while keeping different
group settings is not a rollback of that saved build.

## Retirement and deletion

Every grouped specification must supply `groupRetirementWorkflow.workflowType`
and `taskQueue`. An unversioned management worker outside the group implements
this workflow. The controller supplies the canonical Temporal deployment name and
build ID as two separate string arguments. The workflow must keep those arguments
when continuing as new. It must complete only after the application has permanently
stopped accepting new work for that build and no application caller relies on
Temporal's implicit ramp selection (including `UseRampingVersion`). Existing
pinned workflows may still be running. Retry temporary dependency failures within
the workflow; a failed or canceled execution retains the group. The controller
imposes no execution or run deadline and does not restart closed executions.

Once a retirement workflow exists, the same saved build cannot become the desired
build again. Deploy the previous code with a fresh build ID for rollback. Build
identities must never be reused after application admission has closed, including
after saved records and Temporal history expire. Keep retirement workflow history
available until the saved build is deleted.

The controller checks the execution's type, queue, effective unversioned behavior,
and exact original arguments for its latest run. Success authorizes routing
maintenance, not worker shutdown. For an obsolete inactive or unregistered build
when the current resource uses the `AllAtOnce` rollout strategy, the controller
briefly sets that build as a zero-percent ramp and clears it. This
initializes Temporal's normal drainage tracking even if the build was never
current. The operation explicitly permits missing queues and absent pollers, so a
partially started build can still enter drainage. It does not route ordinary new
work to that build. Applications using implicit ramp selection must not opt into
this contract, since those requests can select a zero-percent ramp.

This maintenance operation is not performed while the current resource uses
`Manual` or `Progressive` rollout. A historical build's saved strategy does not
grant or veto current routing authority. All strategies can retire builds that
Temporal already reports drained after the application check succeeds.

Actual release routing takes priority. The controller waits while another build
occupies the ramp slot or a rollout routing update is pending. It never replaces
an existing canary. Each maintenance write uses Temporal's routing conflict token;
a concurrent change causes a retry after fresh state is read. A failed retirement
check or readiness cancellation emits a warning while the current release keeps
its normal readiness status; other obsolete groups can still retire. A process stopped
between assignment and removal can resume from the approved build's observed
zero-percent assignment without an extra saved phase record.

A grouped version keeps every saved member count until its application workflow
has succeeded, its controller-created readiness workflows have closed, and
Temporal reports it globally `Drained`. Temporal's drainage check includes
remaining pinned workflow activity and its visibility grace period. After its
saved `sunset.scaledownDelay` measured from Temporal's `DrainedSince`, the
controller scales every member to zero. After the additional saved
`sunset.deleteDelay`, it waits for Kubernetes to observe no remaining or
terminating pods and starts cleanup. An application workflow's success time or a
zero running-workflow count never substitutes for `Drained`.

The controller records cleanup intent in the saved revision's metadata before
removing the Temporal version. If cleanup is interrupted, later reconciles finish
deleting members instead of recreating workers. The saved record is deleted only
after every member Deployment object is gone. A build whose cleanup has already
started completes cleanup before it can be created again.

Enabling grouping uses a new build ID. The controller rejects adoption of an
existing build that has no saved grouped settings. Whole-resource deletion keeps
the existing `WorkerDeployment` deletion behavior; it is a separate operation from
normal drained-version retirement. Check the migration and deletion behavior of
your existing deployment before replacing that resource.
