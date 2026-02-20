// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
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

// newTestScheme creates a scheme with all required types registered.
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
		Client:             fakeClient,
		Scheme:             scheme,
		TemporalClientPool: clientpool.New(nil, fakeClient),
		Recorder:           recorder,
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
				TemporalNamespace: "default",
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

// makeTemporalConnection creates a minimal TemporalConnection for testing.
func makeTemporalConnection(name, namespace, hostPort string) *temporaliov1alpha1.TemporalConnection {
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

// drainEvents reads all events from the recorder channel and returns them.
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

func TestReconcile_TemporalConnectionNotFound_EmitsEvent(t *testing.T) {
	twd := makeTWD("test-worker", "default", "nonexistent-connection")
	r, recorder := newTestReconciler([]client.Object{twd})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-worker", Namespace: "default"},
	})

	require.Error(t, err)

	events := drainEvents(recorder)
	assertEventEmitted(t, events, "TemporalConnectionNotFound")

	// Check that the event contains the connection name
	for _, event := range events {
		if strings.Contains(event, "TemporalConnectionNotFound") {
			assert.Contains(t, event, "nonexistent-connection")
			assert.Contains(t, event, "Warning")
		}
	}
}

func TestReconcile_TemporalConnectionNotFound_SetsCondition(t *testing.T) {
	twd := makeTWD("test-worker", "default", "nonexistent-connection")
	r, _ := newTestReconciler([]client.Object{twd})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-worker", Namespace: "default"},
	})

	require.Error(t, err)

	// Fetch the updated TWD to check conditions
	var updated temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "test-worker", Namespace: "default"}, &updated))

	cond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionTemporalConnectionValid)
	require.NotNil(t, cond, "TemporalConnectionValid condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "TemporalConnectionNotFound", cond.Reason)
	assert.Contains(t, cond.Message, "nonexistent-connection")
}

func TestReconcile_AuthSecretInvalid_EmitsEvent(t *testing.T) {
	// Create a TemporalConnection with mTLS that references a secret,
	// but the MutualTLSSecretRef has an empty name (will cause resolveAuthSecretName to fail)
	tc := makeTemporalConnection("my-connection", "default", "localhost:7233")
	tc.Spec.MutualTLSSecretRef = &temporaliov1alpha1.SecretReference{Name: ""} // empty name triggers error in getTLSSecretName

	twd := makeTWD("test-worker", "default", "my-connection")
	r, recorder := newTestReconciler([]client.Object{twd, tc})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-worker", Namespace: "default"},
	})

	// The mTLS secret ref has a name ("") which is technically non-empty in Go,
	// so resolveAuthSecretName will return AuthModeTLS with secretName "".
	// The UpsertClient will try to fetch the secret and fail.
	// Either way, an error should occur and an event should be emitted.
	require.Error(t, err)

	events := drainEvents(recorder)
	// Should have either AuthSecretInvalid or TemporalClientCreationFailed
	hasEvent := false
	for _, event := range events {
		if strings.Contains(event, "AuthSecretInvalid") || strings.Contains(event, "TemporalClientCreationFailed") {
			hasEvent = true
			break
		}
	}
	assert.True(t, hasEvent, "expected AuthSecretInvalid or TemporalClientCreationFailed event, got: %v", events)
}

func TestReconcile_TemporalClientCreationFailed_EmitsEventAndCondition(t *testing.T) {
	// TemporalConnection exists but references a TLS secret that doesn't exist in k8s
	tc := makeTemporalConnection("my-connection", "default", "localhost:7233")
	tc.Spec.MutualTLSSecretRef = &temporaliov1alpha1.SecretReference{Name: "missing-tls-secret"}

	twd := makeTWD("test-worker", "default", "my-connection")
	r, recorder := newTestReconciler([]client.Object{twd, tc})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-worker", Namespace: "default"},
	})

	require.Error(t, err)

	events := drainEvents(recorder)
	assertEventEmitted(t, events, "TemporalClientCreationFailed")

	// Check condition
	var updated temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "test-worker", Namespace: "default"}, &updated))

	// TemporalConnectionValid should be True (connection was fetched successfully)
	connCond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionTemporalConnectionValid)
	require.NotNil(t, connCond)
	assert.Equal(t, metav1.ConditionTrue, connCond.Status)

	// TemporalNamespaceAccessible should be False (client creation failed)
	nsCond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionTemporalNamespaceAccessible)
	require.NotNil(t, nsCond)
	assert.Equal(t, metav1.ConditionFalse, nsCond.Status)
	assert.Equal(t, "TemporalClientCreationFailed", nsCond.Reason)
}

func TestReconcile_TWDNotFound_NoEvent(t *testing.T) {
	// No TWD exists — reconciling should return nil error (not found is ignored)
	r, recorder := newTestReconciler(nil)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: "default"},
	})

	require.NoError(t, err)

	events := drainEvents(recorder)
	assert.Empty(t, events, "no events should be emitted when TWD is not found")
}

func TestSetCondition_SetsNewCondition(t *testing.T) {
	twd := makeTWD("test-worker", "default", "my-connection")
	r, _ := newTestReconciler(nil)

	r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, "TestReason", "Test message")

	require.Len(t, twd.Status.Conditions, 1)
	assert.Equal(t, temporaliov1alpha1.ConditionReady, twd.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, twd.Status.Conditions[0].Status)
	assert.Equal(t, "TestReason", twd.Status.Conditions[0].Reason)
	assert.Equal(t, "Test message", twd.Status.Conditions[0].Message)
	assert.Equal(t, int64(1), twd.Status.Conditions[0].ObservedGeneration)
}

