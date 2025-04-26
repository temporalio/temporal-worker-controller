// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"encoding/json"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
)

var (
	testTemporalNamespace      = "ns"
	deprecatedVersionImageName = "ignored-because-cannot-recreate-in-tests"
)

func newTestPodSpec(image string) v1.PodTemplateSpec {
	return v1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "main",
					Image: image,
				},
			},
		},
	}
}

type testTemporalWorkerDeployment struct {
	v       *temporaliov1alpha1.TemporalWorkerDeployment
	buildID string
}

func (t *testTemporalWorkerDeployment) Get() *temporaliov1alpha1.TemporalWorkerDeployment {
	return t.v
}

func (t *testTemporalWorkerDeployment) getSpec() *temporaliov1alpha1.TemporalWorkerDeploymentSpec {
	return &t.v.Spec
}

func (t *testTemporalWorkerDeployment) getSpecCopy() *temporaliov1alpha1.TemporalWorkerDeploymentSpec {
	// DeepCopyJSON performs a deep copy of a struct using JSON marshaling and unmarshaling.
	newSpec := &temporaliov1alpha1.TemporalWorkerDeploymentSpec{}

	bytes, err := json.Marshal(t.getSpec())
	if err != nil {
		panic(err)
	}
	err = json.Unmarshal(bytes, newSpec)
	if err != nil {
		panic(err)
	}
	return newSpec
}

func (t *testTemporalWorkerDeployment) getTemporalNamespace() string {
	return t.v.Spec.WorkerOptions.TemporalNamespace
}

func newTestTWD(name, image string) *testTemporalWorkerDeployment {
	initialReplicas := int32(0)
	ret := &testTemporalWorkerDeployment{
		v: &temporaliov1alpha1.TemporalWorkerDeployment{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "temporal.io/v1alpha1",
				Kind:       "TemporalWorkerDeployment",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testTemporalNamespace,
			},
			Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
				Replicas: &initialReplicas,
				Template: newTestPodSpec(image),
				WorkerOptions: temporaliov1alpha1.WorkerOptions{
					TemporalNamespace: testTemporalNamespace,
				},
				RolloutStrategy: temporaliov1alpha1.RolloutStrategy{},
				SunsetStrategy:  temporaliov1alpha1.SunsetStrategy{},
			},
			Status: temporaliov1alpha1.TemporalWorkerDeploymentStatus{},
		},
	}

	// We want to get and store the build id from the original pod spec like we do in genstatus,
	// _before_ the controller adds labels and environment variables, which will
	// change the hash value.
	ret.buildID = ret.getBuildID()

	return ret
}

func (t *testTemporalWorkerDeployment) withReplicas(n int32) *testTemporalWorkerDeployment {
	t.v.Spec.Replicas = &n
	return t
}

func (t *testTemporalWorkerDeployment) withStatus(status temporaliov1alpha1.TemporalWorkerDeploymentStatus) *testTemporalWorkerDeployment {
	t.v.Status = status
	return t
}

func (t *testTemporalWorkerDeployment) withAllAtOnce() *testTemporalWorkerDeployment {
	t.v.Spec.RolloutStrategy = temporaliov1alpha1.RolloutStrategy{Strategy: temporaliov1alpha1.UpdateAllAtOnce}
	return t
}

func (t *testTemporalWorkerDeployment) withProgressive() *testTemporalWorkerDeployment {
	t.v.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
	t.v.Spec.RolloutStrategy.Steps = []temporaliov1alpha1.RolloutStep{
		{RampPercentage: 1, PauseDuration: metav1.Duration{Duration: time.Second}},
		{RampPercentage: 10, PauseDuration: metav1.Duration{Duration: time.Second}},
		{RampPercentage: 100, PauseDuration: metav1.Duration{Duration: time.Second}},
	}
	return t
}

func (t *testTemporalWorkerDeployment) withManual() *testTemporalWorkerDeployment {
	t.v.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateManual
	return t
}

func (t *testTemporalWorkerDeployment) withGate(wfType string) *testTemporalWorkerDeployment {
	t.v.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: wfType}
	return t
}

