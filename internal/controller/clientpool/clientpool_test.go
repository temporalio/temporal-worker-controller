// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package clientpool

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	sdkclient "go.temporal.io/sdk/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

type noopLogger struct{}

func (noopLogger) Debug(string, ...interface{}) {}
func (noopLogger) Info(string, ...interface{})  {}
func (noopLogger) Warn(string, ...interface{})  {}
func (noopLogger) Error(string, ...interface{}) {}

func newTestPool() *ClientPool {
	return &ClientPool{
		logger:           noopLogger{},
		clients:          make(map[ClientPoolKey]ClientInfo),
		dialFn:           sdkclient.Dial,
		systemCertPoolFn: x509.SystemCertPool,
	}
}

// generateSelfSignedCACert creates a self-signed CA cert and returns the parsed cert, private key, and PEM bytes.
func generateSelfSignedCACert(t *testing.T, notBefore, notAfter time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, key, certPEM
}

// generateLeafCert creates a leaf cert signed by the given CA, suitable for use as a client cert.
// Returns the parsed cert, cert PEM, and key PEM.
func generateLeafCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, dnsName string, notBefore, notAfter time.Time) (cert *x509.Certificate, certPEM []byte, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	require.NoError(t, err)
	cert, err = x509.ParseCertificate(certDER)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return cert, certPEM, keyPEM
}

func makeOpts(hostPort string) NewClientOptions {
	return NewClientOptions{
		TemporalNamespace: "default",
		K8sNamespace:      "test-ns",
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort: hostPort,
			MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{
				Name: "test-tls-secret",
			},
		},
	}
}

func makeTLSSecret(certPEM, keyPEM, caPEM []byte) corev1.Secret {
	data := map[string][]byte{
		"tls.crt": certPEM,
		"tls.key": keyPEM,
	}
	if caPEM != nil {
		data["ca.crt"] = caPEM
	}
	return corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-tls-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeTLS,
		Data:       data,
	}
}

// ─── mockSDKClient ────────────────────────────────────────────────────────────

// mockSDKClient embeds sdkclient.Client so only overridden methods need to be implemented.
// Any unimplemented method panics with a nil-pointer dereference at runtime.
type mockSDKClient struct {
	sdkclient.Client

	// Used to verify that CheckHealth is not called when API Key auth is used.
	checkHealthCalled bool
	closed            bool
}

func (m *mockSDKClient) CheckHealth(_ context.Context, _ *sdkclient.CheckHealthRequest) (*sdkclient.CheckHealthResponse, error) {
	m.checkHealthCalled = true
	return &sdkclient.CheckHealthResponse{}, nil
}

func (m *mockSDKClient) Close() { m.closed = true }

// ─── Tests: fetchClientUsingMTLSSecret ────────────────────────────────────────

// TestFetchMTLS_NoCACert_RootCAsNil verifies that when no ca.crt is present in the TLS
// secret, RootCAs is left nil so Go's TLS stack falls back to the system CA bundle.
func TestFetchMTLS_NoCACert_RootCAsNil(t *testing.T) {
	now := time.Now()
	caCert, caKey, _ := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, certPEM, keyPEM := generateLeafCert(t, caCert, caKey, "test.example.com", now.Add(-time.Hour), now.Add(time.Hour))

	cp := newTestPool()
	secret := makeTLSSecret(certPEM, keyPEM, nil) // no ca.crt
	opts, _, _, err := cp.fetchClientUsingMTLSSecret(secret, makeOpts("localhost:7233"))

	require.NoError(t, err)
	assert.Nil(t, opts.ConnectionOptions.TLS.RootCAs, "RootCAs must be nil when no ca.crt is provided so Go uses the system CA bundle")
}

