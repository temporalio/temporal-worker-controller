package testhelpers

import (
	"context"
	"testing"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"go.temporal.io/server/temporaltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// TemporalWorkerDeploymentBuilder provides a fluent interface for building test TWD objects
type TemporalWorkerDeploymentBuilder struct {
	twd *temporaliov1alpha1.TemporalWorkerDeployment

	statusBuilder *StatusBuilder
}

// NewTemporalWorkerDeploymentBuilder creates a new builder with sensible defaults
func NewTemporalWorkerDeploymentBuilder() *TemporalWorkerDeploymentBuilder {
	return &TemporalWorkerDeploymentBuilder{
		twd: MakeTWDWithName("", ""),
	}
}

// WithName sets the name
func (b *TemporalWorkerDeploymentBuilder) WithName(name string) *TemporalWorkerDeploymentBuilder {
	b.twd.ObjectMeta.Name = name
	b.twd.Name = name
	return b
}

// WithNamespace sets the namespace
func (b *TemporalWorkerDeploymentBuilder) WithNamespace(namespace string) *TemporalWorkerDeploymentBuilder {
	b.twd.ObjectMeta.Namespace = namespace
	return b
}

// WithManualStrategy sets the rollout strategy to manual
func (b *TemporalWorkerDeploymentBuilder) WithManualStrategy() *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateManual
	return b
}

// WithAllAtOnceStrategy sets the rollout strategy to all-at-once
func (b *TemporalWorkerDeploymentBuilder) WithAllAtOnceStrategy() *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateAllAtOnce
	return b
}

// WithProgressiveStrategy sets the rollout strategy to progressive with given steps
func (b *TemporalWorkerDeploymentBuilder) WithProgressiveStrategy(steps ...temporaliov1alpha1.RolloutStep) *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
	b.twd.Spec.RolloutStrategy.Steps = steps
	return b
}

// WithGate sets the rollout strategy have a gate workflow
func (b *TemporalWorkerDeploymentBuilder) WithGate(expectSuccess bool) *TemporalWorkerDeploymentBuilder {
	if expectSuccess {
		b.twd.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: successTestWorkflowType}
	} else {
		b.twd.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: failTestWorkflowType}
	}
	return b
}

// WithReplicas sets the number of replicas
func (b *TemporalWorkerDeploymentBuilder) WithReplicas(replicas int32) *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.Replicas = &replicas
	return b
}

// WithTargetTemplate sets the template of the worker deployment to a pod spec with the given image name, thus defining the target version.
func (b *TemporalWorkerDeploymentBuilder) WithTargetTemplate(imageName string) *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.Template = MakePodSpecWithImage(imageName)
	return b
}

// WithTemporalConnection sets the temporal connection name
func (b *TemporalWorkerDeploymentBuilder) WithTemporalConnection(connectionName string) *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.WorkerOptions.TemporalConnection = connectionName
	return b
}

// WithTemporalNamespace sets the temporal namespace
func (b *TemporalWorkerDeploymentBuilder) WithTemporalNamespace(temporalNamespace string) *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.WorkerOptions.TemporalNamespace = temporalNamespace
	return b
}

// WithLabels sets the labels
func (b *TemporalWorkerDeploymentBuilder) WithLabels(labels map[string]string) *TemporalWorkerDeploymentBuilder {
	if b.twd.ObjectMeta.Labels == nil {
		b.twd.ObjectMeta.Labels = make(map[string]string)
	}
	for k, v := range labels {
		b.twd.ObjectMeta.Labels[k] = v
	}
	return b
}

func (b *TemporalWorkerDeploymentBuilder) WithStatus(statusBuilder *StatusBuilder) *TemporalWorkerDeploymentBuilder {
	b.statusBuilder = statusBuilder
	return b
}

// Build returns the constructed TemporalWorkerDeployment
func (b *TemporalWorkerDeploymentBuilder) Build() *temporaliov1alpha1.TemporalWorkerDeployment {
	// Set defaults if not already set
	if b.twd.Spec.WorkerOptions.TemporalConnection == "" {
		b.twd.Spec.WorkerOptions.TemporalConnection = b.twd.Name
	}

	if b.twd.ObjectMeta.Labels == nil {
		b.twd.ObjectMeta.Labels = map[string]string{"app": "test-worker"}
	}

	if b.statusBuilder != nil {
		b.twd.Status = *b.statusBuilder.
			WithName(b.twd.Name).
			WithNamespace(b.twd.Namespace).
			Build()
	}
	return b.twd
}

