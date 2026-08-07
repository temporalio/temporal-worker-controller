// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// newRenderedConfigMap returns an unstructured ConfigMap standing in for a
// rendered WRT resource (the controller treats rendered objects generically).
func newRenderedConfigMap(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("ConfigMap")
	u.SetNamespace(namespace)
	u.SetName(name)
	return u
}

func newTestWRT(namespace, name string) *temporaliov1alpha1.WorkerResourceTemplate {
	return &temporaliov1alpha1.WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

func newTestTWD(namespace, name string) *temporaliov1alpha1.WorkerDeployment {
	return &temporaliov1alpha1.WorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

// A version whose rendered hash matches the recorded LastAppliedHash but whose
// rendered resource is absent from the cluster (version sunset deletes rendered
// resources, and the build ID can be re-registered while its stale status entry
// survives) must be re-applied, not skipped — otherwise the returning build ID
// runs without its rendered resources.
func TestExecutePlan_WRTApply_ReappliesWhenResourceMissing(t *testing.T) {
	const ns = "default"
	wrt := newTestWRT(ns, "test-wrt")
	twd := newTestTWD(ns, "test-twd")

	r, _ := newTestReconciler([]client.Object{wrt, twd})

	rendered := newRenderedConfigMap(ns, "test-wrt-rendered")
	p := &plan{
		ApplyWorkerResources: []planner.WorkerResourceApply{{
			Resource:        rendered,
			WRTName:         wrt.Name,
			WRTNamespace:    ns,
			BuildID:         "build-1",
			RenderedHash:    "hash-1",
			LastAppliedHash: "hash-1", // matches, but the resource is gone
		}},
	}

	err := r.applyWorkerResourceTemplates(context.Background(), ctrl.Log, p)
	require.NoError(t, err)

	// The rendered resource must have been re-created despite the hash match.
	got := &corev1.ConfigMap{}
	require.NoError(t, r.Get(context.Background(),
		types.NamespacedName{Namespace: ns, Name: "test-wrt-rendered"}, got))

	// The WRT status must record the build as applied (not skipped-with-stale-state).
	gotWRT := &temporaliov1alpha1.WorkerResourceTemplate{}
	require.NoError(t, r.Get(context.Background(),
		types.NamespacedName{Namespace: ns, Name: wrt.Name}, gotWRT))
	require.Len(t, gotWRT.Status.Versions, 1)
	assert.Equal(t, "build-1", gotWRT.Status.Versions[0].BuildID)
	assert.Equal(t, "hash-1", gotWRT.Status.Versions[0].LastAppliedHash)
}

// The skip fast-path must still hold when the resource exists and the hash is
// unchanged: no SSA apply call is made.
func TestExecutePlan_WRTApply_SkipsWhenResourceExistsAndHashUnchanged(t *testing.T) {
	const ns = "default"
	wrt := newTestWRT(ns, "test-wrt")
	twd := newTestTWD(ns, "test-twd")
	existing := newRenderedConfigMap(ns, "test-wrt-rendered")

	patchCalls := 0
	r, _ := newTestReconcilerWithInterceptors(
		[]client.Object{wrt, twd, existing},
		interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				patchCalls++
				return c.Patch(ctx, obj, patch, opts...)
			},
		},
	)

	p := &plan{
		ApplyWorkerResources: []planner.WorkerResourceApply{{
			Resource:        newRenderedConfigMap(ns, "test-wrt-rendered"),
			WRTName:         wrt.Name,
			WRTNamespace:    ns,
			BuildID:         "build-1",
			RenderedHash:    "hash-1",
			LastAppliedHash: "hash-1",
		}},
	}

	err := r.applyWorkerResourceTemplates(context.Background(), ctrl.Log, p)
	require.NoError(t, err)
	assert.Zero(t, patchCalls, "hash-unchanged apply with existing resource must be skipped")
}
