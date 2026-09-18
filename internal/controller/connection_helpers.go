// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/connectionprovider"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// sameConnectionRef reports whether two connectionRefs resolve to the same
// connection. Identity is name + GroupKind (not a hardcoded kind name), so a
// namespaced Connection "foo" and a cluster-scoped connection "foo" are
// distinct — consistent with the watch mapper in findTWDsUsingConnectionKind.
func sameConnectionRef(a, b temporaliov1alpha1.ConnectionReference) bool {
	return connectionprovider.RefName(a) == connectionprovider.RefName(b) &&
		connectionprovider.RefGroupKind(a) == connectionprovider.RefGroupKind(b)
}

// ensureConnectionFinalizer adds our finalizer to the Connection so it
// cannot be deleted while this WD still needs it for cleanup.
func (r *WorkerDeploymentReconciler) ensureConnectionFinalizer(
	ctx context.Context,
	l logr.Logger,
	conn client.Object,
) error {
	if !controllerutil.ContainsFinalizer(conn, finalizerName) {
		l.Info("Adding finalizer to connection", "connection", conn.GetName())
		controllerutil.AddFinalizer(conn, finalizerName)
		if err := r.Update(ctx, conn); err != nil {
			return fmt.Errorf("unable to add finalizer to connection %q: %w", conn.GetName(), err)
		}
	}
	return nil
}

// releaseConnectionFinalizerIfUnused removes our finalizer from the connection
// identified by ref, unless some other WorkerDeployment (other than
// selfNamespace/selfName) still references it. It is used both when a WD is
// deleted (ref = current connectionRef) and when a WD's connectionRef changes
// (ref = previously-observed connectionRef).
func (r *WorkerDeploymentReconciler) releaseConnectionFinalizerIfUnused(
	ctx context.Context,
	l logr.Logger,
	ref temporaliov1alpha1.ConnectionReference,
	selfNamespace, selfName string,
) error {
	// Resolve the provider once: it supplies both the cluster-scoped flag (for
	// list scope) and the Fetch (for the finalizer update). An unknown kind
	// never had a finalizer placed on it (the reconcile path blocks before
	// that), so there is nothing to release.
	prov, err := connectionprovider.LookupProvider(r.Providers, ref)
	if err != nil {
		var unknown *connectionprovider.UnknownKindError
		if errors.As(err, &unknown) {
			return nil
		}
		return err
	}
	isCluster := prov.IsClusterScoped()

	// A namespace-scoped controller never placed a finalizer on a cluster-scoped
	// connection, and cannot read one to check. Nothing to release.
	if isCluster && r.DisableClusterConnections {
		return nil
	}

	// Scope the "is it still used?" query correctly for the kind:
	//   - Namespaced: only WDs in its own namespace can reference it, so
	//     restrict the list to selfNamespace.
	//   - Cluster-scoped: a WD in ANY namespace can reference it, so we must
	//     list across all namespaces.
	var listOpts []client.ListOption
	if !isCluster {
		listOpts = append(listOpts, client.InNamespace(selfNamespace))
	}

	var wds temporaliov1alpha1.WorkerDeploymentList
	if err := r.List(ctx, &wds, listOpts...); err != nil {
		return fmt.Errorf("unable to list WorkerDeployments: %w", err)
	}

	refGK := connectionprovider.RefGroupKind(ref)
	refName := connectionprovider.RefName(ref)
	for i := range wds.Items {
		wd := &wds.Items[i]
		// Skip self by namespace and name: under a cluster-wide list, two WDs in
		// different namespaces can share the same name, so name alone is not a
		// unique identity.
		if wd.Namespace == selfNamespace && wd.Name == selfName {
			continue
		}
		otherRef := wd.Spec.WorkerOptions.ConnectionRef
		// Same target only if BOTH name and GroupKind match, so a namespaced
		// Connection "foo" and a cluster-scoped connection "foo" are distinct.
		if connectionprovider.RefName(otherRef) == refName && connectionprovider.RefGroupKind(otherRef) == refGK {
			l.Info("Connection still referenced by another WorkerDeployment, keeping finalizer",
				"connection", refName, "clusterScoped", isCluster,
				"referencedBy", wd.Name, "referencedByNamespace", wd.Namespace)
			return nil
		}
	}

	// Fetch by the passed ref, not the WD's current connectionRef: during a
	// connectionRef change the WD's current ref points at the new connection, so
	// fetching by current ref would strip the finalizer off the wrong object.
	connection, err := prov.Fetch(ctx, ref, selfNamespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unable to fetch connection %q: %w", refName, err)
	}
	connObj := connection.Object()

	if controllerutil.ContainsFinalizer(connObj, finalizerName) {
		l.Info("Removing finalizer from connection", "connection", refName, "clusterScoped", isCluster)
		controllerutil.RemoveFinalizer(connObj, finalizerName)
		if err := r.Update(ctx, connObj); err != nil {
			return fmt.Errorf("unable to remove finalizer from connection %q: %w", refName, err)
		}
	}

	return nil
}
