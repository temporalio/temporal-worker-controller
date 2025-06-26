// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package clientpool

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"

	"crypto/x509"
	"encoding/pem"

	sdkclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
)

type ClientPoolKey struct {
	HostPort  string
	Namespace string
}

type ClientInfo struct {
	client     sdkclient.Client
	tls        *tls.Config // Storing the TLS config associated with the client to check certificate expiration. If the certificate is expired, a new client will be created.
	expiryTime time.Time   // Effective expiration time (cert.NotAfter - buffer) for efficient expiration checking
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

	// Check if any certificate is expired
	expired, err := isCertificateExpired(info.expiryTime)
	if err != nil {
		cp.logger.Error("Error checking certificate expiration", "error", err)
		return nil, false
	}
	if expired {
		cp.logger.Warn("Certificate is expired or is going to expire soon")
		return nil, false
	}

	return info.client, true
}

type NewClientOptions struct {
	TemporalNamespace string
	K8sNamespace      string
	Spec              v1alpha1.TemporalConnectionSpec
}

func (cp *ClientPool) UpsertClient(ctx context.Context, opts NewClientOptions) (sdkclient.Client, error) {
	clientOpts := sdkclient.Options{
		Logger:    cp.logger,
		HostPort:  opts.Spec.HostPort,
		Namespace: opts.TemporalNamespace,
		// TODO(jlegrone): fix this
		Credentials: sdkclient.NewAPIKeyStaticCredentials(os.Getenv("TEMPORAL_CLOUD_API_KEY")),
		//Credentials: client.NewAPIKeyDynamicCredentials(func(ctx context.Context) (string, error) {
		//	token, ok := os.LookupEnv("TEMPORAL_CLOUD_API_KEY")
		//	if ok {
		//		if token == "" {
		//			return "", fmt.Errorf("empty token")
		//		}
		//		return token, nil
		//	}
		//	return "", fmt.Errorf("token not found")
		//}),
		//Credentials: client.NewMTLSCredentials(tls.Certificate{
		//	Certificate:                  cert.Certificate,
		//	PrivateKey:                   cert.PrivateKey,
		//	SupportedSignatureAlgorithms: nil,
		//	OCSPStaple:                   nil,
		//	SignedCertificateTimestamps:  nil,
		//	Leaf:                         nil,
		//}),
	}

	var pemCert []byte
	var expiryTime time.Time

	// Get the connection secret if it exists
	if opts.Spec.MutualTLSSecret != "" {
		var secret corev1.Secret
		if err := cp.k8sClient.Get(ctx, types.NamespacedName{
			Name:      opts.Spec.MutualTLSSecret,
			Namespace: opts.K8sNamespace,
		}, &secret); err != nil {
			return nil, err
		}
		if secret.Type != corev1.SecretTypeTLS {
			err := fmt.Errorf("secret %s must be of type kubernetes.io/tls", secret.Name)
			return nil, err
		}

		// Extract the certificate to calculate the effective expiration time
		pemCert = secret.Data["tls.crt"]

		// Check if certificate is expired before creating the client
		expiryTime, err := calculateCertificateExpirationTime(pemCert, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("failed to check certificate expiration: %v", err)
		}
		expired, err := isCertificateExpired(expiryTime)
		if err != nil {
			return nil, fmt.Errorf("failed to check certificate expiration: %v", err)
		}
		if expired {
			return nil, fmt.Errorf("certificate is expired or is going to expire soon")
		}

		cert, err := tls.X509KeyPair(secret.Data["tls.crt"], secret.Data["tls.key"])
		if err != nil {
			return nil, err
		}
		clientOpts.ConnectionOptions.TLS = &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
	}

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
		HostPort:  opts.Spec.HostPort,
		Namespace: opts.TemporalNamespace,
	}
	cp.clients[key] = ClientInfo{
		client:     c,
		tls:        clientOpts.ConnectionOptions.TLS,
		expiryTime: expiryTime,
	}

	return c, nil
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
