// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"

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
	BuildIDLabel                 = "temporal.io/build-id"
	DeploymentNameSeparator      = "/" // TODO(carlydf): change this to "." once the server accepts `.` in deployment names
	VersionIDSeparator           = "." // TODO(carlydf): change this to ":"
	K8sResourceNameSeparator     = "-"
	MaxBuildIdLen                = 63
	ConnectionSpecHashAnnotation = "temporal.io/connection-spec-hash"
)

// DeploymentState represents the Kubernetes state of all deployments for a temporal worker deployment
type DeploymentState struct {
	// Map of buildID to deployment
	Deployments map[string]*appsv1.Deployment
	// Sorted deployments by creation time
	DeploymentsByTime []*appsv1.Deployment
	// Map of buildID to deployment references
	DeploymentRefs map[string]*corev1.ObjectReference
}

// GetDeploymentState queries Kubernetes to get the state of all deployments
// associated with a TemporalWorkerDeployment
func GetDeploymentState(
	ctx context.Context,
	k8sClient client.Client,
	namespace string,
	ownerName string,
	workerDeploymentName string,
) (*DeploymentState, error) {
	state := &DeploymentState{
		Deployments:       make(map[string]*appsv1.Deployment),
		DeploymentsByTime: []*appsv1.Deployment{},
		DeploymentRefs:    make(map[string]*corev1.ObjectReference),
	}

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

	// Track each k8s deployment by build ID
	for i := range childDeploys.Items {
		deploy := &childDeploys.Items[i]
		if buildID, ok := deploy.GetLabels()[BuildIDLabel]; ok {
			state.Deployments[buildID] = deploy
			state.DeploymentsByTime = append(state.DeploymentsByTime, deploy)
			state.DeploymentRefs[buildID] = NewObjectRef(deploy)
		}
		// Any deployments without the build ID label are ignored
	}

	return state, nil
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

// ComputeVersionID generates a version ID from the worker deployment spec
func ComputeVersionID(w *temporaliov1alpha1.TemporalWorkerDeployment) string {
	return ComputeWorkerDeploymentName(w) + VersionIDSeparator + ComputeBuildID(w)
}

func ComputeBuildID(w *temporaliov1alpha1.TemporalWorkerDeployment) string {
	if containers := w.Spec.Template.Spec.Containers; len(containers) > 0 {
		if img := containers[0].Image; img != "" {
			shortHashSuffix := K8sResourceNameSeparator + utils.ComputeHash(&w.Spec.Template, nil, true)
			maxImgLen := MaxBuildIdLen - len(shortHashSuffix)
			imagePrefix := computeImagePrefix(img, maxImgLen)
			return imagePrefix + shortHashSuffix
		}
	}
	return utils.ComputeHash(&w.Spec.Template, nil, false)
}

// ComputeWorkerDeploymentName generates the base worker deployment name
func ComputeWorkerDeploymentName(w *temporaliov1alpha1.TemporalWorkerDeployment) string {
	// Use the name and namespace to form the worker deployment name
	return w.GetName() + DeploymentNameSeparator + w.GetNamespace()
}

// ComputeVersionedDeploymentName generates a name for a versioned deployment
func ComputeVersionedDeploymentName(baseName, buildID string) string {
	return baseName + "-" + buildID
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
	return CleanAndTruncateString(s, maxLen)
}

// CleanAndTruncateString truncates string to the first n characters, and then replaces characters that can't be in a
// kubernetes resource name with a `-` character which can be.
// Pass n = -1 to skip truncation.
func CleanAndTruncateString(s string, n int) string {
	if len(s) > n && n > 0 {
		s = s[:n]
	}
	// Keep only letters, numbers, and dashes
	re := regexp.MustCompile(`[^a-zA-Z0-9-]+`)
	return re.ReplaceAllString(s, K8sResourceNameSeparator)
}

// NewDeploymentWithOwnerRef creates a new deployment resource, including owner references
func NewDeploymentWithOwnerRef(
	typeMeta *metav1.TypeMeta,
	objectMeta *metav1.ObjectMeta,
	spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec,
	workerDeploymentName string,
	buildID string,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) *appsv1.Deployment {
	selectorLabels := map[string]string{}
	// Merge labels from TemporalWorker with build ID
	if spec.Selector != nil {
		for k, v := range spec.Selector.MatchLabels {
			selectorLabels[k] = v
		}
	}
	selectorLabels[BuildIDLabel] = buildID

	// Set pod labels
	podLabels := make(map[string]string)
	for k, v := range spec.Template.Labels {
		podLabels[k] = v
	}
	for k, v := range selectorLabels {
		podLabels[k] = v
	}

	podSpec := spec.Template.Spec.DeepCopy()

	// Add environment variables to containers
	for i, container := range podSpec.Containers {
		container.Env = append(container.Env,
			corev1.EnvVar{
				Name:  "TEMPORAL_HOST_PORT",
				Value: connection.HostPort,
			},
			corev1.EnvVar{
				Name:  "TEMPORAL_NAMESPACE",
				Value: spec.WorkerOptions.TemporalNamespace,
			},
			corev1.EnvVar{
				Name:  "TEMPORAL_DEPLOYMENT_NAME",
				Value: workerDeploymentName,
			},
			corev1.EnvVar{
				Name:  "WORKER_BUILD_ID",
				Value: buildID,
			},
		)
		podSpec.Containers[i] = container
	}

	// Add TLS config if mTLS is enabled
	if connection.MutualTLSSecret != "" {
		for i, container := range podSpec.Containers {
			container.Env = append(container.Env,
				corev1.EnvVar{
					Name:  "TEMPORAL_TLS_KEY_PATH",
					Value: "/etc/temporal/tls/tls.key",
				},
				corev1.EnvVar{
					Name:  "TEMPORAL_TLS_CERT_PATH",
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
					SecretName: connection.MutualTLSSecret,
				},
			},
		})
	}

	// Build pod annotations
	podAnnotations := make(map[string]string)
	for k, v := range spec.Template.Annotations {
		podAnnotations[k] = v
	}
	podAnnotations[ConnectionSpecHashAnnotation] = ComputeConnectionSpecHash(connection)
	blockOwnerDeletion := true

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:                       ComputeVersionedDeploymentName(objectMeta.Name, buildID),
			Namespace:                  objectMeta.Namespace,
			DeletionGracePeriodSeconds: nil,
			Labels:                     selectorLabels,
			Annotations:                spec.Template.Annotations,
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
		Spec: appsv1.DeploymentSpec{
			Replicas: spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: podAnnotations,
				},
				Spec: *podSpec,
			},
			MinReadySeconds: spec.MinReadySeconds,
		},
	}
}

func ComputeConnectionSpecHash(connection temporaliov1alpha1.TemporalConnectionSpec) string {
	// HostPort is required, but MutualTLSSecret can be empty for non-mTLS connections
	if connection.HostPort == "" {
		return ""
	}

	hasher := sha256.New()

	// Hash connection spec fields in deterministic order
	_, _ = hasher.Write([]byte(connection.HostPort))
	_, _ = hasher.Write([]byte(connection.MutualTLSSecret))

	return hex.EncodeToString(hasher.Sum(nil))
}

func NewDeploymentWithControllerRef(
	w *temporaliov1alpha1.TemporalWorkerDeployment,
	buildID string,
	connection temporaliov1alpha1.TemporalConnectionSpec,
	reconcilerScheme *runtime.Scheme,
) (*appsv1.Deployment, error) {
	d := NewDeploymentWithOwnerRef(
		&w.TypeMeta,
		&w.ObjectMeta,
		&w.Spec,
		ComputeWorkerDeploymentName(w),
		buildID,
		connection,
	)
	if err := ctrl.SetControllerReference(w, d, reconcilerScheme); err != nil {
		return nil, err
	}
	return d, nil
}
