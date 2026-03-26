// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package v1alpha1

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	//+kubebuilder:scaffold:imports
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	sigsyaml "sigs.k8s.io/yaml"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var ctx context.Context
var cancel context.CancelFunc

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Webhook Suite")
}

var _ = BeforeSuite(func() {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		Skip("Skipping webhook integration tests: KUBEBUILDER_ASSETS not set")
	}

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment")
	wrtWebhook := loadWRTWebhookFromHelmChart(filepath.Join("..", "..", "helm", "temporal-worker-controller"))
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			// CRDs live in the crds chart's templates directory
			filepath.Join("..", "..", "helm", "temporal-worker-controller-crds", "templates"),
		},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			ValidatingWebhooks: []*admissionregistrationv1.ValidatingWebhookConfiguration{wrtWebhook},
		},
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	scheme := runtime.NewScheme()
	err = AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	err = admissionv1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	// corev1, rbacv1, and authorizationv1 are needed by the integration tests.
	// corev1: create Namespaces
	// rbacv1: create Roles and RoleBindings for RBAC setup
	// authorizationv1: create SubjectAccessReview objects inside the webhook validator
	err = corev1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	err = rbacv1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	err = authorizationv1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// start webhook server using Manager
	webhookInstallOptions := &testEnv.WebhookInstallOptions
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookInstallOptions.LocalServingHost,
			Port:    webhookInstallOptions.LocalServingPort,
			CertDir: webhookInstallOptions.LocalServingCertDir,
		}),
		LeaderElection: false,
		Metrics:        server.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	err = (&TemporalWorkerDeployment{}).SetupWebhookWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	// Set env vars consumed by NewWorkerResourceTemplateValidator before constructing it.
	// POD_NAMESPACE and SERVICE_ACCOUNT_NAME identify the controller SA for the SAR checks.
	// "test-system" / "test-controller" are the values used in the integration tests.
	// ALLOWED_KINDS mirrors the default Helm values so integration tests can create HPAs.
	Expect(os.Setenv("POD_NAMESPACE", "test-system")).To(Succeed())
	Expect(os.Setenv("SERVICE_ACCOUNT_NAME", "test-controller")).To(Succeed())
	Expect(os.Setenv("ALLOWED_KINDS", "HorizontalPodAutoscaler")).To(Succeed())

	err = NewWorkerResourceTemplateValidator(mgr).SetupWebhookWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:webhook

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).NotTo(HaveOccurred())
	}()

	// wait for the webhook server to get ready
	dialer := &net.Dialer{Timeout: time.Second}
	addrPort := fmt.Sprintf("%s:%d", webhookInstallOptions.LocalServingHost, webhookInstallOptions.LocalServingPort)
	Eventually(func() error {
		conn, err := tls.DialWithDialer(dialer, "tcp", addrPort, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			return err
		}
		conn.Close()
		return nil
	}).Should(Succeed())

})

// loadWRTWebhookFromHelmChart renders the Helm chart's webhook.yaml with default values using
// `helm template` and extracts the ValidatingWebhookConfiguration for WorkerResourceTemplate.
// This makes the test authoritative against the Helm chart rather than a hand-maintained copy.
func loadWRTWebhookFromHelmChart(chartPath string) *admissionregistrationv1.ValidatingWebhookConfiguration {
	out, err := exec.Command("helm", "template", "test", chartPath, "--show-only", "templates/webhook.yaml").Output()
	Expect(err).NotTo(HaveOccurred(), "helm template failed — is helm installed?")

	// The file contains multiple YAML documents; find the WRT ValidatingWebhookConfiguration.
	for _, doc := range bytes.Split(out, []byte("\n---\n")) {
		trimmed := strings.TrimSpace(string(doc))
		if !strings.Contains(trimmed, "kind: ValidatingWebhookConfiguration") {
			continue
		}
		if !strings.Contains(trimmed, "vworkerresourcetemplate.kb.io") {
			continue
		}
		jsonBytes, convErr := sigsyaml.YAMLToJSON([]byte(trimmed))
		Expect(convErr).NotTo(HaveOccurred(), "failed to convert WRT webhook YAML to JSON")
		var wh admissionregistrationv1.ValidatingWebhookConfiguration
		Expect(json.Unmarshal(jsonBytes, &wh)).To(Succeed(), "failed to decode WRT ValidatingWebhookConfiguration")
		return &wh
	}
	Fail("WRT ValidatingWebhookConfiguration not found in rendered helm chart webhook.yaml")
	return nil
}

var _ = AfterSuite(func() {
	if testEnv == nil {
		return // BeforeSuite was skipped; nothing to tear down
	}
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
