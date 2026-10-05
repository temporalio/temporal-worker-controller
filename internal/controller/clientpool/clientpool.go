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
	SecretName          string            // invalidate cache when the secret name changes
	TLSCACertSecretName string            // invalidate cache when TLS.CACertSecretRef changes
	AuthMode            v1alpha1.AuthMode // invalidate cache when the auth mode changes
}

// CachedClient is a Temporal SDK client paired with a validity check. The pool calls IsValid on every
// cache hit; a false result triggers eviction and re-dial. It is not called at creation time —
// creation success is the error return of CreateClientFunc.
type CachedClient struct {
	Client  sdkclient.Client
	IsValid func() bool
}

// clientAuth carries parsed auth material for the default creation path. It is not cached; the expiry
// is captured in the CachedClient.IsValid closure instead.
type clientAuth struct {
	mode        v1alpha1.AuthMode
	tls         *tls.Config
	credentials sdkclient.Credentials // non-nil only for API key auth
	expiryTime  time.Time             // mTLS only: NotAfter minus safety buffer
}

// CreateClientFunc creates a Temporal SDK client for a connection spec: secret parsing, options, dialing,
// and health check. The pool caches the returned CachedClient.
type CreateClientFunc func(
	ctx context.Context,
	spec v1alpha1.ConnectionSpec,
	temporalNamespace, k8sNamespace, identity string,
) (CachedClient, error)

type ClientPool struct {
	mux       sync.RWMutex
	logger    log.Logger
	clients   map[ClientPoolKey]CachedClient
	k8sClient runtimeclient.Client
	dialFn    func(sdkclient.Options) (sdkclient.Client, error) // sdkclient.Dial in production; stubbable in tests

	// CustomizeClientOptions mutates SDK options before dialing. Consumed by
	// DefaultCreateClient; a wrapper using a custom CreateClientFn owns
	// construction entirely and does not use this hook.
	CustomizeClientOptions func(sdkclient.Options) sdkclient.Options

	// CreateClientFn creates and health-checks a client. Defaults to
	// DefaultCreateClient; a wrapper overrides it to own construction.
	CreateClientFn CreateClientFunc
}

type AuthConfigError struct{ Err error }

func (e *AuthConfigError) Error() string { return e.Err.Error() }
func (e *AuthConfigError) Unwrap() error { return e.Err }

type DialError struct{ Err error }

