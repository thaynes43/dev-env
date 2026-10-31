package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/grantattest"
	"github.com/thaynes43/dev-env/internal/templates"
	"github.com/thaynes43/dev-env/internal/version"
)

// brokerOptions are the flags of `dev-env-operator broker` (D-61).
type brokerOptions struct {
	sessionNamespace           string
	enableAdminLeases          bool
	adminIssuerNamespace       string
	policyNamespace            string
	leaderElect                bool
	enableProxmoxGrants        bool
	enableHWSSHGrants          bool
	enableAttested             bool
	enableMaintenance          bool
	enableAttestedMaint        bool
	maintenanceHuman           string
	reservedDecisionClasses    []string
	reservedExcludedNamespaces []string
	grantDelegates             []string
	grantPolicyDelegates       []string
	legacyGrantDelegates       []broker.LegacyDelegateBinding
	metricsAddr                string
	probeAddr                  string
	attestedCreators           []string
}

func parseBrokerFlags(args []string) (brokerOptions, error) {
	// The broker's own namespace, from the downward API in its Deployment.
	ownNamespace := os.Getenv("POD_NAMESPACE")
	if ownNamespace == "" {
		ownNamespace = "dev-env-system"
	}
	var o brokerOptions
	// The issuer is deliberately not derived from SessionNamespace.
	fs := flag.NewFlagSet(binaryName+" broker", flag.ContinueOnError)
	fs.StringVar(&o.sessionNamespace, "session-namespace", "dev-agents", "namespace of the AgentSessions, their pods, the AccessGrants and the grant ServiceAccounts (D-54)")
	fs.BoolVar(&o.enableAdminLeases, "enable-admin-leases", false, "HELD: reusable reserved RD2 admin issuance; requires the once-approved dedicated namespace and security bundle")
	fs.StringVar(&o.adminIssuerNamespace, "admin-issuer-namespace", "", "HELD: dedicated admin credential namespace; separate from session-namespace, default unconfigured")
	fs.StringVar(&o.policyNamespace, "policy-namespace", ownNamespace, "namespace of the GrantPolicies; the broker's leader-election Lease lives there too")
	fs.BoolVar(&o.leaderElect, "leader-elect", true, "elect one leader among the replicas; only the leader reconciles")
	fs.BoolVar(&o.enableProxmoxGrants, "enable-proxmox-grants", false, "enable keeper-backed Proxmox grants after node trust and acceptance are configured")
	fs.BoolVar(&o.enableHWSSHGrants, "enable-hw-ssh-grants", false, "enable hw-ssh credential grants (D-89): the keeper leases the static shared hw-ssh key into the session pod. Removing the lease is not revocation: the key stays valid on every host")
	fs.BoolVar(&o.enableAttested, "enable-attested-grants", false, "approve a request through a GrantPolicy with spec.attested when its requesting session recorded an agent attestation (a claim that the user or a coordinator approved; not verified human identity). Off, attested policies approve nothing")
	fs.StringVar(&o.maintenanceHuman, "maintenance-human-service-account", "dev-env-system/dev-env-human", "must match operator --human-service-account for authenticated maintenance decisions")
	fs.BoolVar(&o.enableMaintenance, "enable-maintenance-grants", false, "HELD: enable isolated bounded maintenance leases after policy review")
	fs.BoolVar(&o.enableAttestedMaint, "enable-attested-maintenance", false, "HELD (ADR-013): also approve a maintenance grant through an attested GrantPolicy whose maintenance scope matches its envelope exactly, when its requesting session recorded an agent attestation (a claim, not verified human identity). Needs --enable-attested-grants and --enable-maintenance-grants; the human decision route is checked first and is unchanged. Off, maintenance is approved by the human route only, or by a listed --grant-delegates delegate through a GrantPolicy with spec.delegated")
	reservedClasses := fs.String("reserved-decision-classes", "", "HELD SOURCE ONLY: explicit S7 classes rd2,rd3a,rd3b,rd4-storage,rd4-hardware; empty disables reserved decisions and hardware audit. Must match operator/broker; Tom message references are agent assertions, never verified human consent")
	reservedExcluded := fs.String("reserved-excluded-namespaces", strings.Join(grantattest.ReservedDefaultExclusions(), ","), "HELD: exact reviewed namespace exclusion bounds for S7 namespaced cases; proposed defaults are not permanent human-only restrictions. Explicit empty changes the bound; no implicit fallback. Must match operator/broker and requires the held activation review")
	delegates := fs.String("grant-delegates", "", "HELD (DESIGN-042 6.2): comma-separated ServiceAccounts, <namespace>/<name>, whose recorded delegate decisions the broker applies through a GrantPolicy with spec.delegated (approvedBy delegate/<namespace>/<name>; not a human decision). Must match the operator's --grant-delegates. Empty applies none")
	policyDelegates := fs.String("grant-policy-delegates", "", "HELD (DESIGN-042 6.6): default-empty subset of --grant-delegates allowed exact live delegated-policy reach outside their tree. Must match both services")
	legacyDelegates := fs.String("grant-legacy-delegates", "", "HELD: exact existing baseline Claude actors ns/sa:saUID:session:sessionUID:pod:podUID, at most four; requires both delegate lists. Default empty")
	fs.StringVar(&o.metricsAddr, "metrics-bind-address", ":8080", "address of the Prometheus metrics endpoint; 0 turns it off")
	fs.StringVar(&o.probeAddr, "health-probe-bind-address", ":8081", "address of /healthz and /readyz")
	creators := fs.String("attested-maintenance-creators", "", "HELD (DESIGN-042 S6): comma-separated ServiceAccounts, <namespace>/<name>, at most 16. With --enable-attested-maintenance, only a top-level session whose spec.parent is exactly one of them (depth 0) may approve maintenance for itself; a child session never does. Unset: the --maintenance-human-service-account value. Set empty: no creator, so no session approves maintenance for itself")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	// DESIGN-042 S6. Set, the creators are exactly the list, and empty is none.
	// Unset, they are the human ServiceAccount, or none when that is not one
	// namespace/name: startup is unchanged either way.
	creatorsSet := false
	fs.Visit(func(f *flag.Flag) { creatorsSet = creatorsSet || f.Name == "attested-maintenance-creators" })
	if creatorsSet {
		c, cerr := broker.ParseAttestedMaintenanceCreators(*creators)
		if cerr != nil {
			return o, cerr
		}
		o.attestedCreators = c
	} else if c, cerr := broker.ParseAttestedMaintenanceCreators(o.maintenanceHuman); cerr == nil && len(c) == 1 {
		o.attestedCreators = c
	}
	if o.sessionNamespace == "" || o.policyNamespace == "" {
		return o, errors.New("--session-namespace and --policy-namespace must not be empty")
	}
	var err error
	o.legacyGrantDelegates, err = broker.ParseLegacyGrantDelegates(*legacyDelegates, o.sessionNamespace, controller.ServiceAccountName)
	if err != nil {
		return o, err
	}
	refused := []string{o.maintenanceHuman, o.sessionNamespace + "/" + templates.WorkbenchServiceAccount, "dev-agents/dev-env-v2-portal"}
	if len(o.legacyGrantDelegates) == 0 {
		refused = append(refused, o.sessionNamespace+"/"+controller.ServiceAccountName)
	}
	o.reservedDecisionClasses, err = grantattest.ParseReservedClasses(*reservedClasses)
	if err != nil {
		return o, err
	}
	o.reservedExcludedNamespaces, err = grantattest.ParseReservedExclusions(*reservedExcluded)
	if err != nil {
		return o, err
	}
	o.grantDelegates, err = broker.ParseGrantDelegates(*delegates, refused...)
	if err != nil {
		return o, err
	}
	if o.grantPolicyDelegates, err = broker.ParseGrantPolicyDelegates(*policyDelegates, o.grantDelegates); err != nil {
		return o, err
	}
	if err = broker.ValidateLegacyDelegateLists(o.legacyGrantDelegates, o.grantDelegates, o.grantPolicyDelegates); err != nil {
		return o, err
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
	mgr, _, err := broker.NewManager(cfg, broker.Options{
		SessionNamespace:               o.sessionNamespace,
		EnableAdminLeases:              o.enableAdminLeases,
		AdminIssuerNamespace:           o.adminIssuerNamespace,
		PolicyNamespace:                o.policyNamespace,
		EnableProxmoxGrants:            o.enableProxmoxGrants,
		EnableHWSSHGrants:              o.enableHWSSHGrants,
		EnableAttestedGrants:           o.enableAttested,
		AttestedMaintenanceCreators:    o.attestedCreators,
		EnableMaintenanceGrants:        o.enableMaintenance,
		EnableAttestedMaintenance:      o.enableAttestedMaint,
		MaintenanceHumanServiceAccount: o.maintenanceHuman,
		GrantDelegates:                 o.grantDelegates,
		ReservedDecisionClasses:        o.reservedDecisionClasses,
		ReservedExcludedNamespaces:     o.reservedExcludedNamespaces,
		GrantPolicyDelegates:           o.grantPolicyDelegates,
		LegacyGrantDelegates:           o.legacyGrantDelegates,
		LeaderElect:                    o.leaderElect,
		MetricsAddr:                    o.metricsAddr,
		ProbeAddr:                      o.probeAddr,
		// Pushover plugs in at plan 07 step 6; until then the broker logs
		// that Tom is wanted.
		Notifier:  broker.LogNotifier{Log: log.WithName("notify")},
		Installer: installer,
	})
	if err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}