// StatusBuilder provides a fluent interface for building expected status objects
// Versions will be built based on the TWD name and k8s namespace
type StatusBuilder struct {
	name         string
	k8sNamespace string

	targetVersionBuilder      func(name string, namespace string) temporaliov1alpha1.TargetWorkerDeploymentVersion
	currentVersionBuilder     func(name string, namespace string) *temporaliov1alpha1.CurrentWorkerDeploymentVersion
	deprecatedVersionsBuilder func(name string, namespace string) []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion

	// ConflictToken, LastModifierIdentity, and VersionCount not currently tested
}

// NewStatusBuilder creates a new status builder
func NewStatusBuilder() *StatusBuilder {
	return &StatusBuilder{}
}

// WithName sets the name
func (sb *StatusBuilder) WithName(name string) *StatusBuilder {
	sb.name = name
	return sb
}

// WithNamespace sets the namespace
func (sb *StatusBuilder) WithNamespace(k8sNamespace string) *StatusBuilder {
	sb.k8sNamespace = k8sNamespace
	return sb
}

// WithCurrentVersion sets the current version in the status
func (sb *StatusBuilder) WithCurrentVersion(imageName string, healthy, createDeployment bool) *StatusBuilder {
	sb.currentVersionBuilder = func(twdName string, namespace string) *temporaliov1alpha1.CurrentWorkerDeploymentVersion {
		return MakeCurrentVersion(namespace, twdName, imageName, healthy, createDeployment)
	}
	return sb
}

// WithTargetVersion sets the target version in the status.
// Set createDeployment to true if the test runner should create the Deployment, or false if you expect the controller to create it..
// Target Version is required.
func (sb *StatusBuilder) WithTargetVersion(imageName string, status temporaliov1alpha1.VersionStatus, rampPercentage float32, healthy bool, createDeployment bool) *StatusBuilder {
	sb.targetVersionBuilder = func(twdName string, namespace string) temporaliov1alpha1.TargetWorkerDeploymentVersion {
		return MakeTargetVersion(namespace, twdName, imageName, status, rampPercentage, healthy, createDeployment)
	}
	return sb
}

// WithDeprecatedVersions adds deprecated versions to the status.
// Note: The image name and replica count are not stored in the status. If you need to know those in your test case,
// use TestCaseBuilder.WithDeprecatedVersions instead, which will add your deprecated versions to the status and also save
// the image names and replica counts that you need in the Test Case info.
func (sb *StatusBuilder) WithDeprecatedVersions(infos ...DeprecatedVersionInfo) *StatusBuilder {
	sb.deprecatedVersionsBuilder = func(twdName string, namespace string) []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion {
		ret := make([]*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion, len(infos))
		for i, info := range infos {
			ret[i] = MakeDeprecatedVersion(namespace, twdName, info.image, info.status, info.healthy, info.createDeployment, info.hasDeployment)

		}
		return ret
	}
	return sb
}

// Build returns the constructed status
func (sb *StatusBuilder) Build() *temporaliov1alpha1.TemporalWorkerDeploymentStatus {
	if sb.targetVersionBuilder == nil {
		return nil
	}
	ret := &temporaliov1alpha1.TemporalWorkerDeploymentStatus{
		TargetVersion: sb.targetVersionBuilder(sb.name, sb.k8sNamespace),
	}
	if sb.currentVersionBuilder != nil {
		ret.CurrentVersion = sb.currentVersionBuilder(sb.name, sb.k8sNamespace)
	}
	if sb.deprecatedVersionsBuilder != nil {
		ret.DeprecatedVersions = sb.deprecatedVersionsBuilder(sb.name, sb.k8sNamespace)
	}
	return ret
}

type TestCase struct {
	// If starting from a particular state, specify that in input.Status
	twd *temporaliov1alpha1.TemporalWorkerDeployment
	// TemporalWorkerDeploymentStatus only tracks the names of the Deployments for deprecated
	// versions, so for test scenarios that start with existing deprecated version Deployments,
	// specify the number of replicas for each deprecated build here.
	existingDeploymentReplicas map[string]int32
	// TemporalWorkerDeploymentStatus only tracks the build ids of the Deployments for deprecated
	// versions, not their images so for test scenarios that start with existing deprecated version Deployments,
	// specify the images for each deprecated build here.
	existingDeploymentImages map[string]string
	expectedStatus           *temporaliov1alpha1.TemporalWorkerDeploymentStatus
	// validate that deployments have correct # of replicas. TODO(carlydf): validate replica count for more than just the deprecated versions
	expectedDeploymentReplicas map[string]int32
	// Time to delay before checking expected status
	waitTime *time.Duration

	// Arbitrary function called at the end of setting up the environment specified by input.Status.
	// Can be used for additional state creation / destruction
	setupFunc func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)
}