func (t *testTemporalWorkerDeployment) withSunset(scaledownDelay, deleteDelay time.Duration) *testTemporalWorkerDeployment {
	t.v.Spec.SunsetStrategy.DeleteDelay = &metav1.Duration{Duration: deleteDelay}
	t.v.Spec.SunsetStrategy.ScaledownDelay = &metav1.Duration{Duration: scaledownDelay}
	return t
}

func (t *testTemporalWorkerDeployment) withSpecificBuildID(bid string) *testTemporalWorkerDeployment {
	t.buildID = bid
	for _, ev := range t.getSpec().Template.Spec.Containers[0].Env {
		if ev.Name == "WORKER_BUILD_ID" {
			ev.Value = bid
		}
	}
	if t.getSpec().Template.Labels == nil {
		t.getSpec().Template.Labels = map[string]string{}
	}

	t.getSpec().Template.Labels[buildIDLabel] = bid
	return t
}

func (t *testTemporalWorkerDeployment) getBuildID() string {
	if t.buildID != "" {
		return t.buildID
	}
	return computeBuildID(&t.v.Spec)
}

func (t *testTemporalWorkerDeployment) getName() string {
	return t.v.Name
}

func (t *testTemporalWorkerDeployment) getWorkerDeploymentName() string {
	return computeWorkerDeploymentName(t.v)
}

func (t *testTemporalWorkerDeployment) getVersionID() string {
	return computeVersionID(t.getWorkerDeploymentName(), t.getBuildID())
}

func (t *testTemporalWorkerDeployment) getDeployment() *appsv1.Deployment {
	dep := newDeploymentWithoutOwnerRef(
		&t.v.TypeMeta,
		&t.v.ObjectMeta,
		t.getSpecCopy(), // this will be mutated, so we need to make a copy
		t.getWorkerDeploymentName(),
		t.getBuildID(),
		temporaliov1alpha1.TemporalConnectionSpec{},
	)
	controller := true
	dep.ObjectMeta.OwnerReferences[0].Controller = &controller
	return dep
}

func (t *testTemporalWorkerDeployment) getTestVersion() *testWorkerDeploymentVersion {
	return &testWorkerDeploymentVersion{
		ownerTWD: t,
		v: &temporaliov1alpha1.WorkerDeploymentVersion{
			VersionID: t.getVersionID(),
		},
	}
}

type testWorkerDeploymentVersion struct {
	ownerTWD *testTemporalWorkerDeployment
	v        *temporaliov1alpha1.WorkerDeploymentVersion
}

func (t *testWorkerDeploymentVersion) Get() *temporaliov1alpha1.WorkerDeploymentVersion {
	return t.v
}

func (t *testWorkerDeploymentVersion) withStatus(status temporaliov1alpha1.VersionStatus) *testWorkerDeploymentVersion {
	t.v.Status = status
	if status == temporaliov1alpha1.VersionStatusDrained {
		t.withDrainedSince(time.Now().Add(-5 * time.Minute))
	}
	return t
}

func (t *testWorkerDeploymentVersion) withRamp(percent float32, rampingSince time.Time) *testWorkerDeploymentVersion {
	ts := metav1.NewTime(rampingSince)
	t.v.RampingSince = &ts
	t.v.RampPercentage = &percent
	return t
}

func (t *testWorkerDeploymentVersion) withTaskQueues(tqNames ...string) *testWorkerDeploymentVersion {
	t.v.TaskQueues = make([]temporaliov1alpha1.TaskQueue, len(tqNames))
	for i, tq := range tqNames {
		t.v.TaskQueues[i] = temporaliov1alpha1.TaskQueue{Name: tq}
	}
	return t
}

func (t *testWorkerDeploymentVersion) withTestWorkflows(testWFs []temporaliov1alpha1.WorkflowExecution) *testWorkerDeploymentVersion {
	t.v.TestWorkflows = testWFs
	return t
}

func (t *testWorkerDeploymentVersion) withK8sDeployment(healthy bool) *testWorkerDeploymentVersion {
	if healthy {
		ts := metav1.NewTime(time.Now())
		t.v.HealthySince = &ts
	}
	t.v.Deployment = newObjectRef(t.ownerTWD.getDeployment())
	return t
}

// useful when testing versions that have been drained but may/may not be eligible to be deleted.
func (t *testWorkerDeploymentVersion) withDrainedSince(drainedTime time.Time) *testWorkerDeploymentVersion {
	t.v.DrainedSince = &metav1.Time{Time: drainedTime}
	return t
}

