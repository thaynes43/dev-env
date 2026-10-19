package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/claudeauth"
)

type claudeTestTransport struct {
	Calls int
	Run   func(call int, refresh secretValue) (claudeTokens, error)
}

func (t *claudeTestTransport) refresh(_ context.Context, r secretValue, _ []string) (claudeTokens, error) {
	t.Calls++
	return t.Run(t.Calls, r)
}

func (*claudeTestTransport) profile(context.Context, secretValue) (claudeProfile, error) {
	return claudeProfile{}, errClaudeProfile
}

// rotated is the default provider answer: a new access token per call and a
// rotated refresh token.
func rotated(call int, _ secretValue) (claudeTokens, error) {
	return claudeTokens{Access: newSecretValue(fmt.Sprintf("%s-%d", claudeAccessCanary, call+1)), Refresh: newSecretValue(claudeNewRefreshCanary),
		ExpiresIn: 8 * time.Hour, RefreshExpiresIn: 30 * 24 * time.Hour}, nil
}

// failPatchClient fails every Patch on one Secret while Fail is set.
type failPatchClient struct {
	client.Client
	Name      string
	Fail      atomic.Bool
	Gets      atomic.Int32 // Gets of the named Secret so far
	FailGetAt int32        // fail that numbered Get of the named Secret (0: none)
}

func (c *failPatchClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name == c.Name && c.FailGetAt != 0 && c.Gets.Add(1) == c.FailGetAt {
		return errors.New("synthetic get failure")
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *failPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if c.Fail.Load() && obj.GetName() == c.Name {
		return errors.New("synthetic patch failure")
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

type claudeFix struct {
	w     *claudeWorker
	tr    *claudeTestTransport
	c     *failPatchClient
	clk   *clocktesting.FakeClock
	live  types.NamespacedName
	lease types.NamespacedName
}

func newClaudeFix(t *testing.T) *claudeFix {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := errors.Join(corev1.AddToScheme(scheme), coordinationv1.AddToScheme(scheme)); err != nil {
		t.Fatal(err)
	}
	jn := types.NamespacedName{Namespace: "system", Name: DefaultClaudeJournalSecret}
	ln := types.NamespacedName{Namespace: "agents", Name: DefaultClaudeLiveSecret}
	lease := types.NamespacedName{Namespace: "system", Name: LeaderElectionID}
	c := &failPatchClient{Name: jn.Name, Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: jn.Namespace, Name: jn.Name}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ln.Namespace, Name: ln.Name, UID: "synthetic-live"}},
	).Build()}
	clk := clocktesting.NewFakeClock(claudeNow)
	tr := &claudeTestTransport{Run: rotated}
	f := &claudeFix{tr: tr, c: c, clk: clk, live: ln, lease: lease}
	f.w = &claudeWorker{Journal: &claudeJournal{Client: c, Secret: jn}, Transport: tr, Publisher: &claudePublicSecret{Client: c, Secret: ln},
		Fence: func(context.Context, time.Duration) error { return nil }, Clock: clk, Log: logr.Discard()}
	return f
}

func (f *claudeFix) seed(t *testing.T, r claudeRecord) {
	t.Helper()
	saveClaude(t, f.w.Journal, r)
}

func (f *claudeFix) journal(t *testing.T) claudeRecord {
	t.Helper()
	_, r, err := f.w.Journal.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *claudeFix) liveSecret(t *testing.T) corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := f.c.Get(context.Background(), f.live, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *claudeFix) liveGeneration(t *testing.T) uint64 {
	t.Helper()
	g, err := f.w.Publisher.generation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// leaseFence is the real fence against a Lease held by holder and renewed now.
func (f *claudeFix) leaseFence(t *testing.T, identity string) func(context.Context, time.Duration) error {
	t.Helper()
	return (&LeaseFence{Reader: f.c, Lease: f.lease, Identity: identity, Clock: f.clk, MaxAge: 10 * time.Second}).CheckBudget
}

func (f *claudeFix) holdLease(t *testing.T, holder string) {
	t.Helper()
	dur, now := int32(15), metav1.NewMicroTime(f.clk.Now())
	l := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: f.lease.Namespace, Name: f.lease.Name},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, RenewTime: &now, LeaseDurationSeconds: &dur}}
	ctx := context.Background()
	var cur coordinationv1.Lease
	if f.c.Get(ctx, f.lease, &cur) != nil {
		if err := f.c.Create(ctx, l); err != nil {
			t.Fatal(err)
		}
		return
	}
	cur.Spec = l.Spec
	if err := f.c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
}

func (f *claudeFix) tick() { f.w.tick(context.Background()) }

