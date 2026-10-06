// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/distribution/reference"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/k8s.io/utils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DeployOwnerKey = ".metadata.controller"
	// BuildIDLabel is the label that identifies the build ID for a deployment
	BuildIDLabel = "temporal.io/build-id"
	// WorkerDeploymentNameLabel identifies Deployments managed for a TemporalWorkerDeployment.
	WorkerDeploymentNameLabel = "temporal.io/deployment-name"
	// WorkerGroupLabel names the worker group of a Deployment in a version that uses groups.
	// Deployments without it belong to a WorkerDeployment without groups.
	WorkerGroupLabel = "temporal.io/worker-group"
	// WorkerDeploymentNameSeparator joins the K8s namespace and the WorkerDeployment resource
	// name to form the Temporal-server-side worker deployment name (namespace/wdName).
	WorkerDeploymentNameSeparator = "/"
	// WorkerDeploymentNameSeparatorK8sLabelCompliant is the substitute used when the
	// Temporal-server worker deployment name needs to be stored in a Kubernetes label value
	// (which disallows "/"). See cleanDeploymentNameForK8sLabelValue.
	WorkerDeploymentNameSeparatorK8sLabelCompliant = "_"
	ResourceNameSeparator                          = "-"
	MaxBuildIDLen                                  = 63
	MaxDeploymentNameLen                           = 47
	ConnectionSpecHashAnnotation                   = "temporal.io/connection-spec-hash"
	PodTemplateSpecHashAnnotation                  = "temporal.io/pod-template-spec-hash"
	groupsBuildIDHashLen                           = 10

	// Environment variables read by the Temporal Go SDK's envconfig package
	// (go.temporal.io/sdk/contrib/envconfig) to configure the worker's connection.
	EnvTemporalAddress             = "TEMPORAL_ADDRESS"
	EnvTemporalNamespace           = "TEMPORAL_NAMESPACE"
	EnvTemporalDeploymentName      = "TEMPORAL_DEPLOYMENT_NAME"
	EnvTemporalWorkerBuildID       = "TEMPORAL_WORKER_BUILD_ID"
	EnvTemporalTLS                 = "TEMPORAL_TLS"
	EnvTemporalTLSServerName       = "TEMPORAL_TLS_SERVER_NAME"
	EnvTemporalTLSClientKeyPath    = "TEMPORAL_TLS_CLIENT_KEY_PATH"
	EnvTemporalTLSClientCertPath   = "TEMPORAL_TLS_CLIENT_CERT_PATH"
	EnvTemporalTLSServerCACertPath = "TEMPORAL_TLS_SERVER_CA_CERT_PATH"
	EnvTemporalAPIKey              = "TEMPORAL_API_KEY"

	// TemporalTLSVolumeName is the mTLS client cert/key secret volume mounted into worker
	// pods when Connection.mutualTLSSecretRef is set.
	TemporalTLSVolumeName     = "temporal-tls"
	TemporalTLSMountPath      = "/etc/temporal/tls"
	TemporalTLSClientKeyPath  = TemporalTLSMountPath + "/tls.key"
	TemporalTLSClientCertPath = TemporalTLSMountPath + "/tls.crt"

	// TemporalTLSCAVolumeName is the private-CA secret volume mounted into worker pods when
	// Connection.tls.caCertSecretRef is set. See ConnectionTLSConfig.CACertSecretRef.
	TemporalTLSCAVolumeName = "temporal-tls-ca"
	TemporalTLSCAMountPath  = "/etc/temporal/tls-ca"
	TemporalTLSCACertPath   = TemporalTLSCAMountPath + "/ca.crt"
)

// DeploymentState represents the Kubernetes state of all deployments for a temporal worker deployment
type DeploymentState struct {
	// Map of buildID to the default group's deployment. Use VersionDeployments to
	// decide whether a version has any deployment.
	Deployments map[string]*appsv1.Deployment
	// Sorted deployments by creation time
	DeploymentsByTime []*appsv1.Deployment
	// Map of buildID to the default group's deployment reference
	DeploymentRefs map[string]*corev1.ObjectReference
	// Map of buildID to group name to deployment, for every group including the default
	WorkerGroupDeployments map[string]map[string]*appsv1.Deployment
}

