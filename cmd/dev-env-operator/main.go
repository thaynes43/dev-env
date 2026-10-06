// Command dev-env-operator is the dev-env v2 control plane (DESIGN-001 3.1): it
// serves the /v1 API and reconciles AgentSession resources into pods and volumes.
// The access broker is a second mode of this binary (`dev-env-operator broker`,
// DESIGN-001 6.12), deployed as its own Deployment with its own ServiceAccount.
//
// Built so far: the AgentSession reconciler (plan 01 step 2), which creates each
// session's pod and volume from dev-env-templates. The /v1 API arrives in plan 01
// step 3, the broker mode in plan 07.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/templates"
	"github.com/thaynes43/dev-env/internal/version"
)

const (
	binaryName = "dev-env-operator"
	// leaderElectionID is the Lease the two replicas compete for (DESIGN-001
	// 3.1). Only the leader reconciles.
	leaderElectionID = "dev-env-operator.dev-env.haynesops.com"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.Get().String(binaryName))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "broker" {
		_, _ = fmt.Fprintf(os.Stderr, "%s: the broker mode is not built yet; it arrives in plan 07.\n", binaryName)
		os.Exit(1)
	}
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", binaryName, err)
		os.Exit(1)
	}
}

type options struct {
	sessionNamespace   string
	templatesNamespace string
	templatesName      string
	leaderElect        bool
	metricsAddr        string
	probeAddr          string
	apiURL             string
}

func parseFlags(args []string) (options, error) {
	// The operator's own namespace, from the downward API in the Deployment;
	// dev-env-system when unset (D-02).
	ownNamespace := os.Getenv("POD_NAMESPACE")
	if ownNamespace == "" {
		ownNamespace = "dev-env-system"
	}
	var o options
	fs := flag.NewFlagSet(binaryName, flag.ContinueOnError)
	fs.StringVar(&o.sessionNamespace, "session-namespace", "dev-agents", "namespace of the AgentSessions and their pods and volumes (D-02)")
	fs.StringVar(&o.templatesNamespace, "templates-namespace", ownNamespace, "namespace of the templates ConfigMap; the leader-election Lease lives there too")
	fs.StringVar(&o.templatesName, "templates-name", templates.DefaultName, "name of the templates ConfigMap (D-04)")
	fs.BoolVar(&o.leaderElect, "leader-elect", true, "elect one leader among the replicas; only the leader reconciles")
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "address of the Prometheus metrics endpoint; 0 turns it off")
	fs.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "address of /healthz and /readyz")
	fs.StringVar(&o.apiURL, "api-url", "", "base URL of the operator's /v1 API that session pods report to (AGENTD_API_URL, D-41); empty turns their heartbeat off")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if o.sessionNamespace == "" || o.templatesNamespace == "" || o.templatesName == "" {
		return o, errors.New("--session-namespace, --templates-namespace and --templates-name must not be empty")
	}
	return o, nil
}

func run(args []string) error {
	o, err := parseFlags(args)
	if err != nil {
		return err
	}
	ctrl.SetLogger(logr.FromSlogHandler(slog.NewJSONHandler(os.Stderr, nil)))
	log := ctrl.Log.WithName(binaryName)
	log.Info("starting", "version", version.Get().String(binaryName),
		"sessionNamespace", o.sessionNamespace, "templates", o.templatesNamespace+"/"+o.templatesName)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	templatesKey := types.NamespacedName{Namespace: o.templatesNamespace, Name: o.templatesName}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		// The operator's RBAC reaches dev-agents (and dev-tools, later) only,
		// plus the templates in its own namespace (DESIGN-001 6.11, D-44).
		Cache:                         controller.CacheOptions(o.sessionNamespace, templatesKey),
		Metrics:                       metricsserver.Options{BindAddress: o.metricsAddr},
		HealthProbeBindAddress:        o.probeAddr,
		LeaderElection:                o.leaderElect,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       o.templatesNamespace,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return err
	}
	r := &controller.Reconciler{
		Client:    mgr.GetClient(),
		Templates: templatesKey,
		APIURL:    o.apiURL,
		APIReader: mgr.GetAPIReader(),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