func clearResourceVersion(obj metav1.Object) {
	obj.SetResourceVersion("")
}

func TestGeneratePlan(t *testing.T) {
	type testCase struct {
		workerDeploymentName string
		observedReplicas     int32
		desiredReplicas      int32
		observedState        *temporaliov1alpha1.TemporalWorkerDeploymentStatus
		desiredState         *temporaliov1alpha1.TemporalWorkerDeploymentSpec
		expectedPlan         plan
	}

	testCases := map[string]testCase{
		//"no action needed": {
		//	workerDeploymentName: "foo",
		//	observedReplicas:     3,
		//	desiredReplicas:      3,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DefaultVersion: newTestTWD("foo", "a").withReplicas(3).getTestVersion().
		//			withK8sDeployment(true).
		//			withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
		//	},
		//	desiredState: newTestTWD("foo", "a").withReplicas(3).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("foo", "a").getWorkerDeploymentName(),
		//		DeleteDeployments:    nil,
		//		CreateDeployment:     nil,
		//		ScaleDeployments:     make(map[*v1.ObjectReference]uint32),
		//		UpdateVersionConfig:  nil,
		//	},
		//},
		//"scale up inactive deployments if the number of replicas don't match": {
		//	workerDeploymentName: "fia",
		//	observedReplicas:     0,
		//	desiredReplicas:      3,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
		//			newTestTWD("fia", "a").withReplicas(0).getTestVersion().
		//				withK8sDeployment(true).
		//				withStatus(temporaliov1alpha1.VersionStatusInactive).Get(),
		//		},
		//	},
		//	desiredState: newTestTWD("fia", "a").withReplicas(3).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("fia", "a").getWorkerDeploymentName(),
		//		DeleteDeployments:    nil,
		//		CreateDeployment:     nil,
		//		ScaleDeployments: map[*v1.ObjectReference]uint32{
		//			newObjectRef(newTestTWD("fia", "a").getDeployment()): 3,
		//		},
		//		UpdateVersionConfig: nil,
		//	},
		//},
		//"scale up ramping target deployment if the number of replicas don't match": {
		//	workerDeploymentName: "fii",
		//	observedReplicas:     0,
		//	desiredReplicas:      3,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DefaultVersion: newTestTWD("fii", "a").withReplicas(1).getTestVersion().Get(),
		//		TargetVersion: newTestTWD("fii", "b").withReplicas(0).getTestVersion().
		//			withK8sDeployment(true).
		//			withStatus(temporaliov1alpha1.VersionStatusRamping).Get(),
		//		DeprecatedVersions: nil,
		//	},
		//	desiredState: newTestTWD("fii", "b").withReplicas(3).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("fii", "b").getWorkerDeploymentName(),
		//		DeleteDeployments:    nil,
		//		CreateDeployment:     nil,
		//		ScaleDeployments: map[*v1.ObjectReference]uint32{
		//			newObjectRef(newTestTWD("fii", "b").getDeployment()): 3,
		//		},
		//		UpdateVersionConfig: nil,
		//	},
		//},
		//"scale up current deployment if the number of replicas don't match": {
		//	workerDeploymentName: "aii",
		//	observedReplicas:     0,
		//	desiredReplicas:      3,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DefaultVersion: newTestTWD("aii", "a").withReplicas(0).getTestVersion().
		//			withK8sDeployment(true).
		//			withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
		//	},
		//	desiredState: newTestTWD("aii", "a").withReplicas(3).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("aii", "a").getWorkerDeploymentName(),
		//		DeleteDeployments:    nil,
		//		CreateDeployment:     nil,
		//		ScaleDeployments: map[*v1.ObjectReference]uint32{
		//			newObjectRef(newTestTWD("aii", "a").getDeployment()): 3,
		//		},
		//		UpdateVersionConfig: nil,
		//	},
		//},
		//"create new k8s deployment from the pod template": {
		//	workerDeploymentName: "baz",
		//	observedReplicas:     3,
		//	desiredReplicas:      3,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DefaultVersion: newTestTWD("baz", "a").withReplicas(3).getTestVersion().
		//			withK8sDeployment(true).
		//			withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
		//		// k8 deployment does not exist and will be created from the pod template
		//		TargetVersion: newTestTWD("baz", "b").withReplicas(3).getTestVersion().
		//			withStatus(temporaliov1alpha1.VersionStatusNotRegistered).Get(),
		//		DeprecatedVersions: nil,
		//	},
		//	desiredState: newTestTWD("baz", "b").withReplicas(3).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("baz", "b").getWorkerDeploymentName(),
		//		DeleteDeployments:    nil,
		//		CreateDeployment:     newTestTWD("baz", "b").withReplicas(3).getDeployment(),
		//		ScaleDeployments:     make(map[*v1.ObjectReference]uint32),
		//		UpdateVersionConfig:  nil,
		//	},
		//},
		//"delete unregistered deployment version": {
		//	workerDeploymentName: "bar",
		//	observedReplicas:     3,
		//	desiredReplicas:      3,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DefaultVersion: newTestTWD("bar", "a").withReplicas(3).getTestVersion().
		//			withK8sDeployment(true).
		//			withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
		//		DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
		//			newTestTWD("bar", deprecatedVersionImageName).withReplicas(3).
		//				withSpecificBuildID("b").
		//				getTestVersion().
		//				withK8sDeployment(true).
		//				withStatus(temporaliov1alpha1.VersionStatusNotRegistered).Get(),
		//		},
		//	},
		//	desiredState: newTestTWD("bar", "a").withReplicas(3).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("bar", "b").getWorkerDeploymentName(),
		//		DeleteDeployments: []*appsv1.Deployment{
		//			newTestTWD("bar", deprecatedVersionImageName).withReplicas(3).withSpecificBuildID("b").getDeployment(),
		//		},
		//		ScaleDeployments:    map[*v1.ObjectReference]uint32{},
		//		CreateDeployment:    nil,
		//		UpdateVersionConfig: nil,
		//	},
		//},
		//"delete deployment when scaled down to 0 replicas": {
		//	workerDeploymentName: "def",
		//	observedReplicas:     0,
		//	desiredReplicas:      0,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DefaultVersion: newTestTWD("def", "a").withReplicas(0).
		//			getTestVersion().
		//			withK8sDeployment(true).
		//			withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
		//		DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
		//			newTestTWD("def", "b").withReplicas(0).getTestVersion().
		//				withK8sDeployment(true).
		//				withStatus(temporaliov1alpha1.VersionStatusDrained).
		//				withDrainedSince(time.Now().Add(-62 * time.Minute)).Get(),
		//		},
		//	},
		//	desiredState: newTestTWD("def", "a").withReplicas(0).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("def", "a").getWorkerDeploymentName(),
		//		DeleteDeployments: []*appsv1.Deployment{
		//			newTestTWD("def", deprecatedVersionImageName).withReplicas(0).
		//				withSpecificBuildID(newTestTWD("def", "b").getBuildID()).
		//				getDeployment(),
		//		},
		//		CreateDeployment:    nil,
		//		ScaleDeployments:    make(map[*v1.ObjectReference]uint32),
		//		UpdateVersionConfig: nil,
		//	},
		//},
		//"don't delete deployment if not scaled down to 0 replicas even though it's past scaledown + delete delay": {
		//	workerDeploymentName: "abc",
		//	observedReplicas:     3,
		//	desiredReplicas:      3,
		//	observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		//		DefaultVersion: newTestTWD("abc", "a").withReplicas(3).
		//			getTestVersion().
		//			withK8sDeployment(true).
		//			withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
		//		DeprecatedVersions: []*temporaliov1alpha1.WorkerDeploymentVersion{
		//			newTestTWD("abc", "b").withReplicas(3).getTestVersion().
		//				withK8sDeployment(true).
		//				withStatus(temporaliov1alpha1.VersionStatusDrained).
		//				withDrainedSince(time.Now().Add(-62 * time.Minute)).Get(),
		//		},
		//	},
		//	desiredState: newTestTWD("abc", "a").withReplicas(3).getSpec(),
		//	expectedPlan: plan{
		//		TemporalNamespace:    testTemporalNamespace,
		//		WorkerDeploymentName: newTestTWD("abc", "a").getWorkerDeploymentName(),
		//		DeleteDeployments:    nil,
		//		CreateDeployment:     nil,
		//		ScaleDeployments: map[*v1.ObjectReference]uint32{
		//			newObjectRef(newTestTWD("abc", deprecatedVersionImageName).withReplicas(0).
		//				withSpecificBuildID(newTestTWD("abc", "b").getBuildID()).
		//				getDeployment()): 0,
		//		},
		//		UpdateVersionConfig: nil,
		//	},
		//},
		"unset ramp if target == default but ramping version is not target version (ie. rollback after partial ramp)": {
			workerDeploymentName: "xyz",
			observedReplicas:     3,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestTWD("xyz", "a").withReplicas(3).
					getTestVersion().
					withK8sDeployment(true).
					withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
				RampingVersion: newTestTWD("xyz", "b").withReplicas(3).
					getTestVersion().
					withK8sDeployment(true).
					withStatus(temporaliov1alpha1.VersionStatusRamping).Get(),
				TargetVersion: newTestTWD("xyz", "a").withReplicas(3).
					getTestVersion().
					withK8sDeployment(true).
					withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
			},
			desiredState: newTestTWD("xyz", "a").withReplicas(3).getSpec(),
			expectedPlan: plan{
				TemporalNamespace:    testTemporalNamespace,
				WorkerDeploymentName: newTestTWD("xyz", "a").getWorkerDeploymentName(),
				DeleteDeployments:    nil,
				CreateDeployment:     nil,
				ScaleDeployments:     nil,
				UpdateVersionConfig: &versionConfig{
					conflictToken:  nil,
					versionID:      newTestTWD("xyz", "a").getVersionID(),
					setDefault:     false,
					rampPercentage: 0,
					unsetRamp:      true,
				},
			},
		},
		"overwrite ramp if target != default but ramping version is not target version (ie. roll-forward after partial ramp)": {
			workerDeploymentName: "wyx",
			observedReplicas:     3,
			desiredReplicas:      3,
			observedState: &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				DefaultVersion: newTestTWD("wyx", "a").withReplicas(3).
					getTestVersion().
					withK8sDeployment(true).
					withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
				RampingVersion: newTestTWD("wyx", "b").withReplicas(3).
					getTestVersion().
					withK8sDeployment(true).
					withStatus(temporaliov1alpha1.VersionStatusRamping).Get(),
				TargetVersion: newTestTWD("wyx", "c").withReplicas(3).
					getTestVersion().
					withK8sDeployment(true).
					withStatus(temporaliov1alpha1.VersionStatusCurrent).Get(),
			},
			desiredState: newTestTWD("wyx", "c").withReplicas(3).getSpec(),
			expectedPlan: plan{
				TemporalNamespace:    testTemporalNamespace,
				WorkerDeploymentName: newTestTWD("wyx", "c").getWorkerDeploymentName(),
				DeleteDeployments:    nil,
				CreateDeployment:     nil,
				ScaleDeployments:     nil,
				UpdateVersionConfig: &versionConfig{
					conflictToken:  nil,
					versionID:      newTestTWD("wyx", "c").getVersionID(),
					setDefault:     true,
					rampPercentage: 0,
					unsetRamp:      true,
				},
			},
		},
	}

	// Create a new scheme and client for each test case to avoid state bleeding
	scheme := runtime.NewScheme()
	_ = temporaliov1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme) // Add this to register Deployment type

	// Create the fake client
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &TemporalWorkerDeploymentReconciler{
		Client: c,
		Scheme: scheme,
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {

			// Create the TemporalWorkerDeployment resource.
			desiredImage := tc.desiredState.Template.Spec.Containers[0].Image
			desiredTWD := newTestTWD(tc.workerDeploymentName, desiredImage).
				withReplicas(tc.desiredReplicas).
				withStatus(*tc.observedState).
				withAllAtOnce().
				withSunset(time.Minute, time.Hour) // TODO(carlydf): find a way to specify this in the test case
			err := c.Create(context.Background(), desiredTWD.Get())
			if err != nil {
				t.Fatal(err)
			}

			// Create the Deployments referenced in the status
			if tc.observedState.DefaultVersion != nil && tc.observedState.DefaultVersion.Deployment != nil {
				// The current default version may have been created with a different desired image -> different desired build ID
				// We don't know what that image was, so we have to override the build id instead of generating it
				buildID := strings.Split(tc.observedState.DefaultVersion.VersionID, versionIDSeparator)[1]
				deployment := newTestTWD(desiredTWD.getName(), deprecatedVersionImageName).
					withReplicas(tc.observedReplicas).
					withSpecificBuildID(buildID).
					getDeployment()
				err = c.Create(context.Background(), deployment)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.observedState.RampingVersion != nil && tc.observedState.RampingVersion.Deployment != nil {
				// The current ramping version may have been created with a different desired image -> different desired build ID
				// We don't know what that image was, so we have to override the build id instead of generating it
				buildID := strings.Split(tc.observedState.RampingVersion.VersionID, versionIDSeparator)[1]
				deployment := newTestTWD(desiredTWD.getName(), deprecatedVersionImageName).
					withReplicas(tc.observedReplicas).
					withSpecificBuildID(buildID).
					getDeployment()
				err = c.Create(context.Background(), deployment)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.observedState.TargetVersion != nil && tc.observedState.TargetVersion.Deployment != nil {
				// The current target version may have been created with a different desired image -> different desired build ID
				// We don't know what that image was, so we have to override the build id instead of generating it
				buildID := strings.Split(tc.observedState.TargetVersion.VersionID, versionIDSeparator)[1]
				deployment := newTestTWD(desiredTWD.getName(), deprecatedVersionImageName).
					withReplicas(tc.observedReplicas).
					withSpecificBuildID(buildID).
					getDeployment()
				err = c.Create(context.Background(), deployment)
				if err != nil && !strings.Contains(err.Error(), "already exists") {
					t.Fatal(err)
				}
			}

			// Create the Deployments referenced in the deprecated versions
			for _, version := range tc.observedState.DeprecatedVersions {
				if version.Deployment != nil {
					buildID := strings.Split(version.VersionID, versionIDSeparator)[1]
					deployment := newTestTWD(desiredTWD.getName(), deprecatedVersionImageName).
						withReplicas(tc.observedReplicas).
						withSpecificBuildID(buildID).
						getDeployment()
					err = c.Create(context.Background(), deployment)
					if err != nil {
						t.Fatal(err)
					}
				}
			}

			actualPlan, err := r.generatePlan(
				context.Background(),
				log.FromContext(context.Background()),
				desiredTWD.Get(),
				temporaliov1alpha1.TemporalConnectionSpec{},
			)
			if err != nil {
				t.Fatal(err)
			}

			// Clear ResourceVersion before comparison
			if actualPlan.CreateDeployment != nil {
				clearResourceVersion(actualPlan.CreateDeployment)
			}
			for _, d := range actualPlan.DeleteDeployments {
				clearResourceVersion(d)
			}

			assertEqualPlans(t, &tc.expectedPlan, actualPlan)
		})

	}
}

