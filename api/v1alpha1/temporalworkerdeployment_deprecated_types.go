package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TemporalWorkerDeployment is deprecated. Use WorkerDeployment instead.
//
// This stub exists so the controller scheme recognises objects stored under
// the deprecated CRD. The deprecated CRD rejects new creates via a CEL rule
// (oldSelf != null) so only pre-existing instances reach the controller.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=twd;twdeployment;tworkerdeployment
type TemporalWorkerDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is stored as-is; the controller does not interpret it.
	// +kubebuilder:pruning:PreserveUnknownFields
	Spec runtime.RawExtension `json:"spec,omitempty"`

	Status DeprecatedTWDStatus `json:"status,omitempty"`
}

// DeprecatedTWDStatus holds the observed state of a TemporalWorkerDeployment stub.
type DeprecatedTWDStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// TemporalWorkerDeploymentList contains a list of TemporalWorkerDeployment.
type TemporalWorkerDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TemporalWorkerDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TemporalWorkerDeployment{}, &TemporalWorkerDeploymentList{})
}
