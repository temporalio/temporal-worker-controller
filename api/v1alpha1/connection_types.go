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

	// CACertSecretRef references a Secret with a required "ca.crt" key whose certificate is appended to
	// the system trust store for this connection. Applicable when the auth mode is either
	// AuthModeAPIKey or AuthModeNoCredentials; for AuthModeTLS, bundle the CA into
	// MutualTLSSecretRef's own "ca.crt" key instead.
	// The Secret must be of type Opaque or
	// kubernetes.io/tls and exist in the same Kubernetes namespace as the Connection.
	// +optional
	CACertSecretRef *SecretReference `json:"caCertSecretRef,omitempty"`
}

// APIKeyConfig contains desired state for a Connection that uses Temporal API
// Keys for authentication.
//
// See: https://docs.temporal.io/cloud/api-keys
//
// You can populate the API Key via a Kubernetes Secret using the `secretRef`
// field. Alternately, you can write the API Key to a file in the Pod and use
// the `path` field to tell the controller about that API Key file.
// +kubebuilder:validation:XValidation:rule="has(self.secretRef) != has(self.path)",message="Only one of secretRef or path may be set"
type APIKeyConfig struct {
	// SecretRef selects the Secret key that contains the API key used for
	// authentication. The Secret must be `type: kubernetes.io/opaque` and
	// exist in the same Kubernetes namespace as the Connection resource.
	//
	// This is a corev1.SecretKeySelector and encodes both:
	//   - LocalObjectReference.Name: the name of the Secret resource
	//   - Key: the data key within Secret.Data whose value is the API key token
	// +optional
	SecretRef *corev1.SecretKeySelector `json:"secretRef,omitempty"`
	// Path is the file path in the Pod where the API Key can be found.
	// +optional
	Path string `json:"path,omitempty"`
}

// ConnectionSpec defines the desired state of Connection
// +kubebuilder:validation:XValidation:rule="!(has(self.mutualTLSSecretRef) && (has(self.apiKeySecretRef) || has(self.apiKey)))",message="Only one of mutualTLSSecretRef, apiKeySecretRef or apiKey may be set"
// +kubebuilder:validation:XValidation:rule="(has(self.mutualTLSSecretRef) || (!(has(self.apiKeySecretRef) && has(self.apiKey))))",message="Only one of apiKeySecretRef or apiKey may be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.mutualTLSSecretRef) && has(self.tls) && has(self.tls.caCertSecretRef))",message="tls.caCertSecretRef cannot be combined with mutualTLSSecretRef; bundle the CA into that secret's own ca.crt key instead"
type ConnectionSpec struct {
	// HostPort is the Temporal server endpoint. Accepts a host:port (e.g.
	// "production.tmprl.cloud:7233") or a gRPC resolver target URI
	// (e.g. "dns:///production.tmprl.cloud:7233",
	// "xds://example.dest").
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9.-]+:[0-9]+|(dns|xds|passthrough)://[a-zA-Z0-9._/:-]+)$`
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
	// Deprecated: Use apiKey.secretRef instead
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`

	// APIKey contains configuration when authenticating to the Temporal API
	// using API Keys.
	// +optional
	APIKey *APIKeyConfig `json:"apiKey,omitempty"`
}

// Validate returns an error if the ConnectionSpec is not valid.
func (s ConnectionSpec) Validate() error {
	switch s.AuthMode() {
	case AuthModeTLS:
		if s.MutualTLSSecretRef == nil || s.MutualTLSSecretRef.Name == "" {
			return errors.New("TLS secret name is not set")
		}
	case AuthModeAPIKey:
		if s.APIKeySecretRef != nil {
			if s.APIKeySecretRef.Name == "" {
				return errors.New("API key secret name is not set")
			}
			return nil
		}
		// CEL validation ensures either APIKey or APIKeySecretRef is non-nil.
		if s.APIKey.SecretRef != nil {
			if s.APIKey.SecretRef.Name == "" {
				return errors.New("API key secret name is not set")
			}
		} else {
			if s.APIKey.Path == "" {
				return errors.New("API key path is not set")
			}
		}
	}
	return nil
}

// AuthMode returns the authentication mode for the ConnectionSpec.
func (s ConnectionSpec) AuthMode() AuthMode {
	switch {
	case s.MutualTLSSecretRef != nil:
		return AuthModeTLS
	case s.APIKeySecretRef != nil || s.APIKey != nil:
		return AuthModeAPIKey
	default:
		return AuthModeNoCredentials
	}
}

// SecretName extracts the secret name from the ConnectionSpec, returning an
// empty string if authentication mode does not requires it.
func (s ConnectionSpec) SecretName() string {
	switch s.AuthMode() {
	case AuthModeNoCredentials:
		return ""
	case AuthModeTLS:
		if s.MutualTLSSecretRef == nil {
			return ""
		}
		return s.MutualTLSSecretRef.Name
	case AuthModeAPIKey:
		if s.APIKeySecretRef != nil {
			return s.APIKeySecretRef.Name
		}
		// CEL validation ensures either APIKey or APIKeySecretRef is non-nil,
		// however we still need to check whether APIKey.SecretRef is nil,
		// since the user may have specified Path instead.
		if s.APIKey.SecretRef != nil {
			return s.APIKey.SecretRef.Name
		}
		return ""
	default:
		return ""
	}
}

// SecretKeySelector returns the corev1.SecretKeySelector that is configured for
// the ConnectionSpec, or nil if either not using API Key authentication *or*
// using the path-based APIKey configuration.
func (s ConnectionSpec) SecretKeySelector() *corev1.SecretKeySelector {
	if s.AuthMode() != AuthModeAPIKey {
		return nil
	}
	if s.APIKeySecretRef != nil {
		return s.APIKeySecretRef
	}
	// CEL validation ensures either APIKey or APIKeySecretRef is non-nil,
	return s.APIKey.SecretRef
}

func (s ConnectionSpec) TLSServerName() string {
	if s.TLS == nil {
		return ""
	}
	return s.TLS.ServerName
}

// TLSCACertSecretName returns the name of the Secret referenced by TLS.CACertSecretRef, or an
// empty string when unset.
func (s ConnectionSpec) TLSCACertSecretName() string {
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
