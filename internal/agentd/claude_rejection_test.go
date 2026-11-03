package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// A provider revokes the keeper's login while the applied access token has not
// expired. The pod records one local, credential-free marker so the status
// text can say "rejected, next generation pending" instead of "logged in".
// Every time is relative to one injected clock.
//
// RED evidence: the file cannot compile on main (the feature does not exist).
// The revisions' RED runs used a scratch worktree of the earlier head with the
// new test file and a one-line shim for new constants; the names and failures
// are recorded in .agents/sagas/distributed-dev-env/verification/
// keeper-consumer-rejected-lease-2026-11-03.md.

// rejectionRig drives the poll in the daemon's own order: sync, observe, continue.
type rejectionRig struct {
	*continueRig
	clk  time.Time
	life time.Duration
}

func newRejectionRig(t *testing.T) *rejectionRig {
	t.Helper()
	rr := &rejectionRig{continueRig: newContinueRig(t, protocol.ModeRemote, true, true), clk: loginNow.Add(time.Hour), life: 9 * time.Hour}
	rr.useClock()
	return rr
}

func (rr *rejectionRig) useClock() { rr.d.Now = func() time.Time { return rr.clk } }

// restartAgentd is a new agentd process on the same volume and the same clock.
func (rr *rejectionRig) restartAgentd() {
	rr.restart()
	rr.useClock()
}

func (rr *rejectionRig) advance(d time.Duration) { rr.clk = rr.clk.Add(d) }

func (rr *rejectionRig) poll() {
	rr.d.syncClaudeAccess()
	rr.d.observeClaudeRejection()
	rr.d.continueAfterRenewal(context.Background())
}

// publishGeneration applies generation g at the clock; the receipt's mtime is
// the moment it was applied.
func (rr *rejectionRig) publishGeneration(g uint64) {
	rr.t.Helper()
	publish(rr.t, rr.live, projAccess(g, rr.life))
	rr.poll()
	rr.stampReceipt()
}

func (rr *rejectionRig) stampReceipt() {
	rr.t.Helper()
	if err := os.Chtimes(rr.s.statePath("claude-access-generation.json"), rr.clk, rr.clk); err != nil {
		rr.t.Fatal(err)
	}
}

func (rr *rejectionRig) markerPath() string { return rr.s.statePath(rejectionFile) }

func (rr *rejectionRig) marker() (claudeRejection, bool) {
	var m claudeRejection
	if readJSONFile(rr.markerPath(), &m) != nil {
		return claudeRejection{}, false
	}
	return m, true
}

func (rr *rejectionRig) markerExists() bool {
	_, err := os.Lstat(rr.markerPath())
	return err == nil
}

// text is the agentd login status line at the clock.
func (rr *rejectionRig) text() string { return projectionLoginStatus(rr.s, rr.clk) }

func (rr *rejectionRig) rejected() bool        { return strings.Contains(rr.text(), "login-rejected:") }
func (rr *rejectionRig) unverified() bool      { return strings.Contains(rr.text(), "login-unverified:") }
func (rr *rejectionRig) readyOrLoggedIn() bool { return strings.HasPrefix(rr.text(), "logged in") }

// revokedCaseLife is the standard case: generation 1 applied, then, 10 minutes
// later and long before the token's own expiry, the transcript ends in a 401.
func revokedCaseLife(t *testing.T, life time.Duration) *rejectionRig {
	t.Helper()
	rr := newRejectionRig(t)
	rr.life = life
	rr.publishGeneration(1)
	rr.transcript(healthyTurn(rr.clk.Add(time.Minute)))
	rr.advance(10 * time.Minute)
	rr.appendTurn(revokedTurn(rr.clk))
	rr.poll()
	return rr
}

func revokedCase(t *testing.T) *rejectionRig { return revokedCaseLife(t, 9*time.Hour) }

// acceptedTurn is an assistant entry that carries a real model response.
func acceptedTurn(at time.Time) string {
	return acceptedTurnWith(at.Format(time.RFC3339Nano), "claude-sonnet-5-5", `{"input_tokens":3,"output_tokens":9}`, false)
}

func acceptedTurnWith(ts, model, usage string, sidechain bool) string {
	stamp := ""
	if ts != "" {
		stamp = fmt.Sprintf(`"timestamp":%q,`, ts)
	}
	msg := fmt.Sprintf(`{"role":"assistant","model":%q,"content":[{"type":"text","text":"done"}]`, model)
	if usage != "" {
		msg += `,"usage":` + usage
	}
	msg += `}`
	return fmt.Sprintf(`{"type":"assistant",%s"isSidechain":%t,"message":%s}`, stamp, sidechain, msg)
}