// VersionDeployments returns every group's deployment for a build ID, keyed by group name.
func (s *DeploymentState) VersionDeployments(buildID string) map[string]*appsv1.Deployment {
	if s.WorkerGroupDeployments != nil {
		return s.WorkerGroupDeployments[buildID]
	}
	// States built without WorkerGroupDeployments (e.g. in tests) only have default groups.
	if d, ok := s.Deployments[buildID]; ok {
		return map[string]*appsv1.Deployment{temporaliov1alpha1.DefaultWorkerGroupName: d}
	}
	return nil
}

// VersionDeploymentList returns every group's deployment for a build ID: named
// groups sorted by name, then the default group.
func (s *DeploymentState) VersionDeploymentList(buildID string) []*appsv1.Deployment {
	groups := s.VersionDeployments(buildID)
	names := make([]string, 0, len(groups))
	for name := range groups {
		if name != temporaliov1alpha1.DefaultWorkerGroupName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	list := make([]*appsv1.Deployment, 0, len(groups))
	for _, name := range names {
		list = append(list, groups[name])
	}
	if d, ok := groups[temporaliov1alpha1.DefaultWorkerGroupName]; ok {
		list = append(list, d)
	}
	return list
}

// BuildIDs returns the sorted build IDs that have at least one deployment.
func (s *DeploymentState) BuildIDs() []string {
	seen := make(map[string]struct{}, len(s.Deployments)+len(s.WorkerGroupDeployments))
	for buildID := range s.Deployments {
		seen[buildID] = struct{}{}
	}
	for buildID := range s.WorkerGroupDeployments {
		seen[buildID] = struct{}{}
	}
	buildIDs := make([]string, 0, len(seen))
	for buildID := range seen {
		buildIDs = append(buildIDs, buildID)
	}
	sort.Strings(buildIDs)
	return buildIDs
}

// HasWorkerGroupLabel reports whether any of a version's deployments carries the group label,
// which marks a multi-group version.
func HasWorkerGroupLabel(deployments map[string]*appsv1.Deployment) bool {
	for _, d := range deployments {
		if _, ok := d.Labels[WorkerGroupLabel]; ok {
			return true
		}
	}
	return false
}

// WorkerGroupName returns the worker group a deployment belongs to.
func WorkerGroupName(d *appsv1.Deployment) string {
	if group := d.GetLabels()[WorkerGroupLabel]; group != "" {
		return group
	}
	return temporaliov1alpha1.DefaultWorkerGroupName
}

// GetDeploymentState queries Kubernetes to get the state of all deployments
// associated with a WorkerDeployment
func GetDeploymentState(
	ctx context.Context,
	k8sClient client.Client,
	namespace string,
	ownerName string,
	workerDeploymentName string,
) (*DeploymentState, error) {
	// List k8s deployments that correspond to managed worker deployment versions
	var childDeploys appsv1.DeploymentList
	if err := k8sClient.List(
		ctx,
		&childDeploys,
		client.InNamespace(namespace),
		client.MatchingFields{DeployOwnerKey: ownerName},
	); err != nil {
		return nil, fmt.Errorf("unable to list child deployments: %w", err)
	}

	// Sort deployments by creation timestamp
	sort.SliceStable(childDeploys.Items, func(i, j int) bool {
		return childDeploys.Items[i].ObjectMeta.CreationTimestamp.Before(&childDeploys.Items[j].ObjectMeta.CreationTimestamp)
	})

	deploys := make([]*appsv1.Deployment, len(childDeploys.Items))
	for i := range childDeploys.Items {
		deploys[i] = &childDeploys.Items[i]
	}
	return NewDeploymentState(deploys...), nil
}

// NewDeploymentState indexes deployments by build ID and group, keeping their order.
// Deployments without the build ID label are ignored.
func NewDeploymentState(deploys ...*appsv1.Deployment) *DeploymentState {
	state := &DeploymentState{
		Deployments:            make(map[string]*appsv1.Deployment),
		DeploymentsByTime:      []*appsv1.Deployment{},
		DeploymentRefs:         make(map[string]*corev1.ObjectReference),
		WorkerGroupDeployments: make(map[string]map[string]*appsv1.Deployment),
	}
	for _, deploy := range deploys {
		buildID, ok := deploy.GetLabels()[BuildIDLabel]
		if !ok {
			continue
		}
		group := WorkerGroupName(deploy)
		if state.WorkerGroupDeployments[buildID] == nil {
			state.WorkerGroupDeployments[buildID] = make(map[string]*appsv1.Deployment)
		}
		state.WorkerGroupDeployments[buildID][group] = deploy
		state.DeploymentsByTime = append(state.DeploymentsByTime, deploy)
		if group == temporaliov1alpha1.DefaultWorkerGroupName {
			state.Deployments[buildID] = deploy
			state.DeploymentRefs[buildID] = NewObjectRef(deploy)
		}
	}
	return state
}

// IsDeploymentHealthy checks if a deployment is in the "Available" state
func IsDeploymentHealthy(deployment *appsv1.Deployment) (bool, *metav1.Time) {
	// TODO(jlegrone): do we need to sort conditions by timestamp to check only latest?
	for _, c := range deployment.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true, &c.LastTransitionTime
		}
	}
	return false, nil
}

