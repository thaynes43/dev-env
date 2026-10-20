package keeper

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// series returns every sample of one metric as "label=value,..." -> value.
func series(t *testing.T, hub *LoginHub, name string) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(NewLoginCollector(hub)); err != nil {
		t.Fatal(err)
	}
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			out[labels(m)] = m.GetGauge().GetValue()
		}
	}
	return out
}

func labels(m *dto.Metric) string {
	var parts []string
	for _, l := range m.GetLabel() {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	return strings.Join(parts, ",")
}

func TestLoginGaugesFollowTheHub(t *testing.T) {
	hub := NewLoginHub(ProviderClaude, ProviderCodex)
	// Before any observation: Unknown is 1, and no lapse series exists.
	st := series(t, hub, "dev_env_keeper_login_state")
	if len(st) != 8 || st["provider=claude,state=Unknown"] != 1 || st["provider=claude,state=Ready"] != 0 {
		t.Fatalf("initial state series = %v", st)
	}
	if got := series(t, hub, "dev_env_keeper_login_lapse_timestamp_seconds"); len(got) != 0 {
		t.Fatalf("lapse series exist before a date is known: %v", got)
	}

	lapse := time.Date(2026, 11, 16, 17, 51, 0, 0, time.UTC)
	hub.Report(ProviderClaude, LoginReady, lapse)
	hub.Report(ProviderCodex, LoginReady, time.Time{}) // Codex has no known lapse date
	st = series(t, hub, "dev_env_keeper_login_state")
	if st["provider=claude,state=Ready"] != 1 || st["provider=claude,state=Unknown"] != 0 || st["provider=codex,state=Ready"] != 1 {
		t.Fatalf("ready state series = %v", st)
	}
	got := series(t, hub, "dev_env_keeper_login_lapse_timestamp_seconds")
	if len(got) != 1 || got["provider=claude"] != float64(lapse.Unix()) {
		t.Fatalf("lapse series = %v (Codex must have none, never 0)", got)
	}

	hub.Report(ProviderClaude, LoginNeedsLogin, lapse) // a NeedsLogin login has no lapse series
	st = series(t, hub, "dev_env_keeper_login_state")
	if st["provider=claude,state=NeedsLogin"] != 1 || st["provider=claude,state=Ready"] != 0 {
		t.Fatalf("needs-login series = %v", st)
	}
	if got := series(t, hub, "dev_env_keeper_login_lapse_timestamp_seconds"); len(got) != 0 {
		t.Fatalf("a stale lapse stayed exported: %v", got)
	}
	hub.Report(ProviderClaude, "nonsense", time.Time{})
	if series(t, hub, "dev_env_keeper_login_state")["provider=claude,state=Unknown"] != 1 {
		t.Fatal("an unknown state word must export as Unknown")
	}
}

func TestNilHubIsInert(t *testing.T) {
	var h *LoginHub
	h.Report(ProviderClaude, LoginReady, time.Now())
	if len(h.Snapshot()) != 0 {
		t.Fatal("nil hub reported something")
	}
}

func TestClaudeWorkerReportsTheJournalToTheGauges(t *testing.T) {
	f := newClaudeFix(t)
	hub := NewLoginHub(ProviderClaude)
	f.w.Obs = hub
	rec := syntheticClaudeRecord(1)
	f.seed(t, rec)
	f.tick()
	if s := hub.Snapshot()[ProviderClaude]; s.State != LoginReady || !s.LapsesAt.Equal(rec.RefreshExpiresAt) {
		t.Fatalf("ready = %+v, want lapse %v", s, rec.RefreshExpiresAt)
	}
	// A refresh renews the lapse date the provider reports.
	f.clk.Step(6 * time.Hour)
	f.tick()
	if s := hub.Snapshot()[ProviderClaude]; s.State != LoginReady || !s.LapsesAt.Equal(f.journal(t).RefreshExpiresAt) || !s.LapsesAt.After(rec.RefreshExpiresAt) {
		t.Fatalf("after refresh = %+v", s)
	}
	// A provider that gives no date leaves no series.
	g := newClaudeFix(t)
	g.w.Obs = NewLoginHub(ProviderClaude)
	noDate := syntheticClaudeRecord(1)
	noDate.RefreshExpiresAt = time.Time{}
	g.seed(t, noDate)
	g.tick()
	if s := g.w.Obs.Snapshot()[ProviderClaude]; s.State != LoginReady || !s.LapsesAt.IsZero() {
		t.Fatalf("no-date = %+v", s)
	}
	// An interrupted attempt is NeedsLogin.
	h := newClaudeFix(t)
	h.w.Obs = NewLoginHub(ProviderClaude)
	stuck := syntheticClaudeRecord(1)
	stuck.Stage, stuck.AttemptUID = claudeIntent, "11111111-1111-4111-8111-111111111111"
	h.seed(t, stuck)
	h.tick()
	if s := h.w.Obs.Snapshot()[ProviderClaude]; s.State != LoginNeedsLogin || !s.LapsesAt.IsZero() {
		t.Fatalf("needs-login = %+v", s)
	}
}

// A deliberate Codex renewal pauses refresh and sets halted for the ceremony's
// life. The gauges and the notifier must see LoginInProgress, not NeedsLogin, so
// a renewal from Ready opens no incident and ends none.
func TestCodexWorkerReportsACeremonyAsInProgress(t *testing.T) {
	w, _, _ := codexFixture(t)
	hub := NewLoginHub(ProviderCodex)
	w.Obs = hub
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := w.serveControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	saveCodexFixture(t, w, freshCodexRecord(t, w))
	if w.tick(ctx) != nil {
		t.Fatal("a fresh record must tick clean")
	}
	if s := hub.Snapshot()[ProviderCodex]; s.State != LoginReady || !s.LapsesAt.IsZero() {
		t.Fatalf("ready = %+v", s)
	}
	start := w.begin(ctx)
	if !start.OK {
		t.Fatalf("begin: %+v", start)
	}
	_ = w.tick(ctx)
	if s := hub.Snapshot()[ProviderCodex]; s.State != LoginInProgress {
		t.Fatalf("during a ceremony = %+v, want LoginInProgress", s)
	}
	if c := w.cancelLogin(ctx, start.AttemptUID); !c.OK {
		t.Fatalf("cancel: %+v", c)
	}
	if w.tick(ctx) != nil {
		t.Fatal("tick after cancel")
	}
	if s := hub.Snapshot()[ProviderCodex]; s.State != LoginReady {
		t.Fatalf("after cancel = %+v, want Ready", s)
	}
	// Once the reservation has lapsed the login really needs a person.
	start = w.begin(ctx)
	if !start.OK {
		t.Fatalf("second begin: %+v", start)
	}
	w.Clock.(interface{ Step(time.Duration) }).Step(codexCeremonyLifetime + time.Minute)
	_ = w.tick(ctx)
	if s := hub.Snapshot()[ProviderCodex]; s.State != LoginNeedsLogin {
		t.Fatalf("after the reservation lapsed = %+v, want NeedsLogin", s)
	}
}
