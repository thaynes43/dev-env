package agentd

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The model pre-flight of a summoned task (DESIGN-064 6.9, story 791; the
// coordinator's rulings (c) and (d) on item 743 comment 7540 and answer
// op791-a-1; not rulings by Tom). Before the provider of a summoned task's
// first launch starts, run-agent runs one short turn on the session's model
// ("reply with ok", as v1's session-launch.sh does). When that model does not
// answer and its login was not rejected, it tries the session's fallback
// model once. The task starts on the first model that answered. When neither
// did, nothing starts, and start-preflight.json says why, so the operator
// closes the session no-usable-model.
//
// Every turn is a provider process inside story 780's start fence: the start
// deadline is checked before each turn and again before the real start, and
// a turn's limit is cut to the time left. A turn the deadline cut short is
// the fence's refusal (start-refused.json), never a cause here. One provider
// process runs at a time: a turn is stopped and waited for before the next
// one or the real start is spawned.
//
// An ordinary launch has no Preflight and runs exactly as before: only
// BuildLaunch sets it, and only for a summoned Claude task.

const (
	// startPreflightFile is ~/.agentd/start-preflight.json, run-agent's record
	// of the pre-flight of the summoned launch it names.
	startPreflightFile = "start-preflight.json"
	// preflightPrompt is v1's probe text (session-launch.sh, probe_model).
	preflightPrompt = "reply with ok"
	// preflightTurnLimit is v1's limit on one probe turn.
	preflightTurnLimit = 90 * time.Second
	// preflightOutputMax bounds what run-agent keeps of a turn's output.
	preflightOutputMax = 256 << 10
)

// launchPreflight is Launch.Preflight: the models the pre-flight tries, in
// order. Model is the session's model, the one the launch's argv names.
type launchPreflight struct {
	Model    string `json:"model"`
	Fallback string `json:"fallback,omitempty"`
}

// The states of a startPreflight.
const (
	// preflightChecking: run-agent is running the pre-flight, or it ended
	// without a record of the start (its process died, or the start fence
	// refused the spawn).
	preflightChecking = "checking"
	// preflightStarted: the provider started on Model.
	preflightStarted = "started"
	// preflightNoUsableModel: no model answered; Failure says why and
	// nothing started.
	preflightNoUsableModel = "no-usable-model"
)

// startPreflight is start-preflight.json. Pid and Start name the run-agent
// process that runs the pre-flight, so the status reads busy only while it
// lives (procStartTime, as for agent.pid).
type startPreflight struct {
	State string `json:"state"`
	Model string `json:"model,omitempty"`
	// FallbackCause is why the session's model did not answer when Model is
	// the fallback.
	FallbackCause  string    `json:"fallbackCause,omitempty"`
	Failure        string    `json:"failure,omitempty"`
	Pid            int       `json:"pid"`
	Start          string    `json:"start"`
	At             time.Time `json:"at"`
	ConversationID string    `json:"conversationId"`
	BootID         string    `json:"bootId"`
}

// preflightOutcome is how one pre-flight turn ended.
type preflightOutcome struct {
	// Answered: the model replied (classifyPreflight).
	Answered bool
	// Failure is the turn's cause when it did not answer: a
	// protocol.StartFailure value.
	Failure string
	// TimedOut: the turn reached its limit and was stopped.
	TimedOut bool
	// Stopped: run-agent received a signal during the turn.
	Stopped bool
	// Gone: the turn's process was shown to have ended. When it was not,
	// nothing further starts.
	Gone bool
}

// preflightTurn runs one pre-flight turn of l on model, stopped at limit. It
// is runPreflightTurn; tests replace it with a provider stand-in.
var preflightTurn = runPreflightTurn

// preflightArgv is a pre-flight turn's command: one headless turn on model
// with no tools (--tools ""), no MCP servers, no saved session and one turn
// at most, its output as stream-json for classifyPreflight. The prompt goes
// to stdin. No --dangerously-skip-permissions: with no tools there is
// nothing to permit.
func preflightArgv(bin, model string) []string {
	return []string{bin, "--model", model,
		"--tools", "", "--strict-mcp-config", "--no-session-persistence", "--max-turns", "1",
		"--output-format", "stream-json", "--verbose", "-p"}
}

// preflightModelArg is the index of the launch argv's model value: claudeArgs
// puts "--model" and the session's model right after the CLI. -1 when the
// argv does not have that shape, so run-agent cannot run it on another model.
func preflightModelArg(argv []string, model string) int {
	if len(argv) > 2 && argv[1] == "--model" && argv[2] == model {
		return 2
	}
	return -1
}

// withModel is argv with its model value at i replaced by model; argv is not
// changed, so the launch run-agent read stays as agentd wrote it.
func withModel(argv []string, i int, model string) []string {
	out := slices.Clone(argv)
	out[i] = model
	return out
}

// preflightLimit is the next turn's limit at now: 90 seconds, or the time
// left to the launch's start deadline when that is shorter (cut). The caller
// has checked the deadline with spawnFence just before.
func preflightLimit(l Launch, now time.Time) (limit time.Duration, cut bool) {
	limit = preflightTurnLimit
	if l.StartDeadline == "" {
		return limit, false
	}
	deadline, err := protocol.ParseStartDeadline(l.StartDeadline)
	if err != nil {
		return 0, true
	}
	if left := deadline.Sub(now); left < limit {
		return left, true
	}
	return limit, false
}

// runPreflight runs the pre-flight of l before its provider starts. It
// returns the model the provider starts on and, when that is the fallback,
// why the session's model did not answer; or ok false when nothing may
// start: no model answered (recorded as no-usable-model), the start fence
// refused (recorded by the fence), a signal stopped it, or a record could not
// be written. note writes to the pane and the task log.
func runPreflight(l Launch, stateDir string, note func(string, ...any), signals <-chan os.Signal) (model, fallbackCause string, ok bool) {
	pf := l.Preflight
	if preflightModelArg(l.Argv, pf.Model) < 0 {
		note("the launch does not name its model where the pre-flight expects it; the provider was not started")
		return "", "", false
	}
	self := os.Getpid()
	rec := startPreflight{State: preflightChecking, Pid: self, Start: procStartTime(self), At: runAgentNow().UTC(), ConversationID: l.ConversationID, BootID: l.BootID}
	// Without this record the status cannot tell a running pre-flight from a
	// process that is gone, so nothing starts without it.
	if err := writeWorkspaceJSON(statePathIn(stateDir, startPreflightFile), rec); err != nil {
		note("the pre-flight record could not be written; the provider was not started")
		return "", "", false
	}
	models := []string{pf.Model}
	if pf.Fallback != "" && pf.Fallback != pf.Model {
		models = append(models, pf.Fallback)
	}
	failure := protocol.StartFailureNoAnswer
	for i, m := range models {
		if why := spawnFence(l, stateDir); why != "" {
			note("%s; the provider was not started (DESIGN-064 5.9)", why)
			return "", "", false
		}
		limit, cut := preflightLimit(l, runAgentNow())
		res := preflightTurn(l, m, limit, signals)
		switch {
		case res.Stopped:
			note("the model pre-flight was stopped by a signal; the provider was not started")
			return "", "", false
		case res.TimedOut && cut:
			// The start deadline cut this turn short: no model was refused,
			// time ran out. That is the fence's refusal (ruling (c)).
			now := runAgentNow()
			writeStartRefused(l, stateDir, protocol.StartDeadlinePassed, now)
			note("%s during the model pre-flight of %s; the provider was not started (DESIGN-064 5.9)", protocol.StartDeadlinePassed, m)
			return "", "", false
		case res.Answered && res.Gone:
			if i > 0 {
				note("model pre-flight: %s did not answer (%s); starting on the fallback %s", pf.Model, failure, m)
				return m, failure, true
			}
			note("model pre-flight: %s answered", m)
			return m, "", true
		}
		failure = res.Failure
		if !protocol.InSet(protocol.StartFailures, failure) {
			failure = protocol.StartFailureNoAnswer
		}
		note("model pre-flight: %s did not answer: %s", m, failure)
		if !res.Gone {
			// One provider process at a time: one that cannot be shown gone
			// starts nothing further (op791-a-1, point 6).
			failure = protocol.StartFailureNoAnswer
			note("the pre-flight's process could not be shown stopped; nothing further starts")
			break
		}
		if failure == protocol.StartFailureLoginRejected {
			// Ruling (d): the fallback runs on the same token.
			break
		}
	}
	rec.State, rec.Failure, rec.At = preflightNoUsableModel, failure, runAgentNow().UTC()
	if err := writeWorkspaceJSON(statePathIn(stateDir, startPreflightFile), rec); err != nil {
		note("the pre-flight's no-usable-model record could not be written")
	}
	note("no usable model (%s); the provider was not started (DESIGN-064 6.9)", failure)
	return "", "", false
}

// recordPreflightStarted updates start-preflight.json once the provider
// started on model (and why the session's model did not answer, when model
// is the fallback). A failed write leaves the checking record, whose status
// follows the live provider as for any launch.
func recordPreflightStarted(l Launch, stateDir, model, fallbackCause string) {
	var rec startPreflight
	if readJSONFile(statePathIn(stateDir, startPreflightFile), &rec) != nil || rec.ConversationID != l.ConversationID || rec.BootID != l.BootID {
		return
	}
	rec.State, rec.Model, rec.FallbackCause, rec.At = preflightStarted, model, fallbackCause, runAgentNow().UTC()
	_ = writeWorkspaceJSON(statePathIn(stateDir, startPreflightFile), rec)
}

// runPreflightTurn runs one pre-flight turn as a provider process in its own
// process group, on the launch's environment and worktree, and stops it at
// limit or at a signal. It returns once the process has ended, or after the
// stop could not be shown within drainWait plus a second; then Gone is false.
func runPreflightTurn(l Launch, model string, limit time.Duration, signals <-chan os.Signal) preflightOutcome {
	argv := preflightArgv(l.Argv[0], model)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = agentEnv(os.Environ(), l)
	cmd.Stdin = strings.NewReader(preflightPrompt)
	stdout := &tailBuffer{max: preflightOutputMax}
	cmd.Stdout, cmd.Stderr = stdout, io.Discard
	cmd.WaitDelay = drainWait
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if limit <= 0 {
		return preflightOutcome{Failure: protocol.StartFailureNoAnswer, TimedOut: true, Gone: true}
	}
	if err := cmd.Start(); err != nil {
		// Nothing ran: the CLI could not be started.
		return preflightOutcome{Failure: protocol.StartFailureNoAnswer, Gone: true}
	}
	pid := cmd.Process.Pid
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	var out preflightOutcome
	var werr error
	exited := false
	select {
	case werr = <-waitDone:
		exited = true
	case <-timer.C:
		out.TimedOut = true
	case <-signals:
		out.Stopped = true
	}
	if !exited {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case werr = <-waitDone:
			exited = true
		case <-time.After(drainWait + time.Second):
		}
	}
	// Whatever the CLI left in its process group goes with it.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	out.Gone = exited
	if !exited || out.TimedOut || out.Stopped {
		out.Failure = protocol.StartFailureNoAnswer
		return out
	}
	out.Answered, out.Failure = classifyPreflight(stdout.Bytes(), exitCode(cmd, werr) == 0)
	return out
}

