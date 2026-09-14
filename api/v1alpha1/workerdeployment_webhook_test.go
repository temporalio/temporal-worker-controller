// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/testhelpers"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// wdWithGate returns a WorkerDeployment whose gate declares the given payload encoding
// and protobuf message type. Empty values leave the corresponding field unset. The gate
// has no input source unless an option adds one.
func wdWithGate(
	name string,
	encoding temporaliov1alpha1.PayloadMetadataEncodingType,
	messageType string,
	opts ...func(*temporaliov1alpha1.GateWorkflowConfig),
) *temporaliov1alpha1.WorkerDeployment {
	return testhelpers.ModifyObj(testhelpers.MakeWDWithName(name, ""),
		func(obj *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerDeployment {
			gate := &temporaliov1alpha1.GateWorkflowConfig{
				WorkflowType: "my-gate",
				Encoding:     encoding,
				MessageType:  messageType,
			}
			for _, opt := range opts {
				opt(gate)
			}
			obj.Spec.RolloutStrategy.Gate = gate
			return obj
		})
}

// withSecretInput points the gate at a Secret key: a byte-valued source, and therefore
// one the binary encodings accept.
func withSecretInput(g *temporaliov1alpha1.GateWorkflowConfig) {
	g.InputFrom = &temporaliov1alpha1.GateInputSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "gate-input"},
			Key:                  "request.bin",
		},
	}
}

// withInlineInput sets inline JSON input, which cannot carry raw bytes.
func withInlineInput(g *temporaliov1alpha1.GateWorkflowConfig) {
	g.Input = &apiextensionsv1.JSON{Raw: []byte(`{"service":"checkout"}`)}
}

func TestWorkerDeployment_ValidateCreate(t *testing.T) {
	tests := map[string]struct {
		obj      runtime.Object
		errorMsg string
	}{
		// The webhook duplicates the CRD's CEL rules so the constraints still report when
		// the optional webhook is deployed but the CRD is older, and vice versa. The enum
		// itself is schema-only and is therefore not checked here.
		"gate with neither encoding nor messageType": {
			obj: wdWithGate("gate-no-encoding", "", ""),
		},
		"gate with json/protobuf and no messageType": {
			obj: wdWithGate("gate-protojson", temporaliov1alpha1.PayloadMetadataEncodingTypeProtoJSON, ""),
		},
		"gate with json/protobuf and messageType": {
			obj: wdWithGate("gate-protojson-typed", temporaliov1alpha1.PayloadMetadataEncodingTypeProtoJSON, "my.package.DeployRequest"),
		},
		"gate with binary/protobuf, messageType and a Secret input": {
			obj: wdWithGate("gate-proto-typed", temporaliov1alpha1.PayloadMetadataEncodingTypeProto, "my.package.DeployRequest", withSecretInput),
		},
		"gate with binary/protobuf and no messageType": {
			obj:      wdWithGate("gate-proto-untyped", temporaliov1alpha1.PayloadMetadataEncodingTypeProto, "", withSecretInput),
			errorMsg: "messageType is required when encoding is binary/protobuf",
		},

		// The binary encodings need a byte-valued source. These cases are webhook-only:
		// gate.input is invisible to CEL, so the CRD cannot carry an equivalent rule.
		"gate with binary/plain and a Secret input": {
			obj: wdWithGate("gate-binary-secret", temporaliov1alpha1.PayloadMetadataEncodingTypeBinary, "", withSecretInput),
		},
		"gate with binary/plain and inline input": {
			obj:      wdWithGate("gate-binary-inline", temporaliov1alpha1.PayloadMetadataEncodingTypeBinary, "", withInlineInput),
			errorMsg: "cannot be used with inline input",
		},
		"gate with binary/protobuf and inline input": {
			obj: wdWithGate("gate-proto-inline", temporaliov1alpha1.PayloadMetadataEncodingTypeProto,
				"my.package.DeployRequest", withInlineInput),
			errorMsg: "cannot be used with inline input",
		},
		"gate with binary/plain and no input at all": {
			obj:      wdWithGate("gate-binary-no-input", temporaliov1alpha1.PayloadMetadataEncodingTypeBinary, ""),
			errorMsg: "require inputFrom",
		},
		"gate with binary/protobuf and no input at all": {
			obj:      wdWithGate("gate-proto-no-input", temporaliov1alpha1.PayloadMetadataEncodingTypeProto, "my.package.DeployRequest"),
			errorMsg: "require inputFrom",
		},

		// The text encodings are unaffected. Inline input stays valid for them, which is
		// the documented way to pass a protobuf message as JSON.
		"gate with json/protobuf and inline input": {
			obj: wdWithGate("gate-protojson-inline", temporaliov1alpha1.PayloadMetadataEncodingTypeProtoJSON,
				"my.package.DeployRequest", withInlineInput),
		},
		"gate with no encoding and inline input": {
			obj: wdWithGate("gate-plain-inline", "", "", withInlineInput),
		},
		"gate with messageType on a non-protobuf encoding": {
			obj:      wdWithGate("gate-json-typed", temporaliov1alpha1.PayloadMetadataEncodingTypeJSON, "my.package.DeployRequest"),
			errorMsg: "messageType may only be set when encoding is json/protobuf or binary/protobuf",
		},
		"gate with messageType and no encoding": {
			obj:      wdWithGate("gate-typed-no-encoding", "", "my.package.DeployRequest"),
			errorMsg: "messageType may only be set when encoding is json/protobuf or binary/protobuf",
		},
		"valid temporal worker deployment": {
			obj: testhelpers.MakeWDWithName("valid-worker", ""),
		},
		"invalid object type": {
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			errorMsg: "expected a WorkerDeployment",
		},
		"ramp value for step <= previous step": {
			obj: testhelpers.ModifyObj(testhelpers.MakeWDWithName("prog-rollout-decreasing-ramps", ""), func(obj *temporaliov1alpha1.WorkerDeployment) *temporaliov1alpha1.WorkerDeployment {
				obj.Spec.RolloutStrategy.Strategy = temporaliov1alpha1.UpdateProgressive
				obj.Spec.RolloutStrategy.Steps = []temporaliov1alpha1.RolloutStep{
					{5, metav1.Duration{Duration: time.Minute}},
					{10, metav1.Duration{Duration: time.Minute}},
					{9, metav1.Duration{Duration: time.Minute}},
					{50, metav1.Duration{Duration: time.Minute}},
					{50, metav1.Duration{Duration: time.Minute}},
					{75, metav1.Duration{Duration: time.Minute}},
				}
				return obj
			}),
			errorMsg: "[spec.rollout.steps[2].rampPercentage: Invalid value: 9: rampPercentage must increase between each step, spec.rollout.steps[4].rampPercentage: Invalid value: 50: rampPercentage must increase between each step]",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.WorkerDeployment{}

			assertAdmission := func(warnings admission.Warnings, err error) {
				if tc.errorMsg != "" {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tc.errorMsg)
				} else {
					require.NoError(t, err)
				}

				// Warnings should always be nil for this implementation
				assert.Nil(t, warnings)
			}

			// Verify that create and update enforce the same rules
			assertAdmission(webhook.ValidateCreate(ctx, tc.obj))
			assertAdmission(webhook.ValidateUpdate(ctx, nil, tc.obj))
		})
	}
}

