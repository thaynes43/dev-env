package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/claudelogin"
)

type claudeTestTransport struct {
	Calls int
	Run   func(call int, refresh secretValue) (claudeTokens, error)
	Prof  *claudeProfile // the profile answer; nil answers errClaudeProfile
}

func (t *claudeTestTransport) refresh(_ context.Context, r secretValue, _ []string) (claudeTokens, error) {
	t.Calls++
	return t.Run(t.Calls, r)
}

func (t *claudeTestTransport) profile(context.Context, secretValue) (claudeProfile, error) {
	if t.Prof == nil {
		return claudeProfile{}, errClaudeProfile
	}
	return *t.Prof, nil
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

// claudeFixJournalUID is the synthetic incarnation of the fixture journal, so
// ordinary observation tests exercise the hub's UID checks too.
const claudeFixJournalUID = "synthetic-journal-incarnation"

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
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: jn.Namespace, Name: jn.Name, UID: claudeFixJournalUID}},
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

func TestClaudeWorkerLeaseGoneAfterPOSTStillSavesReplacement(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick() // generation 1 published
	f.clk.Step(6 * time.Hour)
	posted := false
	f.w.Fence = func(context.Context, time.Duration) error {
		if posted {
			return errors.New("synthetic: lease budget gone after the POST")
		}
		return nil
	}
	f.tr.Run = func(call int, old secretValue) (claudeTokens, error) {
		posted = true
		return rotated(call, old)
	}
	f.tick()
	r := f.journal(t)
	if f.tr.Calls != 1 || r.Stage != claudeReady || r.Access.Generation != 2 || r.Refresh.Reveal() != claudeNewRefreshCanary || f.liveGeneration(t) != 1 {
		t.Fatalf("replacement must be saved, and not published without a lease: calls=%d stage=%s gen=%d", f.tr.Calls, r.Stage, r.Access.Generation)
	}
	posted = false // the next leader republishes the saved replacement; no new POST
	f.tick()
	if f.tr.Calls != 1 || f.liveGeneration(t) != 2 {
		t.Fatal("saved replacement was not republished")
	}
}

