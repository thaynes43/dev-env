package keeper

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/asks"
	"github.com/thaynes43/dev-env/internal/reminders"
)

// LeaderElectionID is the keeper's Lease, in its own namespace. The keeper runs
// one replica with the Recreate strategy; the Lease keeps a second process (a pod
// on a partitioned node that has not stopped yet, a stray scale) from starting to
// refresh too. It is not a fence (see the package doc, D-52).
const LeaderElectionID = "dev-env-keeper.dev-env.haynesops.com"

// Options configure Run.
type Options struct {
	// Namespace is the keeper's own namespace, where its Lease lives.
	Namespace string
	// GHTokenSecret is the Secret the gh token goes into (D-13).
	GHTokenSecret types.NamespacedName
	// GitHubApp mints the gh token.
	GitHubApp *GitHubApp
	// OpsGHTokenSecret and OpsGitHubApp mint the haynes-ops-bot token
	// (DESIGN-064 6.2). A nil OpsGitHubApp leaves the Jobs as the dev bot
	// token alone, which is the command's default.
	OpsGHTokenSecret types.NamespacedName
	OpsGitHubApp     *GitHubApp
	// ProxmoxGrants is disabled by default; it does not enable the hw-ssh lease.
	ProxmoxGrants ProxmoxGrantOptions
	// HWSSHGrants leases the static hw-ssh key into a session pod (D-89); off by
	// default. Removing a lease is not revocation of the key.
	HWSSHGrants HWSSHGrantOptions
	CodexAuth   CodexOptions
	// ClaudeAuth keeps the shared Claude login fresh; off by default.
	ClaudeAuth ClaudeOptions
	// Notices configures the login gauges' notifier (plan 13, story 13.1).
	// Zero values take the defaults; the notifier runs only while its Pushover
	// credential files exist. Disabled is a bad notice flag: the command logs
	// it and Run does not start the notifier, so the validation error cannot
	// stop the credential keeper.
	Notices NoticeOptions
	// Reminders configures the reminder scheduler (DESIGN-032); off by default.
	Reminders RemindersOptions
	// AskNotifications is source-only/default off (DESIGN-034 lane B).
	AskNotifications AskNotificationOptions
	// Interval is the longest wait between two refreshes; DefaultInterval when
	// zero.
	Interval time.Duration

	LeaderElect bool
	// LeaseDuration, RenewDeadline and RetryPeriod tune leader election; the
	// zero values keep controller-runtime's 15 s, 10 s and 2 s.
	LeaseDuration, RenewDeadline, RetryPeriod time.Duration
	// MetricsAddr and ProbeAddr bind the metrics and the /healthz and /readyz
	// endpoints; "0" turns either off.
	MetricsAddr, ProbeAddr string

	Log logr.Logger
	// Clock is the real clock when nil. Tests only.
	Clock clock.Clock
}

// keeperJobs are the Keeper's Jobs: only credentials that are safe to mint
// again. The haynes-dev-bot installation token always is one. The haynes-ops-bot
// installation token is one only when OpsGitHubApp is set, which the command
// does for --enable-ops-gh-token (default off). A rotating login is never a
// Job, whatever else o enables; Run adds it as its own journaled, fenced
// runnable (codexWorker), as the package doc says.
func keeperJobs(o Options) []Job {
	jobs := []Job{GitHubTokenJob("gh-token", o.GitHubApp, o.GHTokenSecret)}
	if o.OpsGitHubApp == nil {
		return jobs
	}
	// Every ops mint refuses the dev bot's issuer, an issuer pair of different
	// forms, the dev bot's signing key, and the dev bot's installation. A
	// symlink of the dev directory has a different path, so validateOps
	// cannot see it.
	o.OpsGitHubApp.DistinctFrom = o.GitHubApp
	return append(jobs, GitHubTokenJob("ops-gh-token", o.OpsGitHubApp, o.OpsGHTokenSecret))
}

// validateOps refuses an ops App that would mint a token for every repository
// the installation can access, or write it over the dev bot's Secret.
func validateOps(o Options) error {
	if o.OpsGitHubApp == nil {
		return nil
	}
	if o.OpsGHTokenSecret.Namespace == "" || o.OpsGHTokenSecret.Name == "" || o.OpsGHTokenSecret == o.GHTokenSecret {
		return errors.New("keeper: the ops token Secret must be set and must not be the dev bot's Secret")
	}
	// Clean drops a trailing slash and dot segments. It does not resolve a
	// symlink; Mint compares issuers for that.
	if o.OpsGitHubApp.Dir == "" || o.GitHubApp == nil || filepath.Clean(o.OpsGitHubApp.Dir) == filepath.Clean(o.GitHubApp.Dir) {
		return errors.New("keeper: the ops App directory must be set and must not be the dev bot's directory")
	}
	if len(o.OpsGitHubApp.Permissions) == 0 || len(o.OpsGitHubApp.Repositories) == 0 {
		return errors.New("keeper: the ops App needs a permission set and a repository list")
	}
	return nil
}

