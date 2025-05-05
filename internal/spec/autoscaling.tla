---- MODULE AutoScaling ----
EXTENDS Naturals, TLC

\* queueDepth: current number of tasks in the queue
\* workers: number of active workers
\* cooldown: cooldown counter before another scaling operation
VARIABLES queueDepth, workers, cooldown

CONSTANTS 
    UPPER_QUEUE_DEPTH_THRESHOLD, \* Queue depth above which we scale up
    LOWER_QUEUE_DEPTH_THRESHOLD, \* Queue depth below which we scale down
    MAX_WORKERS,                 \* Maximum allowed workers
    MIN_WORKERS,                 \* Minimum allowed workers
    COOLDOWN_PERIOD,             \* Cooldown period between scaling actions
    MAX_QUEUE_DEPTH              \* Hard cap on queue depth

(* ---( INITIAL STATE )--- *)
\* System starts with empty queue, minimum workers, and no cooldown
Init ==
    /\ queueDepth = 0
    /\ workers = MIN_WORKERS
    /\ cooldown = 0

(* ---( ACTIONS )--- *)

\* Simulates a task being enqueued into the system
EnqueueTasks ==
    /\ queueDepth' = queueDepth + 1
    /\ UNCHANGED <<workers, cooldown>>

\* Simulates a task being completed by a worker
DequeueTasks ==
    /\ queueDepth > 0
    /\ queueDepth' = queueDepth - 1
    /\ UNCHANGED <<workers, cooldown>>

\* Scales up workers if the queue is too deep and we're not in cooldown
ScaleUp ==
    /\ queueDepth >= UPPER_QUEUE_DEPTH_THRESHOLD
    /\ cooldown = 0
    /\ workers < MAX_WORKERS
    /\ workers' = workers + 1
    /\ cooldown' = COOLDOWN_PERIOD
    /\ UNCHANGED queueDepth

\* Scales down workers if the queue is shallow and we're not in cooldown
ScaleDown ==
    /\ queueDepth <= LOWER_QUEUE_DEPTH_THRESHOLD
    /\ cooldown = 0
    /\ workers > MIN_WORKERS
    /\ workers' = workers - 1
    /\ cooldown' = COOLDOWN_PERIOD
    /\ UNCHANGED queueDepth

\* Decreases the cooldown timer by one if it's active
CooldownStep ==
    /\ cooldown > 0
    /\ cooldown' = cooldown - 1
    /\ UNCHANGED <<queueDepth, workers>>

\* Optional stutter step to allow non-transition
NoOp ==
    /\ UNCHANGED <<queueDepth, workers, cooldown>>

(* ---( NEXT STATE RELATION )--- *)
\* A valid transition is any one of the actions
Next ==
    \/ EnqueueTasks
    \/ DequeueTasks
    \/ ScaleUp
    \/ ScaleDown
    \/ CooldownStep
    \/ NoOp

(* ---( INVARIANTS )--- *)

\* Worker count must always stay within bounds
WorkerBounds ==
    /\ workers >= MIN_WORKERS
    /\ workers <= MAX_WORKERS

\* Cooldown must never be negative
CooldownNonNegative ==
    cooldown >= 0

\* Queue depth must never be negative
QueueDepthNonNegative ==
    queueDepth >= 0

\* Queue depth must remain below the configured cap
QueueDepthBound ==
    queueDepth <= MAX_QUEUE_DEPTH

\* Do not scale up if the queue is below the upper threshold
NoOverScaling ==
    (queueDepth < UPPER_QUEUE_DEPTH_THRESHOLD => workers' = workers)

\* Do not scale down if the queue is above the lower threshold
NoUnderScaling ==
    (queueDepth > LOWER_QUEUE_DEPTH_THRESHOLD => workers' = workers)

\* No scaling operations are allowed while cooldown is active
CooldownEnforced ==
    (cooldown > 0 => workers' = workers)

\* If work exists and we're not in cooldown, progress must be made
ProgressGuarantee ==
    (queueDepth > 0 /\ cooldown = 0 => workers' > workers \/ queueDepth' < queueDepth)

\* System should remain stable if queue depth is within thresholds
StabilityCheck ==
    (queueDepth < UPPER_QUEUE_DEPTH_THRESHOLD /\ queueDepth > LOWER_QUEUE_DEPTH_THRESHOLD => workers' = workers)

(* ---( SPECIFICATION )--- *)
\* Main specification: starts in Init and always follows Next transitions
Spec ==
    Init /\ [][Next]_<<queueDepth, workers, cooldown>>

\* The system must always maintain the defined invariants
Inv ==
    /\ WorkerBounds
    /\ CooldownNonNegative
    /\ QueueDepthNonNegative
    /\ QueueDepthBound
    /\ NoOverScaling
    /\ NoUnderScaling
    /\ CooldownEnforced
    /\ ProgressGuarantee
    /\ StabilityCheck

====
