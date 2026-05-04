/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"k8s.io/client-go/dynamic"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	projectionv1 "github.com/projection-operator/projection/api/v1"
	"github.com/projection-operator/projection/internal/controller"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(projectionv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// runtimeConfig carries the parsed CLI flag values.
type runtimeConfig struct {
	metricsAddr                 string
	probeAddr                   string
	secureMetrics               bool
	enableHTTP2                 bool
	enableLeaderElection        bool
	sourceModeFlag              string
	requeueInterval             time.Duration
	leaderElectionLeaseDuration time.Duration
	selectorWriteConcurrency    int
}

// bindFlags registers projection's CLI flags on the given FlagSet and
// returns a config populated once the FlagSet is parsed. Separate from
// main() so tests can bind to a fresh FlagSet without touching globals.
func bindFlags(fs *flag.FlagSet) *runtimeConfig {
	c := &runtimeConfig{}
	fs.StringVar(&c.metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	fs.StringVar(&c.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	fs.BoolVar(&c.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	fs.BoolVar(&c.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.BoolVar(&c.enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	fs.StringVar(&c.sourceModeFlag, "source-mode", "allowlist",
		"Policy for which source objects are projectable. "+
			"\"allowlist\" (default) requires the source to carry annotation "+
			"projection.be0x74a.io/projectable=\"true\". "+
			"\"permissive\" allows any source. "+
			"An annotation value of \"false\" is always honored as an opt-out "+
			"regardless of mode.")
	fs.DurationVar(&c.requeueInterval, "requeue-interval", 30*time.Second,
		"Requeue cadence for Projection reconciliation — how long until the "+
			"controller retries after a successful or failed reconcile. Longer "+
			"values reduce API load in clusters with many flapping projections; "+
			"shorter values speed up the dev loop.")
	fs.DurationVar(&c.leaderElectionLeaseDuration, "leader-election-lease-duration", 15*time.Second,
		"Duration the leader holds the lease before a standby can take over. "+
			"Only relevant when --leader-elect is set. Shorter values mean "+
			"faster failover on leader crash; longer values reduce apiserver "+
			"churn from lease renewals. Must remain strictly greater than the "+
			"controller-runtime renew-deadline default (10s).")
	fs.IntVar(&c.selectorWriteConcurrency, "selector-write-concurrency", 16,
		"Maximum in-flight destination writes per selector-based Projection "+
			"during fan-out. Each worker issues a Get plus optionally a "+
			"Create or Update against the apiserver; HTTP/2 multiplexing in "+
			"client-go shares a single connection across the workers, so "+
			"this caps parallelism rather than connections. The default of "+
			"16 fits comfortably within typical kube-apiserver APF priority-"+
			"level budgets at production scale; raise it for Projections "+
			"matching thousands of namespaces, lower it on apiserver-"+
			"constrained clusters. Must be > 0.")
	return c
}

func main() {
	cfg := bindFlags(flag.CommandLine)
	var tlsOpts []func(*tls.Config)
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !cfg.enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   cfg.metricsAddr,
		SecureServing: cfg.secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if cfg.secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: cfg.probeAddr,
		LeaderElection:         cfg.enableLeaderElection,
		LeaderElectionID:       "92777bdc.be0x74a.io",
		LeaseDuration:          &cfg.leaderElectionLeaseDuration,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	dynamicClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create dynamic client")
		os.Exit(1)
	}
	var sourceMode controller.SourceMode
	switch cfg.sourceModeFlag {
	case string(controller.SourceModePermissive):
		sourceMode = controller.SourceModePermissive
	case string(controller.SourceModeAllowlist):
		sourceMode = controller.SourceModeAllowlist
	default:
		setupLog.Error(nil, "invalid --source-mode; must be \"permissive\" or \"allowlist\"",
			"value", cfg.sourceModeFlag)
		os.Exit(1)
	}
	setupLog.Info("source projectability policy", "source-mode", sourceMode)
	if cfg.selectorWriteConcurrency <= 0 {
		setupLog.Error(nil, "invalid --selector-write-concurrency; must be > 0",
			"value", cfg.selectorWriteConcurrency)
		os.Exit(1)
	}
	if cfg.selectorWriteConcurrency > 256 {
		// No hard ceiling — apiserver APF budgets and client-go connection
		// pool size both ultimately bound throughput more than this knob —
		// but values this high are unusual enough to warn so operators can
		// confirm the choice was deliberate.
		setupLog.Info("--selector-write-concurrency is unusually high; "+
			"verify the apiserver can absorb the parallel write load",
			"value", cfg.selectorWriteConcurrency)
	}
	if err = (&controller.ProjectionReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		DynamicClient:            dynamicClient,
		RESTMapper:               mgr.GetRESTMapper(),
		SourceMode:               sourceMode,
		RequeueInterval:          cfg.requeueInterval,
		SelectorWriteConcurrency: cfg.selectorWriteConcurrency,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Projection")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

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
