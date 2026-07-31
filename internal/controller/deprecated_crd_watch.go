package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	deprecatedTWDResource = schema.GroupVersionResource{
		Group: "temporal.io", Version: "v1alpha1", Resource: "temporalworkerdeployments",
	}
	deprecatedTCResource = schema.GroupVersionResource{
		Group: "temporal.io", Version: "v1alpha1", Resource: "temporalconnections",
	}
)

type DeprecatedCRDWatches struct {
	TemporalWorkerDeployments bool
	TemporalConnections       bool
}

// DetectDeprecatedCRDWatches returns which deprecated CRDs can be listed and watched in every configured namespace.
func DetectDeprecatedCRDWatches(
	ctx context.Context,
	client dynamic.Interface,
	namespaces []string,
) (DeprecatedCRDWatches, error) {
	twd, err := canListAndWatch(ctx, client, deprecatedTWDResource, namespaces)
	if err != nil {
		return DeprecatedCRDWatches{}, fmt.Errorf("check TemporalWorkerDeployment watch: %w", err)
	}
	tc, err := canListAndWatch(ctx, client, deprecatedTCResource, namespaces)
	if err != nil {
		return DeprecatedCRDWatches{}, fmt.Errorf("check TemporalConnection watch: %w", err)
	}
	return DeprecatedCRDWatches{
		TemporalWorkerDeployments: twd,
		TemporalConnections:       tc,
	}, nil
}

func canListAndWatch(
	ctx context.Context,
	client dynamic.Interface,
	resource schema.GroupVersionResource,
	namespaces []string,
) (bool, error) {
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	for _, namespace := range namespaces {
		resourceClient := client.Resource(resource).Namespace(namespace)
		if _, err := resourceClient.List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
				return false, nil
			}
			return false, err
		}
		stream, err := resourceClient.Watch(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
				return false, nil
			}
			return false, err
		}
		stream.Stop()
	}
	return true, nil
}
