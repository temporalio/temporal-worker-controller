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
	"errors"
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
	cp := New(noopLogger{}, nil, nil)
	cp.dialFn = sdkclient.Dial
	return cp
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

func makeMTLSSpec(hostPort string) temporaliov1alpha1.ConnectionSpec {
	return temporaliov1alpha1.ConnectionSpec{
		HostPort: hostPort,
		MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{
			Name: "test-tls-secret",
		},
	}
}

func TestNewClientOptionsSetsTemporalNamespaceHeader(t *testing.T) {
	clientOpts := newTestPool().getClientOptions(makeMTLSSpec("localhost:7233"), "routing-namespace", "identity", clientAuth{})

	headers, err := clientOpts.HeadersProvider.GetHeaders(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"temporal-namespace": "routing-namespace",
	}, headers)
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
	a, err := cp.fetchClientUsingMTLSSecret(secret, makeMTLSSpec("localhost:7233"))

	require.NoError(t, err)
	assert.Nil(t, a.tls.RootCAs, "RootCAs must be nil when no ca.crt is provided so Go uses the system CA bundle")
}

// TestFetchMTLS_CACertAppendsToSystemPool is the regression test for PR #227.
//
// Before the fix, fetchClientUsingMTLSSecret used x509.NewCertPool() (empty pool) and then
// appended the custom CA. This broke connections to publicly-signed servers (e.g. Temporal
// Cloud) because system root CAs were discarded.
//
// After the fix it calls x509.SystemCertPool() first and then appends, so both the system
// CAs and the custom CA are present in the returned pool.
//
// Note: we can no longer inject a fake system pool to assert system CAs are preserved
// alongside the custom CA (that would require a test seam on the system pool loader).
// We assert the deterministic part — the custom CA is trusted after appending — and
// rely on the production code calling x509.SystemCertPool() to preserve system CAs.
func TestFetchMTLS_CACertAppendsToSystemPool(t *testing.T) {
	now := time.Now()

	// Custom CA — the ca.crt stored in the k8s TLS secret.
	customCACert, customCAKey, customCAPEM := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, customLeafPEM, _ := generateLeafCert(t, customCACert, customCAKey, "custom.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	customLeafCert, err := decodePEMCert(customLeafPEM)
	require.NoError(t, err)

	// Client cert (signed by custom CA — used as the mTLS identity, not for pool verification).
	_, clientCertPEM, clientKeyPEM := generateLeafCert(t, customCACert, customCAKey, "client.example.com", now.Add(-time.Hour), now.Add(time.Hour))

	cp := newTestPool()

	secret := makeTLSSecret(clientCertPEM, clientKeyPEM, customCAPEM)
	a, err := cp.fetchClientUsingMTLSSecret(secret, makeMTLSSpec("localhost:7233"))
	require.NoError(t, err)

	pool := a.tls.RootCAs
	require.NotNil(t, pool, "RootCAs must be set when ca.crt is provided")

	// The custom CA must be trusted after being appended to the system pool.
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
	_, err := cp.fetchClientUsingMTLSSecret(secret, makeMTLSSpec("localhost:7233"))

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
	a, err := cp.fetchClientUsingMTLSSecret(secret, makeMTLSSpec("localhost:7233"))

	require.NoError(t, err)
	assert.Equal(t, temporaliov1alpha1.AuthModeTLS, a.mode)
	assert.NotNil(t, a.expiryTime)
	require.NotNil(t, a.tls)
	assert.Len(t, a.tls.Certificates, 1)
}

func TestFetchMTLS_TLSServerNameOverride(t *testing.T) {
	now := time.Now()
	caCert, caKey, _ := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, certPEM, keyPEM := generateLeafCert(t, caCert, caKey, "test.example.com", now.Add(-time.Hour), now.Add(time.Hour))

	cp := newTestPool()
	secret := makeTLSSecret(certPEM, keyPEM, nil)
	opts := makeMTLSSpec("temporal-nlb.example.com:443")
	opts.TLS = &temporaliov1alpha1.ConnectionTLSConfig{
		ServerName: "temporal-cloud.example.com",
	}

	a, err := cp.fetchClientUsingMTLSSecret(secret, opts)

	require.NoError(t, err)
	require.NotNil(t, a.tls)
	assert.Equal(t, "temporal-cloud.example.com", a.tls.ServerName)
}

func TestFetchAPIKey_TLSServerNameOverride(t *testing.T) {
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
	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort: "temporal-nlb.example.com:443",
		TLS: &temporaliov1alpha1.ConnectionTLSConfig{
			ServerName: "temporal-cloud.example.com",
		},
		APIKeySecretRef: apiKeySelector,
	}

	a, err := cp.fetchClientUsingAPIKeySecret(opts, "test-ns", nil)

	require.NoError(t, err)
	require.NotNil(t, a.tls)
	assert.Equal(t, "temporal-cloud.example.com", a.tls.ServerName)
}

