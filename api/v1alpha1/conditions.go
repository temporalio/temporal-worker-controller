package v1alpha1

// Condition type constants.
const (
	// ConditionReady is True for WorkerDeployment when the Temporal
	// connection is reachable and the target version is the current version in Temporal.
	// It is True for WorkerResourceTemplate when all active Build ID instances of the
	// WorkerResourceTemplate have been successfully applied.
	ConditionReady = "Ready"

	// ConditionProgressing is True while a rollout is actively in-flight —
	// i.e., the target version has not yet been promoted to current.
	ConditionProgressing = "Progressing"

	// ConditionStalled is True when reconciliation cannot progress and only a spec
	// change can resolve it — an invalid spec, or a connection kind this controller
	// cannot read. kstatus
	// (https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus) reports
	// Failed when it is True, so Argo Rollouts and Helm --wait abort instead of
	// waiting out their timeout.
	//
	// kstatus's own convention is that such a condition is absent while things are
	// normal, but it only ever tests for True, so this controller writes the
	// condition on every path and sets it False when nothing is stalled. That reads
	// identically to kstatus and keeps every condition the controller owns visible
	// in kubectl describe, consistent with Ready and Progressing.
	//
	// It is set only for failures decidable from information already in hand.
	// Failures that are waiting on another object to exist (a missing Connection or
	// credential Secret) and transient infrastructure failures report Reconciling
	// instead, because neither can be told apart from a normal few-second gap
	// during a deploy. See stalledReasons in the controller package.
	ConditionStalled = "Stalled"

	// ConditionReconciling is True while the controller is still working toward the
	// spec, and False once it has caught up. kstatus reports InProgress when it is
	// True, which is the path kstatus intends for custom resources. Without it,
	// kstatus has to infer the same answer from Ready=False, a fallback its own
	// documentation flags as unreliable.
	//
	// It is close to the inverse of Progressing but not identical: a transient
	// blocking error sets Progressing=False (blocked) and Reconciling=True (still
	// retrying), because those two vocabularies disagree about what a retry is.
	//
	// Reconciling and Stalled must never both be True on the same object.
	// kstatus scans status.conditions in array order and returns on the first
	// match, so the verdict would depend on insertion order. The controller
	// writes both on every path and sets at most one of them to True.
	ConditionReconciling = "Reconciling"
)

// Deprecated condition type constants. Maintained for backward compatibility with
// monitoring and automation built against v1.3.x. Use Ready and Progressing
// instead. These will be removed in the next major version of the CRD.
const (
	// Deprecated: Use ConditionReady and ConditionProgressing instead.
	ConditionConnectionHealthy = "ConnectionHealthy"

	// Deprecated: Use ConditionReady instead.
	ConditionRolloutComplete = "RolloutComplete"
)
