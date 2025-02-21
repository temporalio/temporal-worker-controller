// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"log/slog"
	"os"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/uber-go/tally/v4"
	"github.com/uber-go/tally/v4/prometheus"
	sdktally "go.temporal.io/sdk/contrib/tally"
	"go.temporal.io/sdk/log"
)

func configureObservability(buildID string) (l log.Logger, stopFunc func()) {
	// if err := profiler.Start(
	// 	profiler.WithVersion(buildID),
	// 	profiler.WithLogStartup(false),
	// 	profiler.WithProfileTypes(
	// 		profiler.CPUProfile,
	// 		profiler.HeapProfile,
	// 		profiler.BlockProfile,
	// 		profiler.MutexProfile,
	// 		profiler.GoroutineProfile,
	// 	),
	// ); err != nil {
	// 	panic(err)
	// }

	// tracer.Start(
	// 	tracer.WithUniversalVersion(buildID),
	// 	tracer.WithLogStartup(false),
	// 	tracer.WithSampler(tracer.NewAllSampler()),
	// )

	l = log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	})))

	return l, func() {
		//tracer.Stop()
		//profiler.Stop()
		// Wait a couple seconds before shutting down to ensure metrics etc have been flushed.
		time.Sleep(2 * time.Second)
	}
}

func newPrometheusScope(l log.Logger, c prometheus.Configuration) (tally.Scope, error) {
	reporter, err := c.NewReporter(
		prometheus.ConfigurationOptions{
			Registry: prom.NewRegistry(),
			OnError: func(err error) {
				l.Error("Error in prometheus reporter", "error", err)
			},
		},
	)
	if err != nil {
		return nil, err
	}

	scopeOpts := tally.ScopeOptions{
		CachedReporter:  reporter,
		Separator:       prometheus.DefaultSeparator,
		SanitizeOptions: &sdktally.PrometheusSanitizeOptions,
		Prefix:          "",
	}

	scope, _ := tally.NewRootScope(scopeOpts, time.Second)
	scope = sdktally.NewPrometheusNamingScope(scope)

	return scope, nil
}
