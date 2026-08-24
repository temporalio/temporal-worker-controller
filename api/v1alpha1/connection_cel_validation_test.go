// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

// Integration tests for CRD-level CEL validation rules on Connection.
//
// These tests hit a real kube-apiserver (via envtest) so they verify that the
// x-kubernetes-validations blocks in the generated CRD manifest are syntactically
// valid and semantically correct.

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Connection CRD CEL validation", func() {
	var ns string

	BeforeEach(func() {
		ns = makeTestNamespace("conn-cel")
	})

	baseConnection := func(name string) *Connection {
		return &Connection{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: ConnectionSpec{
				HostPort: "temporal.example.com:7233",
			},
		}
	}

	It("accepts a Connection with only apiKeySecretRef set", func() {
		conn := baseConnection("api-key-only")
		conn.Spec.APIKeySecretRef = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
			Key:                  "apikey",
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
	})

	It("accepts a Connection with apiKeySecretRef and tls.caCertSecretRef set together", func() {
		conn := baseConnection("api-key-with-ca")
		conn.Spec.APIKeySecretRef = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
			Key:                  "apikey",
		}
		conn.Spec.TLS = &ConnectionTLSConfig{
			CACertSecretRef: &SecretReference{Name: "ca-secret"},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
	})

	It("accepts a Connection with no credentials but tls.caCertSecretRef set", func() {
		conn := baseConnection("no-creds-with-ca")
		conn.Spec.TLS = &ConnectionTLSConfig{
			CACertSecretRef: &SecretReference{Name: "ca-secret"},
		}
		Expect(k8sClient.Create(ctx, conn)).To(Succeed())
	})

	It("rejects mutualTLSSecretRef and apiKeySecretRef set together", func() {
		conn := baseConnection("mtls-and-api-key")
		conn.Spec.MutualTLSSecretRef = &SecretReference{Name: "mtls-secret"}
		conn.Spec.APIKeySecretRef = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
			Key:                  "apikey",
		}
		err := k8sClient.Create(ctx, conn)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Only one of mutualTLSSecretRef or apiKeySecretRef may be set"))
	})

	It("rejects mutualTLSSecretRef and tls.caCertSecretRef set together", func() {
		conn := baseConnection("mtls-and-ca-cert")
		conn.Spec.MutualTLSSecretRef = &SecretReference{Name: "mtls-secret"}
		conn.Spec.TLS = &ConnectionTLSConfig{
			CACertSecretRef: &SecretReference{Name: "ca-secret"},
		}
		err := k8sClient.Create(ctx, conn)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("tls.caCertSecretRef cannot be combined with mutualTLSSecretRef"))
	})
})
