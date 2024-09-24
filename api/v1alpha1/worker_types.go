// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type WorkerOptions struct {
	// The name of a TemporalConnection in the same namespace as the TemporalWorker.
	TemporalConnection string `json:"connection"`
	TemporalNamespace  string `json:"temporalNamespace"`
	TaskQueue          string `json:"taskQueue"`
}

// TemporalWorkerSpec defines the desired state of TemporalWorker
type TemporalWorkerSpec struct {
	// Important: Run "make" to regenerate code after modifying this file

	// Number of desired pods. This is a pointer to distinguish between explicit
	// zero and not specified. Defaults to 1.
	// TODO(jlegrone): Configure min replicas per thousand workflow/activity tasks?
	// +optional
	Replicas *int32 `json:"replicas,omitempty" protobuf:"varint,1,opt,name=replicas"`

	// Label selector for pods. Existing ReplicaSets whose pods are
	// selected by this will be the ones affected by this deployment.
	// It must match the pod template's labels.
	// +optional
	Selector *metav1.LabelSelector `json:"selector" protobuf:"bytes,2,opt,name=selector"`

	// Template describes the pods that will be created.
	// The only allowed template.spec.restartPolicy value is "Always".
	Template v1.PodTemplateSpec `json:"template"`

	// Minimum number of seconds for which a newly created pod should be ready
	// without any of its container crashing, for it to be considered available.
	// Defaults to 0 (pod will be considered available as soon as it is ready)
	// +optional
	MinReadySeconds int32 `json:"minReadySeconds,omitempty"`

	// The maximum time in seconds for a deployment to make progress before it
	// is considered to be failed. The deployment controller will continue to
	// process failed deployments and a condition with a ProgressDeadlineExceeded
	// reason will be surfaced in the deployment status. Note that progress will
	// not be estimated during the time a deployment is paused. Defaults to 600s.
	ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds,omitempty" protobuf:"varint,9,opt,name=progressDeadlineSeconds"`

	// How to cut over new workflow executions to the target worker version.
	RolloutStrategy RolloutStrategy `json:"cutover"`

	// TODO(jlegrone): add godoc
	WorkerOptions WorkerOptions `json:"workerOptions"`
}

// ReachabilityStatus indicates whether the version set is processing tasks.
// +enum
type ReachabilityStatus string

const (
	// ReachabilityStatusReachable indicates that the build ID may be used by
	// new workflows or activities (base on versioning rules), or there MAY
	// be open workflows or backlogged activities assigned to it.
	ReachabilityStatusReachable ReachabilityStatus = "Reachable"
	// ReachabilityStatusClosedOnly indicates that the build ID does not have
	// open workflows and is not reachable by new workflows, but MAY have
	// closed workflows within the namespace retention period.
	ReachabilityStatusClosedOnly ReachabilityStatus = "ClosedWorkflows"
	// ReachabilityStatusUnreachable indicates that the build ID is not used
	// for new executions, nor it has been used by any existing execution
	// within the retention period.
	ReachabilityStatusUnreachable ReachabilityStatus = "Unreachable"
	// ReachabilityStatusNotRegistered indicates that the build ID is not registered
	// with Temporal for the given task queue.
	ReachabilityStatusNotRegistered ReachabilityStatus = "NotRegistered"
)

// TemporalWorkerStatus defines the observed state of TemporalWorker
type TemporalWorkerStatus struct {
	// Remember, status should be able to be reconstituted from the state of the world,
	// so it’s generally not a good idea to read from the status of the root object.
	// Instead, you should reconstruct it every run.

	// TargetVersion is the desired next version. If the deployment is nil,
	// then the controller should create it. If not nil, the controller should
	// wait for it to become healthy and then move it to the DefaultVersionSet.
	TargetVersion *VersionedDeployment `json:"targetVersion"`

	// DefaultVersion is the deployment that is currently registered with
	// Temporal as the default. This must never be nil.
	//
	// RampPercentage should always be nil for this version.
	DefaultVersion *VersionedDeployment `json:"defaultVersion"`

	// DeprecatedVersions are deployments that are no longer the default. Any
	// deployments that are unreachable should be deleted by the controller.
	//
	// RampPercentage should only be set for DeprecatedVersions when rollout
	// strategy is set to manual.
	DeprecatedVersions []*VersionedDeployment `json:"deprecatedVersions,omitempty"`

	// TODO(jlegrone): Add description
	VersionConflictToken []byte `json:"versionConflictToken"`
}

