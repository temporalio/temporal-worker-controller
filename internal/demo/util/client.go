// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"context"
	"crypto/tls"
	"os"

	"github.com/uber-go/tally/v4/prometheus"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/datadog/tracing"
	sdktally "go.temporal.io/sdk/contrib/tally"
	"go.temporal.io/sdk/interceptor"
)

func NewClient(buildID string) (c client.Client, stopFunc func()) {
	return newClient(temporalHostPort, temporalNamespace, buildID, tlsKeyFilePath, tlsCertFilePath)
}

func newClient(hostPort, namespace, buildID, tlsKeyFile, tlsCertFile string) (c client.Client, stopFunc func()) {
	l, stopFunc := configureObservability(buildID)

	promScope, err := newPrometheusScope(l, prometheus.Configuration{
		ListenAddress: "0.0.0.0:9090",
		HandlerPath:   "/metrics",
		TimerType:     "histogram",
	})
	if err != nil {
		panic(err)
	}

	cert, err := tls.LoadX509KeyPair(tlsCertFile, tlsKeyFile)
	if err != nil {
		panic(err)
	}

	opts := client.Options{
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
		ConnectionOptions: client.ConnectionOptions{
			TLS: &tls.Config{
				Certificates: []tls.Certificate{cert},
			},
		},
	}

	l.Debug("Client configured", "identity", opts.Identity, "hostPort", opts.HostPort, "namespace", opts.Namespace)

	c, err = client.Dial(opts)
	if err != nil {
		panic(err)
	}

	if _, err := c.CheckHealth(context.Background(), &client.CheckHealthRequest{}); err != nil {
		panic(err)
	}

	if _, err := c.ListWorkflow(context.Background(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: namespace,
	}); err != nil {
		panic(err)
	}

	return c, stopFunc
}
