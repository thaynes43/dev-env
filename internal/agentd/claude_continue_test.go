package agentd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Story110 T09 increment 2: one continue after a revoked-token turn once a new
// generation lands. The transcript lines are synthetic: the real CLI's error
// text is confirmed at the checkpoint.

const continueConv = "11111111-aaaa-4bbb-8ccc-222222222222"

func entry(kind string, at time.Time, extra, content string) string {
	return fmt.Sprintf(`{"type":%q,"timestamp":%q,"isSidechain":false%s,"message":{"role":%q,"content":%s}}`,
		kind, at.Format(time.RFC3339Nano), extra, kind, content)
}

func healthyTurn(at time.Time) string {
	return entry("assistant", at, "", `[{"type":"text","text":"done, tests pass"}]`)
}

func revokedTurn(at time.Time) string {
	return entry("assistant", at, `,"isApiErrorMessage":true`,
		`[{"type":"text","text":"API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"OAuth token has been revoked\"}}"}]`)
}

func userTurn(at time.Time) string { return entry("user", at, "", `"please keep going"`) }

type continueRig struct {
	t       *testing.T
	s       Settings
	live    string
	r       *fakeRunner
	logs    *strings.Builder
	session protocol.Session
	d       *Daemon
	sent    []string
	lines   []string
}

// newContinueRig is a pod whose TUI runs (boot record, launch, live process).
func newContinueRig(t *testing.T, mode string, projected, running bool) *continueRig {
	t.Helper()
	live := filepath.Join(t.TempDir(), "claude-live", "access.json")
	s := projHome(t, live)
	if !projected {
		s.ClaudeLiveFile = ""
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(launchFile), Launch{BootID: "b1", TUI: true, ConversationID: continueConv}); err != nil {
		t.Fatal(err)
	}
	old := deliverSettle
	deliverSettle = 0
	t.Cleanup(func() { deliverSettle = old })
	rg := &continueRig{t: t, s: s, live: live, logs: &strings.Builder{}}
	rg.r = &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Stdin != nil {
			b, _ := io.ReadAll(c.Stdin)
			rg.sent = append(rg.sent, string(b))
		}
		return Result{}, nil
	}}
	rg.session = remoteSession()
	rg.session.Mode = mode
	if mode == protocol.ModeTask {
		rg.session.Prompt = "synthetic"
	}
	if running {
		rg.startAgent()
	}
	rg.restart()
	return rg
}

func (rg *continueRig) startAgent() {
	cli := exec.Command("sleep", "30")
	if err := cli.Start(); err != nil {
		rg.t.Fatal(err)
	}
	rg.t.Cleanup(func() { _ = cli.Process.Kill(); _ = cli.Wait() })
	if err := writeJSONFile(rg.s.statePath(pidFile), agentPid{Pid: cli.Process.Pid, Start: procStartTime(cli.Process.Pid)}); err != nil {
		rg.t.Fatal(err)
	}
}

// restart is a fresh agentd process on the same volume.
func (rg *continueRig) restart() {
	rg.d = &Daemon{S: rg.s, R: rg.r, Log: slog.New(slog.NewTextHandler(rg.logs, nil)), Session: rg.session, Self: "/agentd",
		Now: func() time.Time { return loginNow.Add(time.Hour) }}
}

// transcript replaces the session's transcript with lines.
func (rg *continueRig) transcript(lines ...string) {
	rg.lines = lines
	rg.write()
}

func (rg *continueRig) appendTurn(lines ...string) {
	rg.lines = append(rg.lines, lines...)
	rg.write()
}

func (rg *continueRig) write() {
	p := filepath.Join(rg.s.ClaudeConfigDir, "projects", "-work-repo", continueConv+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		rg.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(rg.lines, "\n")+"\n"), 0o600); err != nil {
		rg.t.Fatal(err)
	}
}