// NewObjectRef creates a reference to a Kubernetes object
func NewObjectRef(obj client.Object) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(),
		Kind:       obj.GetObjectKind().GroupVersionKind().Kind,
		Name:       obj.GetName(),
		Namespace:  obj.GetNamespace(),
		UID:        obj.GetUID(),
	}
}

func ComputeBuildID(w *temporaliov1alpha1.WorkerDeployment) string {
	// Check for user-provided build ID in spec.workerOptions.unsafeCustomBuildID
	if override := w.Spec.WorkerOptions.UnsafeCustomBuildID; override != "" {
		cleaned := cleanBuildID(override)
		if cleaned != "" {
			return TruncateString(cleaned, MaxBuildIDLen)
		}
		// Fall through to default hash-based generation if buildID is invalid after cleaning
	}

	if w.Spec.HasWorkerGroups() {
		return computeGroupsBuildID(w.Spec)
	}
	depSpec := w.Spec.DeploymentSpec()

	if img := firstImage(depSpec.Template); img != "" {
		return imagePrefixedBuildID(img, utils.ComputeHash(&depSpec.Template, nil, true))
	}
	return utils.ComputeHash(&depSpec.Template, nil, false)
}

// computeGroupsBuildID hashes every group's name and pod template, so a change to any
// group starts one new version for all of them. Groups are sorted by name, so their order
// in the spec doesn't matter.
func computeGroupsBuildID(spec temporaliov1alpha1.WorkerDeploymentSpec) string {
	type groupTemplate struct {
		Name     string                 `json:"name"`
		Template corev1.PodTemplateSpec `json:"template"`
	}
	sorted := slices.Clone(spec.WorkerGroups)
	slices.SortFunc(sorted, func(a, b temporaliov1alpha1.WorkerGroup) int { return strings.Compare(a.Name, b.Name) })
	groups := make([]groupTemplate, 0, len(sorted))
	for _, p := range sorted {
		groups = append(groups, groupTemplate{Name: p.Name, Template: p.Deployment.Template})
	}
	data, _ := json.Marshal(groups) // never errors for these types
	hash := HashString(string(data))[:groupsBuildIDHashLen]

	if img := firstImage(sorted[0].Deployment.Template); img != "" {
		return imagePrefixedBuildID(img, hash)
	}
	return hash
}

func firstImage(template corev1.PodTemplateSpec) string {
	if containers := template.Spec.Containers; len(containers) > 0 {
		return containers[0].Image
	}
	return ""
}

// imagePrefixedBuildID prefixes hash with the image's tag, digest or path, cut to fit the
// build ID length limit.
func imagePrefixedBuildID(image, hash string) string {
	suffix := ResourceNameSeparator + hash
	return cleanBuildID(computeImagePrefix(image, MaxBuildIDLen-len(suffix)) + suffix)
}

// ComputeWorkerDeploymentName generates the base worker deployment name
func ComputeWorkerDeploymentName(w *temporaliov1alpha1.WorkerDeployment) string {
	return computeWorkerDeploymentName(w.GetNamespace(), w.GetName())
}

