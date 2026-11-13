package keeper

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Story 782 fix round 1, F1 and F7: the pager's own client, and its session
// namespace.

type summonRoundTrip func(*http.Request) (*http.Response, error)

func (f summonRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// summonBaseTransport unwraps client-go's round trippers down to the
// connection pool.
func summonBaseTransport(t *testing.T, cfg *rest.Config) http.RoundTripper {
	t.Helper()
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt := hc.Transport
	for {
		w, ok := rt.(utilnet.RoundTripperWrapper)
		if !ok {
			return rt
		}
		rt = w.WrappedRoundTripper()
	}
}

// F1, structure: the pager's REST config has its own transport, its own rate
// limiter and a bounded request timeout; the keeper's config is unchanged.
func TestSummonPagesRESTConfigIsItsOwn(t *testing.T) {
	cfg := envConfig(t)
	shared := rest.CopyConfig(cfg)
	shared.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(20, 30)
	pc, err := summonPagesRESTConfig(shared, SummonPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pc.Timeout != summonPagesRequestTimeout || pc.RateLimiter != nil || pc.Dial == nil {
		t.Fatalf("pager config: timeout %s, rate limiter shared %v, dialer %v", pc.Timeout, pc.RateLimiter != nil, pc.Dial != nil)
	}
	if shared.Timeout != cfg.Timeout || shared.Dial != nil || shared.RateLimiter == nil {
		t.Fatal("the keeper's REST config was changed")
	}
	keeper := summonBaseTransport(t, shared)
	if again := summonBaseTransport(t, shared); again != keeper {
		t.Fatal("client-go's cache did not share the keeper's transport: the comparison below proves nothing")
	}
	if summonBaseTransport(t, pc) == keeper {
		t.Fatal("the pager shares the keeper's transport")
	}
	runtime.KeepAlive(keeper)
	withTransport := rest.CopyConfig(cfg)
	withTransport.Transport = http.DefaultTransport
	if _, err := summonPagesRESTConfig(withTransport, SummonPageOptions{}); err == nil {
		t.Fatal("a REST config with a transport of its own was accepted")
	}
	if push := newSummonPushClient(); push.Transport == nil || push.Transport == http.DefaultTransport || push.Timeout != noticeHTTPTimeout {
		t.Fatal("the pager's Pushover client uses the default transport")
	}
}

// F1, behaviour: a stalled pager request, its discovery or its list, holds
// nothing the keeper's other workers use. A Claude publication and an Ask send
// through the keeper's shared client, built as Run builds it, finish while it
// is held; the pager's tick then ends on its own request timeout, which also
// bounds discovery (discovery runs on a background context).
func TestSummonPagesStalledRequestBlocksNothingElse(t *testing.T) {
	cfg := envConfig(t)
	scheme := kruntime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme), coord.AddToScheme(scheme)); err != nil {
		t.Fatal(err)
	}
	shared, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tokenFile, userFile := filepath.Join(dir, "token"), filepath.Join(dir, "user")
	for _, p := range []string{tokenFile, userFile} {
		if err := os.WriteFile(p, []byte(summonTokenCanary), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		stall  func(*http.Request) bool
		expect string
	}{
		// The pager's first request needs the REST mapper's discovery.
		{name: "discovery", stall: func(r *http.Request) bool { return !strings.Contains(r.URL.Path, "/namespaces/") }, expect: "summon page state unavailable"},
		{name: "list", stall: func(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/agentsessions") }, expect: "summon page scan unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			keeperNS, agentsNS := "summon-stall-keeper-"+tc.name, "summon-stall-agents-"+tc.name
			live := types.NamespacedName{Namespace: agentsNS, Name: DefaultClaudeLiveSecret}
			askLedger := types.NamespacedName{Namespace: keeperNS, Name: DefaultAskNotificationConfigMap}
			for _, obj := range []client.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: keeperNS}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: agentsNS}},
				&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: keeperNS, Name: DefaultSummonPagesConfigMap}},
				&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: askLedger.Namespace, Name: askLedger.Name}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: live.Namespace, Name: live.Name}},
			} {
				if err := shared.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			stalled, release := make(chan struct{}, 1), make(chan struct{})
			var held atomic.Int32
			var logs logBuffer
			o := Options{Namespace: keeperNS, LeaderElect: true,
				Notices: NoticeOptions{URL: "https://push.invalid", TokenFile: tokenFile, UserFile: userFile},
				SummonPages: SummonPageOptions{Enabled: true, PortalOrigin: "https://portal.example", SessionNamespace: agentsNS,
					requestTimeout: 300 * time.Millisecond,
					wrapTransport: func(rt http.RoundTripper) http.RoundTripper {
						return summonRoundTrip(func(r *http.Request) (*http.Response, error) {
							if !tc.stall(r) {
								return rt.RoundTrip(r)
							}
							held.Add(1)
							defer held.Add(-1)
							select {
							case stalled <- struct{}{}:
							default:
							}
							<-release
							// Only the pager's own timeout ends the request.
							<-r.Context().Done()
							return nil, r.Context().Err()
						})
					}}}
			p := summonPagerFor(o, cfg, logs.logger())
			if p == nil {
				t.Fatalf("no pager: %s", logs.String())
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				p.tick(ctx)
			}()
			select {
			case <-stalled:
			case <-time.After(30 * time.Second):
				t.Fatal("the pager's request never reached the hook")
			}
			// While it is held: a Claude publication through the shared client.
			pub := &claudePublicSecret{Client: shared, Secret: live}
			if err := pub.publish(ctx, syntheticClaudeAccess(1, time.Now(), 8*time.Hour), time.Time{}, time.Now()); err != nil {
				t.Fatalf("the Claude publication did not finish: %v", err)
			}
			// And an Ask send whose ledger is on the shared client.
			af := newAskNoticeFixture(t)
			af.store = &configMapAskNotificationStore{client: shared, name: askLedger}
			af.restart(t)
			af.add(1, "00000000-0000-4000-8000-000000000001")
			af.n.tick(ctx)
			if len(af.sender.notices) != 1 {
				t.Fatalf("the Ask send did not finish: %d sends, logs %s", len(af.sender.notices), af.logs.String())
			}
			if held.Load() < 1 {
				t.Fatal("the pager's request was not held while they ran")
			}
			close(release)
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("the pager's tick did not end on its own request timeout")
			}
			if !strings.Contains(logs.String(), tc.expect) {
				t.Fatalf("logs: %s", logs.String())
			}
		})
	}
}