func TestRejectionRecordedWhileLocalTokenUnexpired(t *testing.T) {
	rr := revokedCase(t)
	m, ok := rr.marker()
	if !ok || m.Version != 1 || m.Generation != 1 || !m.ObservedAt.Equal(rr.clk) {
		t.Fatalf("marker = %+v, %v; want version 1 generation 1 observed at the refused turn", m, ok)
	}
	if !checkProjectedLogin(rr.s).Usable(rr.clk) {
		t.Fatal("the fixture's local token must still be unexpired")
	}
	text := rr.text()
	for _, want := range []string{"login-rejected:", "generation 1", "next generation", "no login is run in this pod"} {
		if !strings.Contains(text, want) {
			t.Errorf("status text %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, "logged in") {
		t.Errorf("status text %q still claims a usable login", text)
	}
	if len(rr.sent) != 0 {
		t.Errorf("the gap sent %d messages; want none until a newer generation merges", len(rr.sent))
	}
}

func TestRejectionMarkerCarriesNoCredentialOrTranscriptText(t *testing.T) {
	rr := revokedCase(t)
	raw, err := os.ReadFile(rr.markerPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{projToken(1), "revoked", "API Error", "OAuth", "accessToken", "refresh", "sha", "fingerprint"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("marker contains %q: %s", bad, raw)
		}
	}
	if fi, _ := os.Stat(rr.markerPath()); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v; want 0600", fi)
	}
	if strings.Contains(rr.logs.String(), projToken(1)) {
		t.Error("a log line carries the token")
	}
}

func TestRejectionClearsWhenNextGenerationVisibleAndFenceHolds(t *testing.T) {
	rr := revokedCase(t)
	first, _ := rr.marker()
	rr.advance(59 * time.Second)
	rr.poll() // the keeper's next generation is not here yet
	again, _ := rr.marker()
	if !again.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("observedAt moved from %v to %v: the first sight must win", first.ObservedAt, again.ObservedAt)
	}
	if len(rr.sent) != 0 {
		t.Fatalf("%d sends before the next generation", len(rr.sent))
	}
	rr.publishGeneration(2)
	if rr.markerExists() {
		t.Error("marker survived the next generation")
	}
	if !rr.readyOrLoggedIn() {
		t.Errorf("status after the next generation = %q", rr.text())
	}
	// The existing fence: exactly one continue, however many polls follow.
	for range 3 {
		rr.advance(5 * time.Second)
		rr.poll()
	}
	if len(rr.sent) != 1 {
		t.Fatalf("%d continue sends for one generation; want exactly 1", len(rr.sent))
	}
	// The old 401 is still the last turn. It predates generation 2 and must not
	// be charged to it.
	if rr.markerExists() {
		t.Error("a 401 older than the applied generation was recorded against it")
	}
}

// Constraint 4: a new generation may itself be rejected; the marker for N says
// nothing about N+1.
func TestRejectionGenerationRebindingSaysNothingAboutTheNextGeneration(t *testing.T) {
	rr := revokedCase(t)
	// The keeper's N+1 reaches agentd's credentials but the writer has not polled
	// yet: the stale N marker is still on disk. Readers must not apply it to N+1.
	rr.advance(time.Minute)
	publish(t, rr.live, projAccess(2, rr.life))
	rr.d.syncClaudeAccess()
	rr.stampReceipt()
	if !rr.markerExists() {
		t.Fatal("fixture: the stale marker should still be on disk")
	}
	if rr.rejected() || rr.unverified() || !rr.readyOrLoggedIn() {
		t.Errorf("N+1 before re-observation reads %q; want the unobserved default, neither rejected nor unverified", rr.text())
	}
	// And it must not be called proven healthy either: the text is the same
	// default as before this feature ("logged in on the keeper's projection").
	if !strings.Contains(rr.text(), "logged in on the keeper's projection") {
		t.Errorf("text = %q", rr.text())
	}
	rr.poll()
	if rr.markerExists() {
		t.Error("the writer kept a marker for the older generation")
	}
	// N+1 refused later: recorded against N+1 only.
	rr.advance(time.Minute)
	rr.appendTurn(revokedTurn(rr.clk))
	rr.poll()
	m, ok := rr.marker()
	if !ok || m.Generation != 2 || !rr.rejected() || !strings.Contains(rr.text(), "generation 2") {
		t.Fatalf("marker = %+v, %v; text %q; want generation 2 rejected", m, ok, rr.text())
	}
	if len(rr.sent) != 1 {
		t.Errorf("%d sends; the second failure must not buy a second continue for the same generation", len(rr.sent))
	}
}

