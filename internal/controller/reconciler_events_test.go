// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	"github.com/temporalio/temporal-worker-controller/internal/temporal"
	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const testTemporalNamespace = "test-temporal-namespace"

// ─── Helpers ─────────────────────────────────────────────────────────────────

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = temporaliov1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	// Registered so tests can seed and assert on rendered WorkerResourceTemplate
	// copies (HPA templates) with the fake client.
	_ = autoscalingv2.AddToScheme(s)
	return s
}

// newTestReconciler creates a WorkerDeploymentReconciler with a fake client and recorder.
func newTestReconciler(objs []client.Object) (*WorkerDeploymentReconciler, *record.FakeRecorder) {
	return newTestReconcilerWithInterceptors(objs, interceptor.Funcs{})
}

// newTestReconcilerWithInterceptors creates a reconciler with a fake client that uses custom interceptors.
func newTestReconcilerWithInterceptors(objs []client.Object, funcs interceptor.Funcs) (*WorkerDeploymentReconciler, *record.FakeRecorder) {
	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&temporaliov1alpha1.WorkerDeployment{}, &temporaliov1alpha1.WorkerResourceTemplate{}).
		WithIndex(&appsv1.Deployment{}, deployOwnerKey, func(rawObj client.Object) []string {
			deploy := rawObj.(*appsv1.Deployment)
			owner := metav1.GetControllerOf(deploy)
			if owner == nil {
				return nil
			}
			if owner.APIVersion != temporaliov1alpha1.GroupVersion.String() || owner.Kind != "WorkerDeployment" {
				return nil
			}
			return []string{owner.Name}
		}).
		WithIndex(&temporaliov1alpha1.WorkerResourceTemplate{}, wrtWorkerRefKey, func(rawObj client.Object) []string {
			wrt := rawObj.(*temporaliov1alpha1.WorkerResourceTemplate)
			return []string{wrt.Spec.EffectiveWorkerDeploymentName()}
		}).
		WithInterceptorFuncs(funcs).
		Build()

	recorder := record.NewFakeRecorder(10)

	r := &WorkerDeploymentReconciler{
		Client:              fakeClient,
		Scheme:              scheme,
		TemporalClientPool:  clientpool.New(nil, fakeClient),
		Recorder:            recorder,
		DisableRecoverPanic: true,
		MaxDeploymentVersionsIneligibleForDeletion: 75,
	}

	return r, recorder
}

// makeWD creates a minimal WorkerDeployment for testing.
func makeWD(name, namespace, connectionName string) *temporaliov1alpha1.WorkerDeployment {
	replicas := int32(1)
	progressDeadline := int32(600)
	return &temporaliov1alpha1.WorkerDeployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "WorkerDeployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: temporaliov1alpha1.WorkerDeploymentSpec{
			Replicas:                &replicas,
			ProgressDeadlineSeconds: &progressDeadline,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: "temporal/worker:v1",
						},
					},
				},
			},
			WorkerOptions: temporaliov1alpha1.WorkerOptions{
				ConnectionRef: temporaliov1alpha1.ConnectionReference{
					Name: connectionName,
				},
				TemporalNamespace: testTemporalNamespace,
			},
			RolloutStrategy: temporaliov1alpha1.RolloutStrategy{
				Strategy: temporaliov1alpha1.UpdateAllAtOnce,
			},
			SunsetStrategy: temporaliov1alpha1.SunsetStrategy{
				ScaledownDelay: &metav1.Duration{},
				DeleteDelay:    &metav1.Duration{},
			},
		},
	}
}

// makeNoCredsConnection creates a minimal Connection for testing.
func makeNoCredsConnection(name, namespace, hostPort string) *temporaliov1alpha1.Connection {
	return &temporaliov1alpha1.Connection{
		TypeMeta: metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "Connection",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.ConnectionSpec{
			HostPort: hostPort,
		},
	}
}

// drainEvents reads all pending events from the recorder channel.
func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case event := <-recorder.Events:
			events = append(events, event)
		default:
			return events
		}
	}
}

// assertEventEmitted checks that at least one event with the given reason was emitted.
func assertEventEmitted(t *testing.T, events []string, reason string) {
	t.Helper()
	for _, event := range events {
		if strings.Contains(event, reason) {
			return
		}
	}
	t.Errorf("expected event with reason %q, got events: %v", reason, events)
}

// assertNoEventEmitted checks that no event with the given reason was emitted.
func assertNoEventEmitted(t *testing.T, events []string, reason string) {
	t.Helper()
	for _, event := range events {
		if strings.Contains(event, reason) {
			t.Errorf("unexpected event with reason %q found: %s", reason, event)
			return
		}
	}
}

