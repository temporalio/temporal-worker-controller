// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func groupedWorker(buildID string) *v1alpha1.WorkerDeployment {
	w := makeExecplanTWD("grouped-worker", "default")
	w.Spec.WorkerOptions.UnsafeCustomBuildID = buildID
	primary := w.Spec.DeploymentSpec()
	replicas := int32(2)
	primary.Replicas = &replicas
	primary.Template.Spec.ServiceAccountName = "workflow-account"
	primary.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "WORKFLOW_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "workflow-secret"}, Key: "token"}}}}
	w.Spec.Template = nil
	w.Spec.Deployment = &primary
	w.Spec.AdditionalDeployments = []v1alpha1.AdditionalDeployment{{Name: "activities", Replicas: 3, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "activity-account", Containers: []corev1.Container{{Name: "activity-worker", Image: "example.invalid/activity:v1", Env: []corev1.EnvVar{{Name: "ACTIVITY_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "activity-secret"}, Key: "token"}}}}}}}}}}
	w.Spec.ExpectedTaskQueues = []v1alpha1.ExpectedTaskQueue{{Name: "jobs", Kinds: []v1alpha1.TaskQueueKind{v1alpha1.TaskQueueWorkflow, v1alpha1.TaskQueueActivity}}}
	return w
}

func savedGroup(t *testing.T, r *WorkerDeploymentReconciler, w *v1alpha1.WorkerDeployment) (*k8s.GroupRevision, map[string]*appsv1.Deployment) {
	t.Helper()
	saved, err := k8s.NewGroupRevision(w, k8s.ComputeBuildID(w), r.Scheme)
	require.NoError(t, err)
	members, err := saved.Deployments(w, k8s.ComputeBuildID(w), v1alpha1.ConnectionSpec{HostPort: "test:7233"}, r.Scheme)
	require.NoError(t, err)
	return saved, members
}

func groupState(buildID string, saved *k8s.GroupRevision, members map[string]*appsv1.Deployment) *k8s.DeploymentState {
	return &k8s.DeploymentState{Revisions: map[string]*k8s.GroupRevision{buildID: saved}, Members: map[string]map[string]*appsv1.Deployment{buildID: members}, Deployments: map[string]*appsv1.Deployment{buildID: members[k8s.PrimaryMember]}, DeploymentRefs: map[string]*corev1.ObjectReference{buildID: k8s.NewObjectRef(members[k8s.PrimaryMember])}}
}

func groupPlan() *plan {
	return &plan{ScaleDeployments: make(map[*corev1.ObjectReference]uint32)}
}

func registeredGroup(buildID string) *temporal.TemporalWorkerState {
	return &temporal.TemporalWorkerState{Versions: map[string]*temporal.VersionInfo{buildID: {BuildID: buildID, Status: v1alpha1.VersionStatusInactive, RegisteredTaskQueues: []v1alpha1.ExpectedTaskQueue{{Name: "jobs", Kinds: []v1alpha1.TaskQueueKind{v1alpha1.TaskQueueWorkflow, v1alpha1.TaskQueueActivity}}}}}}
}

func markGroupAvailable(members map[string]*appsv1.Deployment) {
	for _, member := range members {
		member.Generation = 2
		count := *member.Spec.Replicas
		member.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: count, UpdatedReplicas: count, ReadyReplicas: count, AvailableReplicas: count, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}}
	}
}

func TestGroupedMembersPreserveSeparateCredentialsAndSelectors(t *testing.T) {
	r, _ := newTestReconciler(nil)
	w := groupedWorker("build-a")
	_, members := savedGroup(t, r, w)
	assert.Equal(t, "workflow-account", members["primary"].Spec.Template.Spec.ServiceAccountName)
	assert.Equal(t, "activity-account", members["activities"].Spec.Template.Spec.ServiceAccountName)
	assert.Equal(t, "workflow-secret", members["primary"].Spec.Template.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "activity-secret", members["activities"].Spec.Template.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Name)
	assert.EqualValues(t, 2, *members["primary"].Spec.Replicas)
	assert.EqualValues(t, 3, *members["activities"].Spec.Replicas)
	assert.NotEqual(t, members["primary"].Spec.Selector, members["activities"].Spec.Selector)
	for name, member := range members {
		assert.Equal(t, "build-a", member.Labels[k8s.BuildIDLabel])
		assert.Equal(t, name, member.Spec.Template.Labels[k8s.MemberLabel])
		assert.Equal(t, w.UID, metav1.GetControllerOf(member).UID)
	}
}

func TestGroupedPromotionRequiresEveryObservedMemberAndQueueKind(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]*appsv1.Deployment, *temporal.TemporalWorkerState)
	}{
		{"ready", func(map[string]*appsv1.Deployment, *temporal.TemporalWorkerState) {}},
		{"failed member", func(m map[string]*appsv1.Deployment, _ *temporal.TemporalWorkerState) {
			m["activities"].Status.AvailableReplicas = 0
		}},
		{"unobserved member", func(m map[string]*appsv1.Deployment, _ *temporal.TemporalWorkerState) {
			m["activities"].Status.ObservedGeneration = 1
		}},
		{"missing member", func(m map[string]*appsv1.Deployment, _ *temporal.TemporalWorkerState) { delete(m, "activities") }},
		{"wrong replica count", func(m map[string]*appsv1.Deployment, _ *temporal.TemporalWorkerState) {
			m["activities"].Status.ReadyReplicas = 2
		}},
		{"missing activity queue", func(_ map[string]*appsv1.Deployment, ts *temporal.TemporalWorkerState) {
			ts.Versions["build-a"].RegisteredTaskQueues[0].Kinds = []v1alpha1.TaskQueueKind{v1alpha1.TaskQueueWorkflow}
		}},
		{"different exact version", func(_ map[string]*appsv1.Deployment, ts *temporal.TemporalWorkerState) {
			ts.Versions["build-a"].BuildID = "build-b"
		}},
		{"unregistered exact version", func(_ map[string]*appsv1.Deployment, ts *temporal.TemporalWorkerState) {
			delete(ts.Versions, "build-a")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, _ := newTestReconciler(nil)
			w := groupedWorker("build-a")
			saved, members := savedGroup(t, r, w)
			markGroupAvailable(members)
			ts := registeredGroup("build-a")
			test.change(members, ts)
			w.Status.TargetVersion.HealthySince = &metav1.Time{Time: time.Now()}
			p := groupPlan()
			p.UpdateVersionConfig = &planner.VersionConfig{BuildID: "build-a", SetCurrent: true}
			p.startTestWorkflows = []startWorkflowConfig{{buildID: "build-a"}}
			require.NoError(t, r.planGroupedDeployments(w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, groupState("build-a", saved, members), p, true))
			assert.Equal(t, test.name == "ready", p.UpdateVersionConfig != nil)
			assert.Equal(t, test.name == "ready", w.Status.TargetVersion.HealthySince != nil)
			assert.Equal(t, test.name == "ready", len(p.startTestWorkflows) == 1)
		})
	}
}