// generation publishes generation g and runs one poll's worth of work.
func (rg *continueRig) generation(g uint64) {
	publish(rg.t, rg.live, projAccess(g, 9*time.Hour))
	rg.poll()
}

func (rg *continueRig) poll() {
	rg.d.syncClaudeAccess()
	rg.d.continueAfterRenewal(context.Background())
}

func (rg *continueRig) continues() int {
	n := 0
	for _, m := range rg.sent {
		if strings.Contains(m, continueText) {
			n++
		}
	}
	return n
}

func (rg *continueRig) assertNoToken() {
	rg.t.Helper()
	all := rg.logs.String() + strings.Join(rg.r.lines(), "\n") + strings.Join(rg.sent, "\n")
	for _, secret := range []string{projToken(1), projToken(2), projToken(3), projMCP, canaryRefresh} {
		if strings.Contains(all, secret) {
			rg.t.Errorf("a token reached the logs, a command line or the delivered text")
		}
	}
}

// A revoked last turn followed by a new generation sends exactly one continue;
// repeated polls and an agentd restart do not send another.
func TestClaudeContinueSendsOnceAfterRevokedTurnAndNewGeneration(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.generation(1)
	if rg.continues() != 0 {
		t.Fatal("sent on a healthy first generation")
	}
	// The token is revoked mid-session: the turn ends in the 401, nothing new yet.
	rg.appendTurn(userTurn(loginNow.Add(2*time.Hour)), revokedTurn(loginNow.Add(2*time.Hour+time.Minute)))
	rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
	rg.poll()
	if rg.continues() != 0 {
		t.Fatal("sent before a new generation was merged")
	}
	rg.generation(2)
	if rg.continues() != 1 {
		t.Fatalf("continues after the new generation: %d", rg.continues())
	}
	if !strings.Contains(rg.sent[len(rg.sent)-1], "[Message from agentd/claude-login") {
		t.Errorf("not sent through Deliver: %q", rg.sent)
	}
	for i := 0; i < 5; i++ {
		rg.poll()
	}
	rg.restart()
	rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
	rg.poll()
	rg.generation(2)
	if rg.continues() != 1 {
		t.Errorf("repeated polls or a restart sent again: %d", rg.continues())
	}
	var rc continueReceipt
	if err := readJSONFile(rg.s.statePath(continueReceiptFile), &rc); err != nil || rc.Generation != 2 || rc.Outcome != "sent" {
		t.Errorf("receipt %+v %v", rc, err)
	}
	// A further revoked turn is handled by the NEXT generation, once.
	rg.appendTurn(userTurn(loginNow.Add(4*time.Hour)), revokedTurn(loginNow.Add(4*time.Hour+time.Minute)))
	rg.d.Now = func() time.Time { return loginNow.Add(5 * time.Hour) }
	rg.poll()
	if rg.continues() != 1 {
		t.Error("sent twice for one generation")
	}
	rg.generation(3)
	rg.poll()
	if rg.continues() != 2 {
		t.Errorf("generation 3 continues: %d", rg.continues())
	}
	rg.assertNoToken()
}

// A restart between the send and the receipt's final write still sends once:
// the receipt is written before the send.
func TestClaudeContinueDoesNotRepeatAnUncertainSend(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	if err := writeJSONFile(rg.s.statePath(continueReceiptFile), continueReceipt{Generation: 1, Outcome: "sending", EvaluatedAt: loginNow.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rg.generation(1)
	if rg.continues() != 0 {
		t.Error("repeated a send whose receipt says it may have happened")
	}
}

func TestClaudeContinueNotSentWhenLastTurnHealthy(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)), userTurn(loginNow.Add(2*time.Minute)), healthyTurn(loginNow.Add(3*time.Minute)))
	rg.generation(1)
	rg.generation(2)
	if rg.continues() != 0 {
		t.Errorf("sent on a healthy last turn: %d", rg.continues())
	}
}