func computeWorkerDeploymentName(k8sNamespace, workerDeploymentResourceName string) string {
	// Use the name and namespace to form the worker deployment name
	return k8sNamespace + WorkerDeploymentNameSeparator + workerDeploymentResourceName
}

// ComputeVersionedDeploymentName generates a name for a versioned deployment
// Name will be <=47 characters and unique for that Worker Deployment Version within the namespace.
func ComputeVersionedDeploymentName(baseName, buildID string) string {
	fullName := baseName + ResourceNameSeparator + buildID
	if len(fullName) > MaxDeploymentNameLen {
		hashName := HashString(fullName)[:10]
		fullName = TruncateString(baseName, 10) + ResourceNameSeparator + TruncateString(buildID, 10) + ResourceNameSeparator + hashName
	}
	return CleanStringForDNS(fullName)
}

// ComputeWorkerGroupDeploymentName names a named group's versioned Deployment. The hash of the
// full triple keeps it from ever taking another group's or a default group's name.
func ComputeWorkerGroupDeploymentName(baseName, group, buildID string) string {
	return hashSuffixedName(
		baseName+WorkerDeploymentNameSeparator+group+WorkerDeploymentNameSeparator+buildID,
		baseName+ResourceNameSeparator+group+ResourceNameSeparator+buildID,
	)
}

