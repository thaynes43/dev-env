package keeper

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// journalSecret reads the fixture journal Secret as stored.
func (f *claudeFix) journalSecret(t *testing.T) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := f.c.Get(context.Background(), f.w.Journal.Secret, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

// setMarker overwrites the unsent marker; a nil value removes it.
func (f *claudeFix) setMarker(t *testing.T, value []byte) {
	t.Helper()
	s := f.journalSecret(t)
	if _, err := f.w.Journal.patch(context.Background(), s, map[string]any{claudeUnsentKey: value}); err != nil {
		t.Fatal(err)
	}
}

func claudeMarkerBytes(t *testing.T, journalUID, attempt string) []byte {
	t.Helper()
	raw, err := json.Marshal(claudeUnsentWire{Version: 1, JournalUID: journalUID, AttemptUID: attempt})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// markedUnsent leaves a RefreshIntent with its unsent marker: the transport
// proved the request unsent before the dispatch commit, and the rollback
// write failed. Nothing was sent.
func (f *claudeFix) markedUnsent(t *testing.T) claudeRecord {
	t.Helper()
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	f.notSent(func() { f.c.Fail.Store(true) })
	f.tick()
	f.c.Fail.Store(false)
	f.tr.Prepare = nil
	s := f.journalSecret(t)
	_, r, err := f.w.Journal.load(context.Background())
	if err != nil || r.Stage != claudeIntent || !claudeUnsentMarked(s, r) || f.tr.Calls != 0 {
		t.Fatal("setup: no marked unsent intent")
	}
	return r
}

// The intent and its unsent marker are written together; the dispatch commit
// removes the marker before the request, and the replacement leaves none.
func TestClaudeIntentCarriesTheUnsentMarkerUntilTheCommit(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	var markedBefore, markedDuring bool
	f.tr.Prepare = func(int) error {
		s := f.journalSecret(t)
		_, r, err := f.w.Journal.load(context.Background())
		markedBefore = err == nil && r.Stage == claudeIntent && claudeUnsentMarked(s, r)
		return nil
	}
	f.tr.Run = func(call int, r secretValue) (claudeTokens, error) {
		s := f.journalSecret(t)
		_, rec, err := f.w.Journal.load(context.Background())
		markedDuring = err != nil || rec.Stage != claudeIntent || len(s.Data[claudeUnsentKey]) != 0
		return rotated(call, r)
	}
	f.tick()
	if !markedBefore {
		t.Fatal("the intent was not stored with its unsent marker")
	}
	if markedDuring {
		t.Fatal("the request was sent while the journal still proved it unsent")
	}
	if r := f.journal(t); f.tr.Calls != 1 || r.Stage != claudeReady || r.Access.Generation != 2 || len(f.journalSecret(t).Data[claudeUnsentKey]) != 0 {
		t.Fatalf("refresh did not complete cleanly: calls=%d stage=%s", f.tr.Calls, r.Stage)
	}
}

// Q1's probe P2 with a fresh successor: the transport proved the request
// unsent and the rollback write failed. The journal's marker lets a keeper
// that never saw the outcome return the intent to Ready, without a provider
// call. Anything that is not that exact marker is tombstoned as before.
func TestClaudeUnsentMarkerLetsASuccessorRollBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		then func(t *testing.T, f *claudeFix, r claudeRecord) *claudeWorker
		want string
	}{
		{name: "fresh successor", want: claudeReady, then: func(t *testing.T, f *claudeFix, _ claudeRecord) *claudeWorker { return f.successorWorker() }},
		{name: "the proving worker", want: claudeReady, then: func(t *testing.T, f *claudeFix, _ claudeRecord) *claudeWorker { return f.w }},
		{name: "marker for another attempt", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix, _ claudeRecord) *claudeWorker {
			f.setMarker(t, claudeMarkerBytes(t, claudeFixJournalUID, claudeUID))
			return f.successorWorker()
		}},
		{name: "marker for another journal", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix, r claudeRecord) *claudeWorker {
			f.setMarker(t, claudeMarkerBytes(t, "another-synthetic-incarnation", r.AttemptUID))
			return f.successorWorker()
		}},
		{name: "corrupt marker", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix, _ claudeRecord) *claudeWorker {
			f.setMarker(t, []byte("{"))
			return f.successorWorker()
		}},
		{name: "no marker, as an older keeper writes", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix, _ claudeRecord) *claudeWorker {
			f.setMarker(t, nil)
			return f.successorWorker()
		}},
		{name: "recreated journal with copied data", want: claudeNeedsLogin, then: func(t *testing.T, f *claudeFix, _ claudeRecord) *claudeWorker {
			old := f.journalSecret(t)
			if err := f.c.Delete(context.Background(), old); err != nil {
				t.Fatal(err)
			}
			if err := f.c.Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: old.Namespace, Name: old.Name, UID: "synthetic-recreated-incarnation"}, Data: old.Data}); err != nil {
				t.Fatal(err)
			}
			return f.successorWorker()
		}},
		{name: "successor's fence refuses", want: claudeIntent, then: func(t *testing.T, f *claudeFix, _ claudeRecord) *claudeWorker {
			s := f.successorWorker()
			s.Fence = func(context.Context, time.Duration) error { return errors.New("synthetic leadership loss") }
			return s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			before := f.markedUnsent(t)
			tc.then(t, f, before).tick(context.Background())
			r := f.journal(t)
			if r.Stage != tc.want || f.tr.Calls != 0 {
				t.Fatalf("stage=%s calls=%d, want %s and no request", r.Stage, f.tr.Calls, tc.want)
			}
			if r.Stage == claudeReady && (r.Refresh.Reveal() != claudeOldRefreshCanary || r.Access.Generation != 1 || r.AttemptUID != "" || len(f.journalSecret(t).Data[claudeUnsentKey]) != 0) {
				t.Fatal("rollback did not restore the unspent record and remove the marker")
			}
		})
	}
}