func TestClaudeContinueNotSentWhenAUserTurnIsNewer(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)), userTurn(loginNow.Add(2*time.Minute)))
	rg.generation(1)
	if rg.continues() != 0 {
		t.Errorf("sent over a newer user turn: %d", rg.continues())
	}
	// Sidechain and housekeeping entries after the error are not turns.
	rg2 := newContinueRig(t, protocol.ModeRemote, true, true)
	rg2.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)),
		`{"type":"summary","summary":"x"}`, `{"type":"assistant","isSidechain":true,"message":{"content":"sub"}}`)
	rg2.generation(1)
	if rg2.continues() != 1 {
		t.Errorf("housekeeping entries hid the error: %d", rg2.continues())
	}
}

// A merge that is held back (an older generation, or a pod-owned login) never
// triggers a continue; a later accepted generation does.
func TestClaudeContinueNotSentWhenTheMergeIsHeldBack(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.generation(2)
	rg.appendTurn(userTurn(loginNow.Add(2*time.Hour)), revokedTurn(loginNow.Add(2*time.Hour+time.Minute)))
	rg.generation(1) // older than the applied generation: refused
	rg.poll()
	if rg.continues() != 0 {
		t.Fatalf("sent while the merge was held back: %d", rg.continues())
	}
	rg.generation(3)
	if rg.continues() != 1 {
		t.Errorf("not sent once a newer generation merged: %d", rg.continues())
	}
}

func TestClaudeContinueNotSentForAPodOwnedLoginOrATask(t *testing.T) {
	// Pod-owned mode: no projection, generation receipts or continues at all.
	owned := newContinueRig(t, protocol.ModeRemote, false, true)
	owned.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	publish(t, owned.live, projAccess(1, 9*time.Hour))
	owned.poll()
	if owned.continues() != 0 {
		t.Error("sent in pod-owned mode")
	}
	if _, err := os.Stat(owned.s.statePath(continueReceiptFile)); err == nil {
		t.Error("pod-owned mode wrote a continue receipt")
	}
	task := newContinueRig(t, protocol.ModeTask, true, true)
	task.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	task.generation(1)
	if task.continues() != 0 {
		t.Error("sent in task mode")
	}
}

// With the agent not running nothing is sent and no receipt is written, so the
// continue still happens once the agent is up.
func TestClaudeContinueWaitsForTheAgent(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, false)
	rg.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	rg.generation(1)
	rg.poll()
	if rg.continues() != 0 {
		t.Fatal("sent with no agent")
	}
	if _, err := os.Stat(rg.s.statePath(continueReceiptFile)); err == nil {
		t.Fatal("a receipt was written with no agent")
	}
	rg.startAgent()
	rg.poll()
	rg.poll()
	if rg.continues() != 1 {
		t.Errorf("continues after the agent started: %d", rg.continues())
	}
}