// TestFetchMTLS_CACertAppendsToSystemPool is the regression test for PR #227.
//
// Before the fix, fetchClientUsingMTLSSecret used x509.NewCertPool() (empty pool) and then
// appended the custom CA. This broke connections to publicly-signed servers (e.g. Temporal
// Cloud) because system root CAs were discarded.
//
// After the fix it calls systemCertPoolFn() first and then appends, so both the injected
// "system" CAs and the custom CA are present in the returned pool.
func TestFetchMTLS_CACertAppendsToSystemPool(t *testing.T) {
	now := time.Now()

	// "System" CA — simulates a root CA that would normally live in the OS trust store.
	sysCACert, sysCAPKey, sysCAPEM := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, sysLeafPEM, _ := generateLeafCert(t, sysCACert, sysCAPKey, "system.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	sysLeafCert, err := decodePEMCert(sysLeafPEM)
	require.NoError(t, err)

	// Custom CA — the ca.crt stored in the k8s TLS secret.
	customCACert, customCAKey, customCAPEM := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, customLeafPEM, _ := generateLeafCert(t, customCACert, customCAKey, "custom.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	customLeafCert, err := decodePEMCert(customLeafPEM)
	require.NoError(t, err)

	// Client cert (signed by custom CA — used as the mTLS identity, not for pool verification).
	_, clientCertPEM, clientKeyPEM := generateLeafCert(t, customCACert, customCAKey, "client.example.com", now.Add(-time.Hour), now.Add(time.Hour))

	cp := newTestPool()
	// Inject a fake "system" pool containing only sysCACert.
	cp.systemCertPoolFn = func() (*x509.CertPool, error) {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(sysCAPEM)
		return pool, nil
	}

	secret := makeTLSSecret(clientCertPEM, clientKeyPEM, customCAPEM)
	opts, _, _, err := cp.fetchClientUsingMTLSSecret(secret, makeOpts("localhost:7233"))
	require.NoError(t, err)

	pool := opts.ConnectionOptions.TLS.RootCAs
	require.NotNil(t, pool, "RootCAs must be set when ca.crt is provided")

	// Both the injected "system" CA and the custom CA must be trusted.
	// If the bug (empty pool) were reintroduced, the system leaf verification would fail.
	_, err = sysLeafCert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, DNSName: "system.example.com"})
	assert.NoError(t, err, "system CA should be in pool (regression: PR #227 used NewCertPool(), dropping system CAs)")

	_, err = customLeafCert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, DNSName: "custom.example.com"})
	assert.NoError(t, err, "custom CA should be in pool")
}

