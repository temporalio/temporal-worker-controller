// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package config

import (
	"log/slog"
	"os"

	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
)

// Option configures a controller built by New.
type Option func(*Config)

// Config holds the options for a controller under construction
type Config struct {
	CreateClient        clientpool.CreateClientFunc
	CustomClientOptions func(sdkclient.Options) sdkclient.Options
	DefaultClient       bool
	PoolLogger          log.Logger
	// When a Worker Deployment has the maximum number of versions (100 per
	// Worker Deployment by default), it will delete the oldest eligible
	// version when a worker with the 101st version arrives.  If no versions
	// are eligible for deletion, that worker's poll will fail, which is
	// dangerous.  To protect against this, when a Worker Deployment has too
	// many versions ineligible for deletion, the controller will stop
	// deploying new workers in order to give the user the opportunity to
	// adjust their sunset policy to avoid this situation before it actually
	// blocks deployment of a new worker version on the server side.
	//
	// MaxDeploymentVersionsIneligibleForDeletion is currently defaulted to 75,
	// which is safe for the default server value of
	// `matching.maxVersionsInDeployment=100`.  Users who reduce
	// `matching.maxVersionsInDeployment` in their dynamicconfig should also
	// reduce this value.
	MaxDeploymentVersionsIneligibleForDeletion int32
	DisableDeprecatedTWD                       bool
	// DisableClusterConnections drops ClusterConnection support. Set when the
	// manager is namespace-scoped: ClusterConnection is cluster-scoped, so a
	// namespaced Role can never authorize listing it and the watch would retry
	// forever against a 403.
	DisableClusterConnections bool
	// Disables panic recovery if true
	DisableRecoverPanic bool
	// WRTHPAMatchLabelsStripTemporalPrefix removes the "temporal_" prefix from
	// controller-managed external metric matchLabels in rendered
	// WorkerResourceTemplates.
	WRTHPAMatchLabelsStripTemporalPrefix bool
}

// ConfigFromEnv returns a new Config struct popualated with any options read
// from environs variables and default values.
func ConfigFromEnv() Config {
	return Config{
		PoolLogger: defaultPoolLogger(),
		MaxDeploymentVersionsIneligibleForDeletion: GetControllerMaxDeploymentVersionsIneligibleForDeletion(),
	}
}

// defaultPoolLogger returns the Temporal SDK logger used for the internally-built client pool
// when WithLogger is not supplied.
func defaultPoolLogger() log.Logger {
	return log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
}

// WithLogger sets the logger used by the internally-built client pool.
func WithLogger(l log.Logger) Option {
	return func(c *Config) {
		c.PoolLogger = l
	}
}

// WithCreateClient overrides the default client-creation path. The function
// owns the full construction: secret parsing, options, dialing, and health
// check.
func WithCreateClient(fn clientpool.CreateClientFunc) Option {
	return func(c *Config) {
		c.CreateClient = fn
	}
}

// WithDefaultClient uses the built-in client-creation path (parse Secret,
// build options, dial, health-check). Accepts optional CustomizeClientOptions
// to mutate SDK options before dialing.
func WithDefaultClient(customize ...func(sdkclient.Options) sdkclient.Options) Option {
	return func(c *Config) {
		c.DefaultClient = true
		if len(customize) > 0 {
			c.CustomClientOptions = customize[0]
		}
	}
}

// WithMaxDeploymentVersionsIneligibleForDeletion sets the cap on how many
// worker deployment versions may be ineligible for deletion before the
// controller stops deploying new versions. When unset, the built-in default is
// used.
func WithMaxDeploymentVersionsIneligibleForDeletion(n int32) Option {
	return func(c *Config) {
		c.MaxDeploymentVersionsIneligibleForDeletion = n
	}
}

// WithDisableDeprecatedTWD disables watching the deprecated
// TemporalWorkerDeployment CRD. Set when the CRD is not installed.
func WithDisableDeprecatedTWD(disable bool) Option {
	return func(c *Config) {
		c.DisableDeprecatedTWD = disable
	}
}

// WithDisableClusterConnections drops ClusterConnection support. Set when the
// manager is namespace-scoped (a namespaced Role cannot list the
// cluster-scoped ClusterConnection CRD).
func WithDisableClusterConnections(disable bool) Option {
	return func(c *Config) {
		c.DisableClusterConnections = disable
	}
}

// WithDisableRecoverPanic disables panic recovery in the reconciler.
func WithDisableRecoverPanic(disable bool) Option {
	return func(c *Config) {
		c.DisableRecoverPanic = disable
	}
}

// WithWRTHPAMatchLabelsStripTemporalPrefix sets whether to strip the
// "temporal_" prefix from auto-injected WorkerResourceTemplate HPA external
// metric matchLabels.
func WithWRTHPAMatchLabelsStripTemporalPrefix(strip bool) Option {
	return func(c *Config) {
		c.WRTHPAMatchLabelsStripTemporalPrefix = strip
	}
}