func TestRejectionMarkerOlderThanAppliedGenerationIsIgnoredAndRemoved(t *testing.T) {
	rr := newRejectionRig(t)
	rr.publishGeneration(2)
	rr.transcript(healthyTurn(rr.clk.Add(time.Minute)))
	if err := writeJSONFile(rr.markerPath(), claudeRejection{Version: 1, Generation: 1, ObservedAt: rr.clk.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if !rr.readyOrLoggedIn() {
		t.Errorf("a marker for generation 1 changed generation 2's text: %q", rr.text())
	}
	rr.poll()
	if rr.markerExists() {
		t.Error("the writer kept a parsed marker for an older generation")
	}
}

func TestRejectionSurvivesAgentdRestartInsideTheGap(t *testing.T) {
	rr := revokedCase(t)
	first, _ := rr.marker()
	rr.advance(30 * time.Second)
	rr.restartAgentd()
	rr.poll()
	m, ok := rr.marker()
	if !ok || !m.ObservedAt.Equal(first.ObservedAt) || m.Generation != 1 {
		t.Fatalf("marker after restart = %+v, %v; want the original observation", m, ok)
	}
	if !rr.rejected() {
		t.Errorf("text after the restart = %q", rr.text())
	}
	if len(rr.sent) != 0 {
		t.Errorf("restart inside the gap sent %d messages", len(rr.sent))
	}
	// Restart again across the next generation: still one continue.
	rr.advance(29 * time.Second)
	rr.restartAgentd()
	rr.publishGeneration(2)
	rr.restartAgentd()
	rr.poll()
	if len(rr.sent) != 1 || rr.markerExists() {
		t.Errorf("sends %d, marker present %v after restarts across the next generation", len(rr.sent), rr.markerExists())
	}
}

// Constraint 1: age never turns an unresolved same-generation rejection into Ready.
func TestRejectionNeverAgesIntoReady(t *testing.T) {
	rr := revokedCaseLife(t, 48*time.Hour)
	if _, ok := rr.marker(); !ok {
		t.Fatal("fixture: no marker")
	}
	first, _ := rr.marker()
	pending := rr.text()
	if !strings.Contains(pending, "next generation") || strings.Contains(pending, "check the keeper") {
		t.Errorf("text inside the window = %q", pending)
	}
	rr.advance(rejectionPendingWindow + time.Second)
	if t2 := rr.text(); !strings.Contains(t2, "login-rejected:") || !strings.Contains(t2, "check the keeper") {
		t.Errorf("text past the window = %q", t2)
	}
	for _, age := range []time.Duration{6 * time.Hour, 7 * time.Hour, 24 * time.Hour} {
		rr.clk = first.ObservedAt.Add(age)
		rr.poll()
		got := rr.text()
		if !strings.Contains(got, "login-rejected:") || !strings.Contains(got, "generation 1") || !strings.Contains(got, first.ObservedAt.UTC().Format(time.RFC3339)) {
			t.Errorf("at %s the text is %q; want the same-generation rejection with its fixed observation time", age, got)
		}
		if rr.readyOrLoggedIn() || !rr.markerExists() {
			t.Errorf("at %s the unresolved rejection became ready or its marker vanished", age)
		}
		if m, _ := rr.marker(); !m.ObservedAt.Equal(first.ObservedAt) {
			t.Errorf("at %s the marker was rewritten: %+v", age, m)
		}
		// Heartbeat message too.
		if err := writeJSONFile(rr.s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady}); err != nil {
			t.Fatal(err)
		}
		if st := CollectStatus(context.Background(), rr.r, rr.s, rr.session, rr.clk); !strings.HasPrefix(st.Agent.Error, "login-rejected:") {
			t.Errorf("at %s the heartbeat message is %q", age, st.Agent.Error)
		}
	}
}

// Only the access token's own expiry hands the display back to the expired-token path.
func TestRejectionYieldsToTheExpiredTokenDisplayOnlyWhenTheTokenExpires(t *testing.T) {
	rr := revokedCase(t) // access life 9h from loginNow
	rr.clk = loginNow.Add(9*time.Hour - time.Second)
	if !rr.rejected() {
		t.Errorf("one second before expiry: %q", rr.text())
	}
	rr.clk = loginNow.Add(9*time.Hour + time.Second)
	if got := rr.text(); strings.Contains(got, "login-rejected") || !strings.HasPrefix(got, "login-required:") {
		t.Errorf("after the token expired: %q; want the existing expired-token display", got)
	}
}

// Constraint 3: only explicit proof clears.
func TestRejectionClearsOnlyOnExplicitProofOfAcceptance(t *testing.T) {
	refused := func(rr *rejectionRig) time.Time { m, _ := rr.marker(); return m.ObservedAt }
	cases := map[string]func(rr *rejectionRig) string{
		"user turn": func(rr *rejectionRig) string { return userTurn(refused(rr).Add(time.Minute)) },
		"unrelated API error": func(rr *rejectionRig) string {
			return entry("assistant", refused(rr).Add(time.Minute), `,"isApiErrorMessage":true`, `[{"type":"text","text":"API Error: 529 overloaded"}]`)
		},
		"plain text turn with no model or usage": func(rr *rejectionRig) string { return healthyTurn(refused(rr).Add(time.Minute)) },
		"synthetic model": func(rr *rejectionRig) string {
			return acceptedTurnWith(refused(rr).Add(time.Minute).Format(time.RFC3339Nano), "<synthetic>", `{"input_tokens":0}`, false)
		},
		"empty usage": func(rr *rejectionRig) string {
			return acceptedTurnWith(refused(rr).Add(time.Minute).Format(time.RFC3339Nano), "claude-sonnet-5-5", `{}`, false)
		},
		"no usage": func(rr *rejectionRig) string {
			return acceptedTurnWith(refused(rr).Add(time.Minute).Format(time.RFC3339Nano), "claude-sonnet-5-5", "", false)
		},
		"missing timestamp": func(rr *rejectionRig) string {
			return acceptedTurnWith("", "claude-sonnet-5-5", `{"input_tokens":3}`, false)
		},
		"same instant as the refusal": func(rr *rejectionRig) string { return acceptedTurn(refused(rr)) },
		"before the refusal":          func(rr *rejectionRig) string { return acceptedTurn(refused(rr).Add(-time.Minute)) },
		"sidechain": func(rr *rejectionRig) string {
			return acceptedTurnWith(refused(rr).Add(time.Minute).Format(time.RFC3339Nano), "claude-sonnet-5-5", `{"input_tokens":3}`, true)
		},
		"a second auth error": func(rr *rejectionRig) string { return revokedTurn(refused(rr).Add(time.Minute)) },
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			rr := revokedCase(t)
			before, _ := rr.marker()
			rr.advance(2 * time.Minute)
			rr.appendTurn(line(rr))
			rr.poll()
			m, ok := rr.marker()
			if !ok || !m.ObservedAt.Equal(before.ObservedAt) || !rr.rejected() {
				t.Errorf("an ambiguous turn changed the rejection: marker %+v %v, text %q", m, ok, rr.text())
			}
		})
	}
	t.Run("explicit proof clears", func(t *testing.T) {
		rr := revokedCase(t)
		rr.advance(time.Minute)
		rr.appendTurn(acceptedTurn(rr.clk))
		rr.poll()
		if rr.markerExists() || !rr.readyOrLoggedIn() {
			t.Errorf("explicit proof did not clear: exists %v text %q", rr.markerExists(), rr.text())
		}
	})
}