func TestGroupedPartialCreationRestartsFromSavedRevision(t *testing.T) {
	w := groupedWorker("build-a")
	creates := 0
	r, _ := newTestReconcilerWithInterceptors(nil, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, member := obj.(*appsv1.Deployment); member {
			creates++
			if creates == 2 {
				return errors.New("interrupted after first member")
			}
			var revisions appsv1.ControllerRevisionList
			if err := c.List(ctx, &revisions); err != nil {
				return err
			}
			if len(revisions.Items) != 1 {
				return errors.New("member created before saved build")
			}
		}
		return c.Create(ctx, obj, opts...)
	}})
	state := &k8s.DeploymentState{}
	ts := &temporal.TemporalWorkerState{Versions: make(map[string]*temporal.VersionInfo)}
	p := groupPlan()
	require.NoError(t, r.planGroupedDeployments(w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, true))
	_, err := r.executeK8sOperations(context.Background(), logr.Discard(), w, p)
	require.ErrorContains(t, err, "interrupted after first member")
	state, err = k8s.GetDeploymentState(context.Background(), r.Client, w.Namespace, w.Name, w.UID)
	require.NoError(t, err)
	assert.Len(t, state.Revisions, 1)
	assert.Len(t, state.Members["build-a"], 1)
	p = groupPlan()
	require.NoError(t, r.planGroupedDeployments(w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, true))
	assert.Empty(t, p.CreateRevisions)
	require.Len(t, p.CreateDeployments, 1)
	_, err = r.executeK8sOperations(context.Background(), logr.Discard(), w, p)
	require.NoError(t, err)
	state, err = k8s.GetDeploymentState(context.Background(), r.Client, w.Namespace, w.Name, w.UID)
	require.NoError(t, err)
	assert.Len(t, state.Members["build-a"], 2)
}