// noticesStart reports whether Run adds the notifier. Disabled notice flags
// are logged by the command and must not reach NewNotifier: that returns the
// validation error and would stop the credential keeper.
func noticesStart(providerCount int, notices NoticeOptions) bool {
	return providerCount > 0 && !notices.Disabled
}

// Run runs the keeper until ctx ends or this process loses the leader election,
// which returns an error so the process exits and the kubelet restarts it.
func Run(ctx context.Context, cfg *rest.Config, o Options) error {
	o.AskNotifications = o.AskNotifications.DisableInvalid(o.Notices, o.LeaderElect, o.Log)
	// Direct callers need the same non-fatal handling as the command. An
	// invalid shared Pushover URL must not stop login workers or token minting.
	if !o.Notices.Disabled && o.Notices.Validate() != nil {
		o.Notices.Disabled = true
		o.Log.Error(nil, "notices disabled: invalid configuration")
	}
	if o.Namespace == "" || o.GHTokenSecret.Namespace == "" || o.GHTokenSecret.Name == "" || o.GitHubApp == nil {
		return errors.New("keeper: the namespace, the gh token Secret and the GitHub App are required")
	}
	if err := validateOps(o); err != nil {
		return err
	}
	if o.ClaudeAuth.Enabled && (o.Namespace == o.GHTokenSecret.Namespace || (o.ClaudeAuth.LoginDir != "" && !filepath.IsAbs(o.ClaudeAuth.LoginDir))) {
		return errors.New("claude auth requires separate private/public namespaces and an absolute login directory")
	}
	if o.CodexAuth.Enabled && (o.Namespace == o.GHTokenSecret.Namespace || (o.CodexAuth.LoginDir != "" && !filepath.IsAbs(o.CodexAuth.LoginDir))) {
		return errors.New("codex auth requires separate private/public namespaces and absolute private staging")
	}
	scheme := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme), coord.AddToScheme(scheme)); err != nil {
		return err
	}
	// An uncached client: the keeper only patches Secrets it is named on, and
	// its Role grants no list or watch (DESIGN-001 6.11).
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	mgrOpts := ctrl.Options{
		Scheme:                        scheme,
		Logger:                        o.Log,
		Metrics:                       metricsserver.Options{BindAddress: o.MetricsAddr},
		HealthProbeBindAddress:        o.ProbeAddr,
		LeaderElection:                o.LeaderElect,
		LeaderElectionID:              LeaderElectionID,
		LeaderElectionNamespace:       o.Namespace,
		LeaderElectionReleaseOnCancel: true,
	}
	if o.LeaseDuration > 0 {
		mgrOpts.LeaseDuration = &o.LeaseDuration
	}
	if o.RenewDeadline > 0 {
		mgrOpts.RenewDeadline = &o.RenewDeadline
	}
	if o.RetryPeriod > 0 {
		mgrOpts.RetryPeriod = &o.RetryPeriod
	}
	var credentialFence *LeaseFence
	if o.ProxmoxGrants.Enabled && !o.LeaderElect {
		return errors.New("proxmox grants require keeper leader election")
	}
	if o.CodexAuth.Enabled && !o.LeaderElect {
		return errors.New("codex auth requires keeper leader election")
	}
	if o.ClaudeAuth.Enabled && !o.LeaderElect {
		return errors.New("claude auth requires keeper leader election")
	}
	if o.LeaderElect {
		identity := "keeper-" + string(uuid.NewUUID())
		deadline := o.RenewDeadline
		if deadline <= 0 {
			deadline = 10 * time.Second
		}
		lock, lerr := resourcelock.NewFromKubeconfig("leases", o.Namespace, LeaderElectionID, resourcelock.ResourceLockConfig{Identity: identity}, cfg, deadline)
		if lerr != nil {
			return errors.New("could not configure credential leader election")
		}
		mgrOpts.LeaderElectionResourceLockInterface = lock
		clk := o.Clock
		if clk == nil {
			clk = clock.RealClock{}
		}
		credentialFence = &LeaseFence{Reader: c, Lease: types.NamespacedName{Namespace: o.Namespace, Name: LeaderElectionID}, Identity: identity, Clock: clk, MaxAge: deadline}
	}
	mgr, err := ctrl.NewManager(cfg, mgrOpts)
	if err != nil {
		return err
	}
	if o.GitHubApp.Clock == nil && o.Clock != nil {
		o.GitHubApp.Clock = o.Clock
	}
	if o.OpsGitHubApp != nil && o.OpsGitHubApp.Clock == nil && o.Clock != nil {
		o.OpsGitHubApp.Clock = o.Clock
	}
	k := &Keeper{
		Jobs:     keeperJobs(o),
		Writer:   &SecretWriter{Client: c},
		Log:      o.Log,
		Interval: o.Interval,
		Clock:    o.Clock,
	}
	if err := mgr.Add(k); err != nil {
		return err
	}
	// The login gauges and the notifier read what the workers observe.
	var providers []string
	if o.ClaudeAuth.Enabled {
		providers = append(providers, ProviderClaude)
	}
	if o.CodexAuth.Enabled {
		providers = append(providers, ProviderCodex)
	}
	hub := NewLoginHub(providers...)
	if err := registerLoginCollector(NewLoginCollector(hub)); err != nil {
		return err
	}
	// A bad notice flag sets Disabled. NewNotifier would return that validation
	// error and stop the credential keeper, so the notifier stays off.
	if noticesStart(len(providers), o.Notices) {
		nf, err := loginNotifier(o, hub, c, credentialFence)
		if err != nil {
			return err
		}
		if err := mgr.Add(nf); err != nil {
			return err
		}
	}
	if o.AskNotifications.Enabled {
		a := o.AskNotifications
		if a.StateConfigMap == "" {
			a.StateConfigMap = DefaultAskNotificationConfigMap
		}
		notices := o.Notices.withDefaults()
		nf, err := newAskNotifier(a,
			asks.KubeStore{Client: c, Live: c, Namespace: asks.Namespace, Log: o.Log.WithName("ask-reads")},
			&configMapAskNotificationStore{client: c, name: types.NamespacedName{Namespace: o.Namespace, Name: a.StateConfigMap}},
			&PushoverSender{URL: notices.URL, TokenFile: notices.TokenFile, UserFile: notices.UserFile}, o.Clock, o.Log.WithName("ask-notifications"))
		if err != nil {
			return err
		}
		if err := mgr.Add(nf); err != nil {
			return err
		}
	}
	if o.Reminders.Enabled {
		if !o.LeaderElect {
			return errors.New("reminders require keeper leader election")
		}
		notices := o.Notices.withDefaults()
		if !PushoverCredentialsPresent(notices.TokenFile, notices.UserFile) {
			// Like the notifier: no credential, no scheduler, and the credential
			// keeper still starts.
			o.Log.Info("reminders disabled: no Pushover credentials")
		} else {
			ns := o.Reminders.Namespace
			if ns == "" {
				ns = o.Namespace
			}
			if err := mgr.Add(&reminders.Scheduler{
				Client: c, Reader: c, Namespace: ns,
				Sender:    ReminderSender{Pushover: &PushoverSender{URL: notices.URL, TokenFile: notices.TokenFile, UserFile: notices.UserFile}},
				Every:     o.Reminders.Every,
				MissGrace: o.Reminders.MissGrace,
				Log:       o.Log.WithName("reminders"),
			}); err != nil {
				return err
			}
		}
	}
	if o.CodexAuth.Enabled {
		a := o.CodexAuth
		if a.JournalSecret == "" {
			a.JournalSecret = DefaultCodexJournalSecret
		}
		if a.LiveSecret == "" {
			a.LiveSecret = DefaultCodexLiveSecret
		}
		if a.LoginDir == "" {
			a.LoginDir = DefaultCodexLoginDir
		}
		clk := o.Clock
		if clk == nil {
			clk = clock.RealClock{}
		}
		worker := &codexWorker{Journal: &codexJournal{Client: c, Secret: types.NamespacedName{Namespace: o.Namespace, Name: a.JournalSecret}}, Transport: newCodexHTTPRefresh(), Publisher: &codexPublicSecret{Client: c, Secret: types.NamespacedName{Namespace: o.GHTokenSecret.Namespace, Name: a.LiveSecret}}, Fence: credentialFence.CheckBudget, Clock: clk, LoginDir: a.LoginDir, Obs: hub, Identity: credentialFence.Identity, Log: o.Log.WithName("codex-auth")}
		if err := mgr.Add(worker); err != nil {
			return err
		}
	}
	if o.ClaudeAuth.Enabled {
		clk := o.Clock
		if clk == nil {
			clk = clock.RealClock{}
		}
		r := claudeRunnable(o, c, credentialFence, clk)
		if w, ok := r.(*claudeWorker); ok {
			w.Obs = hub
		}
		if err := mgr.Add(r); err != nil {
			return err
		}
	}
	{
		g := o.ProxmoxGrants
		if g.SessionNamespace == "" {
			g.SessionNamespace = o.GHTokenSecret.Namespace
		}
		if g.JournalSecret == "" {
			g.JournalSecret = DefaultCredentialJournalSecret
		}
		if g.CADir == "" {
			g.CADir = DefaultSSHCAPath
		}
		if g.TargetsFile == "" {
			g.TargetsFile = DefaultSSHTargetsFile
		}
		if g.KnownHostsFile == "" {
			g.KnownHostsFile = DefaultSSHKnownHostsFile
		}
		fence := func(context.Context) error { return errors.New("credential leader election is disabled") }
		if credentialFence != nil {
			fence = credentialFence.Check
		}
		native := &NativeSSH{CADir: g.CADir, TargetsFile: g.TargetsFile, KnownHostsFile: g.KnownHostsFile, BeforeDispatch: fence}
		installer, ierr := newCredentialExecInstaller(cfg)
		if ierr != nil {
			return errors.New("could not configure credential installation")
		}
		clk := o.Clock
		if clk == nil {
			clk = clock.RealClock{}
		}
		hw := o.HWSSHGrants
		if hw.KeySecret == "" {
			hw.KeySecret = DefaultHWSSHKeySecret
		}
		routed := routedInstaller{proxmox: installer, hwssh: &hwsshInstaller{exec: installer, reader: c, keySecret: types.NamespacedName{Namespace: g.SessionNamespace, Name: hw.KeySecret}}}
		worker := &credentialWorker{Enabled: g.Enabled, HWSSHEnabled: hw.Enabled, Client: c, Namespace: o.Namespace, SessionNamespace: g.SessionNamespace, Journal: &credentialJournal{Client: c, Secret: types.NamespacedName{Namespace: o.Namespace, Name: g.JournalSecret}}, Provider: &proxmoxProvider{SSH: native}, Installer: routed, Fence: fence, Clock: clk, Log: o.Log.WithName("proxmox-grants")}
		if err := mgr.Add(worker); err != nil {
			return err
		}
		if g.Enabled {
			if err := mgr.AddReadyzCheck("proxmox-trust", func(_ *http.Request) error { return native.Ready() }); err != nil {
				return err
			}
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("credentials", k.ReadyCheck); err != nil {
		return err
	}
	return mgr.Start(ctx)
}

// registerLoginCollector adds the login gauges to controller-runtime's metrics
// registry, replacing a collector an earlier Run in the same process left
// there (tests start the keeper more than once).
func registerLoginCollector(c *LoginCollector) error {
	err := metrics.Registry.Register(c)
	var already prometheus.AlreadyRegisteredError
	if errors.As(err, &already) {
		metrics.Registry.Unregister(already.ExistingCollector)
		err = metrics.Registry.Register(c)
	}
	return err
}

// loginNotifier builds the notifier Run registers for the login gauges. With
// Claude auth on, a NeedsLogin from the hub is checked behind the leader fence
// against the durable journal before anything is sent, so a stale in-memory
// observation cannot page. Claude auth requires leader election, so fence is
// set whenever ClaudeAuth is enabled.
func loginNotifier(o Options, hub *LoginHub, c client.Client, fence *LeaseFence) (*Notifier, error) {
	notices := o.Notices.withDefaults()
	clk := o.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	nf, err := NewNotifier(notices, hub,
		&ConfigMapNoticeStore{Client: c, Name: types.NamespacedName{Namespace: o.Namespace, Name: notices.StateConfigMap}},
		&PushoverSender{URL: notices.URL, TokenFile: notices.TokenFile, UserFile: notices.UserFile}, clk, o.Log.WithName("notices"))
	if err != nil {
		return nil, err
	}
	if o.ClaudeAuth.Enabled {
		name := o.ClaudeAuth.JournalSecret
		if name == "" {
			name = DefaultClaudeJournalSecret
		}
		nf.VerifyClaude = verifyClaudeJournal(&claudeJournal{Client: c, Secret: types.NamespacedName{Namespace: o.Namespace, Name: name}})
		nf.ClaudeFence = fence.CheckBudget
	}
	return nf, nil
}
