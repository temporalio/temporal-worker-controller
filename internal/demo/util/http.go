// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package util

import (
	"math/rand"
	"net/http"
	"time"
)

// sleepyTransport wraps an http.RoundTripper and introduces random network delays
type sleepyTransport struct {
	Transport http.RoundTripper
	MaxDelay  time.Duration
}

// RoundTrip implements http.RoundTripper with random delays
func (dt *sleepyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Introduce random delay up to MaxDelay
	delay := time.Duration(rand.Int63n(int64(dt.MaxDelay)))
	time.Sleep(delay)

	return dt.Transport.RoundTrip(req)
}

// NewHTTPClient creates an HTTP client with random network delays up to maxDelay
func NewHTTPClient(maxDelay time.Duration) *http.Client {
	return &http.Client{
		Transport: &sleepyTransport{
			Transport: http.DefaultTransport,
			MaxDelay:  maxDelay,
		},
	}
}
