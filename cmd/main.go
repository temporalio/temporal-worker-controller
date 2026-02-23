// Unless explicitly stated otherwise all files in this repository are licensed under the MIT License.
//
// This product includes software developed at Datadog (https://www.datadoghq.com/). Copyright 2024 Datadog, Inc.

package main

import (
	"flag"
	"log/slog"
	"os"
	"strings"

	temporaliov1alpha1 "github.com/temporalio/temporal-worker-controller/api/v1alpha1"
	"github.com/temporalio/temporal-worker-controller/internal/controller"
	"github.com/temporalio/temporal-worker-controller/internal/controller/clientpool"
	"go.temporal.io/sdk/log"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
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
	var watchNamespace string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&watchNamespace,"watch-namespace","",
		"Namespace(s) that the controller watches. Can be a single namespace or a comma-separated list. "+
		"If empty, the controller watches all namespaces.",	)
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// If flag is not set, fall back to environment variable
	if watchNamespace == "" {
		watchNamespace = os.Getenv("WATCH_NAMESPACE")
	}

	// Parse comma-separated namespaces into []string, trimming whitespace and dropping empty entries
	var watchNamespaces []string
	if watchNamespace != "" {
		parts := strings.Split(watchNamespace, ",")
		watchNamespaces = make([]string, 0, len(parts))
		for _, p := range parts {
			ns := strings.TrimSpace(p)
			if ns == "" {
				continue
			}
			watchNamespaces = append(watchNamespaces, ns)
		}
	}

	//ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	ctrl.SetLogger(zap.New(zap.JSONEncoder()))

	managerOptions := ctrl.Options{
		Scheme: scheme,
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
	}

	// Scope manager cache/watches to 0/1/N namespaces.
	if len(watchNamespaces) > 0 {
		setupLog.Info("running controller in namespace-scoped mode", "namespaces", watchNamespaces)

		defaultNamespaces := map[string]cache.Config{}
		for _, ns := range watchNamespaces {
			defaultNamespaces[ns] = cache.Config{}
		}

		managerOptions.Cache = cache.Options{
			DefaultNamespaces: defaultNamespaces,
		}
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions)

	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.TemporalWorkerDeploymentReconciler{
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
		MaxDeploymentVersionsIneligibleForDeletion: controller.GetControllerMaxDeploymentVersionsIneligibleForDeletion(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TemporalWorkerDeployment")
		os.Exit(1)
	}
	// TODO(jlegrone): Enable the webhook after fixing TLS
	//if err = (&temporaliov1alpha1.TemporalWorker{}).SetupWebhookWithManager(mgr); err != nil {
	//	setupLog.Error(err, "unable to create webhook", "webhook", "TemporalWorker")
	//	os.Exit(1)
	//}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
