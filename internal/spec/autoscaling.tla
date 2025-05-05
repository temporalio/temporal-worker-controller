---- MODULE AutoScaling ----
EXTENDS Naturals, TLC

VARIABLES queueLen, workers, cooldown

CONSTANTS 
    UPPER_THRESHOLD, \* Max queue length before scaling up
    LOWER_THRESHOLD, \* Min queue length to trigger scaling down
    MAX_WORKERS,     \* Hard upper limit on workers
    MIN_WORKERS,     \* Hard lower limit on workers
    COOLDOWN_PERIOD  \* Steps to wait before another scale

(* ---( INITIAL STATE )--- *)
Init ==
    /\ queueLen = 0
    /\ workers = MIN_WORKERS
    /\ cooldown = 0

(* ---( ACTIONS )--- *)

\* Simulate task arrival
EnqueueTasks ==
    /\ queueLen' = queueLen + 1
    /\ UNCHANGED <<workers, cooldown>>

\* Simulate task completion
DequeueTasks ==
    /\ queueLen > 0
    /\ queueLen' = queueLen - 1
    /\ UNCHANGED <<workers, cooldown>>

\* Handle scaling up
ScaleUp ==
    /\ queueLen >= UPPER_THRESHOLD
    /\ cooldown = 0
    /\ workers < MAX_WORKERS
    /\ workers' = workers + 1
    /\ cooldown' = COOLDOWN_PERIOD
    /\ UNCHANGED queueLen

\* Handle scaling down
ScaleDown ==
    /\ queueLen <= LOWER_THRESHOLD
    /\ cooldown = 0
    /\ workers > MIN_WORKERS
    /\ workers' = workers - 1
    /\ cooldown' = COOLDOWN_PERIOD
    /\ UNCHANGED queueLen

\* Cooldown timer ticks down
CooldownStep ==
    /\ cooldown > 0
    /\ cooldown' = cooldown - 1
    /\ UNCHANGED <<queueLen, workers>>

\* No-op step to allow stuttering when needed
NoOp ==
    /\ UNCHANGED <<queueLen, workers, cooldown>>

(* ---( NEXT STATE RELATION )--- *)
Next ==
    \/ EnqueueTasks
    \/ DequeueTasks
    \/ ScaleUp
    \/ ScaleDown
    \/ CooldownStep
    \/ NoOp

(* ---( INVARIANTS )--- *)
\* Invariant: workers stay within bounds
WorkerBounds ==
    /\ workers >= MIN_WORKERS
    /\ workers <= MAX_WORKERS

\* Invariant: cooldown is never negative
CooldownNonNegative ==
    cooldown >= 0

\* Invariant: queueLen cannot be negative
QueueLenNonNegative ==
    queueLen >= 0

(* ---( SPECIFICATION )--- *)
Spec ==
    Init /\ [][Next]_<<queueLen, workers, cooldown>>

\* Properties to check
Inv ==
    WorkerBounds /\ CooldownNonNegative /\ QueueLenNonNegative

====

