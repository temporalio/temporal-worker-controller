// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// TemporalConnectionSpec defines the desired state of TemporalConnection
type TemporalConnectionSpec struct {
	// The host and port of the Temporal server.
	HostPort string `json:"hostPort"`
}

// TemporalConnectionStatus defines the observed state of TemporalConnection
type TemporalConnectionStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

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