func HashString(s string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

func computeImagePrefix(s string, maxLen int) string {
	ref, err := reference.Parse(s)
	if err == nil {
		switch v := ref.(type) {
		case reference.Tagged: // (e.g., "docker.io/library/busybox:latest", "docker.io/library/busybox:latest@sha256:<digest>")
			s = v.Tag() // -> latest
		case reference.Digested: // (e.g., "docker.io@sha256:<digest>", "docker.io/library/busybo@sha256:<digest>")
			s = v.Digest().Hex() // -> <digest>
		case reference.Named: // (e.g., "docker.io/library/busybox")
			s = reference.Path(v) // -> library/busybox
		default:
		}
	}
	return TruncateString(s, maxLen)
}

// TruncateString truncates string to the first n characters.
// Pass n = -1 to skip truncation.
func TruncateString(s string, n int) string {
	if len(s) > n && n > 0 {
		s = s[:n]
	}
	return s
}

func CleanStringForDNS(s string) string {
	// Keep only letters, numbers, and dashes.
	re := regexp.MustCompile(`[^a-zA-Z0-9-]+`)
	// Lowercase to ensure RFC 1123 DNS label compliance for Kubernetes resource names.
	return strings.ToLower(re.ReplaceAllString(s, ResourceNameSeparator))
}

func cleanDeploymentNameForK8sLabelValue(s string) string {
	return strings.ReplaceAll(s, WorkerDeploymentNameSeparator, WorkerDeploymentNameSeparatorK8sLabelCompliant)
}

// Build ID is used as a label in k8s, and as the build ID for
// the worker in Temporal. That means it needs to conform to both
// system's requirements.
//
// https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set
// Valid label value:
// - must be 63 characters or less (can be empty),
// - unless empty, must begin and end with an alphanumeric character ([a-z0-9A-Z]),
// - could contain dashes (-), underscores (_), dots (.), and alphanumerics between.
//
// Temporal build IDs only need to be ASCII.
func cleanBuildID(s string) string {
	// Keep only letters, numbers, dashes, underscores, and dots.
	re := regexp.MustCompile(`[^a-zA-Z0-9-._]+`)
	s = re.ReplaceAllString(s, ResourceNameSeparator)
	// Trim leading/trailing separators to comply with K8s label requirements
	// (must begin and end with alphanumeric character)
	return strings.Trim(s, "-._")
}

// ComputeSelectorLabels returns the selector labels used by a versioned Deployment.
// These are the same labels set on the Deployment.Spec.Selector.MatchLabels.
func ComputeSelectorLabels(twdName, buildID string) map[string]string {
	return map[string]string{
		WorkerDeploymentNameLabel: TruncateString(CleanStringForDNS(twdName), 63),
		BuildIDLabel:              TruncateString(buildID, 63),
	}
}

// ComputeWorkerGroupSelectorLabels returns the selector labels of a group's Deployment in a
// multi-group version.
func ComputeWorkerGroupSelectorLabels(twdName, buildID, group string) map[string]string {
	labels := ComputeSelectorLabels(twdName, buildID)
	labels[WorkerGroupLabel] = group
	return labels
}

// NewDeploymentWithOwnerRef creates a new deployment resource, including owner references
func NewDeploymentWithOwnerRef(
	typeMeta *metav1.TypeMeta,
	objectMeta *metav1.ObjectMeta,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	workerDeploymentName string,
	buildID string,
	connection temporaliov1alpha1.ConnectionSpec,
) *appsv1.Deployment {
	d, _ := NewWorkerGroupDeploymentWithOwnerRef(typeMeta, objectMeta, spec, workerDeploymentName, buildID,
		temporaliov1alpha1.DefaultWorkerGroupName, connection) // a spec without groups always has the default group
	return d
}

// NewWorkerGroupDeploymentWithOwnerRef creates one worker group's deployment for a version. Groups
// carry the group label in their selector; the default group of a spec without groups doesn't.
func NewWorkerGroupDeploymentWithOwnerRef(
	typeMeta *metav1.TypeMeta,
	objectMeta *metav1.ObjectMeta,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	workerDeploymentName string,
	buildID string,
	group string,
	connection temporaliov1alpha1.ConnectionSpec,
) (*appsv1.Deployment, error) {
	depSpec, ok := spec.WorkerGroupDeploymentSpec(group)
	if !ok {
		return nil, fmt.Errorf("worker group %q is not in the WorkerDeployment spec", group)
	}
	name := ComputeVersionedDeploymentName(objectMeta.Name, buildID)
	selectorLabels := ComputeSelectorLabels(objectMeta.GetName(), buildID)
	if spec.HasWorkerGroups() {
		name = ComputeWorkerGroupDeploymentName(objectMeta.Name, group, buildID)
		selectorLabels = ComputeWorkerGroupSelectorLabels(objectMeta.GetName(), buildID, group)
	}

	depSpec.Selector = &metav1.LabelSelector{
		MatchLabels: selectorLabels,
	}

	// Set pod labels
	podLabels := make(map[string]string)
	for k, v := range depSpec.Template.Labels {
		podLabels[k] = v
	}
	for k, v := range selectorLabels {
		podLabels[k] = v
	}

	// Build pod annotations by merging any annotations set by the user in
	// spec.deployment.template.annotations with the constructed connection and
	// pod template spec hashes.
	podAnnotations := make(map[string]string)
	for k, v := range depSpec.Template.Annotations {
		podAnnotations[k] = v
	}
	podAnnotations[ConnectionSpecHashAnnotation] = ComputeConnectionSpecHash(connection)
	// Store hash of user-provided pod template spec BEFORE controller modifications
	// This enables drift detection when build ID is stable
	podAnnotations[PodTemplateSpecHashAnnotation] = ComputePodTemplateSpecHash(depSpec.Template)
	blockOwnerDeletion := true
	depSpec.Template.ObjectMeta = metav1.ObjectMeta{
		Labels:      podLabels,
		Annotations: podAnnotations,
	}

	// Apply controller-managed environment variables and volume mounts
	podSpec := depSpec.Template.Spec.DeepCopy()
	ApplyControllerPodSpecModifications(
		podSpec,
		connection,
		spec.WorkerOptions.TemporalNamespace,
		workerDeploymentName,
		buildID,
	)
	depSpec.Template.Spec = *podSpec

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:                       name,
			Namespace:                  objectMeta.Namespace,
			DeletionGracePeriodSeconds: nil,
			Labels:                     selectorLabels,
			Annotations:                depSpec.Template.Annotations,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         typeMeta.APIVersion,
				Kind:               typeMeta.Kind,
				Name:               objectMeta.Name,
				UID:                objectMeta.UID,
				BlockOwnerDeletion: &blockOwnerDeletion,
				Controller:         nil,
			}},
			// TODO(jlegrone): Add finalizer managed by the controller in order to prevent
			//                 deleting deployments that are still reachable.
		},
		Spec: depSpec,
	}, nil
}