func (e *DialError) Error() string { return e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

func New(l log.Logger, c runtimeclient.Client, customizeOptions func(sdkclient.Options) sdkclient.Options) *ClientPool {
	cp := &ClientPool{
		logger:                 l,
		clients:                make(map[ClientPoolKey]CachedClient),
		k8sClient:              c,
		dialFn:                 sdkclient.Dial,
		CustomizeClientOptions: customizeOptions,
	}
	cp.CreateClientFn = cp.DefaultCreateClient
	return cp
}

// EvictClient removes and closes the client for key. No-op if absent.
func (cp *ClientPool) EvictClient(key ClientPoolKey) {
	cp.mux.Lock()
	defer cp.mux.Unlock()
	if cc, ok := cp.clients[key]; ok {
		cc.Client.Close()
		delete(cp.clients, key)
	}
}

// GetClient returns a cached or newly created Temporal client for the connection spec. The pool
// caches the result; on a cache hit, IsValid decides whether the cached client is still usable.
func (cp *ClientPool) GetClient(
	ctx context.Context,
	spec v1alpha1.ConnectionSpec,
	temporalNamespace, k8sNamespace, identity string,
) (sdkclient.Client, ClientPoolKey, error) {
	if err := spec.Validate(); err != nil {
		return nil, ClientPoolKey{}, &AuthConfigError{Err: err}
	}
	key := cp.createKey(spec, temporalNamespace)
	if cc, ok := cp.getClientByKey(key); ok && cc.IsValid() {
		return cc.Client, key, nil
	}
	cc, err := cp.CreateClientFn(ctx, spec, temporalNamespace, k8sNamespace, identity)
	if err != nil {
		return nil, ClientPoolKey{}, err
	}
	cp.cacheClient(key, cc)
	return cc.Client, key, nil
}

// DefaultCreateClient is the built-in creation path: parse the referenced Secret, build SDK
// options, dial, health-check, and return a CachedClient whose IsValid checks mTLS
// cert expiry. It reads CustomizeClientOptions from the pool struct; a wrapper
// using a custom CreateClientFn owns construction entirely.
func (cp *ClientPool) DefaultCreateClient(
	ctx context.Context,
	spec v1alpha1.ConnectionSpec,
	temporalNamespace, k8sNamespace, identity string,
) (CachedClient, error) {
	auth, err := cp.parseClientSecret(ctx, spec, k8sNamespace)
	if err != nil {
		return CachedClient{}, &AuthConfigError{Err: err}
	}
	clientOpts := cp.getClientOptions(spec, temporalNamespace, identity, auth)
	if cp.CustomizeClientOptions != nil {
		clientOpts = cp.CustomizeClientOptions(clientOpts)
	}
	client, err := cp.dialFn(clientOpts)
	if err != nil {
		return CachedClient{}, &DialError{Err: err}
	}
	if err := cp.healthCheck(client, auth); err != nil {
		client.Close()
		return CachedClient{}, &DialError{Err: err}
	}
	return CachedClient{
		Client:  client,
		IsValid: cp.defaultValidityCheck(auth),
	}, nil
}

// defaultValidityCheck returns the IsValid closure for a cached client. For mTLS it checks cert expiry on
// every call; otherwise the client is always considered valid.
func (cp *ClientPool) defaultValidityCheck(auth clientAuth) func() bool {
	if auth.mode != v1alpha1.AuthModeTLS {
		return func() bool { return true }
	}
	return func() bool {
		expired := isCertificateExpired(auth.expiryTime)
		if expired {
			cp.logger.Warn("Certificate is expired or is going to expire soon")
			return false
		}
		return true
	}
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

func (cp *ClientPool) getClientByKey(key ClientPoolKey) (CachedClient, bool) {
	cp.mux.RLock()
	defer cp.mux.RUnlock()
	cc, ok := cp.clients[key]
	if !ok {
		return CachedClient{}, false
	}
	return cc, true
}

// Clients returns a snapshot of the cached clients.
func (cp *ClientPool) Clients() map[ClientPoolKey]sdkclient.Client {
	cp.mux.RLock()
	defer cp.mux.RUnlock()
	out := make(map[ClientPoolKey]sdkclient.Client, len(cp.clients))
	for k, cc := range cp.clients {
		out[k] = cc.Client
	}
	return out
}

type namespaceHeadersProvider string

func (p namespaceHeadersProvider) GetHeaders(context.Context) (map[string]string, error) {
	return map[string]string{"temporal-namespace": string(p)}, nil
}

func (cp *ClientPool) getClientOptions(spec v1alpha1.ConnectionSpec, temporalNamespace, identity string, auth clientAuth) sdkclient.Options {
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

func (cp *ClientPool) fetchClientUsingMTLSSecret(secret corev1.Secret, spec v1alpha1.ConnectionSpec) (clientAuth, error) {
	tlsServerName := spec.TLSServerName()

	pemCert := secret.Data["tls.crt"]
	exp, err := calculateCertificateExpirationTime(pemCert, 5*time.Minute)
	if err != nil {
		return clientAuth{}, errors.New("failed to check certificate expiration: " + err.Error())
	}
	expired := isCertificateExpired(exp)
	if expired {
		return clientAuth{}, errors.New("certificate is expired or is going to expire soon")
	}

	cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		return clientAuth{}, err
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ServerName:   tlsServerName,
	}
	// Append the secret's CA to the system pool so privately-signed servers are trusted alongside public
	// CAs. When ca.crt is absent, RootCAs stays unset and Go uses the system pool.
	if caCert, ok := secret.Data["ca.crt"]; ok && len(caCert) > 0 {
		rootCAs, err := cp.TLSCertPool(caCert)
		if err != nil {
			return clientAuth{}, err
		}
		tlsCfg.RootCAs = rootCAs
	}

	return clientAuth{
		mode:       v1alpha1.AuthModeTLS,
		tls:        tlsCfg,
		expiryTime: exp,
	}, nil
}

func (cp *ClientPool) fetchClientUsingAPIKeySecret(spec v1alpha1.ConnectionSpec, k8sNamespace string, caCert []byte) (clientAuth, error) {
	tlsServerName := spec.TLSServerName()
	tlsCfg := &tls.Config{ServerName: tlsServerName}
	rootCAs, err := cp.TLSCertPool(caCert)
	if err != nil {
		return clientAuth{}, err
	}
	tlsCfg.RootCAs = rootCAs

	secretName := spec.APIKeySecretRef.Name
	secretKey := spec.APIKeySecretRef.Key
	credentials := sdkclient.NewAPIKeyDynamicCredentials(func(ctx context.Context) (string, error) {
		return cp.fetchAPIKeyFromSecret(ctx, secretName, k8sNamespace, secretKey)
	})

	return clientAuth{
		mode:        v1alpha1.AuthModeAPIKey,
		tls:         tlsCfg,
		credentials: credentials,
	}, nil
}

func (cp *ClientPool) fetchClientUsingNoCredentials(spec v1alpha1.ConnectionSpec, caCert []byte) (clientAuth, error) {
	tlsServerName := spec.TLSServerName()
	rootCAs, err := cp.TLSCertPool(caCert)
	if err != nil {
		return clientAuth{}, err
	}
	var tlsCfg *tls.Config
	if tlsServerName != "" || rootCAs != nil {
		tlsCfg = &tls.Config{ServerName: tlsServerName, RootCAs: rootCAs}
	}

	return clientAuth{
		mode: v1alpha1.AuthModeNoCredentials,
		tls:  tlsCfg,
	}, nil
}

// TLSCertPool returns the system CA pool with caCert appended. Returns (nil, nil) when caCert
// is empty, leaving RootCAs unset so Go falls back to the system pool.
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

// parseClientSecret fetches the referenced Secret, resolves the CA cert, and returns auth material for the
// connection's auth mode.
func (cp *ClientPool) parseClientSecret(
	ctx context.Context,
	spec v1alpha1.ConnectionSpec,
	k8sNamespace string,
) (clientAuth, error) {
	var secret corev1.Secret
	if spec.SecretName() != "" {
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      spec.SecretName(),
			Namespace: k8sNamespace,
		}, &secret); err != nil {
			return clientAuth{}, err
		}
	}

	// TLS.CACertSecretRef applies to API_KEY and NO_CREDENTIALS only; AuthModeTLS ignores it (its own
	// ca.crt covers it), and the two are mutually exclusive by CEL validation.
	var caCert []byte
	if caCertSecretName := spec.TLSCACertSecretName(); caCertSecretName != "" {
		var caSecret corev1.Secret
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      caCertSecretName,
			Namespace: k8sNamespace,
		}, &caSecret); err != nil {
			return clientAuth{}, fmt.Errorf("failed to read CA secret %q: %w", caCertSecretName, err)
		}
		// This field's only purpose is carrying a CA, so a missing ca.crt key is a misconfiguration, not
		// "no CA requested".
		var ok bool
		caCert, ok = caSecret.Data["ca.crt"]
		if !ok || len(caCert) == 0 {
			return clientAuth{}, fmt.Errorf("CA secret %q referenced by tls.caCertSecretRef has no ca.crt key", caCertSecretName)
		}
	}

	switch spec.AuthMode() {
	case v1alpha1.AuthModeTLS:
		if secret.Type != corev1.SecretTypeTLS && secret.Type != corev1.SecretTypeOpaque {
			return clientAuth{}, fmt.Errorf("secret %s must be of type kubernetes.io/tls or Opaque", secret.Name)
		}
		return cp.fetchClientUsingMTLSSecret(secret, spec)

	case v1alpha1.AuthModeAPIKey:
		if secret.Type != corev1.SecretTypeOpaque {
			return clientAuth{}, fmt.Errorf("secret %s must be of type kubernetes.io/opaque", secret.Name)
		}
		return cp.fetchClientUsingAPIKeySecret(spec, k8sNamespace, caCert)

	case v1alpha1.AuthModeNoCredentials:
		return cp.fetchClientUsingNoCredentials(spec, caCert)

	default:
		return clientAuth{}, fmt.Errorf("invalid auth mode: %s", spec.AuthMode())
	}
}

