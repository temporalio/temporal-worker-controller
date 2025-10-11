// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SecretReference contains the name of a Secret resource in the same namespace.
type SecretReference struct {
	// Name of the Secret resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// TemporalConnectionSpec defines the desired state of TemporalConnection
// +kubebuilder:validation:XValidation:rule="!(has(self.mutualTLSSecretRef) && has(self.apiKeySecretRef))",message="Only one of mutualTLSSecretRef or apiKeySecretRef may be set"
type TemporalConnectionSpec struct {
	// The host and port of the Temporal server.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9.-]+:[0-9]+$`
	HostPort string `json:"hostPort"`

	// MutualTLSSecretRef is the name of the Secret that contains the TLS certificate and key
	// for mutual TLS authentication. The secret must be `type: kubernetes.io/tls` and exist
	// in the same Kubernetes namespace as the TemporalConnection resource.
	//
	// More information about creating a TLS secret:
	// https://kubernetes.io/docs/concepts/configuration/secret/#tls-secrets
	// +optional
	MutualTLSSecretRef *SecretReference `json:"mutualTLSSecretRef,omitempty"`

	// APIKeySecretRef is the name of the Secret that contains the API key. The secret must be `type: kubernetes.io/opaque` and exist
	// in the same Kubernetes namespace as the TemporalConnection resource.
	// +optional
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`
}

// TemporalConnectionStatus defines the observed state of TemporalConnection
type TemporalConnectionStatus struct {
	// TODO(jlegrone): Add additional status fields following Kubernetes API conventions
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=tconn
//+kubebuilder:printcolumn:name="Host",type="string",JSONPath=".spec.hostPort",description="Temporal server endpoint"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"

// TemporalConnection is the Schema for the temporalconnections API
type TemporalConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TemporalConnectionSpec   `json:"spec,omitempty"`
	Status TemporalConnectionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TemporalConnectionList contains a list of TemporalConnection
type TemporalConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TemporalConnection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TemporalConnection{}, &TemporalConnectionList{})
}