func TestClaudeWorkerNewerLeaderJournalIsNotOverwritten(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.tick()
	f.clk.Step(6 * time.Hour)
	f.tr.Run = func(call int, old secretValue) (claudeTokens, error) {
		// While our POST is in flight a newer leader recovers the intent.
		other := &claudeWorker{Journal: f.w.Journal, Fence: func(context.Context, time.Duration) error { return nil }, Clock: f.clk, Log: logr.Discard()}
		other.needsLogin(context.Background(), f.journal(t).AttemptUID)
		return rotated(call, old)
	}
	f.tick()
	r := f.journal(t)
	if r.Stage != claudeNeedsLogin || r.Access.Generation != 1 || !r.Refresh.Empty() || f.liveGeneration(t) != 1 {
		t.Fatal("a newer leader's journal write was overwritten or published over")
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

// A stale POST callback must not tombstone or report another leader's intent.
func TestClaudeStaleRefreshDoesNotTombstoneNewIntent(t *testing.T) {
	f := newClaudeFix(t)
	f.w.Obs = NewLoginHub(ProviderClaude)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	f.tr.Run = func(_ int, _ secretValue) (claudeTokens, error) {
		next := f.journal(t)
		next.AttemptUID = "22222222-2222-4222-8222-222222222222"
		f.seed(t, next)
		return claudeTokens{}, errors.New("synthetic uncertain failure")
	}
	f.tick()
	got := f.journal(t)
	if got.Stage != claudeIntent || got.AttemptUID != "22222222-2222-4222-8222-222222222222" {
		t.Error("stale callback changed the new leader's intent")
	}
	if f.w.last == "needs-login" || f.w.Obs.Snapshot()[ProviderClaude].State == LoginNeedsLogin {
		t.Error("stale callback reported a false needs-login state")
	}
}

func TestClaudeIntentObservationRequiresFencedVerifiedCAS(t *testing.T) {
	for _, mode := range []string{"fence", "read", "cas"} {
		t.Run(mode, func(t *testing.T) {
			f := newClaudeFix(t)
			f.w.Obs = NewLoginHub(ProviderClaude)
			r := syntheticClaudeRecord(1)
			r.Stage, r.AttemptUID = claudeIntent, claudeUID
			f.seed(t, r)
			switch mode {
			case "fence":
				f.w.Fence = func(context.Context, time.Duration) error { return errors.New("synthetic") }
			case "read":
				f.c.Gets.Store(0)
				f.c.FailGetAt = 2 // tick sees intent, verification load fails
			case "cas":
				f.c.Fail.Store(true)
			}
			f.tick()
			if f.w.last == "needs-login" || f.w.Obs.Snapshot()[ProviderClaude].State == LoginNeedsLogin {
				t.Fatal("unverified intent reported as needs-login")
			}
			f.c.Fail.Store(false)
			f.c.FailGetAt = 0
			if f.journal(t).Stage != claudeIntent {
				t.Fatal("failed transition changed the intent")
			}
		})
	}
}

func TestClaudeUnsentRollbackBackoffAndRestart(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	f.tr.Run = func(int, secretValue) (claudeTokens, error) { return claudeTokens{}, errClaudeNotSent }
	f.tick()
	if f.journal(t).Stage != claudeReady || f.tr.Calls != 1 {
		t.Fatal("unsent intent was not rolled back")
	}
	f.tick()
	if f.tr.Calls != 1 {
		t.Fatal("unsent attempt retried without backoff")
	}
	f.clk.Step(time.Minute)
	f.tick()
	if f.tr.Calls != 2 || f.journal(t).Stage != claudeReady {
		t.Fatal("bounded retry did not preserve ready record")
	}
	other := &claudeWorker{Journal: f.w.Journal, Transport: f.tr, Publisher: f.w.Publisher, Fence: f.w.Fence, Clock: f.clk, Log: logr.Discard()}
	if _, r, err := other.load(context.Background()); err != nil || r.Stage != claudeReady {
		t.Fatal("rollback did not persist across worker restart")
	}
}

type claudeLostAckClient struct {
	client.Client
	patches int
	loseAt  int
}

func (c *claudeLostAckClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	c.patches++
	if c.patches == c.loseAt {
		return errors.New("synthetic lost acknowledgement")
	}
	return nil
}

func TestClaudeNeedsLoginLostAckDoesNotReportBeforeRead(t *testing.T) {
	f := newClaudeFix(t)
	f.w.Obs = NewLoginHub(ProviderClaude)
	r := syntheticClaudeRecord(1)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	f.seed(t, r)
	f.w.Journal.Client = &claudeLostAckClient{Client: f.c, loseAt: 1}
	f.tick()
	if f.w.last == "needs-login" || f.w.Obs.Snapshot()[ProviderClaude].State == LoginNeedsLogin {
		t.Fatal("ambiguous write was reported")
	}
	if f.journal(t).Stage != claudeNeedsLogin {
		t.Fatal("expected committed tombstone")
	}
	f.tick()
	if f.w.Obs.Snapshot()[ProviderClaude].State != LoginNeedsLogin {
		t.Fatal("persisted tombstone was not observed on later tick")
	}
}

func TestClaudeReplacementLostAckSurvivesRestartAndGenerationFloor(t *testing.T) {
	f := newClaudeFix(t)
	a := syntheticClaudeAccess(7, claudeNow, 8*time.Hour)
	if err := f.w.Publisher.publish(context.Background(), a, time.Time{}, claudeNow); err != nil {
		t.Fatal(err)
	}
	f.seed(t, syntheticClaudeRecord(1))
	f.w.Journal.Client = &claudeLostAckClient{Client: f.c, loseAt: 2}
	f.clk.Step(6 * time.Hour)
	f.tick()
	if f.tr.Calls != 1 || f.journal(t).Access.Generation != 8 || f.liveGeneration(t) != 7 {
		t.Fatal("lost acknowledgement did not preserve the replacement before publication")
	}
	other := &claudeWorker{Journal: f.w.Journal, Transport: f.tr, Publisher: f.w.Publisher, Fence: f.w.Fence, Clock: f.clk, Log: logr.Discard()}
	other.tick(context.Background())
	if f.tr.Calls != 1 || f.liveGeneration(t) != 8 {
		t.Fatal("restart failed to publish saved replacement without replay")
	}
}

func TestClaudeConcurrentWorkersDoNotOverwriteNewAdoption(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.tr.Run = func(int, secretValue) (claudeTokens, error) {
		close(started)
		<-release
		return claudeTokens{}, errors.New("synthetic uncertain")
	}
	go func() { defer close(done); f.tick() }()
	<-started
	other := &claudeWorker{Journal: f.w.Journal, Fence: f.w.Fence, Clock: f.clk, Log: logr.Discard()}
	other.tick(context.Background())
	adopted := syntheticClaudeRecord(2)
	f.seed(t, adopted)
	close(release)
	<-done
	got := f.journal(t)
	if got.Stage != claudeReady || got.Access.Generation != 2 || got.Refresh.Empty() {
		t.Fatal("stale callback overwrote newer adoption")
	}
}

func TestClaudePreDispatchCancellationRollsBack(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fences := 0
	f.w.Fence = func(context.Context, time.Duration) error {
		fences++
		if fences == 2 {
			cancel()
		}
		return nil
	}
	tr := newClaudeHTTPRefresh()
	tr.endpoint = "https://example.invalid/token"
	tr.Client = &http.Client{Transport: claudeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("request was dispatched")
		return nil, errors.New("synthetic")
	})}
	f.w.Transport = tr
	f.w.tick(ctx)
	if f.journal(t).Stage != claudeReady {
		t.Fatal("proven unsent request did not roll back")
	}
}