// healthCheck probes the client for readiness. Skipped for API key auth (namespace-scoped credentials
// can't call this system-level RPC); safe because client.Dial already calls GetSystemInfo, a
// superset of CheckHealth.
func (cp *ClientPool) healthCheck(c sdkclient.Client, auth clientAuth) error {
	if auth.mode == v1alpha1.AuthModeAPIKey {
		return nil
	}
	if _, err := c.CheckHealth(context.Background(), &sdkclient.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("temporal server health check failed: %w", err)
	}
	return nil
}

func (cp *ClientPool) cacheClient(key ClientPoolKey, cc CachedClient) {
	cp.mux.Lock()
	defer cp.mux.Unlock()
	cp.clients[key] = cc
}

// SetClientForTesting caches a stub client, bypassing the dial. Test-only.
func (cp *ClientPool) SetClientForTesting(key ClientPoolKey, c sdkclient.Client) {
	cp.cacheClient(key, CachedClient{Client: c, IsValid: func() bool { return true }})
}

func (cp *ClientPool) Close() {
	cp.mux.Lock()
	defer cp.mux.Unlock()

	for _, cc := range cp.clients {
		cc.Client.Close()
	}

	cp.clients = make(map[ClientPoolKey]CachedClient)
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

	return cert.NotAfter.Add(-bufferTime), nil
}

func isCertificateExpired(expiryTime time.Time) bool {
	return time.Now().After(expiryTime)
}
