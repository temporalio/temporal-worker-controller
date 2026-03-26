package v1alpha1

// Condition type constants.
const (
	// ConditionReady is True for TemporalWorkerDeployment when the Temporal
	// connection is reachable and the target version is the current version in Temporal.
	// It is True for WorkerResourceTemplate when all active Build ID instances of the
	// WorkerResourceTemplate have been successfully applied.
	ConditionReady = "Ready"

	// ConditionProgressing is True while a rollout is actively in-flight —
	// i.e., the target version has not yet been promoted to current.
	ConditionProgressing = "Progressing"
)

// Deprecated condition type constants. Maintained for backward compatibility with
// monitoring and automation built against v1.3.x. Use Ready and Progressing
// instead. These will be removed in the next major version of the CRD.
const (
	// Deprecated: Use ConditionReady and ConditionProgressing instead.
	ConditionTemporalConnectionHealthy = "TemporalConnectionHealthy"

	// Deprecated: Use ConditionReady instead.
	ConditionRolloutComplete = "RolloutComplete"
)