type claudeBeforePatchClient struct {
	client.Client
	before func()
}

func (c *claudeBeforePatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if c.before != nil {
		run := c.before
		c.before = nil
		run()
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestClaudeIntentCASCannotOverwriteNewerIntent(t *testing.T) {
	f := newClaudeFix(t)
	f.w.Obs = NewLoginHub(ProviderClaude)
	r := syntheticClaudeRecord(1)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	f.seed(t, r)
	f.w.Journal.Client = &claudeBeforePatchClient{Client: f.c, before: func() {
		j := &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}
		_, next, err := j.load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		next.AttemptUID = "22222222-2222-4222-8222-222222222222"
		saveClaude(t, j, next)
	}}
	f.tick()
	got := f.journal(t)
	if got.Stage != claudeIntent || got.AttemptUID != "22222222-2222-4222-8222-222222222222" {
		t.Fatal("stale CAS overwrote new intent")
	}
	if f.w.last == "needs-login" || f.w.Obs.Snapshot()[ProviderClaude].State == LoginNeedsLogin {
		t.Fatal("failed CAS was reported")
	}
}

func TestClaudeIntentReadbackFailureDoesNotReport(t *testing.T) {
	f := newClaudeFix(t)
	f.w.Obs = NewLoginHub(ProviderClaude)
	r := syntheticClaudeRecord(1)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	f.seed(t, r)
	f.c.Gets.Store(0)
	f.c.FailGetAt = 3
	f.tick()
	if f.w.last == "needs-login" || f.w.Obs.Snapshot()[ProviderClaude].State == LoginNeedsLogin {
		t.Fatal("unverified CAS was reported")
	}
	f.c.FailGetAt = 0
	if f.journal(t).Stage != claudeNeedsLogin {
		t.Fatal("expected committed tombstone")
	}
}

func TestClaudeFailureLogRedactsTransportError(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	var log string
	f.w.Log = funcr.New(func(prefix, args string) { log += prefix + args }, funcr.Options{})
	f.tr.Run = func(int, secretValue) (claudeTokens, error) {
		return claudeTokens{}, errors.New("PRIVATE_SENTINEL https://example.invalid/PRIVATE_SENTINEL")
	}
	f.tick()
	if strings.Contains(log, "PRIVATE_SENTINEL") || strings.Contains(log, claudeOldRefreshCanary) || strings.Contains(log, claudeUID) {
		t.Fatal("sensitive data escaped into log")
	}
	if !strings.Contains(log, "possibly-spent") || !strings.Contains(log, "duration_ms") || !strings.Contains(log, "phase") {
		t.Fatal("closed failure fields missing")
	}
}

// pauseClaudeReadback returns a snapshot and pauses before the old worker can
// consume it, allowing a new leader's journal adoption to interleave.
type pauseClaudeReadback struct {
	client.Client
	reads   atomic.Int32
	pauseAt int32
	paused  chan struct{}
	resume  chan struct{}
}

func (c *pauseClaudeReadback) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := c.Client.Get(ctx, key, obj, opts...)
	if err == nil && key.Name == DefaultClaudeJournalSecret && c.reads.Add(1) == c.pauseAt {
		close(c.paused)
		<-c.resume
	}
	return err
}

