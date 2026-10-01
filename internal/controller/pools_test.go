// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// makePooledWD returns a WorkerDeployment with the given pools, or with a single
// spec.deployment when there are none.
func makePooledWD(name, namespace string, pools ...string) *temporaliov1alpha1.WorkerDeployment {
	twd := makeExecplanTWD(name, namespace)
	template := *twd.Spec.Template
	twd.Spec.Template = nil
	if len(pools) == 0 {
		twd.Spec.Deployment = &appsv1.DeploymentSpec{Replicas: twd.Spec.Replicas, Template: template}
	}
	twd.Spec.Replicas = nil
	for _, pool := range pools {
		twd.Spec.Pools = append(twd.Spec.Pools, temporaliov1alpha1.WorkerPool{
			Name:       pool,
			Deployment: appsv1.DeploymentSpec{Template: *template.DeepCopy()},
		})
	}
	return twd
}

func labelPool(d *appsv1.Deployment, pool string) *appsv1.Deployment {
	d.Labels[k8s.PoolLabel] = pool
	d.Spec.Selector.MatchLabels[k8s.PoolLabel] = pool
	d.Spec.Template.Labels[k8s.PoolLabel] = pool
	if pool != temporaliov1alpha1.DefaultPoolName {
		d.Name += "-" + pool
	}
	return d
}

func TestExecutePlan_NewMultiPoolVersion_CreatesEveryPool(t *testing.T) {
	const namespace = "default"
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makePooledWD("my-worker", namespace, "workflows", "activities")
	buildID := k8s.ComputeBuildID(twd)
	r, _ := newTestReconciler([]client.Object{twd})

	status := temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: buildID},
		},
	}
	p := runPlanCycle(t, r, twd, connection, status)
	require.Len(t, p.CreateDeployments, 2)

	var created appsv1.DeploymentList
	require.NoError(t, r.List(context.Background(), &created, client.InNamespace(namespace)))
	pools := map[string]string{}
	for _, d := range created.Items {
		assert.Equal(t, buildID, d.Labels[k8s.BuildIDLabel])
		assert.Equal(t, d.Labels[k8s.PoolLabel], d.Spec.Selector.MatchLabels[k8s.PoolLabel])
		pools[d.Labels[k8s.PoolLabel]] = d.Name
	}
	assert.Equal(t, map[string]string{
		"workflows":  k8s.ComputePoolDeploymentName(twd.Name, "workflows", buildID),
		"activities": k8s.ComputePoolDeploymentName(twd.Name, "activities", buildID),
	}, pools)
}

func TestExecutePlan_PoolRemovedUnderCustomBuildID_KeepsVersion(t *testing.T) {
	const (
		namespace = "default"
		buildID   = "custom"
	)
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makePooledWD("my-worker", namespace, "workflows")
	twd.Spec.WorkerOptions.UnsafeCustomBuildID = buildID
	workflows := labelPool(makeVersionedDeployment(twd, buildID, 1, connection), "workflows")
	activities := labelPool(makeVersionedDeployment(twd, buildID, 1, connection), "activities")
	r, _ := newTestReconciler([]client.Object{twd, workflows, activities})

	handle := newPruneStubHandle(nil)
	status := statusWithDeprecated(buildID, workflows)
	p := runPlanCycleWith(t, r, twd, connection, status, newStubTemporalClientWithHandle(handle))

	assert.Empty(t, p.DeleteDeployments)
	assert.Empty(t, handle.deletedVersions, "removing a pool must not delete the version")
	assert.False(t, deploymentExists(t, r, namespace, activities.Name))
	assert.True(t, deploymentExists(t, r, namespace, workflows.Name))
}

func TestReconcile_PoolsAddedUnderSameCustomBuildID_AreBlocked(t *testing.T) {
	const namespace = "default"
	tc := makeNoCredsConnection("my-conn", namespace, "localhost:7233")
	twd := makePooledWD("my-worker", namespace, "activities")
	twd.Spec.WorkerOptions.ConnectionRef.Name = tc.Name
	twd.Spec.WorkerOptions.UnsafeCustomBuildID = "custom"
	existing := makeVersionedDeployment(twd, "custom", 1, tc.Spec)
	r, _ := newTestReconciler([]client.Object{twd, tc, existing})
	r.TemporalClientPool.SetClientForTesting(noCredsPoolKey(tc.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace), newStubTemporalClient(nil))

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: namespace}})
	require.NoError(t, err, "the rest of the plan still runs")

	var got temporaliov1alpha1.WorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: namespace}, &got))
	cond := meta.FindStatusCondition(got.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, temporaliov1alpha1.ReasonInvalidSpec, cond.Reason)
	assert.Contains(t, cond.Message, "unsafeCustomBuildID")
	assert.False(t, deploymentExists(t, r, namespace, k8s.ComputePoolDeploymentName(twd.Name, "activities", "custom")))
}

