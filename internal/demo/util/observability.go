// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/log"
)

func configureObservability(buildID string, metricsPort int) (l log.Logger, m opentelemetry.MetricsHandler, stopFunc func()) {
	slogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	}))
	l = log.NewStructuredLogger(slogger)

	exporter, err := prometheus.New()
	if err != nil {
		panic(err)
	}
	m = opentelemetry.NewMetricsHandler(opentelemetry.MetricsHandlerOptions{
		Meter:             metric.NewMeterProvider(metric.WithReader(exporter)).Meter("worker"),
		InitialAttributes: attribute.NewSet(attribute.String("version", buildID)),
	})

	go func() {
		addr := fmt.Sprintf(":%d", metricsPort)
		slogger.Info("Serving metrics", slog.String("address", fmt.Sprintf("localhost%s/metrics", addr)))
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(addr, nil); err != nil {
			slogger.Error("error serving http", slog.Any("error", err))
			return
		}
	}()

	return l, m, func() {
		// Wait a few seconds before shutting down to ensure metrics etc have been flushed.
		time.Sleep(5 * time.Second)
	}
}
