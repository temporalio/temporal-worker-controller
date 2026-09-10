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
	// cannot read. It follows the kstatus "abnormal-true" convention
	// (https://github.com/kubernetes-sigs/cli-utils/tree/master/pkg/kstatus) —
	// absent while things are normal — and kstatus reports Failed when it is True,
	// so Argo Rollouts and Helm --wait abort instead of waiting out their timeout.
	//
	// It is set only for failures decidable from information already in hand.
	// Failures that are waiting on another object to exist (a missing Connection or
	// credential Secret) and transient infrastructure failures report Reconciling
	// instead, because neither can be told apart from a normal few-second gap
	// during a deploy. See stalledReasons in the controller package.
	ConditionStalled = "Stalled"

	// ConditionReconciling is the kstatus "abnormal-true" counterpart to
	// Progressing: True while the controller is still working toward the spec,
	// and absent once it has caught up. kstatus reports InProgress when it is
	// True, which is the path kstatus intends for custom resources — without it
	// kstatus has to infer the same answer from Ready=False, a fallback its own
	// documentation flags as unreliable.
	//
	// Reconciling and Stalled must never both be True on the same object:
	// kstatus scans status.conditions in array order and returns on the first
	// match, so the verdict would depend on insertion order. The controller
	// always removes one when it sets the other.
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