func TestFetchNoCredentials_TLSServerNameOverride(t *testing.T) {
	cp := newTestPool()
	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort: "temporal-nlb.example.com:443",
		TLS: &temporaliov1alpha1.ConnectionTLSConfig{
			ServerName: "temporal-cloud.example.com",
		},
	}

	a, err := cp.fetchClientUsingNoCredentials(opts, nil)

	require.NoError(t, err)
	require.NotNil(t, a.tls)
	assert.Equal(t, "temporal-cloud.example.com", a.tls.ServerName)
	assert.Equal(t, temporaliov1alpha1.AuthModeNoCredentials, a.mode)
}

func newTestPoolWithFakeClient(objects ...runtime.Object) *ClientPool {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	cp := New(noopLogger{}, k8sClient, nil)
	cp.dialFn = sdkclient.Dial
	return cp
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
	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort:        "localhost:7233",
		APIKeySecretRef: apiKeySelector,
	}

	a, err := cp.fetchClientUsingAPIKeySecret(opts, "test-ns", nil)

	require.NoError(t, err)
	assert.Equal(t, temporaliov1alpha1.AuthModeAPIKey, a.mode)
	assert.True(t, a.expiryTime.IsZero())
	assert.NotNil(t, a.credentials, "API key credentials must be set")
	require.NotNil(t, a.tls, "TLS config must be non-nil for gRPC API key transport")
}

// TestFetchAPIKey_CACertAppendsToSystemPool verifies that TLS.CACertSecretRef, resolved by
// the caller into a caCert argument, is appended to the system CA pool for API-key auth —
// the same additive behavior TestFetchMTLS_CACertAppendsToSystemPool covers for mTLS auth
// (PR #227). See that test for why the system-CA-preserved assertion was dropped.
func TestFetchAPIKey_CACertAppendsToSystemPool(t *testing.T) {
	now := time.Now()

	customCACert, customCAKey, customCAPEM := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, customLeafPEM, _ := generateLeafCert(t, customCACert, customCAKey, "custom.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	customLeafCert, err := decodePEMCert(customLeafPEM)
	require.NoError(t, err)

	apiKeySecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"apikey": []byte("test-api-key-value")},
	}
	cp := newTestPoolWithFakeClient(&apiKeySecret)

	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort: "localhost:7233",
		TLS: &temporaliov1alpha1.ConnectionTLSConfig{
			CACertSecretRef: &temporaliov1alpha1.SecretReference{Name: "ca-secret"},
		},
		APIKeySecretRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
			Key:                  "apikey",
		},
	}

	a, err := cp.fetchClientUsingAPIKeySecret(opts, "test-ns", customCAPEM)
	require.NoError(t, err)

	pool := a.tls.RootCAs
	require.NotNil(t, pool, "RootCAs must be set when a CA cert is supplied")

	_, err = customLeafCert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, DNSName: "custom.example.com"})
	assert.NoError(t, err, "custom CA should be trusted")
}