// countWDStatusWrites returns interceptor funcs that count status writes issued for a
// WorkerDeployment, passing each one through to the fake client.
func countWDStatusWrites(count *int) interceptor.Funcs {
	return interceptor.Funcs{
		SubResourceUpdate: func(
			ctx context.Context,
			c client.Client,
			subResourceName string,
			obj client.Object,
			opts ...client.SubResourceUpdateOption,
		) error {
			if _, ok := obj.(*temporaliov1alpha1.WorkerDeployment); ok && subResourceName == "status" {
				*count++
			}
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	}
}

// ─── Stub types ──────────────────────────────────────────────────────────────

// stubWDHandle implements sdkclient.WorkerDeploymentHandle with configurable per-method errors.
type stubWDHandle struct {
	sdkclient.WorkerDeploymentHandle
	describeErr      error
	setCurrentErr    error
	setRampingErr    error
	updateMetaErr    error
	deleteVersionErr error
	deletedVersions  []string
}

func (s *stubWDHandle) Describe(_ context.Context, _ sdkclient.WorkerDeploymentDescribeOptions) (sdkclient.WorkerDeploymentDescribeResponse, error) {
	return sdkclient.WorkerDeploymentDescribeResponse{}, s.describeErr
}

func (s *stubWDHandle) SetCurrentVersion(_ context.Context, _ sdkclient.WorkerDeploymentSetCurrentVersionOptions) (sdkclient.WorkerDeploymentSetCurrentVersionResponse, error) {
	return sdkclient.WorkerDeploymentSetCurrentVersionResponse{}, s.setCurrentErr
}

func (s *stubWDHandle) SetRampingVersion(_ context.Context, _ sdkclient.WorkerDeploymentSetRampingVersionOptions) (sdkclient.WorkerDeploymentSetRampingVersionResponse, error) {
	return sdkclient.WorkerDeploymentSetRampingVersionResponse{}, s.setRampingErr
}

func (s *stubWDHandle) UpdateVersionMetadata(_ context.Context, _ sdkclient.WorkerDeploymentUpdateVersionMetadataOptions) (sdkclient.WorkerDeploymentUpdateVersionMetadataResponse, error) {
	return sdkclient.WorkerDeploymentUpdateVersionMetadataResponse{}, s.updateMetaErr
}

func (s *stubWDHandle) DeleteVersion(_ context.Context, opts sdkclient.WorkerDeploymentDeleteVersionOptions) (sdkclient.WorkerDeploymentDeleteVersionResponse, error) {
	s.deletedVersions = append(s.deletedVersions, opts.BuildID)
	return sdkclient.WorkerDeploymentDeleteVersionResponse{}, s.deleteVersionErr
}

// stubWDClient implements sdkclient.WorkerDeploymentClient, returning a fixed handle.
type stubWDClient struct {
	sdkclient.WorkerDeploymentClient
	handle sdkclient.WorkerDeploymentHandle
}

func (s *stubWDClient) GetHandle(_ string) sdkclient.WorkerDeploymentHandle { return s.handle }

// stubWorkflowServiceClient implements workflowservice.WorkflowServiceClient, returning
// a valid empty response for DescribeWorkerDeployment (no versions, no routing config),
// or a configurable error if describeDeploymentErr is set.
type stubWorkflowServiceClient struct {
	workflowservice.WorkflowServiceClient
	describeDeploymentErr error
}

func (s *stubWorkflowServiceClient) DescribeWorkerDeployment(_ context.Context, _ *workflowservice.DescribeWorkerDeploymentRequest, _ ...grpc.CallOption) (*workflowservice.DescribeWorkerDeploymentResponse, error) {
	if s.describeDeploymentErr != nil {
		return nil, s.describeDeploymentErr
	}
	return &workflowservice.DescribeWorkerDeploymentResponse{
		WorkerDeploymentInfo: &deploymentpb.WorkerDeploymentInfo{
			RoutingConfig: &deploymentpb.RoutingConfig{},
		},
	}, nil
}

func (s *stubWorkflowServiceClient) DescribeWorkerDeploymentVersion(_ context.Context, _ *workflowservice.DescribeWorkerDeploymentVersionRequest, _ ...grpc.CallOption) (*workflowservice.DescribeWorkerDeploymentVersionResponse, error) {
	return nil, &serviceerror.NotFound{}
}

// stubTemporalClient implements sdkclient.Client, routing WorkerDeploymentClient and
// ExecuteWorkflow to configurable stubs.
type stubTemporalClient struct {
	sdkclient.Client
	wdClient              sdkclient.WorkerDeploymentClient
	execErr               error
	describeDeploymentErr error
}

func (s *stubTemporalClient) WorkerDeploymentClient() sdkclient.WorkerDeploymentClient {
	return s.wdClient
}

func (s *stubTemporalClient) WorkflowService() workflowservice.WorkflowServiceClient {
	return &stubWorkflowServiceClient{describeDeploymentErr: s.describeDeploymentErr}
}

func (s *stubTemporalClient) ExecuteWorkflow(_ context.Context, _ sdkclient.StartWorkflowOptions, _ interface{}, _ ...interface{}) (sdkclient.WorkflowRun, error) {
	return nil, s.execErr
}

// Close satisfies sdkclient.Client.Close so the stub can be evicted via
// ClientPool.EvictClient without panicking through the embedded nil Client
// interface.
func (s *stubTemporalClient) Close() {}

// newStubTemporalClient returns a stub client whose WorkflowService().DescribeWorkerDeployment
// returns a valid empty response, and whose ExecuteWorkflow returns execErr.
func newStubTemporalClient(execErr error) *stubTemporalClient {
	handle := &stubWDHandle{describeErr: &serviceerror.NotFound{}}
	return &stubTemporalClient{
		wdClient: &stubWDClient{handle: handle},
		execErr:  execErr,
	}
}

// newStubTemporalClientWithHandle wraps the given handle in a stub client, so that
// executePlan's WorkerDeploymentClient().GetHandle(...) returns that exact handle.
func newStubTemporalClientWithHandle(handle sdkclient.WorkerDeploymentHandle) *stubTemporalClient {
	return &stubTemporalClient{
		wdClient: &stubWDClient{handle: handle},
	}
}

// noCredsPoolKey returns the ClientPoolKey for a no-credentials Connection.
func noCredsPoolKey(hostPort, temporalNamespace string) clientpool.ClientPoolKey {
	return clientpool.ClientPoolKey{
		HostPort:   hostPort,
		Namespace:  temporalNamespace,
		SecretName: "",
		AuthMode:   temporaliov1alpha1.AuthModeNoCredentials,
	}
}

// ─── setCondition tests ───────────────────────────────────────────────────────

func TestSetCondition(t *testing.T) {
	r, _ := newTestReconciler(nil)

	t.Run("SetsNewCondition", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, "TestReason", "Test message")

		require.Len(t, twd.Status.Conditions, 1)
		assert.Equal(t, temporaliov1alpha1.ConditionReady, twd.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, twd.Status.Conditions[0].Status)
		assert.Equal(t, "TestReason", twd.Status.Conditions[0].Reason)
		assert.Equal(t, "Test message", twd.Status.Conditions[0].Message)
		assert.Equal(t, int64(1), twd.Status.Conditions[0].ObservedGeneration)
	})

	t.Run("UpdatesExistingCondition", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, "InitialReason", "Initial message")
		require.Len(t, twd.Status.Conditions, 1)

		r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, "UpdatedReason", "Updated message")

		require.Len(t, twd.Status.Conditions, 1, "update should not add a duplicate")
		assert.Equal(t, metav1.ConditionFalse, twd.Status.Conditions[0].Status)
		assert.Equal(t, "UpdatedReason", twd.Status.Conditions[0].Reason)
		assert.Equal(t, "Updated message", twd.Status.Conditions[0].Message)
	})

	t.Run("MultipleDifferentConditions", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, "TestReason", "All good")
		r.setCondition(twd, temporaliov1alpha1.ConditionProgressing, metav1.ConditionFalse, "TestReason", "Not progressing")

		require.Len(t, twd.Status.Conditions, 2)

		readyCond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionReady)
		require.NotNil(t, readyCond)
		assert.Equal(t, metav1.ConditionTrue, readyCond.Status)

		progressingCond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
		require.NotNil(t, progressingCond)
		assert.Equal(t, metav1.ConditionFalse, progressingCond.Status)
	})
}

