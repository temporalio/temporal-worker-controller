// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"go.temporal.io/sdk/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(temporaliov1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var watchNamespaces string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&watchNamespaces, "watch-namespaces", "",
		"Comma-separated list of namespaces the controller watches. "+
			"If empty, the controller watches all namespaces.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if watchNamespaces == "" {
		watchNamespaces = os.Getenv("WATCH_NAMESPACES")
	}
	namespaces := controller.ParseWatchNamespaces(watchNamespaces)

	//ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	ctrl.SetLogger(zap.New(zap.JSONEncoder()))

	if len(namespaces) > 0 {
		setupLog.Info("running controller in namespace-scoped mode", "namespaces", namespaces)
	}

	cacheOptions, err := controller.NewCacheOptions(namespaces)
	if err != nil {
		setupLog.Error(err, "unable to build manager cache options")
		os.Exit(1)
	}

	config := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Cache:  cacheOptions,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "98e39f52.temporal.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})

	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	detectionClient, err := dynamic.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "unable to create deprecated CRD detection client")
		os.Exit(1)
	}
	detectionCtx, cancelDetection := context.WithTimeout(context.Background(), 10*time.Second)
	deprecatedCRDWatches, err := controller.DetectDeprecatedCRDWatches(detectionCtx, detectionClient, namespaces)
	cancelDetection()
	if err != nil {
		setupLog.Error(err, "unable to detect deprecated CRD watches")
		os.Exit(1)
	}
	if !deprecatedCRDWatches.TemporalWorkerDeployments {
		setupLog.Info("skipping deprecated TemporalWorkerDeployment watches")
	}
	if !deprecatedCRDWatches.TemporalConnections {
		setupLog.Info("skipping deprecated TemporalConnection watches")
	}

	if err = (&controller.WorkerDeploymentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		TemporalClientPool: clientpool.New(
			log.NewStructuredLogger(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
				AddSource:   false,
				Level:       nil,
				ReplaceAttr: nil,
			}))),
			mgr.GetClient(),
		),
		Recorder: mgr.GetEventRecorderFor("temporal-worker-controller"),
		MaxDeploymentVersionsIneligibleForDeletion: controller.GetControllerMaxDeploymentVersionsIneligibleForDeletion(),
		DisableDeprecatedTWD:                       !deprecatedCRDWatches.TemporalWorkerDeployments,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkerDeployment")
		os.Exit(1)
	}
	if deprecatedCRDWatches.TemporalWorkerDeployments {
		if err = (&controller.DeprecatedTWDReconciler{
			Client: mgr.GetClient(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "TemporalWorkerDeployment")
			os.Exit(1)
		}
	}
	if deprecatedCRDWatches.TemporalConnections {
		if err = (&controller.DeprecatedTCReconciler{
			Client: mgr.GetClient(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "TemporalConnection")
			os.Exit(1)
		}
	}
	if err = temporaliov1alpha1.NewWorkerResourceTemplateValidator(mgr).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "WorkerResourceTemplate")
		os.Exit(1)
	}
	if err := (&temporaliov1alpha1.WorkerDeployment{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "WorkerDeployment")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	podNamespace := os.Getenv("POD_NAMESPACE")
	if podNamespace == "" {
		setupLog.Error(nil, "POD_NAMESPACE environment variable must be set")
		os.Exit(1)
	}

	if os.Getenv(controller.IdentityEnvKey) == "" {
		setupLog.Error(nil, "CONTROLLER_IDENTITY environment variable must be set")
		os.Exit(1)
	}

	saName := os.Getenv("SERVICE_ACCOUNT_NAME")
	if saName == "" {
		setupLog.Error(nil, "SERVICE_ACCOUNT_NAME environment variable must be set")
		os.Exit(1)
	}

	var sa corev1.ServiceAccount
	if err := mgr.GetAPIReader().Get(context.Background(), types.NamespacedName{Namespace: podNamespace, Name: saName}, &sa); err != nil {
		setupLog.Error(err, "unable to fetch service account UID for controller identity suffix")
		os.Exit(1)
	}
	if err := os.Setenv(controller.IdentitySuffixEnvKey, string(sa.UID)); err != nil {
		setupLog.Error(err, fmt.Sprintf("unable to set %s", controller.IdentitySuffixEnvKey))
		os.Exit(1)
	}

	// Migration window: also record the namespace UID as the legacy identity suffix so the
	// controller can re-claim Worker Deployments still managed under its previous
	// (namespace-UID) identity instead of deadlocking after this upgrade. This requires the
	// cluster-scoped namespaces "get" grant; both the read and the grant can be removed in a
	// future release once all managed deployments have been re-claimed under the SA-UID identity.
	var ns corev1.Namespace
	if err := mgr.GetAPIReader().Get(context.Background(), types.NamespacedName{Name: podNamespace}, &ns); err != nil {
		setupLog.Error(err, "unable to fetch namespace UID for legacy controller identity suffix")
		os.Exit(1)
	}
	if err := os.Setenv(controller.LegacyIdentitySuffixEnvKey, string(ns.UID)); err != nil {
		setupLog.Error(err, fmt.Sprintf("unable to set %s", controller.LegacyIdentitySuffixEnvKey))
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