func TestClaudeObservationDoesNotOutliveNewLeaderAdoption(t *testing.T) {
	f := newClaudeFix(t)
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	r := syntheticClaudeRecord(1)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	f.seed(t, r)
	f.holdLease(t, "old")
	f.w.Fence = f.leaseFence(t, "old")
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 2, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.w.needsLogin(context.Background(), claudeUID) }()
	<-paused.paused
	f.holdLease(t, "new")
	adopted := syntheticClaudeRecord(2)
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: hub, Fence: f.leaseFence(t, "new"), Clock: f.clk, Log: logr.Discard()}
	secret, _, err := newWorker.load(context.Background())
	if err != nil || newWorker.Fence(context.Background(), claudeSaveTimeout+claudeSafetyMargin) != nil || newWorker.Journal.save(context.Background(), secret, adopted) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic adoption failed")
	}
	newWorker.observe(context.Background(), string(secret.UID), adopted)
	close(paused.resume)
	<-done
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginReady {
		t.Fatalf("stale observation after newer adoption: got %s, want Ready", got)
	}
}

// A persisted tombstone loaded by tick can also become stale before observe.
func TestClaudeTickObservationDoesNotOutliveNewLeaderAdoption(t *testing.T) {
	f := newClaudeFix(t)
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	r := syntheticClaudeRecord(1)
	r.Stage, r.Refresh, r.RefreshExpiresAt = claudeNeedsLogin, secretValue{}, time.Time{}
	f.seed(t, r)
	f.holdLease(t, "old")
	f.w.Fence = f.leaseFence(t, "old")
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 1, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.tick() }()
	<-paused.paused
	f.holdLease(t, "new")
	adopted := syntheticClaudeRecord(2)
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: hub, Fence: f.leaseFence(t, "new"), Clock: f.clk, Log: logr.Discard()}
	secret, _, err := newWorker.load(context.Background())
	if err != nil || newWorker.Fence(context.Background(), claudeSaveTimeout+claudeSafetyMargin) != nil || newWorker.Journal.save(context.Background(), secret, adopted) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic adoption failed")
	}
	newWorker.observe(context.Background(), string(secret.UID), adopted)
	close(paused.resume)
	<-done
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginReady {
		t.Fatalf("stale tick observation after newer adoption: got %s, want Ready", got)
	}
}

// Each keeper process owns its own hub. A newer observation in one hub must
// not be mistaken for ordering information available to the other hub.
func TestClaudeObservationIndependentHubsAfterAdoption(t *testing.T) {
	f := newClaudeFix(t)
	oldHub, newHub := NewLoginHub(ProviderClaude), NewLoginHub(ProviderClaude)
	f.w.Obs = oldHub
	r := syntheticClaudeRecord(1)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	f.seed(t, r)
	f.holdLease(t, "old")
	f.w.Fence = f.leaseFence(t, "old")
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 2, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.w.needsLogin(context.Background(), claudeUID) }()
	<-paused.paused
	// The old notifier has begun a pass while it still holds leadership.
	notices := newNoticeFix(t, time.Date(2026, 11, 3, 16, 0, 0, 0, time.UTC))
	notices.n.Hub = oldHub
	notices.n.VerifyClaude = verifyClaudeJournal(&claudeJournal{Client: f.c, Secret: f.w.Journal.Secret})
	notifierPaused, notifierResume, notifierDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	notices.n.Enabled = func() bool { close(notifierPaused); <-notifierResume; return true }
	go func() { defer close(notifierDone); notices.tick() }()
	<-notifierPaused
	f.holdLease(t, "new")
	adopted := syntheticClaudeRecord(2)
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: newHub, Fence: f.leaseFence(t, "new"), Clock: f.clk, Log: logr.Discard()}
	secret, _, err := newWorker.load(context.Background())
	if err != nil || newWorker.Journal.save(context.Background(), secret, adopted) != nil {
		close(paused.resume)
		<-done
		close(notifierResume)
		<-notifierDone
		t.Fatal("synthetic adoption failed")
	}
	newWorker.observe(context.Background(), string(secret.UID), adopted)
	close(paused.resume)
	<-done
	close(notifierResume)
	<-notifierDone
	if notices.fake.count() != 0 {
		t.Fatal("stale old-process observation sent an actionable notification after adoption")
	}
}

