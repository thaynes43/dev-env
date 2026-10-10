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
// routes (plan 07 step 2, D-56). The broker mode (broker.go, internal/broker,
// D-61) decides, makes and revokes kube and break-glass grants (plan 07 step 3)
// and egress grants (step 5, D-64), whose expiry also has an operator backstop.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/activity"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/grantexpiry"
	"github.com/thaynes43/dev-env/internal/podexec"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskbudget"
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
		if err := runBroker(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%s broker: %v\n", binaryName, err)
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", binaryName, err)
		os.Exit(1)
	}
}

type options struct {
	nativeFixtureEnabled  bool
	nativeFixtureImage    string
	taskBudgetsEnabled    bool
	taskBudgetNamespace   string
	taskBudgetWorkerImage string
	assignedTaskBudgets   map[string]string
	sessionNamespace      string
	templatesNamespace    string
	templatesName         string
	leaderElect           bool
	metricsAddr           string
	probeAddr             string
	apiURL                string
	apiAddr               string
	apiTLSDir             string
	humanSA               string
	clientSAs             []string
	grantApprovalURL      string
	coordinatorEnabled    bool
	managedCodexTasks     bool
	managedChildDecisions bool
	coordinatorHosts      []apiserver.CoordinatorHost
	catalogBinding        *apiserver.CatalogBinding
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
	fs.BoolVar(&o.coordinatorEnabled, "enable-coordinator-callers", false, "enable configured live-bound scoped coordinator callers")
	fs.BoolVar(&o.managedCodexTasks, "enable-managed-codex-tasks", false, "enable accepted-project managed Codex task admission")
	fs.BoolVar(&o.managedChildDecisions, "enable-managed-child-decisions", false, "enable private recorded child decisions and same-writer native continuation")
	fs.BoolVar(&o.taskBudgetsEnabled, "enable-task-budgets", false, "enable retained finite managed task budget authority; native admission requires a separate trusted inspector")
	fs.BoolVar(&o.nativeFixtureEnabled, "enable-native-lifecycle-fixture", false, "admit only the fixed isolated zero-task native lifecycle fixture")
	fs.StringVar(&o.nativeFixtureImage, "native-fixture-image", "", "signature-verified immutable agent image for the isolated fixture")
	fs.StringVar(&o.taskBudgetNamespace, "task-budget-namespace", "", "dedicated protected namespace for retained budget ConfigMaps")
	fs.StringVar(&o.taskBudgetWorkerImage, "task-budget-worker-image", "", "reviewed immutable worker image containing pre-boot budget deadline")
	assignedBudgets := fs.String("assigned-task-budgets", "", "explicit JSON configured HostID to TaskUID map")
	hosts := fs.String("coordinator-hosts", "", "explicit JSON host bindings; empty configures none")
	catalog := fs.String("project-catalog", "", "explicit accepted namespace/name ConfigMap binding")
	cloneOwner := fs.String("project-clone-owner", "", "configured GitHub clone owner for accepted project tasks")
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
	policy := apiserver.Policy{Human: o.humanSA, Clients: o.clientSAs, SessionNamespace: o.sessionNamespace, SessionServiceAccount: controller.ServiceAccountName}
	var err error
	o.coordinatorHosts, err = apiserver.ParseCoordinatorHosts(*hosts, policy)
	if err != nil {
		return o, err
	}
	if *catalog != "" || *cloneOwner != "" {
		parts := strings.Split(*catalog, "/")
		if len(parts) != 2 {
			return o, errors.New("project-catalog must be the explicit namespace/name")
		}
		b := &apiserver.CatalogBinding{Key: types.NamespacedName{Namespace: parts[0], Name: parts[1]}, CloneOwner: *cloneOwner}
		if err := b.Validate(); err != nil {
			return o, err
		}
		o.catalogBinding = b
	}
	if (o.coordinatorEnabled || o.managedCodexTasks) && (o.apiAddr == "0" || o.catalogBinding == nil) {
		return o, errors.New("enabled task features require the API and a concrete accepted catalog binding")
	}
	if o.coordinatorEnabled && (!o.managedCodexTasks || len(o.coordinatorHosts) == 0) {
		return o, errors.New("coordinators require managed task support and explicit host bindings")
	}
	if o.managedChildDecisions && (!o.coordinatorEnabled || !o.managedCodexTasks) {
		return o, errors.New("managed child decisions require enabled configured coordinators and managed native tasks")
	}

	if *assignedBudgets != "" {
		if len(*assignedBudgets) > 4096 {
			return o, errors.New("task budget assignment exceeds bound")
		}
		d := json.NewDecoder(strings.NewReader(*assignedBudgets))
		if d.Decode(&o.assignedTaskBudgets) != nil || d.Decode(new(any)) != io.EOF || len(o.assignedTaskBudgets) > 32 {
			return o, errors.New("invalid task budget assignment")
		}
		for host, uid := range o.assignedTaskBudgets {
			known := false
			for _, h := range o.coordinatorHosts {
				if h.HostID == host {
					known = true
				}
			}
			if !known || !protocol.ValidTaskBudgetIdentifier(uid) {
				return o, errors.New("task budget assignment requires a known configured host and bounded task UID")
			}
		}
	}
	if o.taskBudgetsEnabled {
		if !o.coordinatorEnabled || !o.managedCodexTasks || len(o.assignedTaskBudgets) == 0 || o.taskBudgetNamespace == "" || o.taskBudgetNamespace == o.sessionNamespace || o.taskBudgetNamespace == o.templatesNamespace || !regexp.MustCompile(`^.+@sha256:[a-f0-9]{64}$`).MatchString(o.taskBudgetWorkerImage) {
			return o, errors.New("task budgets require configured coordinators, assignments, dedicated protected namespace and reviewed worker image digest")
		}
	} else if o.taskBudgetNamespace != "" || o.taskBudgetWorkerImage != "" || len(o.assignedTaskBudgets) > 0 {
		return o, errors.New("task budget configuration requires explicit enable-task-budgets")
	}
	if err := validateNativeFixtureOptions(o); err != nil {
		return o, err
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
	var decisionParents []string
	if o.coordinatorEnabled && o.managedChildDecisions {
		for _, host := range o.coordinatorHosts {
			decisionParents = append(decisionParents, host.ServiceAccount)
		}
	}
	var budgets *taskbudget.Service
	if o.taskBudgetsEnabled {
		budgets = &taskbudget.Service{Store: taskbudget.KubeStore{Client: mgr.GetClient(), Live: mgr.GetAPIReader(), Namespace: o.taskBudgetNamespace}, Validator: controller.ManagedBudgetEvidenceValidator{Reader: mgr.GetAPIReader()}, ManagedInspector: controller.ManagedPodAdmissionInspector{Reader: mgr.GetAPIReader(), Templates: templatesKey, Image: o.taskBudgetWorkerImage}}
		budgets.NativeInspector, err = newNativeFixtureInspector(mgr.GetAPIReader(), o.nativeFixtureImage, o.nativeFixtureEnabled)
		if err != nil {
			return err
		}
	}
	r := &controller.Reconciler{
		Client:      mgr.GetClient(),
		TaskBudgets: budgets, TaskBudgetWorkerImage: o.taskBudgetWorkerImage,
		Templates:                   templatesKey,
		APIURL:                      o.apiURL,
		ManagedCodexTasks:           o.managedCodexTasks,
		ManagedChildDecisions:       o.managedChildDecisions,
		ManagedChildDecisionParents: decisionParents,
		APIReader:                   mgr.GetAPIReader(),
		Rescuer:                     rescuer,
		WorkspaceStopper:            rescuer,
		Recorder:                    mgr.GetEventRecorder(binaryName),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return err
	}
	// Egress grants have no token expiry. The operator revokes their policies
	// at expiresAt even when the broker is unavailable (plan 07 step 5, D-64).
	expiry := &grantexpiry.Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), SessionNamespace: o.sessionNamespace,
	}
	if err := expiry.SetupWithManager(mgr); err != nil {
		return err
	}
	// The sessions' metrics, read from the cache at scrape time on every
	// replica: the RescueFailed page's source (D-57).
	ctrlmetrics.Registry.MustRegister(&controller.SessionCollector{Reader: mgr.GetCache(), Namespace: o.sessionNamespace})
	// Expired declare-activity declarations go within a minute (D-17, D-66).
	if err := mgr.Add(&activity.Reaper{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Namespace: o.templatesNamespace,
		Log: ctrl.Log.WithName("activities")}); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	// The API's log and message routes run agentd in a session's pod (D-65).
	podExec, err := podexec.New(cfg)
	if err != nil {
		return err
	}
	// The shelf pod holds the shared volume: rescues are listed, restored
	// from and pruned through it (D-67). The pruner runs in the leader.
	rescueShelf := &shelf.Shelf{Reader: mgr.GetAPIReader(), Exec: podExec, Namespace: o.sessionNamespace}
	if err := mgr.Add(&shelf.Pruner{Shelf: rescueShelf, Reader: mgr.GetAPIReader(), Namespace: o.sessionNamespace,
		Templates: apiserver.TemplatesFrom(mgr.GetClient(), templatesKey), Log: ctrl.Log.WithName("shelf")}); err != nil {
		return err
	}
	if o.apiAddr != "0" {
		api := &apiserver.Server{
			Client:      mgr.GetClient(),
			TaskBudgets: budgets, AssignedTaskBudgets: o.assignedTaskBudgets,
			Live: mgr.GetAPIReader(),
			Auth: apiserver.TokenReviewer{Client: mgr.GetClient()},
			Policy: apiserver.Policy{
				Human:                 o.humanSA,
				Clients:               o.clientSAs,
				SessionNamespace:      o.sessionNamespace,
				SessionServiceAccount: controller.ServiceAccountName,
				CoordinatorEnabled:    o.coordinatorEnabled, Coordinators: o.coordinatorHosts,
				// declare-activity's declarations live beside the operator
				// (D-66).
				ActivityNamespace: o.templatesNamespace,
			},
			Projects: o.catalogBinding, ManagedCodexTasks: o.managedCodexTasks, ManagedChildDecisions: o.managedChildDecisions,
			Exec:             podExec,
			Shelf:            rescueShelf,
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