func TestSetCondition_UpdatesExistingCondition(t *testing.T) {
	twd := makeTWD("test-worker", "default", "my-connection")
	r, _ := newTestReconciler(nil)

	// Set initial condition
	r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, "InitialReason", "Initial message")
	require.Len(t, twd.Status.Conditions, 1)

	// Update the condition
	r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionFalse, "UpdatedReason", "Updated message")

	// Should still be exactly 1 condition, not 2
	require.Len(t, twd.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionFalse, twd.Status.Conditions[0].Status)
	assert.Equal(t, "UpdatedReason", twd.Status.Conditions[0].Reason)
	assert.Equal(t, "Updated message", twd.Status.Conditions[0].Message)
}

func TestSetCondition_MultipleDifferentConditions(t *testing.T) {
	twd := makeTWD("test-worker", "default", "my-connection")
	r, _ := newTestReconciler(nil)

	r.setCondition(twd, temporaliov1alpha1.ConditionTemporalConnectionValid, metav1.ConditionTrue, "Valid", "Connection is valid")
	r.setCondition(twd, temporaliov1alpha1.ConditionTemporalNamespaceAccessible, metav1.ConditionTrue, "Accessible", "Namespace is accessible")
	r.setCondition(twd, temporaliov1alpha1.ConditionReady, metav1.ConditionTrue, "Ready", "All good")

	require.Len(t, twd.Status.Conditions, 3)

	connCond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionTemporalConnectionValid)
	require.NotNil(t, connCond)
	assert.Equal(t, metav1.ConditionTrue, connCond.Status)

	nsCond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionTemporalNamespaceAccessible)
	require.NotNil(t, nsCond)
	assert.Equal(t, metav1.ConditionTrue, nsCond.Status)

	readyCond := meta.FindStatusCondition(twd.Status.Conditions, temporaliov1alpha1.ConditionReady)
	require.NotNil(t, readyCond)
	assert.Equal(t, metav1.ConditionTrue, readyCond.Status)
}

func TestReconcile_ValidationFailure_NoEventEmitted(t *testing.T) {
	// Use Progressive strategy with no steps to trigger a validation failure
	twd := makeTWD("test-worker", "default", "my-connection")
	twd.Spec.RolloutStrategy = temporaliov1alpha1.RolloutStrategy{
		Strategy: temporaliov1alpha1.UpdateProgressive,
		Steps:    nil, // Progressive requires steps
	}

	// Also need a connection for this test — but validation happens before connection fetch
	tc := makeTemporalConnection("my-connection", "default", "localhost:7233")
	r, recorder := newTestReconciler([]client.Object{twd, tc})

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-worker", Namespace: "default"},
	})

	// Validation failures don't return errors — they requeue after 5 minutes
	require.NoError(t, err)
	assert.True(t, result.Requeue, "should requeue on validation failure")

	// No events should be emitted for validation failures (user just needs to fix their spec)
	events := drainEvents(recorder)
	assertNoEventEmitted(t, events, "TemporalConnectionNotFound")
	assertNoEventEmitted(t, events, "TemporalClientCreationFailed")
}

func TestReconcile_ConnectionValid_ThenClientFails_ConditionsReflectBoth(t *testing.T) {
	// Connection exists and is fetchable, but uses API key auth with a missing secret
	tc := makeTemporalConnection("my-connection", "default", "localhost:7233")
	tc.Spec.APIKeySecretRef = &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "missing-api-key-secret"},
		Key:                  "api-key",
	}

	twd := makeTWD("test-worker", "default", "my-connection")
	r, recorder := newTestReconciler([]client.Object{twd, tc})

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-worker", Namespace: "default"},
	})

	require.Error(t, err)

	events := drainEvents(recorder)
	assertEventEmitted(t, events, "TemporalClientCreationFailed")

	// Verify conditions: connection valid but namespace not accessible
	var updated temporaliov1alpha1.TemporalWorkerDeployment
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Name: "test-worker", Namespace: "default"}, &updated))

	connCond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionTemporalConnectionValid)
	require.NotNil(t, connCond, "TemporalConnectionValid condition should be set")
	assert.Equal(t, metav1.ConditionTrue, connCond.Status, "connection was fetched successfully")

	nsCond := meta.FindStatusCondition(updated.Status.Conditions, temporaliov1alpha1.ConditionTemporalNamespaceAccessible)
	require.NotNil(t, nsCond, "TemporalNamespaceAccessible condition should be set")
	assert.Equal(t, metav1.ConditionFalse, nsCond.Status, "client creation should have failed")
}

func TestReconcile_EventMessageContainsUsefulContext(t *testing.T) {
	twd := makeTWD("my-deployment", "prod", "prod-connection")
	r, recorder := newTestReconciler([]client.Object{twd})

	_, _ = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-deployment", Namespace: "prod"},
	})

	events := drainEvents(recorder)
	require.NotEmpty(t, events)

	// Verify the event message contains the connection name for debugging
	for _, event := range events {
		if strings.Contains(event, "TemporalConnectionNotFound") {
			assert.Contains(t, event, "prod-connection",
				"Event message should include the missing connection name for debugging")
		}
	}
}
