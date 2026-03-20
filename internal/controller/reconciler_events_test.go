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

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"github.com/temporalio/temporal-worker-controller/internal/planner"
	deploymentpb "go.temporal.io/api/deployment/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	sdkclient "go.temporal.io/sdk/client"
	"google.golang.org/grpc"
	appsv1 "k8s.io/api/apps/v1"
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
	return s
}

// newTestReconciler creates a TemporalWorkerDeploymentReconciler with a fake client and recorder.
func newTestReconciler(objs []client.Object) (*TemporalWorkerDeploymentReconciler, *record.FakeRecorder) {
	return newTestReconcilerWithInterceptors(objs, interceptor.Funcs{})
}

// newTestReconcilerWithInterceptors creates a reconciler with a fake client that uses custom interceptors.
func newTestReconcilerWithInterceptors(objs []client.Object, funcs interceptor.Funcs) (*TemporalWorkerDeploymentReconciler, *record.FakeRecorder) {
	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&temporaliov1alpha1.TemporalWorkerDeployment{}).
		WithIndex(&appsv1.Deployment{}, deployOwnerKey, func(rawObj client.Object) []string {
			deploy := rawObj.(*appsv1.Deployment)
			owner := metav1.GetControllerOf(deploy)
			if owner == nil {
				return nil
			}
			if owner.APIVersion != temporaliov1alpha1.GroupVersion.String() || owner.Kind != "TemporalWorkerDeployment" {
				return nil
			}
			return []string{owner.Name}
		}).
		WithInterceptorFuncs(funcs).
		Build()

	recorder := record.NewFakeRecorder(10)

	r := &TemporalWorkerDeploymentReconciler{
		Client:              fakeClient,
		Scheme:              scheme,
		TemporalClientPool:  clientpool.New(nil, fakeClient),
		Recorder:            recorder,
		DisableRecoverPanic: true,
		MaxDeploymentVersionsIneligibleForDeletion: 75,
	}

	return r, recorder
}

