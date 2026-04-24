// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

// Integration tests for CRD-level CEL validation rules on TemporalWorkerDeployment.
//
// These tests hit a real kube-apiserver (via envtest) so they verify that the
// x-kubernetes-validations blocks in the generated CRD manifest are syntactically
// valid and semantically correct. The webhook Go code is NOT involved here — we are
// testing what the API server enforces regardless of whether the webhook is enabled.

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("TemporalWorkerDeployment CRD CEL validation", func() {
	var ns string

	BeforeEach(func() {
		ns = makeTestNamespace("twd-cel")
	})

	// baseTWD returns a minimal valid TWD in the given namespace.
	baseTWD := func(name string) *TemporalWorkerDeployment {
		return &TemporalWorkerDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: TemporalWorkerDeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker", Image: "worker:latest"}},
					},
				},
				RolloutStrategy: RolloutStrategy{Strategy: UpdateAllAtOnce},
				WorkerOptions: WorkerOptions{
					TemporalConnectionRef: TemporalConnectionReference{Name: "my-connection"},
					TemporalNamespace:     "default",
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
})