// Within one generation, the terminal observation must supersede an earlier
// Ready read even when the reader is paused across the terminal write.
func TestClaudeOldReadyObservationDoesNotOutliveTombstone(t *testing.T) {
	f := newClaudeFix(t)
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	f.seed(t, syntheticClaudeRecord(1))
	f.holdLease(t, "old")
	f.w.Fence = f.leaseFence(t, "old")
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 1, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.tick() }()
	<-paused.paused
	f.holdLease(t, "new")
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: hub, Fence: f.leaseFence(t, "new"), Clock: f.clk, Log: logr.Discard()}
	secret, intent, err := newWorker.load(context.Background())
	intent.Stage, intent.AttemptUID = claudeIntent, claudeUID
	if err != nil || newWorker.Journal.save(context.Background(), secret, intent) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic intent failed")
	}
	newWorker.needsLogin(context.Background(), claudeUID)
	close(paused.resume)
	<-done
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginNeedsLogin {
		t.Fatalf("stale Ready after tombstone: got %s, want NeedsLogin", got)
	}
}

// This uses only synthetic staged bytes and fake transport, but exercises the
// actual adoption method rather than a direct call to observe.
func TestClaudeStagedAdoptionOrdersOldTombstoneInSharedHub(t *testing.T) {
	f := newClaudeFix(t)
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	r := syntheticClaudeRecord(1)
	r.Stage, r.AttemptUID = claudeIntent, claudeUID
	f.seed(t, r)
	f.holdLease(t, "old")
	f.w.Fence = f.leaseFence(t, "old")
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 2, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.w.needsLogin(context.Background(), claudeUID) }()
	<-paused.paused
	f.holdLease(t, "new")
	dir := t.TempDir()
	stagedDir := filepath.Join(dir, claudeUID, ".claude")
	if err := os.MkdirAll(stagedDir, 0o700); err != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic staging setup failed")
	}
	if err := os.WriteFile(filepath.Join(dir, claudeUID, claudelogin.StagedCredentials), loginCredentials(t, nil), 0o600); err != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic staging setup failed")
	}
	f.tr.Prof = &claudeProfile{AccountUUID: r.Access.AccountUUID, OrganizationUUID: r.Access.OrganizationUUID}
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Transport: f.tr, Publisher: f.w.Publisher,
		Obs: hub, Fence: f.leaseFence(t, "new"), Clock: f.clk, Log: logr.Discard(), LoginDir: dir,
		attempt: claudeAttempt{uid: claudeUID, until: f.clk.Now().Add(time.Minute)}}
	result := newWorker.adopt(context.Background(), claudeUID)
	close(paused.resume)
	<-done
	if !result.OK || result.Generation != 2 {
		t.Fatal("synthetic staged adoption failed")
	}
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginReady {
		t.Fatalf("old tombstone displaced adopted Ready: %s", got)
	}
}

func TestClaudeNotifierVerifiesJournalBeforeNeedsLogin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stage   string
		missing bool
		want    int
	}{
		{name: "current ready", stage: claudeReady},
		{name: "current tombstone", stage: claudeNeedsLogin, want: 1},
		{name: "in flight", stage: claudeIntent},
		{name: "unavailable", stage: claudeNeedsLogin, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			r := syntheticClaudeRecord(1)
			r.Stage = tc.stage
			if tc.stage == claudeNeedsLogin {
				r.Refresh, r.RefreshExpiresAt = secretValue{}, time.Time{}
			}
			if tc.stage == claudeIntent {
				r.AttemptUID = claudeUID
			}
			f.seed(t, r)
			notices := newNoticeFix(t, claudeNow)
			notices.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
			name := f.w.Journal.Secret
			if tc.missing {
				name.Name = "missing-synthetic-journal"
			}
			notices.n.VerifyClaude = verifyClaudeJournal(&claudeJournal{Client: f.c, Secret: name})
			notices.tick()
			if got := notices.fake.count(); got != tc.want {
				t.Fatalf("synthetic notice count = %d, want %d", got, tc.want)
			}
		})
	}
}