// TestFetchMTLS_ExpiredCert_ReturnsError verifies that an expired client cert is rejected.
func TestFetchMTLS_ExpiredCert_ReturnsError(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	caCert, caKey, _ := generateSelfSignedCACert(t, past.Add(-time.Hour), past)
	_, certPEM, keyPEM := generateLeafCert(t, caCert, caKey, "test.example.com", past.Add(-time.Hour), past)

	cp := newTestPool()
	secret := makeTLSSecret(certPEM, keyPEM, nil)
	_, _, _, err := cp.fetchClientUsingMTLSSecret(secret, makeOpts("localhost:7233"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

// TestFetchMTLS_ValidCert_Succeeds is a smoke test for the happy path.
func TestFetchMTLS_ValidCert_Succeeds(t *testing.T) {
	now := time.Now()
	caCert, caKey, _ := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, certPEM, keyPEM := generateLeafCert(t, caCert, caKey, "test.example.com", now.Add(-time.Hour), now.Add(time.Hour))

	cp := newTestPool()
	secret := makeTLSSecret(certPEM, keyPEM, nil)
	clientOpts, key, auth, err := cp.fetchClientUsingMTLSSecret(secret, makeOpts("localhost:7233"))

	require.NoError(t, err)
	assert.Equal(t, AuthModeTLS, key.AuthMode)
	assert.Equal(t, AuthModeTLS, auth.mode)
	assert.NotNil(t, auth.mTLS)
	assert.NotNil(t, clientOpts.ConnectionOptions.TLS)
	assert.Len(t, clientOpts.ConnectionOptions.TLS.Certificates, 1)
}

func newTestPoolWithFakeClient(objects ...runtime.Object) *ClientPool {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	return &ClientPool{
		logger:           noopLogger{},
		clients:          make(map[ClientPoolKey]ClientInfo),
		k8sClient:        k8sClient,
		dialFn:           sdkclient.Dial,
		systemCertPoolFn: x509.SystemCertPool,
	}
}

// ─── Tests: fetchClientUsingAPIKeySecret ──────────────────────────────────────

// TestFetchAPIKey_CredentialsAndTLSSet verifies that API key auth sets credentials and
// an empty (non-nil) TLS config, which gRPC requires for TLS transport even with token auth.
func TestFetchAPIKey_CredentialsAndTLSSet(t *testing.T) {
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"apikey": []byte("test-api-key-value")},
	}
	cp := newTestPoolWithFakeClient(&secret)
	apiKeySelector := &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
		Key:                  "apikey",
	}
	opts := NewClientOptions{
		TemporalNamespace: "default",
		K8sNamespace:      "test-ns",
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
			HostPort:        "localhost:7233",
			APIKeySecretRef: apiKeySelector,
		},
	}

	clientOpts, key, auth, err := cp.fetchClientUsingAPIKeySecret(opts)

	require.NoError(t, err)
	assert.Equal(t, AuthModeAPIKey, key.AuthMode)
	assert.Equal(t, AuthModeAPIKey, auth.mode)
	assert.Nil(t, auth.mTLS)
	assert.NotNil(t, clientOpts.Credentials, "API key credentials must be set")
	assert.NotNil(t, clientOpts.ConnectionOptions.TLS, "TLS config must be non-nil for gRPC API key transport")
}

// TestFetchAPIKey_CredentialClosureReadsLiveSecret verifies that fetchAPIKeyFromSecret
// reads from the K8s secret at call time, picking up rotated keys without a client re-dial.
// The credentials closure delegates to fetchAPIKeyFromSecret, so this covers the end-to-end path.
func TestFetchAPIKey_CredentialClosureReadsLiveSecret(t *testing.T) {
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"apikey": []byte("original-key")},
	}
	cp := newTestPoolWithFakeClient(&secret)

	token, err := cp.fetchAPIKeyFromSecret(context.Background(), "api-key-secret", "test-ns", "apikey")
	require.NoError(t, err)
	assert.Equal(t, "original-key", token)

	// Simulate key rotation by updating the secret in the fake client.
	secret.Data["apikey"] = []byte("rotated-key")
	require.NoError(t, cp.k8sClient.Update(context.Background(), &secret))

	// Next call must return the rotated key without any client eviction or re-dial.
	token, err = cp.fetchAPIKeyFromSecret(context.Background(), "api-key-secret", "test-ns", "apikey")
	require.NoError(t, err)
	assert.Equal(t, "rotated-key", token)
}

// ─── Tests: ParseClientSecret ─────────────────────────────────────────────────

// TestParseClientSecret_OpaqueSecretType verifies that an Opaque secret containing tls.crt
// and tls.key is accepted for mTLS auth. This is the regression test for the fix that
// relaxed the type check in ParseClientSecret to accept both kubernetes.io/tls and Opaque.
func TestParseClientSecret_OpaqueSecretType(t *testing.T) {
	now := time.Now()
	caCert, caKey, _ := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, certPEM, keyPEM := generateLeafCert(t, caCert, caKey, "test.example.com", now.Add(-time.Hour), now.Add(time.Hour))

	opaqueSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tls-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"tls.crt": certPEM, "tls.key": keyPEM},
	}

	cp := newTestPool()
	_, key, auth, err := cp.fetchClientUsingMTLSSecret(opaqueSecret, makeOpts("localhost:7233"))

	require.NoError(t, err, "Opaque secret with tls.crt and tls.key should be accepted for mTLS auth")
	assert.Equal(t, AuthModeTLS, key.AuthMode)
	assert.Equal(t, AuthModeTLS, auth.mode)
	assert.NotNil(t, auth.mTLS)
}