func TestGroupedNewBuildDoesNotRewriteHistoricalMembers(t *testing.T) {
	r, _ := newTestReconciler(nil)
	a := groupedWorker("build-a")
	b := groupedWorker("build-b")
	b.Spec.AdditionalDeployments[0].Template.Spec.ServiceAccountName = "new-activity-account"
	sa, ma := savedGroup(t, r, a)
	sb, mb := savedGroup(t, r, b)
	state := groupState("build-a", sa, ma)
	state.Revisions["build-b"] = sb
	state.Members["build-b"] = mb
	state.Deployments["build-b"] = mb["primary"]
	delete(ma, "activities")
	c := groupedWorker("build-c")
	c.Spec.AdditionalDeployments = nil
	c.Spec.Deployment.Template.Spec.Containers[0].Image = "example.invalid/worker:v3"
	ts := registeredGroup("build-a")
	ts.CurrentBuildID = "build-a"
	ts.Versions["build-a"].Status = v1alpha1.VersionStatusDraining
	p := groupPlan()
	require.NoError(t, r.planGroupedDeployments(c, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, true))
	require.Len(t, p.CreateDeployments, 1)
	restored := p.CreateDeployments[0]
	assert.Equal(t, "build-a", restored.Labels[k8s.BuildIDLabel])
	assert.Equal(t, "activity-account", restored.Spec.Template.Spec.ServiceAccountName)
	assert.Equal(t, "example.invalid/activity:v1", restored.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "new-activity-account", mb["activities"].Spec.Template.Spec.ServiceAccountName)
	assert.Empty(t, p.UpdateDeployments)
	assert.Empty(t, p.ScaleDeployments)
}

func TestGroupedSameBuildChangedSettingsRejectedAndCutoverNotAdopted(t *testing.T) {
	r, _ := newTestReconciler(nil)
	w := groupedWorker("build-a")
	saved, members := savedGroup(t, r, w)
	ts := registeredGroup("build-a")
	changed := w.DeepCopy()
	changed.Spec.AdditionalDeployments[0].Replicas = 4
	err := r.planGroupedDeployments(changed, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, groupState("build-a", saved, members), groupPlan(), true)
	assert.ErrorContains(t, err, "different saved settings")
	state := &k8s.DeploymentState{Deployments: map[string]*appsv1.Deployment{"build-a": members["primary"]}}
	err = r.planGroupedDeployments(w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, groupPlan(), true)
	assert.ErrorContains(t, err, "without saved grouped settings")
}

