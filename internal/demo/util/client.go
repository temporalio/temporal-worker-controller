// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"context"
	"os"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

func NewClient(buildID string, metricsPort int) (c client.Client, stopFunc func()) {
	return newClient(buildID, metricsPort)
}

func newClient(buildID string, metricsPort int) (c client.Client, stopFunc func()) {
	l, m, stopFunc := configureObservability(buildID, metricsPort)

	// Load client options from environment variables using envconfig
	opts, err := envconfig.LoadDefaultClientOptions()
	if err != nil {
		panic(err)
	}

	// Override with our custom settings
	opts.Identity = os.Getenv("HOSTNAME")
	opts.Logger = l
	opts.MetricsHandler = m

	l.Debug("Client configured", "identity", opts.Identity, "hostPort", opts.HostPort, "namespace", opts.Namespace)

	c, err = client.Dial(opts)
	if err != nil {
		panic(err)
	}

	// if _, err := c.CheckHealth(context.Background(), &client.CheckHealthRequest{}); err != nil {
	// 	panic(err)
	// }

	if _, err := c.ListWorkflow(context.Background(), &workflowservice.ListWorkflowExecutionsRequest{
		Namespace: opts.Namespace,
	}); err != nil {
		panic(err)
	}

	return c, stopFunc
}
