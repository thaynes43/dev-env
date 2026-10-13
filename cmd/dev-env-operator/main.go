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
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/activity"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/contextpublication"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/grantexpiry"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/podexec"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/taskclaim"
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
	directoryEnabled              bool
	directoryProjects             []string
	metadataDiscoveryProjects     []string
	retainedCommonRules           bool
	contextsEnabled               bool
	contextReadProjects           []string
	inboxEnabled                  bool
	inboxProjects                 []string
	inboxCoordinatorReadProjects  []string
	taskClaimsEnabled             bool
	taskClaimReadProjects         []string
	nativeFixtureEnabled          bool
	nativeFixtureImage            string
	taskBudgetsEnabled            bool
	taskBudgetNamespace           string
	taskBudgetWorkerImage         string
	assignedTaskBudgets           map[string]string
	sessionNamespace              string
	templatesNamespace            string
	templatesName                 string
	leaderElect                   bool
	metricsAddr                   string
	probeAddr                     string
	apiURL                        string
	apiAddr                       string
	apiTLSDir                     string
	humanSA                       string
	clientSAs                     []string
	grantApprovalURL              string
	coordinatorEnabled            bool
	privateProjectTasks           bool
	managedCodexTasks             bool
	privateHistoryRetention       bool
	privateTerminalRecord         bool
	privateExecutorStop           bool
	homeHistoryArchive            bool
	homeExpiryGrace               time.Duration
	privateProjectCanariesEnabled bool
	privateProjectCanaries        *projectcanary.Policy
	managedChildDecisions         bool
	coordinatorHosts              []apiserver.CoordinatorHost
	catalogBinding                *apiserver.CatalogBinding
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
	fs.BoolVar(&o.privateProjectTasks, "enable-private-project-tasks", false, "enable new accepted-project tasks with private repositories and worktrees")
	fs.BoolVar(&o.managedCodexTasks, "enable-managed-codex-tasks", false, "enable accepted-project managed Codex task admission")
	fs.BoolVar(&o.privateHistoryRetention, "enable-private-history-retention", false, "retain private managed HOME and durable Session identity on reap and suspended autoarchive")
	fs.BoolVar(&o.privateTerminalRecord, "enable-private-terminal-record", false, "record the durable terminal observation of a private executor, and the missed marker, in its Session's status (DESIGN-025 3); records only, deletes nothing")
	fs.BoolVar(&o.privateExecutorStop, "enable-private-executor-stop", false, "stop the recorded executor of a Reaping private Session with activeDeadlineSeconds, after a confirmed stop intent; an owner hold blocks it (DESIGN-025 4); needs --enable-private-terminal-record")
	fs.BoolVar(&o.homeHistoryArchive, "enable-home-history-archive", false, "run the HOME expiry of private Sessions up to Archived: after the grace, rescue in a hold pod bound to the HOME, export the provider history into the shelf's history archive and verify it; deletes no HOME (DESIGN-025 5); needs --enable-private-terminal-record")
	fs.DurationVar(&o.homeExpiryGrace, "home-expiry-grace", controller.DefaultHomeExpiryGrace, "the HOME expiry's grace after the recorded stop; it may only lengthen Q-31's 720h (DESIGN-025 13)")
	fs.BoolVar(&o.retainedCommonRules, "enable-retained-common-rules", false, "retain reviewed common instruction inputs for new fixed private managed canaries")
	fs.BoolVar(&o.privateProjectCanariesEnabled, "enable-private-project-canaries", false, "enable only two explicitly configured private managed subscription rule canaries")
	canaries := fs.String("private-project-canaries", "", "strict operator-owned JSON records for at most two fixed private managed canaries")
	fs.BoolVar(&o.managedChildDecisions, "enable-managed-child-decisions", false, "enable private recorded child decisions and same-writer native continuation")
	fs.BoolVar(&o.taskBudgetsEnabled, "enable-task-budgets", false, "enable retained finite managed task budget authority; native admission requires a separate trusted inspector")
	fs.BoolVar(&o.taskClaimsEnabled, "enable-task-claims", false, "enable immutable explicit TaskClaim ownership in the separate coordination API group")
	claimProjects := fs.String("task-claims-read-projects", "", "explicit comma-separated projects for bounded TaskClaim metadata discovery")
	fs.BoolVar(&o.contextsEnabled, "enable-published-contexts", false, "enable immutable explicitly published context in the protected coordination API group")
	fs.BoolVar(&o.directoryEnabled, "enable-session-directory", false, "enable prospective immutable managed-canary session metadata directory")
	directoryProjects := fs.String("session-directory-projects", "", "explicit prospective directory enrollment projects")
	discoveryProjects := fs.String("metadata-discovery-projects", "", "separate explicit projects granting directory and retained context metadata discovery")
	contextProjects := fs.String("context-payload-read-projects", "", "explicit comma-separated projects whose sessions may publish context and whose published context payloads may be read")
	fs.BoolVar(&o.inboxEnabled, "enable-durable-inbox", false, "enable immutable API inbox messages and explicit read acknowledgements")
	inboxProjects := fs.String("inbox-projects", "", "explicit comma-separated projects enabled for durable Session inbox access")
	inboxCoordinatorProjects := fs.String("inbox-coordinator-read-projects", "", "separate explicit projects granting configured coordinators inbox content read only")
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
	if (o.coordinatorEnabled || o.managedCodexTasks || o.privateProjectTasks) && (o.apiAddr == "0" || o.catalogBinding == nil) {
		return o, errors.New("enabled task features require the API and a concrete accepted catalog binding")
	}
	if o.coordinatorEnabled && (!o.managedCodexTasks || len(o.coordinatorHosts) == 0) {
		return o, errors.New("coordinators require managed task support and explicit host bindings")
	}
	o.privateProjectCanaries, err = projectcanary.Parse(*canaries)
	if err != nil {
		return o, err
	}
	if o.retainedCommonRules && !o.privateProjectCanariesEnabled {
		return o, errors.New("retained common rules require the enabled fixed private canary route")
	}
	if o.privateProjectCanariesEnabled {
		if o.privateProjectCanaries.ValidateCommon(o.retainedCommonRules) != nil {
			return o, errors.New("common rule policy revisions do not match the enabled route")
		}
		if o.apiAddr == "0" || o.apiURL == "" || o.catalogBinding == nil || !o.privateHistoryRetention || o.privateProjectTasks || o.privateProjectCanaries.ValidateClients(o.clientSAs, o.managedCodexTasks, o.humanSA, o.sessionNamespace+"/"+controller.ServiceAccountName) != nil {
			return o, errors.New("private canaries require enabled API, fixed catalog, retention, exact client policy and disabled general private admission")
		}
		apiURL, err := url.Parse(o.apiURL)
		if err != nil || apiURL.Scheme != "https" || apiURL.Host == "" || apiURL.RawQuery != "" || apiURL.Fragment != "" {
			return o, errors.New("private canaries require a concrete HTTPS operator API URL")
		}
	} else {
		o.privateProjectCanaries = nil
	}
	if o.managedChildDecisions && (!o.coordinatorEnabled || !o.managedCodexTasks) {
		return o, errors.New("managed child decisions require enabled configured coordinators and managed native tasks")
	}
	o.taskClaimReadProjects, err = apiserver.ParseTaskClaimReadProjects(*claimProjects)
	if err != nil {
		return o, err
	}
	if o.taskClaimsEnabled {
		if o.apiAddr == "0" || o.catalogBinding == nil || len(o.taskClaimReadProjects) == 0 || len(validation.IsDNS1123Label(o.templatesNamespace)) != 0 {
			return o, errors.New("task claims require the API, accepted catalog and explicit project metadata read scope")
		}
	} else if len(o.taskClaimReadProjects) != 0 {
		return o, errors.New("task claim read scope requires explicit enable-task-claims")
	}

	o.contextReadProjects, err = apiserver.ParseContextReadProjects(*contextProjects)
	if err != nil {
		return o, err
	}
	if o.contextsEnabled {
		if o.apiAddr == "0" || o.catalogBinding == nil || len(o.contextReadProjects) == 0 || len(validation.IsDNS1123Label(o.templatesNamespace)) != 0 {
			return o, errors.New("published context requires the API, accepted catalog and explicit payload read scope")
		}
	} else if len(o.contextReadProjects) != 0 {
		return o, errors.New("context payload scope requires explicit enable-published-contexts")
	}

	o.directoryProjects, err = apiserver.ParseTaskClaimReadProjects(*directoryProjects)
	if err != nil {
		return o, err
	}
	o.metadataDiscoveryProjects, err = apiserver.ParseTaskClaimReadProjects(*discoveryProjects)
	if err != nil {
		return o, err
	}
	if o.directoryEnabled {
		if o.apiAddr == "0" || o.catalogBinding == nil || o.privateProjectCanaries == nil || !o.privateHistoryRetention || len(o.directoryProjects) == 0 || len(o.metadataDiscoveryProjects) == 0 || len(validation.IsDNS1123Label(o.templatesNamespace)) != 0 {
			return o, errors.New("session directory requires API, accepted catalog, private managed canaries, retention and explicit enrollment/discovery projects")
		}
		for _, project := range o.directoryProjects {
			if !slices.Contains(o.metadataDiscoveryProjects, project) {
				return o, errors.New("directory enrollment project requires explicit metadata discovery scope")
			}
		}
	} else if len(o.directoryProjects) != 0 || len(o.metadataDiscoveryProjects) != 0 {
		return o, errors.New("directory scopes require enable-session-directory")
	}
	o.inboxProjects, err = apiserver.ParseTaskClaimReadProjects(*inboxProjects)
	if err != nil {
		return o, err
	}
	o.inboxCoordinatorReadProjects, err = apiserver.ParseTaskClaimReadProjects(*inboxCoordinatorProjects)
	if err != nil {
		return o, err
	}
	if o.inboxEnabled {
		if o.apiAddr == "0" || o.catalogBinding == nil || len(o.inboxProjects) == 0 || len(validation.IsDNS1123Label(o.templatesNamespace)) != 0 {
			return o, errors.New("durable inbox requires API, accepted catalog and explicit project content scope")
		}
		for _, project := range o.inboxCoordinatorReadProjects {
			if !slices.Contains(o.inboxProjects, project) || !o.coordinatorEnabled {
				return o, errors.New("inbox coordinator content scope requires an enabled configured coordinator and enabled project")
			}
		}
	} else if len(o.inboxProjects) != 0 || len(o.inboxCoordinatorReadProjects) != 0 {
		return o, errors.New("inbox scopes require explicit enable-durable-inbox")
	}

	if o.privateProjectCanaries.OutputCaptureEnabled() && !o.contextsEnabled {
		return o, errors.New("managed output requires enabled protected context publication")
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
	if o.privateExecutorStop && !o.privateTerminalRecord {
		return o, errors.New("--enable-private-executor-stop requires --enable-private-terminal-record: the terminal record is what proves the stop (DESIGN-025 13)")
	}
	if o.homeHistoryArchive && !o.privateTerminalRecord {
		return o, errors.New("--enable-home-history-archive requires --enable-private-terminal-record: the HOME expiry rests on the terminal record (DESIGN-025 13)")
	}
	if o.homeExpiryGrace < controller.DefaultHomeExpiryGrace {
		return o, fmt.Errorf("--home-expiry-grace %s is under %s: the grace may only be lengthened (DESIGN-025 13)", o.homeExpiryGrace, controller.DefaultHomeExpiryGrace)
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
	if err := coord.AddToScheme(scheme); err != nil {
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
	cloneOwner := ""
	if o.catalogBinding != nil {
		cloneOwner = o.catalogBinding.CloneOwner
	}
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
	directoryStore := &sessiondirectory.KubeStore{Client: mgr.GetClient(), Live: mgr.GetAPIReader(), Namespace: o.templatesNamespace}
	// Registered whatever the flag says, so the series exist at 0 (DESIGN-025 10).
	terminalCounter := controller.NewPrivateExecutorTerminalCounter()
	ctrlmetrics.Registry.MustRegister(terminalCounter)
	stopCounter := controller.NewPrivateExecutorStopCounter()
	ctrlmetrics.Registry.MustRegister(stopCounter)
	expiryCounters := controller.NewHomeExpiryCounters()
	ctrlmetrics.Registry.MustRegister(expiryCounters.Collectors()...)
	// The API's log and message routes run agentd in a session's pod (D-65),
	// and the HOME expiry's relay runs the history export there and the
	// archive commands in the shelf (DESIGN-025 6.4).
	podExec, err := podexec.New(cfg)
	if err != nil {
		return err
	}
	// The shelf pod holds the shared volume: rescues are listed, restored
	// from and pruned through it (D-67), and history archived through it.
	rescueShelf := &shelf.Shelf{Reader: mgr.GetAPIReader(), Exec: podExec, Namespace: o.sessionNamespace}
	r := &controller.Reconciler{
		SessionDirectory: directoryStore,
		Client:           mgr.GetClient(),
		TaskBudgets:      budgets, TaskBudgetWorkerImage: o.taskBudgetWorkerImage,
		Templates:                   templatesKey,
		APIURL:                      o.apiURL,
		ManagedCodexTasks:           o.managedCodexTasks,
		RetainedCommonRules:         o.retainedCommonRules,
		PrivateHistoryRetention:     o.privateHistoryRetention,
		PrivateTerminalRecord:       o.privateTerminalRecord,
		PrivateExecutorTerminal:     terminalCounter,
		PrivateExecutorStop:         o.privateExecutorStop,
		PrivateExecutorStops:        stopCounter,
		HomeHistoryArchive:          o.homeHistoryArchive,
		HomeExpiryGrace:             o.homeExpiryGrace,
		HomeExpiryCounters:          expiryCounters,
		HistoryRelay:                &controller.ExecHistoryRelay{Exec: podExec, Shelf: rescueShelf, Slots: shelf.NewExecSlots(shelf.DefaultExecSlots)},
		PrivateProjectCanaries:      o.privateProjectCanaries,
		PrivateProjectCloneOwner:    cloneOwner,
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
	// The HOME expiry's gauges, read from the same cache at scrape time
	// (DESIGN-025 10).
	ctrlmetrics.Registry.MustRegister(&controller.HomeExpiryCollector{Reader: mgr.GetCache(), Namespace: o.sessionNamespace})
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
	// The shelf's pruner runs in the leader (D-67).
	if err := mgr.Add(&shelf.Pruner{Shelf: rescueShelf, Reader: mgr.GetAPIReader(), Namespace: o.sessionNamespace,
		Templates: apiserver.TemplatesFrom(mgr.GetClient(), templatesKey), Log: ctrl.Log.WithName("shelf")}); err != nil {
		return err
	}
	if o.apiAddr != "0" {
		var claims *taskclaim.KubeStore
		if o.taskClaimsEnabled {
			claims = &taskclaim.KubeStore{Client: mgr.GetClient(), Live: mgr.GetAPIReader(), Namespace: o.templatesNamespace}
		}
		var contexts *contextpublication.KubeStore
		if o.contextsEnabled {
			contexts = &contextpublication.KubeStore{Client: mgr.GetClient(), Live: mgr.GetAPIReader(), Namespace: o.templatesNamespace}
		}
		var inboxStore *inbox.KubeStore
		if o.inboxEnabled {
			inboxStore = &inbox.KubeStore{Client: mgr.GetClient(), Live: mgr.GetAPIReader(), Namespace: o.templatesNamespace}
		}
		// Coordination records are never deleted, so their bounds are lifetime
		// bounds; the counts are exported beside them (#175 review).
		var recordKinds []taskclaim.RecordKind
		if claims != nil {
			recordKinds = append(recordKinds, taskclaim.RecordKind{Kind: "TaskClaim", Limits: claims.Limits.Effective(taskclaim.DefaultLimits)})
		}
		if contexts != nil {
			recordKinds = append(recordKinds, taskclaim.RecordKind{Kind: "PublishedContext", Limits: contexts.Limits.Effective(contextpublication.DefaultLimits)})
		}
		if inboxStore != nil {
			recordKinds = append(recordKinds, taskclaim.RecordKind{Kind: "InboxMessage", Limits: inboxStore.Limits.Effective(inbox.DefaultLimits)})
		}
		if len(recordKinds) > 0 {
			ctrlmetrics.Registry.MustRegister(&taskclaim.RecordCollector{Reader: mgr.GetAPIReader(), Namespace: o.templatesNamespace, Kinds: recordKinds})
		}
		var directories *sessiondirectory.KubeStore
		var directoryContexts *contextpublication.KubeStore
		if o.directoryEnabled {
			directories = directoryStore
			directoryContexts = &contextpublication.KubeStore{Client: mgr.GetClient(), Live: mgr.GetAPIReader(), Namespace: o.templatesNamespace}
		}
		api := &apiserver.Server{
			SessionDirectory: directories, SessionDirectoryProjects: o.directoryProjects, MetadataDiscoveryProjects: o.metadataDiscoveryProjects, DirectoryContexts: directoryContexts,
			PublishedContexts: contexts, ContextReadProjects: o.contextReadProjects,
			Inbox: inboxStore, InboxProjects: o.inboxProjects, InboxCoordinatorReadProjects: o.inboxCoordinatorReadProjects,
			TaskClaims: claims, TaskClaimReadProjects: o.taskClaimReadProjects,
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
			Projects: o.catalogBinding, PrivateProjectTasks: o.privateProjectTasks, ManagedCodexTasks: o.managedCodexTasks, ManagedChildDecisions: o.managedChildDecisions,
			PrivateProjectCanaries: o.privateProjectCanaries, RetainedCommonRules: o.retainedCommonRules,
			PrivateHistoryRetention: o.privateHistoryRetention, PrivateCanaryTemplates: templatesKey,
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
