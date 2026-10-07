package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/version"
)

// brokerOptions are the flags of `dev-env-operator broker` (D-58).
type brokerOptions struct {
	sessionNamespace string
	policyNamespace  string
	leaderElect      bool
	metricsAddr      string
	probeAddr        string
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
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if o.sessionNamespace == "" || o.policyNamespace == "" {
		return o, errors.New("--session-namespace and --policy-namespace must not be empty")
	}
	return o, nil
}

// runBroker runs the access broker (DESIGN-001 6.12, D-58): the same binary,
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
	mgr, _, err := broker.NewManager(cfg, broker.Options{
		SessionNamespace: o.sessionNamespace,
		PolicyNamespace:  o.policyNamespace,
		LeaderElect:      o.leaderElect,
		MetricsAddr:      o.metricsAddr,
		ProbeAddr:        o.probeAddr,
		// Pushover (plan 07 step 6) and the token install (step 4) plug in
		// here; until then the broker logs that Tom is wanted and installs
		// nothing.
		Notifier: broker.LogNotifier{Log: log.WithName("notify")},
	})
	if err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
