package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Every reading here is synthetic, built from the shapes in DESIGN-059 4.1
// and 4.2 (a Codex rollout window, a Claude statusline window). None comes
// from a provider or a real account.

const sessionAllowanceTestName = "dev-env-1110-000930"

// allowanceSession adds a session of the given agent and mode with its pod,
// then moves the clock ten minutes past the pod's creation.
func allowanceSession(t *testing.T, f *fixture, name string, agent v1alpha1.AgentKind, mode v1alpha1.SessionMode) string {
	t.Helper()
	tok := f.sessionPod(name, "full", 0)
	var s v1alpha1.AgentSession
	ctx := context.Background()
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	s.Spec.Agent, s.Spec.Mode = agent, mode
	if err := f.c.Update(ctx, &s); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(10 * time.Minute)
	return tok
}

func sessionAllowanceConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: SessionAllowanceNamespace, Name: SessionAllowanceName}}
}

func enableSessionAllowances(f *fixture) *SessionAllowanceStore {
	store := &SessionAllowanceStore{Writer: f.c, Live: f.c}
	f.srv.SessionAllowances = store
	f.h = f.srv.Handler()
	return store
}

func codexReading(at time.Time, used string, reset time.Time) *protocol.AllowanceReading {
	return &protocol.AllowanceReading{Provider: protocol.AgentCodex, Source: protocol.AllowanceSourceCodexRollout, ReadAt: at,
		Windows: []protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: used, ResetsAt: reset.Truncate(time.Second)}}}
}

func claudeReading(at time.Time, used string, reset time.Time) *protocol.AllowanceReading {
	return &protocol.AllowanceReading{Provider: protocol.AgentClaude, Source: protocol.AllowanceSourceClaudeStatusline, ReadAt: at,
		Windows: []protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: used, ResetsAt: reset.Truncate(time.Second)}}}
}

func beatWith(t *testing.T, f *fixture, name, tok string, r *protocol.AllowanceReading) {
	t.Helper()
	st := status(name)
	st.Allowance = r
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, st); w.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
}

func storedEntry(t *testing.T, f *fixture, agent string) (sessionAllowanceEntry, bool) {
	t.Helper()
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: SessionAllowanceNamespace, Name: SessionAllowanceName}, &cm); err != nil {
		t.Fatal(err)
	}
	raw, ok := cm.Data[sessionAllowanceKey(agent)]
	if !ok {
		return sessionAllowanceEntry{}, false
	}
	var e sessionAllowanceEntry
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("entry %q: %v", raw, err)
	}
	return e, true
}

// Default off: with no store, a heartbeat that carries a reading writes
// nothing to the ConfigMap and patches the session exactly as one without it.
func TestSessionAllowancesDefaultOff(t *testing.T) {
	f := newFixture(t, sessionAllowanceConfigMap())
	tok := allowanceSession(t, f, sessionAllowanceTestName, v1alpha1.AgentCodex, v1alpha1.ModeTask)
	beatWith(t, f, sessionAllowanceTestName, tok, nil)
	without := f.session(sessionAllowanceTestName).Status
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now.Add(-time.Minute), "65", f.now.Add(30*time.Hour)))
	with := f.session(sessionAllowanceTestName).Status
	a, _ := json.Marshal(without)
	b, _ := json.Marshal(with)
	if string(a) != string(b) {
		t.Fatalf("the reading changed the session status:\n%s\n%s", a, b)
	}
	var cm corev1.ConfigMap
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: SessionAllowanceNamespace, Name: SessionAllowanceName}, &cm); err != nil || len(cm.Data) != 0 {
		t.Fatalf("the ConfigMap was written while off: %v %v", cm.Data, err)
	}
}

