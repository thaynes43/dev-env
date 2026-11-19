package keeper

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/claudelogin"
)

type observedClaudeProfile struct {
	claudeRefreshTransport
	beforeResponse func()
}

func (tr observedClaudeProfile) profile(ctx context.Context, access secretValue) (claudeProfile, error) {
	tr.beforeResponse()
	return tr.claudeRefreshTransport.profile(ctx, access)
}

type observedClaudePublisher struct {
	claudePublisher
	afterPublish func(bool, error) (bool, error)
}

func (p observedClaudePublisher) publish(ctx context.Context, a claudeauth.Access, exp, now time.Time) (bool, error) {
	return p.afterPublish(p.claudePublisher.publish(ctx, a, exp, now))
}

// Exercise the worker's adoption path with synthetic staging and a fake
// profile, without a socket or a CLI process.
func adoptClaudeFixture(t *testing.T, f *claudeFix) claudelogin.Response {
	t.Helper()
	f.w.LoginDir = t.TempDir()
	f.tr.Prof = &claudeProfile{AccountUUID: "acct-1", OrganizationUUID: "org-1", SubscriptionType: "max", RateLimitTier: "20x"}
	ctx := context.Background()
	attempt := f.w.begin(ctx)
	if !attempt.OK {
		t.Fatal("could not open synthetic staging")
	}
	path := filepath.Join(f.w.LoginDir, attempt.AttemptUID, claudelogin.StagedCredentials)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := loginCredentials(t, func(o map[string]any) {
		o["expiresAt"] = f.clk.Now().Add(8 * time.Hour).UnixMilli()
		o["refreshTokenExpiresAt"] = f.clk.Now().Add(30 * 24 * time.Hour).UnixMilli()
	})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return f.w.adopt(ctx, attempt.AttemptUID)
}

func readClaudeProvenance(t *testing.T, f *claudeFix) []claudeTransition {
	t.Helper()
	p, err := decodeClaudeProvenance(f.journalSecret(t).Data[claudeProvenanceKey])
	if err != nil {
		t.Fatal(err)
	}
	return p.Transitions
}

func TestClaudeProvenanceAdoptionAndHistory(t *testing.T) {
	f := newClaudeFix(t)
	start := f.clk.Now()
	f.w.Transport = observedClaudeProfile{f.tr, func() { f.clk.Step(time.Second) }}
	f.w.Publisher = observedClaudePublisher{f.w.Publisher, func(written bool, err error) (bool, error) {
		f.clk.Step(2 * time.Second)
		return written, err
	}}
	if got := adoptClaudeFixture(t, f); got.Code != "Adopted" || got.Generation != 1 {
		t.Fatal("fixture adoption failed")
	}
	history := readClaudeProvenance(t, f)
	first := history[0]
	if len(history) != 1 || first.Generation != 1 || first.Operation != claudeAdoption ||
		first.AccessExpirySource != claudeStagedExpiry || first.AccessLifetime != int64(8*time.Hour) ||
		first.AccessExpiresAt != start.Add(8*time.Hour).UnixNano() ||
		first.DispatchedAt != start.UnixNano() || first.RespondedAt != start.Add(time.Second).UnixNano() ||
		first.PublishedAt != start.Add(3*time.Second).UnixNano() || first.RefreshExpiryPresent || first.RefreshRotated ||
		(first.IncarnationHigh == 0 && first.IncarnationLow == 0) {
		t.Fatal("adoption did not record its own timing, expiry source and incarnation")
	}
	f.w = f.successorWorker()
	for range 2 {
		f.clk.Step(6 * time.Hour)
		f.tick()
	}
	history = readClaudeProvenance(t, f)
	if len(history) != 3 || history[0] != first {
		t.Fatal("refresh or restart lost the adoption")
	}
	for _, row := range history[1:] {
		if row.IncarnationHigh != first.IncarnationHigh || row.IncarnationLow != first.IncarnationLow {
			t.Fatal("scheduled refresh changed the login incarnation")
		}
	}
	if got := adoptClaudeFixture(t, f); got.Code != "Adopted" || got.Generation != 4 {
		t.Fatal("fixture readoption failed")
	}
	history = readClaudeProvenance(t, f)
	if len(history) != 3 || history[0].Generation != 2 || history[1].Generation != 3 || history[2].Generation != 4 ||
		history[2].Operation != claudeAdoption ||
		(history[2].IncarnationHigh == first.IncarnationHigh && history[2].IncarnationLow == first.IncarnationLow) {
		t.Fatal("readoption did not start a fresh incarnation or keep exactly two prior transitions")
	}
}