func TestWorkerDeployment_ValidateUpdate(t *testing.T) {
	tests := map[string]struct {
		oldObj   runtime.Object
		newObj   runtime.Object
		errorMsg string
	}{
		"valid update": {
			oldObj: nil,
			newObj: testhelpers.MakeWDWithName("valid-worker", ""),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.WorkerDeployment{}

			warnings, err := webhook.ValidateUpdate(ctx, tc.oldObj, tc.newObj)

			if tc.errorMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorMsg)
			} else {
				require.NoError(t, err)
			}

			// Warnings should always be nil for this implementation
			assert.Nil(t, warnings)
		})
	}
}

func TestWorkerDeployment_ValidateDelete(t *testing.T) {
	ctx := context.Background()
	webhook := &temporaliov1alpha1.WorkerDeployment{}

	obj := &temporaliov1alpha1.WorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker",
		},
	}

	warnings, err := webhook.ValidateDelete(ctx, obj)

	// ValidateDelete should always return nil, nil
	assert.NoError(t, err)
	assert.Nil(t, warnings)
}

func TestWorkerDeployment_Default(t *testing.T) {
	tests := map[string]struct {
		obj      runtime.Object
		expected func(t *testing.T, obj *temporaliov1alpha1.WorkerDeployment)
	}{
		"sets default sunset strategy delays": {
			obj: testhelpers.MakeWDWithName("default-sunset-delays", ""),
			expected: func(t *testing.T, obj *temporaliov1alpha1.WorkerDeployment) {
				require.NotNil(t, obj.Spec.SunsetStrategy.ScaledownDelay)
				assert.Equal(t, time.Hour, obj.Spec.SunsetStrategy.ScaledownDelay.Duration)
				require.NotNil(t, obj.Spec.SunsetStrategy.DeleteDelay)
				assert.Equal(t, 24*time.Hour, obj.Spec.SunsetStrategy.DeleteDelay.Duration)
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			webhook := &temporaliov1alpha1.WorkerDeployment{}

			err := webhook.Default(ctx, tc.obj)
			require.NoError(t, err)

			obj, ok := tc.obj.(*temporaliov1alpha1.WorkerDeployment)
			require.True(t, ok)

			tc.expected(t, obj)
		})
	}
}
