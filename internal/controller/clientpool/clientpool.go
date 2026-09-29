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

type ClientPoolKey struct {
	HostPort            string
	TLSServerName       string
	Namespace           string            // Temporal namespace
	SecretName          string            // Include secret name in key to invalidate cache when the secret name changes
	TLSCACertSecretName string            // Include CA secret name in key to invalidate cache when TLS.CACertSecretRef changes
	AuthMode            v1alpha1.AuthMode // Include auth mode in key to invalidate cache when the auth mode changes for the secret
}

type ClientAuth struct {
	mode        v1alpha1.AuthMode
	tls         *tls.Config           // set on SDK ConnectionOptions.TLS at dial time
	credentials sdkclient.Credentials // non-nil only for API key auth; set on SDK Options.Credentials
	expiryTime  time.Time             // mTLS only: NotAfter minus safety buffer; zero for other modes
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
	// dialFn establishes a Temporal SDK connection from the given options. In production
	// this is sdkclient.Dial; in tests it can be replaced with a function that returns a
	// mock client without making any network calls.
	dialFn func(sdkclient.Options) (sdkclient.Client, error)
}

type AuthConfigError struct{ Err error }

func (e *AuthConfigError) Error() string { return e.Err.Error() }
func (e *AuthConfigError) Unwrap() error { return e.Err }

type DialError struct{ Err error }

func (e *DialError) Error() string { return e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

func New(l log.Logger, c runtimeclient.Client) *ClientPool {
	return &ClientPool{
		logger:    l,
		clients:   make(map[ClientPoolKey]ClientInfo),
		k8sClient: c,
		dialFn:    sdkclient.Dial,
	}
}

// EvictClient removes the client for the given key from the pool and closes it.
// Safe to call when the key is not present.
func (cp *ClientPool) EvictClient(key ClientPoolKey) {
	cp.mux.Lock()
	defer cp.mux.Unlock()
	if info, ok := cp.clients[key]; ok {
		info.client.Close()
		delete(cp.clients, key)
	}
}

func (cp *ClientPool) GetClient(
	ctx context.Context,
	spec v1alpha1.ConnectionSpec,
	temporalNamespace, k8sNamespace, identity string,
) (sdkclient.Client, ClientPoolKey, error) {
	// Validate the spec
	if err := spec.Validate(); err != nil {
		return nil, ClientPoolKey{}, &AuthConfigError{Err: err}
	}
	// Check if client is already in cache
	key := cp.createKey(spec, temporalNamespace)
	if info, ok := cp.getClientByKey(key); ok && cp.isCachedClientValid(info) {
		return info.client, key, nil
	}
	// Create a new client
	auth, err := cp.parseClientSecret(ctx, spec, k8sNamespace)
	if err != nil {
		return nil, ClientPoolKey{}, &AuthConfigError{Err: err}
	}
	clientOpts := cp.getClientOptions(spec, temporalNamespace, identity, auth)
	client, err := cp.dialFn(clientOpts)
	if err != nil {
		return nil, ClientPoolKey{}, &DialError{Err: err}
	}
	if err := cp.healthCheck(client, auth); err != nil {
		client.Close()
		return nil, ClientPoolKey{}, &DialError{Err: err}
	}
	// Cache the new client
	cp.cacheClient(key, client, auth)
	return client, key, nil
}

func (cp *ClientPool) createKey(spec v1alpha1.ConnectionSpec, temporalNamespace string) ClientPoolKey {
	return ClientPoolKey{
		HostPort:            spec.HostPort,
		TLSServerName:       spec.TLSServerName(),
		Namespace:           temporalNamespace,
		SecretName:          spec.SecretName(),
		TLSCACertSecretName: spec.TLSCACertSecretName(),
		AuthMode:            spec.AuthMode(),
	}
}

func (cp *ClientPool) getClientByKey(key ClientPoolKey) (ClientInfo, bool) {
	cp.mux.RLock()
	defer cp.mux.RUnlock()

	info, ok := cp.clients[key]
	if !ok {
		return ClientInfo{}, false
	}
	return info, true
}

func (cp *ClientPool) isCachedClientValid(info ClientInfo) bool {
	if info.auth.mode != v1alpha1.AuthModeTLS {
		return true
	}
	expired, err := isCertificateExpired(info.auth.expiryTime)
	if err != nil {
		cp.logger.Error("Error checking certificate expiration", "error", err)
		return false
	}
	if expired {
		cp.logger.Warn("Certificate is expired or is going to expire soon")
		return false
	}
	return true
}

func (cp *ClientPool) Clients() map[ClientPoolKey]sdkclient.Client {
	cp.mux.RLock()
	defer cp.mux.RUnlock()
	out := make(map[ClientPoolKey]sdkclient.Client, len(cp.clients))
	for k, info := range cp.clients {
		out[k] = info.client
	}
	return out
}

type namespaceHeadersProvider string

func (p namespaceHeadersProvider) GetHeaders(context.Context) (map[string]string, error) {
	return map[string]string{"temporal-namespace": string(p)}, nil
}

func (cp *ClientPool) getClientOptions(spec v1alpha1.ConnectionSpec, temporalNamespace, identity string, auth ClientAuth) sdkclient.Options {
	opts := sdkclient.Options{
		Logger:          cp.logger,
		HostPort:        spec.HostPort,
		Namespace:       temporalNamespace,
		Identity:        identity,
		HeadersProvider: namespaceHeadersProvider(temporalNamespace),
	}
	opts.ConnectionOptions.TLS = auth.tls
	if auth.credentials != nil {
		opts.Credentials = auth.credentials
	}
	return opts
}

func (cp *ClientPool) fetchClientUsingMTLSSecret(secret corev1.Secret, spec v1alpha1.ConnectionSpec) (ClientAuth, error) {
	tlsServerName := spec.TLSServerName()

	// Extract the certificate to calculate the effective expiration time
	pemCert := secret.Data["tls.crt"]

	// Check if certificate is expired before creating the client
	exp, err := calculateCertificateExpirationTime(pemCert, 5*time.Minute)
	if err != nil {
		return ClientAuth{}, errors.New("failed to check certificate expiration: " + err.Error())
	}
	expired, err := isCertificateExpired(exp)
	if err != nil {
		return ClientAuth{}, errors.New("failed to check certificate expiration: " + err.Error())
	}
	if expired {
		return ClientAuth{}, errors.New("certificate is expired or is going to expire soon")
	}

	cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		return ClientAuth{}, err
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ServerName:   tlsServerName,
	}
	// If the secret contains a CA certificate, append it to the system CA pool for
	// server certificate verification. This enables connecting to Temporal servers whose
	// TLS certificates are signed by private or internal CAs (e.g. cert-manager in a
	// self-hosted cluster) while still trusting publicly-signed endpoints like Temporal
	// Cloud. When ca.crt is absent, RootCAs remains unset and Go's TLS implementation
	// uses the system CA bundle by default.
	if caCert, ok := secret.Data["ca.crt"]; ok && len(caCert) > 0 {
		rootCAs, err := cp.TLSCertPool(caCert)
		if err != nil {
			return ClientAuth{}, err
		}
		tlsCfg.RootCAs = rootCAs
	}

	return ClientAuth{
		mode:       v1alpha1.AuthModeTLS,
		tls:        tlsCfg,
		expiryTime: exp,
	}, nil
}

