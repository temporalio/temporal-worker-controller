// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AuthMode is the mode for authenticating to a Temporal server.
type AuthMode string

const (
	AuthModeTLS                 AuthMode = "TLS"
	AuthModeAPIKey              AuthMode = "API_KEY"
	AuthModeServiceAccountToken AuthMode = "SERVICE_ACCOUNT_TOKEN"
	AuthModeNoCredentials       AuthMode = "NO_CREDENTIALS"
	// Add more auth modes here as they are supported

	// DefaultServiceAccountTokenExpirationSeconds is the token lifetime used when
	// ServiceAccountTokenAuth.ExpirationSeconds is unset. One hour matches the
	// Kubernetes projected-token default.
	DefaultServiceAccountTokenExpirationSeconds int64 = 3600
	// MinServiceAccountTokenExpirationSeconds is the Kubernetes TokenRequest minimum.
	MinServiceAccountTokenExpirationSeconds int64 = 600
	// MaxServiceAccountTokenExpirationSeconds rejects values far beyond a typical
	// apiserver --service-account-max-token-expiration. The apiserver may still
	// issue a shorter token than requested.
	MaxServiceAccountTokenExpirationSeconds int64 = 31536000
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
	// the system trust store for this connection. Applicable when the auth mode is
	// AuthModeAPIKey, AuthModeServiceAccountToken, or AuthModeNoCredentials; for AuthModeTLS, bundle the CA into
	// MutualTLSSecretRef's own "ca.crt" key instead.
	// The Secret must be of type Opaque or
	// kubernetes.io/tls and exist in the same Kubernetes namespace as the Connection.
	// +optional
	CACertSecretRef *SecretReference `json:"caCertSecretRef,omitempty"`
}

// ServiceAccountTokenAuth authenticates with a projected Kubernetes ServiceAccount
// token. Worker pods receive a token volume that kubelet rotates. The controller
// re-reads its own projected token on each RPC. This is for self-hosted Temporal
// with a JWT ClaimMapper; Temporal Cloud does not accept these tokens.
type ServiceAccountTokenAuth struct {
	// Audience is the TokenRequest audience. The Temporal server's JWT ClaimMapper
	// must expect this value. Worker pods request a token for their own ServiceAccount
	// with this audience.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Audience string `json:"audience"`

	// ExpirationSeconds is the requested token lifetime. Kubernetes may issue a
	// shorter token. Unset means 3600 seconds.
	// +optional
	// +kubebuilder:validation:Minimum=600
	// +kubebuilder:validation:Maximum=31536000
	ExpirationSeconds *int64 `json:"expirationSeconds,omitempty"`
}

// ExpirationSecondsOrDefault returns ExpirationSeconds, or
// DefaultServiceAccountTokenExpirationSeconds when unset.
func (s *ServiceAccountTokenAuth) ExpirationSecondsOrDefault() int64 {
	if s == nil || s.ExpirationSeconds == nil {
		return DefaultServiceAccountTokenExpirationSeconds
	}
	return *s.ExpirationSeconds
}

// ConnectionSpec defines the desired state of Connection
// +kubebuilder:validation:XValidation:rule="!(has(self.mutualTLSSecretRef) && has(self.apiKeySecretRef))",message="Only one of mutualTLSSecretRef or apiKeySecretRef may be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.mutualTLSSecretRef) && has(self.serviceAccountToken))",message="Only one of mutualTLSSecretRef or serviceAccountToken may be set"
// +kubebuilder:validation:XValidation:rule="!(has(self.apiKeySecretRef) && has(self.serviceAccountToken))",message="Only one of apiKeySecretRef or serviceAccountToken may be set"
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
	APIKeySecretRef *corev1.SecretKeySelector `json:"apiKeySecretRef,omitempty"`

	// ServiceAccountToken configures bearer authentication with a projected
	// Kubernetes ServiceAccount token. Mutually exclusive with MutualTLSSecretRef
	// and APIKeySecretRef. Worker pods get a projected token volume and
	// TEMPORAL_API_KEY_FILE. The controller reads the token file given by
	// --service-account-token-path.
	// +optional
	ServiceAccountToken *ServiceAccountTokenAuth `json:"serviceAccountToken,omitempty"`
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
	case AuthModeServiceAccountToken:
		if s.ServiceAccountToken == nil || s.ServiceAccountToken.Audience == "" {
			return errors.New("service account token audience is not set")
		}
		if exp := s.ServiceAccountToken.ExpirationSeconds; exp != nil {
			if *exp < MinServiceAccountTokenExpirationSeconds || *exp > MaxServiceAccountTokenExpirationSeconds {
				return fmt.Errorf("service account token expirationSeconds must be between %d and %d",
					MinServiceAccountTokenExpirationSeconds, MaxServiceAccountTokenExpirationSeconds)
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
	case s.APIKeySecretRef != nil:
		return AuthModeAPIKey
	case s.ServiceAccountToken != nil:
		return AuthModeServiceAccountToken
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