// The fragments v1's probe_model reads as a refusal (session-launch.sh line
// 121), split by cause. They are matched only on an entry classifyEntries
// already calls an API error, so a reply that quotes them is never one. Not
// verified against the real CLI: no test calls a provider.
var (
	preflightQuotaFragments = []string{"out of usage credits", "usage limit", "rate limit"}
	preflightModelFragments = []string{"unknown model", "invalid model", "not available", "/model to switch"}
)

// classifyPreflight reads a pre-flight turn's stream-json output with the
// classifier agentd already uses for a session's transcript
// (classifyLastAssistant). A rejected login is its Revoked; an answer is its
// Accepted, a real model response, from a CLI that exited 0. An API error
// names a quota or a model refusal by v1's fragments. Anything else, an
// unmatched API error or no entry at all, is no answer (op791-a-1).
func classifyPreflight(stdout []byte, exitOK bool) (answered bool, failure string) {
	t := classifyLastAssistant(stdout)
	switch {
	case t.Revoked:
		return false, protocol.StartFailureLoginRejected
	case t.Accepted && exitOK:
		return true, ""
	case t.APIError:
		for _, f := range preflightQuotaFragments {
			if strings.Contains(t.ErrorText, f) {
				return false, protocol.StartFailureQuotaRefused
			}
		}
		for _, f := range preflightModelFragments {
			if strings.Contains(t.ErrorText, f) {
				return false, protocol.StartFailureModelRefused
			}
		}
	}
	return false, protocol.StartFailureNoAnswer
}

