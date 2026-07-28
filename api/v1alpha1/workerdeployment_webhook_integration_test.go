// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

// Integration tests for the WorkerDeployment validating webhook.
//
// These tests run through the real envtest HTTP admission path — the kube-apiserver
// sends actual AdmissionRequests to the webhook server — validating that:
//   - The webhook is correctly registered and called on WorkerDeployment create/update
//   - The rules the CRD schema cannot express are enforced end-to-end:
//       * Progressive rampPercentage must strictly increase between steps
//       * gate.input and gate.inputFrom are mutually exclusive
//   - Valid specs are admitted
//
// The webhook configuration is rendered from the Helm chart with webhook.enabled=true
// (see webhook_suite_test.go), matching a real chart install with the flag on.

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// makeWDForWebhook builds a minimal WorkerDeployment that satisfies the CRD schema
// (required: rollout, sunset, template, workerOptions) so admission reaches the webhook.
func makeWDForWebhook(name, ns string) *WorkerDeployment {
	scaledownDelay := metav1.Duration{Duration: time.Hour}
	deleteDelay := metav1.Duration{Duration: 24 * time.Hour}
	return &WorkerDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: WorkerDeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test-worker"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "worker", Image: "example.com/worker:v1"}},
				},
			},
			RolloutStrategy: RolloutStrategy{Strategy: UpdateAllAtOnce},
			SunsetStrategy: SunsetStrategy{
				ScaledownDelay: &scaledownDelay,
				DeleteDelay:    &deleteDelay,
			},
			WorkerOptions: WorkerOptions{
				ConnectionRef:     ConnectionReference{Name: "test-connection"},
				TemporalNamespace: "default",
			},
		},
	}
}

var _ = Describe("WorkerDeployment validating webhook", func() {
	It("admits a valid WorkerDeployment", func() {
		ns := makeTestNamespace("wd-webhook-valid")
		wd := makeWDForWebhook("valid-worker", ns)

		Expect(k8sClient.Create(ctx, wd)).To(Succeed())
		Expect(k8sClient.Delete(ctx, wd)).To(Succeed())
	})

	It("rejects Progressive rollout steps with non-increasing ramp percentages", func() {
		ns := makeTestNamespace("wd-webhook-ramp")
		wd := makeWDForWebhook("bad-ramp-worker", ns)
		wd.Spec.RolloutStrategy.Strategy = UpdateProgressive
		wd.Spec.RolloutStrategy.Steps = []RolloutStep{
			{RampPercentage: 50, PauseDuration: metav1.Duration{Duration: time.Minute}},
			{RampPercentage: 25, PauseDuration: metav1.Duration{Duration: time.Minute}},
		}

		err := k8sClient.Create(ctx, wd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("rampPercentage must increase between each step"))
	})

	It("rejects a gate that sets both input and inputFrom", func() {
		ns := makeTestNamespace("wd-webhook-gate")
		wd := makeWDForWebhook("bad-gate-worker", ns)
		wd.Spec.RolloutStrategy.Gate = &GateWorkflowConfig{
			WorkflowType: "gate-workflow",
			Input:        &apiextensionsv1.JSON{Raw: []byte(`{"key":"value"}`)},
			InputFrom: &GateInputSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "gate-input"},
					Key:                  "input",
				},
			},
		}

		err := k8sClient.Create(ctx, wd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("only one of input or inputFrom may be set"))
	})

	It("enforces the same rules on update", func() {
		ns := makeTestNamespace("wd-webhook-update")
		wd := makeWDForWebhook("update-worker", ns)
		Expect(k8sClient.Create(ctx, wd)).To(Succeed())

		wd.Spec.RolloutStrategy.Strategy = UpdateProgressive
		wd.Spec.RolloutStrategy.Steps = []RolloutStep{
			{RampPercentage: 30, PauseDuration: metav1.Duration{Duration: time.Minute}},
			{RampPercentage: 30, PauseDuration: metav1.Duration{Duration: time.Minute}},
		}

		err := k8sClient.Update(ctx, wd)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("rampPercentage must increase between each step"))
	})
})
