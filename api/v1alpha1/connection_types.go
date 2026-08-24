// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AuthMode is the mode for authenticating to a Temporal server.
type AuthMode string

const (
	AuthModeTLS           AuthMode = "TLS"
	AuthModeAPIKey        AuthMode = "API_KEY"
	AuthModeNoCredentials AuthMode = "NO_CREDENTIALS"
	// Add more auth modes here as they are supported
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SecretReference contains the name of a Secret resource in the same namespace.
type SecretReference struct {
	// Name of the Secret resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// ConnectionTLSConfig defines TLS settings for a Connection.
type ConnectionTLSConfig struct {
	// ServerName overrides the server name used to verify the Temporal server
	// certificate when TLS is enabled.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9.-]+$`
	ServerName string `json:"serverName,omitempty"`

	// CACertSecretRef references a Secret (key "ca.crt") whose certificate is appended to
	// the system trust store for this connection, independent of AuthMode.
	// The Secret must be of type Opaque or
	// kubernetes.io/tls and exist in the same Kubernetes namespace as the Connection.
	// +optional
	CACertSecretRef *SecretReference `json:"caCertSecretRef,omitempty"`
}

// ConnectionSpec defines the desired state of Connection
// +kubebuilder:validation:XValidation:rule="!(has(self.mutualTLSSecretRef) && has(self.apiKeySecretRef))",message="Only one of mutualTLSSecretRef or apiKeySecretRef may be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.mutualTLSSecretRef) && has(self.tls) && has(self.tls.caCertSecretRef))",message="tls.caCertSecretRef cannot be combined with mutualTLSSecretRef; bundle the CA into that secret's own ca.crt key instead"
type ConnectionSpec struct {
	// The host and port of the Temporal server.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9.-]+:[0-9]+$`
	HostPort string `json:"hostPort"`

	// TLS configures TLS behavior for the Temporal server connection.
	// +optional
	TLS *ConnectionTLSConfig `json:"tls,omitempty"`

	// MutualTLSSecretRef is the name of the Secret that contains the TLS certificate and key
	// for mutual TLS authentication. The secret must be `type: kubernetes.io/tls` or
	// `type: Opaque` and exist in the same Kubernetes namespace as the Connection
	// resource. Opaque secrets are useful when bundling tls.crt, tls.key, and ca.crt into
	// a single secret (e.g. multi-file cert-manager outputs).
	//
	// More information about creating a TLS secret:
	// https://kubernetes.io/docs/concepts/configuration/secret/#tls-secrets
	// +optional
	MutualTLSSecretRef *SecretReference `json:"mutualTLSSecretRef,omitempty"`

	// APIKeySecretRef selects the Secret key that contains the API key used for authentication.
	// The Secret must be `type: kubernetes.io/opaque` and exist in the same Kubernetes namespace as
	// the Connection resource. This is a corev1.SecretKeySelector and encodes both:
	//   - LocalObjectReference.Name: the name of the Secret resource
	//   - Key: the data key within Secret.Data whose value is the API key token
	// +optional
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`
}

// Validate returns an error if the ConnectionSpec is not valid.
func (s ConnectionSpec) Validate() error {
	switch s.AuthMode() {
	case AuthModeTLS:
		if s.MutualTLSSecretRef == nil || s.MutualTLSSecretRef.Name == "" {
			return errors.New("TLS secret name is not set")
		}
	case AuthModeAPIKey:
		if s.APIKeySecretRef == nil || s.APIKeySecretRef.Name == "" {
			return errors.New("API key secret name is not set")
		}
	}
	return nil
}

// AuthMode returns the authentication mode for the ConnectionSpec.
func (s ConnectionSpec) AuthMode() AuthMode {
	switch {
	case s.MutualTLSSecretRef != nil:
		return AuthModeTLS
	case s.APIKeySecretRef != nil:
		return AuthModeAPIKey
	default:
		return AuthModeNoCredentials
	}
}

// SecretName extracts the secret name from the ConnectionSpec, returning an
// empty string authentication mode does not requires it.
func (s ConnectionSpec) SecretName() string {
	switch s.AuthMode() {
	case AuthModeTLS:
		if s.MutualTLSSecretRef == nil {
			return ""
		}
		return s.MutualTLSSecretRef.Name
	case AuthModeAPIKey:
		if s.APIKeySecretRef == nil {
			return ""
		}
		return s.APIKeySecretRef.Name
	default:
		return ""
	}
}

func (s ConnectionSpec) TLSServerName() string {
	if s.TLS == nil {
		return ""
	}
	return s.TLS.ServerName
}

// CACertSecretName returns the name of the Secret referenced by TLS.CACertSecretRef, or an
// empty string when unset.
func (s ConnectionSpec) CACertSecretName() string {
	if s.TLS == nil || s.TLS.CACertSecretRef == nil {
		return ""
	}
	return s.TLS.CACertSecretRef.Name
}

// ConnectionStatus defines the observed state of Connection
type ConnectionStatus struct {
	// TODO(jlegrone): Add additional status fields following Kubernetes API conventions
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Host",type="string",JSONPath=".spec.hostPort",description="Temporal server endpoint"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"

// Connection is the Schema for the connection API
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConnectionSpec   `json:"spec,omitempty"`
	Status ConnectionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ConnectionList contains a list of Connection
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Connection{}, &ConnectionList{})
}