// Constraint 2: a marker that cannot be believed is "cannot tell", never Ready,
// and the writer never deletes it.
func TestRejectionUnbelievableMarkerIsCannotTellAndNeverDeleted(t *testing.T) {
	validDoc := func(rr *rejectionRig, gen uint64, at time.Time) string {
		return fmt.Sprintf(`{"version":1,"generation":%d,"observedAt":%q}`, gen, at.UTC().Format(time.RFC3339))
	}
	cases := map[string]func(rr *rejectionRig){
		"garbage": func(rr *rejectionRig) { _ = os.WriteFile(rr.markerPath(), []byte("not json"), 0o600) },
		"unknown field": func(rr *rejectionRig) {
			_ = os.WriteFile(rr.markerPath(), []byte(`{"version":1,"generation":1,"observedAt":"2026-10-19T13:00:00Z","accessToken":"x"}`), 0o600)
		},
		"wrong version": func(rr *rejectionRig) {
			_ = os.WriteFile(rr.markerPath(), []byte(`{"version":2,"generation":1,"observedAt":"2026-10-19T13:00:00Z"}`), 0o600)
		},
		"zero generation": func(rr *rejectionRig) { _ = os.WriteFile(rr.markerPath(), []byte(validDoc(rr, 0, rr.clk)), 0o600) },
		"future dated": func(rr *rejectionRig) {
			_ = os.WriteFile(rr.markerPath(), []byte(validDoc(rr, 1, rr.clk.Add(time.Hour))), 0o600)
		},
		"generation above the applied one": func(rr *rejectionRig) {
			_ = os.WriteFile(rr.markerPath(), []byte(validDoc(rr, 7, rr.clk)), 0o600)
		},
		"oversize": func(rr *rejectionRig) {
			_ = os.WriteFile(rr.markerPath(), []byte(strings.Repeat(" ", 8<<10)+validDoc(rr, 1, rr.clk)), 0o600)
		},
		"empty": func(rr *rejectionRig) { _ = os.WriteFile(rr.markerPath(), nil, 0o600) },
		"unreadable": func(rr *rejectionRig) {
			_ = os.WriteFile(rr.markerPath(), []byte(validDoc(rr, 1, rr.clk)), 0o600)
			_ = os.Chmod(rr.markerPath(), 0)
		},
		"a directory": func(rr *rejectionRig) { _ = os.Mkdir(rr.markerPath(), 0o700) },
		"a symlink": func(rr *rejectionRig) {
			target := rr.s.statePath("elsewhere")
			_ = os.WriteFile(target, []byte(validDoc(rr, 1, rr.clk)), 0o600)
			_ = os.Symlink(target, rr.markerPath())
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			if os.Geteuid() == 0 && name == "unreadable" {
				t.Skip("root reads a mode 0 file")
			}
			rr := newRejectionRig(t)
			rr.publishGeneration(1)
			rr.transcript(healthyTurn(rr.clk.Add(time.Minute))) // no refusal to re-record
			rr.advance(10 * time.Minute)
			setup(rr)
			rr.poll()
			if rr.readyOrLoggedIn() || rr.rejected() || !rr.unverified() {
				t.Errorf("text = %q; want cannot-tell", rr.text())
			}
			if _, err := os.Lstat(rr.markerPath()); err != nil {
				t.Errorf("the writer deleted an unbelievable marker: %v", err)
			}
			if err := writeJSONFile(rr.s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady}); err != nil {
				t.Fatal(err)
			}
			st := CollectStatus(context.Background(), rr.r, rr.s, rr.session, rr.clk)
			if st.Agent.State != protocol.AgentBusy || !strings.HasPrefix(st.Agent.Error, "login-unverified:") {
				t.Errorf("heartbeat: state %q message %q", st.Agent.State, st.Agent.Error)
			}
		})
	}
}