func TestSessionAllowancesStore(t *testing.T) {
	f := newFixture(t, sessionAllowanceConfigMap())
	tok := allowanceSession(t, f, sessionAllowanceTestName, v1alpha1.AgentCodex, v1alpha1.ModeTask)
	enableSessionAllowances(f)
	reset := f.now.Add(30 * time.Hour)
	first := f.now.Add(-time.Minute)
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(first, "65", reset))
	e, ok := storedEntry(t, f, protocol.AgentCodex)
	if !ok || e.Schema != SessionAllowanceSchema || len(e.Readings) != 1 {
		t.Fatalf("entry %+v %v", e, ok)
	}
	got := e.Readings[0]
	if got.Session != sessionAllowanceTestName || got.Source != protocol.AllowanceSourceCodexRollout || !got.ReadAt.Equal(first) || !got.ReportedAt.Equal(f.now) || got.Windows[0].UsedPercent != "65" {
		t.Fatalf("stored %+v", got)
	}
	if _, ok := storedEntry(t, f, protocol.AgentClaude); ok {
		t.Fatal("a Claude entry from a Codex reading")
	}

	// Within a minute of the last reading from this session: not taken.
	f.now = f.now.Add(30 * time.Second)
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now, "66", reset))
	if e, _ := storedEntry(t, f, protocol.AgentCodex); len(e.Readings) != 1 || e.Readings[0].Windows[0].UsedPercent != "65" {
		t.Fatalf("rate limit: %+v", e)
	}
	// A minute later, a new percent with the same reset: held for five minutes.
	f.now = f.now.Add(time.Minute)
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now, "67", reset))
	if e, _ := storedEntry(t, f, protocol.AgentCodex); len(e.Readings) != 1 || e.Readings[0].Windows[0].UsedPercent != "65" {
		t.Fatalf("write limit: %+v", e)
	}
	// Five minutes after the write: stored, and the last two are kept.
	f.now = f.now.Add(4 * time.Minute)
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now, "68", reset))
	e, _ = storedEntry(t, f, protocol.AgentCodex)
	if len(e.Readings) != 2 || e.Readings[0].Windows[0].UsedPercent != "68" || e.Readings[1].Windows[0].UsedPercent != "65" {
		t.Fatalf("after five minutes: %+v", e)
	}
	// A changed reset is written sooner, and the oldest reading drops off.
	f.now = f.now.Add(time.Minute)
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now, "1", f.now.Add(7*24*time.Hour)))
	e, _ = storedEntry(t, f, protocol.AgentCodex)
	if len(e.Readings) != 2 || e.Readings[0].Windows[0].UsedPercent != "1" || e.Readings[1].Windows[0].UsedPercent != "68" {
		t.Fatalf("after a reset: %+v", e)
	}
	// An older reading never replaces a newer one, even after five minutes.
	f.now = f.now.Add(10 * time.Minute)
	beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now.Add(-20*time.Minute), "99", reset))
	if e, _ := storedEntry(t, f, protocol.AgentCodex); e.Readings[0].Windows[0].UsedPercent != "1" {
		t.Fatalf("an older reading won: %+v", e)
	}
}

func TestSessionAllowancesBounds(t *testing.T) {
	type tc struct {
		agent   v1alpha1.AgentKind
		mode    v1alpha1.SessionMode
		reading func(now time.Time) *protocol.AllowanceReading
	}
	for name, c := range map[string]tc{
		"provider is not the session's agent": {v1alpha1.AgentCodex, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return claudeReading(now.Add(-time.Minute), "1", now.Add(time.Hour))
		}},
		"claude reading from a headless task": {v1alpha1.AgentClaude, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return claudeReading(now.Add(-time.Minute), "1", now.Add(time.Hour))
		}},
		"cursor session": {v1alpha1.AgentCursor, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return codexReading(now.Add(-time.Minute), "1", now.Add(time.Hour))
		}},
		"before the pod started": {v1alpha1.AgentCodex, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return codexReading(now.Add(-11*time.Minute), "1", now.Add(time.Hour))
		}},
		"in the future": {v1alpha1.AgentCodex, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return codexReading(now.Add(2*time.Minute), "1", now.Add(time.Hour))
		}},
		"percent over 100": {v1alpha1.AgentCodex, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return codexReading(now.Add(-time.Minute), "101", now.Add(time.Hour))
		}},
		"reset before the reading": {v1alpha1.AgentCodex, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return codexReading(now.Add(-time.Minute), "1", now.Add(-2*time.Minute))
		}},
		"reset past one window": {v1alpha1.AgentCodex, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			return codexReading(now.Add(-time.Minute), "1", now.Add(8*24*time.Hour))
		}},
		"source of the other provider": {v1alpha1.AgentCodex, v1alpha1.ModeTask, func(now time.Time) *protocol.AllowanceReading {
			r := codexReading(now.Add(-time.Minute), "1", now.Add(time.Hour))
			r.Source = protocol.AllowanceSourceClaudeStatusline
			return r
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, sessionAllowanceConfigMap())
			tok := allowanceSession(t, f, sessionAllowanceTestName, c.agent, c.mode)
			enableSessionAllowances(f)
			beatWith(t, f, sessionAllowanceTestName, tok, c.reading(f.now))
			for _, agent := range sessionAllowanceProviders {
				if e, ok := storedEntry(t, f, agent); ok {
					t.Fatalf("stored %+v", e)
				}
			}
		})
	}
	t.Run("a little ahead is stored at the operator's time", func(t *testing.T) {
		f := newFixture(t, sessionAllowanceConfigMap())
		tok := allowanceSession(t, f, sessionAllowanceTestName, v1alpha1.AgentClaude, v1alpha1.ModeRemote)
		enableSessionAllowances(f)
		beatWith(t, f, sessionAllowanceTestName, tok, claudeReading(f.now.Add(20*time.Second), "34", f.now.Add(50*time.Hour)))
		e, ok := storedEntry(t, f, protocol.AgentClaude)
		if !ok || !e.Readings[0].ReadAt.Equal(f.now) || e.Readings[0].Source != protocol.AllowanceSourceClaudeStatusline {
			t.Fatalf("%+v %v", e, ok)
		}
	})
}