func (tc *TestCase) GetTWD() *temporaliov1alpha1.TemporalWorkerDeployment {
	return tc.twd
}

func (tc *TestCase) GetExistingDeploymentReplicas() map[string]int32 {
	return tc.existingDeploymentReplicas
}

func (tc *TestCase) GetExistingDeploymentImages() map[string]string {
	return tc.existingDeploymentImages
}

func (tc *TestCase) GetExpectedStatus() *temporaliov1alpha1.TemporalWorkerDeploymentStatus {
	return tc.expectedStatus
}

func (tc *TestCase) GetExpectedDeploymentReplicas() map[string]int32 {
	return tc.expectedDeploymentReplicas
}

func (tc *TestCase) GetWaitTime() *time.Duration {
	return tc.waitTime
}

func (tc *TestCase) GetSetupFunc() func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv) {
	return tc.setupFunc
}

// TestCaseBuilder provides a fluent interface for building test cases
type TestCaseBuilder struct {
	name              string
	k8sNamespace      string
	temporalNamespace string

	twdBuilder              *TemporalWorkerDeploymentBuilder
	expectedStatusBuilder   *StatusBuilder
	existingDeploymentInfos []DeploymentInfo
	expectedDeploymentInfos []DeploymentInfo
	waitTime                *time.Duration

	setupFunc func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)
}

// NewTestCase creates a new test case builder
func NewTestCase() *TestCaseBuilder {
	return &TestCaseBuilder{
		twdBuilder:              NewTemporalWorkerDeploymentBuilder(),
		expectedStatusBuilder:   NewStatusBuilder(),
		existingDeploymentInfos: make([]DeploymentInfo, 0),
		expectedDeploymentInfos: make([]DeploymentInfo, 0),
	}
}

// NewTestCaseWithValues creates a new test case builder with the given values
func NewTestCaseWithValues(name, k8sNamespace, temporalNamespace string) *TestCaseBuilder {
	return &TestCaseBuilder{
		name:              name,
		k8sNamespace:      k8sNamespace,
		temporalNamespace: temporalNamespace,

		twdBuilder:              NewTemporalWorkerDeploymentBuilder(),
		expectedStatusBuilder:   NewStatusBuilder(),
		existingDeploymentInfos: make([]DeploymentInfo, 0),
		expectedDeploymentInfos: make([]DeploymentInfo, 0),
	}
}

// WithSetupFunction defines a function that the test case will call while setting up the state.
func (tcb *TestCaseBuilder) WithSetupFunction(f func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)) *TestCaseBuilder {
	tcb.setupFunc = f
	return tcb
}

// WithInput sets the input TWD
func (tcb *TestCaseBuilder) WithInput(twdBuilder *TemporalWorkerDeploymentBuilder) *TestCaseBuilder {
	tcb.twdBuilder = twdBuilder
	return tcb
}

// WithWaitTime sets the wait time. Use this if you are expecting no change to the initial status and want to ensure
// that after some time, there is still no change.
func (tcb *TestCaseBuilder) WithWaitTime(waitTime time.Duration) *TestCaseBuilder {
	tcb.waitTime = &waitTime
	return tcb
}

type DeprecatedVersionInfo struct {
	image   string // determines build id
	status  temporaliov1alpha1.VersionStatus
	healthy bool
	// set to true if the test runner needs to create the Deployment, false if the controller will create it
	createDeployment bool
	// set to true if there is a kubernetes Deployment currently running for this version
	hasDeployment bool
}

func NewDeprecatedVersionInfo(imageName string, status temporaliov1alpha1.VersionStatus, healthy, createDeployment, hasDeployment bool) DeprecatedVersionInfo {
	return DeprecatedVersionInfo{
		image:            imageName,
		status:           status,
		healthy:          healthy,
		createDeployment: createDeployment,
		hasDeployment:    hasDeployment,
	}
}

// DeploymentInfo defines the necessary information about a Deployment, so that tests can
// recreate and validate state that is not visible in the TemporalWorkerDeployment status
type DeploymentInfo struct {
	image    string
	replicas int32
}