// Once the authoritative read has linearized, a later adoption may overlap
// delivery. Cancellation still prevents a not-yet-started send.
func TestClaudeNotifierDecisionBeforeAdoptionDeliveryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		want   int
	}{
		{name: "live context may deliver", want: 1},
		{name: "canceled context suppresses", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			r := syntheticClaudeRecord(1)
			r.Stage, r.Refresh, r.RefreshExpiresAt = claudeNeedsLogin, secretValue{}, time.Time{}
			f.seed(t, r)
			notices := newNoticeFix(t, claudeNow)
			notices.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
			paused := &pauseClaudeReadback{Client: f.c, pauseAt: 1, paused: make(chan struct{}), resume: make(chan struct{})}
			notices.n.VerifyClaude = verifyClaudeJournal(&claudeJournal{Client: paused, Secret: f.w.Journal.Secret})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); notices.n.Tick(ctx) }()
			<-paused.paused
			newHub := NewLoginHub(ProviderClaude)
			newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: newHub, Clock: f.clk, Log: logr.Discard()}
			secret, _, err := newWorker.load(context.Background())
			adopted := syntheticClaudeRecord(2)
			if err != nil || newWorker.Journal.save(context.Background(), secret, adopted) != nil {
				close(paused.resume)
				<-done
				t.Fatal("synthetic adoption failed")
			}
			newWorker.observe(context.Background(), string(secret.UID), adopted)
			if tc.cancel {
				cancel()
			}
			close(paused.resume)
			<-done
			if got := notices.fake.count(); got != tc.want {
				t.Fatalf("synthetic delivery count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestClaudeNotifierDecisionMetadataAndFence(t *testing.T) {
	f := newClaudeFix(t)
	r := syntheticClaudeRecord(1)
	r.Stage, r.Refresh, r.RefreshExpiresAt = claudeNeedsLogin, secretValue{}, time.Time{}
	f.seed(t, r)
	d := verifyClaudeJournal(f.w.Journal)(context.Background())
	if d.status.State != LoginNeedsLogin || d.resourceVersion == "" || d.readStarted.IsZero() || d.readFinished.Before(d.readStarted) {
		t.Fatal("decision is missing its opaque record identity or read bounds")
	}
	notices := newNoticeFix(t, claudeNow)
	notices.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	notices.n.VerifyClaude = verifyClaudeJournal(f.w.Journal)
	notices.n.ClaudeFence = func(context.Context, time.Duration) error { return errClaudeLeadership }
	notices.tick()
	if notices.fake.count() != 0 {
		t.Fatal("notification escaped a failed leader fence")
	}
}

// The notifier must not act on its old hub snapshot when a newer Ready is
// committed before the authoritative journal decision read starts.
func TestClaudeNotifierOldSnapshotThenNewReadyBeforeDecision(t *testing.T) {
	f := newClaudeFix(t)
	r := syntheticClaudeRecord(1)
	r.Stage, r.Refresh, r.RefreshExpiresAt = claudeNeedsLogin, secretValue{}, time.Time{}
	f.seed(t, r)
	notices := newNoticeFix(t, claudeNow)
	notices.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
	decision := verifyClaudeJournal(f.w.Journal)
	snapshotTaken, resume := make(chan struct{}), make(chan struct{})
	notices.n.VerifyClaude = func(ctx context.Context) claudeNoticeDecision {
		close(snapshotTaken)
		<-resume
		return decision(ctx)
	}
	done := make(chan struct{})
	go func() { defer close(done); notices.tick() }()
	<-snapshotTaken
	secret, _, err := f.w.Journal.load(context.Background())
	if err != nil || f.w.Journal.save(context.Background(), secret, syntheticClaudeRecord(2)) != nil {
		close(resume)
		<-done
		t.Fatal("synthetic adoption failed")
	}
	close(resume)
	<-done
	if notices.fake.count() != 0 {
		t.Fatal("old hub snapshot caused a notice after Ready committed before decision")
	}
}

// A recreated journal is a new incarnation: its empty record must be visible
// even if this hub has already observed a high generation from the old UID.
func TestClaudeRecreatedJournalSupersedesOldIncarnation(t *testing.T) {
	f := newClaudeFix(t)
	ctx := context.Background()
	var old corev1.Secret
	if f.c.Get(ctx, f.w.Journal.Secret, &old) != nil {
		t.Fatal("synthetic journal missing")
	}
	old.UID = types.UID("synthetic-old-journal")
	if f.c.Update(ctx, &old) != nil {
		t.Fatal("synthetic UID setup failed")
	}
	f.seed(t, syntheticClaudeRecord(7))
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	f.tick()
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 1, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.tick() }()
	<-paused.paused
	if f.c.Delete(ctx, &old) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic deletion failed")
	}
	fresh := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: old.Namespace, Name: old.Name, UID: types.UID("synthetic-new-journal")}}
	if f.c.Create(ctx, fresh) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic recreation failed")
	}
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: hub, Clock: f.clk, Log: logr.Discard()}
	newWorker.tick(ctx)
	close(paused.resume)
	<-done
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginNeedsLogin {
		t.Fatalf("recreated journal and delayed old read: got %s, want NeedsLogin", got)
	}
	notices := newNoticeFix(t, claudeNow)
	notices.n.Hub = hub
	notices.n.VerifyClaude = verifyClaudeJournal(newWorker.Journal)
	notices.tick()
	if notices.fake.count() != 1 {
		t.Fatal("new empty journal did not reach notifier")
	}
}

