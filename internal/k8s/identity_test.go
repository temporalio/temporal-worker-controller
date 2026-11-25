// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package k8s_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// These goldens pin single-template identity: a change here renames live
// Deployments or starts a rollout for existing users.

func identityFixture(image string) *temporaliov1alpha1.WorkerDeployment {
	return &temporaliov1alpha1.WorkerDeployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "temporal.io/v1alpha1", Kind: "WorkerDeployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payment-processor",
			Namespace: "staging",
			UID:       "wd-uid",
		},
		Spec: temporaliov1alpha1.WorkerDeploymentSpec{
			Deployment: &appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](3),
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "payments"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker", Image: image}},
					},
				},
			},
			WorkerOptions: temporaliov1alpha1.WorkerOptions{TemporalNamespace: "payments"},
		},
	}
}

var identityConnection = temporaliov1alpha1.ConnectionSpec{
	HostPort:           "temporal.example.com:7233",
	MutualTLSSecretRef: &temporaliov1alpha1.SecretReference{Name: "temporal-mtls"},
}

func TestIdentityGolden_BuildID(t *testing.T) {
	digest := "sha256:a428de44a9059f31a59237a5881c2d2cffa93757d99026156e4ea544577ab7f3"
	tests := []struct {
		name string
		wd   func() *temporaliov1alpha1.WorkerDeployment
		want string
	}{
		{
			name: "tagged image",
			wd: func() *temporaliov1alpha1.WorkerDeployment {
				return identityFixture("registry.example.com/payments/worker:v1.2.3")
			},
			want: "v1.2.3-49cd",
		},
		{
			name: "digest image",
			wd: func() *temporaliov1alpha1.WorkerDeployment {
				return identityFixture("registry.example.com/payments/worker@" + digest)
			},
			want: "a428de44a9059f31a59237a5881c2d2cffa93757d99026156e4ea54457-977c",
		},
		{
			name: "untagged image",
			wd: func() *temporaliov1alpha1.WorkerDeployment {
				return identityFixture("registry.example.com/payments/worker")
			},
			want: "payments-worker-b6d4",
		},
		{
			name: "no image",
			wd:   func() *temporaliov1alpha1.WorkerDeployment { return identityFixture("") },
			want: "444444b7c8",
		},
		{
			name: "deprecated template field",
			wd: func() *temporaliov1alpha1.WorkerDeployment {
				w := identityFixture("registry.example.com/payments/worker:v1.2.3")
				w.Spec.Template = &w.Spec.Deployment.Template
				w.Spec.Replicas = w.Spec.Deployment.Replicas
				w.Spec.Deployment = nil
				return w
			},
			want: "v1.2.3-49cd",
		},
		{
			name: "custom build ID is cleaned",
			wd: func() *temporaliov1alpha1.WorkerDeployment {
				w := identityFixture("registry.example.com/payments/worker:v1.2.3")
				w.Spec.WorkerOptions.UnsafeCustomBuildID = "release/2024_01!"
				return w
			},
			want: "release-2024_01",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, k8s.ComputeBuildID(tt.wd()))
		})
	}
}

func TestIdentityGolden_Names(t *testing.T) {
	w := identityFixture("registry.example.com/payments/worker:v1.2.3")
	assert.Equal(t, "staging/payment-processor", k8s.ComputeWorkerDeploymentName(w))
	assert.Equal(t, "payment-processor-v1-2-3-abcd", k8s.ComputeVersionedDeploymentName(w.Name, "v1.2.3-abcd"))
	assert.Equal(t, "a-very-lon-release-20-1dd753e16c", k8s.ComputeVersionedDeploymentName("a-very-long-worker-deployment-name", "release-2024-01-15-abcdef"))
	assert.Equal(t, map[string]string{
		"temporal.io/deployment-name": "payment-processor",
		"temporal.io/build-id":        "v1.2.3-abcd",
	}, k8s.ComputeSelectorLabels(w.Name, "v1.2.3-abcd"))
}

func TestIdentityGolden_Deployment(t *testing.T) {
	w := identityFixture("registry.example.com/payments/worker:v1.2.3")
	buildID := k8s.ComputeBuildID(w)
	d := k8s.NewDeploymentWithOwnerRef(&w.TypeMeta, &w.ObjectMeta, &w.Spec, k8s.ComputeWorkerDeploymentName(w), buildID, identityConnection)

	assert.Equal(t, "payment-processor-v1-2-3-49cd", d.Name)
	assert.Equal(t, "staging", d.Namespace)
	wantSelector := map[string]string{
		"temporal.io/deployment-name": "payment-processor",
		"temporal.io/build-id":        "v1.2.3-49cd",
	}
	assert.Equal(t, wantSelector, d.Labels)
	require.NotNil(t, d.Spec.Selector)
	assert.Equal(t, wantSelector, d.Spec.Selector.MatchLabels)
	assert.Equal(t, map[string]string{
		"app":                         "payments",
		"temporal.io/deployment-name": "payment-processor",
		"temporal.io/build-id":        "v1.2.3-49cd",
	}, d.Spec.Template.Labels)
	assert.Equal(t, map[string]string{
		k8s.ConnectionSpecHashAnnotation:  "d5edfb1851cbc87c4d1950b7876bc6a8a5ac14bc4a0a2c3d9a12ac444a84cc02",
		k8s.PodTemplateSpecHashAnnotation: "b05f6479085141ed6bb1c056342642e0672df018f943625b452f074667000c13",
	}, d.Spec.Template.Annotations)
	assert.Equal(t, ptr.To[int32](3), d.Spec.Replicas)

	require.Len(t, d.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, []corev1.EnvVar{
		{Name: "TEMPORAL_ADDRESS", Value: "temporal.example.com:7233"},
		{Name: "TEMPORAL_NAMESPACE", Value: "payments"},
		{Name: "TEMPORAL_DEPLOYMENT_NAME", Value: "staging/payment-processor"},
		{Name: "TEMPORAL_WORKER_BUILD_ID", Value: "v1.2.3-49cd"},
		{Name: "TEMPORAL_TLS", Value: "true"},
		{Name: "TEMPORAL_TLS_CLIENT_KEY_PATH", Value: "/etc/temporal/tls/tls.key"},
		{Name: "TEMPORAL_TLS_CLIENT_CERT_PATH", Value: "/etc/temporal/tls/tls.crt"},
	}, d.Spec.Template.Spec.Containers[0].Env)
}
