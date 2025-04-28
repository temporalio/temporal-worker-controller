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
	testTemporalNamespace = "ns"
)

func TestGeneratePlan(t *testing.T) {
	type testCase struct {
		desired                    *testTemporalWorkerDeployment
		observedTargetVersion      *testWorkerDeploymentVersion
		observedDefaultVersion     *testWorkerDeploymentVersion
		observedRampingVersion     *testWorkerDeploymentVersion
		observedDeprecatedVersions []*testWorkerDeploymentVersion
		expectedPlan               plan
	}
	testCases := make(map[string]testCase)

	testCases["no action needed"] = testCase{
		desired:               newTestTWD("foo", "a").withReplicas(3),
		observedTargetVersion: nil,
		observedDefaultVersion: newTestTWD("foo", "a").withReplicas(3).getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion:     nil,
		observedDeprecatedVersions: nil,
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("foo", "a").getWorkerDeploymentName(),
			DeleteDeployments:    nil,
			CreateDeployment:     nil,
			ScaleDeployments:     make(map[*v1.ObjectReference]uint32),
			UpdateVersionConfig:  nil,
		},
	}

	testCases["scale up inactive deployments if the number of replicas don't match"] = testCase{
		desired:                newTestTWD("fia", "a").withReplicas(3),
		observedTargetVersion:  nil,
		observedDefaultVersion: nil,
		observedRampingVersion: nil,
		observedDeprecatedVersions: []*testWorkerDeploymentVersion{
			newTestTWD("fia", "a").withReplicas(0).getTestVersion().
				withK8sDeployment(true).
				withStatus(temporaliov1alpha1.VersionStatusInactive),
		},
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("fia", "a").getWorkerDeploymentName(),
			DeleteDeployments:    nil,
			CreateDeployment:     nil,
			ScaleDeployments: map[*v1.ObjectReference]uint32{
				newObjectRef(newTestTWD("fia", "a").getDeployment()): 3,
			},
			UpdateVersionConfig: nil,
		},
	}

	testCases["scale up ramping target deployment if the number of replicas don't match"] = testCase{
		desired: newTestTWD("fii", "b").withReplicas(3),
		observedTargetVersion: newTestTWD("fii", "b").withReplicas(0).getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusRamping),
		observedDefaultVersion:     newTestTWD("fii", "a").withReplicas(1).getTestVersion(),
		observedRampingVersion:     nil,
		observedDeprecatedVersions: nil,
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("fii", "b").getWorkerDeploymentName(),
			DeleteDeployments:    nil,
			CreateDeployment:     nil,
			ScaleDeployments: map[*v1.ObjectReference]uint32{
				newObjectRef(newTestTWD("fii", "b").getDeployment()): 3,
			},
			UpdateVersionConfig: nil,
		},
	}

	testCases["scale up current deployment if the number of replicas don't match"] = testCase{
		desired:               newTestTWD("aii", "a").withReplicas(3),
		observedTargetVersion: nil,
		observedDefaultVersion: newTestTWD("aii", "a").withReplicas(0).getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion: nil,
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("aii", "a").getWorkerDeploymentName(),
			DeleteDeployments:    nil,
			CreateDeployment:     nil,
			ScaleDeployments: map[*v1.ObjectReference]uint32{
				newObjectRef(newTestTWD("aii", "a").getDeployment()): 3,
			},
			UpdateVersionConfig: nil,
		},
	}

	testCases["create new k8s deployment from the pod template"] = testCase{
		desired: newTestTWD("baz", "b").withReplicas(3),
		observedTargetVersion: newTestTWD("baz", "b").withReplicas(3).getTestVersion().
			withStatus(temporaliov1alpha1.VersionStatusNotRegistered),
		observedDefaultVersion: newTestTWD("baz", "a").withReplicas(3).getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion:     nil,
		observedDeprecatedVersions: nil,
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("baz", "b").getWorkerDeploymentName(),
			DeleteDeployments:    nil,
			CreateDeployment:     newTestTWD("baz", "b").withReplicas(3).getDeployment(),
			ScaleDeployments:     make(map[*v1.ObjectReference]uint32),
			UpdateVersionConfig:  nil,
		},
	}

	testCases["delete unregistered deployment version"] = testCase{
		desired:               newTestTWD("bar", "a").withReplicas(3),
		observedTargetVersion: nil,
		observedDefaultVersion: newTestTWD("bar", "a").withReplicas(3).getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion: nil,
		observedDeprecatedVersions: []*testWorkerDeploymentVersion{
			newTestTWD("bar", "b").withReplicas(3).
				getTestVersion().
				withK8sDeployment(true).
				withStatus(temporaliov1alpha1.VersionStatusNotRegistered),
		},
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("bar", "b").getWorkerDeploymentName(),
			DeleteDeployments: []*appsv1.Deployment{
				newTestTWD("bar", "b").withReplicas(3).getDeployment(),
			},
			ScaleDeployments:    map[*v1.ObjectReference]uint32{},
			CreateDeployment:    nil,
			UpdateVersionConfig: nil,
		},
	}
	testCases["delete deployment when scaled down to 0 replicas"] = testCase{
		desired:               newTestTWD("def", "a").withReplicas(0).withSunset(time.Minute, time.Hour),
		observedTargetVersion: nil,
		observedDefaultVersion: newTestTWD("def", "a").withReplicas(0).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion: nil,
		observedDeprecatedVersions: []*testWorkerDeploymentVersion{
			newTestTWD("def", "b").withReplicas(0).getTestVersion().
				withK8sDeployment(true).
				withStatus(temporaliov1alpha1.VersionStatusDrained).
				withDrainedSince(time.Now().Add(-62 * time.Minute)),
		},
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("def", "a").getWorkerDeploymentName(),
			DeleteDeployments: []*appsv1.Deployment{
				newTestTWD("def", "b").withReplicas(0).getDeployment(),
			},
			CreateDeployment:    nil,
			ScaleDeployments:    make(map[*v1.ObjectReference]uint32),
			UpdateVersionConfig: nil,
		},
	}
	testCases["don't delete deployment if not scaled down to 0 replicas even though it's past scaledown + delete delay"] = testCase{
		desired:               newTestTWD("abc", "a").withReplicas(3).withSunset(time.Minute, time.Hour),
		observedTargetVersion: nil,
		observedDefaultVersion: newTestTWD("abc", "a").withReplicas(3).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion: nil,
		observedDeprecatedVersions: []*testWorkerDeploymentVersion{
			newTestTWD("abc", "b").withReplicas(3).getTestVersion().
				withK8sDeployment(true).
				withStatus(temporaliov1alpha1.VersionStatusDrained).
				withDrainedSince(time.Now().Add(-62 * time.Minute)),
		},
		expectedPlan: plan{
			TemporalNamespace:    testTemporalNamespace,
			WorkerDeploymentName: newTestTWD("abc", "a").getWorkerDeploymentName(),
			DeleteDeployments:    nil,
			CreateDeployment:     nil,
			ScaleDeployments: map[*v1.ObjectReference]uint32{
				newObjectRef(newTestTWD("abc", "b").withReplicas(0).getDeployment()): 0,
			},
			UpdateVersionConfig: nil,
		},
	}
	testCases["unset ramp if target == default but ramping version is not target version (ie. rollback after partial ramp)"] = testCase{
		desired: newTestTWD("xyz", "a").withReplicas(3).withAllAtOnce(),
		observedTargetVersion: newTestTWD("xyz", "a").withReplicas(3).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedDefaultVersion: newTestTWD("xyz", "a").withReplicas(3).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion: newTestTWD("xyz", "b").withReplicas(3).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusRamping),
		observedDeprecatedVersions: nil,
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
	}
	testCases["overwrite ramp if target != default but ramping version is not target version (ie. roll-forward after partial ramp)"] = testCase{
		desired: newTestTWD("wyx", "c").withReplicas(3).withAllAtOnce(),
		observedTargetVersion: newTestTWD("wyx", "c").withReplicas(3).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedDefaultVersion: newTestTWD("wyx", "a").withReplicas(3).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusCurrent),
		observedRampingVersion: newTestTWD("wyx", "b").withReplicas(3).
			getTestVersion().
			withK8sDeployment(true).
			withStatus(temporaliov1alpha1.VersionStatusRamping),
		observedDeprecatedVersions: nil,
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

			// Give the desired TWD the correct status
			dvs := make([]*temporaliov1alpha1.WorkerDeploymentVersion, len(tc.observedDeprecatedVersions))
			for i, e := range tc.observedDeprecatedVersions {
				dvs[i] = e.Get()
			}
			tc.desired = tc.desired.withStatus(temporaliov1alpha1.TemporalWorkerDeploymentStatus{
				TargetVersion:        tc.observedTargetVersion.Get(),
				DefaultVersion:       tc.observedDefaultVersion.Get(),
				RampingVersion:       tc.observedRampingVersion.Get(),
				DeprecatedVersions:   dvs,
				VersionConflictToken: nil,
			})

			// Create the TemporalWorkerDeployment resource.
			err := c.Create(context.Background(), tc.desired.Get())
			if err != nil {
				t.Fatal(err)
			}

			// Create the observed Deployments
			if tc.observedDefaultVersion != nil && tc.observedDefaultVersion.Get().Deployment != nil {
				err = c.Create(context.Background(), tc.observedDefaultVersion.ownerTWD.getDeployment())
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.observedRampingVersion != nil && tc.observedRampingVersion.Get().Deployment != nil {
				err = c.Create(context.Background(), tc.observedRampingVersion.ownerTWD.getDeployment())
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.observedTargetVersion != nil && tc.observedTargetVersion.Get().Deployment != nil {
				err = c.Create(context.Background(), tc.observedTargetVersion.ownerTWD.getDeployment())
				if err != nil && !strings.Contains(err.Error(), "already exists") { // might already exist if == default
					t.Fatal(err)
				}
			}

			// Create the Deployments referenced in the deprecated versions
			for _, version := range tc.observedDeprecatedVersions {
				if version.Get().Deployment != nil {
					err = c.Create(context.Background(), version.ownerTWD.getDeployment())
					if err != nil {
						t.Fatal(err)
					}
				}
			}

			actualPlan, err := r.generatePlan(
				context.Background(),
				log.FromContext(context.Background()),
				tc.desired.Get(),
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
	if t == nil {
		return nil
	}
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