// claudeCommitHookClient runs hook once, before the first journal patch that
// carries no record: the dispatch commit. loseAck makes that patch commit and
// then lose its acknowledgement.
type claudeCommitHookClient struct {
	client.Client
	hook       func()
	loseAck    bool
	failCommit bool // fail without applying
	done       bool
}

func claudeIsCommitPatch(obj client.Object, p client.Patch) bool {
	raw, err := p.Data(obj)
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &body) != nil {
		return false
	}
	_, record := body.Data[claudeJournalKey]
	_, marker := body.Data[claudeUnsentKey]
	return marker && !record
}

func (c *claudeCommitHookClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	if c.done || !claudeIsCommitPatch(obj, p) {
		return c.Client.Patch(ctx, obj, p, opts...)
	}
	c.done = true
	if c.hook != nil {
		c.hook()
	}
	if c.failCommit {
		return errors.New("synthetic commit failure")
	}
	err := c.Client.Patch(ctx, obj, p, opts...)
	if err == nil && c.loseAck {
		return errors.New("synthetic lost acknowledgement")
	}
	return err
}

// A successor's rollback and the owner's dispatch commit are compare-and-swaps
// on the same resourceVersion: when the successor wins, the owner's commit
// fails and nothing is sent.
func TestClaudeSuccessorRollbackBeatsTheDispatchCommit(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	f.w.Journal = &claudeJournal{Client: &claudeCommitHookClient{Client: f.c, hook: func() {
		f.successorWorker().tick(context.Background())
	}}, Secret: f.w.Journal.Secret}
	f.tick()
	if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeReady || r.Refresh.Reveal() != claudeOldRefreshCanary || r.Access.Generation != 1 {
		t.Fatalf("owner dispatched after a successor rolled back: calls=%d stage=%s", f.tr.Calls, r.Stage)
	}
}