// makeTWD creates a minimal TemporalWorkerDeployment for testing.
func makeTWD(name, namespace, connectionName string) *temporaliov1alpha1.TemporalWorkerDeployment {
	replicas := int32(1)
	progressDeadline := int32(600)
	return &temporaliov1alpha1.TemporalWorkerDeployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "TemporalWorkerDeployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: temporaliov1alpha1.TemporalWorkerDeploymentSpec{
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
				TemporalConnectionRef: temporaliov1alpha1.TemporalConnectionReference{
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

// makeNoCredsTemporalConnection creates a minimal TemporalConnection for testing.
func makeNoCredsTemporalConnection(name, namespace, hostPort string) *temporaliov1alpha1.TemporalConnection {
	return &temporaliov1alpha1.TemporalConnection{
		TypeMeta: metav1.TypeMeta{
			APIVersion: temporaliov1alpha1.GroupVersion.String(),
			Kind:       "TemporalConnection",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: temporaliov1alpha1.TemporalConnectionSpec{
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

// ─── Stub types ──────────────────────────────────────────────────────────────

// stubWDHandle implements sdkclient.WorkerDeploymentHandle with configurable per-method errors.
type stubWDHandle struct {
	sdkclient.WorkerDeploymentHandle
	describeErr   error
	setCurrentErr error
	setRampingErr error
	updateMetaErr error
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

// newStubTemporalClient returns a stub client whose WorkflowService().DescribeWorkerDeployment
// returns a valid empty response, and whose ExecuteWorkflow returns execErr.
func newStubTemporalClient(execErr error) *stubTemporalClient {
	handle := &stubWDHandle{describeErr: &serviceerror.NotFound{}}
	return &stubTemporalClient{
		wdClient: &stubWDClient{handle: handle},
		execErr:  execErr,
	}
}

// noCredsPoolKey returns the ClientPoolKey for a no-credentials TemporalConnection.
func noCredsPoolKey(hostPort, temporalNamespace string) clientpool.ClientPoolKey {
	return clientpool.ClientPoolKey{
		HostPort:   hostPort,
		Namespace:  temporalNamespace,
		SecretName: "",
		AuthMode:   clientpool.AuthModeNoCredentials,
	}
}

// ─── setCondition tests ───────────────────────────────────────────────────────

func TestSetCondition(t *testing.T) {
	r, _ := newTestReconciler(nil)

	t.Run("SetsNewCondition", func(t *testing.T) {
		twd := makeTWD("test-worker", "default", "my-connection")
		r.setCondition(twd, temporaliov1alpha1.ConditionRolloutComplete, metav1.ConditionTrue, "TestReason", "Test message")

		require.Len(t, twd.Status.Conditions, 1)
		assert.Equal(t, temporaliov1alpha1.ConditionRolloutComplete, twd.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, twd.Status.Conditions[0].Status)
		assert.Equal(t, "TestReason", twd.Status.Conditions[0].Reason)
		assert.Equal(t, "Test message", twd.Status.Conditions[0].Message)
		assert.Equal(t, int64(1), twd.Status.Conditions[0].ObservedGeneration)
	})

	t.Run("UpdatesExistingCondition", func(t *testing.T) {
		twd := makeTWD("test-worker", "default", "my-connection")
		r.setCondition(twd, temporaliov1alpha1.ConditionRolloutComplete, metav1.ConditionTrue, "InitialReason", "Initial message")
		require.Len(t, twd.Status.Conditions, 1)

		r.setCondition(twd, temporaliov1alpha1.ConditionRolloutComplete, metav1.ConditionFalse, "UpdatedReason", "Updated message")

		require.Len(t, twd.Status.Conditions, 1, "update should not add a duplicate")
		assert.Equal(t, metav1.ConditionFalse, twd.Status.Conditions[0].Status)
		assert.Equal(t, "UpdatedReason", twd.Status.Conditions[0].Reason)
		assert.Equal(t, "Updated message", twd.Status.Conditions[0].Message)
	})

	t.Run("MultipleDifferentConditions", func(t *testing.T) {
		twd := makeTWD("test-worker", "default", "my-connection")
		r.setCondition(twd, temporaliov1alpha1.ConditionTemporalConnectionHealthy, metav1.ConditionTrue, "Healthy", "Connection is healthy")
		r.setCondition(twd, temporaliov1alpha1.ConditionRolloutComplete, metav1.ConditionTrue, "Ready", "All good")

		require.Len(t, twd.Status.Conditions, 2)

		connCond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionTemporalConnectionHealthy)
		require.NotNil(t, connCond)
		assert.Equal(t, metav1.ConditionTrue, connCond.Status)

		readyCond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionRolloutComplete)
		require.NotNil(t, readyCond)
		assert.Equal(t, metav1.ConditionTrue, readyCond.Status)
	})
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

func TestReconcile_ValidationFailure_NoEventEmitted(t *testing.T) {
	// Progressive strategy with no steps is invalid; the reconciler requeues without emitting events.
	twd := makeTWD("test-worker", "default", "my-connection")
	twd.Spec.RolloutStrategy = temporaliov1alpha1.RolloutStrategy{
		Strategy: temporaliov1alpha1.UpdateProgressive,
		Steps:    nil,
	}
	tc := makeNoCredsTemporalConnection("my-connection", "default", "localhost:7233")
	r, recorder := newTestReconciler([]client.Object{twd, tc})

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})

	require.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter, "should requeue on validation failure")
	events := drainEvents(recorder)
	assertNoEventEmitted(t, events, temporaliov1alpha1.ReasonTemporalConnectionNotFound)
	assertNoEventEmitted(t, events, temporaliov1alpha1.ReasonTemporalClientCreationFailed)
}

// TestReconcile_TemporalConnectionNotFound covers all three related assertions: event emission,
// event message content, and condition update.
func TestReconcile_TemporalConnectionNotFound(t *testing.T) {
	connName := "nonexistent-connection"
	twd := makeTWD("test-worker", "default", connName)
	r, recorder := newTestReconciler([]client.Object{twd})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
	})

	require.Error(t, err)

	events := drainEvents(recorder)
	assertEventEmitted(t, events, temporaliov1alpha1.ReasonTemporalConnectionNotFound)
	for _, event := range events {
		if strings.Contains(event, temporaliov1alpha1.ReasonTemporalConnectionNotFound) {
			assert.Contains(t, event, connName, "event message should include the missing connection name")
			assert.Contains(t, event, "Warning")
		}
	}

	var updated temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updated))
	cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionTemporalConnectionHealthy)
	require.NotNil(t, cond, "TemporalConnectionHealthy condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, temporaliov1alpha1.ReasonTemporalConnectionNotFound, cond.Reason)
	assert.Contains(t, cond.Message, connName)
}

