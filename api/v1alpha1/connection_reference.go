package v1alpha1

import corev1 "k8s.io/api/core/v1"

// ConnectionReference identifies a connection resource to use. Exactly one of
// Name or ObjectRef must be set.
//
// Name is a shorthand that selects a namespaced Connection in the
// WorkerDeployment's own namespace. ObjectRef is the general form carrying full
// type information (apiGroup + kind + name) and selects either a namespaced
// Connection or a cluster-scoped ClusterConnection.
//
// The authentication secret is always in the WorkerDeployment's own
// namespace regardless of which connection is referenced. Cross-namespace
// connection is not yet implemented, so objectRef.namespace must NOT
// be set.
// +kubebuilder:validation:XValidation:rule="has(self.name) != has(self.objectRef)",message="exactly one of name or objectRef must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.objectRef) || self.objectRef.kind in ['Connection','ClusterConnection']",message="objectRef.kind must be Connection or ClusterConnection"
// +kubebuilder:validation:XValidation:rule="!has(self.objectRef) || self.objectRef.apiGroup == 'temporal.io'",message="objectRef.apiGroup must be temporal.io"
// +kubebuilder:validation:XValidation:rule="!has(self.objectRef) || !has(self.objectRef.__namespace__)",message="objectRef.namespace is not supported yet"
type ConnectionReference struct {
	// Name of a namespaced Connection in the WorkerDeployment's namespace.
	// Shorthand for objectRef: {apiGroup: temporal.io, kind: Connection, name: <name>}.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name,omitempty"`
	// ObjectRef references the connection resource by full type information.
	// kind must be "Connection" or "ClusterConnection", apiGroup must be
	// "temporal.io", and namespace must not be set (not yet supported).
	// +optional
	ObjectRef *corev1.TypedObjectReference `json:"objectRef,omitempty"`
}

// IsCluster reports whether the ConnectionReference targets a cluster-scoped
// ClusterConnection.
func (r ConnectionReference) IsCluster() bool {
	return r.ObjectRef != nil && r.ObjectRef.Kind == "ClusterConnection"
}

// ResolvedName returns the connection resource name from either form.
func (r ConnectionReference) ResolvedName() string {
	if r.ObjectRef != nil {
		return r.ObjectRef.Name
	}
	return r.Name
}

// ResolvesTo reports whether a ConnectionReference resolves to the same
// connection as another ConnectionReference.
func (r ConnectionReference) ResolvesTo(other ConnectionReference) bool {
	return (r.ResolvedName() == other.ResolvedName() && r.IsCluster() == other.IsCluster())
}