func TestClaudeProvenanceRefresh(t *testing.T) {
	for _, tc := range []struct {
		name     string
		refresh  string
		expiry   time.Duration
		present  bool
		rotation bool
	}{
		{name: "omitted replacement and expiry"},
		{name: "unchanged replacement with expiry", refresh: claudeOldRefreshCanary, expiry: time.Hour, present: true},
		{name: "changed replacement without expiry", refresh: claudeNewRefreshCanary, rotation: true},
		{name: "changed replacement with explicit zero", refresh: claudeNewRefreshCanary, rotation: true, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			start := f.clk.Now()
			f.tr.Prepare = func(int) error {
				f.clk.Step(time.Second)
				return nil
			}
			f.tr.Run = func(int, secretValue) (claudeTokens, error) {
				if s := f.journalSecret(t); len(s.Data[claudeUnsentKey]) != 0 || len(s.Data[claudeProvenanceKey]) != 0 {
					t.Fatal("request ran before the dispatch commit or after replacement evidence")
				}
				f.clk.Step(2 * time.Second)
				return claudeTokens{
					Access: newSecretValue("synthetic-next-access"), ExpiresIn: 8 * time.Hour,
					Refresh: newSecretValue(tc.refresh), RefreshExpiresIn: tc.expiry, RefreshExpiryPresent: tc.present,
				}, nil
			}
			f.w.Publisher = observedClaudePublisher{f.w.Publisher, func(written bool, err error) (bool, error) {
				f.clk.Step(4 * time.Second)
				return written, err
			}}
			f.tick()
			history := readClaudeProvenance(t, f)
			want := claudeTransition{
				Generation: 2, Operation: claudeScheduledRefresh, AccessExpirySource: claudeResponseExpiry,
				DispatchedAt: start.Add(time.Second).UnixNano(), RespondedAt: start.Add(3 * time.Second).UnixNano(),
				PublishedAt: start.Add(7 * time.Second).UnixNano(), AccessLifetime: int64(8 * time.Hour),
				AccessExpiresAt: start.Add(8*time.Hour + 3*time.Second).UnixNano(),
				RefreshRotated:  tc.rotation, RefreshExpiryPresent: tc.present,
			}
			if len(history) != 1 || history[0] != want || f.tr.Calls != 1 {
				t.Fatal("refresh provenance differs from the fake-clock observation")
			}
		})
	}
}

func TestClaudeProvenancePublicationRetry(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	f.clk.Step(6 * time.Hour)
	ctx := context.Background()
	live := &failPatchClient{Name: f.live.Name, Client: f.c}
	live.Fail.Store(true)
	f.w.Publisher = &claudePublicSecret{Client: live, Secret: f.live}
	f.tick()
	before := f.journalSecret(t)
	first := readClaudeProvenance(t, f)[0]
	if first.PublishedAt != 0 || first.Generation != 2 || f.liveGeneration(t) != 0 || f.journal(t).Stage != claudeReady {
		t.Fatal("failed publication must leave only the saved replacement and its evidence")
	}
	live.Fail.Store(false)
	f.clk.Step(time.Minute)
	f.successorWorker().tick(ctx)
	if f.tr.Calls != 1 || f.liveGeneration(t) != 2 || readClaudeProvenance(t, f)[0].PublishedAt != f.clk.Now().UnixNano() {
		t.Fatal("successor did not record a publication-only retry after confirmation")
	}
	if after := f.journalSecret(t); !bytes.Equal(before.Data[claudeJournalKey], after.Data[claudeJournalKey]) {
		t.Fatal("publication timestamp changed the existing journal record")
	}
}