func TestSessionAllowancesStoreFailuresKeepTheHeartbeat(t *testing.T) {
	t.Run("no ConfigMap", func(t *testing.T) {
		f := newFixture(t)
		tok := allowanceSession(t, f, sessionAllowanceTestName, v1alpha1.AgentCodex, v1alpha1.ModeTask)
		enableSessionAllowances(f)
		beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now.Add(-time.Minute), "65", f.now.Add(30*time.Hour)))
		var cm corev1.ConfigMap
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: SessionAllowanceNamespace, Name: SessionAllowanceName}, &cm); !apierrors.IsNotFound(err) {
			t.Fatalf("the operator created the ConfigMap: %v", err)
		}
		if f.session(sessionAllowanceTestName).Status.Agent == nil {
			t.Fatal("the heartbeat was not recorded")
		}
	})
	t.Run("a conflicting write", func(t *testing.T) {
		f := newFixtureWith(t, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if obj.GetName() == SessionAllowanceName {
				return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, obj.GetName(), nil)
			}
			return c.Update(ctx, obj, opts...)
		}}, sessionAllowanceConfigMap())
		tok := allowanceSession(t, f, sessionAllowanceTestName, v1alpha1.AgentCodex, v1alpha1.ModeTask)
		store := enableSessionAllowances(f)
		beatWith(t, f, sessionAllowanceTestName, tok, codexReading(f.now.Add(-time.Minute), "65", f.now.Add(30*time.Hour)))
		if _, ok := storedEntry(t, f, protocol.AgentCodex); ok {
			t.Fatal("stored through a conflict")
		}
		if len(store.written) != 0 {
			t.Fatalf("a failed write was remembered: %+v", store.written)
		}
	})
}

// Two operator replicas share the ConfigMap: one that has not written yet
// still sees the other's newer reading and keeps it.
func TestSessionAllowancesTwoReplicas(t *testing.T) {
	f := newFixture(t, sessionAllowanceConfigMap())
	allowanceSession(t, f, sessionAllowanceTestName, v1alpha1.AgentCodex, v1alpha1.ModeTask)
	ctx := context.Background()
	sess := f.session(sessionAllowanceTestName)
	var pod corev1.Pod
	if err := f.c.Get(ctx, client.ObjectKey{Namespace: sessionNS, Name: sessionAllowanceTestName}, &pod); err != nil {
		t.Fatal(err)
	}
	a, b := &SessionAllowanceStore{Writer: f.c, Live: f.c}, &SessionAllowanceStore{Writer: f.c, Live: f.c}
	reset := f.now.Add(30 * time.Hour)
	if err := a.Record(ctx, sess, &pod, *codexReading(f.now.Add(-time.Minute), "50", reset), f.now); err != nil {
		t.Fatal(err)
	}
	if err := b.Record(ctx, sess, &pod, *codexReading(f.now.Add(-2*time.Minute), "40", reset), f.now); !errors.Is(err, errSessionAllowanceHeld) {
		t.Fatalf("replica b: %v", err)
	}
	newest, err := b.Newest(ctx)
	if err != nil || newest[protocol.AgentCodex].Windows[0].UsedPercent != "50" {
		t.Fatalf("newest %+v %v", newest, err)
	}
}

