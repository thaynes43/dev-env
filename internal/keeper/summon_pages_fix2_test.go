package keeper

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Story 782, fix round 2: the proofs for the coordinator's rulings on the
// re-check of fix round 1 (rulings in PR 1102 comment 6840605385; not a ruling
// by Tom). No test here reaches a cluster: the servers are local httptest
// servers or the fake client, and no test reads the process's cluster
// environment.

const summonBearerCanary = "SUMMONBEARERCANARY0123456789abcdef"

// summonProductionShapedConfig is the shape of the keeper's production REST
// config: what rest.InClusterConfig returns (a host, a CA file, a bearer
// token and its file), with the QPS ctrl.GetConfig sets. Nothing is dialled.
func summonProductionShapedConfig(t *testing.T) *rest.Config {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca, token := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte(summonBearerCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	return &rest.Config{Host: "https://127.0.0.1:1", TLSClientConfig: rest.TLSClientConfig{CAFile: ca},
		BearerToken: summonBearerCanary, BearerTokenFile: token, QPS: -1}
}

// F1 (HIGH): a keeper REST config whose transport, transport wrapper or
// dialer is its own cannot be shown independent of the keeper's other
// workers, so the pager is disabled and says which, in fixed words. Nothing of
// the config is dropped to make it fit, and none of its hooks runs. The
// production-shaped config is accepted with its authentication intact.
func TestSummonPagesRefusesAConfigWithHooksOfItsOwn(t *testing.T) {
	var lock sync.Mutex
	var ran atomic.Int32
	wrapper := func(rt http.RoundTripper) http.RoundTripper {
		ran.Add(1)
		return summonRoundTrip(func(r *http.Request) (*http.Response, error) {
			lock.Lock() // the lock the keeper's own requests take
			defer lock.Unlock()
			return rt.RoundTrip(r)
		})
	}
	dialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		ran.Add(1)
		lock.Lock()
		defer lock.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	o := Options{Namespace: "dev-env-system", LeaderElect: true,
		SummonPages: SummonPageOptions{Enabled: true, PortalOrigin: "https://portal.example", SessionNamespace: "dev-agents"}}
	for _, tc := range []struct {
		name  string
		set   func(*rest.Config)
		words string
	}{
		{"a wrapper that shares a lock", func(c *rest.Config) { c.WrapTransport = wrapper }, "transport wrapper"},
		{"a dialer that shares a lock", func(c *rest.Config) { c.Dial = dialer }, "dialer"},
		{"a transport", func(c *rest.Config) { c.Transport = &http.Transport{} }, "transport"},
		{"all three", func(c *rest.Config) {
			c.WrapTransport, c.Dial = wrapper, dialer
			c.Transport = http.DefaultTransport
		}, "transport, transport wrapper and dialer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := summonProductionShapedConfig(t)
			tc.set(cfg)
			before := rest.CopyConfig(cfg)
			var logs bytes.Buffer
			if p := summonPagerFor(o, cfg, logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))); p != nil {
				t.Fatal("the pager was built on a config with a hook of its own")
			}
			want := fmt.Sprintf(`"msg":"summon pages disabled: the pager's own client could not be built","reason":"the keeper's REST config has its own %s"`, tc.words)
			if strings.Count(logs.String(), "summon pages disabled") != 1 || !strings.Contains(logs.String(), want) {
				t.Fatalf("logs %s", logs.String())
			}
			for _, value := range []string{summonBearerCanary, cfg.Host, cfg.CAFile} {
				if strings.Contains(logs.String(), value) {
					t.Fatalf("the log line carries a config value: %s", logs.String())
				}
			}
			if ran.Load() != 0 {
				t.Fatalf("the keeper's own hook ran %d times", ran.Load())
			}
			if (cfg.WrapTransport == nil) != (before.WrapTransport == nil) || (cfg.Dial == nil) != (before.Dial == nil) ||
				cfg.Transport != before.Transport || cfg.BearerToken != summonBearerCanary {
				t.Fatal("the keeper's REST config was changed")
			}
		})
	}
	t.Run("the production-shaped config", func(t *testing.T) {
		cfg := summonProductionShapedConfig(t)
		var logs bytes.Buffer
		if p := summonPagerFor(o, cfg, logr.FromSlogHandler(slog.NewJSONHandler(&logs, nil))); p == nil {
			t.Fatalf("refused: %s", logs.String())
		}
		pc, err := summonPagesRESTConfig(cfg, SummonPageOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if pc.BearerToken != cfg.BearerToken || pc.BearerTokenFile != cfg.BearerTokenFile || pc.CAFile != cfg.CAFile || pc.Host != cfg.Host {
			t.Fatal("the pager's copy dropped the config's authentication")
		}
		if pc.Dial == nil || pc.WrapTransport != nil || pc.Transport != nil || cfg.Dial != nil {
			t.Fatal("the pager's copy has no dialer of its own, or the keeper's config gained one")
		}
	})
}

