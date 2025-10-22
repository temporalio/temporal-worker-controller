// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package clientpool

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type AuthMode string

const (
	AuthModeTLS           AuthMode = "TLS"
	AuthModeAPIKey        AuthMode = "API_KEY"
	AuthModeNoCredentials AuthMode = "NO_CREDENTIALS"
	// Add more auth modes here as they are supported
)

type ClientPoolKey struct {
	HostPort   string
	Namespace  string
	SecretName string   // Include secret name in key to invalidate cache when the secret name changes
	AuthMode   AuthMode // Include auth mode in key to invalidate cache when the auth mode changes for the secret
}

type MTLSAuth struct {
	tlsConfig  *tls.Config
	expiryTime time.Time // Time we consider the cert expired (NotAfter minus safety buffer)
}

type ClientAuth struct {
	mode AuthMode
	mTLS *MTLSAuth // non-nil when mode == AuthMTLS, nil when mode == AuthAPIKey
}

type ClientInfo struct {
	client sdkclient.Client
	auth   ClientAuth
}

type ClientPool struct {
	mux       sync.RWMutex
	logger    log.Logger
	clients   map[ClientPoolKey]ClientInfo
	k8sClient runtimeclient.Client
}

func New(l log.Logger, c runtimeclient.Client) *ClientPool {
	return &ClientPool{
		logger:    l,
		clients:   make(map[ClientPoolKey]ClientInfo),
		k8sClient: c,
	}
}

func (cp *ClientPool) GetSDKClient(key ClientPoolKey) (sdkclient.Client, bool) {
	cp.mux.RLock()
	defer cp.mux.RUnlock()

	info, ok := cp.clients[key]
	if !ok {
		return nil, false
	}

	if key.AuthMode == AuthModeTLS {
		// Check if any certificate is expired
		expired, err := isCertificateExpired(info.auth.mTLS.expiryTime)
		if err != nil {
			cp.logger.Error("Error checking certificate expiration", "error", err)
			return nil, false
		}
		if expired {
			cp.logger.Warn("Certificate is expired or is going to expire soon")
			return nil, false
		}
	}

	return info.client, true
}

type NewClientOptions struct {
	TemporalNamespace string
	K8sNamespace      string
	Spec              v1alpha1.TemporalConnectionSpec
}

func (cp *ClientPool) fetchClientUsingMTLSSecret(secret corev1.Secret, opts NewClientOptions) (sdkclient.Client, error) {

	clientOpts := sdkclient.Options{
		Logger:    cp.logger,
		HostPort:  opts.Spec.HostPort,
		Namespace: opts.TemporalNamespace,
	}

	var pemCert []byte
	var expiryTime time.Time

	// Extract the certificate to calculate the effective expiration time
	pemCert = secret.Data["tls.crt"]

	// Check if certificate is expired before creating the client
	exp, err := calculateCertificateExpirationTime(pemCert, 5*time.Minute)
	if err != nil {
		return nil, errors.New("failed to check certificate expiration: " + err.Error())
	}
	expired, err := isCertificateExpired(exp)
	if err != nil {
		return nil, errors.New("failed to check certificate expiration: " + err.Error())
	}
	if expired {
		return nil, errors.New("certificate is expired or is going to expire soon")
	}

	cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		return nil, err
	}
	clientOpts.ConnectionOptions.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	expiryTime = exp

	c, err := sdkclient.Dial(clientOpts)
	if err != nil {
		return nil, err
	}

	if _, err := c.CheckHealth(context.Background(), &sdkclient.CheckHealthRequest{}); err != nil {
		panic(err)
	}

	cp.mux.Lock()
	defer cp.mux.Unlock()

	key := ClientPoolKey{
		HostPort:   opts.Spec.HostPort,
		Namespace:  opts.TemporalNamespace,
		SecretName: opts.Spec.MutualTLSSecretRef.Name,
		AuthMode:   AuthModeTLS,
	}
	cp.clients[key] = ClientInfo{
		client: c,
		auth: ClientAuth{
			mode: AuthModeTLS,
			mTLS: &MTLSAuth{tlsConfig: clientOpts.ConnectionOptions.TLS, expiryTime: expiryTime},
		},
	}

	return c, nil
}