func TestGroupedRetirementWaitsForGlobalDrainAndStopsAllMembers(t *testing.T) {
	r, _ := newTestReconciler(nil)
	w := groupedWorker("build-a")
	saved, members := savedGroup(t, r, w)
	saved.Spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{Duration: time.Minute}
	saved.Spec.SunsetStrategy.DeleteDelay = &metav1.Duration{Duration: time.Minute}
	state := groupState("build-a", saved, members)
	next := groupedWorker("build-b")
	ts := registeredGroup("build-a")
	ts.CurrentBuildID = "build-b"
	ts.Versions["build-a"].Status = v1alpha1.VersionStatusDraining
	p := groupPlan()
	require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
	assert.Empty(t, p.ScaleDeployments)
	assert.Empty(t, p.DeleteDeployments)
	drained := time.Now().Add(-3 * time.Minute)
	ts.Versions["build-a"].Status = v1alpha1.VersionStatusDrained
	ts.Versions["build-a"].DrainedSince = &drained
	p = groupPlan()
	require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
	assert.Len(t, p.ScaleDeployments, 2)
	assert.Empty(t, p.DeleteDeployments)
	for _, replicas := range p.ScaleDeployments {
		assert.Zero(t, replicas)
	}
	for _, member := range members {
		zero := int32(0)
		member.Spec.Replicas = &zero
		member.Status = appsv1.DeploymentStatus{ObservedGeneration: member.Generation}
	}
	p = groupPlan()
	require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
	assert.Len(t, p.DeleteDeployments, 2)
	assert.Len(t, p.MarkRevisionsForDeletion, 1)
	assert.Empty(t, p.DeleteRevisions)
	saved.Object.Annotations = map[string]string{k8s.GroupDeletionAnnotation: "true"}
	state.Members["build-a"] = nil
	p = groupPlan()
	require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
	assert.Len(t, p.DeleteRevisions, 1)
}

func TestGroupedRollbackRestoresSavedCountsBeforeRouting(t *testing.T) {
	r, _ := newTestReconciler(nil)
	w := groupedWorker("build-a")
	saved, members := savedGroup(t, r, w)
	for _, member := range members {
		zero := int32(0)
		member.Spec.Replicas = &zero
	}
	state := groupState("build-a", saved, members)
	ts := registeredGroup("build-a")
	ts.CurrentBuildID = "build-b"
	ts.Versions["build-a"].Status = v1alpha1.VersionStatusDrained
	p := groupPlan()
	p.UpdateVersionConfig = &planner.VersionConfig{BuildID: "build-a", SetCurrent: true}
	require.NoError(t, r.planGroupedDeployments(w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, true))
	assert.Nil(t, p.UpdateVersionConfig)
	counts := map[string]uint32{}
	for ref, count := range p.ScaleDeployments {
		counts[ref.Name] = count
	}
	assert.EqualValues(t, 2, counts[members["primary"].Name])
	assert.EqualValues(t, 3, counts[members["activities"].Name])
	for _, member := range members {
		count := int32(counts[member.Name])
		member.Spec.Replicas = &count
	}
	markGroupAvailable(members)
	p = groupPlan()
	p.UpdateVersionConfig = &planner.VersionConfig{BuildID: "build-a", SetCurrent: true}
	require.NoError(t, r.planGroupedDeployments(w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, true))
	assert.NotNil(t, p.UpdateVersionConfig)
}

func TestGroupedTemporalDeleteFailureKeepsEveryMemberAndSavedRevision(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			r, _ := newTestReconciler(nil)
			w := groupedWorker("build-a")
			saved, members := savedGroup(t, r, w)
			w.Status.DeprecatedVersions = []*v1alpha1.DeprecatedWorkerDeploymentVersion{{BaseWorkerDeploymentVersion: v1alpha1.BaseWorkerDeploymentVersion{BuildID: "build-a", Status: v1alpha1.VersionStatusDrained}}}
			p := groupPlan()
			for _, member := range members {
				p.DeleteDeployments = append(p.DeleteDeployments, member)
			}
			p.DeleteRevisions = []*appsv1.ControllerRevision{saved.Object}
			var deleteErr error
			if fail {
				deleteErr = errors.New("still has pollers")
			}
			handle := newPruneStubHandle(deleteErr)
			r.deleteDeprecatedVersions(context.Background(), logr.Discard(), w, handle, newStubTemporalClientWithHandle(handle), p)
			assert.Len(t, handle.deletedVersions, 1)
			if fail {
				assert.Empty(t, p.DeleteDeployments)
				assert.Empty(t, p.DeleteRevisions)
			} else {
				assert.Len(t, p.DeleteDeployments, 2)
				assert.Len(t, p.DeleteRevisions, 1)
			}
		})
	}
}

