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
	// PoolLabel names the worker pool of a Deployment in a version that uses pools.
	// Deployments without it belong to a WorkerDeployment without pools.
	PoolLabel = "temporal.io/worker-pool"
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
	poolsBuildIDHashLen                            = 10
)

// DeploymentState represents the Kubernetes state of all deployments for a temporal worker deployment
type DeploymentState struct {
	// Map of buildID to the default pool's deployment. Use VersionDeployments to
	// decide whether a version has any deployment.
	Deployments map[string]*appsv1.Deployment
	// Sorted deployments by creation time
	DeploymentsByTime []*appsv1.Deployment
	// Map of buildID to the default pool's deployment reference
	DeploymentRefs map[string]*corev1.ObjectReference
	// Map of buildID to pool name to deployment, for every pool including the default
	PoolDeployments map[string]map[string]*appsv1.Deployment
}

// VersionDeployments returns every pool's deployment for a build ID, keyed by pool name.
func (s *DeploymentState) VersionDeployments(buildID string) map[string]*appsv1.Deployment {
	if s.PoolDeployments != nil {
		return s.PoolDeployments[buildID]
	}
	// States built without PoolDeployments (e.g. in tests) only have default pools.
	if d, ok := s.Deployments[buildID]; ok {
		return map[string]*appsv1.Deployment{temporaliov1alpha1.DefaultPoolName: d}
	}
	return nil
}

