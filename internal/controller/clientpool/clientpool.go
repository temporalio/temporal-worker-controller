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
	HostPort         string
	TLSServerName    string
	Namespace        string            // Temporal namespace
	SecretName       string            // Include secret name in key to invalidate cache when the secret name changes
	CACertSecretName string            // Include CA secret name in key to invalidate cache when TLS.CACertSecretRef changes
	AuthMode         v1alpha1.AuthMode // Include auth mode in key to invalidate cache when the auth mode changes for the secret
}

type MTLSAuth struct {
	tlsConfig  *tls.Config
	expiryTime time.Time // Time we consider the cert expired (NotAfter minus safety buffer)
}

type ClientAuth struct {
	mode v1alpha1.AuthMode
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
	// dialFn establishes a Temporal SDK connection from the given options. In production
	// this is sdkclient.Dial; in tests it can be replaced with a function that returns a
	// mock client without making any network calls.
	dialFn func(sdkclient.Options) (sdkclient.Client, error)
	// systemCertPoolFn loads the host OS certificate pool used as the base when a custom
	// CA is appended via ca.crt. In production this is x509.SystemCertPool; in tests it
	// can be replaced to inject a known set of "system" root CAs without depending on the
	// OS trust store.
	systemCertPoolFn func() (*x509.CertPool, error)
}

