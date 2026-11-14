package agentd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/utils/clock"

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
	GroupOwned     bool      `json:"groupOwned,omitempty"`
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
	// Refused is the final spawn fence's refusal, never a model failure.
	Refused string
	// DeadlineCut says preparation shortened the turn to the absolute deadline.
	DeadlineCut bool
	// Gone: the owned leader was waited for and the recorded group was
	// observed to contain only the verified launcher. Otherwise nothing starts.
	Gone bool
}

// preflightTurn runs one pre-flight turn of l on model, stopped at limit. It
// is runPreflightTurn; tests replace it with a provider stand-in.
var preflightTurn = runPreflightTurn

// Test seams at the prepared-command boundary and the bounded exit proof.
var beforePreflightStart func(string)
var preflightGroupAbsent = waitNativeGroupAbsent
var preflightNewTimer = clock.RealClock{}.NewTimer

// preflightArgv is a pre-flight turn's command: one headless turn on model
// with no tools (--tools ""), no user, project or local settings and so none
// of their hooks (--setting-sources "", ruling 4 of story 791 fix round 1),
// no MCP servers, no saved session and one turn at most, its output as
// stream-json for classifyPreflight. The prompt goes to stdin. No
// --dangerously-skip-permissions: with no tools there is nothing to permit.
// No --bare: it reads no OAuth token. The login still comes from the
// environment, which no settings source carries: the answer in
// testdata/preflight was captured with these flags.
func preflightArgv(bin, model string) []string {
	return []string{bin, "--model", model,
		"--tools", "", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--max-turns", "1",
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
	var rec startPreflight
	if readJSONFile(statePathIn(stateDir, startPreflightFile), &rec) != nil || rec.Pid != os.Getpid() || rec.Start != procStartTime(os.Getpid()) {
		note("the pre-flight invocation claim is unconfirmed; the provider was not started")
		return "", "", false
	}
	l.preflightStateDir = stateDir
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
		case res.Refused != "":
			note("%s; the provider was not started", res.Refused)
			return "", "", false
		case res.TimedOut && (cut || res.DeadlineCut), startDeadlineRefusal(l.StartDeadline, false, runAgentNow()) != "":
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

// runPreflightTurn inherits the launcher's recorded group. It waits for its
// own child, then proves no member except that verified launcher remains.
// Stop uses the same group without that exception. Neither signal delivery
// nor the leader's Wait alone proves Gone (OP791 round 2 coordinator ruling).
func runPreflightTurn(l Launch, model string, limit time.Duration, signals <-chan os.Signal) preflightOutcome {
	runtime.LockOSThread() // Linux parent-death signalling is tied to this thread.
	defer runtime.UnlockOSThread()
	if err := leadPreflightGroup(); err != nil {
		return preflightOutcome{Failure: protocol.StartFailureNoAnswer}
	}
	self := agentPid{Pid: os.Getpid(), Start: procStartTime(os.Getpid())}
	argv := preflightArgv(l.Argv[0], model)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = agentEnv(os.Environ(), l)
	cmd.Stdin = strings.NewReader(preflightPrompt)
	stdout := &tailBuffer{max: preflightOutputMax}
	cmd.Stdout, cmd.Stderr = stdout, io.Discard
	cmd.WaitDelay = drainWait
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if limit <= 0 {
		return preflightOutcome{Failure: protocol.StartFailureNoAnswer, TimedOut: true, Gone: true}
	}
	if beforePreflightStart != nil {
		beforePreflightStart(model)
	}
	unlock, err := preflightSpawnLock(l, l.preflightStateDir)
	if err != nil {
		return preflightOutcome{Stopped: true, Gone: true}
	}
	// Command preparation and lock acquisition can consume the window.
	// Check again at the actual start and arm the timer from what is left.
	if why := checkSpawnFence(l, l.preflightStateDir); why != "" {
		unlock()
		return preflightOutcome{Refused: why, Gone: true}
	}
	remaining, cut := preflightLimit(l, runAgentNow())
	limit = min(limit, remaining)
	if limit <= 0 {
		unlock()
		return preflightOutcome{TimedOut: true, DeadlineCut: cut, Gone: true}
	}
	select {
	case <-signals:
		unlock()
		return preflightOutcome{Stopped: true, Gone: true}
	default:
	}
	timer := preflightNewTimer(limit)
	defer timer.Stop()
	if why := checkSpawnFence(l, l.preflightStateDir); why != "" {
		unlock()
		return preflightOutcome{Refused: why, Gone: true}
	}
	if err := cmd.Start(); err != nil {
		unlock()
		// Nothing ran: the CLI could not be started.
		return preflightOutcome{Failure: protocol.StartFailureNoAnswer, Gone: true}
	}
	unlock()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	out := preflightOutcome{DeadlineCut: cut}
	var werr error
	exited := false
	select {
	case werr = <-waitDone:
		exited = true
	case <-timer.C():
		out.TimedOut = true
	case <-signals:
		out.Stopped = true
	}
	if !exited {
		_ = killPreflightMembers(self)
		select {
		case werr = <-waitDone:
			exited = true
		case <-time.After(drainWait + time.Second):
		}
	}
	cleanup := killPreflightMembers(self)
	out.Gone = exited && cleanup == nil && preflightGroupAbsent(self.Pid, time.Second, self)
	if !out.Gone || out.TimedOut || out.Stopped {
		out.Failure = protocol.StartFailureNoAnswer
		return out
	}
	out.Answered, out.Failure = classifyPreflight(stdout.Bytes(), exitCode(cmd, werr) == 0, model)
	return out
}

// How a pre-flight turn's stream is read (story 791 fix round 1, coordinator
// rulings 1 and 2 in PR 1120 comment 6843361182; not rulings by Tom). Claude
// Code 2.1.321 really writes these (testdata/preflight): the stream's own
// field names, not the transcript's, so the session transcript's classifier
// (classifyLastAssistant) is not used here and is unchanged.
//
// An entry is an API error when it carries the flag (is_api_error_message,
// or the transcript's isApiErrorMessage), an error code, an error status
// (api_error_status or apiErrorStatus), or, without a real response, text
// that starts "API Error". Its cause is read from the error code first, then
// the status, and only then from the CLI's wording. A reply that quotes those
// words is not an API error, so it is still an answer.

// The CLI's error codes (the entry's top-level "error") and statuses that
// name a cause. authentication_failed (a missing login and a rejected token)
// and model_not_found with 404 are from real streams; rate_limit,
// billing_error and 429 are the CLI's own strings, NOT VERIFIED AGAINST A
// REAL STREAM (no quota refusal could be captured).
var (
	preflightLoginCodes = []string{"authentication_failed"}
	preflightModelCodes = []string{"model_not_found"}
	preflightQuotaCodes = []string{"rate_limit", "billing_error"}
)

// The wordings, lower-case, read only when the code and the status name no
// cause. A rejected login: the transcript's markers (authErrorMarkers) and
// the two real streams' "not logged in" and "failed to authenticate". A
// refused model: v1's fragments (session-launch.sh line 121) and the real
// stream's wording. A quota refusal: v1's fragments, not verified.
var (
	preflightLoginFragments = []string{"not logged in", "failed to authenticate"}
	preflightModelFragments = []string{"unknown model", "invalid model", "not available", "/model to switch",
		"issue with the selected model", "may not exist or you may not have access"}
	preflightQuotaFragments = []string{"out of usage credits", "usage limit", "rate limit"}
)

// preflightEntry is one line of a pre-flight turn's stream-json output. Each
// field is kept raw, so one of an unexpected type never fails the line.
type preflightEntry struct {
	Type        string          `json:"type"`
	Flag        json.RawMessage `json:"is_api_error_message"`
	FlagCamel   json.RawMessage `json:"isApiErrorMessage"`
	Status      json.RawMessage `json:"api_error_status"`
	StatusCamel json.RawMessage `json:"apiErrorStatus"`
	Error       json.RawMessage `json:"error"`
	Message     json.RawMessage `json:"message"`
}

// classifyPreflight reads the last assistant entry of a pre-flight turn's
// stream for asked, the model the turn was run on. Answered: a real response
// (a model name other than "<synthetic>" and non-zero usage, as
// acceptedResponse reads it) from asked (answeredBy), from a CLI that exited
// 0. A real response from another model is a refused model: the CLI can fall
// back to another model by itself (ruling 2). An API error is a rejected
// login, a refused model or a quota refusal by its code, its status or its
// wording, else no answer; so is anything else (op791-a-1).
func classifyPreflight(stdout []byte, exitOK bool, asked string) (answered bool, failure string) {
	lines := bytes.Split(stdout, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var e preflightEntry
		if json.Unmarshal(bytes.TrimSpace(lines[i]), &e) != nil || e.Type != "assistant" {
			continue
		}
		text := strings.ToLower(entryText(e.Message))
		response := acceptedResponse(e.Message)
		code := rawJSONString(e.Error)
		status := rawJSONStatus(e.Status)
		if status == 0 {
			status = rawJSONStatus(e.StatusCamel)
		}
		apiError := rawJSONTrue(e.Flag) || rawJSONTrue(e.FlagCamel) || code != "" || status != 0 ||
			(!response && strings.HasPrefix(strings.TrimSpace(text), "api error"))
		switch {
		case apiError:
			return false, preflightRefusal(code, status, text)
		case !response:
			return false, protocol.StartFailureNoAnswer
		case !answeredBy(asked, responseModel(e.Message)):
			return false, protocol.StartFailureModelRefused
		case !exitOK:
			return false, protocol.StartFailureNoAnswer
		}
		return true, ""
	}
	return false, protocol.StartFailureNoAnswer
}

// preflightRefusal is an API-error entry's cause: by its code, then its
// status, then its wording; no answer when none names one.
func preflightRefusal(code string, status int, text string) string {
	switch {
	case slices.Contains(preflightLoginCodes, code):
		return protocol.StartFailureLoginRejected
	case slices.Contains(preflightModelCodes, code):
		return protocol.StartFailureModelRefused
	case slices.Contains(preflightQuotaCodes, code):
		return protocol.StartFailureQuotaRefused
	case status == 401:
		return protocol.StartFailureLoginRejected
	case status == 404:
		return protocol.StartFailureModelRefused
	case status == 429:
		return protocol.StartFailureQuotaRefused
	case containsFragment(text, authErrorMarkers) || containsFragment(text, preflightLoginFragments):
		return protocol.StartFailureLoginRejected
	case containsFragment(text, preflightQuotaFragments):
		return protocol.StartFailureQuotaRefused
	case containsFragment(text, preflightModelFragments):
		return protocol.StartFailureModelRefused
	}
	return protocol.StartFailureNoAnswer
}

// answeredBy reports whether got, the model an answer names, is asked: the
// same id, or asked with a date suffix (-YYYYMMDD), either with or without
// "[1m]". The real answer names exactly the asked id (testdata/preflight).
func answeredBy(asked, got string) bool {
	a, g := strings.TrimSuffix(asked, "[1m]"), strings.TrimSuffix(got, "[1m]")
	if a == "" || g == "" {
		return false
	}
	if g == a {
		return true
	}
	date, ok := strings.CutPrefix(g, a+"-")
	return ok && len(date) == 8 && strings.Trim(date, "0123456789") == ""
}

// responseModel is the model a message names.
func responseModel(msg json.RawMessage) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(msg, &m)
	return m.Model
}

func containsFragment(text string, fragments []string) bool {
	return slices.ContainsFunc(fragments, func(f string) bool { return strings.Contains(text, f) })
}

// rawJSONTrue: the value is JSON true.
func rawJSONTrue(r json.RawMessage) bool { return string(bytes.TrimSpace(r)) == "true" }

// rawJSONString is a JSON string's value; "" for anything else.
func rawJSONString(r json.RawMessage) string {
	var s string
	_ = json.Unmarshal(r, &s)
	return s
}

// rawJSONStatus is an HTTP status given as a JSON number or string; 0 for
// anything else.
func rawJSONStatus(r json.RawMessage) int {
	var n int
	if json.Unmarshal(r, &n) == nil {
		return n
	}
	if n, err := strconv.Atoi(rawJSONString(r)); err == nil {
		return n
	}
	return 0
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

// preflightLive keeps a recorded group visible after its launcher dies.
// Terminal evidence cannot hide a probe descendant whose absence is uncertain.
// Older checking records retain their original PID-only interpretation.
func preflightLive(s Settings, first Launch) bool {
	rec, ok := readPreflight(s, first)
	if !ok || rec.Pid <= 0 || rec.Start == "" || rec.State == preflightStarted {
		return false
	}
	p := agentPid{Pid: rec.Pid, Start: rec.Start}
	if rec.GroupOwned {
		var refused startRefused
		terminal := rec.State == preflightNoUsableModel || (readJSONFile(s.statePath(startRefusedFile), &refused) == nil && refused.ConversationID == first.ConversationID && refused.BootID == first.BootID)
		if terminal && preflightGroupOnlyLauncher(rec.Pid, p) {
			return false
		}
		return pidAlive(p) || !waitNativeGroupAbsent(rec.Pid, 0)
	}
	if rec.State != preflightChecking || !pidAlive(p) {
		return false
	}
	_, err := os.Lstat(s.statePath(pidFile))
	return os.IsNotExist(err)
}

// preflightFailed reports whether first's pre-flight recorded that no model
// was usable, so nothing started.
func preflightFailed(s Settings, first Launch) bool {
	rec, ok := readPreflight(s, first)
	return ok && rec.State == preflightNoUsableModel && protocol.InSet(protocol.StartFailures, rec.Failure) && !preflightLive(s, first)
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
	case rec.State == preflightNoUsableModel && protocol.InSet(protocol.StartFailures, rec.Failure) && !preflightLive(s, first):
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
