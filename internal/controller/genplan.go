// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

type plan struct {
	// Where to take actions

	TemporalNamespace string
	TaskQueue         string

	// Which actions to take

	DeleteDeployments []*appsv1.Deployment
	CreateDeployment  *appsv1.Deployment
	ScaleDeployments  map[*v1.ObjectReference]uint32
	// Register a new build ID as the default or with ramp
	UpdateVersionConfig *versionConfig
}

type versionConfig struct {
	// Token to use for conflict detection
	conflictToken []byte
	// build ID for which this config applies
	buildID string

	// One of rampPercentage OR setDefault must be set to a non-zero value.

	// Set this as the default build ID for all new executions
	setDefault bool
	// Acceptable values [0,100]
	rampPercentage uint8
}

func (r *TemporalWorkerReconciler) generatePlan(
	ctx context.Context,
	w *temporaliov1alpha1.TemporalWorker,
	rules *workflowservice.GetWorkerVersioningRulesResponse,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) (*plan, error) {
	plan := plan{
		TemporalNamespace: w.Spec.WorkerOptions.TemporalNamespace,
		TaskQueue:         w.Spec.WorkerOptions.TaskQueue,
		ScaleDeployments:  make(map[*v1.ObjectReference]uint32),
	}

	// Scale the active deployment if it doesn't match desired replicas
	if w.Status.DefaultVersion != nil && w.Status.DefaultVersion.Deployment != nil {
		defaultDeployment := w.Status.DefaultVersion.Deployment
		d, err := r.getDeployment(ctx, defaultDeployment)
		if err != nil {
			return nil, err
		}
		if d.Spec.Replicas != nil && *d.Spec.Replicas != *w.Spec.Replicas {
			plan.ScaleDeployments[defaultDeployment] = uint32(*w.Spec.Replicas)
		}
	}

	// TODO(jlegrone): generate warnings/events on the TemporalWorker resource when buildIDs are reachable
	//                 but have no corresponding Deployment.

	// Scale or delete deployments based on reachability
	for _, versionSet := range w.Status.DeprecatedVersions {
		if versionSet.Deployment == nil {
			// There's nothing we can do if the deployment was already deleted out of band.
			continue
		}

		d, err := r.getDeployment(ctx, versionSet.Deployment)
		if err != nil {
			return nil, err
		}

		switch versionSet.Reachability {
		case temporaliov1alpha1.ReachabilityStatusReachable:
			// Scale up reachable deployments
			if d.Spec.Replicas != nil && *d.Spec.Replicas != *w.Spec.Replicas {
				plan.ScaleDeployments[versionSet.Deployment] = uint32(*w.Spec.Replicas)
			}
		case temporaliov1alpha1.ReachabilityStatusUnreachable:
			// Scale down unreachable deployments. We do this instead
			// of deleting them so that they can be scaled back up if
			// their build ID is promoted to default again (i.e. during
			// a rollback).
			if d.Spec.Replicas != nil && *d.Spec.Replicas != 0 {
				plan.ScaleDeployments[versionSet.Deployment] = 0
			}
		case temporaliov1alpha1.ReachabilityStatusClosedOnly:
			// TODO(jlegrone): Compute scale based on load? Or percentage of replicas?
			// Scale down queryable deployments
			const closedOnlyReplicas = 0
			if d.Spec.Replicas != nil && *d.Spec.Replicas != closedOnlyReplicas {
				plan.ScaleDeployments[versionSet.Deployment] = closedOnlyReplicas
			}
		case temporaliov1alpha1.ReachabilityStatusNotRegistered:
			// Delete unregistered deployments
			plan.DeleteDeployments = append(plan.DeleteDeployments, d)
		}
	}

	desiredBuildID := computeBuildID(&w.Spec)

	if targetVersion := w.Status.TargetVersion; targetVersion != nil {
		if targetVersion.Deployment == nil {
			// Create new deployment from current pod template when it doesn't exist
			d, err := r.newDeployment(w, desiredBuildID, connection)
			if err != nil {
				return nil, err
			}
			existing, _ := r.getDeployment(ctx, newObjectRef(d))
			if existing == nil {
				plan.CreateDeployment = d
			}
		} else {
			d, err := r.getDeployment(ctx, targetVersion.Deployment)
			if err != nil {
				return nil, err
			}

			if targetVersion.BuildID != desiredBuildID {
				// Delete the latest (unregistered) deployment if the desired build ID has changed
				plan.DeleteDeployments = append(plan.DeleteDeployments, d)
			} else {
				// Scale the existing deployment and update versioning config

				// Scale deployment if necessary
				if d.Spec.Replicas == nil || (d.Spec.Replicas != nil && *d.Spec.Replicas != *w.Spec.Replicas) {
					plan.ScaleDeployments[newObjectRef(d)] = uint32(*w.Spec.Replicas)
				}

				// Update version configuration
				// TODO(jlegrone): What to do about ramp values for existing versions? Right now they are not removed.
				plan.UpdateVersionConfig = getVersionConfigDiff(rules, w.Spec.RolloutStrategy, &w.Status)
				if plan.UpdateVersionConfig != nil {
					plan.UpdateVersionConfig.conflictToken = w.Status.VersionConflictToken
				}
			}
		}
	}

	return &plan, nil
}

