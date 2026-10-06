// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package planner

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func groupDeployment(buildID, group string, replicas int32) *appsv1.Deployment {
	d := createDeploymentWithDefaultConnectionSpecHash(replicas)
	d.Name = "wd-" + buildID
	d.Labels = map[string]string{k8s.BuildIDLabel: buildID}
	if group != "" {
		d.Name += "-" + group
		d.Labels[k8s.WorkerGroupLabel] = group
	}
	return d
}

func names(ds []*appsv1.Deployment) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

func scaleNames(scales map[*corev1.ObjectReference]uint32) map[string]uint32 {
	out := make(map[string]uint32, len(scales))
	for ref, replicas := range scales {
		out[ref.Name] = replicas
	}
	return out
}

func deprecatedVersion(buildID string, status temporaliov1alpha1.VersionStatus, withDefault bool) *temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
	v := &temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
		BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: buildID, Status: status},
	}
	if withDefault {
		v.Deployment = &corev1.ObjectReference{Name: "wd-" + buildID}
	}
	return v
}

func sunsetSpec(t *testing.T) *temporaliov1alpha1.WorkerDeploymentSpec {
	t.Helper()
	spec := &temporaliov1alpha1.WorkerDeploymentSpec{}
	require.NoError(t, spec.Default(context.Background()))
	spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{}
	spec.SunsetStrategy.DeleteDelay = &metav1.Duration{}
	return spec
}

func TestGetDeleteDeployments_Groups(t *testing.T) {
	drainedAt := &metav1.Time{Time: time.Now().Add(-time.Hour)}

	t.Run("drained version deletes every group with the default last", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("old", "", 0), groupDeployment("old", "activities", 0))
		v := deprecatedVersion("old", temporaliov1alpha1.VersionStatusDrained, true)
		v.DrainedSince = drainedAt
		v.EligibleForDeletion = true
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{v}}

		assert.Equal(t, []string{"wd-old-activities", "wd-old"}, names(getDeleteDeployments(state, status, sunsetSpec(t), true)))
	})

	t.Run("drained version waits until every group is scaled to zero", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("old", "", 0), groupDeployment("old", "activities", 1))
		v := deprecatedVersion("old", temporaliov1alpha1.VersionStatusDrained, true)
		v.DrainedSince = drainedAt
		v.EligibleForDeletion = true
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{v}}

		assert.Empty(t, getDeleteDeployments(state, status, sunsetSpec(t), true))
	})

	t.Run("inactive version waits until every group has no pods", func(t *testing.T) {
		busy := groupDeployment("old", "activities", 0)
		busy.Status.Replicas = 1
		state := k8s.NewDeploymentState(groupDeployment("old", "", 0), busy)
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
			deprecatedVersion("old", temporaliov1alpha1.VersionStatusInactive, true),
		}}

		assert.Empty(t, getDeleteDeployments(state, status, sunsetSpec(t), true))
	})

	t.Run("named groups are deleted after the default group is already gone", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("old", "activities", 0))
		status := &temporaliov1alpha1.WorkerDeploymentStatus{DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{
			deprecatedVersion("old", temporaliov1alpha1.VersionStatusNotRegistered, false),
		}}

		assert.Equal(t, []string{"wd-old-activities"}, names(getDeleteDeployments(state, status, sunsetSpec(t), true)))
	})
}