func TestSyncConditions(t *testing.T) {
	r, _ := newTestReconciler(nil)

	assertCondition := func(t *testing.T, twd *temporaliov1alpha1.WorkerDeployment, condType string, status metav1.ConditionStatus, reason string) {
		t.Helper()
		cond := meta.FindStatusCondition(twd.Status.Conditions, condType)
		require.NotNil(t, cond, "condition %s should be set", condType)
		assert.Equal(t, status, cond.Status, "condition %s status", condType)
		assert.Equal(t, reason, cond.Reason, "condition %s reason", condType)
	}

	t.Run("ReadyWhenVersionIsCurrent", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		twd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
		// An empty (but non-nil) PollerHealth map represents a version with no task
		// queues seen yet to check -- vacuously active -- as opposed to nil, which
		// means poller status was never checked at all (Unknown).
		temporalState := &temporal.TemporalWorkerState{
			Versions: map[string]*temporal.VersionInfo{
				twd.Status.TargetVersion.BuildID: {PollerHealth: map[string]bool{}},
			},
		}
		r.syncConditions(twd, temporalState)

		assertCondition(t, twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, temporaliov1alpha1.ReasonRolloutComplete)
		assertCondition(t, twd, temporaliov1alpha1.ConditionProgressing, metav1.ConditionFalse, temporaliov1alpha1.ReasonActivePollers)
		// Deprecated conditions
		assertCondition(t, twd, temporaliov1alpha1.ConditionConnectionHealthy, metav1.ConditionTrue, temporaliov1alpha1.ReasonConnectionHealthy) //nolint:staticcheck // backward compat
		assertCondition(t, twd, temporaliov1alpha1.ConditionRolloutComplete, metav1.ConditionTrue, temporaliov1alpha1.ReasonRolloutComplete)     //nolint:staticcheck // backward compat
	})

	t.Run("ProgressingWhenCurrentVersionHasNoActivePollers", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		twd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
		temporalState := &temporal.TemporalWorkerState{
			Versions: map[string]*temporal.VersionInfo{
				twd.Status.TargetVersion.BuildID: {PollerHealth: map[string]bool{"tq-1": false}},
			},
		}
		r.syncConditions(twd, temporalState)

		// Ready stays about rollout completion; poller presence is surfaced on Progressing.
		assertCondition(t, twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, temporaliov1alpha1.ReasonRolloutComplete)
		assertCondition(t, twd, temporaliov1alpha1.ConditionProgressing, metav1.ConditionTrue, temporaliov1alpha1.ReasonWaitingForPollers)
	})

	t.Run("ProgressingUnknownWhenCurrentVersionPollerStatusUnknown", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		twd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusCurrent
		temporalState := &temporal.TemporalWorkerState{
			Versions: map[string]*temporal.VersionInfo{},
		}
		r.syncConditions(twd, temporalState)

		assertCondition(t, twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, temporaliov1alpha1.ReasonRolloutComplete)
		assertCondition(t, twd, temporaliov1alpha1.ConditionProgressing, metav1.ConditionFalse, temporaliov1alpha1.ReasonPollerStatusUnknown)
	})

	t.Run("ProgressingWhenVersionIsRamping", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		twd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusRamping
		r.syncConditions(twd, nil)

		assertCondition(t, twd, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, temporaliov1alpha1.ReasonRamping)
		assertCondition(t, twd, temporaliov1alpha1.ConditionProgressing, metav1.ConditionTrue, temporaliov1alpha1.ReasonRamping)
		// Deprecated conditions
		assertCondition(t, twd, temporaliov1alpha1.ConditionConnectionHealthy, metav1.ConditionTrue, temporaliov1alpha1.ReasonConnectionHealthy) //nolint:staticcheck // backward compat
	})

	t.Run("ProgressingWhenVersionIsInactive", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		twd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusInactive
		r.syncConditions(twd, nil)

		assertCondition(t, twd, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, temporaliov1alpha1.ReasonWaitingForPromotion)
		assertCondition(t, twd, temporaliov1alpha1.ConditionProgressing, metav1.ConditionTrue, temporaliov1alpha1.ReasonWaitingForPromotion)
		// Deprecated conditions
		assertCondition(t, twd, temporaliov1alpha1.ConditionConnectionHealthy, metav1.ConditionTrue, temporaliov1alpha1.ReasonConnectionHealthy) //nolint:staticcheck // backward compat
	})

	t.Run("ProgressingWhenVersionIsNotRegistered", func(t *testing.T) {
		twd := makeWD("test-worker", "default", "my-connection")
		twd.Status.TargetVersion.Status = temporaliov1alpha1.VersionStatusNotRegistered
		r.syncConditions(twd, nil)

		assertCondition(t, twd, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, temporaliov1alpha1.ReasonWaitingForPollers)
		assertCondition(t, twd, temporaliov1alpha1.ConditionProgressing, metav1.ConditionTrue, temporaliov1alpha1.ReasonWaitingForPollers)
		// Deprecated conditions
		assertCondition(t, twd, temporaliov1alpha1.ConditionConnectionHealthy, metav1.ConditionTrue, temporaliov1alpha1.ReasonConnectionHealthy) //nolint:staticcheck // backward compat
	})
}