// TODO (Shivam): Change hash when secret name is updated as well.
func ComputeConnectionSpecHash(connection temporaliov1alpha1.ConnectionSpec) string {
	// HostPort is required, but MutualTLSSecret can be empty for non-mTLS connections
	if connection.HostPort == "" {
		return ""
	}

	hasher := sha256.New()

	// Hash connection spec fields in deterministic order
	_, _ = hasher.Write([]byte(connection.HostPort))
	_, _ = hasher.Write([]byte(connection.TLSServerName()))
	_, _ = hasher.Write([]byte(connection.TLSCACertSecretName()))
	if connection.MutualTLSSecretRef != nil {
		_, _ = hasher.Write([]byte(connection.MutualTLSSecretRef.Name))
	} else if connection.APIKeySecretRef != nil {
		_, _ = hasher.Write([]byte(connection.APIKeySecretRef.Name))
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

// ComputePodTemplateSpecHash computes a SHA256 hash of the user-provided pod template spec.
// This hash is used to detect drift when the build ID is stable but the pod spec has changed.
// JSON marshaling is used so that new zero-value fields added in future k8s API versions
// (which carry omitempty) are excluded, keeping hashes stable across k8s upgrades.
func ComputePodTemplateSpecHash(template corev1.PodTemplateSpec) string {
	hasher := sha256.New()
	data, _ := json.Marshal(template) // never errors for corev1.PodTemplateSpec
	_, _ = hasher.Write(data)
	return hex.EncodeToString(hasher.Sum(nil))
}

// ApplyControllerPodSpecModifications applies controller-managed environment variables and
// volume mounts to a pod spec. This is used both when creating new deployments and when
// updating existing deployments for drift detection.
func ApplyControllerPodSpecModifications(
	podSpec *corev1.PodSpec,
	connection temporaliov1alpha1.ConnectionSpec,
	temporalNamespace string,
	workerDeploymentName string,
	buildID string,
) {
	// Add environment variables to containers
	for i, container := range podSpec.Containers {
		container.Env = append(container.Env,
			corev1.EnvVar{
				Name:  EnvTemporalAddress,
				Value: connection.HostPort,
			},
			corev1.EnvVar{
				Name:  EnvTemporalNamespace,
				Value: temporalNamespace,
			},
			corev1.EnvVar{
				Name:  EnvTemporalDeploymentName,
				Value: workerDeploymentName,
			},
			corev1.EnvVar{
				Name:  EnvTemporalWorkerBuildID,
				Value: buildID,
			},
		)
		podSpec.Containers[i] = container
	}

	if tlsServerName := connection.TLSServerName(); tlsServerName != "" {
		for i, container := range podSpec.Containers {
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  EnvTemporalTLSServerName,
				Value: tlsServerName,
			})
			podSpec.Containers[i] = container
		}
	}

	// Add TLS config if mTLS is enabled
	if connection.MutualTLSSecretRef != nil {
		for i, container := range podSpec.Containers {
			container.Env = append(container.Env,
				corev1.EnvVar{
					Name:  EnvTemporalTLS,
					Value: "true",
				},
				corev1.EnvVar{
					Name:  EnvTemporalTLSClientKeyPath,
					Value: TemporalTLSClientKeyPath,
				},
				corev1.EnvVar{
					Name:  EnvTemporalTLSClientCertPath,
					Value: TemporalTLSClientCertPath,
				},
			)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      TemporalTLSVolumeName,
				MountPath: TemporalTLSMountPath,
			})
			podSpec.Containers[i] = container
		}
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: TemporalTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: connection.MutualTLSSecretRef.Name,
				},
			},
		})
	} else if connection.APIKeySecretRef != nil {
		for i, container := range podSpec.Containers {
			container.Env = append(container.Env,
				corev1.EnvVar{
					Name: EnvTemporalAPIKey,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: connection.APIKeySecretRef,
					},
				},
			)
			podSpec.Containers[i] = container
		}
	}

	// Trust a private CA for API-key or no-credentials auth. Mutually exclusive with
	// MutualTLSSecretRef (enforced by ConnectionSpec's CEL validation) -- mTLS bundles its
	// own CA into that secret's ca.crt key instead, see ConnectionTLSConfig.CACertSecretRef.
	if caCertSecretName := connection.TLSCACertSecretName(); caCertSecretName != "" {
		for i, container := range podSpec.Containers {
			container.Env = append(container.Env,
				corev1.EnvVar{
					Name:  EnvTemporalTLS,
					Value: "true",
				},
				corev1.EnvVar{
					Name:  EnvTemporalTLSServerCACertPath,
					Value: TemporalTLSCACertPath,
				},
			)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      TemporalTLSCAVolumeName,
				MountPath: TemporalTLSCAMountPath,
			})
			podSpec.Containers[i] = container
		}
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: TemporalTLSCAVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: caCertSecretName,
				},
			},
		})
	}
}