func TestGetScaleDeployments_DeprecatedGroups(t *testing.T) {
	status := func(v *temporaliov1alpha1.DeprecatedWorkerDeploymentVersion) *temporaliov1alpha1.WorkerDeploymentStatus {
		return &temporaliov1alpha1.WorkerDeploymentStatus{
			TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
				BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: "new"},
			},
			DeprecatedVersions: []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{v},
		}
	}

	t.Run("drained version scales every group to zero", func(t *testing.T) {
		v := deprecatedVersion("old", temporaliov1alpha1.VersionStatusDrained, true)
		v.DrainedSince = &metav1.Time{Time: time.Now().Add(-time.Hour)}
		state := k8s.NewDeploymentState(groupDeployment("old", "", 2), groupDeployment("old", "activities", 3))

		assert.Equal(t, map[string]uint32{"wd-old": 0, "wd-old-activities": 0},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(v), sunsetSpec(t))))
	})

	t.Run("inactive non-target version scales every group to zero", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("old", "", 2), groupDeployment("old", "activities", 3))

		assert.Equal(t, map[string]uint32{"wd-old": 0, "wd-old-activities": 0},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(deprecatedVersion("old", temporaliov1alpha1.VersionStatusInactive, true)), sunsetSpec(t))))
	})

	t.Run("draining version scales every group at zero back up", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("old", "", 0), groupDeployment("old", "activities", 0))

		assert.Equal(t, map[string]uint32{"wd-old": 1, "wd-old-activities": 1},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(deprecatedVersion("old", temporaliov1alpha1.VersionStatusDraining, true)), sunsetSpec(t))))
	})

	t.Run("named groups are scaled after the default group is already gone", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("old", "activities", 3))

		assert.Equal(t, map[string]uint32{"wd-old-activities": 0},
			scaleNames(getScaleDeployments(logr.Discard(), state, status(deprecatedVersion("old", temporaliov1alpha1.VersionStatusInactive, false)), sunsetSpec(t))))
	})
}

func TestGetUpdateDeployments_ConnectionDriftUpdatesEveryGroup(t *testing.T) {
	state := k8s.NewDeploymentState(groupDeployment("v1", "", 1), groupDeployment("v1", "activities", 1))
	status := &temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: "v1"},
		},
	}
	spec := &temporaliov1alpha1.WorkerDeploymentSpec{}

	updates := getUpdateDeployments(state, status, spec, createOutdatedConnectionSpec())

	assert.ElementsMatch(t, []string{"wd-v1", "wd-v1-activities"}, names(updates))
}

func groupTemplate(image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: image}}}}
}

// groupedSpec returns a spec with a default group and the named groups, each running image.
// groupedSpec returns a spec with the given groups, or a single deployment when there are none.
func groupedSpec(t *testing.T, image string, replicas map[string]*int32, groups ...string) *temporaliov1alpha1.WorkerDeploymentSpec {
	t.Helper()
	spec := sunsetSpec(t)
	if len(groups) == 0 {
		spec.Deployment = &appsv1.DeploymentSpec{Replicas: replicas[temporaliov1alpha1.DefaultWorkerGroupName], Template: groupTemplate(image)}
	}
	for _, name := range groups {
		spec.WorkerGroups = append(spec.WorkerGroups, temporaliov1alpha1.WorkerGroup{
			Name:       name,
			Deployment: appsv1.DeploymentSpec{Replicas: replicas[name], Template: groupTemplate(image)},
		})
	}
	return spec
}

func targetStatus(buildID string) *temporaliov1alpha1.WorkerDeploymentStatus {
	return &temporaliov1alpha1.WorkerDeploymentStatus{
		TargetVersion: temporaliov1alpha1.TargetWorkerDeploymentVersion{
			BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: buildID},
		},
	}
}