func (cp *ClientPool) fetchClientUsingAPIKeySecret(spec v1alpha1.ConnectionSpec, k8sNamespace string, caCert []byte) (ClientAuth, error) {
	tlsServerName := spec.TLSServerName()
	tlsCfg := &tls.Config{ServerName: tlsServerName}
	rootCAs, err := cp.TLSCertPool(caCert)
	if err != nil {
		return ClientAuth{}, err
	}
	tlsCfg.RootCAs = rootCAs

	secretName := spec.APIKeySecretRef.Name
	secretKey := spec.APIKeySecretRef.Key
	credentials := sdkclient.NewAPIKeyDynamicCredentials(func(ctx context.Context) (string, error) {
		return cp.fetchAPIKeyFromSecret(ctx, secretName, k8sNamespace, secretKey)
	})

	return ClientAuth{
		mode:        v1alpha1.AuthModeAPIKey,
		tls:         tlsCfg,
		credentials: credentials,
	}, nil
}

func (cp *ClientPool) fetchClientUsingNoCredentials(spec v1alpha1.ConnectionSpec, caCert []byte) (ClientAuth, error) {
	tlsServerName := spec.TLSServerName()
	rootCAs, err := cp.TLSCertPool(caCert)
	if err != nil {
		return ClientAuth{}, err
	}
	var tlsCfg *tls.Config
	if tlsServerName != "" || rootCAs != nil {
		tlsCfg = &tls.Config{ServerName: tlsServerName, RootCAs: rootCAs}
	}

	return ClientAuth{
		mode: v1alpha1.AuthModeNoCredentials,
		tls:  tlsCfg,
	}, nil
}

// TLSCertPool returns the system CA pool with caCert appended, so a connection can trust a
// private CA while still trusting publicly-signed endpoints (e.g. Temporal Cloud). Returns
// (nil, nil) when caCert is empty, leaving RootCAs unset so Go falls back to the system pool.
func (cp *ClientPool) TLSCertPool(caCert []byte) (*x509.CertPool, error) {
	if len(caCert) == 0 {
		return nil, nil
	}
	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		cp.logger.Warn("Failed to load system CA pool, falling back to empty pool", "error", err)
		rootCAs = x509.NewCertPool()
	}
	if !rootCAs.AppendCertsFromPEM(caCert) {
		return nil, errors.New("failed to parse CA certificate from secret")
	}
	return rootCAs, nil
}

