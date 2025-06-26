// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.temporal.io/api/serviceerror"
	sdkclient "go.temporal.io/sdk/client"
)

const (
	controllerIdentityKey     = "temporal.io/controller"
	controllerVersionKey      = "temporal.io/controller-version"
	defaultControllerIdentity = "temporal-worker-controller"
)

// TODO(carlydf): Cache describe success for versions that already exist
// awaitVersionRegistration should be called after a poller starts polling with config of this version, since that is
// what will register the version with the server. SetRamp and SetCurrent will fail if the version does not exist.
func awaitVersionRegistration(
	ctx context.Context,
	l logr.Logger,
	deploymentHandler sdkclient.WorkerDeploymentHandle,
	namespace, versionID string) error {
	ticker := time.NewTicker(1 * time.Second)
	for {
		l.Info(fmt.Sprintf("checking if version %s exists", versionID))
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-ticker.C:
			_, err := deploymentHandler.DescribeVersion(ctx, sdkclient.WorkerDeploymentDescribeVersionOptions{
				Version: versionID,
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
			return awaitVersionRegistrationInDeployment(ctx, l, deploymentHandler, namespace, versionID)
		}
	}
}

func awaitVersionRegistrationInDeployment(
	ctx context.Context,
	l logr.Logger,
	deploymentHandler sdkclient.WorkerDeploymentHandle,
	namespace, versionID string) error {
	deploymentName, _, _ := strings.Cut(versionID, ".")
	ticker := time.NewTicker(1 * time.Second)
	for {
		l.Info(fmt.Sprintf("checking if version %s exists in worker deployment", versionID))
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-ticker.C:
			resp, err := deploymentHandler.Describe(ctx, sdkclient.WorkerDeploymentDescribeOptions{})
			var notFoundErr *serviceerror.NotFound
			if err != nil {
				if errors.As(err, &notFoundErr) {
					continue
				} else {
					return fmt.Errorf("unable to describe worker deployment %s: %w", deploymentName, err)
				}
			}
			for _, vs := range resp.Info.VersionSummaries {
				if vs.Version == versionID {
					return nil
				}
			}
		}
	}
}

// getControllerVersion returns the version from environment variable (set by Helm from image.tag)
func getControllerVersion() string {
	if version := os.Getenv("CONTROLLER_VERSION"); version != "" {
		return version
	}
	return "unknown"
}

// getControllerIdentity returns the identity from environment variable (set by Helm)
func getControllerIdentity() string {
	if identity := os.Getenv("CONTROLLER_IDENTITY"); identity != "" {
		return identity
	}
	return defaultControllerIdentity
}