func TestGetCreateDeploymentWorkerGroups(t *testing.T) {
	cappedStatus := func() *temporaliov1alpha1.WorkerDeploymentStatus {
		s := targetStatus("new")
		s.DeprecatedVersions = []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{deprecatedVersion("old", temporaliov1alpha1.VersionStatusDraining, true)}
		return s
	}

	tests := []struct {
		name        string
		state       *k8s.DeploymentState
		status      *temporaliov1alpha1.WorkerDeploymentStatus
		spec        *temporaliov1alpha1.WorkerDeploymentSpec
		maxVersions int32
		wantGroups  []string
		wantBlocked bool
	}{
		{
			name:        "new version without groups",
			state:       k8s.NewDeploymentState(),
			status:      targetStatus("new"),
			spec:        groupedSpec(t, "worker:v1", nil),
			maxVersions: 75,
			wantGroups:  []string{temporaliov1alpha1.DefaultWorkerGroupName},
		},
		{
			name:        "new version creates every group",
			state:       k8s.NewDeploymentState(),
			status:      targetStatus("new"),
			spec:        groupedSpec(t, "worker:v1", nil, "batch", "activities"),
			maxVersions: 75,
			wantGroups:  []string{"activities", "batch"},
		},
		{
			name:        "new version is held back at the version cap",
			state:       k8s.NewDeploymentState(),
			status:      cappedStatus(),
			spec:        groupedSpec(t, "worker:v1", nil, "activities"),
			maxVersions: 1,
		},
		{
			name:        "group added to an existing version ignores the version cap",
			state:       k8s.NewDeploymentState(groupDeployment("new", "workflows", 1), groupDeployment("new", "activities", 1)),
			status:      cappedStatus(),
			spec:        groupedSpec(t, "worker:v1", nil, "workflows", "activities", "batch"),
			maxVersions: 1,
			wantGroups:  []string{"batch"},
		},
		{
			name:        "complete version creates nothing",
			state:       k8s.NewDeploymentState(groupDeployment("new", "workflows", 1), groupDeployment("new", "activities", 1)),
			status:      targetStatus("new"),
			spec:        groupedSpec(t, "worker:v1", nil, "workflows", "activities"),
			maxVersions: 75,
		},
		{
			name:        "groups added to a version without groups are blocked",
			state:       k8s.NewDeploymentState(groupDeployment("new", "", 1)),
			status:      targetStatus("new"),
			spec:        groupedSpec(t, "worker:v1", nil, "activities"),
			maxVersions: 75,
			wantBlocked: true,
		},
		{
			name:        "groups removed from a version with groups are blocked",
			state:       k8s.NewDeploymentState(groupDeployment("new", "workflows", 1)),
			status:      targetStatus("new"),
			spec:        groupedSpec(t, "worker:v1", nil),
			maxVersions: 75,
			wantBlocked: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups, blockedReason := getCreateDeploymentWorkerGroups(tt.state, tt.status, tt.spec, tt.maxVersions)
			assert.Equal(t, tt.wantGroups, groups)
			assert.Equal(t, tt.wantBlocked, blockedReason != "")
		})
	}
}

func TestGetDeleteWorkerGroupDeployments(t *testing.T) {
	t.Run("groups removed from the spec are deleted from the target version", func(t *testing.T) {
		state := k8s.NewDeploymentState(
			groupDeployment("v1", "workflows", 1),
			groupDeployment("v1", "activities", 1),
			groupDeployment("v1", "batch", 1),
			groupDeployment("old", "batch", 1),
		)
		assert.Equal(t, []string{"wd-v1-batch"}, names(getDeleteWorkerGroupDeployments(state, targetStatus("v1"), groupedSpec(t, "worker:v1", nil, "workflows", "activities"))))
	})

	t.Run("switching between groups and no groups under one build deletes nothing", func(t *testing.T) {
		grouped := k8s.NewDeploymentState(groupDeployment("v1", "workflows", 1), groupDeployment("v1", "activities", 1))
		assert.Empty(t, getDeleteWorkerGroupDeployments(grouped, targetStatus("v1"), groupedSpec(t, "worker:v1", nil)))
		single := k8s.NewDeploymentState(groupDeployment("v1", "", 1))
		assert.Empty(t, getDeleteWorkerGroupDeployments(single, targetStatus("v1"), groupedSpec(t, "worker:v1", nil, "activities")))
	})

	t.Run("single-group version is left alone", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("v1", "", 1))
		assert.Empty(t, getDeleteWorkerGroupDeployments(state, targetStatus("v1"), groupedSpec(t, "worker:v1", nil)))
	})
}

