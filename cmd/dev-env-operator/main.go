// Command dev-env-operator is the dev-env v2 control plane (DESIGN-001 3.1): it
// serves the /v1 API and reconciles AgentSession resources into pods and volumes.
// The access broker is a second mode of this binary (`dev-env-operator broker`,
// DESIGN-001 6.12), deployed as its own Deployment with its own ServiceAccount.
//
// Built so far: the AgentSession reconciler (plan 01 step 2), which creates each
// session's pod and volume from dev-env-templates, rescues a pod by exec before a
// suspend deletes it and archives a reaped session's volume after a verified
// rescue (plan 01 step 5, D-51), and the /v1 API (plan 01 step 3,
// internal/apiserver, D-46), which every replica serves on :8443, with its grant
// routes (plan 07 step 2, D-56). The broker mode arrives later in plan 07.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
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
	apiAddr            string
	apiTLSDir          string
	humanSA            string
	clientSAs          []string
	grantApprovalURL   string
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
	fs.StringVar(&o.apiAddr, "api-bind-address", ":8443", "address of the /v1 API (HTTPS, D-46); 0 turns it off")
	fs.StringVar(&o.apiTLSDir, "api-tls-dir", "/etc/dev-env-operator/api-tls", "directory holding the /v1 API's tls.crt and tls.key, the cert-manager Secret's mount")
	fs.StringVar(&o.humanSA, "human-service-account", ownNamespace+"/dev-env-human", "Tom's ServiceAccount, <namespace>/<name>, whose token his laptop mints (D-05)")
	fs.StringVar(&o.grantApprovalURL, "grant-approval-url", "", "base URL of the broker's approval page, such as https://dev-env.example.com/grants/; a pending grant's view links to it plus the grant's name (D-56). Empty links nothing")
	clients := fs.String("client-service-accounts", "dev-agents/dev-env-workbench", "comma-separated ServiceAccounts, <namespace>/<name>, of trusted clients that are not sessions: the workbench, and the v1 pod (dev/dev-env) until cutover (D-46)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if o.sessionNamespace == "" || o.templatesNamespace == "" || o.templatesName == "" {
		return o, errors.New("--session-namespace, --templates-namespace and --templates-name must not be empty")
	}
	if o.apiAddr != "0" {
		if o.apiTLSDir == "" {
			return o, errors.New("--api-tls-dir must not be empty while the API is on")
		}
		if _, err := apiserver.ParseServiceAccountRefs([]string{o.humanSA}); err != nil || o.humanSA == "" {
			return o, fmt.Errorf("--human-service-account: %q is not <namespace>/<serviceaccount>", o.humanSA)
		}
		refs, err := apiserver.ParseServiceAccountRefs(strings.Split(*clients, ","))
		if err != nil {
			return o, fmt.Errorf("--client-service-accounts: %w", err)
		}
		o.clientSAs = refs
		if o.grantApprovalURL != "" {
			u, err := url.Parse(o.grantApprovalURL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
				return o, fmt.Errorf("--grant-approval-url: %q is not an https URL with a host and no query", o.grantApprovalURL)
			}
		}
	}
	return o, nil
}

func run(args []string) error {
	o, err := parseFlags(args)
	if err != nil {
		return err
	}
	logHandler := slog.NewJSONHandler(os.Stderr, nil)
	ctrl.SetLogger(logr.FromSlogHandler(logHandler))
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
	// Rescue runs agentd in the session's pod by exec (D-08, D-51).
	rescuer, err := controller.NewExecRescuer(cfg)
	if err != nil {
		return err
	}
	r := &controller.Reconciler{
		Client:    mgr.GetClient(),
		Templates: templatesKey,
		APIURL:    o.apiURL,
		APIReader: mgr.GetAPIReader(),
		Rescuer:   rescuer,
		Recorder:  mgr.GetEventRecorder(binaryName),
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
	if o.apiAddr != "0" {
		api := &apiserver.Server{
			Client: mgr.GetClient(),
			Live:   mgr.GetAPIReader(),
			Auth:   apiserver.TokenReviewer{Client: mgr.GetClient()},
			Policy: apiserver.Policy{
				Human:                 o.humanSA,
				Clients:               o.clientSAs,
				SessionNamespace:      o.sessionNamespace,
				SessionServiceAccount: controller.ServiceAccountName,
			},
			Templates:        apiserver.TemplatesFrom(mgr.GetClient(), templatesKey),
			Log:              ctrl.Log.WithName("api"),
			GrantApprovalURL: o.grantApprovalURL,
		}
		runner := &apiserver.Runner{
			Addr:     o.apiAddr,
			CertDir:  o.apiTLSDir,
			Handler:  api.Handler(),
			ErrorLog: slog.NewLogLogger(logHandler, slog.LevelWarn),
		}
		if err := mgr.Add(runner); err != nil {
			return err
		}
		if err := mgr.AddReadyzCheck("api", runner.ReadyCheck); err != nil {
			return err
		}
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