func TestClaudeProvenanceIncompletePublication(t *testing.T) {
	for _, tc := range []struct {
		name     string
		after    func(t *testing.T, f *claudeFix) error
		badLapse bool
	}{
		{name: "lost publication acknowledgement", after: func(*testing.T, *claudeFix) error { return errClaudePublication }},
		{name: "stale journal CAS", after: func(t *testing.T, f *claudeFix) error {
			j := f.journalSecret(t)
			if _, err := f.w.Journal.patch(context.Background(), j, map[string]any{"fixture-change": []byte("changed")}); err != nil {
				t.Fatal(err)
			}
			return nil
		}},
		{name: "lapse-only rewrite after restart", after: func(*testing.T, *claudeFix) error { return errClaudePublication }, badLapse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			base := f.w.Publisher
			f.w.Publisher = observedClaudePublisher{base, func(written bool, err error) (bool, error) {
				if !written || err != nil {
					t.Fatal("fixture projection was not written")
				}
				err = tc.after(t, f)
				return err == nil, err
			}}
			f.tick()
			first := readClaudeProvenance(t, f)[0]
			if first.DispatchedAt == 0 || first.RespondedAt == 0 || first.PublishedAt != 0 || f.liveGeneration(t) != 2 {
				t.Fatal("incomplete publication was reported as a known time")
			}
			if tc.badLapse {
				record := f.journal(t)
				if written, err := base.publish(context.Background(), record.Access, time.Time{}, f.clk.Now()); written || err != nil {
					t.Fatal("fixture did not remove only the lapse document")
				}
			}
			f.w.Publisher = base
			f.clk.Step(time.Minute)
			f.successorWorker().tick(context.Background())
			if readClaudeProvenance(t, f)[0] != first || f.tr.Calls != 1 {
				t.Fatal("restart guessed an unavailable time or repeated the refresh")
			}
		})
	}
}

func TestClaudeProvenanceMalformedSidecarIsObservational(t *testing.T) {
	f := newClaudeFix(t)
	f.seed(t, syntheticClaudeRecord(1))
	if _, err := f.w.Journal.patch(context.Background(), f.journalSecret(t), map[string]any{claudeProvenanceKey: []byte(`{"version":999}`)}); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.tr.Calls != 0 || f.liveGeneration(t) != 1 {
		t.Fatal("invalid provenance controlled the refresh decision or prevented publication")
	}
	f.clk.Step(6 * time.Hour)
	f.tick()
	rows := readClaudeProvenance(t, f)
	if len(rows) != 1 || rows[0].Generation != 2 || rows[0].IncarnationHigh != 0 || rows[0].IncarnationLow != 0 {
		t.Fatal("refresh did not replace invalid provenance with the actual observation")
	}
}

