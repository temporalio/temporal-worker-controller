// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package clientpool

import (
	"sync"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
)

type ClientPool struct {
	mux     sync.RWMutex
	logger  log.Logger
	clients map[string]client.Client
}

func New(l log.Logger) *ClientPool {
	return &ClientPool{
		logger:  l,
		clients: make(map[string]client.Client),
	}
}

func (cp *ClientPool) GetWorkflowServiceClient(hostPort string) (workflowservice.WorkflowServiceClient, bool) {
	cp.mux.RLock()
	defer cp.mux.RUnlock()

	c, ok := cp.clients[hostPort]
	if ok {
		return c.WorkflowService(), true
	}
	return nil, false
}

func (cp *ClientPool) UpsertClient(hostPort string) (workflowservice.WorkflowServiceClient, error) {
	c, err := client.Dial(client.Options{
		Logger:   cp.logger,
		HostPort: hostPort,
	})
	if err != nil {
		return nil, err
	}

	cp.mux.Lock()
	defer cp.mux.Unlock()

	cp.clients[hostPort] = c

	return c.WorkflowService(), nil
}

func (cp *ClientPool) Close() {
	cp.mux.Lock()
	defer cp.mux.Unlock()

	for _, c := range cp.clients {
		c.Close()
	}

	cp.clients = make(map[string]client.Client)
}
