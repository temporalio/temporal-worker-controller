// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

// Package controller lets a wrapper binary embed the worker-deployment controller and run it with
// custom Temporal client configuration.
package controller

import (
	"errors"

	ctrl "sigs.k8s.io/controller-runtime"

	internalcontroller "github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/pkg/controller/config"
	sdkclient "go.temporal.io/sdk/client"
)

// Controller reconciles WorkerDeployment resources.
type Controller = internalcontroller.WorkerDeploymentReconciler

// CachedClient is a Temporal SDK client paired with a validity check. IsValid is called on every
// cache hit. A false result triggers eviction and re-dial.
type CachedClient = clientpool.CachedClient

// CreateClientFunc creates a Temporal SDK client for a connection spec: secret parsing, options,
// dialing, and health check. The pool caches the returned CachedClient.
type CreateClientFunc = clientpool.CreateClientFunc

// CustomizeClientOptionsFunc mutates SDK client options before dialing. It only applies to
// the default client-creation path (WithDefaultClient).
type CustomizeClientOptionsFunc func(sdkclient.Options) sdkclient.Options

type (
	Option = config.Option
)

var (
	WithLogger                                     = config.WithLogger
	WithCreateClient                               = config.WithCreateClient
	WithDefaultClient                              = config.WithDefaultClient
	WithMaxDeploymentVersionsIneligibleForDeletion = config.WithMaxDeploymentVersionsIneligibleForDeletion
	WithDisableDeprecatedTWD                       = config.WithDisableDeprecatedTWD
	WithDisableRecoverPanic                        = config.WithDisableRecoverPanic
	WithDisableClusterConnections                  = config.WithDisableClusterConnections
	WithWRTHPAMatchLabelsStripTemporalPrefix       = config.WithWRTHPAMatchLabelsStripTemporalPrefix
)

// New returns a Controller wired with the given manager, or an
// error if the options are misconfigured (e.g. both WithCreateClient and WithDefaultClient).
// The manager supplies the Kubernetes client, scheme, and event recorder; the Temporal
// client pool is built internally from the manager's client.
func New(mgr ctrl.Manager, opts ...Option) (*Controller, error) {
	cfg := config.ConfigFromEnv()
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.CreateClient != nil && cfg.DefaultClient {
		return nil, errors.New("controller.WithCreateClient and controller.WithDefaultClient are mutually exclusive")
	}
	pool := clientpool.New(cfg.PoolLogger, mgr.GetClient(), cfg.CustomClientOptions)
	if cfg.CreateClient != nil {
		pool.CreateClientFn = cfg.CreateClient
	}
	return &Controller{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		TemporalClientPool: pool,
		Recorder:           mgr.GetEventRecorderFor("temporal-worker-controller"), //nolint:staticcheck // deprecated; migration to GetEventRecorder requires changing the reconciler field type and all Eventf call sites
		Config:             cfg,
	}, nil
}
