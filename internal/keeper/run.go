package keeper

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
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
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/thaynes43/dev-env/api/v1alpha1"
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
	// ProxmoxGrants is disabled by default and never enables general hw-ssh.
	ProxmoxGrants ProxmoxGrantOptions
	CodexAuth     CodexOptions
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

// Run runs the keeper until ctx ends or this process loses the leader election,
// which returns an error so the process exits and the kubelet restarts it.
func Run(ctx context.Context, cfg *rest.Config, o Options) error {
	if o.Namespace == "" || o.GHTokenSecret.Namespace == "" || o.GHTokenSecret.Name == "" || o.GitHubApp == nil {
		return errors.New("keeper: the namespace, the gh token Secret and the GitHub App are required")
	}
	if o.CodexAuth.Enabled && (o.Namespace == o.GHTokenSecret.Namespace || (o.CodexAuth.LoginDir != "" && !filepath.IsAbs(o.CodexAuth.LoginDir))) {
		return errors.New("Codex auth requires separate private/public namespaces and absolute private staging")
	}
	scheme := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme)); err != nil {
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
		return errors.New("Codex auth requires keeper leader election")
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
	k := &Keeper{
		Jobs:     []Job{GitHubTokenJob("gh-token", o.GitHubApp, o.GHTokenSecret)},
		Writer:   &SecretWriter{Client: c},
		Log:      o.Log,
		Interval: o.Interval,
		Clock:    o.Clock,
	}
	if err := mgr.Add(k); err != nil {
		return err
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
		worker := &codexWorker{Journal: &codexJournal{Client: c, Secret: types.NamespacedName{Namespace: o.Namespace, Name: a.JournalSecret}}, Transport: newCodexHTTPRefresh(), Publisher: &codexPublicSecret{Writer: &SecretWriter{Client: c}, Namespace: o.GHTokenSecret.Namespace, Name: a.LiveSecret}, Fence: credentialFence.CheckBudget, Clock: clk, LoginDir: a.LoginDir, Identity: credentialFence.Identity, Log: o.Log.WithName("codex-auth")}
		if err := mgr.Add(worker); err != nil {
			return err
		}
		if err := mgr.AddReadyzCheck("codex-auth", worker.ReadyCheck); err != nil {
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
		worker := &credentialWorker{Enabled: g.Enabled, Client: c, Namespace: o.Namespace, SessionNamespace: g.SessionNamespace, Journal: &credentialJournal{Client: c, Secret: types.NamespacedName{Namespace: o.Namespace, Name: g.JournalSecret}}, Provider: &proxmoxProvider{SSH: native}, Installer: installer, Fence: fence, Clock: clk, Log: o.Log.WithName("proxmox-grants")}
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