func TestRejectionUnbelievableMarkerOlderThanTheAppliedGenerationCannotConcernIt(t *testing.T) {
	rr := newRejectionRig(t)
	rr.publishGeneration(1)
	rr.transcript(healthyTurn(rr.clk.Add(time.Minute)))
	if err := os.WriteFile(rr.markerPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := rr.clk.Add(-time.Hour)
	if err := os.Chtimes(rr.markerPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if !rr.readyOrLoggedIn() {
		t.Errorf("a file last written before the generation arrived changed its text: %q", rr.text())
	}
	rr.poll()
	if !rr.markerExists() {
		t.Error("the writer deleted a marker it could not parse")
	}
}

func TestRejectionFreshObservationReplacesAnUnreadableMarker(t *testing.T) {
	rr := newRejectionRig(t)
	rr.publishGeneration(1)
	rr.transcript(healthyTurn(rr.clk.Add(time.Minute)))
	rr.advance(10 * time.Minute)
	if err := os.WriteFile(rr.markerPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rr.poll()
	if !rr.unverified() {
		t.Fatalf("fixture: %q", rr.text())
	}
	rr.appendTurn(revokedTurn(rr.clk))
	rr.poll()
	if m, ok := rr.marker(); !ok || m.Generation != 1 || !rr.rejected() {
		t.Errorf("marker %+v %v text %q; want a stated rejection", m, ok, rr.text())
	}
}

func TestRejectionNotRecordedWhenNotAttributable(t *testing.T) {
	cases := map[string]func(*rejectionRig){
		"error older than the applied generation": func(rr *rejectionRig) {
			rr.transcript(revokedTurn(rr.clk.Add(-time.Hour)))
			rr.publishGeneration(1)
		},
		"error with no timestamp": func(rr *rejectionRig) {
			rr.publishGeneration(1)
			rr.advance(time.Minute)
			rr.transcript(entry("assistant", time.Time{}, `,"isApiErrorMessage":true,"apiErrorStatus":401`, `[{"type":"text","text":"API Error"}]`))
		},
		"last turn is a user turn": func(rr *rejectionRig) {
			rr.publishGeneration(1)
			rr.advance(time.Minute)
			rr.transcript(revokedTurn(rr.clk), userTurn(rr.clk.Add(time.Second)))
		},
		"agent not running": func(rr *rejectionRig) {
			_ = os.Remove(rr.s.statePath(pidFile))
			rr.publishGeneration(1)
			rr.advance(time.Minute)
			rr.transcript(revokedTurn(rr.clk))
		},
		"no projection": func(rr *rejectionRig) {
			rr.d.S.ClaudeLiveFile = ""
			rr.advance(time.Minute)
			rr.transcript(revokedTurn(rr.clk))
		},
		"task session": func(rr *rejectionRig) {
			rr.d.Session.Mode = protocol.ModeTask
			rr.publishGeneration(1)
			rr.advance(time.Minute)
			rr.transcript(revokedTurn(rr.clk))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			rr := newRejectionRig(t)
			setup(rr)
			rr.poll()
			if rr.markerExists() {
				t.Errorf("recorded %v", rr.text())
			}
			if rr.rejected() || rr.unverified() {
				t.Errorf("status text %q", rr.text())
			}
		})
	}
}

func TestRejectionObservationNeverTouchesTheContinueReceipt(t *testing.T) {
	rr := newRejectionRig(t)
	rr.publishGeneration(1)
	rr.advance(10 * time.Minute)
	rr.transcript(revokedTurn(rr.clk))
	before, _ := os.ReadFile(rr.s.statePath(continueReceiptFile))
	rr.d.observeClaudeRejection()
	after, _ := os.ReadFile(rr.s.statePath(continueReceiptFile))
	if string(before) != string(after) {
		t.Errorf("observing a rejection changed the continue receipt:\n%s\n%s", before, after)
	}
	if !rr.markerExists() {
		t.Fatal("the rejection was not recorded")
	}
	if len(rr.sent) != 0 {
		t.Errorf("observing a rejection sent %d messages", len(rr.sent))
	}
}

func TestRejectionShownInHeartbeatTextWithoutChangingTheStateWord(t *testing.T) {
	rr := revokedCase(t)
	if err := writeJSONFile(rr.s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), rr.r, rr.s, rr.session, rr.clk)
	if st.Agent.State != protocol.AgentBusy {
		t.Fatalf("agent state = %q; the wire state word must not change", st.Agent.State)
	}
	if !strings.HasPrefix(st.Agent.Error, "login-rejected:") {
		t.Errorf("agent message = %q", st.Agent.Error)
	}
	rr.advance(time.Minute)
	rr.publishGeneration(2)
	st = CollectStatus(context.Background(), rr.r, rr.s, rr.session, rr.clk)
	if st.Agent.Error != "" {
		t.Errorf("agent message after the next generation = %q", st.Agent.Error)
	}
}

// Races: the writer's critical section is exclusive and its decisions are made
// on a view read inside it.
func TestRejectionBusyLockMakesThePollDoNothing(t *testing.T) {
	rr := newRejectionRig(t)
	rr.publishGeneration(1)
	rr.advance(10 * time.Minute)
	rr.transcript(revokedTurn(rr.clk))
	f, err := os.OpenFile(rr.s.statePath(rejectionLock), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	rr.poll()
	if rr.markerExists() {
		t.Fatal("a poll wrote while another writer held the lock")
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	rr.poll()
	if !rr.markerExists() {
		t.Error("the next poll did not write after the lock was released")
	}
}

func TestRejectionStaleDeleteNeverRemovesANewerGenerationsMarker(t *testing.T) {
	rr := revokedCase(t) // marker for generation 1
	// A second agentd writer (an overlapping process) on the same volume.
	other := &Daemon{S: rr.s, R: rr.r, Log: rr.d.Log, Session: rr.session, Self: "/agentd", Now: func() time.Time { return rr.clk }}
	rr.advance(time.Minute)
	rr.publishGeneration(2) // writer A deletes the stale marker
	rr.advance(time.Minute)
	rr.appendTurn(revokedTurn(rr.clk))
	rr.d.observeClaudeRejection() // writer A records generation 2
	want, ok := rr.marker()
	if !ok || want.Generation != 2 {
		t.Fatalf("fixture: marker %+v %v", want, ok)
	}
	// Writer B, which only ever knew generation 1's world, now runs its poll.
	other.observeClaudeRejection()
	got, ok := rr.marker()
	if !ok || got != want {
		t.Errorf("writer B disturbed the generation 2 marker: %+v %v, want %+v", got, ok, want)
	}
}

// This proves only that looping writers leave a whole, correct marker (atomic
// replace plus convergence). Mutual exclusion is proved by the interleaved test
// below, which holds one writer inside its critical section.
func TestRejectionLoopingWritersLeaveAWholeMarker(t *testing.T) {
	rr := revokedCase(t)
	mk := func() *Daemon {
		return &Daemon{S: rr.s, R: rr.r, Log: rr.d.Log, Session: rr.session, Self: "/agentd", Now: func() time.Time { return rr.clk }}
	}
	a, b := mk(), mk()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	torn := make(chan string, 1)
	for _, d := range []*Daemon{a, b} {
		wg.Add(1)
		go func(d *Daemon) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					d.observeClaudeRejection()
				}
			}
		}(d)
	}
	wg.Add(1)
	go func() { // a reader must never see a torn marker
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if raw, err := os.ReadFile(rr.markerPath()); err == nil {
					var m claudeRejection
					if json.Unmarshal(raw, &m) != nil {
						select {
						case torn <- string(raw):
						default:
						}
					}
				}
			}
		}
	}()
	// Move the world forward under them: generation 2 lands, then a fresh refusal.
	// rr.clk is only changed here, before the writers can read it again, by
	// holding the clock steady and changing files instead.
	time.Sleep(20 * time.Millisecond)
	publish(t, rr.live, projAccess(2, rr.life))
	rr.d.syncClaudeAccess() // the one place SyncClaudeAccess runs, as in the daemon
	rr.stampReceipt()
	time.Sleep(20 * time.Millisecond)
	rr.appendTurn(revokedTurn(rr.clk.Add(time.Second)))
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	select {
	case raw := <-torn:
		t.Fatalf("a reader saw a torn marker: %q", raw)
	default:
	}
	rr.advance(2 * time.Second)
	a.observeClaudeRejection()
	m, ok := rr.marker()
	if !ok || m.Generation != 2 {
		t.Errorf("after the writers settled: marker %+v %v; want exactly generation 2", m, ok)
	}
	if !rr.rejected() || !strings.Contains(rr.text(), "generation 2") {
		t.Errorf("text = %q", rr.text())
	}
	if raw, _ := os.ReadFile(rr.markerPath()); strings.Count(string(raw), "generation") != 1 {
		t.Errorf("marker = %s", raw)
	}
}

