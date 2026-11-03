// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

func TestNewWebhookOptionsPort(t *testing.T) {
	for _, port := range []int{-1, 0, 1, 9443, 10443, 65535, 65536} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			options, err := newWebhookOptions(port, "", "tls.crt", "tls.key")
			if port < 1 || port > 65535 {
				require.EqualError(t, err, fmt.Sprintf("webhook port must be between 1 and 65535, got %d", port))
				require.Equal(t, webhook.Options{}, options)
				return
			}
			require.NoError(t, err)
			require.Equal(t, webhook.Options{Port: port}, options)
		})
	}
}

func TestNewWebhookOptionsCertificates(t *testing.T) {
	tests := map[string]struct {
		certDir  string
		certName string
		keyName  string
		want     webhook.Options
	}{
		"default directory": {
			certName: "tls.crt",
			keyName:  "tls.key",
			want:     webhook.Options{Port: 10443},
		},
		"custom directory": {
			certDir:  "/custom/certs",
			certName: "tls.crt",
			keyName:  "tls.key",
			want: webhook.Options{
				Port: 10443, CertDir: "/custom/certs", CertName: "tls.crt", KeyName: "tls.key",
			},
		},
		"custom file names": {
			certDir:  "/custom/certs",
			certName: "server.crt",
			keyName:  "server.key",
			want: webhook.Options{
				Port: 10443, CertDir: "/custom/certs", CertName: "server.crt", KeyName: "server.key",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			options, err := newWebhookOptions(10443, tt.certDir, tt.certName, tt.keyName)
			require.NoError(t, err)
			require.Equal(t, tt.want, options)
		})
	}
}