func TestSyncConditions_MultiPoolTargetWaitingForPools(t *testing.T) {
	r, _ := newTestReconciler(nil)
	twd := makePooledWD("test-worker", "default", "activities", "batch", "workflows")
	twd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusInactive
	twd.Status.TargetVersion.Pools = []temporaliov1alpha1.WorkerPoolStatus{
		{Name: "activities"},
		{Name: "workflows", Deployment: &corev1.ObjectReference{Name: "d"}, HealthySince: &metav1.Time{Time: time.Now()}},
	}

	r.syncConditions(twd, nil, "")

	cond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, temporaliov1alpha1.ReasonWaitingForPollers, cond.Reason)
	assert.Contains(t, cond.Message, "activities, batch")
	assert.NotContains(t, cond.Message, "workflows")
}

func TestHandleDeletion_DeletesEveryPool(t *testing.T) {
	const namespace = "default"
	conn := makeNoCredsConnection("my-conn", namespace, "localhost:7233")
	twd := makePooledWD("del-worker", namespace, "workflows", "activities")
	twd.Spec.WorkerOptions.ConnectionRef.Name = conn.Name
	def := labelPool(makeVersionedDeployment(twd, "v1", 1, conn.Spec), "workflows")
	activities := labelPool(makeVersionedDeployment(twd, "v1", 1, conn.Spec), "activities")
	r, _ := newTestReconciler([]client.Object{twd, conn, def, activities})
	r.TemporalClientPool.SetClientForTesting(noCredsPoolKey(conn.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace),
		newStubTemporalClientWithHandle(&stubWDHandle{}))

	require.NoError(t, r.handleDeletion(context.Background(), logr.Discard(), twd))

	assert.False(t, deploymentExists(t, r, namespace, def.Name))
	assert.False(t, deploymentExists(t, r, namespace, activities.Name))
}

func TestExecutePlan_WRTWithMissingPool_SetsPoolNotFound(t *testing.T) {
	const namespace = "default"
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makePooledWD("my-worker", namespace, "activities")
	buildID := k8s.ComputeBuildID(twd)
	ghost := makeExecplanWRT("ghost-hpa", twd)
	ghost.Spec.Pool = "ghost"
	activities := makeExecplanWRT("activities-hpa", twd)
	activities.Spec.Pool = "activities"
	r, _ := newTestReconciler([]client.Object{twd, ghost, activities})

	status := temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: buildID},
		},
	}
	runPlanCycle(t, r, twd, connection, status)

	var got temporaliov1alpha1.WorkerResourceTemplate
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: ghost.Name}, &got))
	cond := meta.FindStatusCondition(got.Status.Conditions, temporaliov1alpha1.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, temporaliov1alpha1.ReasonWRTPoolNotFound, cond.Reason)
	assert.Contains(t, cond.Message, `"ghost"`)

	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: activities.Name}, &got))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, temporaliov1alpha1.ConditionReady),
		"a declared pool without a Deployment yet is not missing")
}

func TestReconcile_BlockedPools_StatusAndEventsSettle(t *testing.T) {
	const namespace = "default"
	tc := makeNoCredsConnection("my-conn", namespace, "localhost:7233")
	twd := makePooledWD("my-worker", namespace, "activities")
	twd.Spec.WorkerOptions.ConnectionRef.Name = tc.Name
	twd.Spec.WorkerOptions.UnsafeCustomBuildID = "custom"
	existing := makeVersionedDeployment(twd, "custom", 1, tc.Spec)
	writes := 0
	r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd, tc, existing}, countWDStatusWrites(&writes))
	r.TemporalClientPool.SetClientForTesting(noCredsPoolKey(tc.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace), newStubTemporalClient(nil))
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: namespace}}

	for range 3 {
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		drainEvents(recorder)
	}
	writes = 0
	var events []string
	for range 3 {
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		events = append(events, drainEvents(recorder)...)
	}

	assert.Zero(t, writes, "a blocked WorkerDeployment must not rewrite its status every reconcile")
	assert.Empty(t, events)
}

