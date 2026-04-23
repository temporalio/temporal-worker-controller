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

// WorkerDeploymentBuilder provides a fluent interface for building test TWD objects
type WorkerDeploymentBuilder struct {
	twd *temporaliov1alpha1.WorkerDeployment

	statusBuilder *StatusBuilder
}

// NewWorkerDeploymentBuilder creates a new builder with sensible defaults
func NewWorkerDeploymentBuilder() *WorkerDeploymentBuilder {
	return &WorkerDeploymentBuilder{
		twd: MakeTWDWithName("", ""),
	}
}

// WithName sets the name
func (b *WorkerDeploymentBuilder) WithName(name string) *WorkerDeploymentBuilder {
	b.twd.ObjectMeta.Name = name
	b.twd.Name = name
	return b
}

// WithNamespace sets the namespace
func (b *WorkerDeploymentBuilder) WithNamespace(namespace string) *WorkerDeploymentBuilder {
	b.twd.ObjectMeta.Namespace = namespace
	return b
}

// WithManualStrategy sets the rollout strategy to manual
func (b *WorkerDeploymentBuilder) WithManualStrategy() *WorkerDeploymentBuilder {
	b.twd.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateManual
	return b
}

// WithAllAtOnceStrategy sets the rollout strategy to all-at-once
func (b *WorkerDeploymentBuilder) WithAllAtOnceStrategy() *WorkerDeploymentBuilder {
	b.twd.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateAllAtOnce
	return b
}

// WithProgressiveStrategy sets the rollout strategy to progressive with given steps
func (b *WorkerDeploymentBuilder) WithProgressiveStrategy(steps ...temporaliov1alpha1.RolloutStep) *WorkerDeploymentBuilder {
	b.twd.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
	b.twd.Spec.RolloutStrategy.Steps = steps
	return b
}

// WithGate sets the rollout strategy have a gate workflow
func (b *WorkerDeploymentBuilder) WithGate(expectSuccess bool) *WorkerDeploymentBuilder {
	if expectSuccess {
		b.twd.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: successTestWorkflowType}
	} else {
		b.twd.Spec.RolloutStrategy.Gate = &temporaliov1alpha1.GateWorkflowConfig{WorkflowType: failTestWorkflowType}
	}
	return b
}

// WithReplicas sets the number of replicas
func (b *WorkerDeploymentBuilder) WithReplicas(replicas int32) *WorkerDeploymentBuilder {
	b.twd.Spec.Replicas = &replicas
	return b
}

// WithTargetTemplate sets the template of the worker deployment to a pod spec with the given image name, thus defining the target version.
func (b *WorkerDeploymentBuilder) WithTargetTemplate(imageName string) *WorkerDeploymentBuilder {
	b.twd.Spec.Template = MakePodSpecWithImage(imageName)
	return b
}

// WithUnsafeCustomBuildID sets the optional custom build id of the TWD, thus defining a stable target version separate from the hash of the pod spec.
func (b *WorkerDeploymentBuilder) WithUnsafeCustomBuildID(buildID string) *WorkerDeploymentBuilder {
	b.twd.Spec.WorkerOptions.UnsafeCustomBuildID = buildID
	return b
}

// WithConnection sets the temporal connection name
func (b *WorkerDeploymentBuilder) WithConnection(connectionName string) *WorkerDeploymentBuilder {
	b.twd.Spec.WorkerOptions.ConnectionRef = temporaliov1alpha1.ConnectionReference{Name: connectionName}
	return b
}

// WithTemporalNamespace sets the temporal namespace
func (b *WorkerDeploymentBuilder) WithTemporalNamespace(temporalNamespace string) *WorkerDeploymentBuilder {
	b.twd.Spec.WorkerOptions.TemporalNamespace = temporalNamespace
	return b
}

// WithLabels sets the labels
func (b *WorkerDeploymentBuilder) WithLabels(labels map[string]string) *WorkerDeploymentBuilder {
	if b.twd.ObjectMeta.Labels == nil {
		b.twd.ObjectMeta.Labels = make(map[string]string)
	}
	for k, v := range labels {
		b.twd.ObjectMeta.Labels[k] = v
	}
	return b
}

