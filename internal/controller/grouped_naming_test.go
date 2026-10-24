// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// These tests load Deployments created with the previous naming format and
// verify that grouping still preserves, restores, reports, and retires members.
package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestGroupedNamingUpgradePreservesExistingAndRestoresMissingMembers(t *testing.T) {
	for _, missing := range []string{"", k8s.PrimaryMember, "activities"} {
		t.Run("missing="+missing, func(t *testing.T) {
			ctx := context.Background()
			r, _ := newTestReconciler(nil)
			old := groupedWorker("release-123456789")
			saved, members := savedGroup(t, r, old)
			savedData := append([]byte(nil), saved.Object.Data.Raw...)
			require.NoError(t, r.Create(ctx, saved.Object))
			before := make(map[string]*appsv1.Deployment)
			for memberName, member := range members {
				member.Name = k8s.ComputeVersionedDeploymentName(old.Name+"-"+memberName, old.Spec.WorkerOptions.UnsafeCustomBuildID)
				member.UID = types.UID("original-" + memberName)
				if memberName != missing {
					require.NoError(t, r.Create(ctx, member))
					before[memberName] = member.DeepCopy()
				}
			}
			next := groupedWorker("release-2")
			next.Spec.AdditionalDeployments[0].Template.Spec.ServiceAccountName = "new-activity-account"
			ts := registeredGroup(old.Spec.WorkerOptions.UnsafeCustomBuildID)
			ts.CurrentBuildID = old.Spec.WorkerOptions.UnsafeCustomBuildID
			state, err := k8s.GetDeploymentState(ctx, r.Client, old.Namespace, old.Name, old.UID)
			require.NoError(t, err)
			p := groupPlan()
			require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
			assert.Empty(t, p.CreateRevisions)
			assert.Empty(t, p.UpdateDeployments)
			assert.Empty(t, p.ScaleDeployments)
			assert.Empty(t, p.DeleteDeployments)
			if missing == "" {
				assert.Empty(t, p.CreateDeployments)
			} else {
				require.Len(t, p.CreateDeployments, 1)
				restored := p.CreateDeployments[0]
				assert.Equal(t, missing, restored.Labels[k8s.MemberLabel])
				assert.NotEqual(t, members[missing].Name, restored.Name)
				assert.Equal(t, members[missing].Spec, restored.Spec)
				assert.Equal(t, members[missing].OwnerReferences, restored.OwnerReferences)
			}
			_, err = r.executeK8sOperations(ctx, logr.Discard(), next, p)
			require.NoError(t, err)
			state, err = k8s.GetDeploymentState(ctx, r.Client, old.Namespace, old.Name, old.UID)
			require.NoError(t, err)
			assert.Len(t, state.Members[ts.CurrentBuildID], 2)
			assert.Equal(t, savedData, state.Revisions[ts.CurrentBuildID].Object.Data.Raw)
			for memberName, original := range before {
				assert.Equal(t, original, state.Members[ts.CurrentBuildID][memberName])
			}
			status := newStateMapper(state, ts, k8s.ComputeWorkerDeploymentName(next)).mapToStatus(k8s.ComputeBuildID(next))
			require.NotNil(t, status.CurrentVersion)
			assert.Equal(t, k8s.NewObjectRef(state.Members[ts.CurrentBuildID][k8s.PrimaryMember]), status.CurrentVersion.Deployment)
			p = groupPlan()
			require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
			assert.Empty(t, p.CreateDeployments)
			assert.Empty(t, p.UpdateDeployments)
			assert.Empty(t, p.ScaleDeployments)
			assert.Empty(t, p.DeleteDeployments)
		})
	}
}

func TestGroupedNamingUpgradeRetirementUsesObservedNames(t *testing.T) {
	r, _ := newTestReconciler(nil)
	old := groupedWorker("release-123456789")
	saved, members := savedGroup(t, r, old)
	saved.Spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{Duration: time.Minute}
	saved.Spec.SunsetStrategy.DeleteDelay = &metav1.Duration{Duration: time.Minute}
	for memberName, member := range members {
		member.Name = k8s.ComputeVersionedDeploymentName(old.Name+"-"+memberName, old.Spec.WorkerOptions.UnsafeCustomBuildID)
		member.UID = types.UID("original-" + memberName)
	}
	next := groupedWorker("release-2")
	ts := registeredGroup(old.Spec.WorkerOptions.UnsafeCustomBuildID)
	ts.CurrentBuildID = k8s.ComputeBuildID(next)
	drained := time.Now().Add(-3 * time.Minute)
	ts.Versions[k8s.ComputeBuildID(old)].Status = v1alpha1.VersionStatusDrained
	ts.Versions[k8s.ComputeBuildID(old)].DrainedSince = &drained
	state := groupState(k8s.ComputeBuildID(old), saved, members)
	p := groupPlan()
	require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
	require.NoError(t, planGroupRetirements(context.Background(), approvedRetirements(t, p, nil), p))
	assert.Len(t, p.ScaleDeployments, len(members))
	refs := make([]*corev1.ObjectReference, 0, len(p.ScaleDeployments))
	for ref, replicas := range p.ScaleDeployments {
		assert.Zero(t, replicas)
		refs = append(refs, ref)
	}
	assert.ElementsMatch(t, []*corev1.ObjectReference{
		k8s.NewObjectRef(members[k8s.PrimaryMember]),
		k8s.NewObjectRef(members["activities"]),
	}, refs)
	for _, member := range members {
		zero := int32(0)
		member.Spec.Replicas = &zero
		member.Status = appsv1.DeploymentStatus{ObservedGeneration: member.Generation}
	}
	p = groupPlan()
	require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
	require.NoError(t, planGroupRetirements(context.Background(), approvedRetirements(t, p, nil), p))
	assert.ElementsMatch(t, []*appsv1.Deployment{members[k8s.PrimaryMember], members["activities"]}, p.DeleteDeployments)
}
