package keeper

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/thaynes43/dev-env/internal/sessionraise"
)

// Run uses registerRaisePager for this startup step. The fake manager records
// client acquisition and registration, then starts every registered runnable.
// Every Kubernetes operation stays in the fake client, including key creation.
type raiseStartupManager struct {
	client                 client.Client
	clientGets, readerGets int
	runnables              []manager.Runnable
}

func (m *raiseStartupManager) GetClient() client.Client {
	m.clientGets++
	return m.client
}

func (m *raiseStartupManager) GetAPIReader() client.Reader {
	m.readerGets++
	return m.client
}

func (m *raiseStartupManager) Add(r manager.Runnable) error {
	m.runnables = append(m.runnables, r)
	return nil
}

func (m *raiseStartupManager) start(t *testing.T) {
	t.Helper()
	// Start always makes its first pass before observing cancellation. No
	// timer or goroutine is needed, and all I/O uses the fake client.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, r := range m.runnables {
		if p, ok := r.(*RaisePager); ok {
			p.Sender = &fakeRaiseSender{}
		}
		if err := r.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRaiseStartupOffAndInvalidTouchesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		leader  bool
		notices NoticeOptions
	}{
		{name: "default off", leader: true},
		{name: "off with invalid settings", notices: NoticeOptions{URL: "http://invalid.example"}},
		{name: "no leader election", enabled: true},
		{name: "invalid notices", enabled: true, leader: true, notices: NoticeOptions{URL: "http://invalid.example"}},
		{name: "disabled notices", enabled: true, leader: true, notices: NoticeOptions{Disabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
			raiseObjects(t, base)
			reads, writes := map[string]int{}, map[string]int{}
			c := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					reads[key.Name]++
					return c.Get(ctx, key, obj, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					writes[obj.GetName()]++
					return c.Update(ctx, obj, opts...)
				},
			})
			mgr := &raiseStartupManager{client: c}
			o := Options{LeaderElect: tc.leader, Notices: tc.notices, RaisePages: RaisePageOptions{Enabled: tc.enabled}, Log: logr.Discard()}
			if err := registerRaisePager(o, mgr); err != nil {
				t.Fatalf("raise configuration stopped keeper startup: %v", err)
			}
			mgr.start(t)
			for _, name := range []string{sessionraise.KeeperSecretName, sessionraise.KeeperPublicName, sessionraise.NoticeLedgerName} {
				if reads[name] != 0 || writes[name] != 0 {
					t.Errorf("%s: reads=%d writes=%d, want zero", name, reads[name], writes[name])
				}
			}
			if len(reads) != 0 || len(writes) != 0 || mgr.clientGets != 0 || mgr.readerGets != 0 || len(mgr.runnables) != 0 {
				t.Fatalf("disabled feature acquired clients or ran: client=%d reader=%d runnables=%d reads=%v writes=%v", mgr.clientGets, mgr.readerGets, len(mgr.runnables), reads, writes)
			}
		})
	}
}

func TestRaiseStartupEnabledPublishesKey(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(raiseScheme(t)).Build()
	raiseObjects(t, c)
	mgr := &raiseStartupManager{client: c}
	when := time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)
	o := Options{
		Namespace: sessionraise.ObjectNamespace, LeaderElect: true, Log: logr.Discard(),
		RaisePages: RaisePageOptions{Enabled: true, SessionNamespace: "sessions", KeySecret: sessionraise.KeeperSecretName},
		Clock:      clocktesting.NewFakeClock(when),
	}
	if err := registerRaisePager(o, mgr); err != nil {
		t.Fatal(err)
	}
	if len(mgr.runnables) != 1 || mgr.clientGets != 1 || mgr.readerGets != 1 {
		t.Fatalf("enabled startup: client=%d reader=%d runnables=%d", mgr.clientGets, mgr.readerGets, len(mgr.runnables))
	}
	p, ok := mgr.runnables[0].(*RaisePager)
	if !ok || !p.NeedLeaderElection() || p.sessionNamespace() != "sessions" || p.secretName() != sessionraise.KeeperSecretName || !p.now().Equal(when) {
		t.Fatalf("wrong raise runnable: %T", mgr.runnables[0])
	}
	// Changing the original options after registration cannot turn it off.
	o.RaisePages.Enabled = false
	mgr.start(t)
	pub, err := sessionraise.ReadPublic(context.Background(), p.store(), sessionraise.KeeperPublicName)
	if err != nil || len(pub) == 0 || !p.keyReady {
		t.Fatalf("enabled startup did not initialize and publish its fake key: ready=%v err=%v", p.keyReady, err)
	}
	// The separate notice ledger is unchanged on an idle enabled start.
	obj := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: o.Namespace, Name: sessionraise.NoticeLedgerName}, obj); err != nil || len(obj.Data) != 0 {
		t.Fatalf("idle startup changed the notice ledger: err=%v", err)
	}
}