func TestGroupedCleanupResumesAfterTemporalDeletionAndMemberFailure(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(map[int]string{1: "before first member", 2: "after first member"}[failAt], func(t *testing.T) {
			setup, _ := newTestReconciler(nil)
			old := groupedWorker("build-a")
			saved, members := savedGroup(t, setup, old)
			before := k8s.HashString(string(saved.Object.Data.Raw))
			next := groupedWorker("build-b")
			objects := []client.Object{next, saved.Object}
			for _, member := range members {
				zero := int32(0)
				member.Spec.Replicas = &zero
				objects = append(objects, member)
			}
			deletes := 0
			fail := true
			r, _ := newTestReconcilerWithInterceptors(objects, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, member := obj.(*appsv1.Deployment); member {
					deletes++
					if fail && deletes == failAt {
						return errors.New("member delete interrupted")
					}
				}
				return c.Delete(ctx, obj, opts...)
			}})
			ts := registeredGroup("build-a")
			ts.CurrentBuildID = "build-b"
			ts.Versions["build-a"].Status = v1alpha1.VersionStatusDrained
			drained := time.Now().Add(-time.Hour)
			ts.Versions["build-a"].DrainedSince = &drained
			next.Status = statusWithDeprecated("build-b", nil, drainedVersion("build-a", members["primary"], metav1.NewTime(drained)))
			p := groupPlan()
			require.NoError(t, r.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, groupState("build-a", saved, members), p, false))
			handle := newPruneStubHandle(nil)
			tc := newStubTemporalClientWithHandle(handle)
			require.ErrorContains(t, r.executePlan(context.Background(), logr.Discard(), next, tc, p), "member delete interrupted")
			assert.Equal(t, []string{"build-a"}, handle.deletedVersions)
			var retained appsv1.ControllerRevision
			require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(saved.Object), &retained))
			assert.Equal(t, "true", retained.Annotations[k8s.GroupDeletionAnnotation])
			assert.Equal(t, before, k8s.HashString(string(retained.Data.Raw)))

			// The next process sees no Temporal version after the successful delete.
			// It must finish deleting members instead of recreating active workers.
			fail = false
			restarted := &WorkerDeploymentReconciler{Client: r.Client, Scheme: r.Scheme, Recorder: r.Recorder}
			ts = &temporal.TemporalWorkerState{CurrentBuildID: "build-b", Versions: make(map[string]*temporal.VersionInfo)}
			state, err := k8s.GetDeploymentState(context.Background(), r.Client, next.Namespace, next.Name, next.UID)
			require.NoError(t, err)
			next.Status = *newStateMapper(state, ts, k8s.ComputeWorkerDeploymentName(next)).mapToStatus("build-b")
			applyGroupedStatus(&next.Status, state, ts)
			p = groupPlan()
			require.NoError(t, restarted.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
			assert.Empty(t, p.CreateDeployments)
			assert.Empty(t, p.ScaleDeployments)
			require.NoError(t, restarted.executePlan(context.Background(), logr.Discard(), next, tc, p))

			state, err = k8s.GetDeploymentState(context.Background(), r.Client, next.Namespace, next.Name, next.UID)
			require.NoError(t, err)
			assert.Empty(t, state.Members)
			next.Status = *newStateMapper(state, ts, k8s.ComputeWorkerDeploymentName(next)).mapToStatus("build-b")
			applyGroupedStatus(&next.Status, state, ts)
			p = groupPlan()
			require.NoError(t, restarted.planGroupedDeployments(next, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, false))
			require.Len(t, p.DeleteRevisions, 1)
			require.NoError(t, restarted.executePlan(context.Background(), logr.Discard(), next, tc, p))
			state, err = k8s.GetDeploymentState(context.Background(), r.Client, next.Namespace, next.Name, next.UID)
			require.NoError(t, err)
			assert.Empty(t, state.Revisions)
			assert.Equal(t, []string{"build-a"}, handle.deletedVersions)
		})
	}
}