func TestClaudeWorkerRefreshesOncePerLifeBeforeExpiry(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick()
	if f.tr.Calls != 0 || f.liveGeneration(t) != 1 {
		t.Fatal("a fresh credential must be published, not refreshed")
	}
	f.clk.Step(5 * time.Hour) // life 8h: due at 5h20m
	f.tick()
	if f.tr.Calls != 0 {
		t.Fatal("refreshed before the last third of the life")
	}
	f.clk.Step(30 * time.Minute)
	f.tick()
	f.tick()
	r := f.journal(t)
	if f.tr.Calls != 1 || r.Access.Generation != 2 || r.Stage != claudeReady || r.Refresh.Reveal() != claudeNewRefreshCanary || f.liveGeneration(t) != 2 {
		t.Fatalf("one refresh expected: calls=%d gen=%d stage=%s", f.tr.Calls, r.Access.Generation, r.Stage)
	}
	if !r.Access.ExpiresAt.After(f.clk.Now().Add(7*time.Hour)) || r.Access.AccountUUID != "acct-1" {
		t.Fatal("expiry or account metadata not carried")
	}
}

type orderPublisher struct {
	claudePublisher
	OnPublish func(claudeauth.Access)
}

func (p *orderPublisher) publish(ctx context.Context, a claudeauth.Access, exp, now time.Time) error {
	p.OnPublish(a)
	return p.claudePublisher.publish(ctx, a, exp, now)
}

func TestClaudeWorkerPersistsReplacementBeforePublishing(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick() // publishes generation 1
	f.clk.Step(6 * time.Hour)
	var sawIntent, sawReady bool
	f.tr.Run = func(call int, old secretValue) (claudeTokens, error) {
		r := f.journal(t)
		sawIntent = r.Stage == claudeIntent && r.AttemptUID != "" && old.Reveal() == claudeOldRefreshCanary && f.liveGeneration(t) == 1
		return rotated(call, old)
	}
	f.w.Publisher = &orderPublisher{claudePublisher: f.w.Publisher, OnPublish: func(a claudeauth.Access) {
		r := f.journal(t)
		sawReady = a.Generation == 2 && r.Stage == claudeReady && r.Access.Generation == 2 && r.Refresh.Reveal() == claudeNewRefreshCanary
	}}
	f.tick()
	if !sawIntent || !sawReady || f.liveGeneration(t) != 2 {
		t.Fatalf("order violated: intent=%v persisted-before-publish=%v", sawIntent, sawReady)
	}
}

func TestClaudeWorkerPublishedBytesNeverContainRefreshToken(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick()
	f.clk.Step(6 * time.Hour)
	f.tick()
	s := f.liveSecret(t)
	text := strings.ToLower(fmt.Sprint(s.Data, s.Annotations, s.StringData))
	for _, bad := range []string{claudeOldRefreshCanary, claudeNewRefreshCanary, "refreshtoken", "refresh_token"} {
		if strings.Contains(text, strings.ToLower(bad)) {
			t.Fatalf("published Secret carries %q", bad)
		}
	}
	if a, err := claudeauth.Decode(s.Data[claudeauth.LiveKey], f.clk.Now()); err != nil || a.Generation != 2 {
		t.Fatal("generation 2 not published")
	}
}

func TestClaudeWorkerLeaseLostBeforePOST(t *testing.T) {
	t.Run("never held", func(t *testing.T) {
		f := newClaudeFix(t)
		f.seed(t, syntheticClaudeRecord(1))
		f.clk.Step(6 * time.Hour)
		f.w.Fence = func(context.Context, time.Duration) error { return errors.New("synthetic: not the leader") }
		f.tick()
		if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeReady || r.Access.Generation != 1 || r.Refresh.Reveal() != claudeOldRefreshCanary {
			t.Fatal("a non-leader changed the journal or called the provider")
		}
	})
	t.Run("lost after the intent, save still possible", func(t *testing.T) {
		f := newClaudeFix(t)
		f.seed(t, syntheticClaudeRecord(1))
		f.clk.Step(6 * time.Hour)
		calls := 0
		f.w.Fence = func(_ context.Context, d time.Duration) error {
			calls++
			if calls >= 2 && d >= claudeRequestBudget {
				return errors.New("synthetic: budget gone")
			}
			return nil
		}
		f.tick()
		if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeReady || r.Access.Generation != 1 || r.Refresh.Reveal() != claudeOldRefreshCanary {
			t.Fatal("an unsent intent was not returned to Ready")
		}
	})
	t.Run("lost after the intent, nothing can be written", func(t *testing.T) {
		f := newClaudeFix(t)
		f.seed(t, syntheticClaudeRecord(1))
		f.clk.Step(6 * time.Hour)
		calls := 0
		f.w.Fence = func(context.Context, time.Duration) error {
			if calls++; calls >= 2 {
				return errors.New("synthetic: lease lost")
			}
			return nil
		}
		f.tick()
		if f.tr.Calls != 0 || f.journal(t).Stage != claudeIntent {
			t.Fatal("expected an unsent RefreshIntent left in the journal")
		}
		f.w.Fence = func(context.Context, time.Duration) error { return nil }
		f.tick() // the next leader refuses to replay it
		if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeNeedsLogin || !r.Refresh.Empty() {
			t.Fatal("a recovered intent must become NeedsLogin without a POST")
		}
	})
}