// F1 (MEDIUM): the pager's client does no client-side rate limiting, so no
// request, discovery included, waits for a token outside the request timeout.
// The re-check's probe: immediate /api and /apis replies, a keeper config of
// one query a second with a burst of one, a 100 millisecond timeout and
// several discovery requests. Each finishes, or ends on the timeout; none
// takes a second.
func TestSummonPagesDiscoveryNeverWaitsForAToken(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":["v1"],"serverAddressByClientCIDRs":[]}`))
		case "/apis":
			_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL, QPS: 1, Burst: 1}
	const timeout = 100 * time.Millisecond
	pc, err := summonPagesRESTConfig(cfg, SummonPageOptions{requestTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	hc, err := rest.HTTPClientFor(pc)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := discovery.NewDiscoveryClientForConfigAndClient(pc, hc)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 4; i++ {
		began := time.Now()
		_, err := dc.ServerGroups()
		took := time.Since(began)
		if err != nil && !summonTimedOut(err) {
			t.Fatalf("discovery %d: %v", i, err)
		}
		if took >= time.Second {
			t.Fatalf("discovery %d took %s: it waited outside the %s timeout", i, took, timeout)
		}
	}
	if took := time.Since(start); took >= time.Second || requests.Load() < 4 {
		t.Fatalf("four discoveries took %s over %d requests", took, requests.Load())
	}
	if pc.Timeout != timeout || pc.RateLimiter != nil || pc.QPS >= 0 || dc.RESTClient().GetRateLimiter() != nil || cfg.QPS != 1 || cfg.Burst != 1 {
		t.Fatalf("pager config: timeout %s, rate limiter %v, QPS %v; keeper QPS %v burst %d", pc.Timeout, dc.RESTClient().GetRateLimiter() != nil, pc.QPS, cfg.QPS, cfg.Burst)
	}
}

func summonTimedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) || strings.Contains(err.Error(), "Client.Timeout")
}