// An error that predates the previous evaluation was already handled.
func TestClaudeContinueIgnoresAnErrorOlderThanThePreviousEvaluation(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	if err := writeJSONFile(rg.s.statePath(continueReceiptFile), continueReceipt{Generation: 1, Outcome: "skipped", EvaluatedAt: loginNow.Add(30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	rg.generation(1)
	rg.generation(2)
	if rg.continues() != 0 {
		t.Errorf("sent for a stale error: %d", rg.continues())
	}
}

func TestClassifyTranscriptShapes(t *testing.T) {
	cases := map[string]struct {
		in   string
		want bool
	}{
		"revoked":           {revokedTurn(loginNow), true},
		"expired text only": {entry("assistant", loginNow, "", `[{"type":"text","text":"API Error: 401 OAuth token has expired"}]`), true},
		"discussion":        {entry("assistant", loginNow, "", `[{"type":"text","text":"the oauth token has expired in the docs"}]`), false},
		"healthy":           {healthyTurn(loginNow), false},
		"user quoting":      {entry("user", loginNow, "", `"API Error: OAuth token has been revoked"`), false},
		"garbage":           {"not json", false},
	}
	for name, c := range cases {
		if got := classifyTranscript([]byte(c.in + "\n")).Revoked; got != c.want {
			t.Errorf("%s: revoked=%v want %v", name, got, c.want)
		}
	}
}

// The real CLI 401 entry (NB-03, 2.37.0) and the neighbours that must not match.
func TestClassifyTranscriptRealCLIAuthFailure(t *testing.T) {
	const apiErr = `,"isApiErrorMessage":true`
	text := func(s string) string { return `[{"type":"text","text":` + fmt.Sprintf("%q", s) + `}]` }
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"real entry: status, code and text", entry("assistant", loginNow, apiErr+`,"apiErrorStatus":401,"error":"authentication_failed"`, text("Not logged in · Please run /login")), true},
		{"status 401 only", entry("assistant", loginNow, apiErr+`,"apiErrorStatus":401`, text("something unrecognised")), true},
		{"error code only", entry("assistant", loginNow, apiErr+`,"error":"authentication_failed"`, text("something unrecognised")), true},
		{"please run /login, mixed case", entry("assistant", loginNow, apiErr, text("Not logged in · PLEASE RUN /LOGIN")), true},
		{"oauth token has been revoked", entry("assistant", loginNow, apiErr, text("OAuth token has been revoked")), true},
		{"api error prefix without the flag", entry("assistant", loginNow, `,"apiErrorStatus":401`, text("API Error: 401 unauthorized")), true},
		{"error as an object does not hide the entry", entry("assistant", loginNow, apiErr+`,"apiErrorStatus":"401","error":{"type":"x"}`, text("Not logged in · Please run /login")), true},
		{"429 rate limit", entry("assistant", loginNow, apiErr+`,"apiErrorStatus":429,"error":"rate_limit"`, text("API Error: 429 rate limited")), false},
		{"500 server error", entry("assistant", loginNow, apiErr+`,"apiErrorStatus":500,"error":"unknown"`, text("API Error: 500 internal server error")), false},
		{"overloaded_error", entry("assistant", loginNow, apiErr+`,"apiErrorStatus":529`, text(`API Error: 529 {"error":{"type":"overloaded_error"}}`)), false},
		{"prose about login, not an error", entry("assistant", loginNow, "", text("You may need to please run /login if the token is gone")), false},
		{"prose with status field but no api error", entry("assistant", loginNow, `,"apiErrorStatus":401,"error":"authentication_failed"`, text("the docs say a 401 means please run /login")), false},
		{"user quoting the real text", entry("user", loginNow, apiErr+`,"apiErrorStatus":401`, `"Not logged in · Please run /login"`), false},
	}
	for _, c := range cases {
		if got := classifyTranscript([]byte(c.in + "\n")).Revoked; got != c.want {
			t.Errorf("%s: revoked=%v want %v", c.name, got, c.want)
		}
	}
}

// A generation applied while the agent was down is not acted on if the
// projection has since become unusable (the merge is held back).
func TestClaudeContinueNotSentWhileTheProjectionIsRefused(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, false)
	rg.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	rg.generation(1)
	writeRaw(t, rg.live, []byte(`{"generation":2,"refreshToken":"canary-not-a-real-token"}`))
	rg.startAgent()
	rg.poll()
	if rg.continues() != 0 {
		t.Fatalf("sent while the projection was refused: %d", rg.continues())
	}
	rg.generation(2)
	if rg.continues() != 1 {
		t.Errorf("not sent once a valid newer generation merged: %d", rg.continues())
	}
}