// readPreflight reads start-preflight.json for the summoned launch first.
// false when it is missing, unreadable or of another launch.
func readPreflight(s Settings, first Launch) (startPreflight, bool) {
	var rec startPreflight
	if readJSONFile(s.statePath(startPreflightFile), &rec) != nil || rec.ConversationID != first.ConversationID || rec.BootID != first.BootID {
		return startPreflight{}, false
	}
	return rec, true
}

// preflightLive reports whether the run-agent of first is still in its
// pre-flight: the record says checking, the run-agent it names is the same
// live process, and no provider of the task has a pid file yet.
func preflightLive(s Settings, first Launch) bool {
	rec, ok := readPreflight(s, first)
	if !ok || rec.State != preflightChecking || !pidAlive(agentPid{Pid: rec.Pid, Start: rec.Start}) {
		return false
	}
	_, err := os.Lstat(s.statePath(pidFile))
	return os.IsNotExist(err)
}

// preflightFailed reports whether first's pre-flight recorded that no model
// was usable, so nothing started.
func preflightFailed(s Settings, first Launch) bool {
	rec, ok := readPreflight(s, first)
	return ok && rec.State == preflightNoUsableModel && protocol.InSet(protocol.StartFailures, rec.Failure)
}

// preflightStartRecord is the status's start record from first's pre-flight
// record: the model the task started on, or why nothing started. nil while
// it runs, and for any other launch.
func preflightStartRecord(s Settings, first Launch) *protocol.StartRecord {
	rec, ok := readPreflight(s, first)
	if !ok {
		return nil
	}
	switch {
	case rec.State == preflightStarted && rec.Model != "":
		out := &protocol.StartRecord{Model: rec.Model}
		if rec.FallbackCause != protocol.StartFailureLoginRejected && protocol.InSet(protocol.StartFailures, rec.FallbackCause) {
			out.FallbackCause = rec.FallbackCause
		}
		return out
	case rec.State == preflightNoUsableModel && protocol.InSet(protocol.StartFailures, rec.Failure):
		return &protocol.StartRecord{Failure: rec.Failure}
	}
	return nil
}

// statePathIn is a state file's path in run-agent's state directory.
func statePathIn(stateDir, name string) string { return filepath.Join(stateDir, name) }

// tailBuffer keeps the last max bytes written to it, so a turn's output never
// grows run-agent without bound and its final events, which classifyPreflight
// reads, are kept. A line cut at the front is skipped as unparsable.
type tailBuffer struct {
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append(b.buf[:0:0], b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte { return b.buf }