// A lost acknowledgement on the dispatch commit is read back. If the journal
// shows the commit, the request is sent and the replacement saved on the read
// Secret. If the read fails, nothing is sent: the proving worker rolls back,
// and a restart, which finds no marker, tombstones (a recorded residual).
func TestClaudeDispatchCommitLostAck(t *testing.T) {
	t.Run("read back as committed", func(t *testing.T) {
		f := newClaudeFix(t)
		f.seed(t, syntheticClaudeRecord(1))
		f.clk.Step(6 * time.Hour)
		f.w.Journal = &claudeJournal{Client: &claudeCommitHookClient{Client: f.c, loseAck: true}, Secret: f.w.Journal.Secret}
		f.tick()
		if r := f.journal(t); f.tr.Calls != 1 || r.Stage != claudeReady || r.Access.Generation != 2 {
			t.Fatalf("committed attempt was not completed: calls=%d stage=%s", f.tr.Calls, r.Stage)
		}
	})
	for _, tc := range []struct {
		name          string
		rollbackFails bool
		then          func(f *claudeFix) *claudeWorker
		want          string
	}{
		{name: "read fails, the proving worker rolls back", then: func(f *claudeFix) *claudeWorker { return f.w }, want: claudeReady},
		{name: "read fails, rollback fails, a restart tombstones", rollbackFails: true, then: func(f *claudeFix) *claudeWorker { return f.successorWorker() }, want: claudeNeedsLogin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			// Journal Gets: the tick's load, refresh's load, the re-read, then
			// the commit's read back.
			f.c.FailGetAt = 4
			f.w.Journal = &claudeJournal{Client: &claudeCommitHookClient{Client: f.c, loseAck: true}, Secret: f.w.Journal.Secret}
			if tc.rollbackFails {
				f.w.Fence = func(_ context.Context, budget time.Duration) error {
					if budget == claudeSaveTimeout+claudeSafetyMargin {
						return errors.New("synthetic rollback refusal")
					}
					return nil
				}
			}
			f.tick()
			if f.tr.Calls != 0 || f.journal(t).Stage != map[bool]string{false: claudeReady, true: claudeIntent}[tc.rollbackFails] {
				t.Fatalf("setup: calls=%d stage=%s", f.tr.Calls, f.journal(t).Stage)
			}
			tc.then(f).tick(context.Background())
			if got := f.journal(t).Stage; got != tc.want || f.tr.Calls != 0 {
				t.Fatalf("stage=%s calls=%d, want %s and no request", got, f.tr.Calls, tc.want)
			}
		})
	}
}

// A commit that failed without applying leaves the marker, which the read back
// sees: nothing is sent, and the attempt is rolled back.
func TestClaudeFailedCommitSendsNothing(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	f.w.Journal = &claudeJournal{Client: &claudeCommitHookClient{Client: f.c, failCommit: true}, Secret: f.w.Journal.Secret}
	f.tick()
	if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeReady || r.Refresh.Reveal() != claudeOldRefreshCanary || r.Access.Generation != 1 {
		t.Fatalf("calls=%d stage=%s, want no request and a rollback", f.tr.Calls, r.Stage)
	}
}

// The commit's read back accepts only this attempt's intent. If another
// writer replaced it with a different attempt's unmarked intent, the commit
// fails, the read back refuses that intent, and nothing is sent or written.
func TestClaudeCommitReadBackRefusesAnotherAttempt(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	other := &claudeJournal{Client: f.c, Secret: f.w.Journal.Secret}
	f.w.Journal = &claudeJournal{Client: &claudeCommitHookClient{Client: f.c, hook: func() {
		s, r, err := other.load(context.Background())
		if err != nil || r.Stage != claudeIntent {
			t.Error("setup: no intent to replace")
			return
		}
		r.AttemptUID = claudeUID // another attempt's intent, saved without a marker
		if err := other.save(context.Background(), s, r); err != nil {
			t.Error(err)
		}
	}}, Secret: f.w.Journal.Secret}
	f.tick()
	if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeIntent || r.AttemptUID != claudeUID {
		t.Fatalf("calls=%d stage=%s: the read back accepted another attempt", f.tr.Calls, r.Stage)
	}
}

// claudeGetHookClient runs hook once, before the numbered Get of the journal.
type claudeGetHookClient struct {
	client.Client
	name string
	at   int
	gets int
	hook func()
}