func (cp *ClientPool) fetchClientUsingAPIKeySecret(secret corev1.Secret, opts NewClientOptions) (sdkclient.Client, error) {
	clientOpts := sdkclient.Options{
		Logger:    cp.logger,
		HostPort:  opts.Spec.HostPort,
		Namespace: opts.TemporalNamespace,
		ConnectionOptions: sdkclient.ConnectionOptions{
			TLS: &tls.Config{},
		},
	}

	clientOpts.Credentials = sdkclient.NewAPIKeyDynamicCredentials(func(ctx context.Context) (string, error) {
		return string(secret.Data[opts.Spec.APIKeySecretRef.Key]), nil
	})

	c, err := sdkclient.Dial(clientOpts)
	if err != nil {
		return nil, err
	}

	cp.mux.Lock()
	defer cp.mux.Unlock()

	key := ClientPoolKey{
		HostPort:   opts.Spec.HostPort,
		Namespace:  opts.TemporalNamespace,
		SecretName: opts.Spec.APIKeySecretRef.Name,
		AuthMode:   AuthModeAPIKey,
	}
	cp.clients[key] = ClientInfo{
		client: c,
		auth: ClientAuth{
			mode: AuthModeAPIKey,
			mTLS: nil,
		},
	}

	return c, nil
}

func (cp *ClientPool) fetchClientUsingNoCredentials(opts NewClientOptions) (sdkclient.Client, error) {
	clientOpts := sdkclient.Options{
		Logger:    cp.logger,
		HostPort:  opts.Spec.HostPort,
		Namespace: opts.TemporalNamespace,
	}

	c, err := sdkclient.Dial(clientOpts)
	if err != nil {
		return nil, err
	}

	key := ClientPoolKey{
		HostPort:   opts.Spec.HostPort,
		Namespace:  opts.TemporalNamespace,
		SecretName: "",
		AuthMode:   AuthModeNoCredentials,
	}
	cp.clients[key] = ClientInfo{
		client: c,
		auth: ClientAuth{
			mode: AuthModeNoCredentials,
			mTLS: nil,
		},
	}

	return c, nil
}

func (cp *ClientPool) UpsertClient(ctx context.Context, secretName string, authMode AuthMode, opts NewClientOptions) (sdkclient.Client, error) {

	// Fetch the secret from k8s cluster, if it exists. Otherwise, create a connection with the server without using any credentials.
	var secret corev1.Secret
	if secretName != "" {
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      secretName,
			Namespace: opts.K8sNamespace,
		}, &secret); err != nil {
			return nil, err
		}
	}

	// Check the secret type
	switch authMode {
	case AuthModeTLS:
		if secret.Type != corev1.SecretTypeTLS {
			err := fmt.Errorf("secret %s must be of type kubernetes.io/tls", secret.Name)
			return nil, err
		}
		return cp.fetchClientUsingMTLSSecret(secret, opts)

	case AuthModeAPIKey:
		if secret.Type != corev1.SecretTypeOpaque {
			err := fmt.Errorf("secret %s must be of type kubernetes.io/opaque", secret.Name)
			return nil, err
		}
		return cp.fetchClientUsingAPIKeySecret(secret, opts)

	case AuthModeNoCredentials:
		return cp.fetchClientUsingNoCredentials(opts)

	default:
		return nil, fmt.Errorf("invalid auth mode: %s", authMode)
	}

}

func (cp *ClientPool) Close() {
	cp.mux.Lock()
	defer cp.mux.Unlock()

	for _, c := range cp.clients {
		c.client.Close()
	}

	cp.clients = make(map[ClientPoolKey]ClientInfo)
}

func calculateCertificateExpirationTime(certBytes []byte, bufferTime time.Duration) (time.Time, error) {
	if len(certBytes) == 0 {
		return time.Time{}, fmt.Errorf("no certificate bytes provided")
	}

	block, _ := pem.Decode(certBytes)
	if block == nil {
		return time.Time{}, fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse certificate: %v", err)
	}

	expiryTime := cert.NotAfter.Add(-bufferTime)
	return expiryTime, nil
}

func isCertificateExpired(expiryTime time.Time) (bool, error) {
	if time.Now().After(expiryTime) {
		return true, nil
	}
	return false, nil
}
