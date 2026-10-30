package v1alpha1

// Integration tests for CRD-level schema validation on WorkerResourceTemplate.
//
// These tests hit a real kube-apiserver (via envtest), so they verify the OpenAPI
// schema in the generated CRD manifest. The API server checks the schema before it
// calls the admission webhook.

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = Describe("WorkerResourceTemplate CRD schema validation", func() {
	var ns string

	BeforeEach(func() {
		ns = makeTestNamespace("wrt-schema")
	})

	It("accepts versionStatuses with known statuses", func() {
		grantControllerSAHPACreateAccess(ns)
		wrt := makeWRTForWebhook("current-hpa", ns, "my-worker", hpaObjForIntegration())
		wrt.Spec.VersionStatuses = []VersionStatus{VersionStatusCurrent, VersionStatusRamping}
		Expect(k8sClient.Create(ctx, wrt)).To(Succeed())
	})

	It("rejects an unknown version status", func() {
		wrt := makeWRTForWebhook("unknown-status", ns, "my-worker", hpaObjForIntegration())
		wrt.Spec.VersionStatuses = []VersionStatus{"Retired"}
		err := k8sClient.Create(ctx, wrt)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.versionStatuses[0]"))
		Expect(err.Error()).To(ContainSubstring("Unsupported value"))
	})

	It("rejects an empty versionStatuses list", func() {
		wrt := makeWRTForWebhook("empty-statuses", ns, "my-worker", hpaObjForIntegration())
		wrt.SetGroupVersionKind(GroupVersion.WithKind("WorkerResourceTemplate"))
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(wrt)
		Expect(err).NotTo(HaveOccurred())
		// Set the list on the unstructured object: omitempty drops an empty slice
		// from the typed object, so it would never reach the API server.
		Expect(unstructured.SetNestedSlice(obj, []interface{}{}, "spec", "versionStatuses")).To(Succeed())
		err = k8sClient.Create(ctx, &unstructured.Unstructured{Object: obj})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec.versionStatuses"))
	})

	It("rejects a duplicate version status", func() {
		wrt := makeWRTForWebhook("duplicate-status", ns, "my-worker", hpaObjForIntegration())
		wrt.Spec.VersionStatuses = []VersionStatus{VersionStatusDraining, VersionStatusDraining}
		err := k8sClient.Create(ctx, wrt)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Duplicate value"))
	})
})