func (c *claudeGetHookClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name == c.name {
		if c.gets++; c.gets == c.at && c.hook != nil {
			c.hook()
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// A rollback on the strength of the marker alone needs the marker on its own
// read: if the owner's dispatch commit lands between the successor's tick and
// its rollback, the request may be in flight, and the successor writes
// nothing.
func TestClaudeMarkedRollbackNeedsTheMarkerOnItsRead(t *testing.T) {
	f := newClaudeFix(t)
	f.markedUnsent(t)
	s := f.successorWorker()
	// Journal Gets: the successor's tick load, then its rollback's load.
	s.Journal = &claudeJournal{Client: &claudeGetHookClient{Client: f.c, name: f.w.Journal.Secret.Name, at: 2, hook: func() {
		if _, err := f.w.Journal.clearUnsent(context.Background(), f.journalSecret(t)); err != nil {
			t.Error(err)
		}
	}}, Secret: f.w.Journal.Secret}
	s.tick(context.Background())
	if r := f.journal(t); r.Stage != claudeIntent || f.tr.Calls != 0 {
		t.Fatalf("stage=%s: a successor rolled back an attempt whose commit had landed", r.Stage)
	}
}

// Residual pinned: once the dispatch commit is stored, the journal no longer
// proves the attempt unsent. If the keeper stops before the request (a crash),
// a successor tombstones, though the token may be unspent: without provider
// idempotency nothing can tell the two apart.
func TestClaudeCrashAfterTheDispatchCommitTombstones(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	s, prev, err := f.w.Journal.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	intent := prev
	intent.Stage, intent.AttemptUID = claudeIntent, claudeUID
	if err := f.w.Journal.saveIntent(context.Background(), s, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.Journal.clearUnsent(context.Background(), f.journalSecret(t)); err != nil {
		t.Fatal(err)
	}
	f.successorWorker().tick(context.Background())
	if r := f.journal(t); r.Stage != claudeNeedsLogin || !r.Refresh.Empty() || f.tr.Calls != 0 {
		t.Fatalf("a committed attempt was not tombstoned: stage=%s", r.Stage)
	}
}

type claudeBudgetEvent struct {
	kind      string // fence, intent, commit, record, request
	budget    time.Duration
	cancelled bool
}

// claudeEventClient records the journal patches in order.
type claudeEventClient struct {
	client.Client
	mu       *sync.Mutex
	events   *[]claudeBudgetEvent
	onCommit func()
}

func (c *claudeEventClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	kind := "record"
	if raw, err := p.Data(obj); err == nil {
		var body struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &body) == nil {
			_, rec := body.Data[claudeJournalKey]
			m, mark := body.Data[claudeUnsentKey]
			switch {
			case mark && !rec:
				kind = "commit"
			case rec && mark && string(m) != "null":
				kind = "intent"
			}
		}
	}
	if kind == "commit" && c.onCommit != nil {
		c.onCommit()
	}
	c.mu.Lock()
	*c.events = append(*c.events, claudeBudgetEvent{kind: kind})
	c.mu.Unlock()
	return c.Client.Patch(ctx, obj, p, opts...)
}

// The Lease arithmetic of the dispatch commit, against the 10 s freshness
// bound of the real fence: the fence before the commit and the one after it
// each need claudeRequestBudget (7 s). Before the commit that covers its 2 s
// save and the 2 s margin; after it, the 3 s request, the 2 s replacement save
// and the 2 s margin. The fence after the commit and the request run detached
// from shutdown, so a cancellation during the commit still completes the
// attempt.
func TestClaudeDispatchCommitLeaseBudget(t *testing.T) {
	const freshness = 10 * time.Second // LeaseFence's default MaxAge
	if claudeRequestBudget != 7*time.Second {
		t.Fatal("the request budget is not 7 s")
	}
	if claudeRequestTime+claudeSaveTimeout+claudeSafetyMargin != claudeRequestBudget {
		t.Fatal("the request budget is not 3 s request + 2 s save + 2 s margin")
	}
	if claudeSaveTimeout+claudeSafetyMargin > claudeRequestBudget || claudeRequestBudget >= freshness {
		t.Fatal("a fence budget does not fit the Lease freshness bound")
	}
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	f.holdLease(t, "keeper-a")
	lease := f.leaseFence(t, "keeper-a")
	var mu sync.Mutex
	var events []claudeBudgetEvent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.w.Fence = func(c context.Context, budget time.Duration) error {
		mu.Lock()
		events = append(events, claudeBudgetEvent{kind: "fence", budget: budget, cancelled: c.Err() != nil})
		mu.Unlock()
		return lease(c, budget)
	}
	f.w.Journal = &claudeJournal{Client: &claudeEventClient{Client: f.c, mu: &mu, events: &events, onCommit: cancel}, Secret: f.w.Journal.Secret}
	f.tr.Run = func(call int, r secretValue) (claudeTokens, error) {
		mu.Lock()
		events = append(events, claudeBudgetEvent{kind: "request"})
		mu.Unlock()
		return rotated(call, r)
	}
	f.w.tick(ctx)
	want := []claudeBudgetEvent{
		{kind: "fence", budget: claudeRequestBudget},
		{kind: "intent"},
		{kind: "fence", budget: claudeRequestBudget},
		{kind: "commit"},
		{kind: "fence", budget: claudeRequestBudget},
		{kind: "request"},
		{kind: "record"},
	}
	mu.Lock()
	got := append([]claudeBudgetEvent(nil), events...)
	mu.Unlock()
	if len(got) < len(want) {
		t.Fatalf("events %v, want prefix %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
	if r := f.journal(t); f.tr.Calls != 1 || r.Stage != claudeReady || r.Access.Generation != 2 {
		t.Fatalf("a shutdown during the commit stranded the attempt: calls=%d stage=%s", f.tr.Calls, r.Stage)
	}
}

// The fence after the commit refuses: nothing is sent, and the proving worker
// rolls the committed intent back from its own proof.
func TestClaudeFenceAfterTheCommitSendsNothing(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	fences := 0
	f.w.Fence = func(context.Context, time.Duration) error {
		if fences++; fences == 3 {
			return errors.New("synthetic leadership loss after the commit")
		}
		return nil
	}
	f.tick()
	if r := f.journal(t); f.tr.Calls != 0 || r.Stage != claudeReady || r.Refresh.Reveal() != claudeOldRefreshCanary || r.Access.Generation != 1 {
		t.Fatalf("calls=%d stage=%s, want no request and a rollback", f.tr.Calls, r.Stage)
	}
}

// claudeNoCommitTransport breaks the transport contract: it answers without
// running the dispatch commit.
type claudeNoCommitTransport struct{ calls int }

func (t *claudeNoCommitTransport) refresh(_ context.Context, r secretValue, _ []string, _ func() error) (claudeTokens, error) {
	t.calls++
	return rotated(t.calls, r)
}

func (*claudeNoCommitTransport) profile(context.Context, secretValue) (claudeProfile, error) {
	return claudeProfile{}, errClaudeProfile
}

// Even a transport that sends without the dispatch commit never leaves the
// unsent marker behind a spent token: the replacement is saved without it, or
// the attempt is tombstoned.
func TestClaudeSendWithoutCommitNeverLeavesTheMarker(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failSave bool
		want     string
	}{
		{name: "replacement saved", want: claudeReady},
		{name: "replacement save fails", failSave: true, want: claudeNeedsLogin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			f.w.Transport = &claudeNoCommitTransport{}
			f.w.Journal = &claudeJournal{Client: &claudeFailReadyClient{Client: f.c, name: f.w.Journal.Secret.Name, fail: tc.failSave}, Secret: f.w.Journal.Secret}
			f.tick()
			if r := f.journal(t); r.Stage != tc.want || len(f.journalSecret(t).Data[claudeUnsentKey]) != 0 {
				t.Fatalf("stage=%s marker=%q, want %s and no marker", r.Stage, f.journalSecret(t).Data[claudeUnsentKey], tc.want)
			}
		})
	}
}

// claudePatchedStage reads the record stage a journal merge patch writes, or
// "" when it writes none.
func claudePatchedStage(obj client.Object, p client.Patch) string {
	raw, err := p.Data(obj)
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &body) != nil {
		return ""
	}
	var encoded string
	if v, ok := body.Data[claudeJournalKey]; !ok || json.Unmarshal(v, &encoded) != nil {
		return ""
	}
	rec, err := base64.StdEncoding.DecodeString(encoded)
	var w claudeRecordWire
	if err != nil || json.Unmarshal(rec, &w) != nil {
		return ""
	}
	return w.Stage
}

// claudeFailReadyClient fails every journal patch that writes a Ready record
// while Fail is set: a rollback that keeps failing.
type claudeFailReadyClient struct {
	client.Client
	name string
	fail bool
}

func (c *claudeFailReadyClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	if c.fail && obj.GetName() == c.name && claudePatchedStage(obj, p) == claudeReady {
		return errors.New("synthetic rollback write failure")
	}
	return c.Client.Patch(ctx, obj, p, opts...)
}