func New(l log.Logger, c runtimeclient.Client) *ClientPool {
	return &ClientPool{
		logger:           l,
		clients:          make(map[ClientPoolKey]ClientInfo),
		k8sClient:        c,
		dialFn:           sdkclient.Dial,
		systemCertPoolFn: x509.SystemCertPool,
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

func (cp *ClientPool) GetSDKClient(key ClientPoolKey) (sdkclient.Client, bool) {
	cp.mux.RLock()
	defer cp.mux.RUnlock()

	info, ok := cp.clients[key]
	if !ok {
		return nil, false
	}

	if key.AuthMode == v1alpha1.AuthModeTLS {
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
	Spec              v1alpha1.ConnectionSpec
	Identity          string
}

type namespaceHeadersProvider string

func (p namespaceHeadersProvider) GetHeaders(context.Context) (map[string]string, error) {
	return map[string]string{"temporal-namespace": string(p)}, nil
}

func (cp *ClientPool) newClientOptions(opts NewClientOptions) sdkclient.Options {
	return sdkclient.Options{
		Logger:          cp.logger,
		HostPort:        opts.Spec.HostPort,
		Namespace:       opts.TemporalNamespace,
		Identity:        opts.Identity,
		HeadersProvider: namespaceHeadersProvider(opts.TemporalNamespace),
	}
}

func (cp *ClientPool) fetchClientUsingMTLSSecret(secret corev1.Secret, opts NewClientOptions) (*sdkclient.Options, *ClientPoolKey, *ClientAuth, error) {
	tlsServerName := opts.Spec.TLSServerName()
	clientOpts := cp.newClientOptions(opts)

	var pemCert []byte
	var expiryTime time.Time

	// Extract the certificate to calculate the effective expiration time
	pemCert = secret.Data["tls.crt"]

	// Check if certificate is expired before creating the client
	exp, err := calculateCertificateExpirationTime(pemCert, 5*time.Minute)
	if err != nil {
		return nil, nil, nil, errors.New("failed to check certificate expiration: " + err.Error())
	}
	expired, err := isCertificateExpired(exp)
	if err != nil {
		return nil, nil, nil, errors.New("failed to check certificate expiration: " + err.Error())
	}
	if expired {
		return nil, nil, nil, errors.New("certificate is expired or is going to expire soon")
	}

	cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
	if err != nil {
		return nil, nil, nil, err
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
		rootCAs, err := cp.mergeCACert(caCert)
		if err != nil {
			return nil, nil, nil, err
		}
		tlsCfg.RootCAs = rootCAs
	}
	clientOpts.ConnectionOptions.TLS = tlsCfg
	expiryTime = exp

	key := ClientPoolKey{
		HostPort:      opts.Spec.HostPort,
		TLSServerName: tlsServerName,
		Namespace:     opts.TemporalNamespace,
		SecretName:    opts.Spec.MutualTLSSecretRef.Name,
		// Always empty here: CEL validation forbids combining mutualTLSSecretRef with tls.caCertSecretRef.
		CACertSecretName: "",
		AuthMode:         v1alpha1.AuthModeTLS,
	}
	auth := ClientAuth{
		mode: v1alpha1.AuthModeTLS,
		mTLS: &MTLSAuth{tlsConfig: clientOpts.ConnectionOptions.TLS, expiryTime: expiryTime},
	}
	return &clientOpts, &key, &auth, nil
}

func (cp *ClientPool) fetchClientUsingAPIKeySecret(opts NewClientOptions, caCert []byte) (*sdkclient.Options, *ClientPoolKey, *ClientAuth, error) {
	tlsServerName := opts.Spec.TLSServerName()
	clientOpts := cp.newClientOptions(opts)
	tlsCfg := &tls.Config{ServerName: tlsServerName}
	rootCAs, err := cp.mergeCACert(caCert)
	if err != nil {
		return nil, nil, nil, err
	}
	tlsCfg.RootCAs = rootCAs
	clientOpts.ConnectionOptions.TLS = tlsCfg

	secretName := opts.Spec.APIKeySecretRef.Name
	secretKey := opts.Spec.APIKeySecretRef.Key
	k8sNamespace := opts.K8sNamespace
	clientOpts.Credentials = sdkclient.NewAPIKeyDynamicCredentials(func(ctx context.Context) (string, error) {
		return cp.fetchAPIKeyFromSecret(ctx, secretName, k8sNamespace, secretKey)
	})

	key := ClientPoolKey{
		HostPort:         opts.Spec.HostPort,
		TLSServerName:    tlsServerName,
		Namespace:        opts.TemporalNamespace,
		SecretName:       opts.Spec.APIKeySecretRef.Name,
		CACertSecretName: opts.Spec.CACertSecretName(),
		AuthMode:         v1alpha1.AuthModeAPIKey,
	}
	auth := ClientAuth{
		mode: v1alpha1.AuthModeAPIKey,
		mTLS: nil,
	}

	return &clientOpts, &key, &auth, nil
}

func (cp *ClientPool) fetchClientUsingNoCredentials(opts NewClientOptions, caCert []byte) (*sdkclient.Options, *ClientPoolKey, *ClientAuth, error) {
	tlsServerName := opts.Spec.TLSServerName()
	clientOpts := cp.newClientOptions(opts)
	rootCAs, err := cp.mergeCACert(caCert)
	if err != nil {
		return nil, nil, nil, err
	}
	if tlsServerName != "" || rootCAs != nil {
		clientOpts.ConnectionOptions.TLS = &tls.Config{ServerName: tlsServerName, RootCAs: rootCAs}
	}

	key := ClientPoolKey{
		HostPort:         opts.Spec.HostPort,
		TLSServerName:    tlsServerName,
		Namespace:        opts.TemporalNamespace,
		SecretName:       "",
		CACertSecretName: opts.Spec.CACertSecretName(),
		AuthMode:         v1alpha1.AuthModeNoCredentials,
	}
	auth := ClientAuth{
		mode: v1alpha1.AuthModeNoCredentials,
		mTLS: nil,
	}

	return &clientOpts, &key, &auth, nil
}

// mergeCACert returns the system CA pool with caCert appended, so a connection can trust a
// private CA while still trusting publicly-signed endpoints (e.g. Temporal Cloud). Returns
// (nil, nil) when caCert is empty, leaving RootCAs unset so Go falls back to the system pool.
func (cp *ClientPool) mergeCACert(caCert []byte) (*x509.CertPool, error) {
	if len(caCert) == 0 {
		return nil, nil
	}
	rootCAs, err := cp.systemCertPoolFn()
	if err != nil {
		cp.logger.Warn("Failed to load system CA pool, falling back to empty pool", "error", err)
		rootCAs = x509.NewCertPool()
	}
	if !rootCAs.AppendCertsFromPEM(caCert) {
		return nil, errors.New("failed to parse CA certificate from secret")
	}
	return rootCAs, nil
}

func (cp *ClientPool) ParseClientSecret(
	ctx context.Context,
	secretName string,
	authMode v1alpha1.AuthMode,
	opts NewClientOptions,
) (*sdkclient.Options, *ClientPoolKey, *ClientAuth, error) {
	// Fetch the secret from k8s cluster, if it exists. Otherwise, create a connection with the server without using any credentials.
	var secret corev1.Secret
	if secretName != "" {
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      secretName,
			Namespace: opts.K8sNamespace,
		}, &secret); err != nil {
			return nil, nil, nil, err
		}
	}

	// TLS.CACertSecretRef is independent of AuthMode, so it applies here regardless of
	// which branch below runs. AuthModeTLS ignores it — MutualTLSSecretRef's own ca.crt
	// key already covers that case, and the two are mutually exclusive by CEL validation.
	var caCert []byte
	if caCertSecretName := opts.Spec.CACertSecretName(); caCertSecretName != "" {
		var caSecret corev1.Secret
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      caCertSecretName,
			Namespace: opts.K8sNamespace,
		}, &caSecret); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to read CA secret %q: %w", caCertSecretName, err)
		}
		// Unlike MutualTLSSecretRef's ca.crt (a secret whose primary job is tls.crt/tls.key,
		// where a CA is genuinely optional), this field's only purpose is carrying a CA. A
		// missing key here is a misconfiguration, not "no CA requested" — treat it as an
		// error rather than silently falling back to system-trust-only.
		var ok bool
		caCert, ok = caSecret.Data["ca.crt"]
		if !ok || len(caCert) == 0 {
			return nil, nil, nil, fmt.Errorf("CA secret %q referenced by tls.caCertSecretRef has no ca.crt key", caCertSecretName)
		}
	}

	// Check the secret type
	switch authMode {
	case v1alpha1.AuthModeTLS:
		if secret.Type != corev1.SecretTypeTLS && secret.Type != corev1.SecretTypeOpaque {
			err := fmt.Errorf("secret %s must be of type kubernetes.io/tls or Opaque", secret.Name)
			return nil, nil, nil, err
		}
		return cp.fetchClientUsingMTLSSecret(secret, opts)

	case v1alpha1.AuthModeAPIKey:
		if secret.Type != corev1.SecretTypeOpaque {
			err := fmt.Errorf("secret %s must be of type kubernetes.io/opaque", secret.Name)
			return nil, nil, nil, err
		}
		return cp.fetchClientUsingAPIKeySecret(opts, caCert)

	case v1alpha1.AuthModeNoCredentials:
		return cp.fetchClientUsingNoCredentials(opts, caCert)

	default:
		return nil, nil, nil, fmt.Errorf("invalid auth mode: %s", authMode)
	}
}

