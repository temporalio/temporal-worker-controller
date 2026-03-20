package v1alpha1

// Integration tests for the WorkerResourceTemplate validating webhook.
//
// These tests run through the real envtest HTTP admission path — the kube-apiserver
// sends actual AdmissionRequests to the webhook server — validating that:
//   - The webhook is correctly registered and called on WRT create/update/delete
//   - Spec-only rejections (kind not in allowed list) work end-to-end
//   - SubjectAccessReview (SAR) checks for the requesting user are enforced
//   - SAR checks for the controller service account are enforced
//   - temporalWorkerDeploymentRef.name immutability is enforced via a real update request
//
// Controller SA identity: POD_NAMESPACE=test-system, SERVICE_ACCOUNT_NAME=test-controller
// (set in webhook_suite_test.go BeforeSuite before the validator is constructed).

import (
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// hpaObjForIntegration returns a minimal valid HPA embedded object spec.
// HPA is in the default allowed kinds list and is namespace-scoped, making
// it a suitable resource for webhook integration tests.
func hpaObjForIntegration() map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"spec": map[string]interface{}{
			"minReplicas": float64(2),
			"maxReplicas": float64(10),
		},
	}
}

// makeWRTForWebhook builds a WorkerResourceTemplate in the given namespace.
func makeWRTForWebhook(name, ns, workerDeploymentRef string, embeddedObj map[string]interface{}) *WorkerResourceTemplate {
	raw, _ := json.Marshal(embeddedObj)
	return &WorkerResourceTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: WorkerResourceTemplateSpec{
			TemporalWorkerDeploymentRef: TemporalWorkerDeploymentReference{Name: workerDeploymentRef},
			Template:                    runtime.RawExtension{Raw: raw},
		},
	}
}

// makeTestNamespace creates a unique namespace for a test and returns its name.
func makeTestNamespace(prefix string) string {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix + "-",
		},
	}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return ns.Name
}

// grantControllerSAHPACreateAccess creates a Role granting the controller SA
// (system:serviceaccount:test-system:test-controller) HPA create access in ns.
// This ensures the controller SA SAR check passes for tests that focus on user SAR.
func grantControllerSAHPACreateAccess(ns string) {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-hpa-creator", Namespace: ns},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"autoscaling"},
				Resources: []string{"horizontalpodautoscalers"},
				Verbs:     []string{"create", "update", "delete"},
			},
		},
	}
	Expect(k8sClient.Create(ctx, role)).To(Succeed())
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-hpa-creator-rb", Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "sa-hpa-creator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "test-controller", Namespace: "test-system"},
		},
	}
	Expect(k8sClient.Create(ctx, rb)).To(Succeed())
}

// grantUserWRTCreateAccess grants a user permission to create WRTs in ns.
// This is required so the kube-apiserver's RBAC check passes before calling the webhook.
func grantUserWRTCreateAccess(ns, username string) {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("wrt-creator-%s", username), Namespace: ns},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"temporal.io"},
				Resources: []string{"workerresourcetemplates"},
				Verbs:     []string{"create", "update", "delete", "get", "list"},
			},
		},
	}
	Expect(k8sClient.Create(ctx, role)).To(Succeed())
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("wrt-creator-rb-%s", username), Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: fmt.Sprintf("wrt-creator-%s", username)},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: username}},
	}
	Expect(k8sClient.Create(ctx, rb)).To(Succeed())
}

// impersonatedClient returns a k8sClient that sends requests as username.
// Impersonation is authorised because the admin credentials in cfg belong to
// the system:masters group, which bypasses all RBAC including impersonation checks.
func impersonatedClient(username string) client.Client {
	impCfg := rest.CopyConfig(cfg)
	impCfg.Impersonate = rest.ImpersonationConfig{
		UserName: username,
		Groups:   []string{"system:authenticated"},
	}
	c, err := client.New(impCfg, client.Options{Scheme: k8sClient.Scheme()})
	Expect(err).NotTo(HaveOccurred())
	return c
}

