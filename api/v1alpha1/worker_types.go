// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// TemporalConnectionReference contains the name of a TemporalConnection resource
// in the same namespace as the TemporalWorkerDeployment.
type TemporalConnectionReference struct {
	// Name of the TemporalConnection resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

type WorkerOptions struct {
	// The name of a TemporalConnection in the same namespace as the TemporalWorkerDeployment.
	TemporalConnectionRef TemporalConnectionReference `json:"connectionRef"`
	// The Temporal namespace for the worker to connect to.
	// +kubebuilder:validation:MinLength=1
	TemporalNamespace string `json:"temporalNamespace"`
}

// TemporalWorkerDeploymentSpec defines the desired state of TemporalWorkerDeployment
type TemporalWorkerDeploymentSpec struct {

	// Number of desired pods. This is a pointer to distinguish between explicit
	// zero and not specified. Defaults to 1.
	// This field makes TemporalWorkerDeploymentSpec implement the scale subresource, which is compatible with auto-scalers.
	// +optional
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas,omitempty" protobuf:"varint,1,opt,name=replicas"`

	// Template describes the pods that will be created.
	// The only allowed template.spec.restartPolicy value is "Always".
	Template corev1.PodTemplateSpec `json:"template"`

	// Minimum number of seconds for which a newly created pod should be ready
	// without any of its container crashing, for it to be considered available.
	// Defaults to 0 (pod will be considered available as soon as it is ready)
	// +optional
	// +kubebuilder:default=0
	MinReadySeconds int32 `json:"minReadySeconds,omitempty"`

	// The maximum time in seconds for a deployment to make progress before it
	// is considered to be failed. The deployment controller will continue to
	// process failed deployments and a condition with a ProgressDeadlineExceeded
	// reason will be surfaced in the deployment status. Note that progress will
	// not be estimated during the time a deployment is paused. Defaults to 600s.
	// +kubebuilder:default=600
	ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds,omitempty" protobuf:"varint,9,opt,name=progressDeadlineSeconds"`

	// How to rollout new workflow executions to the target version.
	RolloutStrategy RolloutStrategy `json:"rollout"`

	// How to manage sunsetting drained versions.
	SunsetStrategy SunsetStrategy `json:"sunset"`

	// WorkerOptions configures the worker's connection to Temporal.
	WorkerOptions WorkerOptions `json:"workerOptions"`
}

// VersionStatus indicates the status of a version.
// +enum
type VersionStatus string

const (
	// VersionStatusNotRegistered indicates that the version is not registered
	// with Temporal for any worker deployment.
	VersionStatusNotRegistered VersionStatus = "NotRegistered"

	// VersionStatusInactive indicates that the version is registered in a Temporal
	// worker deployment, but has not been set to current or ramping.
	// A version is registered in a worker deployment after a poller with appropriate
	// DeploymentOptions starts polling.
	VersionStatusInactive VersionStatus = "Inactive"

	// VersionStatusRamping indicates that the version is the ramping version of its
	// worker deployment. It is accepting some percentage of new workflow executions.
	VersionStatusRamping VersionStatus = "Ramping"

	// VersionStatusCurrent indicates that the version is the current version of its
	// worker deployment. It is accepting all new workflow executions except for the
	// percent that are sent to the ramping version, if one exists.
	VersionStatusCurrent VersionStatus = "Current"

	// VersionStatusDraining indicates that the version has stopped accepting new workflows
	// (is no longer ramping or current) and DOES have open workflows pinned to it.
	VersionStatusDraining VersionStatus = "Draining"

	// VersionStatusDrained indicates that the version has stopped accepting new workflows
	// (is no longer ramping or current) and does NOT have open workflows pinned to it.
	// This version MAY still receive query tasks associated with closed workflows.
	VersionStatusDrained VersionStatus = "Drained"
)

// TemporalWorkerDeploymentStatus defines the observed state of TemporalWorkerDeployment
type TemporalWorkerDeploymentStatus struct {
	// Remember, status should be able to be reconstituted from the state of the world,
	// so it's generally not a good idea to read from the status of the root object.
	// Instead, you should reconstruct it every run.

	// Replicas is the total number of non-terminated pods targeted by this TemporalWorkerDeployment.
	// This is used by the /scale subresource to report current replica count to HPA/KEDA.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Selector is the label selector for pods managed by this TemporalWorkerDeployment.
	// This is used by the /scale subresource to allow HPA/KEDA to discover pods.
	// Format: "app.kubernetes.io/name=<name>"
	// +optional
	Selector string `json:"selector,omitempty"`

	// TargetVersion is the desired next version. If TargetVersion.Deployment is nil,
	// then the controller should create it. If not nil, the controller should
	// wait for it to become healthy and then move it to the CurrentVersion.
	TargetVersion TargetWorkerDeploymentVersion `json:"targetVersion"`

	// CurrentVersion is the version that is currently registered with
	// Temporal as the current version of its worker deployment. This will be nil
	// during initial bootstrap until a version is registered and set as current.
	CurrentVersion *CurrentWorkerDeploymentVersion `json:"currentVersion,omitempty"`

	// DeprecatedVersions are deployment versions that are no longer the default. Any
	// deployment versions that are unreachable should be deleted by the controller.
	DeprecatedVersions []*DeprecatedWorkerDeploymentVersion `json:"deprecatedVersions,omitempty"`

	// VersionConflictToken prevents concurrent modifications to the deployment status.
	// It ensures reconciliation operations don't inadvertently override changes made
	// by external systems while processing is underway.
	VersionConflictToken []byte `json:"versionConflictToken,omitempty"`

	// LastModifierIdentity is the identity of the client that most recently modified the worker deployment.
	// +optional
	LastModifierIdentity string `json:"lastModifierIdentity,omitempty"`

	// VersionCount is the total number of versions currently known by the worker deployment.
	// This includes current, target, ramping, and deprecated versions.
	// +optional
	// +kubebuilder:validation:Minimum=0
	VersionCount int32 `json:"versionCount,omitempty"`

	// TODO(jlegrone): Add additional status fields following Kubernetes API conventions
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
}

// WorkflowExecutionStatus describes the current state of a workflow.
// +enum
type WorkflowExecutionStatus string

const (
	// WorkflowExecutionStatusRunning indicates that the workflow is currently running.
	WorkflowExecutionStatusRunning WorkflowExecutionStatus = "Running"
	// WorkflowExecutionStatusCompleted indicates that the workflow has completed successfully.
	WorkflowExecutionStatusCompleted WorkflowExecutionStatus = "Completed"
	// WorkflowExecutionStatusFailed indicates that the workflow has failed.
	WorkflowExecutionStatusFailed WorkflowExecutionStatus = "Failed"
	// WorkflowExecutionStatusCanceled indicates that the workflow has been canceled.
	WorkflowExecutionStatusCanceled WorkflowExecutionStatus = "Canceled"
	// WorkflowExecutionStatusTerminated indicates that the workflow has been terminated.
	WorkflowExecutionStatusTerminated WorkflowExecutionStatus = "Terminated"
	// WorkflowExecutionStatusTimedOut indicates that the workflow has timed out.
	WorkflowExecutionStatusTimedOut WorkflowExecutionStatus = "TimedOut"
)

type WorkflowExecution struct {
	WorkflowID string                  `json:"workflowID"`
	RunID      string                  `json:"runID"`
	Status     WorkflowExecutionStatus `json:"status"`
	TaskQueue  string                  `json:"taskQueue"`
}

type TaskQueue struct {
	// Name is the name of the task queue.
	Name string `json:"name"`
}

// BaseWorkerDeploymentVersion contains fields common to all worker deployment version types
type BaseWorkerDeploymentVersion struct {
	// BuildID is the unique identifier for this version of the worker deployment.
	BuildID string `json:"buildID"`

	// Status indicates whether workers in this version may
	// be eligible to receive tasks from the Temporal server.
	Status VersionStatus `json:"status"`

	// Healthy indicates whether the deployment version is healthy.
	// +optional
	HealthySince *metav1.Time `json:"healthySince,omitempty"`

	// A pointer to the version's managed k8s deployment.
	// +optional
	Deployment *corev1.ObjectReference `json:"deployment,omitempty"`

	// TaskQueues is a list of task queues that are associated with this version.
	TaskQueues []TaskQueue `json:"taskQueues,omitempty"`

	// ManagedBy is the identity of the client that is managing the rollout of this version.
	// +optional
	ManagedBy string `json:"managedBy,omitempty"`
}

// CurrentWorkerDeploymentVersion represents a worker deployment version that is currently
// the default version for new workflow executions.
type CurrentWorkerDeploymentVersion struct {
	BaseWorkerDeploymentVersion `json:",inline"`
	// No additional fields - current versions are already validated and promoted
}

// TargetWorkerDeploymentVersion represents a worker deployment version that is the desired
// next version. It may be in various states during its lifecycle.
type TargetWorkerDeploymentVersion struct {
	BaseWorkerDeploymentVersion `json:",inline"`

	// A TestWorkflow is used to validate the deployment version before making it the default.
	// +optional
	TestWorkflows []WorkflowExecution `json:"testWorkflows,omitempty"`

	// RampPercentage is the percentage of new workflow executions that are
	// configured to start on this version. For example, 1.5 means 1.5%.
	// Only set when Status is VersionStatusRamping.
	//
	// Acceptable range is [0.0,100.0] (0% to 100%).
	// +kubebuilder:validation:Minimum=0.0
	// +kubebuilder:validation:Maximum=100.0
	RampPercentage *float32 `json:"rampPercentage,omitempty"`

	// RampingSince is time when the version first started ramping.
	// Only set when Status is VersionStatusRamping.
	// +optional
	RampingSince *metav1.Time `json:"rampingSince,omitempty"`

	// RampLastModifiedAt is the time when the ramp percentage was last changed for the target version.
	// +optional
	RampLastModifiedAt *metav1.Time `json:"rampLastModifiedAt,omitempty"`
}

// DeprecatedWorkerDeploymentVersion represents a worker deployment version that is no longer
// the default and is being phased out.
type DeprecatedWorkerDeploymentVersion struct {
	BaseWorkerDeploymentVersion `json:",inline"`

	// DrainedSince is the time at which the version became drained.
	// Only set when Status is VersionStatusDrained.
	// +optional
	DrainedSince *metav1.Time `json:"drainedSince,omitempty"`

	// A Version is eligible for deletion if it is drained and has no pollers on any task queue.
	// After pollers stop polling, the server will still consider them present until `matching.PollerHistoryTTL`
	// has passed.
	// +optional
	EligibleForDeletion bool `json:"eligibleForDeletion,omitempty"`
}

// DefaultVersionUpdateStrategy describes how to cut over new workflow executions
// to the target worker deployment version.
// +kubebuilder:validation:Enum=Manual;AllAtOnce;Progressive
type DefaultVersionUpdateStrategy string

const (
	// UpdateManual scales worker resources up or down, but does not update the current or ramping worker deployment version.
	UpdateManual DefaultVersionUpdateStrategy = "Manual"

	// UpdateAllAtOnce starts 100% of new workflow executions on the new worker deployment version as soon as it's healthy.
	UpdateAllAtOnce DefaultVersionUpdateStrategy = "AllAtOnce"

	// UpdateProgressive ramps up the percentage of new workflow executions targeting the new worker deployment version over time.
	//
	// Note: If the Current Version of a Worker Deployment is nil and the controller cannot confirm that all Task Queues
	// in the Target Version have at least one unversioned poller, the controller will immediately set the new worker
	// deployment version to be Current and ignore the Progressive rollout steps.
	// Sending a percentage of traffic to a "nil" version means that traffic will be sent to unversioned workers. If
	// there are no unversioned workers, those tasks will get stuck. This behavior ensures that all traffic on the task
	// queues in this worker deployment can be handled by an active poller.
	UpdateProgressive DefaultVersionUpdateStrategy = "Progressive"
)

type GateWorkflowConfig struct {
	WorkflowType string `json:"workflowType"`
	// Input is an arbitrary JSON object passed as the first parameter to the gate workflow.
	// For inputs with secrets use SecretKeyRef in InputFrom to omit from logs.
	// +optional
	Input *apiextensionsv1.JSON `json:"input,omitempty"`
	// InputFrom references a key in a ConfigMap or Secret whose contents are passed
	// as the first parameter to the gate workflow. The referenced value should be a JSON document.
	// For inputs with secrets use SecretKeyRef to omit from logs.
	// +optional
	InputFrom *GateInputSource `json:"inputFrom,omitempty"`
}

// GateInputSource references a value from a ConfigMap or a Secret
type GateInputSource struct {
	// Select a key of a ConfigMap in the same namespace
	// +optional
	ConfigMapKeyRef *corev1.ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
	// Select a key of a Secret in the same namespace
	// +optional
	SecretKeyRef *corev1.SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// RolloutStrategy defines strategy to apply during next rollout
type RolloutStrategy struct {
	// Specifies how to treat concurrent executions of a Job.
	// Valid values are:
	// - "Manual"
	// - "AllAtOnce"
	// - "Progressive"
	Strategy DefaultVersionUpdateStrategy `json:"strategy"`

	// Gate specifies a workflow type that must run once to completion on the new worker deployment version before
	// any traffic is directed to the new version.
	Gate *GateWorkflowConfig `json:"gate,omitempty"`

	// Steps to execute progressive rollouts. Only required when strategy is "Progressive".
	// +optional
	Steps []RolloutStep `json:"steps,omitempty" protobuf:"bytes,3,rep,name=steps"`
}

// SunsetStrategy defines strategy to apply when sunsetting k8s deployments of drained versions.
type SunsetStrategy struct {
	// ScaledownDelay specifies how long to wait after a version is drained before scaling its Deployment to zero.
	// Defaults to 1 hour.
	// +optional
	// +kubebuilder:default="1h"
	ScaledownDelay *metav1.Duration `json:"scaledownDelay"`

	// DeleteDelay specifies how long to wait after a version is drained before deleting its Deployment.
	// Defaults to 24 hours.
	// +optional
	// +kubebuilder:default="24h"
	DeleteDelay *metav1.Duration `json:"deleteDelay"`
}

type AllAtOnceRolloutStrategy struct{}

type RolloutStep struct {
	// RampPercentage indicates what percentage of new workflow executions should be
	// routed to the new worker deployment version while this step is active.
	// For example, 15 means 15%.
	//
	// Acceptable range is [1,99] (1% to 99%).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=99
	RampPercentage int `json:"rampPercentage"`

	// PauseDuration indicates how long to pause before progressing to the next step.
	PauseDuration metav1.Duration `json:"pauseDuration"`
}

type ManualRolloutStrategy struct{}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=twd;twdeployment;tworkerdeployment
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas",description="Current replicas"
//+kubebuilder:printcolumn:name="Current",type="string",JSONPath=".status.currentVersion.buildID",description="Current build ID"
//+kubebuilder:printcolumn:name="Target",type="string",JSONPath=".status.targetVersion.buildID",description="Target build ID"
//+kubebuilder:printcolumn:name="Ramp %",type="number",JSONPath=".status.targetVersion.rampPercentage",description="Ramp percentage"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"

// TemporalWorkerDeployment is the Schema for the temporalworkerdeployments API
type TemporalWorkerDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TemporalWorkerDeploymentSpec   `json:"spec,omitempty"`
	Status TemporalWorkerDeploymentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TemporalWorkerDeploymentList contains a list of TemporalWorkerDeployment
type TemporalWorkerDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TemporalWorkerDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TemporalWorkerDeployment{}, &TemporalWorkerDeploymentList{})
}
