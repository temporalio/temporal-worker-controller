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
// uniformly distributed in [MinDelay, MaxDelay).
type sleepyTransport struct {
	Transport http.RoundTripper
	MinDelay  time.Duration
	MaxDelay  time.Duration
}

// RoundTrip implements http.RoundTripper with random delays
func (dt *sleepyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	delay := dt.MinDelay + time.Duration(rand.Int63n(int64(dt.MaxDelay-dt.MinDelay)))
	time.Sleep(delay)
	return dt.Transport.RoundTrip(req)
}

// NewHTTPClient creates an HTTP client with random network delays uniformly distributed
// in [minDelay, maxDelay).
func NewHTTPClient(minDelay, maxDelay time.Duration) *http.Client {
	return &http.Client{
		Transport: &sleepyTransport{
			Transport: http.DefaultTransport,
			MinDelay:  minDelay,
			MaxDelay:  maxDelay,
		},
	}
}