func TestShouldEvictClient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "Nil",
			err:  nil,
			want: false,
		},
		{
			name: "DeadlineExceeded",
			err:  fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
			want: true,
		},
		{
			name: "Unavailable",
			err:  fmt.Errorf("wrapped: %w", serviceerror.NewUnavailable("temporary transport failure")),
			want: true,
		},
		{
			name: "PermissionDenied",
			err:  fmt.Errorf("wrapped: %w", serviceerror.NewPermissionDenied("bad credentials", "")),
			want: true,
		},
		{
			name: "Canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "ResourceExhausted",
			err:  serviceerror.NewResourceExhausted(enumspb.RESOURCE_EXHAUSTED_CAUSE_RPS_LIMIT, "rate limited"),
			want: false,
		},
		{
			name: "NotFound",
			err:  &serviceerror.NotFound{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldEvictClient(tt.err))
		})
	}
}

// ─── Reconcile tests ──────────────────────────────────────────────────────────

func TestReconcile_TWDNotFound_NoEvent(t *testing.T) {
	r, recorder := newTestReconciler(nil)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.Empty(t, drainEvents(recorder), "no events should be emitted when TWD is not found")
}

// TestReconcile_InvalidSpec_EmitsEventAndSetsCondition verifies that spec validation
// errors not enforceable by the CRD schema (e.g. rampPercentage ordering) surface as
// a Warning event and a blocked condition rather than being silently requeued.
func TestReconcile_InvalidSpec_EmitsEventAndSetsCondition(t *testing.T) {
	twd := makeWD("test-worker", "default", "my-connection")
	twd.Spec.RolloutStrategy = temporaliov1alpha1.RolloutStrategy{
		Strategy: temporaliov1alpha1.UpdateProgressive,
		Steps: []temporaliov1alpha1.RolloutStep{
			{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: time.Minute}},
			{RampPercentage: 10, PauseDuration: metav1.Duration{Duration: time.Minute}}, // decreasing — invalid
		},
	}
	tc := makeNoCredsConnection("my-connection", "default", "localhost:7233")
	r, recorder := newTestReconciler([]client.Object{twd, tc})

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})

	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter, "should not requeue — spec update will re-trigger reconciliation")

	events := drainEvents(recorder)
	assertEventEmitted(t, events, temporaliov1alpha1.ReasonInvalidSpec)

	var updated temporaliov1alpha1.WorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updated))
	cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
	require.NotNil(t, cond, "Progressing condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, temporaliov1alpha1.ReasonInvalidSpec, cond.Reason)
}

// TestReconcile_ConnectionNotFound covers all three related assertions: event emission,
// event message content, and condition update.
func TestReconcile_ConnectionNotFound(t *testing.T) {
	connName := "nonexistent-connection"
	twd := makeWD("test-worker", "default", connName)
	r, recorder := newTestReconciler([]client.Object{twd})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})

	require.Error(t, err)

	events := drainEvents(recorder)
	assertEventEmitted(t, events, temporaliov1alpha1.ReasonConnectionNotFound)
	for _, event := range events {
		if strings.Contains(event, temporaliov1alpha1.ReasonConnectionNotFound) {
			assert.Contains(t, event, connName, "event message should include the missing connection name")
			assert.Contains(t, event, "Warning")
		}
	}

	var updated temporaliov1alpha1.WorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updated))
	cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
	require.NotNil(t, cond, "Progressing condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, temporaliov1alpha1.ReasonConnectionNotFound, cond.Reason)
	assert.Contains(t, cond.Message, connName)
	// Deprecated condition
	connHealthy := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionConnectionHealthy) //nolint:staticcheck // backward compat
	require.NotNil(t, connHealthy, "deprecated ConnectionHealthy condition should be set")
	assert.Equal(t, metav1.ConditionFalse, connHealthy.Status)
}