// The old incarnation need not have produced an earlier hub observation: the
// paused read itself can be its first callback.
func TestClaudeUnseenOldIncarnationCannotReplaceRecreatedJournal(t *testing.T) {
	f := newClaudeFix(t)
	ctx := context.Background()
	var old corev1.Secret
	if f.c.Get(ctx, f.w.Journal.Secret, &old) != nil {
		t.Fatal("synthetic journal missing")
	}
	old.UID = types.UID("synthetic-unseen-old")
	if f.c.Update(ctx, &old) != nil {
		t.Fatal("synthetic UID setup failed")
	}
	f.seed(t, syntheticClaudeRecord(7))
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 1, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.tick() }()
	<-paused.paused
	if f.c.Delete(ctx, &old) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic deletion failed")
	}
	fresh := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: old.Namespace, Name: old.Name, UID: types.UID("synthetic-new")}}
	if f.c.Create(ctx, fresh) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic recreation failed")
	}
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: hub, Clock: f.clk, Log: logr.Discard()}
	newWorker.tick(ctx)
	close(paused.resume)
	<-done
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginNeedsLogin {
		t.Fatalf("unseen old incarnation displaced new: %s", got)
	}
}

// A previously unseen old UID must not replace a current new UID, even when
// read-start order and snapshot order do not provide an ordering token.
func TestClaudeCrossedReadStartsCannotResurrectOldIncarnation(t *testing.T) {
	f := newClaudeFix(t)
	ctx := context.Background()
	var old corev1.Secret
	if f.c.Get(ctx, f.w.Journal.Secret, &old) != nil {
		t.Fatal("synthetic journal missing")
	}
	old.UID = types.UID("synthetic-crossed-old")
	if f.c.Update(ctx, &old) != nil {
		t.Fatal("synthetic UID setup failed")
	}
	f.seed(t, syntheticClaudeRecord(7))
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	paused := &pauseClaudeReadback{Client: f.c, pauseAt: 1, paused: make(chan struct{}), resume: make(chan struct{})}
	f.w.Journal.Client = paused
	done := make(chan struct{})
	go func() { defer close(done); f.tick() }()
	<-paused.paused
	if f.c.Delete(ctx, &old) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic deletion failed")
	}
	fresh := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: old.Namespace, Name: old.Name, UID: types.UID("synthetic-crossed-new")}}
	if f.c.Create(ctx, fresh) != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic recreation failed")
	}
	newWorker := &claudeWorker{Journal: &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}, Obs: hub, Clock: f.clk, Log: logr.Discard()}
	secret, record, err := newWorker.load(ctx)
	if err != nil {
		close(paused.resume)
		<-done
		t.Fatal("synthetic read failed")
	}
	newWorker.observe(ctx, string(secret.UID), record)
	close(paused.resume)
	<-done
	if got := hub.Snapshot()[ProviderClaude].State; got != LoginNeedsLogin {
		t.Fatalf("crossed read resurrected old incarnation: %s", got)
	}
}
