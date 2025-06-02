---- MODULE AutoScaling ----
EXTENDS Naturals, TLC

\* queueDepth: current number of tasks in the queue
\* replicas: number of active worker replicas
\* cooldown: cooldown counter before another scaling operation
\* utilization: percentage of current replica utilization (0 to 100)
VARIABLES queueDepth, replicas, cooldown, utilization

CONSTANTS 
    UPPER_QUEUE_DEPTH_THRESHOLD,     \* Queue depth above which we scale up
    LOWER_QUEUE_DEPTH_THRESHOLD,     \* Queue depth below which we scale down
    MAX_REPLICAS,                    \* Maximum allowed replicas
    MIN_REPLICAS,                    \* Minimum allowed replicas
    COOLDOWN_PERIOD,                 \* Cooldown period between scaling actions
    MAX_QUEUE_DEPTH,                 \* Hard cap on queue depth
    UTILIZATION_SCALE_DOWN_THRESHOLD \* Utilization below which it's safe to scale down

(* ---( INITIAL STATE )--- *)
\* System starts with empty queue, minimum replicas, no cooldown, and 0 utilization
Init ==
    /\ queueDepth = 0
    /\ replicas = MIN_REPLICAS
    /\ cooldown = 0
    /\ utilization = 0

(* ---( ACTIONS )--- *)

\* Simulates a task being enqueued into the system
EnqueueTasks ==
    /\ queueDepth' = queueDepth + 1
    /\ UNCHANGED <<replicas, cooldown, utilization>>

\* Simulates a task being completed by a replica
DequeueTasks ==
    /\ queueDepth > 0
    /\ queueDepth' = queueDepth - 1
    /\ UNCHANGED <<replicas, cooldown, utilization>>

\* Simulates external update of utilization (could be nondeterministic for modeling)
UpdateUtilization ==
    /\ utilization' \in 0..100
    /\ UNCHANGED <<queueDepth, replicas, cooldown>>

\* Scales up replicas if the queue is too deep and we're not in cooldown
ScaleUp ==
    /\ queueDepth >= UPPER_QUEUE_DEPTH_THRESHOLD
    /\ cooldown = 0
    /\ replicas < MAX_REPLICAS
    /\ replicas' = replicas + 1
    /\ cooldown' = COOLDOWN_PERIOD
    /\ UNCHANGED <<queueDepth, utilization>>

\* Scales down replicas only if:
\* - queue is shallow
\* - cooldown expired
\* - utilization is low
ScaleDown ==
    /\ queueDepth <= LOWER_QUEUE_DEPTH_THRESHOLD
    /\ cooldown = 0
    /\ utilization <= UTILIZATION_SCALE_DOWN_THRESHOLD
    /\ replicas > MIN_REPLICAS
    /\ replicas' = replicas - 1
    /\ cooldown' = COOLDOWN_PERIOD
    /\ UNCHANGED <<queueDepth, utilization>>

\* Decreases the cooldown timer by one if it's active
CooldownStep ==
    /\ cooldown > 0
    /\ cooldown' = cooldown - 1
    /\ UNCHANGED <<queueDepth, replicas, utilization>>

\* Optional stutter step to allow non-transition
NoOp ==
    /\ UNCHANGED <<queueDepth, replicas, cooldown, utilization>>

(* ---( NEXT STATE RELATION )--- *)
\* A valid transition is any one of the actions
Next ==
    \/ EnqueueTasks
    \/ DequeueTasks
    \/ UpdateUtilization
    \/ ScaleUp
    \/ ScaleDown
    \/ CooldownStep
    \/ NoOp

(* ---( INVARIANTS )--- *)

\* Replica count must always stay within bounds
ReplicaBounds ==
    /\ replicas >= MIN_REPLICAS
    /\ replicas <= MAX_REPLICAS

\* Cooldown must never be negative
CooldownNonNegative ==
    cooldown >= 0

\* Queue depth must never be negative
QueueDepthNonNegative ==
    queueDepth >= 0

\* Queue depth must remain below the configured cap
QueueDepthBound ==
    queueDepth <= MAX_QUEUE_DEPTH

\* If work exists and we're not in cooldown, progress must be made in the next state
ProgressGuaranteeAction ==
    (queueDepth > 0 /\ cooldown = 0) => (replicas' > replicas \/ queueDepth' < queueDepth)
\* Progress guarantee must always hold
ProgressGuarantee ==
    [][ProgressGuaranteeAction]_<<queueDepth, replicas, cooldown, utilization>>

\* Do not scale up if the queue is below the upper threshold
NoOverScalingAction ==
    (queueDepth < UPPER_QUEUE_DEPTH_THRESHOLD => replicas' = replicas)
NoOverScaling ==
    [][NoOverScalingAction]_<<queueDepth, replicas, cooldown, utilization>>

\* Do not scale down if the queue is above the lower threshold or the utilization is high
NoUnderScalingAction ==
    (queueDepth > LOWER_QUEUE_DEPTH_THRESHOLD \/ utilization > UTILIZATION_SCALE_DOWN_THRESHOLD => replicas' = replicas)
NoUnderScaling ==
    [][NoUnderScalingAction]_<<queueDepth, replicas, cooldown, utilization>>

\* Do not scale if the cooldown is active
CooldownEnforcedAction ==
    (cooldown > 0 => replicas' = replicas)
CooldownEnforced ==
    [][CooldownEnforcedAction]_<<queueDepth, replicas, cooldown, utilization>>

\* Do not scale if the queue depth is within thresholds
StabilityCheckAction ==
    (queueDepth < UPPER_QUEUE_DEPTH_THRESHOLD /\ queueDepth > LOWER_QUEUE_DEPTH_THRESHOLD => replicas' = replicas)
StabilityCheck ==
    [][StabilityCheckAction]_<<queueDepth, replicas, cooldown, utilization>>

(* ---( SPECIFICATION )--- *)
\* Main specification: starts in Init and always follows Next transitions
Spec ==
    Init /\ [][Next]_<<queueDepth, replicas, cooldown, utilization>>

\* The system must always maintain the defined invariants
Inv ==
    /\ ReplicaBounds
    /\ CooldownNonNegative
    /\ QueueDepthNonNegative
    /\ QueueDepthBound

====