func TestGetScaleDeployments_CurrentAndTargetGroups(t *testing.T) {
	replicas := map[string]*int32{"workflows": int32Ptr(2), "activities": int32Ptr(4)}

	t.Run("current version scales each group to its own replicas", func(t *testing.T) {
		state := k8s.NewDeploymentState(
			groupDeployment("v1", "workflows", 1),
			groupDeployment("v1", "activities", 1),
			groupDeployment("v1", "retired", 1),
		)
		status := targetStatus("v1")
		status.CurrentVersion = &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: status.TargetVersion.BaseWorkerDeploymentVersion}

		assert.Equal(t, map[string]uint32{"wd-v1-workflows": 2, "wd-v1-activities": 4},
			scaleNames(getScaleDeployments(logr.Discard(), state, status, groupedSpec(t, "worker:v1", replicas, "workflows", "activities"))),
			"a group no longer in the spec is left alone")
	})

	t.Run("target group managed by a scaler is scaled up from zero", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("v2", "workflows", 2), groupDeployment("v2", "autoscaled", 0))

		assert.Equal(t, map[string]uint32{"wd-v2-autoscaled": 1},
			scaleNames(getScaleDeployments(logr.Discard(), state, targetStatus("v2"), groupedSpec(t, "worker:v1", replicas, "workflows", "autoscaled"))))
	})

	t.Run("draining group at zero is scaled up to its own replicas", func(t *testing.T) {
		state := k8s.NewDeploymentState(groupDeployment("old", "workflows", 0), groupDeployment("old", "activities", 0))
		status := targetStatus("new")
		status.DeprecatedVersions = []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{deprecatedVersion("old", temporaliov1alpha1.VersionStatusDraining, false)}

		assert.Equal(t, map[string]uint32{"wd-old-workflows": 2, "wd-old-activities": 4},
			scaleNames(getScaleDeployments(logr.Discard(), state, status, groupedSpec(t, "worker:v1", replicas, "workflows", "activities"))))
	})
}

func driftGroupDeployment(buildID, group, image string) *appsv1.Deployment {
	d := createDeploymentForDriftTest(1, buildID, image)
	d.Name = "wd-" + buildID + "-" + group
	d.Labels[k8s.WorkerGroupLabel] = group
	d.Spec.Selector.MatchLabels[k8s.WorkerGroupLabel] = group
	return d
}

func TestGetUpdateDeployments_PodTemplateDriftPerGroup(t *testing.T) {
	state := k8s.NewDeploymentState(
		driftGroupDeployment("custom", "workflows", "worker:v1"),
		driftGroupDeployment("custom", "activities", "worker:v1"),
	)
	spec := groupedSpec(t, "worker:v1", nil, "workflows", "activities")
	spec.WorkerOptions.UnsafeCustomBuildID = "custom"
	spec.WorkerGroups[1].Deployment.Template = groupTemplate("activities:v2")

	updates := getUpdateDeployments(state, targetStatus("custom"), spec, createDefaultConnectionSpec())

	require.Equal(t, []string{"wd-custom-activities"}, names(updates))
	updated := updates[0]
	assert.Equal(t, "activities:v2", updated.Spec.Template.Spec.Containers[0].Image, "rebuilt from the group's own template")
	assert.Equal(t, "activities", updated.Spec.Selector.MatchLabels[k8s.WorkerGroupLabel])
	assert.Equal(t, "activities", updated.Spec.Template.Labels[k8s.WorkerGroupLabel])
}

func TestGetUpdateDeployments_StrategyPerGroup(t *testing.T) {
	state := k8s.NewDeploymentState(groupDeployment("v1", "workflows", 1), groupDeployment("v1", "activities", 1), groupDeployment("v1", "retired", 1))
	spec := groupedSpec(t, "worker:v1", nil, "workflows", "activities")
	spec.WorkerGroups[1].Deployment.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}

	updates := getUpdateDeployments(state, targetStatus("v1"), spec, createDefaultConnectionSpec())

	require.Equal(t, []string{"wd-v1-activities"}, names(updates), "workflows already matches and a retired group is left alone")
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, updates[0].Spec.Strategy.Type)
}