func TestClaudeRefreshOutcomeCounters(t *testing.T) {
	var (
		unknown = errors.New("synthetic unknown provider detail")
		account = claudeTokens{AccountUUID: "synthetic-other-account"}
	)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "not-sent", err: errClaudeNotSent},
		{name: "possibly-spent", err: unknown},
		{name: "status", err: errClaudeStatus},
		{name: "read", err: errClaudeRead},
		{name: "invalid", err: errClaudeParse},
		{name: "account-mismatch"},
		{name: "save-unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			f.w.Outcomes = newClaudeRefreshOutcomes()
			registry := prometheus.NewRegistry()
			registry.MustRegister(f.w.Outcomes)
			f.seed(t, syntheticClaudeRecord(1))
			f.clk.Step(6 * time.Hour)
			if tc.err == errClaudeNotSent {
				f.notSent(nil)
			}
			f.tr.Run = func(call int, prev secretValue) (claudeTokens, error) {
				switch tc.name {
				case "account-mismatch":
					return account, nil
				case "save-unavailable":
					f.c.Fail.Store(true)
				}
				if tc.err != nil {
					return claudeTokens{}, tc.err
				}
				return rotated(call, prev)
			}
			f.tick()
			families, err := registry.Gather()
			if err != nil || len(families) != 1 || families[0].GetName() != "dev_env_keeper_claude_refresh_outcomes_total" || len(families[0].Metric) != 8 {
				t.Fatal("counter did not expose a fixed set of outcomes")
			}
			var counted bool
			for _, metric := range families[0].Metric {
				if len(metric.Label) != 1 || metric.Label[0].GetName() != "outcome" {
					t.Fatal("unexpected metric label")
				}
				var want float64
				if metric.Label[0].GetValue() == tc.name {
					want = 1
					counted = true
				}
				if metric.GetCounter().GetValue() != want {
					t.Fatal("outcome did not increment only its fixed class")
				}
			}
			if !counted {
				t.Fatal("expected outcome class was absent")
			}
			if tc.name != "success" && len(f.journalSecret(t).Data[claudeProvenanceKey]) != 0 {
				t.Fatal("an incomplete refresh recorded a fabricated transition")
			}
		})
	}
}

func TestClaudeProvenanceStrictBounds(t *testing.T) {
	const row = `{"generation":1,"incarnationHigh":7,"incarnationLow":8,"operation":2,"dispatchedAtUnixNano":1,"respondedAtUnixNano":2,"publishedAtUnixNano":0,"accessLifetimeNanos":100,"accessExpiresAtUnixNano":102,"accessExpirySource":2,"refreshRotated":false,"refreshExpiryPresent":true}`
	const good = `{"version":1,"transitions":[` + row + `]}`
	if _, err := decodeClaudeProvenance([]byte(good)); err != nil {
		t.Fatal("bounded numeric, time and boolean fields were refused")
	}
	fourRows := []string{row}
	for _, generation := range []string{"2", "3", "4"} {
		fourRows = append(fourRows, strings.Replace(row, `"generation":1`, `"generation":`+generation, 1))
	}
	for _, tc := range []struct{ name, raw string }{
		{"version", strings.Replace(good, `"version":1`, `"version":2`, 1)},
		{"oversize", good + strings.Repeat(" ", claudeProvenanceMax)},
		{"trailing document", good + "{}"},
		{"empty history", `{"version":1,"transitions":[]}`},
		{"unordered generations", strings.Replace(good, row, row+","+row, 1)},
		{"too many entries", strings.Replace(good, row, strings.Join(fourRows, ","), 1)},
		{"unknown", strings.Replace(good, `"generation":1`, `"generation":1,"detail":"synthetic"`, 1)},
		{"duplicate", strings.Replace(good, `"generation":1`, `"generation":1,"generation":2`, 1)},
		{"case alias", strings.Replace(good, `"generation":1`, `"generation":1,"Generation":2`, 1)},
		{"operation", strings.Replace(good, `"operation":2`, `"operation":9`, 1)},
		{"expiry source", strings.Replace(good, `"accessExpirySource":2`, `"accessExpirySource":1`, 1)},
		{"lifetime bound", strings.Replace(good, `"accessLifetimeNanos":100`, `"accessLifetimeNanos":999999999999999999`, 1)},
		{"negative timestamp", strings.Replace(good, `"respondedAtUnixNano":2`, `"respondedAtUnixNano":-2`, 1)},
		{"time overflow", strings.Replace(good, `"respondedAtUnixNano":2`, `"respondedAtUnixNano":9223372036854775808`, 1)},
		{"float", strings.Replace(good, `"publishedAtUnixNano":0`, `"publishedAtUnixNano":1.5`, 1)},
		{"string", strings.Replace(good, "false", `"false"`, 1)},
		{"null", strings.Replace(good, "false", "null", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeClaudeProvenance([]byte(tc.raw)); !errors.Is(err, errClaudeProvenance) {
				t.Fatal("accepted malformed provenance")
			}
		})
	}
}