// TestReconcile_ConnectionUnhealthy verifies that credential configuration
// errors (regardless of auth type) emit ReasonAuthSecretInvalid and set the
// ConnectionHealthy condition to False.
//
// ReasonAuthSecretInvalid fires for two distinct failure modes:
//   - resolveAuthSecretName: the secret ref exists but has an empty name (spec validation gap)
//   - ParseClientSecret:     the named k8s Secret cannot be fetched (not found, wrong type, etc.)
//
// ReasonTemporalClientCreationFailed fires only when DialAndUpsertClient fails (network/Temporal
// error). That path requires a live server and is covered by the integration test
// conditions-client-creation-failed.
func TestReconcile_ConnectionUnhealthy(t *testing.T) {
	cases := []struct {
		name           string
		setupConn      func(*temporaliov1alpha1.Connection)
		expectedReason string
	}{
		{
			// Secret name is non-empty but the k8s Secret doesn't exist; ParseClientSecret
			// returns a not-found error, which is reported as AuthSecretInvalid (not ClientCreationFailed).
			name: "MissingTLSSecret_AuthSecretInvalid",
			setupConn: func(tc *temporaliov1alpha1.Connection) {
				tc.Spec.MutualTLSSecretRef = &temporaliov1alpha1.SecretReference{Name: "missing-tls-secret"}
			},
			expectedReason: temporaliov1alpha1.ReasonAuthSecretInvalid,
		},
		{
			// Same as above for API key auth.
			name: "MissingAPIKeySecret_AuthSecretInvalid",
			setupConn: func(tc *temporaliov1alpha1.Connection) {
				tc.Spec.APIKeySecretRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "missing-api-key-secret"},
					Key:                  "api-key",
				}
			},
			expectedReason: temporaliov1alpha1.ReasonAuthSecretInvalid,
		},
		{
			// Secret ref is present but name is empty; resolveAuthSecretName returns an error.
			name: "MalformedTLSSecret_AuthSecretInvalid",
			setupConn: func(tc *temporaliov1alpha1.Connection) {
				tc.Spec.MutualTLSSecretRef = &temporaliov1alpha1.SecretReference{Name: ""}
			},
			expectedReason: temporaliov1alpha1.ReasonAuthSecretInvalid,
		},
		{
			// Same as above for API key auth.
			name: "MalformedAPIKeySecret_AuthSecretInvalid",
			setupConn: func(tc *temporaliov1alpha1.Connection) {
				tc.Spec.APIKeySecretRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ""},
					Key:                  "api-key",
				}
			},
			expectedReason: temporaliov1alpha1.ReasonAuthSecretInvalid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := makeNoCredsConnection("my-connection", "default", "localhost:7233")
			tc.setupConn(conn)
			twd := makeWD("test-worker", conn.Namespace, conn.Name)
			r, recorder := newTestReconciler([]client.Object{twd, conn})

			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
			})

			require.Error(t, err)
			assertEventEmitted(t, drainEvents(recorder), tc.expectedReason)

			var updated temporaliov1alpha1.WorkerDeployment
			require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updated))
			cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tc.expectedReason, cond.Reason)
			// Deprecated condition
			connHealthy := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionConnectionHealthy) //nolint:staticcheck // backward compat
			require.NotNil(t, connHealthy, "deprecated ConnectionHealthy condition should be set")
			assert.Equal(t, metav1.ConditionFalse, connHealthy.Status)
		})
	}
}

// TestReconcile_PlanGenerationFailed_EmitsEvent injects a List failure on the second call.
// The first List (in worker_controller.go) succeeds; the second (inside generatePlan) fails,
// which causes Reconcile to emit ReasonPlanGenerationFailed.
func TestReconcile_PlanGenerationFailed_EmitsEvent(t *testing.T) {
	k8sNamespace := "default"
	hostPort := "localhost:7233"

	tc := makeNoCredsConnection("my-conn", k8sNamespace, hostPort)
	twd := makeWD("test-worker", k8sNamespace, tc.Name)

	listCallCount := 0
	r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd, tc}, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			listCallCount++
			if listCallCount > 1 {
				return fmt.Errorf("simulated List failure on call #%d", listCallCount)
			}
			return c.List(ctx, list, opts...)
		},
	})

	r.TemporalClientPool.SetClientForTesting(
		noCredsPoolKey(tc.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace),
		newStubTemporalClient(nil),
	)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})

	require.Error(t, err)
	assertEventEmitted(t, drainEvents(recorder), ReasonPlanGenerationFailed)

	// Verifying that ConditionProgressing=False is set
	var updated temporaliov1alpha1.WorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updated))
	cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
	require.NotNil(t, cond, "Progressing condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonPlanGenerationFailed, cond.Reason)
	// PlanGenerationFailed is not a connection issue — ConnectionHealthy must not be set.
	connHealthy := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionConnectionHealthy) //nolint:staticcheck // backward compat
	assert.Nil(t, connHealthy, "ConnectionHealthy should not be set for plan generation failures")
}