// TestFetchAPIKey_NoCACert_RootCAsNil verifies that omitting TLS.CACertSecretRef leaves
// RootCAs nil, preserving today's behavior (Go falls back to the system CA bundle).
func TestFetchAPIKey_NoCACert_RootCAsNil(t *testing.T) {
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"apikey": []byte("test-api-key-value")},
	}
	cp := newTestPoolWithFakeClient(&secret)
	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort: "localhost:7233",
		APIKeySecretRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
			Key:                  "apikey",
		},
	}

	a, err := cp.fetchClientUsingAPIKeySecret(opts, "test-ns", nil)

	require.NoError(t, err)
	assert.Nil(t, a.tls.RootCAs)
}

// TestFetchNoCredentials_CACertSetsRootCAs verifies that fetchClientUsingNoCredentials
// applies a supplied CA cert even when no TLS.ServerName override is set, since previously
// this path only allocated a TLS config at all when ServerName was non-empty.
func TestFetchNoCredentials_CACertSetsRootCAs(t *testing.T) {
	now := time.Now()
	caCert, caKey, caPEM := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, leafPEM, _ := generateLeafCert(t, caCert, caKey, "custom.example.com", now.Add(-time.Hour), now.Add(time.Hour))
	leafCert, err := decodePEMCert(leafPEM)
	require.NoError(t, err)

	cp := newTestPool()
	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort: "localhost:7233",
		TLS: &temporaliov1alpha1.ConnectionTLSConfig{
			CACertSecretRef: &temporaliov1alpha1.SecretReference{Name: "ca-secret"},
		},
	}

	a, err := cp.fetchClientUsingNoCredentials(opts, caPEM)

	require.NoError(t, err)
	require.NotNil(t, a.tls, "TLS config must be allocated once a CA cert is supplied")
	require.NotNil(t, a.tls.RootCAs)
	_, err = leafCert.Verify(x509.VerifyOptions{Roots: a.tls.RootCAs, CurrentTime: now, DNSName: "custom.example.com"})
	assert.NoError(t, err)
}

// TestParseClientSecret_APIKeyWithCACertSecretRef is the end-to-end regression test: it
// exercises parseClientSecret (not the fetchClientUsing* functions directly) to confirm the
// CA secret is actually read from the fake k8s client and threaded through to the TLS config.
func TestParseClientSecret_APIKeyWithCACertSecretRef(t *testing.T) {
	now := time.Now()
	caCert, caKey, caPEM := generateSelfSignedCACert(t, now.Add(-time.Hour), now.Add(time.Hour))
	_, leafPEM, _ := generateLeafCert(t, caCert, caKey, "temporal.internal", now.Add(-time.Hour), now.Add(time.Hour))
	leafCert, err := decodePEMCert(leafPEM)
	require.NoError(t, err)

	apiKeySecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"apikey": []byte("test-api-key-value")},
	}
	caSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ca-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"ca.crt": caPEM},
	}
	cp := newTestPoolWithFakeClient(&apiKeySecret, &caSecret)

	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort: "wf-scheduler.example.com:443",
		TLS: &temporaliov1alpha1.ConnectionTLSConfig{
			CACertSecretRef: &temporaliov1alpha1.SecretReference{Name: "ca-secret"},
		},
		APIKeySecretRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
			Key:                  "apikey",
		},
	}

	a, err := cp.parseClientSecret(context.Background(), opts, "test-ns")

	require.NoError(t, err)
	require.NotNil(t, a.tls.RootCAs)
	_, err = leafCert.Verify(x509.VerifyOptions{Roots: a.tls.RootCAs, CurrentTime: now, DNSName: "temporal.internal"})
	assert.NoError(t, err)
}