func TestGroupedConfiguredGateAlsoProtectsRollback(t *testing.T) {
	for _, workflowStatus := range []v1alpha1.WorkflowExecutionStatus{v1alpha1.WorkflowExecutionStatusRunning, v1alpha1.WorkflowExecutionStatusFailed, v1alpha1.WorkflowExecutionStatusCompleted} {
		t.Run(string(workflowStatus), func(t *testing.T) {
			r, _ := newTestReconciler(nil)
			w := groupedWorker("build-a")
			w.Spec.RolloutStrategy.Gate = &v1alpha1.GateWorkflowConfig{WorkflowType: "ValidateBuild"}
			saved, members := savedGroup(t, r, w)
			markGroupAvailable(members)
			ts := registeredGroup("build-a")
			ts.CurrentBuildID = "build-b"
			w.Status.TargetVersion.TestWorkflows = []v1alpha1.WorkflowExecution{{TaskQueue: "jobs", Status: workflowStatus}}
			p := groupPlan()
			p.UpdateVersionConfig = &planner.VersionConfig{BuildID: "build-a", SetCurrent: true}
			require.NoError(t, r.planGroupedDeployments(w, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, groupState("build-a", saved, members), p, true))
			assert.Equal(t, workflowStatus == v1alpha1.WorkflowExecutionStatusCompleted, p.UpdateVersionConfig != nil)
		})
	}
}

func TestGroupedConnectionRotationUpdatesAllMembersWithoutTemplateReplacement(t *testing.T) {
	setup, _ := newTestReconciler(nil)
	w := groupedWorker("build-a")
	saved, members := savedGroup(t, setup, w)
	markGroupAvailable(members)
	objects := []client.Object{w, saved.Object}
	for _, member := range members {
		objects = append(objects, member)
	}
	r, _ := newTestReconciler(objects)
	w.Status.TargetVersion.BaseWorkerDeploymentVersion = baseVersion("build-a", members["primary"], v1alpha1.VersionStatusInactive)
	w.Status.TargetVersion.HealthySince = &metav1.Time{Time: time.Now()}
	connection := v1alpha1.ConnectionSpec{HostPort: "new-address:7233", APIKeySecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "rotated-temporal-key"}, Key: "key"}}
	p, err := r.generatePlan(context.Background(), logr.Discard(), w, connection, registeredGroup("build-a"))
	require.NoError(t, err)
	require.Len(t, p.UpdateDeployments, 2)
	assert.Nil(t, p.UpdateVersionConfig)
	assert.Nil(t, w.Status.TargetVersion.HealthySince)
	for _, member := range p.UpdateDeployments {
		assert.Equal(t, k8s.ComputeConnectionSpecHash(connection), member.Spec.Template.Annotations[k8s.ConnectionSpecHashAnnotation])
		assert.Equal(t, members[member.Labels[k8s.MemberLabel]].Spec.Template.Spec.ServiceAccountName, member.Spec.Template.Spec.ServiceAccountName)
		assert.Equal(t, members[member.Labels[k8s.MemberLabel]].Spec.Template.Spec.Containers[0].Image, member.Spec.Template.Spec.Containers[0].Image)
	}
	assert.Empty(t, p.CreateRevisions)
}