func toolResult(at time.Time) string {
	return entry("user", at, "", `[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]`)
}

// Finding 2: look back past trailing user and tool_result entries to the most
// recent assistant turn.
func TestRejectionClearsOnAnAcceptedAssistantTurnEvenWithUserEntriesAfterIt(t *testing.T) {
	for name, tail := range map[string]func(at time.Time) []string{
		"accepted then user":        func(at time.Time) []string { return []string{acceptedTurn(at), userTurn(at.Add(time.Second))} },
		"accepted then tool_result": func(at time.Time) []string { return []string{acceptedTurn(at), toolResult(at.Add(time.Second))} },
		"accepted then several": func(at time.Time) []string {
			return []string{acceptedTurn(at), toolResult(at.Add(time.Second)), userTurn(at.Add(2 * time.Second))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			rr := revokedCase(t)
			rr.advance(time.Minute)
			rr.appendTurn(tail(rr.clk)...)
			rr.poll()
			if rr.markerExists() || !rr.readyOrLoggedIn() {
				t.Errorf("exists %v text %q", rr.markerExists(), rr.text())
			}
		})
	}
}

func TestRejectionStaysWhenTheMostRecentAssistantTurnIsTheRefusalOrAmbiguous(t *testing.T) {
	cases := map[string]func(rr *rejectionRig, at time.Time) []string{
		"refused then user":        func(rr *rejectionRig, at time.Time) []string { return []string{userTurn(at)} },
		"refused then tool_result": func(rr *rejectionRig, at time.Time) []string { return []string{toolResult(at)} },
		"unmodelled turn then user": func(rr *rejectionRig, at time.Time) []string {
			return []string{healthyTurn(at), userTurn(at.Add(time.Second))}
		},
		"accepted dated before the refusal then user": func(rr *rejectionRig, at time.Time) []string {
			m, _ := rr.marker()
			return []string{acceptedTurn(m.ObservedAt.Add(-time.Minute)), userTurn(at)}
		},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			rr := revokedCase(t)
			rr.advance(time.Minute)
			rr.appendTurn(lines(rr, rr.clk)...)
			rr.poll()
			if !rr.markerExists() || !rr.rejected() {
				t.Errorf("exists %v text %q", rr.markerExists(), rr.text())
			}
		})
	}
}