func (b *WorkerDeploymentBuilder) WithStatus(statusBuilder *StatusBuilder) *WorkerDeploymentBuilder {
	b.statusBuilder = statusBuilder
	return b
}

// Build returns the constructed WorkerDeployment
func (b *WorkerDeploymentBuilder) Build() *temporaliov1alpha1.WorkerDeployment {
	// Set defaults if not already set
	if b.twd.Spec.WorkerOptions.ConnectionRef.Name == "" {
		b.twd.Spec.WorkerOptions.ConnectionRef = temporaliov1alpha1.ConnectionReference{Name: b.twd.Name}
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
		return MakeCurrentVersion(namespace, twdName, imageName, "", healthy, createDeployment)
	}
	return sb
}

// WithTargetVersion sets the target version in the status.
// Set createDeployment to true if the test runner should create the Deployment, or false if you expect the controller to create it..
// Target Version is required.
func (sb *StatusBuilder) WithTargetVersion(imageName string, status temporaliov1alpha1.VersionStatus, rampPercentage int32, healthy bool, createDeployment bool) *StatusBuilder {
	sb.targetVersionBuilder = func(twdName string, namespace string) temporaliov1alpha1.TargetWorkerDeploymentVersion {
		return MakeTargetVersion(namespace, twdName, imageName, "", status, rampPercentage, healthy, createDeployment)
	}
	return sb
}

// WithTargetVersionWithCustomBuild sets the target version in the status with a custom build id not based on the pod spec.
// Set createDeployment to true if the test runner should create the Deployment, or false if you expect the controller to create it..
// Target Version is required.
func (sb *StatusBuilder) WithTargetVersionWithCustomBuild(imageName, unsafeCustomBuildID string, status temporaliov1alpha1.VersionStatus, rampPercentage int32, healthy bool, createDeployment bool) *StatusBuilder {
	sb.targetVersionBuilder = func(twdName string, namespace string) temporaliov1alpha1.TargetWorkerDeploymentVersion {
		tv := MakeTargetVersion(namespace, twdName, imageName, unsafeCustomBuildID, status, rampPercentage, healthy, createDeployment)
		return tv
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
			ret[i] = MakeDeprecatedVersion(namespace, twdName, info.image, "", info.status, info.healthy, info.createDeployment, info.hasDeployment)

		}
		return ret
	}
	return sb
}

// Build returns the constructed status
func (sb *StatusBuilder) Build() *temporaliov1alpha1.WorkerDeploymentStatus {
	if sb.targetVersionBuilder == nil {
		return nil
	}
	ret := &temporaliov1alpha1.WorkerDeploymentStatus{
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

// TestCase represents a single test scenario for integration testing of WorkerDeployment controllers.
// It encapsulates the input state, expected outputs, and any additional setup required for the test.
type TestCase struct {
	// twd is the WorkerDeployment resource to test with.
	// If starting from a particular state, specify that in input.Status.
	twd *temporaliov1alpha1.WorkerDeployment

	// existingDeploymentReplicas specifies the number of replicas for each deprecated build.
	// WorkerDeploymentStatus only tracks the names of the Deployments for deprecated
	// versions, so for test scenarios that start with existing deprecated version Deployments,
	// specify the number of replicas for each deprecated build here.
	existingDeploymentReplicas map[string]int32

	// existingDeploymentImages specifies the images for each deprecated build.
	// WorkerDeploymentStatus only tracks the build ids of the Deployments for deprecated
	// versions, not their images so for test scenarios that start with existing deprecated version Deployments,
	// specify the images for each deprecated build here.
	existingDeploymentImages map[string]string

	// expectedStatus is the expected WorkerDeploymentStatus after the test completes.
	expectedStatus *temporaliov1alpha1.WorkerDeploymentStatus

	// expectedDeploymentReplicas validates that deployments have correct number of replicas.
	// TODO(carlydf): validate replica count for more than just the deprecated versions
	expectedDeploymentReplicas map[string]int32

	// waitTime is the duration to delay before checking expected status.
	waitTime *time.Duration

	// setupFunc is an arbitrary function called at the end of setting up the environment, after making the state match input.Status.
	// It can be used for additional state creation or destruction.
	setupFunc func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)

	// twdMutatorFunc is called on the TWD immediately before it is created in the API server.
	// Use this for test-specific TWD modifications not expressible through the builder
	// (e.g. setting a gate config with InputFrom.ConfigMapKeyRef).
	twdMutatorFunc func(*temporaliov1alpha1.WorkerDeployment)

	// postTWDCreateFunc is called immediately after the TWD is created but before the runner
	// waits for the target Deployment. Use this to inject steps that must happen after TWD
	// creation but before the rollout proceeds (e.g. asserting a blocked rollout, then
	// creating the resource that unblocks it).
	postTWDCreateFunc func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)

	// validatorFunc is an arbitrary function called after the test validates the expected TWD Status has been achieved.
	// It can be used for additional state validation.
	validatorFunc func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)
}