// ─── Tests: DialAndUpsertClient ───────────────────────────────────────────────

// TestDialAndUpsert_APIKeySkipsCheckHealth is the regression test for PR #232.
//
// Before the fix, DialAndUpsertClient called c.CheckHealth() unconditionally. On Temporal
// Cloud, namespace-scoped API keys do not have permission to call the system-scoped
// CheckHealth RPC, so every connection attempt failed.
//
// After the fix, CheckHealth is skipped for AuthModeAPIKey because client.Dial already
// calls GetSystemInfo internally (a superset of CheckHealth).
func TestDialAndUpsert_APIKeySkipsCheckHealth(t *testing.T) {
	mock := &mockSDKClient{}
	cp := newTestPool()
	cp.dialFn = func(_ sdkclient.Options) (sdkclient.Client, error) { return mock, nil }

	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: AuthModeAPIKey}
	auth := ClientAuth{mode: AuthModeAPIKey}

	c, err := cp.DialAndUpsertClient(sdkclient.Options{}, key, auth)

	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.False(t, mock.checkHealthCalled,
		"CheckHealth must NOT be called for API key auth (regression: PR #232 — fails on Temporal Cloud with namespace-scoped keys)")
}

// TestDialAndUpsert_TLSCallsCheckHealth verifies that CheckHealth IS called for TLS auth,
// providing an early connectivity check when API key restrictions don't apply.
func TestDialAndUpsert_TLSCallsCheckHealth(t *testing.T) {
	mock := &mockSDKClient{}
	cp := newTestPool()
	cp.dialFn = func(_ sdkclient.Options) (sdkclient.Client, error) { return mock, nil }

	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: AuthModeTLS}
	auth := ClientAuth{
		mode: AuthModeTLS,
		mTLS: &MTLSAuth{tlsConfig: &tls.Config{}, expiryTime: time.Now().Add(time.Hour)},
	}

	c, err := cp.DialAndUpsertClient(sdkclient.Options{}, key, auth)

	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.True(t, mock.checkHealthCalled, "CheckHealth must be called for TLS auth")
}

// TestDialAndUpsert_NoCredsCallsCheckHealth verifies that CheckHealth IS called for no-credentials mode.
func TestDialAndUpsert_NoCredsCallsCheckHealth(t *testing.T) {
	mock := &mockSDKClient{}
	cp := newTestPool()
	cp.dialFn = func(_ sdkclient.Options) (sdkclient.Client, error) { return mock, nil }

	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: AuthModeNoCredentials}
	auth := ClientAuth{mode: AuthModeNoCredentials}

	c, err := cp.DialAndUpsertClient(sdkclient.Options{}, key, auth)

	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.True(t, mock.checkHealthCalled, "CheckHealth must be called for no-credentials auth")
}

// ─── Tests: EvictClient ───────────────────────────────────────────────────────

func TestEvictClient_RemovesAndClosesClient(t *testing.T) {
	cp := newTestPool()
	key := ClientPoolKey{
		HostPort:   "localhost:7233",
		Namespace:  "default",
		SecretName: "my-secret",
		AuthMode:   AuthModeAPIKey,
	}
	mock := &mockSDKClient{}
	cp.SetClientForTesting(key, mock)

	_, ok := cp.GetSDKClient(key)
	require.True(t, ok, "client should be present before eviction")

	cp.EvictClient(key)

	assert.True(t, mock.closed, "Close should be called on eviction")
	_, ok = cp.GetSDKClient(key)
	assert.False(t, ok, "client should be absent after eviction")
}

func TestEvictClient_NoopWhenKeyAbsent(t *testing.T) {
	cp := newTestPool()
	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: AuthModeNoCredentials}
	// Should not panic when key is not in the pool
	cp.EvictClient(key)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func decodePEMCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	return x509.ParseCertificate(block.Bytes)
}