func TestClaudeWorkerReReadFailureAfterIntentReturnsToReady(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	// Gets of the journal in one due tick: tick, refresh, then the re-read after
	// the intent (3rd), which fails once.
	f.c.Gets.Store(0)
	f.c.FailGetAt = 3
	f.tick()
	if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeReady || r.Access.Generation != 1 || r.Refresh.Reveal() != claudeOldRefreshCanary {
		t.Fatal("an unsent intent must go back to Ready, not to NeedsLogin")
	}
}

func TestClaudeWorkerShutdownAfterPOSTStillSavesAndPublishes(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick() // generation 1 published
	f.clk.Step(6 * time.Hour)
	// Like the real fence, refuse a cancelled context.
	f.w.Fence = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.tr.Run = func(call int, old secretValue) (claudeTokens, error) {
		cancel() // the keeper is told to stop right after the provider answered
		return rotated(call, old)
	}
	f.w.tick(ctx)
	r := f.journal(t)
	if f.tr.Calls != 1 || r.Stage != claudeReady || r.Access.Generation != 2 || r.Refresh.Reveal() != claudeNewRefreshCanary || f.liveGeneration(t) != 2 {
		t.Fatalf("replacement lost to shutdown: calls=%d stage=%s gen=%d", f.tr.Calls, r.Stage, r.Access.Generation)
	}
}

func TestClaudeWorkerTwoWorkersOneLeaseOnePOSTPerCycle(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	other := &claudeWorker{Journal: f.w.Journal, Transport: f.tr, Publisher: f.w.Publisher, Clock: f.clk, Log: logr.Discard()}
	f.w.Fence, other.Fence = f.leaseFence(t, "keeper-a"), f.leaseFence(t, "keeper-b")
	for cycle := 1; cycle <= 2; cycle++ {
		f.clk.Step(6 * time.Hour)
		f.holdLease(t, "keeper-a")
		other.tick(context.Background())
		f.tick()
		other.tick(context.Background())
		f.tick()
		if f.tr.Calls != cycle || f.journal(t).Access.Generation != uint64(cycle+1) || f.liveGeneration(t) != uint64(cycle+1) {
			t.Fatalf("cycle %d: want exactly %d POSTs, got %d", cycle, cycle, f.tr.Calls)
		}
	}
	// Leadership moves: the former leader stops, the new one carries on.
	f.clk.Step(6 * time.Hour)
	f.holdLease(t, "keeper-b")
	f.tick()
	other.tick(context.Background())
	if f.tr.Calls != 3 || f.journal(t).Access.Generation != 4 {
		t.Fatal("exactly one POST after leadership moved")
	}
}

func TestClaudeWorkerCrashAfterIntentIsNeedsLoginNeverReplayed(t *testing.T) {
	f := newClaudeFix(t)
	r := syntheticClaudeRecord(1)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	f.seed(t, r)
	f.clk.Step(6 * time.Hour)
	f.tick()
	f.tick()
	got := f.journal(t)
	if f.tr.Calls != 0 || got.Stage != claudeNeedsLogin || !got.Refresh.Empty() || f.liveGeneration(t) != 0 {
		t.Fatal("a recovered intent must tombstone without a POST or a publication")
	}
}

func TestClaudeWorkerPersistFailureAfterPOSTNeverReplays(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick() // generation 1 published
	f.clk.Step(6 * time.Hour)
	f.tr.Run = func(call int, old secretValue) (claudeTokens, error) {
		f.c.Fail.Store(true) // the process cannot save the replacement
		return rotated(call, old)
	}
	f.tick()
	f.c.Fail.Store(false)
	if f.tr.Calls != 1 || f.journal(t).Stage != claudeIntent || f.liveGeneration(t) != 1 {
		t.Fatal("nothing may be published when the replacement was not saved")
	}
	f.tick()
	f.tick()
	if r := f.journal(t); f.tr.Calls != 1 || r.Stage != claudeNeedsLogin || !r.Refresh.Empty() || f.liveGeneration(t) != 1 {
		t.Fatal("a possibly spent refresh token must not be replayed")
	}
}

