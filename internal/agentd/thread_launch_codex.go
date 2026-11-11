package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

// The Codex adapter of `agent-run thread start` (DESIGN-057 section 5) and the
// read-only pass of `agent-run thread status` (section 11), which shares its
// host checks and first-response read and launches nothing. The adapter
// runs in the host, reached by `agentd ctl thread-start` over the exec route
// `agent-run pair` uses. It creates the thread only through the packaged
// scripts/codex-clean-session.py (the native `codex --remote` TUI, the
// phone-visible `vscode` source), never `codex exec`, `codex queue` or
// `thread/start`, and passes no sandbox or approval flag. Before that client
// starts, thread start writes folder trust for the one platform repository
// (DESIGN-057 section 12). thread status does not. Everything else is
// read-only observation over the peer-verified control socket. It never pairs,
// logs in, resumes, forks or retries a launch.

// CodexThreadWrapper is where the agent image installs the clean-session wrapper.
const CodexThreadWrapper = "/usr/local/lib/dev-env-codex/codex-clean-session.py"

const (
	codexThreadMaxHandoff = 4096 // the wrapper's MAX_TASK
	codexThreadListLimit  = 20
	codexThreadFrameBound = 1 << 20
	// codexThreadObserveErrors consecutive observation failures end a run.
	codexThreadObserveErrors = 3
)

