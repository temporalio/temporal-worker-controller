// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

// Package twc lets a wrapper binary embed the worker-deployment controller and run it with
// custom Temporal client configuration.
package twc

import (
	"log/slog"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	internalcontroller "github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
)

// WorkerDeploymentController reconciles WorkerDeployment resources.
type WorkerDeploymentController = internalcontroller.WorkerDeploymentReconciler

// CachedClient is a Temporal SDK client paired with a validity check. IsValid is called on every
// cache hit to a false result triggers eviction and re-dial. It is not consulted at creation
// time — creation success is the error return of CreateClientFunc.
type CachedClient = clientpool.CachedClient

// CreateClientFunc creates a Temporal SDK client for a connection spec: secret parsing, options,
// dialing, and health check. The pool caches the returned CachedClient.
type CreateClientFunc = clientpool.CreateClientFunc

// ControllerOption configures a controller built by NewController.
type ControllerOption func(*controllerConfig)

// controllerConfig holds the options for a controller under construction; private so
// options can only be set via the With* constructors.
type controllerConfig struct {
	createClient                               CreateClientFunc
	customizeClientOptions                     func(sdkclient.Options) sdkclient.Options
	poolLogger                                 log.Logger
	maxDeploymentVersionsIneligibleForDeletion int32
	disableDeprecatedTWD                       bool
	disableClusterConnections                  bool
	disableRecoverPanic                        bool
	wrtHPAMatchLabelsStripTemporalPrefix       bool
}

// NewController returns a WorkerDeploymentController wired with the given manager. The manager
// supplies the Kubernetes client, scheme, and event recorder; the Temporal client pool is built
// internally from the manager's client. Pass options to customize client construction or
// override the defaults.
func NewController(mgr ctrl.Manager, opts ...ControllerOption) *WorkerDeploymentController {
	cfg := controllerConfig{
		poolLogger: defaultPoolLogger(),
		maxDeploymentVersionsIneligibleForDeletion: internalcontroller.GetControllerMaxDeploymentVersionsIneligibleForDeletion(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	pool := clientpool.New(cfg.poolLogger, mgr.GetClient())
	pool.CustomizeClientOptions = cfg.customizeClientOptions
	pool.CreateClientFn = cfg.createClient
	return &WorkerDeploymentController{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		TemporalClientPool: pool,
		Recorder:           mgr.GetEventRecorderFor("temporal-worker-controller"), //nolint:staticcheck TODO migrate to the new GetEventRecorder
		MaxDeploymentVersionsIneligibleForDeletion: cfg.maxDeploymentVersionsIneligibleForDeletion,
		DisableDeprecatedTWD:                       cfg.disableDeprecatedTWD,
		DisableClusterConnections:                  cfg.disableClusterConnections,
		DisableRecoverPanic:                        cfg.disableRecoverPanic,
		WRTHPAMatchLabelsStripTemporalPrefix:       cfg.wrtHPAMatchLabelsStripTemporalPrefix,
	}
}

// defaultPoolLogger returns the Temporal SDK logger used for the internally-built client pool
// when WithLogger is not supplied.
func defaultPoolLogger() log.Logger {
	return log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
}

// WithLogger sets the logger used by the internally-built client pool.
func WithLogger(l log.Logger) ControllerOption {
	return func(c *controllerConfig) {
		c.poolLogger = l
	}
}

// WithCreateClient overrides the default client-creation path. The function owns the full
// construction: secret parsing, options, dialing, and health check. When unset, the pool's
// built-in DefaultCreateClient is used.
func WithCreateClient(fn CreateClientFunc) ControllerOption {
	return func(c *controllerConfig) {
		c.createClient = fn
	}
}

// WithCustomizeClientOptions sets the pool's CustomizeClientOptions hook, which lets a wrapper
// binary mutate the SDK client options before dialing. It is consumed by the default
// client-creation path; a wrapper using WithCreateClient owns construction entirely.
func WithCustomizeClientOptions(fn func(sdkclient.Options) sdkclient.Options) ControllerOption {
	return func(c *controllerConfig) {
		c.customizeClientOptions = fn
	}
}

// WithMaxDeploymentVersionsIneligibleForDeletion sets the cap on how many worker deployment versions
// may be ineligible for deletion before the controller stops deploying new versions. When unset,
// the built-in default is used.
func WithMaxDeploymentVersionsIneligibleForDeletion(n int32) ControllerOption {
	return func(c *controllerConfig) {
		c.maxDeploymentVersionsIneligibleForDeletion = n
	}
}

// WithDisableDeprecatedTWD disables watching the deprecated TemporalWorkerDeployment CRD. Set when
// the CRD is not installed.
func WithDisableDeprecatedTWD(disable bool) ControllerOption {
	return func(c *controllerConfig) {
		c.disableDeprecatedTWD = disable
	}
}

// WithDisableClusterConnections drops ClusterConnection support. Set when the manager is
// namespace-scoped (a namespaced Role cannot list the cluster-scoped ClusterConnection CRD).
func WithDisableClusterConnections(disable bool) ControllerOption {
	return func(c *controllerConfig) {
		c.disableClusterConnections = disable
	}
}

// WithDisableRecoverPanic disables panic recovery in the reconciler.
func WithDisableRecoverPanic(disable bool) ControllerOption {
	return func(c *controllerConfig) {
		c.disableRecoverPanic = disable
	}
}

// WithWRTHPAMatchLabelsStripTemporalPrefix sets whether to strip the "temporal_" prefix from
// auto-injected WorkerResourceTemplate HPA external metric matchLabels.
func WithWRTHPAMatchLabelsStripTemporalPrefix(strip bool) ControllerOption {
	return func(c *controllerConfig) {
		c.wrtHPAMatchLabelsStripTemporalPrefix = strip
	}
}