func getOldestBuildIDCreateTime(rules *workflowservice.GetWorkerVersioningRulesResponse, buildID string) *timestamppb.Timestamp {
	var rule *taskqueue.TimestampedBuildIdAssignmentRule
	for _, r := range rules.GetAssignmentRules() {
		if r.GetRule().GetTargetBuildId() != buildID {
			break
		}
		rule = r
	}
	return rule.GetCreateTime()
}

func getVersionConfigDiff(rules *workflowservice.GetWorkerVersioningRulesResponse, strategy temporaliov1alpha1.RolloutStrategy, status *temporaliov1alpha1.TemporalWorkerStatus) *versionConfig {
	vcfg := getVersionConfig(strategy, status, getOldestBuildIDCreateTime(rules, status.TargetVersion.BuildID))
	if vcfg == nil {
		return nil
	}
	vcfg.buildID = status.TargetVersion.BuildID

	// Set default version if no assignment rules exist
	if len(rules.GetAssignmentRules()) == 0 {
		vcfg.setDefault = true
		vcfg.rampPercentage = 0
		return vcfg
	}

	// Don't make updates if build id already the default or has correct ramp.
	// TODO(jlegrone): Get ramp values from status instead of rules
	first := rules.GetAssignmentRules()[0].GetRule()
	if first.GetTargetBuildId() == vcfg.buildID {
		ramp := first.GetPercentageRamp()
		// Skip making changes if build id is already the default
		if ramp == nil || ramp.GetRampPercentage() == 100 {
			return nil
		}
		// Skip making changes if ramp is already set to the desired value
		if ramp.GetRampPercentage() == float32(vcfg.rampPercentage) {
			return nil
		}
	}

	return vcfg
}

func getVersionConfig(strategy temporaliov1alpha1.RolloutStrategy, status *temporaliov1alpha1.TemporalWorkerStatus, rampCreateTime *timestamppb.Timestamp) *versionConfig {
	// Do nothing if target version's deployment is not healthy yet
	if status == nil || status.TargetVersion.HealthySince == nil {
		return nil
	}

	switch strategy.Strategy {
	case temporaliov1alpha1.UpdateManual:
		return nil
	case temporaliov1alpha1.UpdateAllAtOnce:
		// Set new default version immediately
		return &versionConfig{
			setDefault: true,
		}
	case temporaliov1alpha1.UpdateProgressive:
		// Determine the correct percentage ramp
		var (
			healthyDuration    time.Duration
			currentRamp        uint8
			totalPauseDuration = healthyDuration
		)
		if rampCreateTime != nil {
			healthyDuration = time.Since(rampCreateTime.AsTime())
		}
		for _, s := range strategy.Steps {
			if s.RampPercentage != 0 {
				currentRamp = s.RampPercentage
			}
			totalPauseDuration += s.PauseDuration.Duration
			if healthyDuration < totalPauseDuration {
				break
			}
		}
		// We've progressed through all steps; it should now be safe to update the default version
		if healthyDuration > 0 && healthyDuration > totalPauseDuration {
			return &versionConfig{
				setDefault: true,
			}
		}
		// We haven't finished waiting for all steps; use the latest ramp value
		return &versionConfig{
			rampPercentage: currentRamp,
		}
	}

	return nil
}

func (r *TemporalWorkerReconciler) getDeployment(ctx context.Context, ref *v1.ObjectReference) (*appsv1.Deployment, error) {
	var d appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: ref.Namespace,
		Name:      ref.Name,
	}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *TemporalWorkerReconciler) newDeployment(
	w *temporaliov1alpha1.TemporalWorker,
	buildID string,
	connection temporaliov1alpha1.TemporalConnectionSpec,
) (*appsv1.Deployment, error) {
	d := newDeploymentWithoutOwnerRef(&w.TypeMeta, &w.ObjectMeta, &w.Spec, buildID, connection)
	if err := ctrl.SetControllerReference(w, d, r.Scheme); err != nil {
		return nil, err
	}
	return d, nil
}

func newDeploymentWithoutOwnerRef(
	typeMeta *metav1.TypeMeta,
	objectMeta *metav1.ObjectMeta,
	spec *temporaliov1alpha1.TemporalWorkerSpec,
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
	selectorLabels[buildIDLabel] = buildID

	// Set pod labels
	if spec.Template.Labels == nil {
		spec.Template.Labels = selectorLabels
	} else {
		for k, v := range selectorLabels {
			spec.Template.Labels[k] = v
		}
	}

	for i, container := range spec.Template.Spec.Containers {
		container.Env = append(container.Env,
			v1.EnvVar{
				Name:  "TEMPORAL_HOST_PORT",
				Value: connection.HostPort,
			},
			v1.EnvVar{
				Name:  "TEMPORAL_NAMESPACE",
				Value: spec.WorkerOptions.TemporalNamespace,
			},
			v1.EnvVar{
				Name:  "TEMPORAL_TASK_QUEUE",
				Value: spec.WorkerOptions.TaskQueue,
			},
			v1.EnvVar{
				Name:  "WORKER_BUILD_ID",
				Value: buildID,
			},
		)
		spec.Template.Spec.Containers[i] = container
	}

	blockOwnerDeletion := true

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:                       fmt.Sprintf("%s-%s", objectMeta.Name, buildID),
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
			Finalizers: nil,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels,
			},
			Template:        spec.Template,
			MinReadySeconds: spec.MinReadySeconds,
		},
	}
}