func TestGetTestWorkflows_MultiGroupTargetWaitsForHealth(t *testing.T) {
	status := targetStatus("v1")
	status.TargetVersion.Status = temporaliov1alpha1.VersionStatusInactive
	status.TargetVersion.TaskQueues = []temporaliov1alpha1.TaskQueue{{Name: "orders"}}
	status.TargetVersion.WorkerGroups = []temporaliov1alpha1.WorkerGroupStatus{{Name: "workflows"}, {Name: "activities"}}
	config := &Config{RolloutStrategy: temporaliov1alpha1.RolloutStrategy{Gate: &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: "Gate"}}}

	assert.Empty(t, getTestWorkflows(status, config, "ns/wd", nil, false), "a group may not be polling yet")

	status.TargetVersion.HealthySince = &metav1.Time{Time: time.Now()}
	assert.Len(t, getTestWorkflows(status, config, "ns/wd", nil, false), 1)
}

func groupWRT(name, group string, template func(string, string) temporaliov1alpha1.WorkerResourceTemplate) temporaliov1alpha1.WorkerResourceTemplate {
	wrt := template(name, "wd")
	wrt.Spec.WorkerGroup = group
	return wrt
}

func renderedScaleTarget(t *testing.T, apply WorkerResourceApply) string {
	t.Helper()
	spec := apply.Resource.Object["spec"].(map[string]interface{})
	return spec["scaleTargetRef"].(map[string]interface{})["name"].(string)
}

func TestGetWorkerResourceApplies_Groups(t *testing.T) {
	state := k8s.NewDeploymentState(
		groupDeployment("v1", "workflows", 1),
		groupDeployment("v1", "activities", 1),
		groupDeployment("v2", "workflows", 1),
		groupDeployment("v2", "activities", 1),
		groupDeployment("old", "", 1),
	)

	t.Run("group WRT renders once per build that has the group", func(t *testing.T) {
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{groupWRT("hpa", "activities", createTestWRT)}
		applies := getWorkerResourceApplies(logr.Discard(), wrts, state, "ns", nil, nil, false)

		targets := map[string]string{}
		for _, a := range applies {
			targets[a.BuildID] = renderedScaleTarget(t, a)
		}
		assert.Equal(t, map[string]string{"v1": "wd-v1-activities", "v2": "wd-v2-activities"}, targets)
	})

	t.Run("WRT without a group targets only versions without groups", func(t *testing.T) {
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{groupWRT("hpa", "", createTestWRT)}
		applies := getWorkerResourceApplies(logr.Discard(), wrts, state, "ns", nil, nil, false)

		targets := map[string]string{}
		for _, a := range applies {
			targets[a.BuildID] = renderedScaleTarget(t, a)
		}
		assert.Equal(t, map[string]string{"old": "wd-old"}, targets)
	})

	t.Run("PDB selects only its group's pods", func(t *testing.T) {
		activities := groupDeployment("v1", "activities", 1)
		activities.Spec.Selector = &metav1.LabelSelector{MatchLabels: k8s.ComputeWorkerGroupSelectorLabels("wd", "v1", "activities")}
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{groupWRT("pdb", "activities", createTestPDBWRT)}

		applies := getWorkerResourceApplies(logr.Discard(), wrts, k8s.NewDeploymentState(activities), "ns", nil, nil, false)
		require.Len(t, applies, 1)

		spec := applies[0].Resource.Object["spec"].(map[string]interface{})
		matchLabels := spec["selector"].(map[string]interface{})["matchLabels"].(map[string]interface{})
		assert.Equal(t, "activities", matchLabels[k8s.WorkerGroupLabel])
	})

	t.Run("group Deployment being removed is not re-rendered", func(t *testing.T) {
		removing := state.WorkerGroupDeployments["v1"]["activities"]
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{groupWRT("hpa", "activities", createTestWRT)}
		applies := getWorkerResourceApplies(logr.Discard(), wrts, state, "ns", []*appsv1.Deployment{removing}, nil, false)

		require.Len(t, applies, 1)
		assert.Equal(t, "v2", applies[0].BuildID)
	})
}

