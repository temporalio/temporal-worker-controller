// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	"go.temporal.io/api/deployment/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"strings"
	"time"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	"github.com/DataDog/temporal-worker-controller/internal/controller/k8s.io/utils"
)

const (
	defaultScaledownDelay = 1 * time.Hour
	defaultDeleteDelay    = 24 * time.Hour
	// TODO(carlydf): Consider making this separator "." if/when Temporal can handle that
	// Use "--" as a separator so that the version ID can be the name of the deployment we create for that version.
	deploymentNameSeparator = "--"
)

func computeWorkerDeploymentName(w *temporaliov1alpha1.TemporalWorkerDeployment) string {
	return w.GetName() + deploymentNameSeparator + w.GetNamespace()
}

func computeVersionID(r *temporaliov1alpha1.TemporalWorkerDeployment) string {
	return getVersionID(computeWorkerDeploymentName(r), computeBuildID(&r.Spec))
}

func getVersionID(workerDeploymentName, buildID string) string {
	return workerDeploymentName + "." + buildID
}

func computeBuildID(spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec) string {
	return utils.ComputeHash(&spec.Template, nil)
}

func getTestWorkflowID(series, taskQueue, buildID string) string {
	return fmt.Sprintf("test-deploy:%s:%s:%s", series, taskQueue, buildID)
}

func getScaledownDelay(spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec) time.Duration {
	if spec.SunsetStrategy.ScaledownDelay == nil {
		return defaultScaledownDelay
	}
	return spec.SunsetStrategy.ScaledownDelay.Duration
}

func getDeleteDelay(spec *temporaliov1alpha1.TemporalWorkerDeploymentSpec) time.Duration {
	if spec.SunsetStrategy.DeleteDelay == nil {
		return defaultDeleteDelay
	}
	return spec.SunsetStrategy.DeleteDelay.Duration
}

func newObjectRef(d *appsv1.Deployment) *v1.ObjectReference {
	if d == nil {
		return nil
	}
	return &v1.ObjectReference{
		Kind:            d.Kind,
		Namespace:       d.Namespace,
		Name:            d.Name,
		UID:             d.UID,
		APIVersion:      d.APIVersion,
		ResourceVersion: d.ResourceVersion,
	}
}

func describeWorkerDeploymentHandleNotFound(
	ctx context.Context,
	temporalClient workflowservice.WorkflowServiceClient,
	req *workflowservice.DescribeWorkerDeploymentRequest) (*workflowservice.DescribeWorkerDeploymentResponse, error) {
	describeResp, err := temporalClient.DescribeWorkerDeployment(ctx, req)

	var notFoundErr *serviceerror.NotFound
	if err != nil {
		if errors.As(err, &notFoundErr) {
			return &workflowservice.DescribeWorkerDeploymentResponse{
				ConflictToken: nil,
				WorkerDeploymentInfo: &deployment.WorkerDeploymentInfo{
					Name:          req.GetDeploymentName(),
					RoutingConfig: &deployment.RoutingConfig{CurrentVersion: "__unversioned__"},
				},
			}, nil
		} else {
			return nil, fmt.Errorf("unable to describe worker deployment %s: %w", req.GetDeploymentName(), err)
		}
	}
	return describeResp, err
}

// TODO(carlydf): Cache describe success for versions that already exist
// awaitVersionRegistration should be called after a poller starts polling with config of this version, since that is
// what will register the version with the server. SetRamp and SetCurrent will fail if the version does not exist.
func awaitVersionRegistration(
	ctx context.Context,
	l logr.Logger,
	temporalClient workflowservice.WorkflowServiceClient,
	namespace, versionID string) error {
	ticker := time.NewTicker(1 * time.Second)
	for {
		l.Info(fmt.Sprintf("checking if version %s exists", versionID))
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-ticker.C:
			_, err := temporalClient.DescribeWorkerDeploymentVersion(ctx, &workflowservice.DescribeWorkerDeploymentVersionRequest{
				Namespace: namespace,
				Version:   versionID,
			})
			var notFoundErr *serviceerror.NotFound
			if err != nil {
				if errors.As(err, &notFoundErr) {
					continue
				} else {
					return fmt.Errorf("unable to describe worker deployment version %s: %w", versionID, err)
				}
			}
			// After the version exists, confirm that it also exists in the worker deployment
			// TODO(carlydf): Remove this check after next Temporal Cloud version which solves this inconsistency
			return awaitVersionRegistrationInDeployment(ctx, l, temporalClient, namespace, versionID)
		}
	}
}

func awaitVersionRegistrationInDeployment(
	ctx context.Context,
	l logr.Logger,
	temporalClient workflowservice.WorkflowServiceClient,
	namespace, versionID string) error {
	deploymentName, _, _ := strings.Cut(versionID, ".")
	ticker := time.NewTicker(1 * time.Second)
	for {
		l.Info(fmt.Sprintf("checking if version %s exists in worker deployment", versionID))
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-ticker.C:
			resp, err := temporalClient.DescribeWorkerDeployment(ctx, &workflowservice.DescribeWorkerDeploymentRequest{
				Namespace:      namespace,
				DeploymentName: deploymentName,
			})
			var notFoundErr *serviceerror.NotFound
			if err != nil {
				if errors.As(err, &notFoundErr) {
					continue
				} else {
					return fmt.Errorf("unable to describe worker deployment %s: %w", deploymentName, err)
				}
			}
			for _, vs := range resp.GetWorkerDeploymentInfo().GetVersionSummaries() {
				if vs.GetVersion() == versionID {
					return nil
				}
			}
		}
	}
}
