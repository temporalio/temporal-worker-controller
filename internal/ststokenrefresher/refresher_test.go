package ststokenrefresher

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

var workerDeploymentsGVR = schema.GroupVersionResource{
	Group: "temporal.io", Version: "v1alpha1", Resource: "workerdeployments",
}

func TestTokenRefreshUpdatesSecretsOnlyWhereTheClusterConnectionIsUsed(t *testing.T) {
	worker := workerDeployment("team-a", "test-connection")
	other := workerDeployment("team-b", "other-temporal")
	workers := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{
			workerDeploymentsGVR: "WorkerDeploymentList",
		}, worker, other,
	)
	secrets := kubefake.NewSimpleClientset(
		apiKeySecret("team-a", "old-token"),
		apiKeySecret("team-b", "other-token"),
	)
	refresher := Refresher{
		ConnectionName: "test-connection",
		Kubernetes:     secrets,
		Dynamic:        workers,
	}

	if err := refresher.syncWorkerNamespaces(context.Background(), "fresh-token"); err != nil {
		t.Fatal(err)
	}

	teamA, err := secrets.CoreV1().Secrets("team-a").Get(context.Background(), "temporal-api-key", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(teamA.Data["api-key"]); got != "fresh-token" {
		t.Fatalf("team-a API key = %q, want refreshed token", got)
	}
	if got := string(teamA.Data["managed-by"]); got != "gitops" {
		t.Fatalf("team-a managed-by = %q, want existing value preserved", got)
	}
	teamB, err := secrets.CoreV1().Secrets("team-b").Get(context.Background(), "temporal-api-key", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(teamB.Data["api-key"]); got != "other-token" {
		t.Fatalf("team-b API key = %q, want it unchanged", got)
	}
}

func workerDeployment(namespace, connection string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "temporal.io/v1alpha1",
		"kind":       "WorkerDeployment",
		"metadata": map[string]interface{}{
			"name": "worker", "namespace": namespace,
		},
		"spec": map[string]interface{}{
			"workerOptions": map[string]interface{}{
				"connectionRef": map[string]interface{}{
					"objectRef": map[string]interface{}{
						"kind": "ClusterConnection", "name": connection,
					},
				},
			},
		},
	}}
}

func apiKeySecret(namespace, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "temporal-api-key", Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"api-key": []byte(value), "managed-by": []byte("gitops")},
	}
}