func TestSessionAllowanceEntryDecode(t *testing.T) {
	at := time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)
	good := StoredSessionReading{Session: "s", Source: protocol.AllowanceSourceCodexRollout, ReadAt: at, ReportedAt: at.Add(time.Minute), Windows: codexReading(at, "5", at.Add(time.Hour)).Windows}
	older := good
	older.ReadAt, older.ReportedAt = at.Add(-time.Hour), at.Add(-time.Hour)
	bad := good
	bad.Windows = []protocol.AllowanceWindow{{Minutes: 300, UsedPercent: "500", ResetsAt: at.Add(time.Hour)}}
	encode := func(e any) string { raw, _ := json.Marshal(e); return string(raw) }
	for name, c := range map[string]struct {
		raw  string
		want int
	}{
		"two good":             {encode(sessionAllowanceEntry{Schema: SessionAllowanceSchema, Provider: "codex", Readings: []StoredSessionReading{good, older}}), 2},
		"a bad one is skipped": {encode(sessionAllowanceEntry{Schema: SessionAllowanceSchema, Provider: "codex", Readings: []StoredSessionReading{bad, good}}), 1},
		"out of order":         {encode(sessionAllowanceEntry{Schema: SessionAllowanceSchema, Provider: "codex", Readings: []StoredSessionReading{older, good}}), 1},
		"another provider":     {encode(sessionAllowanceEntry{Schema: SessionAllowanceSchema, Provider: "claude", Readings: []StoredSessionReading{good}}), 0},
		"another schema":       {encode(sessionAllowanceEntry{Schema: "x", Provider: "codex", Readings: []StoredSessionReading{good}}), 0},
		"an unknown field":     {`{"schema":"` + SessionAllowanceSchema + `","provider":"codex","readings":[],"plan":"x"}`, 0},
		"not json":             {"nope", 0},
		"too large":            {strings.Repeat(" ", maxSessionAllowanceEntry+1), 0},
		"reported before read": {encode(sessionAllowanceEntry{Schema: SessionAllowanceSchema, Provider: "codex", Readings: []StoredSessionReading{{Session: "s", Source: good.Source, ReadAt: at, ReportedAt: at.Add(-time.Second), Windows: good.Windows}}}), 0},
	} {
		e, ok := decodeSessionAllowanceEntry("codex", c.raw)
		if (c.want == 0) == ok || len(e.Readings) != c.want {
			t.Errorf("%s: %+v %v", name, e, ok)
		}
	}
}

// A malformed reading never fails the heartbeat or reaches the store.
func TestSessionAllowancesMalformedKeepsTheHeartbeat(t *testing.T) {
	f := newFixture(t, sessionAllowanceConfigMap())
	tok := allowanceSession(t, f, sessionAllowanceTestName, v1alpha1.AgentCodex, v1alpha1.ModeTask)
	enableSessionAllowances(f)
	for _, malformed := range []string{`"text"`, `{"windows":"x"}`, `{"provider":"codex","source":"codex-rollout","readAt":"2026-10-06T17:30:00Z","windows":[{"minutes":"x"}]}`} {
		f.now = f.now.Add(2 * time.Minute)
		body := `{"session":"` + sessionAllowanceTestName + `","agent":{"state":"busy"},"allowance":` + malformed + `}`
		if w := f.do(http.MethodPost, protocol.HeartbeatPath(sessionAllowanceTestName), tok, body); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", malformed, w.Code, w.Body.String())
		}
		if got := f.session(sessionAllowanceTestName).Status.Agent; got == nil || got.Status != protocol.AgentBusy {
			t.Fatalf("%s: liveness %+v", malformed, got)
		}
	}
	if _, ok := storedEntry(t, f, protocol.AgentCodex); ok {
		t.Fatal("a malformed reading was stored")
	}
}