func NewDeploymentInfo(imageName string, replicas int32) DeploymentInfo {
	return DeploymentInfo{
		image:    imageName,
		replicas: replicas,
	}
}

// WithExistingDeployments adds info to create existing deployments, indexed by the build id that the given image would result in
func (tcb *TestCaseBuilder) WithExistingDeployments(existingDeploymentInfos ...DeploymentInfo) *TestCaseBuilder {
	tcb.existingDeploymentInfos = existingDeploymentInfos
	return tcb
}

// WithExpectedDeployments adds info verify deployments, indexed by the build id that the given image would result in
func (tcb *TestCaseBuilder) WithExpectedDeployments(expectedDeploymentInfos ...DeploymentInfo) *TestCaseBuilder {
	tcb.expectedDeploymentInfos = expectedDeploymentInfos
	return tcb
}

// WithExpectedStatus sets the expected status
func (tcb *TestCaseBuilder) WithExpectedStatus(statusBuilder *StatusBuilder) *TestCaseBuilder {
	tcb.expectedStatusBuilder = statusBuilder
	return tcb
}

// Build returns the constructed test case
func (tcb *TestCaseBuilder) Build() TestCase {
	ret := TestCase{
		setupFunc: tcb.setupFunc,
		waitTime:  tcb.waitTime,
		twd: tcb.twdBuilder.
			WithName(tcb.name).
			WithNamespace(tcb.k8sNamespace).
			WithTemporalConnection(tcb.name).
			WithTemporalNamespace(tcb.temporalNamespace).
			Build(),
		existingDeploymentReplicas: make(map[string]int32),
		existingDeploymentImages:   make(map[string]string),
		expectedStatus: tcb.expectedStatusBuilder.
			WithName(tcb.name).
			WithNamespace(tcb.k8sNamespace).
			Build(),
		expectedDeploymentReplicas: make(map[string]int32),
	}
	for _, info := range tcb.existingDeploymentInfos {
		buildId := MakeBuildId(tcb.name, info.image, nil)
		ret.existingDeploymentReplicas[buildId] = info.replicas
		ret.existingDeploymentImages[buildId] = info.image
	}
	for _, info := range tcb.expectedDeploymentInfos {
		buildId := MakeBuildId(tcb.name, info.image, nil)
		ret.expectedDeploymentReplicas[buildId] = info.replicas
	}
	ret.twd.Spec.Template = SetTaskQueue(ret.twd.Spec.Template, tcb.name)
	return ret
}

// BuildWithValues populates all fields affected by test name, k8s namespace, and temporal namespace and returns the constructed test case
func (tcb *TestCaseBuilder) BuildWithValues(name, k8sNamespace, temporalNamespace string) TestCase {
	tcb.name = name
	tcb.k8sNamespace = k8sNamespace
	tcb.temporalNamespace = temporalNamespace
	return tcb.Build()
}

// ProgressiveStep creates a progressive rollout step
func ProgressiveStep(rampPercentage float32, pauseDuration time.Duration) temporaliov1alpha1.RolloutStep {
	return temporaliov1alpha1.RolloutStep{
		RampPercentage: rampPercentage,
		PauseDuration:  metav1.Duration{Duration: pauseDuration},
	}
}

type TestEnv struct {
	K8sClient k8sclient.Client
	// Manager of the worker controller. Used to set controller ownership metadata on Deployments
	// created during test setup to make it seem as if they were created by the controller.
	Mgr        manager.Manager
	Ts         *temporaltest.TestServer
	Connection *temporaliov1alpha1.TemporalConnection
	// TemporalWorkerDeploymentStatus only tracks the build ids and Deployment names of the Deployments that have been
	// created, so for test scenarios that start with existing Deployments, specify the number of replicas for each.
	ExistingDeploymentReplicas map[string]int32
	// TemporalWorkerDeploymentStatus only tracks the build ids and Deployment names of the Deployments that have been
	// created, so for test scenarios that start with existing Deployments, specify the image names here, so that the
	// test runner can generate the same pod spec and build id as the controller.
	ExistingDeploymentImages map[string]string
	// TemporalWorkerDeploymentStatus only tracks the build ids and Deployment names of the Deployments that have been
	// created, so for test scenarios that check the replicas of Deployments after Reconciliation, specify the number
	// of replicas for each.
	ExpectedDeploymentReplicas map[string]int32
}
