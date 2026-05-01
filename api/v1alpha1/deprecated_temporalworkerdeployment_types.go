// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
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

type DeprecatedWorkerOptions struct {
	// The name of a TemporalConnection in the same namespace as the TemporalWorkerDeployment.
	TemporalConnectionRef TemporalConnectionReference `json:"connectionRef"`
	// The Temporal namespace for the worker to connect to.
	// +kubebuilder:validation:MinLength=1
	TemporalNamespace string `json:"temporalNamespace"`
	// UnsafeCustomBuildID optionally overrides the auto-generated build ID for this worker deployment.
	// When set, the controller uses this value instead of computing a build ID from the
	// pod template hash. This enables rolling updates for non-workflow code changes
	// (bug fixes, config changes) while preserving the same build ID.
	//
	// WARNING: Using a custom build ID requires careful management. If workflow code changes
	// but UnsafeCustomBuildID stays the same, pinned workflows may execute on workers running incompatible
	// code. Only use this when you have a reliable way to detect changes in your workflow
	// definitions (e.g., hashing workflow source files in CI/CD).
	//
	// When the UnsafeCustomBuildID is stable but pod template spec changes, the controller triggers
	// a rolling update instead of creating a new deployment version. The controller uses
	// a hash of the user-provided pod template spec to detect ANY changes, including
	// container images, env vars, commands, volumes, resources, and all other fields.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$`
	UnsafeCustomBuildID string `json:"unsafeCustomBuildID,omitempty"`
}

// TemporalWorkerDeploymentSpec defines the desired state of TemporalWorkerDeployment
type TemporalWorkerDeploymentSpec struct {

	// Number of desired pods. When set, the controller manages replicas for all active
	// worker versions. When omitted (nil), the controller creates versioned Deployments
	// with nil replicas and never calls UpdateScale on active versions — following the
	// Kubernetes-recommended pattern for HPA and other external autoscalers
	// (https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/#migrating-deployments-and-statefulsets-to-horizontal-autoscaling).
	// The controller still scales drained versions (and inactive versions that are not
	// the rollout target) to zero regardless.
	// This field makes TemporalWorkerDeploymentSpec implement the scale subresource, which is compatible with auto-scalers.
	// +optional
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
	WorkerOptions DeprecatedWorkerOptions `json:"workerOptions"`
}

// Condition reason constants for TemporalWorkerDeployment.
//
// These strings appear in status.conditions[].reason and are part of the CRD's
// status API. Operators, monitoring rules, and scripts may depend on them.
// They should be treated as stable within an API version and renamed only with
// a corresponding version bump.
const (
	// ReasonTemporalConnectionNotFound is set on ConditionProgressing=False when the
	// referenced TemporalConnection resource cannot be found.
	ReasonTemporalConnectionNotFound = "TemporalConnectionNotFound"

	// Deprecated: Use ReasonRolloutComplete on ConditionReady instead.
	ReasonTemporalConnectionHealthy = "TemporalConnectionHealthy"
)

// TemporalWorkerDeploymentStatus defines the observed state of TemporalWorkerDeployment
type TemporalWorkerDeploymentStatus struct {
	// Remember, status should be able to be reconstituted from the state of the world,
	// so it's generally not a good idea to read from the status of the root object.
	// Instead, you should reconstruct it every run.

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

	// ManagerIdentity is the identity that has exclusive rights to modify this Worker Deployment's routing config.
	// When set, clients whose identity does not match will be blocked from making routing changes.
	// Empty by default. Use `temporal worker deployment manager-identity set/unset` to change.
	// +optional
	ManagerIdentity string `json:"managerIdentity,omitempty"`

	// VersionCount is the total number of versions currently known by the worker deployment.
	// This includes current, target, ramping, and deprecated versions.
	// +optional
	// +kubebuilder:validation:Minimum=0
	VersionCount int32 `json:"versionCount,omitempty"`

	// Conditions represent the latest available observations of the TemporalWorkerDeployment's current state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
// +kubebuilder:resource:shortName=twd;twdeployment;tworkerdeployment
//+kubebuilder:printcolumn:name="Current",type="string",JSONPath=".status.currentVersion.buildID",description="Current build ID"
//+kubebuilder:printcolumn:name="Target",type="string",JSONPath=".status.targetVersion.buildID",description="Target build ID"
//+kubebuilder:printcolumn:name="Ramp %",type="number",JSONPath=".status.targetVersion.rampPercentage",description="Ramp percentage"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="name cannot be more than 63 characters"
// +kubebuilder:validation:XValidation:rule="oldSelf.hasValue()",message="TemporalWorkerDeployment is deprecated and cannot be created. Use WorkerDeployment instead.",optionalOldSelf=true
// +kubebuilder:deprecatedversion:warning="TemporalWorkerDeployment is deprecated. Use WorkerDeployment instead."

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
