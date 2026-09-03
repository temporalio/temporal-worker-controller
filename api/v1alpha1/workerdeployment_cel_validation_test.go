package v1alpha1

// Integration tests for CRD-level CEL validation rules on WorkerDeployment.
//
// These tests hit a real kube-apiserver (via envtest) so they verify that the
// x-kubernetes-validations blocks in the generated CRD manifest are syntactically
// valid and semantically correct. The webhook Go code is NOT involved here — we are
// testing what the API server enforces regardless of whether the webhook is enabled.

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr[T any](v T) *T { return &v }

var _ = Describe("WorkerDeployment CRD CEL validation", func() {
	var ns string

	BeforeEach(func() {
		ns = makeTestNamespace("twd-cel")
	})

	// baseTWD returns a minimal valid TWD in the given namespace.
	baseTWD := func(name string) *WorkerDeployment {
		return &WorkerDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: WorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker", Image: "worker:latest"}},
					},
				},
				RolloutStrategy: RolloutStrategy{Strategy: UpdateAllAtOnce},
				WorkerOptions: WorkerOptions{
					ConnectionRef:     ConnectionReference{Name: "my-connection"},
					TemporalNamespace: "default",
				},
			},
		}
	}

	It("accepts a valid TWD", func() {
		Expect(k8sClient.Create(ctx, baseTWD("valid-worker"))).To(Succeed())
	})

	It("rejects name longer than 63 characters", func() {
		twd := baseTWD(strings.Repeat("a", 64))
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("name cannot be more than 63 characters"))
	})

	It("rejects Progressive strategy with no steps", func() {
		twd := baseTWD("prog-no-steps")
		twd.Spec.RolloutStrategy = RolloutStrategy{Strategy: UpdateProgressive}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("steps are required for Progressive rollout"))
	})

	It("rejects more than 20 Progressive steps", func() {
		steps := make([]RolloutStep, 21)
		for i := range steps {
			steps[i] = RolloutStep{
				RampPercentage: i + 1,
				PauseDuration:  metav1.Duration{Duration: time.Minute},
			}
		}
		twd := baseTWD("prog-too-many-steps")
		twd.Spec.RolloutStrategy = RolloutStrategy{Strategy: UpdateProgressive, Steps: steps}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Too many"))
	})

	It("rejects a Progressive step with pauseDuration less than 30s", func() {
		twd := baseTWD("short-pause")
		twd.Spec.RolloutStrategy = RolloutStrategy{
			Strategy: UpdateProgressive,
			Steps: []RolloutStep{
				{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: 10 * time.Second}},
			},
		}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("pause duration must be at least 30s"))
	})

	It("rejects gate.inputFrom with both configMapKeyRef and secretKeyRef set", func() {
		twd := baseTWD("bad-gate-inputfrom")
		twd.Spec.RolloutStrategy = RolloutStrategy{
			Strategy: UpdateAllAtOnce,
			Gate: &GateWorkflowConfig{
				WorkflowType: "my-gate",
				InputFrom: &GateInputSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "my-cm"},
						Key:                  "key",
					},
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "my-secret"},
						Key:                  "key",
					},
				},
			},
		}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of configMapKeyRef or secretKeyRef must be set"))
	})

	// gateTWD returns a TWD whose gate declares the given payload encoding and message
	// type. Empty values are dropped by omitempty, so passing "" leaves the field unset
	// as far as the API server (and therefore the CEL has() guards) is concerned.
	gateTWD := func(name string, encoding PayloadMetadataEncodingType, messageType string, opts ...func(*GateWorkflowConfig)) *WorkerDeployment {
		twd := baseTWD(name)
		gate := &GateWorkflowConfig{
			WorkflowType: "my-gate",
			Encoding:     encoding,
			MessageType:  messageType,
		}
		for _, opt := range opts {
			opt(gate)
		}
		twd.Spec.RolloutStrategy = RolloutStrategy{
			Strategy: UpdateAllAtOnce,
			Gate:     gate,
		}
		return twd
	}

	// withSecretInput satisfies the webhook's requirement that the binary encodings read
	// their bytes from a byte-valued source. The webhook runs against this envtest server
	// too, so a binary encoding without it is rejected before the CEL rule under test is
	// reached.
	withSecretInput := func(g *GateWorkflowConfig) {
		g.InputFrom = &GateInputSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "gate-input"},
				Key:                  "request.bin",
			},
		}
	}

	const messageTypeRequiredErr = "gate.messageType is required when gate.encoding is binary/protobuf"
	const messageTypeNotAllowedErr = "gate.messageType may only be set when gate.encoding is json/protobuf or binary/protobuf"

	// Guards the pre-existing behavior: a gate that predates the encoding field must
	// still be accepted unchanged.
	It("accepts a gate with neither encoding nor messageType", func() {
		Expect(k8sClient.Create(ctx, gateTWD("gate-no-encoding", "", ""))).To(Succeed())
	})

	It("accepts every encoding in the enum", func() {
		// binary/protobuf is excluded here because it additionally requires messageType;
		// it is covered by its own case below. Every gate reads from a Secret, which is a
		// valid source for all of these encodings, so the only thing varying is the enum
		// value itself.
		for i, enc := range []PayloadMetadataEncodingType{
			PayloadMetadataEncodingTypeBinary,
			PayloadMetadataEncodingTypeJSON,
			PayloadMetadataEncodingTypeProtoJSON,
		} {
			Expect(k8sClient.Create(ctx, gateTWD(fmt.Sprintf("gate-encoding-%d", i), enc, "", withSecretInput))).
				To(Succeed(), "encoding %q should be accepted", enc)
		}
	})

	It("rejects an encoding outside the enum", func() {
		err := k8sClient.Create(ctx, gateTWD("gate-bad-encoding", "application/xml", ""))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Unsupported value"))
	})

	It("rejects binary/protobuf without messageType", func() {
		err := k8sClient.Create(ctx, gateTWD("gate-proto-no-type", PayloadMetadataEncodingTypeProto, "", withSecretInput))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(messageTypeRequiredErr))
	})

	It("accepts binary/protobuf with messageType", func() {
		Expect(k8sClient.Create(ctx,
			gateTWD("gate-proto-with-type", PayloadMetadataEncodingTypeProto, "my.package.DeployRequest", withSecretInput),
		)).To(Succeed())
	})

	It("accepts json/protobuf with messageType", func() {
		Expect(k8sClient.Create(ctx,
			gateTWD("gate-protojson-with-type", PayloadMetadataEncodingTypeProtoJSON, "my.package.DeployRequest"),
		)).To(Succeed())
	})

	It("rejects messageType with a non-protobuf encoding", func() {
		err := k8sClient.Create(ctx,
			gateTWD("gate-json-with-type", PayloadMetadataEncodingTypeJSON, "my.package.DeployRequest"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(messageTypeNotAllowedErr))
	})

	// Exercises the has(self.gate.encoding) guard specifically: without it the rule would
	// error on the missing field rather than rejecting the resource with this message.
	It("rejects messageType when no encoding is set", func() {
		err := k8sClient.Create(ctx, gateTWD("gate-type-no-encoding", "", "my.package.DeployRequest"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(messageTypeNotAllowedErr))
	})

	It("accepts connectionRef with objectRef (Connection kind)", func() {
		twd := baseTWD("objref-connection")
		twd.Spec.WorkerOptions.ConnectionRef = ConnectionReference{
			ObjectRef: &corev1.TypedObjectReference{
				APIGroup: ptr("temporal.io"),
				Kind:     "Connection",
				Name:     "my-connection",
			},
		}
		Expect(k8sClient.Create(ctx, twd)).To(Succeed())
	})

	It("accepts connectionRef with objectRef (ClusterConnection kind)", func() {
		twd := baseTWD("objref-cluster")
		twd.Spec.WorkerOptions.ConnectionRef = ConnectionReference{
			ObjectRef: &corev1.TypedObjectReference{
				APIGroup: ptr("temporal.io"),
				Kind:     "ClusterConnection",
				Name:     "shared-connection",
			},
		}
		Expect(k8sClient.Create(ctx, twd)).To(Succeed())
	})

	It("rejects connectionRef with both name and objectRef set", func() {
		twd := baseTWD("both-set")
		twd.Spec.WorkerOptions.ConnectionRef = ConnectionReference{
			Name: "my-connection",
			ObjectRef: &corev1.TypedObjectReference{
				APIGroup: ptr("temporal.io"),
				Kind:     "Connection",
				Name:     "my-connection",
			},
		}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of name or objectRef"))
	})

	It("rejects connectionRef with neither name nor objectRef set", func() {
		twd := baseTWD("neither-set")
		twd.Spec.WorkerOptions.ConnectionRef = ConnectionReference{}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exactly one of name or objectRef"))
	})

	It("rejects objectRef with an invalid kind", func() {
		twd := baseTWD("bad-kind")
		twd.Spec.WorkerOptions.ConnectionRef = ConnectionReference{
			ObjectRef: &corev1.TypedObjectReference{
				APIGroup: ptr("temporal.io"),
				Kind:     "SomethingRandom",
				Name:     "my-connection",
			},
		}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("objectRef.kind must be Connection or ClusterConnection"))
	})

	It("rejects objectRef with a non-temporal.io apiGroup", func() {
		twd := baseTWD("bad-apigroup")
		twd.Spec.WorkerOptions.ConnectionRef = ConnectionReference{
			ObjectRef: &corev1.TypedObjectReference{
				APIGroup: ptr("example.com"),
				Kind:     "Connection",
				Name:     "my-connection",
			},
		}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("objectRef.apiGroup must be temporal.io"))
	})

	It("rejects objectRef with a populated namespace (cross-namespace not supported yet)", func() {
		twd := baseTWD("with-namespace")
		twd.Spec.WorkerOptions.ConnectionRef = ConnectionReference{
			ObjectRef: &corev1.TypedObjectReference{
				APIGroup:  ptr("temporal.io"),
				Kind:      "Connection",
				Name:      "my-connection",
				Namespace: ptr("other-namespace"),
			},
		}
		err := k8sClient.Create(ctx, twd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("objectRef.namespace is not supported"))
	})

})