// TestReconcile_PlanExecutionFailed_EmitsEvent injects a Create failure so that
// executeK8sOperations fails for the new Deployment that a fresh TWD always needs,
// causing Reconcile to emit ReasonPlanExecutionFailed.
func TestReconcile_PlanExecutionFailed_EmitsEvent(t *testing.T) {
	k8sNamespace := "default"
	hostPort := "localhost:7233"

	tc := makeNoCredsConnection("my-conn", k8sNamespace, hostPort)
	twd := makeWD("test-worker", k8sNamespace, tc.Name)

	r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd, tc}, interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				return errors.New("simulated Deployment create failure")
			}
			return nil
		},
	})

	r.TemporalClientPool.SetClientForTesting(
		noCredsPoolKey(tc.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace),
		newStubTemporalClient(nil),
	)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})

	require.Error(t, err)
	assertEventEmitted(t, drainEvents(recorder), ReasonPlanExecutionFailed)

	var updated temporaliov1alpha1.WorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updated))
	cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionProgressing)
	require.NotNil(t, cond, "Progressing condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, ReasonPlanExecutionFailed, cond.Reason)
	// PlanExecutionFailed is not a connection issue — ConnectionHealthy must not be set.
	connHealthy := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionConnectionHealthy) //nolint:staticcheck // backward compat
	assert.Nil(t, connHealthy, "ConnectionHealthy should not be set for plan execution failures")
}

// TestReconcile_DescribeWorkerDeploymentNotFound verifies that when the gRPC
// DescribeWorkerDeployment call returns NotFound (no deployment exists in Temporal yet),
// reconciliation succeeds and proceeds to plan generation (creating a new k8s Deployment).
func TestReconcile_DescribeWorkerDeploymentNotFound(t *testing.T) {
	k8sNamespace := "default"
	hostPort := "localhost:7233"

	tc := makeNoCredsConnection("my-conn", k8sNamespace, hostPort)
	twd := makeWD("test-worker", k8sNamespace, tc.Name)

	r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd, tc}, interceptor.Funcs{})

	stub := newStubTemporalClient(nil)
	stub.describeDeploymentErr = &serviceerror.NotFound{}
	r.TemporalClientPool.SetClientForTesting(
		noCredsPoolKey(tc.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace),
		stub,
	)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})

	// NotFound on the first reconcile means the Worker Deployment has not come up on the
	// server side yet; however, the k8s Deployment would be created by the controller
	// with no reconciliation errors.
	require.NoError(t, err)
	assertNoEventEmitted(t, drainEvents(recorder), ReasonPlanGenerationFailed)
}

// TestReconcile_SteadyState_SkipsStatusWrite verifies that once the rollout settles, a
// reconcile that recomputes the same status does not send it back to the API server.
func TestReconcile_SteadyState_SkipsStatusWrite(t *testing.T) {
	ctx := context.Background()
	k8sNamespace := "default"

	tc := makeNoCredsConnection("my-conn", k8sNamespace, "localhost:7233")
	twd := makeWD("test-worker", k8sNamespace, tc.Name)

	writes := 0
	r, _ := newTestReconcilerWithInterceptors([]client.Object{twd, tc}, countWDStatusWrites(&writes))
	r.TemporalClientPool.SetClientForTesting(
		noCredsPoolKey(tc.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace),
		newStubTemporalClient(nil),
	)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}}
	// Reconcile enough times to reach steady state. The first pass computes the status before creating
	// the k8s Deployment, then the second pass adds the k8s Deployment reference to the status once it
	// exists. On the third pass there is nothing new to write.
	for i := 0; i < 5; i++ {
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}

	// Nothing has changed and there should be no further status updates after a few reconciles.
	writes = 0
	for i := 0; i < 5; i++ {
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}
	assert.Zero(t, writes)
}

// TestReconcile_SpecChange_StillWritesStatus verifies that once the rollout settles, a change that
// makes the status differ again is still written to the API server.
func TestReconcile_SpecChange_StillWritesStatus(t *testing.T) {
	ctx := context.Background()
	k8sNamespace := "default"

	tc := makeNoCredsConnection("my-conn", k8sNamespace, "localhost:7233")
	twd := makeWD("test-worker", k8sNamespace, tc.Name)

	writes := 0
	r, _ := newTestReconcilerWithInterceptors([]client.Object{twd, tc}, countWDStatusWrites(&writes))
	r.TemporalClientPool.SetClientForTesting(
		noCredsPoolKey(tc.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace),
		newStubTemporalClient(nil),
	)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}}
	// Reconcile enough times to reach steady state (settles on pass 3, see test case above)
	for i := 0; i < 5; i++ {
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
	}

	var settled temporaliov1alpha1.WorkerDeployment
	require.NoError(t, r.Get(ctx, req.NamespacedName, &settled))

	// Trigger a status change by updating the image tag
	settled.Spec.Template.Spec.Containers[0].Image = "temporal/worker:v2"
	require.NoError(t, r.Update(ctx, &settled))

	writes = 0
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	// A changed status must have been written, exactly once for the one reconcile
	assert.Equal(t, 1, writes)
}