func (tc *TestCase) GetTWD() *temporaliov1alpha1.WorkerDeployment {
	return tc.twd
}

func (tc *TestCase) GetExistingDeploymentReplicas() map[string]int32 {
	return tc.existingDeploymentReplicas
}

func (tc *TestCase) GetExistingDeploymentImages() map[string]string {
	return tc.existingDeploymentImages
}

func (tc *TestCase) GetExpectedStatus() *temporaliov1alpha1.WorkerDeploymentStatus {
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

func (tc *TestCase) GetTWDMutatorFunc() func(*temporaliov1alpha1.WorkerDeployment) {
	return tc.twdMutatorFunc
}

func (tc *TestCase) GetPostTWDCreateFunc() func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv) {
	return tc.postTWDCreateFunc
}

func (tc *TestCase) GetValidatorFunc() func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv) {
	return tc.validatorFunc
}

// TestCaseBuilder provides a fluent interface for building test cases
type TestCaseBuilder struct {
	name              string
	k8sNamespace      string
	temporalNamespace string

	twdBuilder              *WorkerDeploymentBuilder
	expectedStatusBuilder   *StatusBuilder
	existingDeploymentInfos []DeploymentInfo
	expectedDeploymentInfos []DeploymentInfo
	waitTime                *time.Duration

	setupFunc         func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)
	twdMutatorFunc    func(*temporaliov1alpha1.WorkerDeployment)
	postTWDCreateFunc func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)
	validatorFunc     func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)
}

// NewTestCase creates a new test case builder
func NewTestCase() *TestCaseBuilder {
	return &TestCaseBuilder{
		twdBuilder:              NewWorkerDeploymentBuilder(),
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

		twdBuilder:              NewWorkerDeploymentBuilder(),
		expectedStatusBuilder:   NewStatusBuilder(),
		existingDeploymentInfos: make([]DeploymentInfo, 0),
		expectedDeploymentInfos: make([]DeploymentInfo, 0),
	}
}

// WithSetupFunction defines a function that the test case will call while setting up the state, after creating the initial Status.
func (tcb *TestCaseBuilder) WithSetupFunction(f func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)) *TestCaseBuilder {
	tcb.setupFunc = f
	return tcb
}

// WithValidatorFunction defines a function called by the runner after both
// verifyWorkerDeploymentStatusEventually and verifyTemporalStateMatchesStatusEventually
// have confirmed the TWD has reached its expected state. Use it for additional assertions beyond
// the standard TWD status and Temporal state checks — for example WRT-specific resource
// inspection or multi-phase rollout scenarios that require further TWD updates.
func (tcb *TestCaseBuilder) WithValidatorFunction(f func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)) *TestCaseBuilder {
	tcb.validatorFunc = f
	return tcb
}