// TestParseClientSecret_CACertSecretMissingKey_ReturnsError verifies that a CA secret
// referenced by tls.caCertSecretRef but missing its ca.crt key is a hard error, not a
// silent no-op. Unlike MutualTLSSecretRef's ca.crt (optional, since that secret's primary
// job is tls.crt/tls.key), this field's only purpose is carrying a CA — a missing key here
// is a misconfiguration that must surface, not silently fall back to system-trust-only.
func TestParseClientSecret_CACertSecretMissingKey_ReturnsError(t *testing.T) {
	apiKeySecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "api-key-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"apikey": []byte("test-api-key-value")},
	}
	caSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ca-secret", Namespace: "test-ns"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"wrong-key": []byte("not-a-cert")},
	}
	cp := newTestPoolWithFakeClient(&apiKeySecret, &caSecret)

	opts := temporaliov1alpha1.ConnectionSpec{
		HostPort: "wf-scheduler.example.com:443",
		TLS: &temporaliov1alpha1.ConnectionTLSConfig{
			CACertSecretRef: &temporaliov1alpha1.SecretReference{Name: "ca-secret"},
		},
		APIKeySecretRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
			Key:                  "apikey",
		},
	}

	_, err := cp.parseClientSecret(context.Background(), opts, "test-ns")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ca-secret")
	assert.Contains(t, err.Error(), "ca.crt")
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
	a, err := cp.fetchClientUsingMTLSSecret(opaqueSecret, makeMTLSSpec("localhost:7233"))

	require.NoError(t, err, "Opaque secret with tls.crt and tls.key should be accepted for mTLS auth")
	assert.Equal(t, temporaliov1alpha1.AuthModeTLS, a.mode)
	assert.NotNil(t, a.expiryTime)
}

// ─── Tests: dial / healthCheck / upsert ────────────────────────────────────

// TestDialAndUpsert_APIKeySkipsCheckHealth is the regression test for PR #232.
//
// Before the fix, the dial path called c.CheckHealth() unconditionally. On Temporal
// Cloud, namespace-scoped API keys do not have permission to call the system-scoped
// CheckHealth RPC, so every connection attempt failed.
//
// After the fix, CheckHealth is skipped for temporaliov1alpha1.AuthModeAPIKey because client.Dial already
// calls GetSystemInfo internally (a superset of CheckHealth).
func TestDialAndUpsert_APIKeySkipsCheckHealth(t *testing.T) {
	mock := &mockSDKClient{}
	cp := newTestPool()
	cp.dialFn = func(_ sdkclient.Options) (sdkclient.Client, error) { return mock, nil }

	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: temporaliov1alpha1.AuthModeAPIKey}
	auth := clientAuth{mode: temporaliov1alpha1.AuthModeAPIKey}

	c, err := cp.dialFn(sdkclient.Options{})
	require.NoError(t, err)
	require.NoError(t, cp.healthCheck(c, auth))
	cp.cacheClient(key, CachedClient{Client: c, IsValid: cp.defaultValidityCheck(auth)})

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

	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: temporaliov1alpha1.AuthModeTLS}
	auth := clientAuth{
		mode:       temporaliov1alpha1.AuthModeTLS,
		tls:        &tls.Config{},
		expiryTime: time.Now().Add(time.Hour),
	}

	c, err := cp.dialFn(sdkclient.Options{})
	require.NoError(t, err)
	require.NoError(t, cp.healthCheck(c, auth))
	cp.cacheClient(key, CachedClient{Client: c, IsValid: cp.defaultValidityCheck(auth)})

	assert.NotNil(t, c)
	assert.True(t, mock.checkHealthCalled, "CheckHealth must be called for TLS auth")
}

