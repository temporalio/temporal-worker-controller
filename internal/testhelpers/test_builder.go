package testhelpers

import (
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// WithVersion sets the worker version (creates HelloWorld pod spec)
func (b *TemporalWorkerDeploymentBuilder) WithVersion(version string) *TemporalWorkerDeploymentBuilder {
	b.twd.Spec.Template = MakeHelloWorldPodSpec(version)
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

// WithTargetVersionStatus sets the status to have a target version
func (b *TemporalWorkerDeploymentBuilder) WithTargetVersionStatus(imageName string, rampPercentage float32, healthy, createDeployment bool) *TemporalWorkerDeploymentBuilder {
	if b.statusBuilder == nil {
		b.statusBuilder = NewStatusBuilder()
	}

	b.statusBuilder.targetVersionBuilder = func(name string, namespace string) temporaliov1alpha1.TargetWorkerDeploymentVersion {
		return MakeTargetVersion(namespace, name, imageName, rampPercentage, healthy, createDeployment)
	}
	return b
}

// WithCurrentVersionStatus sets the status to have a current version
func (b *TemporalWorkerDeploymentBuilder) WithCurrentVersionStatus(imageName string, healthy, createDeployment bool) *TemporalWorkerDeploymentBuilder {
	if b.statusBuilder == nil {
		b.statusBuilder = NewStatusBuilder()
	}

	b.statusBuilder.currentVersionBuilder = func(name string, namespace string) *temporaliov1alpha1.CurrentWorkerDeploymentVersion {
		return MakeCurrentVersion(namespace, name, imageName, healthy, createDeployment)
	}
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
	sb.currentVersionBuilder = func(name string, namespace string) *temporaliov1alpha1.CurrentWorkerDeploymentVersion {
		return MakeCurrentVersion(namespace, name, imageName, healthy, createDeployment)
	}
	return sb
}

// WithTargetVersion sets the target version in the status.
// Target Version is required.
func (sb *StatusBuilder) WithTargetVersion(imageName string, rampPercentage float32, healthy bool, createDeployment bool) *StatusBuilder {
	sb.targetVersionBuilder = func(name string, namespace string) temporaliov1alpha1.TargetWorkerDeploymentVersion {
		return MakeTargetVersion(namespace, name, imageName, rampPercentage, healthy, createDeployment)
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
	deprecatedBuildReplicas map[string]int32
	deprecatedBuildImages   map[string]string
	expectedStatus          *temporaliov1alpha1.TemporalWorkerDeploymentStatus
	// Time to delay before checking expected status
	waitTime *time.Duration
}

func (tc *TestCase) GetTWD() *temporaliov1alpha1.TemporalWorkerDeployment {
	return tc.twd
}

func (tc *TestCase) GetDeprecatedBuildReplicas() map[string]int32 {
	return tc.deprecatedBuildReplicas
}

func (tc *TestCase) GetDeprecatedBuildImages() map[string]string {
	return tc.deprecatedBuildImages
}

func (tc *TestCase) GetExpectedStatus() *temporaliov1alpha1.TemporalWorkerDeploymentStatus {
	return tc.expectedStatus
}

func (tc *TestCase) GetWaitTime() *time.Duration {
	return tc.waitTime
}

// TestCaseBuilder provides a fluent interface for building test cases
type TestCaseBuilder struct {
	name              string
	k8sNamespace      string
	temporalNamespace string

	twdBuilder             *TemporalWorkerDeploymentBuilder
	expectedStatusBuilder  *StatusBuilder
	deprecatedVersionInfos []DeprecatedVersionInfo
	waitTime               *time.Duration
}

// NewTestCase creates a new test case builder
func NewTestCase() *TestCaseBuilder {
	return &TestCaseBuilder{
		twdBuilder:             NewTemporalWorkerDeploymentBuilder(),
		expectedStatusBuilder:  NewStatusBuilder(),
		deprecatedVersionInfos: make([]DeprecatedVersionInfo, 0),
	}
}

// NewTestCaseWithValues creates a new test case builder with the given values
func NewTestCaseWithValues(name, k8sNamespace, temporalNamespace string) *TestCaseBuilder {
	return &TestCaseBuilder{
		name:              name,
		k8sNamespace:      k8sNamespace,
		temporalNamespace: temporalNamespace,

		twdBuilder:             NewTemporalWorkerDeploymentBuilder(),
		expectedStatusBuilder:  NewStatusBuilder(),
		deprecatedVersionInfos: make([]DeprecatedVersionInfo, 0),
	}
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

// DeprecatedVersionInfo defines the necessary information about a deprecated worker version, so that
// tests can recreate state that is not visible in the TemporalWorkerDeployment status
type DeprecatedVersionInfo struct {
	image    string
	replicas int32
}

func NewDeprecatedVersionInfo(image string, replicas int32) DeprecatedVersionInfo {
	return DeprecatedVersionInfo{
		image:    image,
		replicas: replicas,
	}
}

// WithDeprecatedBuilds adds deprecated build replicas and images, indexed by the build id that the given image would result in
func (tcb *TestCaseBuilder) WithDeprecatedBuilds(deprecatedVersionInfos ...DeprecatedVersionInfo) *TestCaseBuilder {
	tcb.deprecatedVersionInfos = deprecatedVersionInfos
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
		waitTime: tcb.waitTime,
		twd: tcb.twdBuilder.
			WithName(tcb.name).
			WithNamespace(tcb.k8sNamespace).
			WithTemporalConnection(tcb.name).
			WithTemporalNamespace(tcb.temporalNamespace).
			Build(),
		deprecatedBuildReplicas: make(map[string]int32),
		deprecatedBuildImages:   make(map[string]string),
		expectedStatus: tcb.expectedStatusBuilder.
			WithName(tcb.name).
			WithNamespace(tcb.k8sNamespace).
			Build(),
	}
	for _, info := range tcb.deprecatedVersionInfos {
		buildId := MakeBuildId(tcb.name, info.image, nil)
		ret.deprecatedBuildReplicas[buildId] = info.replicas
		ret.deprecatedBuildImages[buildId] = info.image
	}
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