func (cp *ClientPool) DialAndUpsertClient(clientOpts sdkclient.Options, clientPoolKey ClientPoolKey, clientAuth ClientAuth) (sdkclient.Client, error) {
	c, err := cp.dialFn(clientOpts)
	if err != nil {
		return nil, err
	}

	// Skip health check for API key auth — CheckHealth is a system-level
	// (non-namespace-scoped) RPC that fails with namespace-scoped API keys
	// on Temporal Cloud. This is safe because client.Dial already calls
	// GetSystemInfo internally, which is a superset of CheckHealth.
	if clientAuth.mode != v1alpha1.AuthModeAPIKey {
		if _, err := c.CheckHealth(context.Background(), &sdkclient.CheckHealthRequest{}); err != nil {
			c.Close()
			return nil, fmt.Errorf("temporal server health check failed: %w", err)
		}
	}

	cp.mux.Lock()
	defer cp.mux.Unlock()

	cp.clients[clientPoolKey] = ClientInfo{
		client: c,
		auth:   clientAuth,
	}
	return c, nil
}

// SetClientForTesting pre-populates the pool with a stub client, bypassing the network dial.
// Intended for use in unit tests only.
func (cp *ClientPool) SetClientForTesting(key ClientPoolKey, c sdkclient.Client) {
	cp.mux.Lock()
	defer cp.mux.Unlock()
	cp.clients[key] = ClientInfo{client: c, auth: ClientAuth{mode: key.AuthMode}}
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