// TestReconcile_EvictsCachedClientOnTransportFailure verifies that transport-level
// failures from the main Reconcile path evict the cached SDK client. Otherwise the
// next reconcile would reuse the same client and can remain wedged until the
// controller pod restarts and drops the in-memory pool.
func TestReconcile_EvictsCachedClientOnTransportFailure(t *testing.T) {
	k8sNamespace := "default"
	hostPort := "localhost:7233"

	conn := makeNoCredsConnection("my-conn", k8sNamespace, hostPort)
	twd := makeWD("test-worker", k8sNamespace, conn.Name)

	r, recorder := newTestReconciler([]client.Object{twd, conn})

	poolKey := noCredsPoolKey(conn.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace)
	poisoned := newStubTemporalClient(nil)
	poisoned.describeDeploymentErr = context.DeadlineExceeded
	r.TemporalClientPool.SetClientForTesting(poolKey, poisoned)

	cached, ok := r.TemporalClientPool.GetSDKClient(poolKey)
	require.True(t, ok, "poisoned client should be cached before Reconcile runs")
	require.Same(t, poisoned, cached)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})
	require.Error(t, err, "Reconcile must surface the Temporal Describe error")
	require.ErrorIs(t, err, context.DeadlineExceeded, "the original transport error must propagate")
	assertEventEmitted(t, drainEvents(recorder), temporaliov1alpha1.ReasonTemporalStateFetchFailed)

	_, ok = r.TemporalClientPool.GetSDKClient(poolKey)
	require.False(t, ok, "poisoned client must be evicted so the next reconcile dials a fresh one")
}

// ─── executeK8sOperations tests ──────────────────────────────────────────────

func TestExecuteK8sOperations_EmitsEventOnFailure(t *testing.T) {
	namespace := "default"
	twd := makeWD("test-worker", namespace, "my-conn")

	cases := []struct {
		name           string
		interceptors   interceptor.Funcs
		makePlan       func(ns string) *plan
		expectedReason string
	}{
		{
			name: "DeploymentCreateFailed",
			interceptors: interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					return errors.New("simulated create failure")
				},
			},
			makePlan: func(ns string) *plan {
				return &plan{CreateDeployment: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "new-deploy", Namespace: ns},
				}}
			},
			expectedReason: ReasonDeploymentCreateFailed,
		},
		{
			name: "DeploymentDeleteFailed",
			interceptors: interceptor.Funcs{
				Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.DeleteOption) error {
					return errors.New("simulated delete failure")
				},
			},
			makePlan: func(ns string) *plan {
				return &plan{DeleteDeployments: []*appsv1.Deployment{
					{ObjectMeta: metav1.ObjectMeta{Name: "old-deploy", Namespace: ns}},
				}}
			},
			expectedReason: ReasonDeploymentDeleteFailed,
		},
		{
			name: "DeploymentUpdateFailed",
			interceptors: interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					return errors.New("simulated update failure")
				},
			},
			makePlan: func(ns string) *plan {
				return &plan{UpdateDeployments: []*appsv1.Deployment{
					{ObjectMeta: metav1.ObjectMeta{Name: "old-deploy", Namespace: ns}},
				}}
			},
			expectedReason: ReasonDeploymentUpdateFailed,
		},
		{
			name: "DeploymentScaleFailed",
			interceptors: interceptor.Funcs{
				SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
					return errors.New("simulated scale failure")
				},
			},
			makePlan: func(ns string) *plan {
				ref := &corev1.ObjectReference{Namespace: ns, Name: "some-deploy"}
				return &plan{ScaleDeployments: map[*corev1.ObjectReference]uint32{ref: 0}}
			},
			expectedReason: ReasonDeploymentScaleFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd}, tc.interceptors)
			_, err := r.executeK8sOperations(context.Background(), logr.Discard(), twd, tc.makePlan(twd.Namespace))
			require.Error(t, err)
			assertEventEmitted(t, drainEvents(recorder), tc.expectedReason)
		})
	}
}

func TestBuildIDForDeployment(t *testing.T) {
	targetRef := &corev1.ObjectReference{Namespace: "default", Name: "target"}
	currentRef := &corev1.ObjectReference{Namespace: "default", Name: "current"}
	deprecatedRef := &corev1.ObjectReference{Namespace: "default", Name: "deprecated"}
	twd := makeWD("test-worker", "default", "my-conn")
	twd.Status.TargetVersion.BuildID, twd.Status.TargetVersion.Deployment = "target-build", targetRef
	twd.Status.CurrentVersion = &temporaliov1alpha1.CurrentWorkerDeploymentVersion{BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: "current-build", Deployment: currentRef}}
	twd.Status.DeprecatedVersions = []*temporaliov1alpha1.DeprecatedWorkerDeploymentVersion{{BaseWorkerDeploymentVersion: temporaliov1alpha1.BaseWorkerDeploymentVersion{BuildID: "deprecated-build", Deployment: deprecatedRef}}}

	tests := map[string]struct {
		deployment *corev1.ObjectReference
		buildID    string
	}{
		"target":     {targetRef, "target-build"},
		"current":    {currentRef, "current-build"},
		"deprecated": {deprecatedRef, "deprecated-build"},
		"unknown":    {&corev1.ObjectReference{Namespace: "default", Name: "unknown"}, "unknown"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.buildID, buildIDForDeployment(twd, tt.deployment))
		})
	}
}

// ─── startTestWorkflows tests ────────────────────────────────────────────────