type VersionedDeployment struct {
	// Healthy indicates whether the deployment is healthy.
	// +optional
	HealthySince *metav1.Time `json:"healthySince"`

	// The build ID associated with the deployment.
	BuildID string `json:"buildID"`

	// Other compatible build IDs that redirect to this deployment.
	CompatibleBuildIDs []string `json:"compatibleBuildIDs,omitempty"`

	// Reachability indicates whether workers in this version set may
	// be eligible to receive tasks from the Temporal server.
	Reachability ReachabilityStatus `json:"reachability"`

	// RampPercentage is the percentage of new workflow executions that are
	// configured to start on this version.
	//
	// Acceptable range is [0,100].
	RampPercentage *uint8 `json:"rampPercentage,omitempty"`

	// A pointer to the version set's managed deployment.
	// +optional
	Deployment *v1.ObjectReference `json:"deployment"`
}

// DefaultVersionUpdateStrategy describes how to cut over new workflow executions
// to the target worker version.
// +kubebuilder:validation:Enum=Manual;AllAtOnce;Progressive
type DefaultVersionUpdateStrategy string

const (
	UpdateManual DefaultVersionUpdateStrategy = "Manual"

	UpdateAllAtOnce DefaultVersionUpdateStrategy = "AllAtOnce"

	UpdateProgressive DefaultVersionUpdateStrategy = "Progressive"
)

// RolloutStrategy defines strategy to apply during next rollout
type RolloutStrategy struct {
	// Specifies how to treat concurrent executions of a Job.
	// Valid values are:
	// - "Manual": do not automatically update the default worker version;
	// - "AllAtOnce": start 100% of new workflow executions on the new worker version as soon as it's healthy;
	// - "Progressive": ramp up the percentage of new workflow executions targeting the new worker version over time.
	Strategy DefaultVersionUpdateStrategy `json:"strategy"`

	// Steps to execute progressive rollouts. Only required when strategy is "Progressive".
	// +optional
	Steps []RolloutStep `json:"steps,omitempty" protobuf:"bytes,3,rep,name=steps"`
}

type AllAtOnceRolloutStrategy struct{}

type RolloutStep struct {
	// RampPercentage indicates what percentage of new workflow executions should be
	// routed to the new worker version while this step is active.
	//
	// Acceptable range is [0,100].
	RampPercentage uint8 `json:"rampPercentage"`

	// PauseDuration indicates how long to pause before progressing to the next step.
	PauseDuration metav1.Duration `json:"pauseDuration"`
}

type ManualRolloutStrategy struct{}

type QueueStatistics struct {
	// The approximate number of tasks backlogged in this task queue. May count expired tasks but eventually converges
	// to the right value.
	ApproximateBacklogCount int64 `json:"approximateBacklogCount,omitempty"`
	// Approximate age of the oldest task in the backlog based on the creation timestamp of the task at the head of the queue.
	ApproximateBacklogAge metav1.Duration `json:"approximateBacklogAge,omitempty"`
	// Approximate tasks per second added to the task queue based on activity within a fixed window. This includes both backlogged and
	// sync-matched tasks.
	TasksAddRate float32 `json:"tasksAddRate,omitempty"`
	// Approximate tasks per second dispatched to workers based on activity within a fixed window. This includes both backlogged and
	// sync-matched tasks.
	TasksDispatchRate float32 `json:"tasksDispatchRate,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Default",type="string",JSONPath=".status.defaultVersion.buildID",description="Default BuildID for new workflows"
//+kubebuilder:printcolumn:name="Target",type="string",JSONPath=".status.targetVersion.buildID",description="BuildID of the current worker template"
//+kubebuilder:printcolumn:name="Target-Ramp",type="integer",JSONPath=".status.targetVersion.rampPercentage",description="Percentage of new workflows starting on Target BuildID"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// TemporalWorker is the Schema for the temporalworkers API
//
// TODO(jlegrone): Implement default/validate interface https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation.html
type TemporalWorker struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TemporalWorkerSpec   `json:"spec,omitempty"`
	Status TemporalWorkerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TemporalWorkerList contains a list of TemporalWorker
type TemporalWorkerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TemporalWorker `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TemporalWorker{}, &TemporalWorkerList{})
}