func TestClaudeWorkerProviderRefusalIsNeedsLoginNoRetry(t *testing.T) {
	for name, run := range map[string]func(int, secretValue) (claudeTokens, error){
		"error": func(int, secretValue) (claudeTokens, error) { return claudeTokens{}, errClaudeRefresh },
		"other account": func(c int, o secretValue) (claudeTokens, error) {
			tok, _ := rotated(c, o)
			tok.AccountUUID = "acct-other"
			return tok, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.tr.Run = run
			f.clk.Step(6 * time.Hour)
			f.tick()
			f.tick()
			if r := f.journal(t); f.tr.Calls != 1 || r.Stage != claudeNeedsLogin || !r.Refresh.Empty() {
				t.Fatalf("calls=%d stage=%s", f.tr.Calls, r.Stage)
			}
		})
	}
}

func TestClaudeWorkerGenerationStartsAbovePublishedFloor(t *testing.T) {
	f := newClaudeFix(t)
	// The live Secret already holds generation 7; the journal was re-created at 1.
	a := syntheticClaudeAccess(7, claudeNow, 8*time.Hour)
	if err := f.w.Publisher.publish(context.Background(), a, time.Time{}, claudeNow); err != nil {
		t.Fatal(err)
	}
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	f.tick()
	if f.journal(t).Access.Generation != 8 || f.liveGeneration(t) != 8 {
		t.Fatal("next generation must exceed the published one")
	}
}

func TestClaudeWorkerThreeConsumersFollowTwoGenerations(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	homes := make([]string, 3)
	for i := range homes {
		homes[i] = filepath.Join(t.TempDir(), ".credentials.json")
		if err := os.WriteFile(homes[i], []byte(`{"mcpOAuth":{"server":{"k":1}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	follow := func(wantToken string, wantGen uint64) {
		t.Helper()
		for _, h := range homes {
			a, err := claudeauth.Decode(f.liveSecret(t).Data[claudeauth.LiveKey], f.clk.Now())
			if err != nil || a.Generation != wantGen {
				t.Fatalf("published generation: %v", err)
			}
			existing, _ := os.ReadFile(h)
			out, _, err := claudeauth.MergeCredentials(existing, a)
			if err != nil || os.WriteFile(h, out, 0o600) != nil {
				t.Fatal("merge failed")
			}
			var doc struct {
				O struct {
					Access  string `json:"accessToken"`
					Refresh string `json:"refreshToken"`
				} `json:"claudeAiOauth"`
				MCP json.RawMessage `json:"mcpOAuth"`
			}
			raw, _ := os.ReadFile(h)
			if json.Unmarshal(raw, &doc) != nil || doc.O.Access != wantToken || doc.O.Refresh != "" || len(doc.MCP) == 0 || strings.Contains(string(raw), "refresh-synthetic-canary") {
				t.Fatalf("consumer did not follow generation %d", wantGen)
			}
		}
	}
	f.tick()
	follow(claudeAccessCanary, 1)
	f.clk.Step(6 * time.Hour)
	f.tick()
	follow(claudeAccessCanary+"-2", 2)
	f.clk.Step(6 * time.Hour)
	f.tick()
	follow(claudeAccessCanary+"-3", 3)
}

func TestClaudeRunnableOffByDefault(t *testing.T) {
	if r := claudeRunnable(Options{}, nil, nil, clocktesting.NewFakeClock(claudeNow)); r != nil {
		t.Fatal("the worker is registered without --enable-claude-auth")
	}
	o := Options{Namespace: "system", GHTokenSecret: types.NamespacedName{Namespace: "agents", Name: "gh"}, ClaudeAuth: ClaudeOptions{Enabled: true}}
	r := claudeRunnable(o, claudeClient(t), &LeaseFence{}, clocktesting.NewFakeClock(claudeNow))
	w, ok := r.(*claudeWorker)
	if !ok || !w.NeedLeaderElection() || w.Journal.Secret != (types.NamespacedName{Namespace: "system", Name: DefaultClaudeJournalSecret}) {
		t.Fatal("enabled worker must be a leader-only runnable on the default journal Secret")
	}
	if p, ok := w.Publisher.(*claudePublicSecret); !ok || p.Secret != (types.NamespacedName{Namespace: "agents", Name: DefaultClaudeLiveSecret}) {
		t.Fatal("projection must go to the live Secret in the public namespace")
	}
}