func TestGroupedSavedRecordsRejectInvalidRestoredSettings(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*v1alpha1.WorkerDeploymentSpec)
	}{
		{"missing primary count", func(s *v1alpha1.WorkerDeploymentSpec) { s.Deployment.Replicas = nil }},
		{"zero member count", func(s *v1alpha1.WorkerDeploymentSpec) { s.AdditionalDeployments[0].Replicas = 0 }},
		{"duplicate member", func(s *v1alpha1.WorkerDeploymentSpec) {
			s.AdditionalDeployments = append(s.AdditionalDeployments, s.AdditionalDeployments[0])
		}},
		{"missing sunset delay", func(s *v1alpha1.WorkerDeploymentSpec) { s.SunsetStrategy.DeleteDelay = nil }},
		{"missing expected queue", func(s *v1alpha1.WorkerDeploymentSpec) { s.ExpectedTaskQueues = nil }},
		{"invalid queue kind", func(s *v1alpha1.WorkerDeploymentSpec) {
			s.ExpectedTaskQueues[0].Kinds = []v1alpha1.TaskQueueKind{"Other"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			setup, _ := newTestReconciler(nil)
			w := groupedWorker("build-a")
			saved, _ := savedGroup(t, setup, w)
			test.change(&saved.Spec)
			data, err := json.Marshal(saved.Spec)
			require.NoError(t, err)
			saved.Object.Data.Raw = data
			r, _ := newTestReconciler([]client.Object{saved.Object})
			_, err = k8s.GetDeploymentState(context.Background(), r.Client, w.Namespace, w.Name, w.UID)
			assert.ErrorContains(t, err, "invalid saved grouped build")
		})
	}
}

func TestGroupedCandidateCSupersedesPartiallyCreatedBWithoutReplacingAOrB(t *testing.T) {
	r, _ := newTestReconciler(nil)
	a := groupedWorker("build-a")
	b := groupedWorker("build-b")
	b.Spec.AdditionalDeployments[0].Replicas = 4
	b.Spec.AdditionalDeployments[0].Template.Spec.Containers[0].Image = "example.invalid/activity:v2"
	savedA, membersA := savedGroup(t, r, a)
	savedB, membersB := savedGroup(t, r, b)
	delete(membersB, "activities")
	state := groupState("build-a", savedA, membersA)
	state.Revisions["build-b"] = savedB
	state.Members["build-b"] = membersB
	state.Deployments["build-b"] = membersB["primary"]
	c := groupedWorker("build-c")
	c.Spec.AdditionalDeployments[0].Replicas = 5
	c.Spec.AdditionalDeployments[0].Template.Spec.Containers[0].Image = "example.invalid/activity:v3"
	ts := registeredGroup("build-a")
	ts.CurrentBuildID = "build-a"
	ts.Versions["build-a"].Status = v1alpha1.VersionStatusCurrent
	p := groupPlan()
	require.NoError(t, r.planGroupedDeployments(c, v1alpha1.ConnectionSpec{HostPort: "test:7233"}, ts, state, p, true))
	require.Len(t, p.CreateRevisions, 1)
	assert.Equal(t, "build-c", p.CreateRevisions[0].Labels[k8s.BuildIDLabel])
	require.Len(t, p.CreateDeployments, 3)
	for _, deployment := range p.CreateDeployments {
		switch deployment.Labels[k8s.BuildIDLabel] {
		case "build-b":
			assert.Equal(t, "activities", deployment.Labels[k8s.MemberLabel])
			assert.EqualValues(t, 4, *deployment.Spec.Replicas)
			assert.Equal(t, "example.invalid/activity:v2", deployment.Spec.Template.Spec.Containers[0].Image)
		case "build-c":
			if deployment.Labels[k8s.MemberLabel] == "activities" {
				assert.EqualValues(t, 5, *deployment.Spec.Replicas)
				assert.Equal(t, "example.invalid/activity:v3", deployment.Spec.Template.Spec.Containers[0].Image)
			}
		default:
			t.Errorf("unexpected build %q", deployment.Labels[k8s.BuildIDLabel])
		}
	}
	assert.Equal(t, "example.invalid/activity:v1", membersA["activities"].Spec.Template.Spec.Containers[0].Image)
	assert.Empty(t, p.UpdateDeployments)
	assert.Empty(t, p.ScaleDeployments)
	assert.Empty(t, p.DeleteDeployments)
}