// VersionDeploymentList returns every pool's deployment for a build ID: named
// pools sorted by name, then the default pool.
func (s *DeploymentState) VersionDeploymentList(buildID string) []*appsv1.Deployment {
	pools := s.VersionDeployments(buildID)
	names := make([]string, 0, len(pools))
	for name := range pools {
		if name != temporaliov1alpha1.DefaultPoolName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	list := make([]*appsv1.Deployment, 0, len(pools))
	for _, name := range names {
		list = append(list, pools[name])
	}
	if d, ok := pools[temporaliov1alpha1.DefaultPoolName]; ok {
		list = append(list, d)
	}
	return list
}

// BuildIDs returns the sorted build IDs that have at least one deployment.
func (s *DeploymentState) BuildIDs() []string {
	seen := make(map[string]struct{}, len(s.Deployments)+len(s.PoolDeployments))
	for buildID := range s.Deployments {
		seen[buildID] = struct{}{}
	}
	for buildID := range s.PoolDeployments {
		seen[buildID] = struct{}{}
	}
	buildIDs := make([]string, 0, len(seen))
	for buildID := range seen {
		buildIDs = append(buildIDs, buildID)
	}
	sort.Strings(buildIDs)
	return buildIDs
}

// HasPoolLabel reports whether any of a version's deployments carries the pool label,
// which marks a multi-pool version.
func HasPoolLabel(deployments map[string]*appsv1.Deployment) bool {
	for _, d := range deployments {
		if _, ok := d.Labels[PoolLabel]; ok {
			return true
		}
	}
	return false
}

// PoolName returns the worker pool a deployment belongs to.
func PoolName(d *appsv1.Deployment) string {
	if pool := d.GetLabels()[PoolLabel]; pool != "" {
		return pool
	}
	return temporaliov1alpha1.DefaultPoolName
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

// NewDeploymentState indexes deployments by build ID and pool, keeping their order.
// Deployments without the build ID label are ignored.
func NewDeploymentState(deploys ...*appsv1.Deployment) *DeploymentState {
	state := &DeploymentState{
		Deployments:       make(map[string]*appsv1.Deployment),
		DeploymentsByTime: []*appsv1.Deployment{},
		DeploymentRefs:    make(map[string]*corev1.ObjectReference),
		PoolDeployments:   make(map[string]map[string]*appsv1.Deployment),
	}
	for _, deploy := range deploys {
		buildID, ok := deploy.GetLabels()[BuildIDLabel]
		if !ok {
			continue
		}
		pool := PoolName(deploy)
		if state.PoolDeployments[buildID] == nil {
			state.PoolDeployments[buildID] = make(map[string]*appsv1.Deployment)
		}
		state.PoolDeployments[buildID][pool] = deploy
		state.DeploymentsByTime = append(state.DeploymentsByTime, deploy)
		if pool == temporaliov1alpha1.DefaultPoolName {
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

	if w.Spec.HasPools() {
		return computePoolsBuildID(w.Spec)
	}
	depSpec := w.Spec.DeploymentSpec()

	if img := firstImage(depSpec.Template); img != "" {
		return imagePrefixedBuildID(img, utils.ComputeHash(&depSpec.Template, nil, true))
	}
	return utils.ComputeHash(&depSpec.Template, nil, false)
}

// computePoolsBuildID hashes every pool's name and pod template, so a change to any
// pool starts one new version for all of them. Pools are sorted by name, so their order
// in the spec doesn't matter.
func computePoolsBuildID(spec temporaliov1alpha1.WorkerDeploymentSpec) string {
	type poolTemplate struct {
		Name     string                 `json:"name"`
		Template corev1.PodTemplateSpec `json:"template"`
	}
	sorted := slices.Clone(spec.Pools)
	slices.SortFunc(sorted, func(a, b temporaliov1alpha1.WorkerPool) int { return strings.Compare(a.Name, b.Name) })
	pools := make([]poolTemplate, 0, len(sorted))
	for _, p := range sorted {
		pools = append(pools, poolTemplate{Name: p.Name, Template: p.Deployment.Template})
	}
	data, _ := json.Marshal(pools) // never errors for these types
	hash := HashString(string(data))[:poolsBuildIDHashLen]

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

// ComputePoolDeploymentName names a named pool's versioned Deployment. The hash of the
// full triple keeps it from ever taking another pool's or a default pool's name.
func ComputePoolDeploymentName(baseName, pool, buildID string) string {
	return hashSuffixedName(
		baseName+WorkerDeploymentNameSeparator+pool+WorkerDeploymentNameSeparator+buildID,
		baseName+ResourceNameSeparator+pool+ResourceNameSeparator+buildID,
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

// ComputePoolSelectorLabels returns the selector labels of a pool's Deployment in a
// multi-pool version.
func ComputePoolSelectorLabels(twdName, buildID, pool string) map[string]string {
	labels := ComputeSelectorLabels(twdName, buildID)
	labels[PoolLabel] = pool
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
	d, _ := NewPoolDeploymentWithOwnerRef(typeMeta, objectMeta, spec, workerDeploymentName, buildID,
		temporaliov1alpha1.DefaultPoolName, connection) // a spec without pools always has the default pool
	return d
}

// NewPoolDeploymentWithOwnerRef creates one worker pool's deployment for a version. Pools
// carry the pool label in their selector; the default pool of a spec without pools doesn't.
func NewPoolDeploymentWithOwnerRef(
	typeMeta *metav1.TypeMeta,
	objectMeta *metav1.ObjectMeta,
	spec *temporaliov1alpha1.WorkerDeploymentSpec,
	workerDeploymentName string,
	buildID string,
	pool string,
	connection temporaliov1alpha1.ConnectionSpec,
) (*appsv1.Deployment, error) {
	depSpec, ok := spec.PoolDeploymentSpec(pool)
	if !ok {
		return nil, fmt.Errorf("worker pool %q is not in the WorkerDeployment spec", pool)
	}
	name := ComputeVersionedDeploymentName(objectMeta.Name, buildID)
	selectorLabels := ComputeSelectorLabels(objectMeta.GetName(), buildID)
	if spec.HasPools() {
		name = ComputePoolDeploymentName(objectMeta.Name, pool, buildID)
		selectorLabels = ComputePoolSelectorLabels(objectMeta.GetName(), buildID, pool)
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
				Name:  "TEMPORAL_ADDRESS",
				Value: connection.HostPort,
			},
			corev1.EnvVar{
				Name:  "TEMPORAL_NAMESPACE",
				Value: temporalNamespace,
			},
			corev1.EnvVar{
				Name:  "TEMPORAL_DEPLOYMENT_NAME",
				Value: workerDeploymentName,
			},
			corev1.EnvVar{
				Name:  "TEMPORAL_WORKER_BUILD_ID",
				Value: buildID,
			},
		)
		podSpec.Containers[i] = container
	}

	if tlsServerName := connection.TLSServerName(); tlsServerName != "" {
		for i, container := range podSpec.Containers {
			container.Env = append(container.Env, corev1.EnvVar{
				Name:  "TEMPORAL_TLS_SERVER_NAME",
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
					Name:  "TEMPORAL_TLS",
					Value: "true",
				},
				corev1.EnvVar{
					Name:  "TEMPORAL_TLS_CLIENT_KEY_PATH",
					Value: "/etc/temporal/tls/tls.key",
				},
				corev1.EnvVar{
					Name:  "TEMPORAL_TLS_CLIENT_CERT_PATH",
					Value: "/etc/temporal/tls/tls.crt",
				},
			)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "temporal-tls",
				MountPath: "/etc/temporal/tls",
			})
			podSpec.Containers[i] = container
		}
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "temporal-tls",
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
					Name: "TEMPORAL_API_KEY",
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
					Name:  "TEMPORAL_TLS",
					Value: "true",
				},
				corev1.EnvVar{
					Name:  "TEMPORAL_TLS_SERVER_CA_CERT_PATH",
					Value: "/etc/temporal/tls-ca/ca.crt",
				},
			)
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "temporal-tls-ca",
				MountPath: "/etc/temporal/tls-ca",
			})
			podSpec.Containers[i] = container
		}
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "temporal-tls-ca",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: caCertSecretName,
				},
			},
		})
	}
}

// NewPoolDeploymentWithControllerRef creates one worker pool's deployment for a version,
// with the WorkerDeployment as its controller.
func NewPoolDeploymentWithControllerRef(
	w *temporaliov1alpha1.WorkerDeployment,
	buildID string,
	pool string,
	connection temporaliov1alpha1.ConnectionSpec,
	reconcilerScheme *runtime.Scheme,
) (*appsv1.Deployment, error) {
	d, err := NewPoolDeploymentWithOwnerRef(
		&w.TypeMeta,
		&w.ObjectMeta,
		&w.Spec,
		ComputeWorkerDeploymentName(w),
		buildID,
		pool,
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