var (
	codexThreadName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	codexThreadSource = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}$`)
	// codexThreadEfforts is the wrapper's --effort set.
	codexThreadEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	// codexAllSourceKinds is every ThreadSourceKind in the pinned 0.160.1 protocol.
	codexAllSourceKinds = []string{"cli", "vscode", "exec", "appServer", "subAgent", "subAgentReview", "subAgentCompact", "subAgentThreadSpawn", "subAgentOther", "unknown"}
)

// CodexThreadRequest is one launch. Handoff is the new thread's first message.
type CodexThreadRequest struct {
	Model, Effort, Repo, Name, Handoff string
	Wait                               time.Duration
}

// codexThreadSummary is the part of a native Thread this adapter reads.
type codexThreadSummary struct {
	ID, Cwd, Source, Preview string
	CreatedAt                int64
}

// codexTurnObservation is the first turn: its id and status, and whether its
// newest items hold an agent message with text. It never carries the text.
type codexTurnObservation struct {
	TurnID, Status string
	AgentMessage   bool
}

// codexWrapperCall is one invocation of the packaged wrapper.
type codexWrapperCall struct {
	Argv, Env []string
	Stdin     []byte
}

type codexThreadDeps struct {
	access  func(Settings, time.Time) error
	daemon  func(Settings) (codexHostProcess, error)
	remote  func(context.Context, Settings, int) (string, error)
	paired  func(Settings) bool
	list    func(ctx context.Context, s Settings, pid int, cwd string, all bool) ([]codexThreadSummary, error)
	turn    func(ctx context.Context, s Settings, pid int, threadID string) (codexTurnObservation, error)
	launch  func(context.Context, codexWrapperCall) ([]byte, error)
	python  func() (string, error)
	wrapper func() (string, error)
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error
}

func realCodexThreadDeps() codexThreadDeps {
	return codexThreadDeps{
		access: codexHostAccessReady,
		daemon: func(s Settings) (codexHostProcess, error) { return codexRemoteDepsFor().daemonState(s) },
		remote: func(ctx context.Context, s Settings, pid int) (string, error) {
			r, err := probeCodexHostRemote(ctx, s, false, pid)
			return r.Connection, err
		},
		paired: func(s Settings) bool {
			return readJSONFile(s.statePath(codexPairIssuedFile), &codexPairIssued{}) == nil
		},
		list:   listCodexThreads,
		turn:   readCodexFirstTurn,
		launch: runCodexThreadWrapper,
		python: func() (string, error) { return exec.LookPath("python3") },
		wrapper: func() (string, error) {
			info, err := os.Stat(CodexThreadWrapper)
			if err != nil || !info.Mode().IsRegular() {
				return "", errCodexHostUnknown
			}
			return CodexThreadWrapper, nil
		},
		now: time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// codexThreadDepsFor builds the dependencies; tests replace it.
var codexThreadDepsFor = realCodexThreadDeps

// StartCodexThread runs one launch on this host and returns its result.
func StartCodexThread(ctx context.Context, s Settings, sess protocol.Session, req CodexThreadRequest) apiv1.ThreadLaunchResult {
	return startCodexThread(ctx, s, sess, req, codexThreadDepsFor())
}

func startCodexThread(ctx context.Context, s Settings, sess protocol.Session, req CodexThreadRequest, d codexThreadDeps) apiv1.ThreadLaunchResult {
	rec := threadlaunch.NewRecorder("codex", d.now)
	a := &codexThreadAdapter{s: s, sess: sess, req: req, d: d}
	wait := req.Wait
	if wait <= 0 || wait > 10*time.Minute {
		return rec.Finish(apiv1.ThreadFailed, "invalid_input", apiv1.ThreadPhaseHostReady, 0)
	}
	return threadlaunch.Run(ctx, a, rec, threadlaunch.Options{Wait: wait, Poll: 2 * time.Second, Sleep: d.sleep})
}

// CodexThreadStatusRequest names one existing thread on this host. Repo is the
// repository the thread was started in; empty is the host's own.
type CodexThreadStatusRequest struct {
	ThreadID, Repo string
}

// ObserveCodexThread is `agent-run thread status` on this host (DESIGN-057
// section 11): one read-only pass over an existing thread, reported as a
// thread-launch.v1 result. It never runs the wrapper, starts a client or sends
// a thread input.
func ObserveCodexThread(ctx context.Context, s Settings, sess protocol.Session, req CodexThreadStatusRequest) apiv1.ThreadLaunchResult {
	return observeCodexThread(ctx, s, sess, req, codexThreadDepsFor())
}

// codexThreadStatusBound ends a status pass whose reads hang.
const codexThreadStatusBound = time.Minute

func observeCodexThread(ctx context.Context, s Settings, sess protocol.Session, req CodexThreadStatusRequest, d codexThreadDeps) apiv1.ThreadLaunchResult {
	ctx, cancel := context.WithTimeout(ctx, codexThreadStatusBound)
	defer cancel()
	d = d.observeOnly()
	rec := threadlaunch.NewStatusRecorder("codex", d.now)
	a := &codexThreadAdapter{s: s, sess: sess, req: CodexThreadRequest{Repo: req.Repo}, d: d, thread: req.ThreadID, once: true}
	return threadlaunch.Observe(ctx, codexThreadStatus{a}, rec)
}

// observeOnly drops the launch half, so a status pass holds no wrapper,
// interpreter or poll clock: only the host reads and the read-only observers.
// A later change that reached for one on this path would stop, not launch.
func (d codexThreadDeps) observeOnly() codexThreadDeps {
	d.launch, d.python, d.wrapper, d.sleep = nil, nil, nil, nil
	return d
}

// codexThreadStatus is the adapter's read-only half: it exposes Locate, Visible
// and FirstResponse, and neither Preflight nor Create.
type codexThreadStatus struct{ a *codexThreadAdapter }

// Visible finds the thread by id among the newest threads for its cwd. The
// default list (the phone's) is read first: a thread there is created and
// visible in one read, and sub-agent or exec threads cannot crowd it out of
// the newest 20. Only when it is absent does the all-sources list tell a
// hidden thread from an id that is not there.
func (o codexThreadStatus) Visible(ctx context.Context, r *threadlaunch.Recorder) (bool, error) {
	a := o.a
	find := func(all bool) (codexThreadSummary, bool, error) {
		list, err := a.d.list(ctx, a.s, a.pid, a.cwd, all)
		if err != nil {
			return codexThreadSummary{}, false, threadlaunch.Halt(apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseThreadCreated)
		}
		i := slices.IndexFunc(list, func(t codexThreadSummary) bool { return t.ID == a.thread && t.Cwd == a.cwd })
		if i < 0 {
			return codexThreadSummary{}, false, nil
		}
		return list[i], true, nil
	}
	t, ok, err := find(false)
	if err != nil {
		return false, err
	}
	if ok {
		r.Result.ThreadID, r.Result.Source = a.thread, t.Source
		r.Phase(apiv1.ThreadPhaseThreadCreated, "ok", "thread_list_default", "thread_listed_for_cwd")
		r.Phase(apiv1.ThreadPhaseSourceVisible, "ok", "thread_list_default", t.Source)
		return true, nil
	}
	if t, ok, err = find(true); err != nil {
		return false, err
	}
	if !ok {
		r.Phase(apiv1.ThreadPhaseThreadCreated, "error", "thread_list", "thread_not_listed")
		return false, threadlaunch.Halt(apiv1.ThreadFailed, "thread_not_found", apiv1.ThreadPhaseThreadCreated)
	}
	r.Result.ThreadID, r.Result.Source = a.thread, t.Source
	r.Phase(apiv1.ThreadPhaseThreadCreated, "ok", "thread_list", "thread_listed_for_cwd")
	r.Phase(apiv1.ThreadPhaseSourceVisible, "error", "thread_list_default", "hidden_source")
	return false, threadlaunch.Halt(apiv1.ThreadHidden, "hidden_source", apiv1.ThreadPhaseSourceVisible)
}

func (o codexThreadStatus) FirstResponse(ctx context.Context, r *threadlaunch.Recorder) (bool, error) {
	return o.a.FirstResponse(ctx, r)
}

// Locate checks the host as a launch's preflight does and resolves the cwd the
// thread lists are read for. The pairing marker is reported, never required:
// the thread already exists.
func (o codexThreadStatus) Locate(ctx context.Context, r *threadlaunch.Recorder) error {
	a := o.a
	r.Result.Host, r.Result.SessionName = a.sess.Name, a.sess.Name
	if !codexRemoteSession(a.sess) {
		r.Phase(apiv1.ThreadPhaseHostReady, "error", "host_session", "not_codex_host")
		return threadlaunch.Halt(apiv1.ThreadFailed, "not_codex_host", apiv1.ThreadPhaseHostReady)
	}
	if !nativeThreadID.MatchString(a.thread) || (a.req.Repo != "" && !apiv1.ValidRepoName(a.req.Repo)) {
		return threadlaunch.Halt(apiv1.ThreadFailed, "invalid_input", apiv1.ThreadPhaseHostReady)
	}
	if err := a.hostReady(ctx, r); err != nil {
		return err
	}
	if a.d.paired(a.s) {
		r.Phase(apiv1.ThreadPhasePaired, "unknown", "host_state", "pair_code_issued")
	} else {
		r.Phase(apiv1.ThreadPhasePaired, "required", "host_state", "no_pair_code_issued")
	}
	return a.resolveCwd(r)
}

type codexThreadAdapter struct {
	s        Settings
	sess     protocol.Session
	req      CodexThreadRequest
	d        codexThreadDeps
	pid      int
	cwd      string
	before   map[string]bool
	launched time.Time
	thread   string
	failures int
	// once marks a status pass: one observation, no launch and no poll.
	once bool
}

func validCodexThreadRequest(req CodexThreadRequest) bool {
	h := req.Handoff
	return protocol.ValidateCodexEffort(req.Model, req.Effort) == nil && slices.Contains(codexThreadEfforts, req.Effort) &&
		codexThreadName.MatchString(req.Name) && (req.Repo == "" || apiv1.ValidRepoName(req.Repo)) &&
		len(h) > 0 && len(h) <= codexThreadMaxHandoff && utf8.ValidString(h) && strings.TrimSpace(h) != "" && !strings.ContainsRune(h, 0)
}

func (a *codexThreadAdapter) Preflight(ctx context.Context, r *threadlaunch.Recorder) error {
	r.Result.Host, r.Result.SessionName = a.sess.Name, a.sess.Name
	if !codexRemoteSession(a.sess) {
		r.Phase(apiv1.ThreadPhaseHostReady, "error", "host_session", "not_codex_host")
		return threadlaunch.Halt(apiv1.ThreadFailed, "not_codex_host", apiv1.ThreadPhaseHostReady)
	}
	if !validCodexThreadRequest(a.req) {
		return threadlaunch.Halt(apiv1.ThreadFailed, "invalid_input", apiv1.ThreadPhaseHostReady)
	}
	r.Result.Name, r.Result.RequestedModel = a.req.Name, a.req.Model
	if err := a.hostReady(ctx, r); err != nil {
		return err
	}
	// Codex has no read for the phone's side of a pairing. A host that never
	// issued a code cannot be paired; one that did may be.
	if !a.d.paired(a.s) {
		r.Phase(apiv1.ThreadPhasePaired, "required", "host_state", "no_pair_code_issued")
		return threadlaunch.Halt(apiv1.ThreadActionRequired, "pairing_required", apiv1.ThreadPhasePaired)
	}
	r.Phase(apiv1.ThreadPhasePaired, "unknown", "host_state", "pair_code_issued")
	if err := a.resolveCwd(r); err != nil {
		return err
	}
	// Trust the platform clone before the client starts, so Codex does not
	// stop on its folder prompt. A status pass does not call Preflight.
	if err := ensureCodexRepoTrust(ctx, a.s, a.cwd); err != nil {
		code := "codex_config_invalid"
		if errors.Is(err, errFolderNotQualified) {
			code = "folder_not_qualified"
		}
		return threadlaunch.Halt(apiv1.ThreadFailed, code, apiv1.ThreadPhaseHostReady)
	}
	before, err := a.d.list(ctx, a.s, a.pid, a.cwd, true)
	if err != nil {
		return threadlaunch.Halt(apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseHostReady)
	}
	a.before = make(map[string]bool, len(before))
	for _, t := range before {
		a.before[t.ID] = true
	}
	return nil
}

// hostReady records host_ready: the keeper's access projection is current, the
// daemon is live and its Remote Control is connected. It only reads.
func (a *codexThreadAdapter) hostReady(ctx context.Context, r *threadlaunch.Recorder) error {
	if a.d.access(a.s, a.d.now()) != nil {
		r.Phase(apiv1.ThreadPhaseHostReady, "blocked", "host_access", "login_required")
		return threadlaunch.Halt(apiv1.ThreadActionRequired, "login_required", apiv1.ThreadPhaseHostReady)
	}
	p, err := a.d.daemon(a.s)
	if err != nil || p.State != "Live" || p.PID <= 0 {
		r.Phase(apiv1.ThreadPhaseHostReady, "error", "host_daemon", "daemon_not_live")
		return threadlaunch.Halt(apiv1.ThreadFailed, "daemon_unavailable", apiv1.ThreadPhaseHostReady)
	}
	a.pid = p.PID
	conn, err := a.d.remote(ctx, a.s, a.pid)
	if err != nil {
		r.Phase(apiv1.ThreadPhaseHostReady, "unknown", "host_daemon", "remote_status_unavailable")
		return threadlaunch.Halt(apiv1.ThreadFailed, "observation_unavailable", apiv1.ThreadPhaseHostReady)
	}
	if conn != "connected" {
		r.Phase(apiv1.ThreadPhaseHostReady, "error", "host_daemon", "remote_not_connected")
		return threadlaunch.Halt(apiv1.ThreadFailed, "remote_disconnected", apiv1.ThreadPhaseHostReady)
	}
	r.Phase(apiv1.ThreadPhaseHostReady, "ok", "host_daemon", "remote_connected")
	return nil
}

// resolveCwd sets the thread's cwd, ~/repos/<repo> on the host: the request's
// repo, else the host's own.
func (a *codexThreadAdapter) resolveCwd(r *threadlaunch.Recorder) error {
	repo := a.req.Repo
	if repo == "" {
		repo = a.sess.Repo
	}
	if !apiv1.ValidRepoName(repo) {
		return threadlaunch.Halt(apiv1.ThreadFailed, "repo_unavailable", apiv1.ThreadPhaseHostReady)
	}
	a.cwd = a.s.ClonePath(repo)
	info, err := os.Stat(a.cwd)
	if err != nil || !info.IsDir() {
		return threadlaunch.Halt(apiv1.ThreadFailed, "repo_unavailable", apiv1.ThreadPhaseHostReady)
	}
	// A symlink, including one that leaves ~/repos, is not a folder this launch
	// may trust. A missing directory stays repo_unavailable.
	if noSymlinkComponents(a.cwd) != nil {
		return threadlaunch.Halt(apiv1.ThreadFailed, "folder_not_qualified", apiv1.ThreadPhaseHostReady)
	}
	r.Result.Cwd = a.cwd
	return nil
}

// wrapperState is the wrapper's one-line JSON result.
type wrapperState struct {
	State string `json:"state"`
}

func (a *codexThreadAdapter) Create(ctx context.Context, r *threadlaunch.Recorder) error {
	python, err := a.d.python()
	wrapper, werr := a.d.wrapper()
	if err != nil || werr != nil {
		r.Phase(apiv1.ThreadPhaseClientStarted, "error", "wrapper", "wrapper_unavailable")
		return threadlaunch.Halt(apiv1.ThreadFailed, "client_unavailable", apiv1.ThreadPhaseClientStarted)
	}
	socket := filepath.Join(a.s.CodexHome, "app-server-control", "app-server-control.sock")
	call := codexWrapperCall{
		Argv: []string{python, "-I", "-B", wrapper, "--repo", a.cwd, "--socket", socket, "--model", a.req.Model,
			"--effort", a.req.Effort, "--name", a.req.Name, "--client", a.s.CodexBin, "--task-file", "-", "--observe-seconds", "2"},
		Env:   codexThreadEnv(a.s),
		Stdin: []byte(a.req.Handoff),
	}
	// A thread created from here on is newer than this, to the second.
	a.launched = a.d.now()
	out, err := a.d.launch(ctx, call)
	var st wrapperState
	if err != nil || json.Unmarshal(bytes.TrimSpace(out), &st) != nil {
		r.Phase(apiv1.ThreadPhaseClientStarted, "unknown", "wrapper", "launch_unconfirmed")
		return threadlaunch.Halt(apiv1.ThreadUnconfirmed, "launch_unconfirmed", apiv1.ThreadPhaseClientStarted)
	}
	switch st.State {
	case "client-running-unverified":
		r.Phase(apiv1.ThreadPhaseClientStarted, "ok", "wrapper", "client_running")
		return nil
	case "invalid-input":
		r.Phase(apiv1.ThreadPhaseClientStarted, "error", "wrapper", "invalid_input")
		return threadlaunch.Halt(apiv1.ThreadFailed, "invalid_input", apiv1.ThreadPhaseClientStarted)
	case "name-consumed":
		r.Phase(apiv1.ThreadPhaseClientStarted, "error", "wrapper", "name_consumed")
		return threadlaunch.Halt(apiv1.ThreadFailed, "name_consumed", apiv1.ThreadPhaseClientStarted)
	case "prerequisite-unavailable", "unsupported-client":
		r.Phase(apiv1.ThreadPhaseClientStarted, "error", "wrapper", "client_unavailable")
		return threadlaunch.Halt(apiv1.ThreadFailed, "client_unavailable", apiv1.ThreadPhaseClientStarted)
	case "client-not-running":
		r.Phase(apiv1.ThreadPhaseClientStarted, "error", "wrapper", "client_not_running")
		return threadlaunch.Halt(apiv1.ThreadUnconfirmed, "client_not_running", apiv1.ThreadPhaseClientStarted)
	}
	r.Phase(apiv1.ThreadPhaseClientStarted, "unknown", "wrapper", "launch_unconfirmed")
	return threadlaunch.Halt(apiv1.ThreadUnconfirmed, "launch_unconfirmed", apiv1.ThreadPhaseClientStarted)
}

// observed counts a failed observation; only consecutive failures end the run.
// A status pass (once) never polls, so its first failure ends it.
func (a *codexThreadAdapter) observed(err error) error {
	if err == nil {
		a.failures = 0
		return nil
	}
	a.failures++
	if a.once || a.failures >= codexThreadObserveErrors {
		return err
	}
	return errRetryObservation
}

var errRetryObservation = errors.New("observation failed; polling again")

func (a *codexThreadAdapter) Visible(ctx context.Context, r *threadlaunch.Recorder) (bool, error) {
	if a.thread == "" {
		all, err := a.d.list(ctx, a.s, a.pid, a.cwd, true)
		if err := a.observed(err); err != nil {
			return false, retryable(err)
		}
		id, err := a.bind(all)
		if err != nil || id == "" {
			return false, err
		}
		a.thread = id
		r.Result.ThreadID = id
		r.Phase(apiv1.ThreadPhaseThreadCreated, "ok", "thread_list", "new_thread_for_cwd")
	}
	shown, err := a.d.list(ctx, a.s, a.pid, a.cwd, false)
	if err := a.observed(err); err != nil {
		return false, retryable(err)
	}
	for _, t := range shown {
		if t.ID == a.thread {
			r.Result.Source = t.Source
			r.Phase(apiv1.ThreadPhaseSourceVisible, "ok", "thread_list_default", t.Source)
			return true, nil
		}
	}
	r.Phase(apiv1.ThreadPhaseSourceVisible, "error", "thread_list_default", "hidden_source")
	return false, threadlaunch.Halt(apiv1.ThreadHidden, "hidden_source", apiv1.ThreadPhaseSourceVisible)
}

// retryable turns a tolerated observation failure into another poll.
func retryable(err error) error {
	if errors.Is(err, errRetryObservation) {
		return nil
	}
	return err
}

// bind picks the one new thread for this launch's cwd. A candidate is new,
// in the cwd, created at or after the launch and not a sub-agent's. A
// non-empty preview must match the handoff, so another thread started there
// meanwhile (from the phone, say) is never adopted. None is "poll again";
// several, after preferring preview matches, is ambiguous.
func (a *codexThreadAdapter) bind(all []codexThreadSummary) (string, error) {
	handoff := normalizedText(a.req.Handoff)
	var matched, unknown []codexThreadSummary
	for _, t := range all {
		if a.before[t.ID] || t.Cwd != a.cwd || t.CreatedAt < a.launched.Unix()-1 || strings.HasPrefix(t.Source, "subAgent") {
			continue
		}
		switch p := normalizedText(t.Preview); {
		case p == "":
			unknown = append(unknown, t)
		case strings.HasPrefix(handoff, p) || strings.HasPrefix(p, handoff):
			matched = append(matched, t)
		}
	}
	switch {
	case len(matched) == 1:
		return matched[0].ID, nil
	case len(matched) > 1 || len(unknown) > 1:
		return "", threadlaunch.Halt(apiv1.ThreadUnconfirmed, "thread_ambiguous", apiv1.ThreadPhaseThreadCreated)
	case len(unknown) == 1:
		return unknown[0].ID, nil
	}
	return "", nil
}

// normalizedText collapses whitespace, as a thread preview may.
func normalizedText(s string) string { return strings.Join(strings.Fields(s), " ") }

func (a *codexThreadAdapter) FirstResponse(ctx context.Context, r *threadlaunch.Recorder) (bool, error) {
	obs, err := a.d.turn(ctx, a.s, a.pid, a.thread)
	if err := a.observed(err); err != nil {
		return false, retryable(err)
	}
	if obs.TurnID == "" {
		return false, nil
	}
	r.Phase(apiv1.ThreadPhaseHandoff, "ok", "thread_turns", "first_turn_started")
	switch {
	case obs.AgentMessage:
		r.Phase(apiv1.ThreadPhaseFirstResponse, "ok", "thread_items", "agent_message")
		return true, nil
	case obs.Status == "failed" || obs.Status == "interrupted":
		r.Phase(apiv1.ThreadPhaseFirstResponse, "error", "thread_turns", "turn_"+obs.Status)
		return false, threadlaunch.Halt(apiv1.ThreadFailed, "turn_failed", apiv1.ThreadPhaseFirstResponse)
	case obs.Status == "completed":
		r.Unknown(apiv1.ThreadPhaseFirstResponse, "thread_items", "completed_without_observed_message")
		return false, threadlaunch.ErrNotObservable
	}
	return false, nil
}

// codexThreadEnv is the managed Codex launch's scrubbed environment for the
// wrapper. When the wrapper starts the host's tmux server (a Codex host runs
// no other tmux), the client inherits it; a tmux server that already runs
// keeps its own global environment, which the same route set before.
func codexThreadEnv(s Settings) []string {
	unset := append(append(append([]string{}, unsetForAgent...), codexUnset...), "CODEX_SQLITE_HOME", "CODEX_APP_SERVER_TRANSPORT", "CODEX_APP_SERVER_SOCKET", "CODEX_REMOTE_CONTROL_SOCKET", "CODEX_APP_SERVER_DAEMON_BACKEND", "XDG_STATE_HOME", "TMUX", "TMUX_PANE")
	return agentEnv(os.Environ(), Launch{Unset: unset, Env: []string{"HOME=" + s.Home, "CODEX_HOME=" + s.CodexHome, "XDG_STATE_HOME=" + filepath.Join(s.Home, ".local", "state")}})
}

// runCodexThreadWrapper runs the wrapper once with a bounded stdout. Its
// stderr is discarded: it may hold task text.
func runCodexThreadWrapper(ctx context.Context, c codexWrapperCall) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env, cmd.Stdin, cmd.Dir = c.Env, bytes.NewReader(c.Stdin), "/"
	out := &hostBoundedOutput{limit: 8 << 10}
	cmd.Stdout = out
	cmd.WaitDelay = 2 * time.Second
	// A non-zero exit still prints the state; the state decides.
	_ = cmd.Run()
	if out.exceeded {
		return nil, errCodexHostUnknown
	}
	return out.data, nil
}

// openCodexThreadObserver opens a fresh read-only connection with the
// version-checked handshake and the wider frame bound.
func openCodexThreadObserver(ctx context.Context, s Settings, pid int) (*codexHostRPC, error) {
	r, err := openCodexHostRPC(ctx, s, pid)
	if err != nil {
		return nil, err
	}
	r.widen(codexThreadFrameBound)
	var initialize struct {
		UserAgent string `json:"userAgent"`
	}
	if r.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "dev_env_thread_launch", "version": managedCodexVersion}, "capabilities": map[string]bool{"experimentalApi": true}}, &initialize) != nil {
		_ = r.conn.Close()
		return nil, errCodexHostUnknown
	}
	_, tail, ok := strings.Cut(initialize.UserAgent, "/")
	fields := strings.Fields(tail)
	if !ok || len(fields) == 0 || fields[0] != managedCodexVersion || r.conn.WriteJSON(map[string]string{"method": "initialized"}) != nil {
		_ = r.conn.Close()
		return nil, errCodexHostUnknown
	}
	return r, nil
}

// listCodexThreads reads the newest threads for cwd. all=false omits
// sourceKinds: the protocol's default interactive sources, the list the phone
// shows. all=true asks for every source kind, so a hidden thread is still seen.
func listCodexThreads(ctx context.Context, s Settings, pid int, cwd string, all bool) ([]codexThreadSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := openCodexThreadObserver(ctx, s, pid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.conn.Close() }()
	params := map[string]any{"cwd": cwd, "limit": codexThreadListLimit}
	if all {
		params["sourceKinds"] = codexAllSourceKinds
	}
	var response struct {
		Data *[]struct {
			ID        string          `json:"id"`
			Cwd       string          `json:"cwd"`
			Source    json.RawMessage `json:"source"`
			Preview   string          `json:"preview"`
			CreatedAt *int64          `json:"createdAt"`
		} `json:"data"`
	}
	if r.call("thread/list", params, &response) != nil || response.Data == nil || len(*response.Data) > codexThreadListLimit {
		return nil, errCodexHostUnknown
	}
	out := make([]codexThreadSummary, 0, len(*response.Data))
	for _, t := range *response.Data {
		if !nativeThreadID.MatchString(t.ID) || t.CreatedAt == nil || !filepath.IsAbs(t.Cwd) {
			return nil, errCodexHostUnknown
		}
		out = append(out, codexThreadSummary{ID: t.ID, Cwd: t.Cwd, Source: codexSourceName(t.Source), Preview: t.Preview, CreatedAt: *t.CreatedAt})
	}
	return out, nil
}

// codexSourceName reduces a SessionSource (an enum string, or {custom} or
// {subAgent}) to a short token; anything else is "unknown".
func codexSourceName(raw json.RawMessage) string {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		if codexThreadSource.MatchString(name) {
			return name
		}
		return "unknown"
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && len(object) == 1 {
		for key := range object {
			if key == "custom" || key == "subAgent" {
				return key
			}
		}
	}
	return "unknown"
}

// readCodexFirstTurn reads the thread's first turn and, once it exists, its
// newest items. Only the presence of a non-empty agent message is kept.
func readCodexFirstTurn(ctx context.Context, s Settings, pid int, threadID string) (codexTurnObservation, error) {
	if !nativeThreadID.MatchString(threadID) {
		return codexTurnObservation{}, errCodexHostUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := openCodexThreadObserver(ctx, s, pid)
	if err != nil {
		return codexTurnObservation{}, err
	}
	defer func() { _ = r.conn.Close() }()
	var turns struct {
		Data *[]struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if r.call("thread/turns/list", map[string]any{"threadId": threadID, "limit": 1, "sortDirection": "asc", "itemsView": "notLoaded"}, &turns) != nil || turns.Data == nil || len(*turns.Data) > 1 {
		return codexTurnObservation{}, errCodexHostUnknown
	}
	if len(*turns.Data) == 0 {
		return codexTurnObservation{}, nil
	}
	turn := (*turns.Data)[0]
	switch turn.Status {
	case "completed", "interrupted", "failed", "inProgress":
	default:
		return codexTurnObservation{}, errCodexHostUnknown
	}
	if !codexRCTurnID.MatchString(turn.ID) {
		return codexTurnObservation{}, errCodexHostUnknown
	}
	obs := codexTurnObservation{TurnID: turn.ID, Status: turn.Status}
	// Codex 0.160.1 returns ThreadItemEntry objects: {item, turnId, startedAtMs,
	// completedAtMs}. The ThreadItem (type, text) is under item. Reading type
	// and text on the entry itself never sees an agent message (OP694).
	var items struct {
		Data *[]struct {
			Item struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			} `json:"item"`
		} `json:"data"`
	}
	if r.call("thread/items/list", map[string]any{"threadId": threadID, "turnId": turn.ID, "limit": 8, "sortDirection": "desc"}, &items) != nil || items.Data == nil || len(*items.Data) > 8 {
		return codexTurnObservation{}, errCodexHostUnknown
	}
	for _, entry := range *items.Data {
		if entry.Item.Type == "agentMessage" && entry.Item.Text != nil && strings.TrimSpace(*entry.Item.Text) != "" {
			obs.AgentMessage = true
		}
	}
	return obs, nil
}