// F3 (MEDIUM): the proof reads of a tick go on from where the last tick
// stopped, so owners that are still there cannot starve the proof of later,
// deleted ones. A full ledger (the entry bound) holds 20 kept owners ahead of
// deleted ones: the first tick proves the kept ones and has no room; later
// ticks reclaim room, and a new event is then recorded and sent. Still at most
// 20 reads a tick, reads stop on a failure and resume there, an entry goes
// only on proof, and a restart starts from the oldest again.
func TestSummonPagesPruneMovesPastKeptOwners(t *testing.T) {
	f := newSummonFix(t)
	l := f.ledger()
	oldest := f.clock.Now().Add(-3 * time.Hour).Unix()
	var kept []*v1alpha1.AgentSession
	for i := 0; i < summonPagesProofReads; i++ {
		s := f.failedSession(fmt.Sprintf("wo-kept-%02d", i), 3*time.Hour)
		s.Labels = nil // still there, but not in the labelled list
		kept = append(kept, s)
		l.State.Events[summonKey(s, "failed")] = summonPageEntry{At: oldest + int64(i), State: "sent", Session: s.Name}
	}
	f.add(kept...)
	goneKey := func(i int) string { return fmt.Sprintf("00000000-0000-4000-9000-%012d/failed", i) }
	goneName := func(i int) string { return fmt.Sprintf("wo-gone-%04d", i) }
	gone := summonPagesMaxEvents - len(kept)
	for i := 0; i < gone; i++ {
		l.State.Events[goneKey(i)] = summonPageEntry{At: oldest + 3600 + int64(i), State: "sent", Session: goneName(i)}
	}
	if err := (&configMapSummonPageStore{client: f.c, name: summonLedgerName}).save(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	var read []string
	f.sessionGet = func(name string) error { read = append(read, name); return nil }
	keptLeft := func() int {
		n := 0
		for _, s := range kept {
			if _, ok := f.ledger().State.Events[summonKey(s, "failed")]; ok {
				n++
			}
		}
		return n
	}
	goneLeft := func(from, to int) int {
		n, ev := 0, f.ledger().State.Events
		for i := from; i < to; i++ {
			if _, ok := ev[goneKey(i)]; ok {
				n++
			}
		}
		return n
	}
	// tick adds a new event when named; an event not yet recorded is what
	// lets a tick prune.
	tick := func(newSession string) {
		t.Helper()
		f.clock.Step(time.Minute)
		if newSession != "" {
			f.add(f.failedSession(newSession, time.Minute))
		}
		read = nil
		f.tick()
	}

	// The first tick reads the 20 kept owners, which are still there: no room.
	tick("wo-new-1")
	if len(read) != summonPagesProofReads || !strings.HasPrefix(read[0], "wo-kept-") || !strings.HasPrefix(read[len(read)-1], "wo-kept-") {
		t.Fatalf("first tick read %v", read)
	}
	if f.push.count() != 0 || len(f.ledger().State.Events) != summonPagesMaxEvents || !strings.Contains(f.logs.String(), "summon page dedupe capacity reached") {
		t.Fatalf("first tick: sends %d, entries %d, logs %s", f.push.count(), len(f.ledger().State.Events), f.logs.String())
	}
	// The next tick moves on to the deleted owners, proves 20 gone and records
	// and sends the waiting event.
	tick("")
	if len(read) != summonPagesProofReads || read[0] != goneName(0) || read[len(read)-1] != goneName(summonPagesProofReads-1) {
		t.Fatalf("second tick read %v", read)
	}
	if f.sentFor("wo-new-1") != 1 || f.push.count() != 1 || goneLeft(0, summonPagesProofReads) != 0 || keptLeft() != len(kept) {
		t.Fatalf("second tick: pages %v, gone left %d, kept %d", f.push.titles(), goneLeft(0, summonPagesProofReads), keptLeft())
	}
	// And the one after reclaims the next 20.
	tick("wo-new-2")
	if read[0] != goneName(summonPagesProofReads) || f.sentFor("wo-new-2") != 1 || goneLeft(summonPagesProofReads, 2*summonPagesProofReads) != 0 {
		t.Fatalf("third tick read %v, pages %v", read, f.push.titles())
	}
	// A failed read stops the reads; the next tick resumes at that owner.
	failAt := goneName(2*summonPagesProofReads + 4)
	f.sessionGet = func(name string) error {
		read = append(read, name)
		if name == failAt {
			return errors.New("read refused by the test")
		}
		return nil
	}
	tick("wo-new-3")
	if len(read) != 5 || read[4] != failAt || goneLeft(2*summonPagesProofReads, 2*summonPagesProofReads+4) != 0 || goneLeft(2*summonPagesProofReads+4, gone) != gone-2*summonPagesProofReads-4 {
		t.Fatalf("a failed read: read %v", read)
	}
	f.sessionGet = func(name string) error { read = append(read, name); return nil }
	tick("wo-new-4")
	if len(read) != summonPagesProofReads || read[0] != failAt {
		t.Fatalf("after a failed read the next tick read %v", read)
	}
	if keptLeft() != len(kept) || f.push.count() != 4 {
		t.Fatalf("kept %d, pages %v", keptLeft(), f.push.titles())
	}
	// A restart starts from the oldest again: the kept owners.
	f.restart()
	tick("wo-new-5")
	if len(read) != summonPagesProofReads || !strings.HasPrefix(read[0], "wo-kept-") {
		t.Fatalf("after a restart the tick read %v", read)
	}
	f.assertRollingCap()
}

// F6 (MEDIUM): host labels in any script, in the fake endpoint's message. A
// dotted host whose labels are letters, digits or hyphens of any script and
// whose last label is two or more letters, or starts xn--, is redacted with
// or without a port, beside the ASCII and punycode cases; a letter's
// combining marks belong to it (Hindi, Thai); an underscore ends a label; a
// dotted number stays. A cut at the 200th character that leaves a host behind
// is redacted again, counting characters, not bytes.
func TestSummonPagesNoteRedactsHostsOfAnyScriptInTheMessage(t *testing.T) {
	f := newSummonFix(t)
	const hosts = "hosts é.example é.example:443 例.example 例.example:8443/x пример.xn--p1ai пример.xn--p1ai:443 пример.рф, " +
		"beside evil.example evil.example:443 evil.xn--p1ai evil.xn--p1ai:443, версия 2.105.0."
	const hostsShown = "hosts [link removed] [link removed] [link removed] [link removed] [link removed] [link removed] [link removed], " +
		"beside [link removed] [link removed] [link removed] [link removed], версия 2.105.0."
	// "пример.рф1x" is no host; its first 9 characters are, and the cut at
	// the 200th character leaves just those.
	pad := strings.Repeat("d", summonNoteMax-len([]rune(" пример.рф")))
	cut := pad + " пример.рф1x and more"
	if n := len([]rune(hostsShown)); n > summonNoteMax {
		t.Fatalf("the expected note is %d characters: the cut would hide a case", n)
	}
	// Letters with combining marks (the coordinator's answer op782-fix2-a-1):
	// Hindi and Thai.
	const marks = "hindi उदाहरण.भारत, thai ตัวอย่าง.ไทย and ตัวอย่าง.ไทย:443 are hosts; a_b.example and a.example-b too."
	const marksShown = "hindi [link removed], thai [link removed] and [link removed] are hosts; a_[link removed] and [link removed]-b too."
	f.add(f.close(f.session("wo-upgrade-1", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", hosts),
		f.close(f.session("wo-upgrade-2", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", cut),
		f.close(f.session("wo-upgrade-3", v1alpha1.LaneUpgrade), v1alpha1.OutcomeFailed, summonoutcome.Report, "failed", marks))
	f.tick()
	if f.push.count() != 3 {
		t.Fatalf("sent %d", f.push.count())
	}
	shown := map[string]string{}
	for i := 0; i < 3; i++ {
		msg := f.push.form(i).Get("message")
		_, note, ok := strings.Cut(msg, "\nsession note: ")
		if !ok {
			t.Fatalf("no note in %q", msg)
		}
		for _, name := range []string{"wo-upgrade-1", "wo-upgrade-2", "wo-upgrade-3"} {
			if strings.Contains(msg, "session "+name+" ") {
				shown[name] = note
			}
		}
	}
	if shown["wo-upgrade-1"] != hostsShown {
		t.Fatalf("note %q, want %q", shown["wo-upgrade-1"], hostsShown)
	}
	if shown["wo-upgrade-3"] != marksShown {
		t.Fatalf("note %q, want %q", shown["wo-upgrade-3"], marksShown)
	}
	if want := string([]rune(pad + " [link removed]")[:summonNoteMax]); shown["wo-upgrade-2"] != want || len([]rune(shown["wo-upgrade-2"])) != summonNoteMax {
		t.Fatalf("cut note %q (%d characters), want %q", shown["wo-upgrade-2"], len([]rune(shown["wo-upgrade-2"])), want)
	}
	for _, note := range shown {
		for _, host := range []string{"example", "xn--", "пример", "例", "рф", "भारत", "ไทย"} {
			if strings.Contains(note, host) {
				t.Fatalf("a host is left in the note %q", note)
			}
		}
	}
}