var _ = Describe("WorkerResourceTemplate webhook integration", func() {

	// Spec-level validation (kind not in allowed list) fires via real HTTP admission.
	// Deployment is not in the allowed kinds list. The webhook must reject the
	// request with an error mentioning the kind before making any API calls.
	It("rejects a kind not in the allowed list via the real HTTP admission path", func() {
		ns := makeTestNamespace("wh-notallowed")
		wrt := makeWRTForWebhook("t14-notallowed", ns, "my-worker", map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"spec":       map[string]interface{}{"replicas": float64(1)},
		})
		err := k8sClient.Create(ctx, wrt)
		Expect(err).To(HaveOccurred(), "webhook must reject a kind not in the allowed list")
		Expect(err.Error()).To(ContainSubstring("Deployment"))
		Expect(err.Error()).To(ContainSubstring("not in the allowed list"))
	})

	// SAR pass — both the requesting user (admin) and the controller SA have
	// HPA create permission in the namespace. The webhook must allow the WRT creation.
	It("allows creation when user and controller SA both have HPA permission", func() {
		ns := makeTestNamespace("wh-sar-pass")
		grantControllerSAHPACreateAccess(ns)

		wrt := makeWRTForWebhook("t15-sar-pass", ns, "my-worker", hpaObjForIntegration())
		Expect(k8sClient.Create(ctx, wrt)).To(Succeed(),
			"admission must succeed when both user and controller SA can create HPAs")
	})

	// SAR fail — requesting user lacks HPA create permission.
	// The controller SA has HPA access (so that check passes), but the requesting user
	// (impersonated as "webhook-test-alice") has no HPA RBAC. The webhook must reject.
	It("rejects creation when the requesting user lacks HPA permission", func() {
		ns := makeTestNamespace("wh-user-fail")
		grantControllerSAHPACreateAccess(ns)

		const username = "webhook-test-alice"
		// alice needs WRT create permission to reach the webhook (kube-apiserver checks
		// this before forwarding the request to the admission webhook).
		grantUserWRTCreateAccess(ns, username)
		// Intentionally do NOT grant alice HPA create permission.

		aliceClient := impersonatedClient(username)
		wrt := makeWRTForWebhook("t16-user-fail", ns, "my-worker", hpaObjForIntegration())
		err := aliceClient.Create(ctx, wrt)
		Expect(err).To(HaveOccurred(), "webhook must reject when requesting user cannot create HPAs")
		Expect(err.Error()).To(ContainSubstring(username))
		Expect(err.Error()).To(ContainSubstring("not authorized"))
	})

	// SAR fail — controller SA lacks HPA create permission.
	// The admin user (k8sClient) has all permissions, so the user SAR passes. However,
	// system:serviceaccount:test-system:test-controller has no HPA RBAC in this namespace,
	// so the controller SA SAR must fail and the webhook must reject.
	It("rejects creation when the controller SA lacks HPA permission", func() {
		ns := makeTestNamespace("wh-sa-fail")
		// Intentionally do NOT call grantControllerSAHPACreateAccess — the SA has no HPA RBAC here.

		wrt := makeWRTForWebhook("t17-sa-fail", ns, "my-worker", hpaObjForIntegration())
		err := k8sClient.Create(ctx, wrt)
		Expect(err).To(HaveOccurred(), "webhook must reject when controller SA cannot create HPAs")
		Expect(err.Error()).To(ContainSubstring("test-controller"))
		Expect(err.Error()).To(ContainSubstring("not authorized"))
	})

	// temporalWorkerDeploymentRef.name immutability enforced via a real HTTP update request.
	// First create a valid WRT, then attempt to change temporalWorkerDeploymentRef.name via k8sClient.Update.
	// The webhook must reject the update with a message about immutability.
	It("rejects workerDeploymentRef.name change via real HTTP update", func() {
		ns := makeTestNamespace("wh-immutable")
		grantControllerSAHPACreateAccess(ns)

		wrt := makeWRTForWebhook("t18-immutable", ns, "original-worker", hpaObjForIntegration())
		Expect(k8sClient.Create(ctx, wrt)).To(Succeed(), "initial creation must succeed")

		var created WorkerResourceTemplate
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "t18-immutable", Namespace: ns}, &created)).To(Succeed())

		updated := created.DeepCopy()
		updated.Spec.TemporalWorkerDeploymentRef.Name = "different-worker"
		err := k8sClient.Update(ctx, updated)
		Expect(err).To(HaveOccurred(), "webhook must reject workerDeploymentRef.name change")
		Expect(err.Error()).To(ContainSubstring("temporalWorkerDeploymentRef.name"))
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})
})
