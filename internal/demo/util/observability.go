// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/log"
)

func configureObservability(buildID string) (l log.Logger, m opentelemetry.MetricsHandler, stopFunc func()) {
	l = log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource:   true,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	})))

	m = opentelemetry.NewMetricsHandler(opentelemetry.MetricsHandlerOptions{
		Meter:             metric.NewMeterProvider().Meter("worker"),
		InitialAttributes: attribute.NewSet(attribute.String("version", buildID)),
	})

	return l, m, func() {
		// Wait a few seconds before shutting down to ensure metrics etc have been flushed.
		time.Sleep(5 * time.Second)
	}
}
