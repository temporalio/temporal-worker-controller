// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/uber-go/tally/v4"
	"github.com/uber-go/tally/v4/prometheus"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/datadog/tracing"
	sdktally "go.temporal.io/sdk/contrib/tally"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"
	"gopkg.in/DataDog/dd-trace-go.v1/profiler"
)

func setActivityTimeout(ctx workflow.Context, d time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: d,
	})
}

func newClient(hostPort, namespace, buildID string) (c client.Client, stopFunc func()) {
	l, stopFunc := configureObservability(buildID)

	promScope, err := newPrometheusScope(l, prometheus.Configuration{
		ListenAddress: "0.0.0.0:9090",
		HandlerPath:   "/metrics",
		TimerType:     "histogram",
	})
	if err != nil {
		panic(err)
	}

	c, err = client.Dial(client.Options{
		Identity:  os.Getenv("HOSTNAME"),
		HostPort:  hostPort,
		Namespace: namespace,
		Logger:    l,
		Interceptors: []interceptor.ClientInterceptor{
			tracing.NewTracingInterceptor(tracing.TracerOptions{
				DisableSignalTracing: false,
				DisableQueryTracing:  false,
			}),
		},
		MetricsHandler: sdktally.NewMetricsHandler(promScope),
	})
	if err != nil {
		panic(err)
	}

	return c, stopFunc
}

func configureObservability(buildID string) (l log.Logger, stopFunc func()) {
	go func() {
		// Delay pod readiness by 5 seconds
		time.Sleep(5 * time.Second)
		if err := http.ListenAndServe("0.0.0.0:8080", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})); err != nil {
			panic(err)
		}
	}()

	if err := profiler.Start(
		profiler.WithVersion(buildID),
		profiler.WithLogStartup(false),
		profiler.WithProfileTypes(
			profiler.CPUProfile,
			profiler.HeapProfile,
			profiler.BlockProfile,
			profiler.MutexProfile,
			profiler.GoroutineProfile,
		),
	); err != nil {
		panic(err)
	}

	tracer.Start(
		tracer.WithUniversalVersion(buildID),
		tracer.WithLogStartup(false),
		tracer.WithSampler(tracer.NewAllSampler()),
	)

	l = log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	})))

	return l, func() {
		tracer.Stop()
		profiler.Stop()
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

func mustGetEnv(key string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	panic(fmt.Sprintf("environment variable %q must be set", key))
}