// Finding 3: a 401 for a request that began before the new generation was
// applied belongs to the old token. The request is the nearest preceding user
// entry (the prompt or the tool_result that triggered the call).
func TestRejectionAttributesByTheRequestNotTheFailure(t *testing.T) {
	setup := func(t *testing.T, lines func(applied time.Time) []string) *rejectionRig {
		t.Helper()
		rr := newRejectionRig(t)
		rr.publishGeneration(1)
		applied := rr.clk
		rr.advance(time.Minute)
		rr.transcript(lines(applied)...)
		rr.poll()
		return rr
	}
	t.Run("request before apply, failure after: not charged to the new generation", func(t *testing.T) {
		rr := setup(t, func(applied time.Time) []string {
			return []string{userTurn(applied.Add(-time.Minute)), revokedTurn(applied.Add(30 * time.Second))}
		})
		if rr.markerExists() || rr.rejected() {
			t.Errorf("an in-flight old-token failure was recorded against the new generation: %q", rr.text())
		}
	})
	t.Run("tool_result before apply, failure after", func(t *testing.T) {
		rr := setup(t, func(applied time.Time) []string {
			return []string{toolResult(applied.Add(-time.Second)), revokedTurn(applied.Add(30 * time.Second))}
		})
		if rr.markerExists() {
			t.Error("recorded")
		}
	})
	t.Run("request after apply: recorded", func(t *testing.T) {
		rr := setup(t, func(applied time.Time) []string {
			return []string{userTurn(applied.Add(10 * time.Second)), revokedTurn(applied.Add(30 * time.Second))}
		})
		if m, ok := rr.marker(); !ok || m.Generation != 1 {
			t.Errorf("marker %+v %v", m, ok)
		}
	})
	t.Run("no request entry available: falls back to the failure time (pinned limit)", func(t *testing.T) {
		rr := setup(t, func(applied time.Time) []string { return []string{revokedTurn(applied.Add(30 * time.Second))} })
		if m, ok := rr.marker(); !ok || m.Generation != 1 {
			t.Errorf("marker %+v %v", m, ok)
		}
	})
}