func TestGetDeleteWorkerResources_Groups(t *testing.T) {
	state := k8s.NewDeploymentState(
		groupDeployment("v1", "workflows", 1),
		groupDeployment("v1", "activities", 1),
		groupDeployment("v1", "batch", 1),
	)
	withStatus := func(wrt temporaliov1alpha1.WorkerResourceTemplate, buildIDs ...string) temporaliov1alpha1.WorkerResourceTemplate {
		for _, b := range buildIDs {
			wrt.Status.Versions = append(wrt.Status.Versions, temporaliov1alpha1.WorkerResourceTemplateVersionStatus{BuildID: b})
		}
		return wrt
	}
	refsFor := func(refs []WorkerResourceRef) map[string][]string {
		out := map[string][]string{}
		for _, r := range refs {
			out[r.WRTName] = append(out[r.WRTName], r.BuildID)
		}
		return out
	}

	t.Run("removing one group deletes only that group's WRT copies", func(t *testing.T) {
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{
			withStatus(groupWRT("workflows-hpa", "workflows", createTestWRT), "v1"),
			withStatus(groupWRT("activities-hpa", "activities", createTestWRT), "v1"),
			withStatus(groupWRT("batch-hpa", "batch", createTestWRT), "v1"),
		}
		removing := []*appsv1.Deployment{state.WorkerGroupDeployments["v1"]["batch"]}

		assert.Equal(t, map[string][]string{"batch-hpa": {"v1"}}, refsFor(getDeleteWorkerResources(wrts, removing, state, nil)))
	})

	t.Run("status entry for a build without the group is cleaned up", func(t *testing.T) {
		wrts := []temporaliov1alpha1.WorkerResourceTemplate{
			withStatus(groupWRT("gone-hpa", "gone", createTestWRT), "v1"),
			withStatus(groupWRT("activities-hpa", "activities", createTestWRT), "v1"),
		}

		assert.Equal(t, map[string][]string{"gone-hpa": {"v1"}}, refsFor(getDeleteWorkerResources(wrts, nil, state, nil)))
	})
}

func TestGetWRTGroupProblems(t *testing.T) {
	state := k8s.NewDeploymentState(groupDeployment("old", "workflows", 1), groupDeployment("old", "retired", 1))
	spec := groupedSpec(t, "worker:v1", nil, "activities")
	recovered := groupWRT("recovered-hpa", "activities", createTestWRT)
	recovered.Status.Conditions = []metav1.Condition{{Type: temporaliov1alpha1.ConditionReady, Reason: temporaliov1alpha1.ReasonWRTWorkerGroupNotFound}}
	wrts := []temporaliov1alpha1.WorkerResourceTemplate{
		groupWRT("default-hpa", "", createTestWRT),
		groupWRT("activities-hpa", "activities", createTestWRT),
		groupWRT("retired-hpa", "retired", createTestWRT),
		groupWRT("ghost-hpa", "ghost", createTestWRT),
		recovered,
	}

	missing, stale := getWRTGroupProblems(wrts, state, spec)
	assert.Equal(t, []string{"default-hpa", "ghost-hpa"}, missing, "a WRT without a group has nothing to target in a grouped WorkerDeployment")
	assert.Equal(t, []string{"recovered-hpa"}, stale)
}

func TestCheckAndUpdateGroupPodTemplateSpec_SettlesAfterOneUpdate(t *testing.T) {
	d := driftGroupDeployment("custom", "activities", "worker:v1")
	spec := groupedSpec(t, "worker:v1", nil, "activities")
	spec.WorkerOptions.UnsafeCustomBuildID = "custom"
	spec.WorkerGroups[0].Deployment.Template = groupTemplate("activities:v2")

	require.True(t, checkAndUpdateGroupPodTemplateSpec(d, spec, createDefaultConnectionSpec()))
	assert.False(t, checkAndUpdateGroupPodTemplateSpec(d, spec, createDefaultConnectionSpec()),
		"an updated Deployment must not be seen as drifted again")
}