// A request in flight on the old token is a user turn when the new generation
// lands: nothing is decided then, and its 401 is handled on a later poll.
func TestClaudeContinueWaitsOutAnInFlightRequest(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)), userTurn(loginNow.Add(2*time.Hour)))
	rg.generation(1)
	rg.poll()
	if rg.continues() != 0 {
		t.Fatal("sent over an in-flight request")
	}
	if _, err := os.Stat(rg.s.statePath(continueReceiptFile)); err == nil {
		t.Fatal("a receipt was written while a user turn was last")
	}
	rg.appendTurn(revokedTurn(loginNow.Add(2*time.Hour + time.Minute)))
	rg.poll()
	rg.poll()
	if rg.continues() != 1 {
		t.Errorf("continues after the 401 landed: %d", rg.continues())
	}
}

// The projected replacement can arrive before the native client records its
// next turn and resulting 401. A healthy last entry at merge time must not
// permanently consume the replacement generation's recovery opportunity.
func TestClaudeContinueHandlesAuthErrorAfterHealthyMergeEvaluation(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), healthyTurn(loginNow.Add(time.Minute)))
	rg.generation(1)
	rg.d.Now = func() time.Time { return loginNow.Add(3 * time.Hour) }
	rg.generation(2)
	if rg.continues() != 0 {
		t.Fatal("sent before an authentication failure")
	}
	// Healthy polling and a daemon restart must retain the original eligibility
	// time, rather than moving it past the delayed error's timestamp.
	rg.d.Now = func() time.Time { return loginNow.Add(3*time.Hour + 10*time.Second) }
	rg.poll()
	rg.restart()
	rg.d.Now = func() time.Time { return loginNow.Add(3*time.Hour + 15*time.Second) }
	rg.poll()
	rg.appendTurn(userTurn(loginNow.Add(3*time.Hour + time.Second)))
	rg.poll()
	if rg.continues() != 0 {
		t.Fatal("sent over an in-flight user request")
	}
	rg.appendTurn(revokedTurn(loginNow.Add(3*time.Hour + 2*time.Second)))
	rg.poll()
	if rg.continues() != 1 {
		t.Fatalf("continues after a delayed old-token 401: %d, want 1", rg.continues())
	}
	rg.poll()
	rg.restart()
	rg.poll()
	if rg.continues() != 1 {
		t.Fatal("repeated recovery after a confirmed send")
	}
	rg.assertNoToken()
}

// A failure before anything reaches the pane is retried; one after the paste
// started is not.
func TestClaudeContinueRetriesOnlyFailuresBeforeThePaste(t *testing.T) {
	rg := newContinueRig(t, protocol.ModeRemote, true, true)
	rg.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	failLoad := true
	base := rg.r.handle
	rg.r.handle = func(c Cmd) (Result, error) {
		if c.Args[0] == "load-buffer" && failLoad {
			return Result{}, &CmdError{Name: "tmux", ExitCode: 1}
		}
		return base(c)
	}
	rg.generation(1)
	if rg.continues() != 0 {
		t.Fatal("sent although tmux load-buffer failed")
	}
	if _, err := os.Stat(rg.s.statePath(continueReceiptFile)); err == nil {
		t.Fatal("a receipt remained after a failure before the paste")
	}
	failLoad = false
	rg.poll()
	rg.poll()
	if rg.continues() != 1 {
		t.Errorf("continues after the retry: %d", rg.continues())
	}

	late := newContinueRig(t, protocol.ModeRemote, true, true)
	late.transcript(userTurn(loginNow), revokedTurn(loginNow.Add(time.Minute)))
	lbase := late.r.handle
	failEnter := true
	late.r.handle = func(c Cmd) (Result, error) {
		if c.Args[0] == "send-keys" && failEnter {
			return Result{}, &CmdError{Name: "tmux", ExitCode: 1}
		}
		return lbase(c)
	}
	late.generation(1)
	failEnter = false
	late.poll()
	late.poll()
	if late.continues() != 1 {
		t.Errorf("a failure after the paste was retried: %d sends", late.continues())
	}
	var rc continueReceipt
	if err := readJSONFile(late.s.statePath(continueReceiptFile), &rc); err != nil || rc.Outcome != "sending" {
		t.Errorf("receipt %+v %v", rc, err)
	}
}
