package agentd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

// The send rule (DESIGN-059 step 4) with synthetic projections only: the
// Claude and Codex access documents are the same obvious fakes the projection
// tests use, and the readings come from synthetic rollouts and statusline
// documents.

// claudeProjected is a Claude remote session whose pod applied generation 1
// of the keeper's projection.
func claudeProjected(t *testing.T) (Settings, protocol.Session) {
	t.Helper()
	s := projHome(t, filepath.Join(t.TempDir(), "live", "access.json"))
	publish(t, s.ClaudeLiveFile, projAccess(1, time.Hour))
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	return s, protocol.Session{Name: "s", Agent: protocol.AgentClaude, Mode: protocol.ModeRemote}
}

// codexProjected is a Codex task whose pod installed generation 1 of the
// keeper's access-only projection.
func codexProjected(t *testing.T, now time.Time) (Settings, protocol.Session) {
	t.Helper()
	s := testSettings(t, t.TempDir())
	s.CodexAccessFile = filepath.Join(s.Home, "projection", "access.json")
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(raw))
	if err := SyncCodexAccess(s, now); err != nil {
		t.Fatal(err)
	}
	return s, protocol.Session{Name: "s", Agent: protocol.AgentCodex, Mode: protocol.ModeTask}
}

func TestAllowanceCredentialRefusalClaude(t *testing.T) {
	s, sess := claudeProjected(t)
	if got := allowanceCredentialRefusal(s, sess, loginNow); got != "" {
		t.Fatalf("the applied projection was refused: %q", got)
	}
	// The keeper published a newer generation the pod has not applied yet.
	publish(t, s.ClaudeLiveFile, projAccess(2, time.Hour))
	if got := allowanceCredentialRefusal(s, sess, loginNow); got != allowanceNotCurrent {
		t.Fatalf("a newer unapplied generation: %q", got)
	}
	if err := SyncClaudeAccess(s, loginNow); err != nil {
		t.Fatal(err)
	}
	if got := allowanceCredentialRefusal(s, sess, loginNow); got != "" {
		t.Fatalf("after the merge: %q", got)
	}
	// The access token lapsed: not the current projection any more.
	if got := allowanceCredentialRefusal(s, sess, loginNow.Add(2*time.Hour)); got != allowanceNotCurrent {
		t.Fatalf("a lapsed token: %q", got)
	}
	// A pod-owned login is never the keeper's projection.
	writeJSON(t, filepath.Join(s.ClaudeConfigDir, credentialsFileName), map[string]any{"claudeAiOauth": map[string]any{"accessToken": "x", "refreshToken": "canary-refresh", "expiresAt": 1}})
	if got := allowanceCredentialRefusal(s, sess, loginNow); got != allowanceNotCurrent {
		t.Fatalf("a pod-owned login: %q", got)
	}
	s.ClaudeLiveFile = ""
	if got := allowanceCredentialRefusal(s, sess, loginNow); got != allowanceNotProjection {
		t.Fatalf("no projection configured: %q", got)
	}
}

func TestAllowanceCredentialRefusalCodex(t *testing.T) {
	now := time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)
	s, sess := codexProjected(t, now)
	if got := allowanceCredentialRefusal(s, sess, now); got != "" {
		t.Fatalf("the installed projection was refused: %q", got)
	}
	// auth.json no longer the projection's file.
	authPath := filepath.Join(s.CodexHome, "auth.json")
	good, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","tokens":{"refresh_token":"canary"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := allowanceCredentialRefusal(s, sess, now); got != allowanceNotCurrent {
		t.Fatalf("a replaced auth.json: %q", got)
	}
	if err := os.WriteFile(authPath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	// A newer projection the pod has not installed.
	raw, err := codexauth.Encode(syntheticAgentCodexAccess(t, now.Add(time.Hour), 2), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(raw))
	if got := allowanceCredentialRefusal(s, sess, now.Add(time.Hour)); got != allowanceNotCurrent {
		t.Fatalf("a newer uninstalled generation: %q", got)
	}
	s.CodexAccessFile = ""
	if got := allowanceCredentialRefusal(s, sess, now); got != allowanceNotProjection {
		t.Fatalf("no projection configured: %q", got)
	}
	if got := allowanceCredentialRefusal(s, protocol.Session{Agent: protocol.AgentCursor}, now); got != allowanceNotProjection {
		t.Fatalf("cursor: %q", got)
	}
}

// beatOnce runs one heartbeat and returns what it sent.
func beatOnce(t *testing.T, d *Daemon) protocol.Status {
	t.Helper()
	var sent []protocol.Status
	d.Log = slog.New(slog.DiscardHandler)
	d.R = &fakeRunner{}
	d.Send = func(_ context.Context, st protocol.Status) error { sent = append(sent, st); return nil }
	d.beat(context.Background())
	if len(sent) != 1 {
		t.Fatalf("%d heartbeats", len(sent))
	}
	return sent[0]
}

func allowanceProblem(st protocol.Status) string {
	for _, p := range st.Problems {
		if p.Name == allowanceStepName {
			return strings.Join(p.Notes, " ")
		}
	}
	return ""
}