func (cp *ClientPool) parseClientSecret(
	ctx context.Context,
	spec v1alpha1.ConnectionSpec,
	k8sNamespace string,
) (ClientAuth, error) {
	// Fetch the secret from k8s cluster, if it exists. Otherwise, create a connection with the server without using any credentials.
	var secret corev1.Secret
	if spec.SecretName() != "" {
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      spec.SecretName(),
			Namespace: k8sNamespace,
		}, &secret); err != nil {
			return ClientAuth{}, err
		}
	}

	// TLS.CACertSecretRef is applicable when AuthMode is either AuthModeAPIKey or
	// AuthModeNoCredentials. AuthModeTLS ignores it — MutualTLSSecretRef's own ca.crt
	// key already covers that case, and the two are mutually exclusive by CEL validation.
	var caCert []byte
	if caCertSecretName := spec.TLSCACertSecretName(); caCertSecretName != "" {
		var caSecret corev1.Secret
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      caCertSecretName,
			Namespace: k8sNamespace,
		}, &caSecret); err != nil {
			return ClientAuth{}, fmt.Errorf("failed to read CA secret %q: %w", caCertSecretName, err)
		}
		// Unlike MutualTLSSecretRef's ca.crt (a secret whose primary job is tls.crt/tls.key,
		// where a CA is genuinely optional), this field's only purpose is carrying a CA. A
		// missing key here is a misconfiguration, not "no CA requested" — treat it as an
		// error rather than silently falling back to system-trust-only.
		var ok bool
		caCert, ok = caSecret.Data["ca.crt"]
		if !ok || len(caCert) == 0 {
			return ClientAuth{}, fmt.Errorf("CA secret %q referenced by tls.caCertSecretRef has no ca.crt key", caCertSecretName)
		}
	}

	// Check the secret type
	switch spec.AuthMode() {
	case v1alpha1.AuthModeTLS:
		if secret.Type != corev1.SecretTypeTLS && secret.Type != corev1.SecretTypeOpaque {
			return ClientAuth{}, fmt.Errorf("secret %s must be of type kubernetes.io/tls or Opaque", secret.Name)
		}
		return cp.fetchClientUsingMTLSSecret(secret, spec)

	case v1alpha1.AuthModeAPIKey:
		if secret.Type != corev1.SecretTypeOpaque {
			return ClientAuth{}, fmt.Errorf("secret %s must be of type kubernetes.io/opaque", secret.Name)
		}
		return cp.fetchClientUsingAPIKeySecret(spec, k8sNamespace, caCert)

	case v1alpha1.AuthModeNoCredentials:
		return cp.fetchClientUsingNoCredentials(spec, caCert)

	default:
		return ClientAuth{}, fmt.Errorf("invalid auth mode: %s", spec.AuthMode())
	}
}

// healthCheck probes the Temporal server with CheckHealth to fail fast when the
// server is unreachable. It is skipped for API key auth: CheckHealth is a
// system-level RPC, but Temporal Cloud API keys are namespace-scoped and lack
// permission to call it, so the probe would always fail. Skipping is safe
// because client.Dial already performs GetSystemInfo, which is a superset of
// CheckHealth.
func (cp *ClientPool) healthCheck(c sdkclient.Client, auth ClientAuth) error {
	if auth.mode == v1alpha1.AuthModeAPIKey {
		return nil
	}
	if _, err := c.CheckHealth(context.Background(), &sdkclient.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("temporal server health check failed: %w", err)
	}
	return nil
}

func (cp *ClientPool) cacheClient(key ClientPoolKey, c sdkclient.Client, auth ClientAuth) {
	cp.mux.Lock()
	defer cp.mux.Unlock()
	cp.clients[key] = ClientInfo{client: c, auth: auth}
}

// SetClientForTesting pre-populates the pool with a stub client, bypassing the network dial.
// Intended for use in unit tests only.
func (cp *ClientPool) SetClientForTesting(key ClientPoolKey, c sdkclient.Client) {
	cp.cacheClient(key, c, ClientAuth{mode: key.AuthMode})
}

func (cp *ClientPool) Close() {
	cp.mux.Lock()
	defer cp.mux.Unlock()

	for _, c := range cp.clients {
		c.client.Close()
	}

	cp.clients = make(map[ClientPoolKey]ClientInfo)
}

func (cp *ClientPool) fetchAPIKeyFromSecret(ctx context.Context, secretName, k8sNamespace, secretKey string) (string, error) {
	var s corev1.Secret
	if err := cp.k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: k8sNamespace}, &s); err != nil {
		return "", fmt.Errorf("failed to read API key secret %q: %w", secretName, err)
	}
	return string(s.Data[secretKey]), nil
}

func calculateCertificateExpirationTime(certBytes []byte, bufferTime time.Duration) (time.Time, error) {
	if len(certBytes) == 0 {
		return time.Time{}, errors.New("no certificate bytes provided")
	}

	block, _ := pem.Decode(certBytes)
	if block == nil {
		return time.Time{}, errors.New("failed to decode PEM block")
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
