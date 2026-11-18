package ststokenrefresher

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	workerDeploymentsResource = "workerdeployments"
	apiKeySecretName          = "temporal-api-key"
	apiKeySecretKey           = "api-key"
	refreshBeforeExpiry       = time.Minute
)

type Token struct {
	Value     string
	ExpiresAt time.Time
}

type TokenIssuer interface {
	Issue(context.Context) (Token, error)
}

type Refresher struct {
	Audience       string
	ConnectionName string
	Interval       time.Duration

	Issuer     TokenIssuer
	Kubernetes kubernetes.Interface
	Dynamic    dynamic.Interface
	Now        func() time.Time
	Logf       func(string, ...any)
}

func (r *Refresher) Run(ctx context.Context) error {
	if r.Audience == "" || r.ConnectionName == "" {
		return errors.New("audience and connection name are required")
	}
	if r.Issuer == nil || r.Kubernetes == nil || r.Dynamic == nil {
		return errors.New("token issuer and Kubernetes clients are required")
	}
	if r.Interval <= 0 {
		r.Interval = 30 * time.Second
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Logf == nil {
		r.Logf = func(string, ...any) {}
	}

	var token Token
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		if token.Value == "" || !r.Now().Before(token.ExpiresAt.Add(-refreshBeforeExpiry)) {
			fresh, err := r.Issuer.Issue(ctx)
			if err != nil {
				r.Logf("could not refresh Temporal STS token: %v", err)
			} else if fresh.Value == "" || !fresh.ExpiresAt.After(r.Now()) {
				r.Logf("STS returned an empty or expired Temporal token")
			} else {
				token = fresh
				r.Logf("refreshed Temporal STS token; expires at %s", token.ExpiresAt.UTC().Format(time.RFC3339))
			}
		}

		if token.Value != "" && r.Now().Before(token.ExpiresAt) {
			if err := r.syncWorkerNamespaces(ctx, token.Value); err != nil {
				r.Logf("could not update Temporal API-key Secrets: %v", err)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Refresher) syncWorkerNamespaces(ctx context.Context, token string) error {
	list, err := r.Dynamic.Resource(schema.GroupVersionResource{
		Group: "temporal.io", Version: "v1alpha1", Resource: workerDeploymentsResource,
	}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list WorkerDeployments: %w", err)
	}

	namespaces := make(map[string]struct{})
	for i := range list.Items {
		if referencesConnection(&list.Items[i], r.ConnectionName) {
			namespaces[list.Items[i].GetNamespace()] = struct{}{}
		}
	}

	var errs []error
	for namespace := range namespaces {
		if err := r.updateSecret(ctx, namespace, token); err != nil {
			errs = append(errs, fmt.Errorf("namespace %q: %w", namespace, err))
		}
	}
	return errors.Join(errs...)
}

func referencesConnection(worker *unstructured.Unstructured, name string) bool {
	ref, found, err := unstructured.NestedMap(worker.Object, "spec", "workerOptions", "connectionRef", "objectRef")
	return err == nil && found && ref["kind"] == "ClusterConnection" && ref["name"] == name
}

func (r *Refresher) updateSecret(ctx context.Context, namespace, token string) error {
	secrets := r.Kubernetes.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, apiKeySecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("Secret %q must be created by GitOps before the WorkerDeployment", apiKeySecretName)
	}
	if err != nil {
		return fmt.Errorf("get Secret %q: %w", apiKeySecretName, err)
	}
	if secret.Type != "" && secret.Type != corev1.SecretTypeOpaque {
		return fmt.Errorf("Secret %q must have type Opaque", apiKeySecretName)
	}
	if string(secret.Data[apiKeySecretKey]) == token {
		return nil
	}
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data[apiKeySecretKey] = []byte(token)
	if secret.Type == "" {
		secret.Type = corev1.SecretTypeOpaque
	}
	if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update Secret %q: %w", apiKeySecretName, err)
	}
	return nil
}