// assertEqualPlans checks that the two plans are equal by conducting a deep comparsion of all the fields.
// Unlike assert.Equal, however, this function does not deep-compare the keys in the scale deployments map since
// the values are pointers to the deployment objects.
func assertEqualPlans(t *testing.T, expectedPlan *plan, actualPlan *plan) {

	assert.Equal(t, expectedPlan.TemporalNamespace, actualPlan.TemporalNamespace)
	assert.Equal(t, expectedPlan.WorkerDeploymentName, actualPlan.WorkerDeploymentName)
	assert.Equal(t, expectedPlan.DeleteDeployments, actualPlan.DeleteDeployments)
	assert.Equal(t, expectedPlan.CreateDeployment, actualPlan.CreateDeployment)
	assert.Equal(t, expectedPlan.UpdateVersionConfig, actualPlan.UpdateVersionConfig)

	// ignore key values in the scale deployments map since they are pointers to the deployment objects.
	assert.Equal(t, len(expectedPlan.ScaleDeployments), len(actualPlan.ScaleDeployments))

	// Verify whether the right values are present in the scale deployments map
	for k, v := range expectedPlan.ScaleDeployments {
		found := false
		for k2, v2 := range actualPlan.ScaleDeployments {
			if k.Name == k2.Name {
				found = true
				assert.Equal(t, v, v2)
			}
		}
		if !found {
			t.Fatalf("did not find expected deployment %v | actual %+v, expected %+v\n",
				k.Name, actualPlan.ScaleDeployments, expectedPlan.ScaleDeployments)
		}

		assert.True(t, found)
	}
}
