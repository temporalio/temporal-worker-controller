// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package controller

import (
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	temporaliov1alpha1 "github.com/DataDog/temporal-worker-controller/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment

//func TestControllers(t *testing.T) {
//	RegisterFailHandler(Fail)
//
//	RunSpecs(t, "Controller Suite")
//}
//
//var _ = BeforeSuite(func() {
//	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
//
//	By("bootstrapping test environment")
//	testEnv = &envtest.Environment{
//		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
//		ErrorIfCRDPathMissing: true,
//	}
//
//	var err error
//	// cfg is defined in this file globally.
//	cfg, err = testEnv.Start()
//	Expect(err).NotTo(HaveOccurred())
//	Expect(cfg).NotTo(BeNil())
//
//	err = temporaliov1alpha1.AddToScheme(scheme.Scheme)
//	Expect(err).NotTo(HaveOccurred())
//
//	//+kubebuilder:scaffold:scheme
//
//	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
//	Expect(err).NotTo(HaveOccurred())
//	Expect(k8sClient).NotTo(BeNil())
//})
//
//var _ = AfterSuite(func() {
//	By("tearing down the test environment")
//	err := testEnv.Stop()
//	Expect(err).NotTo(HaveOccurred())
//})

func newTestEnv(t *testing.T) (client.Client, *envtest.Environment) {
	e := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		// TODO(jlegrone): Avoid hardcoding this path, and document pre-test dependency installation steps.
		BinaryAssetsDirectory: filepath.Join("..", "..", "bin", "k8s", "1.27.1-darwin-arm64"),
		//CRDInstallOptions: envtest.CRDInstallOptions{
		//	Scheme:             nil,
		//	Paths:              nil,
		//	CRDs:               nil,
		//	ErrorIfPathMissing: false,
		//	MaxTime:            0,
		//	PollInterval:       0,
		//	CleanUpAfterUse:    false,
		//	WebhookOptions:     envtest.WebhookInstallOptions{},
		//},
	}
	t.Cleanup(func() {
		_ = e.Stop()
	})

	config, err := e.Start()
	if err != nil {
		t.Fatal(err)
	}

	s := runtime.NewScheme()

	if err := temporaliov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}

	//+kubebuilder:scaffold:scheme

	c, err := client.New(config, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}

	return c, e
}