// EnsureTLSVolume adds the mTLS client cert/key secret volume or updates its secret name if
// present.
func EnsureTLSVolume(volumes []corev1.Volume, secretName string) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == TemporalTLSVolumeName {
			volumes[i].VolumeSource = corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: secretName},
			}
			return volumes
		}
	}
	return append(volumes, corev1.Volume{
		Name:         TemporalTLSVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName}},
	})
}

// RemoveTLSVolume removes the mTLS client cert/key secret volume if present.
func RemoveTLSVolume(volumes []corev1.Volume) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == TemporalTLSVolumeName {
			return slices.Delete(volumes, i, i+1)
		}
	}
	return volumes
}

// EnsureTLSVolumeMount adds the mTLS client cert/key mount to a container, or fixes its path
// if present.
func EnsureTLSVolumeMount(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == TemporalTLSVolumeName {
			mounts[i].MountPath = TemporalTLSMountPath
			return mounts
		}
	}
	return append(mounts, corev1.VolumeMount{Name: TemporalTLSVolumeName, MountPath: TemporalTLSMountPath})
}

// RemoveTLSVolumeMount removes the mTLS client cert/key mount from a container if present.
func RemoveTLSVolumeMount(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == TemporalTLSVolumeName {
			return slices.Delete(mounts, i, i+1)
		}
	}
	return mounts
}

// EnsureTLSCAVolume adds the private-CA secret volume or updates its secret name if present.
func EnsureTLSCAVolume(volumes []corev1.Volume, secretName string) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == TemporalTLSCAVolumeName {
			volumes[i].VolumeSource = corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: secretName},
			}
			return volumes
		}
	}
	return append(volumes, corev1.Volume{
		Name:         TemporalTLSCAVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secretName}},
	})
}

// RemoveTLSCAVolume removes the private-CA secret volume if present.
func RemoveTLSCAVolume(volumes []corev1.Volume) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == TemporalTLSCAVolumeName {
			return slices.Delete(volumes, i, i+1)
		}
	}
	return volumes
}

// EnsureTLSCAVolumeMount adds the private-CA mount to a container, or fixes its path if
// present.
func EnsureTLSCAVolumeMount(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == TemporalTLSCAVolumeName {
			mounts[i].MountPath = TemporalTLSCAMountPath
			return mounts
		}
	}
	return append(mounts, corev1.VolumeMount{Name: TemporalTLSCAVolumeName, MountPath: TemporalTLSCAMountPath})
}

// RemoveTLSCAVolumeMount removes the private-CA mount from a container if present.
func RemoveTLSCAVolumeMount(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == TemporalTLSCAVolumeName {
			return slices.Delete(mounts, i, i+1)
		}
	}
	return mounts
}

// NewWorkerGroupDeploymentWithControllerRef creates one worker group's deployment for a version,
// with the WorkerDeployment as its controller.
func NewWorkerGroupDeploymentWithControllerRef(
	w *temporaliov1alpha1.WorkerDeployment,
	buildID string,
	group string,
	connection temporaliov1alpha1.ConnectionSpec,
	reconcilerScheme *runtime.Scheme,
) (*appsv1.Deployment, error) {
	d, err := NewWorkerGroupDeploymentWithOwnerRef(
		&w.TypeMeta,
		&w.ObjectMeta,
		&w.Spec,
		ComputeWorkerDeploymentName(w),
		buildID,
		group,
		connection,
	)
	if err != nil {
		return nil, err
	}
	if err := ctrl.SetControllerReference(w, d, reconcilerScheme); err != nil {
		return nil, err
	}
	return d, nil
}
