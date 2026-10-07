package keeper

import (
	"context"
	"errors"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// LeaderElectionID is the keeper's Lease, in its own namespace. The keeper runs
// one replica with the Recreate strategy; the Lease is what keeps a second
// process (a pod on a partitioned node that has not stopped yet, a stray scale)
// from refreshing too. A rotating refresh token must have one owner.
const LeaderElectionID = "dev-env-keeper.dev-env.haynesops.com"

// Options configure Run.
type Options struct {
	// Namespace is the keeper's own namespace, where its Lease lives.
	Namespace string
	// GHTokenSecret is the Secret the gh token goes into (D-13).
	GHTokenSecret types.NamespacedName
	// GitHubApp mints the gh token.
	GitHubApp *GitHubApp
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
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
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
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("credentials", k.ReadyCheck); err != nil {
		return err
	}
	return mgr.Start(ctx)
}