// TestReconcile_TemporalConnectionUnhealthy verifies that credential configuration
// errors (regardless of auth type) emit ReasonAuthSecretInvalid and set the
// TemporalConnectionHealthy condition to False.
//
// ReasonAuthSecretInvalid fires for two distinct failure modes:
//   - resolveAuthSecretName: the secret ref exists but has an empty name (spec validation gap)
//   - ParseClientSecret:     the named k8s Secret cannot be fetched (not found, wrong type, etc.)
//
// ReasonTemporalClientCreationFailed fires only when DialAndUpsertClient fails (network/Temporal
// error). That path requires a live server and is covered by the integration test
// conditions-client-creation-failed.
func TestReconcile_TemporalConnectionUnhealthy(t *testing.T) {
	cases := []struct {
		name           string
		setupConn      func(*temporaliov1alpha1.TemporalConnection)
		expectedReason string
	}{
		{
			// Secret name is non-empty but the k8s Secret doesn't exist; ParseClientSecret
			// returns a not-found error, which is reported as AuthSecretInvalid (not ClientCreationFailed).
			name: "MissingTLSSecret_AuthSecretInvalid",
			setupConn: func(tc *temporaliov1alpha1.TemporalConnection) {
				tc.Spec.MutualTLSSecretRef = &temporaliov1alpha1.SecretReference{Name: "missing-tls-secret"}
			},
			expectedReason: temporaliov1alpha1.ReasonAuthSecretInvalid,
		},
		{
			// Same as above for API key auth.
			name: "MissingAPIKeySecret_AuthSecretInvalid",
			setupConn: func(tc *temporaliov1alpha1.TemporalConnection) {
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
			setupConn: func(tc *temporaliov1alpha1.TemporalConnection) {
				tc.Spec.MutualTLSSecretRef = &temporaliov1alpha1.SecretReference{Name: ""}
			},
			expectedReason: temporaliov1alpha1.ReasonAuthSecretInvalid,
		},
		{
			// Same as above for API key auth.
			name: "MalformedAPIKeySecret_AuthSecretInvalid",
			setupConn: func(tc *temporaliov1alpha1.TemporalConnection) {
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
			conn := makeNoCredsTemporalConnection("my-connection", "default", "localhost:7233")
			tc.setupConn(conn)
			twd := makeTWD("test-worker", conn.Namespace, conn.Name)
			r, recorder := newTestReconciler([]client.Object{twd, conn})

			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace},
			})

			require.Error(t, err)
			assertEventEmitted(t, drainEvents(recorder), tc.expectedReason)

			var updated temporaliov1alpha1.TemporalWorkerDeployment
			require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: twd.Name, Namespace: twd.Namespace}, &updated))
			cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionTemporalConnectionHealthy)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tc.expectedReason, cond.Reason)
		})
	}
}

// TestReconcile_PlanGenerationFailed_EmitsEvent injects a List failure on the second call.
// The first List (in worker_controller.go) succeeds; the second (inside generatePlan) fails,
// which causes Reconcile to emit ReasonPlanGenerationFailed.
func TestReconcile_PlanGenerationFailed_EmitsEvent(t *testing.T) {
	k8sNamespace := "default"
	hostPort := "localhost:7233"

	tc := makeNoCredsTemporalConnection("my-conn", k8sNamespace, hostPort)
	twd := makeTWD("test-worker", k8sNamespace, tc.Name)

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
}

// TestReconcile_PlanExecutionFailed_EmitsEvent injects a Create failure so that
// executeK8sOperations fails for the new Deployment that a fresh TWD always needs,
// causing Reconcile to emit ReasonPlanExecutionFailed.
func TestReconcile_PlanExecutionFailed_EmitsEvent(t *testing.T) {
	k8sNamespace := "default"
	hostPort := "localhost:7233"

	tc := makeNoCredsTemporalConnection("my-conn", k8sNamespace, hostPort)
	twd := makeTWD("test-worker", k8sNamespace, tc.Name)

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
}

// TestReconcile_DescribeWorkerDeploymentNotFound verifies that when the gRPC
// DescribeWorkerDeployment call returns NotFound (no deployment exists in Temporal yet),
// reconciliation succeeds and proceeds to plan generation (creating a new k8s Deployment).
func TestReconcile_DescribeWorkerDeploymentNotFound(t *testing.T) {
	k8sNamespace := "default"
	hostPort := "localhost:7233"

	tc := makeNoCredsTemporalConnection("my-conn", k8sNamespace, hostPort)
	twd := makeTWD("test-worker", k8sNamespace, tc.Name)

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

// ─── executeK8sOperations tests ──────────────────────────────────────────────

func TestExecuteK8sOperations_EmitsEventOnFailure(t *testing.T) {
	namespace := "default"
	twd := makeTWD("test-worker", namespace, "my-conn")

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
			err := r.executeK8sOperations(context.Background(), logr.Discard(), twd, tc.makePlan(twd.Namespace))
			require.Error(t, err)
			assertEventEmitted(t, drainEvents(recorder), tc.expectedReason)
		})
	}
}

// ─── startTestWorkflows tests ────────────────────────────────────────────────

func TestStartTestWorkflows_StartFailed_EmitsEvent(t *testing.T) {
	namespace := "default"
	twd := makeTWD("test-worker", namespace, "my-conn")
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
			twd := makeTWD("test-worker", namespace, "my-conn")
			r, recorder := newTestReconcilerWithInterceptors([]client.Object{twd}, interceptor.Funcs{})

			p := &plan{WorkerDeploymentName: twd.Name, UpdateVersionConfig: tc.config}
			err := r.updateVersionConfig(context.Background(), logr.Discard(), twd, tc.handle, p)
			require.Error(t, err)
			assertEventEmitted(t, drainEvents(recorder), tc.expectedReason)
		})
	}
}