func TestHeartbeatCarriesCodexReading(t *testing.T) {
	now := time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)
	s, sess := codexProjected(t, now)
	at := now.Add(-2 * time.Minute)
	reset := at.Add(30 * time.Hour)
	writeRollout(t, s.CodexHome, "19", rolloutThread, tokenCount(at, rolloutWindow("65", protocol.WeeklyAllowanceMinutes, reset), "null"))
	d := &Daemon{S: s, Session: sess, Now: func() time.Time { return now }}

	st := beatOnce(t, d)
	if st.Allowance == nil || st.Allowance.Provider != protocol.AgentCodex || !st.Allowance.ReadAt.Equal(at) || st.Allowance.Windows[0].UsedPercent != "65" || allowanceProblem(st) != "" {
		t.Fatalf("allowance %+v, problem %q", st.Allowance, allowanceProblem(st))
	}

	// Not the current projection: nothing sent, and the step says why.
	if err := os.WriteFile(filepath.Join(s.CodexHome, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	st = beatOnce(t, d)
	if st.Allowance != nil || allowanceProblem(st) != "reading not sent: "+allowanceNotCurrent {
		t.Fatalf("allowance %+v, problem %q", st.Allowance, allowanceProblem(st))
	}

	// A record of the wrong shape is refused with its own reason.
	writeRollout(t, s.CodexHome, "19", rolloutThread, tokenCount(at, rolloutWindow(`"65"`, protocol.WeeklyAllowanceMinutes, reset), "null"))
	if st = beatOnce(t, d); st.Allowance != nil || allowanceProblem(st) != "reading not sent: "+allowanceShapeUnrecognized {
		t.Fatalf("allowance %+v, problem %q", st.Allowance, allowanceProblem(st))
	}
}

func TestHeartbeatWithoutReading(t *testing.T) {
	// A Codex session with no rollout, a Claude task, and a Cursor task send
	// neither a reading nor a step.
	now := time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)
	s, sess := codexProjected(t, now)
	for _, session := range []protocol.Session{sess, {Name: "s", Agent: protocol.AgentClaude, Mode: protocol.ModeTask}, {Name: "s", Agent: protocol.AgentCursor, Mode: protocol.ModeTask}} {
		st := beatOnce(t, &Daemon{S: s, Session: session, Now: func() time.Time { return now }})
		if st.Allowance != nil || allowanceProblem(st) != "" {
			t.Errorf("%s/%s: %+v %q", session.Agent, session.Mode, st.Allowance, allowanceProblem(st))
		}
	}
	// A Codex pod without the keeper's projection has a rollout and still
	// sends nothing, and names no problem.
	writeRollout(t, s.CodexHome, "19", rolloutThread, tokenCount(now.Add(-time.Minute), weekly("5"), "null"))
	s.CodexAccessFile = ""
	if st := beatOnce(t, &Daemon{S: s, Session: sess, Now: func() time.Time { return now }}); st.Allowance != nil || allowanceProblem(st) != "" {
		t.Errorf("no projection: %+v %q", st.Allowance, allowanceProblem(st))
	}
}

func TestHeartbeatCarriesClaudeReading(t *testing.T) {
	s, sess := claudeProjected(t)
	if err := os.MkdirAll(s.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	at := loginNow.Add(-time.Minute)
	seven := at.Add(50 * time.Hour)
	writeUsageFile(t, s, statuslineDoc(t, callbackInput("", "34", time.Time{}, seven), at))
	d := &Daemon{S: s, Session: sess, Now: func() time.Time { return loginNow }}
	st := beatOnce(t, d)
	if st.Allowance == nil || st.Allowance.Provider != protocol.AgentClaude || !st.Allowance.ReadAt.Equal(at) || allowanceProblem(st) != "" {
		t.Fatalf("allowance %+v, problem %q", st.Allowance, allowanceProblem(st))
	}
	// A later callback with the same numbers keeps the first reading time.
	writeUsageFile(t, s, statuslineDoc(t, callbackInput("", "34", time.Time{}, seven), at.Add(30*time.Second)))
	if st = beatOnce(t, d); st.Allowance == nil || !st.Allowance.ReadAt.Equal(at) {
		t.Fatalf("same values moved the reading time: %+v", st.Allowance)
	}
	// The keeper published a generation the pod has not applied: refused.
	publish(t, d.S.ClaudeLiveFile, projAccess(2, time.Hour))
	if st = beatOnce(t, d); st.Allowance != nil || allowanceProblem(st) != "reading not sent: "+allowanceNotCurrent {
		t.Fatalf("allowance %+v, problem %q", st.Allowance, allowanceProblem(st))
	}
	// A pod that does not follow the projection takes no reading at all.
	d.S.ClaudeLiveFile = ""
	if st = beatOnce(t, d); st.Allowance != nil || allowanceProblem(st) != "" {
		t.Fatalf("allowance %+v, problem %q", st.Allowance, allowanceProblem(st))
	}
}

// A callback with no new values between two heartbeats does not move the
// reading time, because the poll saw the reply's file first.
func TestStatuslinePollKeepsTheReplyTime(t *testing.T) {
	s, sess := claudeProjected(t)
	if err := os.MkdirAll(s.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	reply := loginNow.Add(-50 * time.Second)
	seven := reply.Add(50 * time.Hour)
	d := &Daemon{S: s, Session: sess, Now: func() time.Time { return loginNow }}
	writeUsageFile(t, s, statuslineDoc(t, callbackInput("", "34", time.Time{}, seven), reply))
	d.observeStatusline()
	writeUsageFile(t, s, statuslineDoc(t, callbackInput("", "34", time.Time{}, seven), reply.Add(30*time.Second)))
	if st := beatOnce(t, d); st.Allowance == nil || !st.Allowance.ReadAt.Equal(reply) {
		t.Fatalf("%+v", st.Allowance)
	}
	// A Claude task never watches the file.
	task := &Daemon{S: s, Session: protocol.Session{Name: "s", Agent: protocol.AgentClaude, Mode: protocol.ModeTask}}
	task.observeStatusline()
	if task.statusline.last != nil {
		t.Fatal("a task read the statusline")
	}
}
