// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

// Grouped worker builds save their complete settings before any pods are created.
// The controller reads these records to restore historical members without using
// the newest WorkerDeployment's pod templates or replica counts.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type (
	// GroupRevision contains the saved inputs for one grouped worker build.
	GroupRevision struct {
		Object *appsv1.ControllerRevision
		Spec   v1alpha1.WorkerDeploymentSpec
	}
)

const (
	// MemberLabel distinguishes separate Kubernetes Deployments within one build.
	MemberLabel = "temporal.io/member"
	// PrimaryMember identifies the Deployment configured by the existing fields.
	PrimaryMember = "primary"
	// GroupDeletionAnnotation records that a drained build has entered cleanup.
	// Its saved templates remain available until every member object is gone.
	GroupDeletionAnnotation = "temporal.io/deletion-requested"
)

// NewGroupRevision saves the complete requested build settings in a new record.
// Callers must create this record successfully before creating its Deployments.
func NewGroupRevision(
	w *v1alpha1.WorkerDeployment,
	buildID string,
	scheme *runtime.Scheme,
) (*GroupRevision, error) {
	data, err := json.Marshal(w.Spec)
	if err != nil {
		return nil, fmt.Errorf("encode grouped build %q: %w", buildID, err)
	}
	object := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ComputeVersionedDeploymentName(w.Name+"-group", buildID),
			Namespace: w.Namespace,
			Labels:    ComputeSelectorLabels(w.Name, buildID),
		},
		Data:     runtime.RawExtension{Raw: data},
		Revision: w.Generation,
	}
	if err := ctrl.SetControllerReference(w, object, scheme); err != nil {
		return nil, err
	}
	return &GroupRevision{Object: object, Spec: *w.Spec.DeepCopy()}, nil
}

// GetGroupRevisions reads the build settings protected by Kubernetes Data immutability.
// The controller changes cleanup metadata but never changes the saved Data.
func GetGroupRevisions(
	ctx context.Context,
	c client.Client,
	namespace, ownerName string,
	ownerUID types.UID,
) (map[string]*GroupRevision, error) {
	var objects appsv1.ControllerRevisionList
	if err := c.List(ctx, &objects, client.InNamespace(namespace), client.MatchingLabels{WorkerDeploymentNameLabel: TruncateString(CleanStringForDNS(ownerName), 63)}); err != nil {
		return nil, fmt.Errorf("list grouped builds: %w", err)
	}
	result := make(map[string]*GroupRevision)
	for i := range objects.Items {
		object := &objects.Items[i]
		owner := metav1.GetControllerOf(object)
		if owner == nil || owner.Name != ownerName || owner.Kind != "WorkerDeployment" || owner.APIVersion != v1alpha1.GroupVersion.String() || owner.UID != ownerUID {
			continue
		}
		buildID := object.Labels[BuildIDLabel]
		if buildID == "" {
			return nil, fmt.Errorf("saved grouped build %q has no build identity", object.Name)
		}
		var spec v1alpha1.WorkerDeploymentSpec
		if err := json.Unmarshal(object.Data.Raw, &spec); err != nil {
			return nil, fmt.Errorf("decode saved grouped build %q: %w", object.Name, err)
		}
		if err := validateSavedGroup(ctx, &spec); err != nil {
			return nil, fmt.Errorf("invalid saved grouped build %q: %w", object.Name, err)
		}
		if _, duplicate := result[buildID]; duplicate {
			return nil, fmt.Errorf("multiple saved records for grouped build %q", buildID)
		}
		result[buildID] = &GroupRevision{Object: object, Spec: spec}
	}
	return result, nil
}

// Deployments constructs each member from the saved build, preserving independent
// service accounts, secret references, resource requests, and replica counts.
func (g *GroupRevision) Deployments(
	w *v1alpha1.WorkerDeployment,
	buildID string,
	connection v1alpha1.ConnectionSpec,
	scheme *runtime.Scheme,
) (map[string]*appsv1.Deployment, error) {
	result := make(map[string]*appsv1.Deployment)
	members := append([]v1alpha1.AdditionalDeployment{{Name: PrimaryMember}}, g.Spec.AdditionalDeployments...)
	for _, member := range members {
		copy := w.DeepCopy()
		copy.Spec = *g.Spec.DeepCopy()
		if member.Name != PrimaryMember {
			deploymentSpec := copy.Spec.DeploymentSpec()
			deploymentSpec.Template = *member.Template.DeepCopy()
			deploymentSpec.Replicas = &member.Replicas
			copy.Spec.Deployment = &deploymentSpec
		}
		deployment, err := NewDeploymentWithControllerRef(copy, buildID, connection, scheme)
		if err != nil {
			return nil, err
		}
		deployment.Name = ComputeVersionedDeploymentName(w.Name+"-"+member.Name, buildID)
		deployment.Labels[MemberLabel] = member.Name
		deployment.Spec.Selector.MatchLabels[MemberLabel] = member.Name
		deployment.Spec.Template.Labels[MemberLabel] = member.Name
		result[member.Name] = deployment
	}
	return result, nil
}

// validateSavedGroup checks the JSON loaded from ControllerRevision Data before
// restoring pods. Unlike WorkerDeployment fields, this JSON is not checked by the
// custom resource schema, so missing counts or duplicate members must fail here.
func validateSavedGroup(ctx context.Context, spec *v1alpha1.WorkerDeploymentSpec) error {
	worker := &v1alpha1.WorkerDeployment{Spec: *spec}
	if _, err := worker.ValidateCreate(ctx, worker); err != nil {
		return err
	}
	if len(spec.AdditionalDeployments) == 0 || spec.SunsetStrategy.ScaledownDelay == nil || spec.SunsetStrategy.DeleteDelay == nil {
		return fmt.Errorf("group members and sunset delays are required")
	}
	members := map[string]bool{PrimaryMember: true}
	for _, member := range spec.AdditionalDeployments {
		if members[member.Name] || len(validation.IsDNS1123Label(member.Name)) > 0 || member.Replicas < 1 {
			return fmt.Errorf("member %q needs a unique valid name and a positive replica count", member.Name)
		}
		members[member.Name] = true
	}
	queues := make(map[string]bool)
	for _, queue := range spec.ExpectedTaskQueues {
		if queue.Name == "" || queues[queue.Name] || len(queue.Kinds) == 0 {
			return fmt.Errorf("expected queues need unique nonempty names and task kinds")
		}
		queues[queue.Name] = true
		kinds := make(map[v1alpha1.TaskQueueKind]bool)
		for _, kind := range queue.Kinds {
			if kinds[kind] || (kind != v1alpha1.TaskQueueWorkflow && kind != v1alpha1.TaskQueueActivity) {
				return fmt.Errorf("queue %q has an invalid or repeated task kind", queue.Name)
			}
			kinds[kind] = true
		}
	}
	return nil
}
