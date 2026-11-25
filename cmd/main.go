/*
Copyright 2026.

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

// Scheme, in simple words

// A scheme is a registry that tells your Go program which Go struct corresponds to which Kubernetes kind. It's a lookup table between the two.

// Go type                    ↔   Kubernetes identity (GVK)
// ─────────────────────────      ──────────────────────────────────────
// corev1.Pod                 ↔   v1, Kind=Pod
// appsv1.Deployment          ↔   apps/v1, Kind=Deployment
// computev1.EC2Instance      ↔   compute.cloud.com/v1, Kind=EC2Instance

// Why it's needed

// The API server talks in YAML or JSON and your code works with Go structs. The client has to convert between the two all the time, and to do that it must know the mapping:

// Reading: the API server returns JSON with kind: EC2Instance. The client looks it up in the scheme to find that it should turn it into your EC2Instance struct.
// Writing: when you call r.Update(ctx, &inst), the client looks up the struct's type in the scheme to learn its apiVersion and kind before sending it.

package main

import (
	"crypto/tls"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth" // this make sure that the operator can talk to the k8s cluster using the kubeconfig file, and can authenticate with the API server using the credentials in the kubeconfig file.

	"k8s.io/apimachinery/pkg/runtime"                                 // we understands yaml but kubernetes understands Go structs so it Provides runtime.Scheme, a registry that maps Go types to Kubernetes kinds (EC2Instance Go struct ↔ compute.cloud.com/v1, Kind=EC2Instance).
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"                // Helper utilruntime.Must(...) that panics if a registration fails, so you don't silently run with a broken setup.
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"               // Registers all the built-in types (Pod, Deployment, Service, ...) into a scheme.
	ctrl "sigs.k8s.io/controller-runtime"                             // The core library. ctrl.NewManager(...) creates the manager that runs your controllers, watches resources and calls Reconcile()
	"sigs.k8s.io/controller-runtime/pkg/healthz"                      // Adds /healthz and /readyz endpoints, so Kubernetes can check that your operator Pod is alive and ready (liveness and readiness probes).
	"sigs.k8s.io/controller-runtime/pkg/log/zap"                      // The logger (zap) used for the operator's log output.
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"              // Adds authentication and authorization on the metrics endpoint, so not just anyone can read it.
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server" // Serves Prometheus-style metrics (reconcile counts, durations, errors).
	"sigs.k8s.io/controller-runtime/pkg/webhook"                      // Sets up the webhook server, for admission webhooks that validate or modify/mutate your resources before they're saved. You don't use these until you add webhooks.

	computev1 "github.com/kunalpandey1/custom-kubernetes-operator/api/v1" // EC2Instance types. Registered into the same scheme so the client understands your CRD.
	"github.com/kunalpandey1/custom-kubernetes-operator/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(computev1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var webhookPort int
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Port the webhook server listens on. "+
		"Defaults to 9443. Set -1 to disable the webhook server.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
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
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
		Port:    webhookPort,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "b84c6c5b.cloud.com",
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
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if err := (&controller.EC2InstanceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "ec2instance")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

// explantion:-

// main.go is the starting point of your operator. When the operator runs, this file is what executes, and its job is to set everything up and then start the manager. I'll go through it from top to bottom.

// 1. The scheme (top of the file)

// var scheme = runtime.NewScheme()

// func init() {
// 	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
// 	utilruntime.Must(computev1.AddToScheme(scheme))
// }

// The scheme is a list of resource types your operator understands. The first line adds the built-in Kubernetes types (Pod, Deployment and so on), and the second adds your own EC2Instance. init() runs automatically before main(), so the list is ready by the time the manager starts. setupLog is just a logger used for the startup messages.

// 2. Flags (the long block of flag.StringVar lines)

// These define the command-line options the operator accepts. Each one has a name, a variable to store it in, a default value and a description. The important ones:

// --leader-elect (default false): turns on leader election, so only one copy reconciles at a time. This is off when you run locally.
// --metrics-bind-address (default "0"): where the metrics endpoint listens. 0 means metrics are turned off.
// --health-probe-bind-address (default :8081): where the health checks listen.
// --webhook-port (default 9443): the port for the webhook server.
// The *-cert-path, *-cert-name and *-cert-key flags: where to find TLS certificates for the metrics and webhook servers.
// --enable-http2 (default false): explained below.

// flag.Parse() then reads whatever you passed on the command line and fills in these variables.

// 3. Logger

// opts := zap.Options{Development: true}
// ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

// This sets up the logger (zap). Development: true gives readable, human-friendly log output, which is good for learning. For production you would usually switch to the compact JSON format.

// 4. Disabling HTTP/2

// disableHTTP2 := func(c *tls.Config) {
// 	c.NextProtos = []string{"http/1.1"}
// }

// HTTP/2 had some known security problems (the Rapid Reset attack, linked in the comment), so Kubebuilder turns it off by default for the metrics and webhook servers. They fall back to HTTP/1.1. The --enable-http2 flag exists only if you deliberately want it back. You can leave this as it is.

// 5. Webhook server

// webhookServer := webhook.NewServer(webhookServerOptions)

// This creates the server that will receive admission requests from the API server. It uses the port from the flag, plus the certificates if you gave a certificate path. You haven't written any webhooks yet, so it isn't doing anything useful for you at the moment. It's set up now so that adding webhooks later needs no changes here.

// 6. Metrics server

// metricsServerOptions := metricsserver.Options{ BindAddress: metricsAddr, SecureServing: secureMetrics, ... }
// if secureMetrics {
// 	metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
// }

// This configures the metrics endpoint, which exposes numbers like how many reconciles ran and how many failed. With secureMetrics on (the default), it serves over HTTPS and uses the filter to check authentication and permissions, so only authorized users can read the metrics. If you don't give it certificates, controller-runtime creates self-signed ones, which the comments say is fine for development but not for production.

// 7. Creating the manager (the main part)

// mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
// 	Scheme:                 scheme,
// 	Metrics:                metricsServerOptions,
// 	WebhookServer:          webhookServer,
// 	HealthProbeBindAddress: probeAddr,
// 	LeaderElection:         enableLeaderElection,
// 	LeaderElectionID:       "b84c6c5b.cloud.com",
// })

// This is the manager from your diagram. Each piece is passed into it:

// ctrl.GetConfigOrDie() finds how to connect to the cluster. It reads your kubeconfig when you run locally and uses the in-cluster credentials when the operator runs as a Pod. "OrDie" means it exits with an error if it can't find any.
// Scheme is the list of types from step 1.
// Metrics, WebhookServer and HealthProbeBindAddress are the servers set up above.
// LeaderElection and LeaderElectionID control leader election. The ID b84c6c5b.cloud.com is the name of the shared Lease that the copies compete for. It is generated from your domain.

// The long comment about LeaderElectionReleaseOnCancel is an optional setting that lets the leader hand over leadership quickly when it shuts down. It's commented out, and you don't need it.

// If creating the manager fails, the program logs the error and exits with os.Exit(1).

// 8. Registering your controller

// if err := (&controller.EC2InstanceReconciler{
// 	Client: mgr.GetClient(),
// 	Scheme: mgr.GetScheme(),
// }).SetupWithManager(mgr); err != nil { ... }
// // +kubebuilder:scaffold:builder

// This is the important connection. It creates your reconciler, gives it the manager's client and scheme, and registers it with the manager through SetupWithManager. After this, the manager knows to call your Reconcile() whenever an EC2Instance changes.

// If you add another kind later, Kubebuilder adds another block like this at the scaffold:builder marker comment. Leave that comment in place.

// 9. Health checks

// mgr.AddHealthzCheck("healthz", healthz.Ping)
// mgr.AddReadyzCheck("readyz", healthz.Ping)

// These add two endpoints, /healthz ("am I alive?") and /readyz ("am I ready?"), on the probe port (:8081). Kubernetes uses them as liveness and readiness probes to decide whether to restart your operator's Pod. healthz.Ping is the simplest possible check: it always says "yes" as long as the process is responding.

// 10. Starting everything

// mgr.Start(ctrl.SetupSignalHandler())

// This is the line that starts the forever loop. It starts the cache, the controllers, the webhook server and the metrics server, and then it blocks and keeps running. SetupSignalHandler() makes it listen for shutdown signals, such as Ctrl+C or Kubernetes stopping the Pod, so it can shut everything down cleanly. If the manager stops with an error, the program logs it and exits.

// The whole flow in short

// When the operator starts, init() builds the list of types, main() reads the flags, sets up logging, and configures the webhook and metrics servers. It then creates the manager, registers your EC2InstanceReconciler with it, adds the health checks, and starts the manager, which keeps running and calls your Reconcile() when resources change.

// What you'll edit

// Almost nothing here. The file is generated scaffolding, and your own work happens in ec2instance_controller.go. The parts you might touch later are the flags (to add your own settings) and the controller registration (when you add more kinds).

// Piece 1: &controller.EC2InstanceReconciler{...}

// &controller.EC2InstanceReconciler{
//     Client: mgr.GetClient(),
//     Scheme: mgr.GetScheme(),
// }

// This creates your reconciler object, the struct from internal/controller/ec2instance_controller.go that holds your Reconcile() function.

// controller. is the package name (internal/controller).
// & means "give me a pointer to this struct" instead of a copy. The manager needs to work with the one real object.
// Client: mgr.GetClient() hands the reconciler the manager's client, which is what r.Get(), r.Update() and r.Status().Update() use inside Reconcile(). Reads come from the manager's cache, and writes go to the API server.
// Scheme: mgr.GetScheme() gives it the same scheme (the type registry from init()), so it can convert between Go structs and Kubernetes kinds.

// The reconciler doesn't create its own client or scheme. It borrows the manager's, which is why all controllers share one cache and one connection.

// Piece 2: .SetupWithManager(mgr)

// (...).SetupWithManager(mgr)

// This calls the method that registers the reconciler with the manager. It lives in your ec2instance_controller.go and looks like this:

// func (r *EC2InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
//     return ctrl.NewControllerManagedBy(mgr).
//         For(&computev1.EC2Instance{}).
//         Complete(r)
// }

// It tells the manager: "watch EC2Instance resources, and call this reconciler's Reconcile() whenever one changes." Predicates, Owns(...) and similar options also go here. Registration only happens here. The reconciler doesn't actually run until the manager starts at the bottom of main().

// Piece 3: the error check

// if err := (...).SetupWithManager(mgr); err != nil {
//     setupLog.Error(err, "Failed to create controller", "controller", "ec2instance")
//     os.Exit(1)
// }

// SetupWithManager returns an error, which is nil if it worked. If it failed, the block runs:

// setupLog.Error(...) logs the error with a message and a label saying which controller failed ("controller", "ec2instance").
// os.Exit(1) stops the program with a non-zero exit code, which signals failure. If this is running as a Pod, Kubernetes sees the failure and restarts it.