// Finding 4: a corrupt or missing receipt with a marker present is cannot-tell.
func TestRejectionUnreadableReceiptWithAMarkerIsCannotTell(t *testing.T) {
	for name, content := range map[string]string{"garbage": "not json", "empty": "", "generation zero": `{"generation":0,"fingerprint":"` + strings.Repeat("a", 64) + `"}`} {
		t.Run(name, func(t *testing.T) {
			rr := revokedCase(t)
			if err := os.WriteFile(rr.s.statePath("claude-access-generation.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if rr.readyOrLoggedIn() || !rr.unverified() {
				t.Errorf("text = %q; want cannot-tell", rr.text())
			}
		})
	}
	t.Run("no marker is still no rejection", func(t *testing.T) {
		rr := newRejectionRig(t)
		rr.publishGeneration(1)
		if err := os.WriteFile(rr.s.statePath("claude-access-generation.json"), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if rr.unverified() || rr.rejected() {
			t.Errorf("text = %q", rr.text())
		}
	})
}

// Finding 4: failures are logged in fixed words, never silent, and a rejection
// that could not be written still reaches the heartbeat.
func TestRejectionWriteFailureIsLoggedOnceAndStillReachesTheHeartbeat(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	rr := newRejectionRig(t)
	rr.publishGeneration(1)
	rr.advance(10 * time.Minute)
	rr.transcript(revokedTurn(rr.clk))
	var beats []protocol.Status
	rr.d.Send = func(_ context.Context, st protocol.Status) error { beats = append(beats, st); return nil }
	if err := writeJSONFile(rr.s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	// Create the lock while writable, then make the state directory read-only.
	rr.d.observeClaudeRejection()
	_ = os.Remove(rr.markerPath())
	if err := os.Chmod(rr.s.StateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rr.s.StateDir, 0o700) })
	for range 3 {
		rr.advance(5 * time.Second)
		rr.d.observeClaudeRejection()
	}
	if got := strings.Count(rr.logs.String(), "Claude rejection record could not be written"); got != 1 {
		t.Errorf("write failure logged %d times:\n%s", got, rr.logs.String())
	}
	if strings.Contains(rr.logs.String(), rr.s.StateDir) || strings.Contains(rr.logs.String(), "permission") {
		t.Errorf("the log line carries a path or error text:\n%s", rr.logs.String())
	}
	rr.d.beat(context.Background())
	if len(beats) != 1 || beats[0].Agent.State != protocol.AgentBusy || !strings.HasPrefix(beats[0].Agent.Error, "login-rejected:") {
		t.Errorf("heartbeat after a failed write: %+v", beats)
	}
	// Back to writable: the next poll persists it and the in-memory overlay ends.
	_ = os.Chmod(rr.s.StateDir, 0o700)
	rr.d.observeClaudeRejection()
	if !rr.markerExists() {
		t.Error("the marker was not written once the directory was writable")
	}
}

func TestRejectionRemoveFailureAndLockFailureAreLogged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	t.Run("remove", func(t *testing.T) {
		rr := revokedCase(t)
		rr.advance(time.Minute)
		publish(t, rr.live, projAccess(2, rr.life))
		rr.d.syncClaudeAccess()
		rr.stampReceipt()
		if err := os.Chmod(rr.s.StateDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(rr.s.StateDir, 0o700) })
		rr.d.observeClaudeRejection()
		if got := strings.Count(rr.logs.String(), "Claude rejection record could not be removed"); got != 1 {
			t.Errorf("remove failure logged %d times:\n%s", got, rr.logs.String())
		}
	})
	t.Run("lock", func(t *testing.T) {
		rr := newRejectionRig(t)
		rr.publishGeneration(1)
		_ = os.Remove(rr.s.statePath(rejectionLock))
		if err := os.Mkdir(rr.s.statePath(rejectionLock), 0o700); err != nil {
			t.Fatal(err)
		}
		rr.d.observeClaudeRejection()
		rr.d.observeClaudeRejection()
		if got := strings.Count(rr.logs.String(), "Claude rejection record could not be locked"); got != 1 {
			t.Errorf("lock failure logged %d times:\n%s", got, rr.logs.String())
		}
	})
}

// Finding 6: the heartbeat text must not change with the clock inside a state.
func TestRejectionHeartbeatTextIsStableWhileUnresolved(t *testing.T) {
	rr := revokedCase(t)
	a := rr.text()
	rr.advance(3 * time.Minute)
	if b := rr.text(); a != b {
		t.Errorf("text changed with the clock:\n%q\n%q", a, b)
	}
	m, _ := rr.marker()
	if !strings.Contains(a, m.ObservedAt.UTC().Format(time.RFC3339)) {
		t.Errorf("text %q lacks the fixed observation time", a)
	}
}

// Finding 7: exclusion, interleaved. Writer A is held inside its critical
// section after reading a stale marker; writer B, which would otherwise
// delete it and record the new generation's refusal, must do nothing.
func TestRejectionSecondWriterIsExcludedWhileTheFirstIsInsideItsCriticalSection(t *testing.T) {
	rr := revokedCase(t) // marker for generation 1
	other := &Daemon{S: rr.s, R: rr.r, Log: rr.d.Log, Session: rr.session, Self: "/agentd", Now: func() time.Time { return rr.clk }}
	rr.advance(time.Minute)
	publish(t, rr.live, projAccess(2, rr.life)) // applied, but no writer has polled yet
	rr.d.syncClaudeAccess()
	rr.stampReceipt()
	rr.advance(time.Minute)
	rr.appendTurn(revokedTurn(rr.clk)) // a refusal of generation 2 both writers could record
	before, _ := os.ReadFile(rr.markerPath())
	fired := 0
	rejectionAfterRead = func() {
		fired++
		if fired > 1 {
			return
		}
		other.observeClaudeRejection() // B, while A holds the lock
		during, err := os.ReadFile(rr.markerPath())
		if err != nil || string(during) != string(before) {
			t.Errorf("writer B changed the marker while A was inside its critical section: %q %v", during, err)
		}
	}
	t.Cleanup(func() { rejectionAfterRead = nil })
	rr.d.observeClaudeRejection() // A
	if m, ok := rr.marker(); !ok || m.Generation != 2 {
		t.Errorf("after A: marker %+v %v; want generation 2", m, ok)
	}
}
