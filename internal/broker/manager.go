package broker

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// LeaderElectionID is the broker's Lease, in its own namespace: not the
// operator's, so the two modes elect their leaders apart.
const LeaderElectionID = "dev-env-broker.dev-env.haynesops.com"

// Name is the broker's name in logs and Events.
const Name = "dev-env-broker"

// Options configure NewManager.
type Options struct {
	// SessionNamespace holds the sessions, their pods and the grants (D-54).
	SessionNamespace string
	// PolicyNamespace holds the GrantPolicies and the Lease.
	PolicyNamespace string
	// LeaderElect elects one leader among the replicas; only it reconciles.
	LeaderElect bool
	// LeaseDuration, RenewDeadline and RetryPeriod tune leader election; zero
	// keeps controller-runtime's defaults.
	LeaseDuration, RenewDeadline, RetryPeriod time.Duration
	// MetricsAddr and ProbeAddr are the metrics and /healthz addresses; "0"
	// turns one off.
	MetricsAddr, ProbeAddr string
	// Clock is the broker's time; nil is the real clock.
	Clock clock.PassiveClock
	// Notifier tells Tom a grant waits; nil tells no one.
	Notifier Notifier
	// Installer installs a grant's token in the session's pod; nil installs
	// nothing (plan 07 step 4 brings one).
	Installer Installer

	// skipNameValidation lets one test process start several brokers.
	skipNameValidation bool
	// reconciled hears of every finished reconcile (tests).
	reconciled func(types.NamespacedName)
}

// CacheOptions scope the broker's cache to what its RBAC reads (DESIGN-001 6.11
// broker row): AccessGrants, AgentSessions and session pods in the session
// namespace, and GrantPolicies in its own.
func CacheOptions(sessionNamespace, policyNamespace string) cache.Options {
	return cache.Options{
		DefaultNamespaces: map[string]cache.Config{sessionNamespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.GrantPolicy{}: {Namespaces: map[string]cache.Config{policyNamespace: {}}},
			&corev1.Pod{}: {Label: labels.SelectorFromSet(labels.Set{
				v1alpha1.LabelAppName: v1alpha1.AppNameSession,
			})},
		},
	}
}

// NewManager builds the broker as `dev-env-operator broker` runs it: a
// controller-runtime manager with leader election on its own Lease, its cache
// scoped by CacheOptions, and the Broker registered. ServiceAccounts and
// bindings are read through the API server only, never cached or listed. The
// Broker is returned for Decide.
func NewManager(cfg *rest.Config, o Options) (ctrl.Manager, *Broker, error) {
	if o.SessionNamespace == "" || o.PolicyNamespace == "" {
		return nil, nil, errors.New("the session and policy namespaces must be set")
	}
	scheme := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme)); err != nil {
		return nil, nil, err
	}
	mo := ctrl.Options{
		Scheme: scheme,
		Cache:  CacheOptions(o.SessionNamespace, o.PolicyNamespace),
		Client: client.Options{Cache: &client.CacheOptions{
			DisableFor: []client.Object{&corev1.ServiceAccount{}, &rbacv1.RoleBinding{}, &rbacv1.ClusterRoleBinding{}},
		}},
		Metrics:                       metricsserver.Options{BindAddress: o.MetricsAddr},
		HealthProbeBindAddress:        o.ProbeAddr,
		LeaderElection:                o.LeaderElect,
		LeaderElectionID:              LeaderElectionID,
		LeaderElectionNamespace:       o.PolicyNamespace,
		LeaderElectionReleaseOnCancel: true,
	}
	if o.LeaseDuration > 0 {
		mo.LeaseDuration = &o.LeaseDuration
	}
	if o.RenewDeadline > 0 {
		mo.RenewDeadline = &o.RenewDeadline
	}
	if o.RetryPeriod > 0 {
		mo.RetryPeriod = &o.RetryPeriod
	}
	if o.skipNameValidation {
		mo.Controller = config.Controller{SkipNameValidation: ptr.To(true)}
	}
	mgr, err := ctrl.NewManager(cfg, mo)
	if err != nil {
		return nil, nil, err
	}
	clk := o.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	b := &Broker{
		Client:           mgr.GetClient(),
		APIReader:        mgr.GetAPIReader(),
		SessionNamespace: o.SessionNamespace,
		PolicyNamespace:  o.PolicyNamespace,
		Clock:            clk,
		Notifier:         o.Notifier,
		Installer:        o.Installer,
		Recorder:         mgr.GetEventRecorder(Name),
	}
	b.reconciled = o.reconciled
	if err := b.SetupWithManager(mgr); err != nil {
		return nil, nil, err
	}
	if err := errors.Join(mgr.AddHealthzCheck("healthz", healthz.Ping), mgr.AddReadyzCheck("readyz", healthz.Ping)); err != nil {
		return nil, nil, err
	}
	return mgr, b, nil
}

// SetupWithManager registers the reconciler, one grant at a time. Besides its
// grants it watches a session that ends (deleted, or being deleted) and a
// session pod that appears or starts Running, which enqueue that session's
// grants, and every GrantPolicy change, which enqueues every pending grant.
func (b *Broker) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("accessgrant").
		For(&v1alpha1.AccessGrant{}).
		Watches(&v1alpha1.AgentSession{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
				return b.grantsOf(ctx, o.GetName(), false)
			}),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(event.CreateEvent) bool { return false },
				UpdateFunc: func(e event.UpdateEvent) bool {
					return e.ObjectOld.GetDeletionTimestamp().IsZero() && !e.ObjectNew.GetDeletionTimestamp().IsZero()
				},
				DeleteFunc:  func(event.DeleteEvent) bool { return true },
				GenericFunc: func(event.GenericEvent) bool { return false },
			})).
		Watches(&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
				s := o.GetLabels()[v1alpha1.LabelSession]
				if s == "" || o.GetName() != s {
					return nil
				}
				return b.grantsOf(ctx, s, false)
			}),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(event.CreateEvent) bool { return true },
				UpdateFunc: func(e event.UpdateEvent) bool {
					was, is := e.ObjectOld.(*corev1.Pod), e.ObjectNew.(*corev1.Pod)
					return is.Status.Phase == corev1.PodRunning && was.Status.Phase != corev1.PodRunning
				},
				DeleteFunc:  func(event.DeleteEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
			})).
		Watches(&v1alpha1.GrantPolicy{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, _ client.Object) []reconcile.Request {
				return b.grantsOf(ctx, "", true)
			})).
		Complete(b)
}

// grantsOf lists grants from the cache: a session's (by the session label the
// API sets, D-56, and spec.requester.session), or every pending one.
func (b *Broker) grantsOf(ctx context.Context, session string, pendingOnly bool) []reconcile.Request {
	opts := []client.ListOption{client.InNamespace(b.SessionNamespace)}
	if session != "" {
		opts = append(opts, client.MatchingLabels{v1alpha1.LabelSession: session})
	}
	var list v1alpha1.AccessGrantList
	if err := b.Client.List(ctx, &list, opts...); err != nil {
		log.FromContext(ctx).Error(err, "list grants", "session", session)
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		g := &list.Items[i]
		if session != "" && g.Spec.Requester.Session != session {
			continue
		}
		if pendingOnly && g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	}
	return reqs
}