func TestSyncConditions_BlockedSpecIsNotReady(t *testing.T) {
	r, _ := newTestReconciler(nil)
	for _, status := range []temporaliov1alpha1.VersionStatus{temporaliov1alpha1.VersionStatusCurrent, temporaliov1alpha1.VersionStatusInactive} {
		t.Run(string(status), func(t *testing.T) {
			twd := makePooledWD("test-worker", "default", "activities")
			twd.Status.TargetVersion.Status = status

			r.syncConditions(twd, nil, "adding pools requires a new unsafeCustomBuildID")

			for _, condType := range []string{temporaliov1alpha1.ConditionReady, temporaliov1alpha1.ConditionProgressing} {
				cond := meta.FindStatusCondition(twd.Status.Conditions, condType)
				require.NotNil(t, cond, condType)
				assert.Equal(t, metav1.ConditionFalse, cond.Status, condType)
				assert.Equal(t, temporaliov1alpha1.ReasonInvalidSpec, cond.Reason, condType)
			}
		})
	}
}

func TestExecutePlan_PoolNotFound_SetDespiteOtherApplyFailure(t *testing.T) {
	const namespace = "default"
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makePooledWD("my-worker", namespace)
	buildID := k8s.ComputeBuildID(twd)
	def := makeVersionedDeployment(twd, buildID, 1, connection)
	ghost := makeExecplanWRT("ghost-hpa", twd)
	ghost.Spec.Pool = "ghost"
	broken := makeExecplanWRT("broken", twd)
	broken.Spec.Template.Raw = []byte(`{"apiVersion": "autoscaling/v2", "kind": "HorizontalPodAutoscaler", "spec": "not an object"}`)
	r, _ := newTestReconciler([]client.Object{twd, def, ghost, broken})

	w := twd.DeepCopy()
	w.Status = statusWithDeprecated(buildID, def)
	p, err := r.generatePlan(context.Background(), logr.Discard(), w, connection, &temporal.TemporalWorkerState{})
	require.NoError(t, err)
	require.Error(t, r.executePlan(context.Background(), logr.Discard(), w, newStubTemporalClient(nil), p), "the broken WRT fails")

	var got temporaliov1alpha1.WorkerResourceTemplate
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: ghost.Name}, &got))
	cond := meta.FindStatusCondition(got.Status.Conditions, temporaliov1alpha1.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, temporaliov1alpha1.ReasonWRTPoolNotFound, cond.Reason)
}

func TestExecutePlan_PoolNotFound_ClearedOnceThePoolIsDeclared(t *testing.T) {
	const namespace = "default"
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makePooledWD("my-worker", namespace, "activities")
	wrt := makeExecplanWRT("activities-hpa", twd)
	wrt.Spec.Pool = "activities"
	wrt.Status.Conditions = []metav1.Condition{{
		Type: temporaliov1alpha1.ConditionReady, Status: metav1.ConditionFalse,
		Reason: temporaliov1alpha1.ReasonWRTPoolNotFound, LastTransitionTime: metav1.Now(),
	}}
	r, _ := newTestReconciler([]client.Object{twd, wrt})
	require.NoError(t, r.Status().Update(context.Background(), wrt))

	// The version cap holds back the new version, so the declared pool has no Deployment yet.
	r.MaxDeploymentVersionsIneligibleForDeletion = 0
	runPlanCycle(t, r, twd, connection, temporaliov1alpha1.WorkerDeploymentStatus{})

	var got temporaliov1alpha1.WorkerResourceTemplate
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: wrt.Name}, &got))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, temporaliov1alpha1.ConditionReady),
		"a stale PoolNotFound must not outlive the missing pool")
}

func TestExecutePlan_WRTWithoutPoolOnPooledWorkerDeployment_SaysToSetPool(t *testing.T) {
	const namespace = "default"
	connection := temporaliov1alpha1.ConnectionSpec{HostPort: "test:7233"}
	twd := makePooledWD("my-worker", namespace, "workflows")
	wrt := makeExecplanWRT("hpa", twd)
	r, _ := newTestReconciler([]client.Object{twd, wrt})

	runPlanCycle(t, r, twd, connection, temporaliov1alpha1.WorkerDeploymentStatus{})

	var got temporaliov1alpha1.WorkerResourceTemplate
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: wrt.Name}, &got))
	cond := meta.FindStatusCondition(got.Status.Conditions, temporaliov1alpha1.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, temporaliov1alpha1.ReasonWRTPoolNotFound, cond.Reason)
	assert.Contains(t, cond.Message, "set spec.pool")
}
