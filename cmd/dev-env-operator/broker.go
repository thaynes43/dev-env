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
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/version"
)

// brokerOptions are the flags of `dev-env-operator broker` (D-61).
type brokerOptions struct {
	sessionNamespace string
	policyNamespace  string
	leaderElect      bool
	metricsAddr      string
	probeAddr        string
	consoleAddr      string
	consoleOrigin    string
	consoleOwner     string
	consoleReauthURL string
	consoleIssuer    string
	consoleAudience  string
	pushoverDir      string
}

func parseBrokerFlags(args []string) (brokerOptions, error) {
	// The broker's own namespace, from the downward API in its Deployment.
	ownNamespace := os.Getenv("POD_NAMESPACE")
	if ownNamespace == "" {
		ownNamespace = "dev-env-system"
	}
	var o brokerOptions
	fs := flag.NewFlagSet(binaryName+" broker", flag.ContinueOnError)
	fs.StringVar(&o.sessionNamespace, "session-namespace", "dev-agents", "namespace of the AgentSessions, their pods, the AccessGrants and the grant ServiceAccounts (D-54)")
	fs.StringVar(&o.policyNamespace, "policy-namespace", ownNamespace, "namespace of the GrantPolicies; the broker's leader-election Lease lives there too")
	fs.BoolVar(&o.leaderElect, "leader-elect", true, "elect one leader among the replicas; only the leader reconciles")
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "address of the Prometheus metrics endpoint; 0 turns it off")
	fs.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "address of /healthz and /readyz")
	fs.StringVar(&o.consoleAddr, "console-bind-address", "0", "address of the approval console behind the isolated Authentik forward-auth path; 0 turns it off (D-67)")
	fs.StringVar(&o.consoleOrigin, "console-origin", "", "exact external HTTPS origin of the approval console")
	fs.StringVar(&o.consoleOwner, "console-owner", "", "explicit Authentik username that may read and decide grants")
	fs.StringVar(&o.consoleReauthURL, "console-reauth-url", "", "fixed HTTPS URL of the dedicated fresh Authentik login flow")
	fs.StringVar(&o.consoleIssuer, "console-auth-issuer", "", "optional exact issuer constraint for the trusted upstream proxy token")
	fs.StringVar(&o.consoleAudience, "console-auth-audience", "", "optional audience constraint for the trusted upstream proxy token")
	fs.StringVar(&o.pushoverDir, "pushover-secret-dir", "", "mounted directory holding the Pushover token and user-key files")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if o.sessionNamespace == "" || o.policyNamespace == "" {
		return o, errors.New("--session-namespace and --policy-namespace must not be empty")
	}
	if o.consoleAddr == "" {
		return o, errors.New("--console-bind-address must not be empty; use 0 to turn it off")
	}
	if o.consoleAddr == "0" {
		if o.consoleOrigin != "" || o.consoleOwner != "" || o.consoleReauthURL != "" || o.consoleIssuer != "" || o.consoleAudience != "" || o.pushoverDir != "" {
			return o, errors.New("console options require --console-bind-address")
		}
		return o, nil
	}
	var err error
	if o.consoleOrigin, err = broker.ConsoleOrigin(o.consoleOrigin); err != nil {
		return o, err
	}
	if err := broker.ValidateReauthURL(o.consoleReauthURL); err != nil {
		return o, err
	}
	if _, err := broker.NewForwardAuthAuthenticator(o.consoleOwner, o.consoleIssuer, o.consoleAudience); err != nil {
		return o, err
	}
	if o.consoleIssuer != "" {
		u, err := url.Parse(o.consoleIssuer)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return o, errors.New("--console-auth-issuer must be an exact HTTPS issuer URL")
		}
	}
	if strings.TrimSpace(o.consoleAudience) != o.consoleAudience || len(o.consoleAudience) > 253 {
		return o, errors.New("--console-auth-audience must be a plain audience identifier")
	}
	if o.pushoverDir == "" {
		return o, errors.New("--pushover-secret-dir is required when the console is enabled")
	}
	return o, nil
}

// runBroker runs the access broker (DESIGN-001 6.12, D-61): the same binary,
// its own Deployment, ServiceAccount and Lease.
func runBroker(args []string) error {
	o, err := parseBrokerFlags(args)
	if err != nil {
		return err
	}
	ctrl.SetLogger(logr.FromSlogHandler(slog.NewJSONHandler(os.Stderr, nil)))
	log := ctrl.Log.WithName(broker.Name)
	log.Info("starting", "version", version.Get().String(binaryName),
		"sessionNamespace", o.sessionNamespace, "policyNamespace", o.policyNamespace)
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	installer, err := broker.NewExecInstaller(cfg)
	if err != nil {
		return err
	}
	var notifier broker.Notifier = broker.LogNotifier{Log: log.WithName("notify")}
	if o.consoleAddr != "0" {
		notifier, err = broker.NewPushoverNotifier(o.pushoverDir, o.consoleOrigin)
		if err != nil {
			return err
		}
	}
	mgr, b, err := broker.NewManager(cfg, broker.Options{
		SessionNamespace: o.sessionNamespace,
		PolicyNamespace:  o.policyNamespace,
		LeaderElect:      o.leaderElect,
		MetricsAddr:      o.metricsAddr,
		ProbeAddr:        o.probeAddr,
		Notifier:         notifier,
		Installer:        installer,
	})
	if err != nil {
		return err
	}
	if o.consoleAddr != "0" {
		auth, err := broker.NewForwardAuthAuthenticator(o.consoleOwner, o.consoleIssuer, o.consoleAudience)
		if err != nil {
			return err
		}
		console, err := broker.NewConsole(b, auth, o.consoleOrigin, o.consoleReauthURL)
		if err != nil {
			return err
		}
		runner := &broker.ConsoleRunner{Addr: o.consoleAddr, Handler: console.Handler()}
		if err := mgr.Add(runner); err != nil {
			return err
		}
		if err := mgr.AddReadyzCheck("console", runner.ReadyCheck); err != nil {
			return err
		}
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