// WithTWDMutatorFunc defines a function called on the TWD immediately before it is created.
// Use this for test-specific modifications that are not expressible through the builder
// (e.g. setting a gate config with InputFrom.ConfigMapKeyRef).
func (tcb *TestCaseBuilder) WithTWDMutatorFunc(f func(*temporaliov1alpha1.WorkerDeployment)) *TestCaseBuilder {
	tcb.twdMutatorFunc = f
	return tcb
}

// WithPostTWDCreateFunc defines a function called immediately after the TWD is created but before
// the runner waits for the target Deployment. Use this to inject steps that must happen after
// TWD creation but before the rollout proceeds (e.g. asserting a blocked rollout, then creating
// the resource that unblocks it).
func (tcb *TestCaseBuilder) WithPostTWDCreateFunc(f func(t *testing.T, ctx context.Context, tc TestCase, env TestEnv)) *TestCaseBuilder {
	tcb.postTWDCreateFunc = f
	return tcb
}

// WithInput sets the input TWD
func (tcb *TestCaseBuilder) WithInput(twdBuilder *WorkerDeploymentBuilder) *TestCaseBuilder {
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
// recreate and validate state that is not visible in the WorkerDeployment status
type DeploymentInfo struct {
	image               string
	replicas            int32
	unsafeCustomBuildID string
}

func NewDeploymentInfo(imageName string, replicas int32) DeploymentInfo {
	return DeploymentInfo{
		image:    imageName,
		replicas: replicas,
	}
}

func NewDeploymentInfoWithUnsafeCustomBuildID(imageName, unsafeCustomBuildID string, replicas int32) DeploymentInfo {
	return DeploymentInfo{
		image:               imageName,
		replicas:            replicas,
		unsafeCustomBuildID: unsafeCustomBuildID,
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
		setupFunc:         tcb.setupFunc,
		twdMutatorFunc:    tcb.twdMutatorFunc,
		postTWDCreateFunc: tcb.postTWDCreateFunc,
		validatorFunc:     tcb.validatorFunc,
		waitTime:          tcb.waitTime,
		twd: tcb.twdBuilder.
			WithName(tcb.name).
			WithNamespace(tcb.k8sNamespace).
			WithConnection(tcb.name).
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
		buildId := MakeBuildID(tcb.name, info.image, info.unsafeCustomBuildID, nil)
		ret.existingDeploymentReplicas[buildId] = info.replicas
		ret.existingDeploymentImages[buildId] = info.image
	}
	for _, info := range tcb.expectedDeploymentInfos {
		buildId := MakeBuildID(tcb.name, info.image, info.unsafeCustomBuildID, nil)
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
func ProgressiveStep(rampPercentage int32, pauseDuration time.Duration) temporaliov1alpha1.RolloutStep {
	return temporaliov1alpha1.RolloutStep{
		RampPercentage: int(rampPercentage),
		PauseDuration:  metav1.Duration{Duration: pauseDuration},
	}
}

type TestEnv struct {
	K8sClient k8sclient.Client
	// Manager of the worker controller. Used to set controller ownership metadata on Deployments
	// created during test setup to make it seem as if they were created by the controller.
	Mgr        manager.Manager
	Ts         *temporaltest.TestServer
	Connection *temporaliov1alpha1.Connection
	// WorkerDeploymentStatus only tracks the build ids and Deployment names of the Deployments that have been
	// created, so for test scenarios that start with existing Deployments, specify the number of replicas for each.
	ExistingDeploymentReplicas map[string]int32
	// WorkerDeploymentStatus only tracks the build ids and Deployment names of the Deployments that have been
	// created, so for test scenarios that start with existing Deployments, specify the image names here, so that the
	// test runner can generate the same pod spec and build id as the controller.
	ExistingDeploymentImages map[string]string
	// WorkerDeploymentStatus only tracks the build ids and Deployment names of the Deployments that have been
	// created, so for test scenarios that check the replicas of Deployments after Reconciliation, specify the number
	// of replicas for each.
	ExpectedDeploymentReplicas map[string]int32
}