// F7: the pager lists the session namespace it was given, never another
// option's; an empty one disables it and says so; Start logs it once.
func TestSummonPagesSessionNamespaceIsExplicit(t *testing.T) {
	base := Options{Namespace: "dev-env-system", LeaderElect: true,
		GHTokenSecret: types.NamespacedName{Namespace: "token-namespace", Name: "gh"},
		ProxmoxGrants: ProxmoxGrantOptions{SessionNamespace: "grant-namespace"}}
	cfg := &rest.Config{Host: "https://127.0.0.1:1"}
	var logs bytes.Buffer
	log := logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))
	o := base
	o.SummonPages = SummonPageOptions{Enabled: true, PortalOrigin: "https://portal.example"}
	if p := summonPagerFor(o, cfg, log); p != nil || !strings.Contains(logs.String(), "the session namespace is empty") {
		t.Fatalf("an empty session namespace: pager %v, logs %s", p != nil, logs.String())
	}
	logs.Reset()
	o.SummonPages.SessionNamespace = "session-namespace"
	p := summonPagerFor(o, cfg, log)
	if p == nil {
		t.Fatalf("no pager: %s", logs.String())
	}
	if r, ok := p.reader.(*kubeSummonSessions); !ok || r.namespace != "session-namespace" || p.namespace != "session-namespace" {
		t.Fatalf("the pager lists %+v", p.reader)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = p.Start(ctx)
	if strings.Count(logs.String(), `"sessionNamespace":"session-namespace"`) != 1 || strings.Contains(logs.String(), "token-namespace") || strings.Contains(logs.String(), "grant-namespace") {
		t.Fatalf("logs %s", logs.String())
	}
}