// TestDialAndUpsert_NoCredsCallsCheckHealth verifies that CheckHealth IS called for no-credentials mode.
func TestDialAndUpsert_NoCredsCallsCheckHealth(t *testing.T) {
	mock := &mockSDKClient{}
	cp := newTestPool()
	cp.dialFn = func(_ sdkclient.Options) (sdkclient.Client, error) { return mock, nil }

	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: temporaliov1alpha1.AuthModeNoCredentials}
	auth := clientAuth{mode: temporaliov1alpha1.AuthModeNoCredentials}

	c, err := cp.dialFn(sdkclient.Options{})
	require.NoError(t, err)
	require.NoError(t, cp.healthCheck(c, auth))
	cp.cacheClient(key, CachedClient{Client: c, IsValid: cp.defaultValidityCheck(auth)})

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
		AuthMode:   temporaliov1alpha1.AuthModeAPIKey,
	}
	mock := &mockSDKClient{}
	cp.SetClientForTesting(key, mock)

	c := cp.Clients()[key]
	require.NotNil(t, c, "client should be present before eviction")

	cp.EvictClient(key)

	assert.True(t, mock.closed, "Close should be called on eviction")
	c = cp.Clients()[key]
	assert.Nil(t, c, "client should be absent after eviction")
}

func TestEvictClient_NoopWhenKeyAbsent(t *testing.T) {
	cp := newTestPool()
	key := ClientPoolKey{HostPort: "localhost:7233", Namespace: "default", AuthMode: temporaliov1alpha1.AuthModeNoCredentials}
	// Should not panic when key is not in the pool
	cp.EvictClient(key)
}

// ─── Tests: GetClient ─────────────────────────────────────────────────────────
//
// These verify the cache-hit / error-classification behavior of GetClient. The
// Secret/TLS/dial internals behind it are covered by the fetchClientUsing* and
// dial / healthCheck / upsert tests above.

// newPoolWithDefaults builds a ClientPool with a controllable dialFn.
func newPoolWithDefaults(dialFn func(sdkclient.Options) (sdkclient.Client, error)) *ClientPool {
	cp := newTestPool()
	cp.dialFn = dialFn
	return cp
}

func TestGetClient_CacheHit_ReturnsCachedWithoutDialing(t *testing.T) {
	spec := temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233"}
	// A dial would fail this test if the cache miss path were taken.
	cp := newPoolWithDefaults(func(sdkclient.Options) (sdkclient.Client, error) {
		t.Fatal("cache hit must not dial")
		return nil, nil
	})
	stub := &mockSDKClient{}
	cp.SetClientForTesting(ClientPoolKey{
		HostPort:  "h:7233",
		Namespace: "ns",
		AuthMode:  temporaliov1alpha1.AuthModeNoCredentials,
	}, stub)

	got, _, err := cp.GetClient(context.Background(), spec, "ns", "k8s-ns", "identity")
	require.NoError(t, err)
	assert.Same(t, stub, got, "GetClient must return the cached client on a hit")
}

func TestGetClient_InvalidSpec_ReturnsAuthConfigError(t *testing.T) {
	cp := newPoolWithDefaults(sdkclient.Dial)
	spec := temporaliov1alpha1.ConnectionSpec{
		HostPort:           "h:7233",
		MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: ""}, // invalid: empty name
	}

	_, _, err := cp.GetClient(context.Background(), spec, "ns", "k8s-ns", "identity")
	require.Error(t, err)
	var authErr *AuthConfigError
	require.ErrorAs(t, err, &authErr) // invalid spec must surface as AuthConfigError
}

func TestGetClient_DialFailure_ReturnsDialError(t *testing.T) {
	cp := newPoolWithDefaults(func(sdkclient.Options) (sdkclient.Client, error) {
		return nil, errors.New("boom")
	})
	spec := temporaliov1alpha1.ConnectionSpec{HostPort: "h:7233"} // no-creds -> reaches dial

	_, _, err := cp.GetClient(context.Background(), spec, "ns", "k8s-ns", "identity")
	require.Error(t, err)
	var dialErr *DialError
	require.ErrorAs(t, err, &dialErr) // dial failure must surface as DialError
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func decodePEMCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	return x509.ParseCertificate(block.Bytes)
}