func TestStartTestWorkflows_StartFailed_EmitsEvent(t *testing.T) {
	namespace := "default"
	twd := makeWD("test-worker", namespace, "my-conn")
	r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd}, interceptor.Funcs{})

	p := &plan{
		WorkerDeploymentName: twd.Name,
		startTestWorkflows: []startWorkflowConfig{
			{
				workflowType: "MyGateWorkflow",
				workflowID:   "my-gate-wf-id",
				buildID:      "build-abc",
				taskQueue:    "my-task-queue",
			},
		},
	}

	err := r.startTestWorkflows(context.Background(), logr.Discard(), twd,
		newStubTemporalClient(errors.New("simulated ExecuteWorkflow failure")), p)
	require.Error(t, err)
	assertEventEmitted(t, drainEvents(recorder), ReasonTestWorkflowStartFailed)
}

// ─── updateVersionConfig tests ───────────────────────────────────────────────

func TestUpdateVersionConfig_EmitsEventOnFailure(t *testing.T) {
	cases := []struct {
		name           string
		handle         *stubWDHandle
		config         *planner.VersionConfig
		expectedReason string
	}{
		{
			name:           "SetCurrentFailed",
			handle:         &stubWDHandle{setCurrentErr: errors.New("simulated SetCurrentVersion failure")},
			config:         &planner.VersionConfig{BuildID: "build-abc", SetCurrent: true, ManagerIdentity: "some-manager"},
			expectedReason: ReasonVersionPromotionFailed,
		},
		{
			name:           "SetRampingFailed",
			handle:         &stubWDHandle{setRampingErr: errors.New("simulated SetRampingVersion failure")},
			config:         &planner.VersionConfig{BuildID: "build-abc", RampPercentage: 25, ManagerIdentity: "some-manager"},
			expectedReason: ReasonVersionPromotionFailed,
		},
		{
			// SetCurrentVersion succeeds; UpdateVersionMetadata fails.
			name:           "MetadataUpdateFailed",
			handle:         &stubWDHandle{updateMetaErr: errors.New("simulated UpdateVersionMetadata failure")},
			config:         &planner.VersionConfig{BuildID: "build-abc", SetCurrent: true, ManagerIdentity: "some-manager"},
			expectedReason: ReasonMetadataUpdateFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			namespace := "default"
			twd := makeWD("test-worker", namespace, "my-conn")
			r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd}, interceptor.Funcs{})

			p := &plan{WorkerDeploymentName: twd.Name, UpdateVersionConfig: tc.config}
			err := r.updateVersionConfig(context.Background(), logr.Discard(), twd, tc.handle, p)
			require.Error(t, err)
			assertEventEmitted(t, drainEvents(recorder), tc.expectedReason)
		})
	}
}

// TestHandleDeletion_EvictsCachedClientOnTemporalFailure verifies that
// transport-level failures inside handleDeletion evict the cached SDK client.
// NotFound is treated as "already cleaned up" and must not evict.
func TestHandleDeletion_EvictsCachedClientOnTemporalFailure(t *testing.T) {
	t.Run("DescribeError_EvictsClient", func(t *testing.T) {
		k8sNamespace := "default"
		hostPort := "localhost:7233"

		conn := makeNoCredsConnection("my-conn", k8sNamespace, hostPort)
		twd := makeWD("del-worker", k8sNamespace, conn.Name)

		r, _ := newTestReconciler([]client.Object{twd, conn})

		poolKey := noCredsPoolKey(conn.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace)
		poisoned := &stubTemporalClient{
			wdClient: &stubWDClient{handle: &stubWDHandle{
				describeErr: context.DeadlineExceeded,
			}},
		}
		r.TemporalClientPool.SetClientForTesting(poolKey, poisoned)

		// Sanity: the poisoned client is what handleDeletion will pick up.
		cached, ok := r.TemporalClientPool.GetSDKClient(poolKey)
		require.True(t, ok, "poisoned client should be cached before handleDeletion runs")
		require.Same(t, poisoned, cached)

		err := r.handleDeletion(context.Background(), logr.Discard(), twd)
		require.Error(t, err, "handleDeletion must surface the Temporal Describe error")
		require.ErrorIs(t, err, context.DeadlineExceeded, "the original error must propagate so the reconciler requeues")

		_, ok = r.TemporalClientPool.GetSDKClient(poolKey)
		require.False(t, ok, "poisoned client must be evicted from the pool after a Temporal-server-side failure so the next reconcile dials a fresh one")
	})

	t.Run("DescribeNotFound_RetainsClient", func(t *testing.T) {
		// NotFound on Describe is treated as success (nothing to clean up).
		// The cached client is healthy and must not be evicted.
		k8sNamespace := "default"
		hostPort := "localhost:7233"

		conn := makeNoCredsConnection("my-conn", k8sNamespace, hostPort)
		twd := makeWD("del-worker", k8sNamespace, conn.Name)

		r, _ := newTestReconciler([]client.Object{twd, conn})

		poolKey := noCredsPoolKey(conn.Spec.HostPort, twd.Spec.WorkerOptions.TemporalNamespace)
		healthy := newStubTemporalClient(nil) // describeErr=&serviceerror.NotFound{}
		r.TemporalClientPool.SetClientForTesting(poolKey, healthy)

		err := r.handleDeletion(context.Background(), logr.Discard(), twd)
		require.NoError(t, err, "Describe returning NotFound must be treated as success")

		cached, ok := r.TemporalClientPool.GetSDKClient(poolKey)
		require.True(t, ok, "a healthy cached client must remain in the pool after a successful handleDeletion")
		require.Same(t, healthy, cached)
	})
}
