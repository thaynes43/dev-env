package agentrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

// thread start (DESIGN-057) starts one fresh interactive thread or session that
// Tom can see in his provider's phone app, behind one adapter per provider:
// Codex in an existing host (agentd ctl thread-start over the exec route pair
// uses), Claude as a new remote session (rc start's flow), Cursor and Grok
// refused until qualified. It prints exactly one thread-launch.v1 line and
// exits with its outcome's code; only an observed first response exits 0.

const threadMaxHandoff = 4096

var threadNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type threadArgs struct {
	agent, host, project, repo, model, effort, handoff, name, key string
	wait                                                          time.Duration
}

func (a *app) thread(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		return a.help([]string{"thread"})
	}
	if len(args) == 0 || args[0] != "start" {
		return a.threadRefuse("", apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	return a.threadStart(ctx, args[1:])
}

// threadRefuse prints a result for a launch that never reached an adapter.
func (a *app) threadRefuse(provider, outcome, code string, exit int) error {
	rec := threadlaunch.NewRecorder(firstOf(provider, "unknown"), a.env.Now)
	return a.threadEmit(rec.Finish(outcome, code, apiv1.ThreadPhaseHostReady, 0), exit)
}

func (a *app) threadEmit(r apiv1.ThreadLaunchResult, exit int) error {
	b, err := threadlaunch.Encode(r)
	if err != nil {
		return fail(ExitFailed, "thread result unavailable")
	}
	if _, err := a.env.Stdout.Write(b); err != nil {
		return fail(ExitFailed, "thread output unavailable")
	}
	return &exitError{code: exit}
}

func (a *app) threadStart(ctx context.Context, args []string) error {
	cmd := newCommand("thread", outputJSON)
	var t threadArgs
	cmd.fs.StringVar(&t.agent, "agent", "", "")
	cmd.fs.StringVar(&t.host, "host", "", "")
	cmd.fs.StringVar(&t.project, "project", "", "")
	cmd.fs.StringVar(&t.repo, "repo", "", "")
	cmd.fs.StringVar(&t.model, "model", "", "")
	cmd.fs.StringVar(&t.effort, "effort", "", "")
	cmd.fs.StringVar(&t.handoff, "handoff", "", "")
	cmd.fs.StringVar(&t.name, "name", "", "")
	cmd.fs.StringVar(&t.key, "idempotency-key", "", "")
	cmd.fs.DurationVar(&t.wait, "wait", 5*time.Minute, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitOK {
			return err
		}
		return a.threadRefuse(t.agent, apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	p, known := threadlaunch.Lookup(t.agent)
	if len(pos) != 0 || (cmd.c.output != "" && cmd.c.output != outputJSON) || !known || t.wait <= 0 || t.wait > 10*time.Minute {
		if !known {
			_, _ = io.WriteString(a.env.Stderr, "agent-run: thread start --agent is one of "+strings.Join(threadlaunch.Names(), ", ")+"\n")
		}
		return a.threadRefuse(t.agent, apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	switch p.Route {
	case threadlaunch.RouteHost:
		return a.threadOnHost(ctx, cmd, t)
	case threadlaunch.RouteSession:
		return a.threadInSession(ctx, cmd, t)
	}
	return a.threadRefuse(p.Name, apiv1.ThreadBlocked, "provider_not_qualified", apiv1.ThreadLaunchExit(apiv1.ThreadBlocked))
}

// threadReadHandoff is the one handoff rule for every provider: the Codex
// wrapper's 1 to 4096 UTF-8 bytes, not blank, and rc start's rule of no
// control character but newline and tab.
func threadReadHandoff(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, threadMaxHandoff+1))
	if err != nil || len(b) > threadMaxHandoff || !utf8.Valid(b) || strings.TrimSpace(string(b)) == "" || !rcNoControl(string(b)) {
		return nil, false
	}
	return b, true
}

// threadOnHost is the Codex adapter's client half: check the host, then exec
// agentd ctl thread-start in it with the handoff on stdin. Authorization is
// sessionExecTarget's: no exec from an ordinary session pod (D-19), and RBAC
// decides everywhere else. No grant is added.
func (a *app) threadOnHost(ctx context.Context, cmd *command, t threadArgs) error {
	invalid := func() error { return a.threadRefuse("codex", apiv1.ThreadFailed, "invalid_input", ExitUsage) }
	if !projectName.MatchString(t.host) || t.project != "" || t.key != "" || (t.repo != "" && !apiv1.ValidRepoName(t.repo)) || protocol.ValidateCodexEffort(t.model, t.effort) != nil || t.effort == "" {
		return invalid()
	}
	if t.name == "" {
		t.name = "thread-" + a.env.Now().UTC().Format("0102-150405")
	}
	handoff, ok := threadReadHandoff(t.handoff)
	if !threadNamePattern.MatchString(t.name) || !ok {
		return invalid()
	}
	rec := threadlaunch.NewRecorder("codex", a.env.Now)
	rec.Result.Host, rec.Result.SessionName, rec.Result.Name, rec.Result.RequestedModel = t.host, t.host, t.name, t.model
	if a.inSessionPod() && !a.inWorkbenchPod() {
		// D-19: an agent in an ordinary session pod has no exec into another pod.
		rec.Phase(apiv1.ThreadPhaseHostReady, "error", "client", "caller_not_allowed")
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, "caller_not_allowed", apiv1.ThreadPhaseHostReady, t.wait), ExitFailed)
	}
	argv, s, err := a.sessionExecTarget(ctx, cmd, "thread start", t.host)
	if err != nil {
		// The message is agent-run's own fixed text; it explains the refusal.
		_, _ = io.WriteString(a.env.Stderr, "agent-run: "+err.Error()+"\n")
		code := "exec_failed"
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitAuth {
			code = "unauthenticated"
		}
		rec.Phase(apiv1.ThreadPhaseHostReady, "error", "client", code)
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, code, apiv1.ThreadPhaseHostReady, t.wait), ExitFailed)
	}
	switch {
	case s.Agent != "codex" || s.Mode != "remote":
		rec.Phase(apiv1.ThreadPhaseHostReady, "error", "session_view", "not_codex_host")
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, "not_codex_host", apiv1.ThreadPhaseHostReady, t.wait), ExitFailed)
	case s.Phase != "Running":
		rec.Phase(apiv1.ThreadPhaseHostReady, "error", "session_view", "host_not_running")
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, "host_not_running", apiv1.ThreadPhaseHostReady, t.wait), ExitFailed)
	}
	argv = append(argv, "-i", "--", "agentd", "ctl", "thread-start", "--model", t.model, "--effort", t.effort, "--name", t.name, "--wait", t.wait.String())
	if t.repo != "" {
		argv = append(argv, "--repo", t.repo)
	}
	if a.env.RunCapture == nil {
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, "exec_failed", apiv1.ThreadPhaseHostReady, t.wait), ExitFailed)
	}
	// The host bounds its own wait; this one only stops a stuck exec.
	execCtx, cancel := context.WithTimeout(ctx, t.wait+time.Minute)
	defer cancel()
	// stderr (kubectl's and agentd's fixed text) reaches the caller and a
	// bounded copy tells an old agentd's usage line from other failures.
	errCopy := &cappedOutput{}
	code, out, err := a.env.RunCapture(execCtx, argv, bytes.NewReader(handoff), io.MultiWriter(a.env.Stderr, errCopy))
	unconfirmed := func() error {
		return a.threadEmit(rec.Finish(apiv1.ThreadUnconfirmed, "host_result_unconfirmed", apiv1.ThreadPhaseClientStarted, t.wait), apiv1.ThreadLaunchExit(apiv1.ThreadUnconfirmed))
	}
	if err != nil {
		// kubectl overflowed or did not start: the host may still have run.
		return unconfirmed()
	}
	empty := len(bytes.TrimSpace(out)) == 0
	res, decodeErr := apiv1.DecodeThreadLaunch(bytes.TrimSuffix(out, []byte("\n")))
	switch {
	case decodeErr != nil && empty && code == 2 && bytes.Contains(errCopy.data, []byte("usage: ctl")):
		// agentd's ctl answers an unknown verb with its usage and exit 2.
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, "host_unsupported", apiv1.ThreadPhaseHostReady, t.wait), ExitFailed)
	case decodeErr != nil && empty && code == 1:
		// thread-start prints a result on every path it reaches, so exit 1
		// with nothing on stdout is kubectl's own refusal (forbidden, not
		// found, connection): nothing ran on the host.
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, "exec_failed", apiv1.ThreadPhaseHostReady, t.wait), ExitFailed)
	case decodeErr != nil || code != apiv1.ThreadLaunchExit(res.Outcome) || res.Provider != "codex" || (res.Host != "" && res.Host != t.host):
		return unconfirmed()
	}
	return a.threadEmit(res, code)
}

// threadInSession is the Claude adapter: rc start's create, follow and one
// handoff, translated. Claude's supervisor owns one conversation per session,
// so a fresh conversation is a fresh remote session; --host is refused.
func (a *app) threadInSession(ctx context.Context, cmd *command, t threadArgs) error {
	if t.host != "" || t.name != "" {
		_, _ = io.WriteString(a.env.Stderr, "agent-run: claude starts a new remote session per conversation: give --project, not --host or --name\n")
		return a.threadRefuse("claude", apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	if _, ok := threadReadHandoff(t.handoff); !ok {
		return a.threadRefuse("claude", apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	r := newRCRun(a)
	r.wait = t.wait
	var rc *apiv1.RCLaunchResult
	r.sink = func(res apiv1.RCLaunchResult) { rc = &res }
	_ = r.startClaude(ctx, cmd.c.conn, t.project, t.repo, "claude", t.model, t.effort, t.handoff, t.key)
	if rc == nil {
		return a.threadRefuse("claude", apiv1.ThreadUnconfirmed, "session_launch_failed", apiv1.ThreadLaunchExit(apiv1.ThreadUnconfirmed))
	}
	res := claudeThreadResult(*rc)
	exit := apiv1.ThreadLaunchExit(res.Outcome)
	if res.Error != nil && res.Error.Code == "invalid_input" {
		exit = ExitUsage
	}
	return a.threadEmit(res, exit)
}

// claudeThreadResult maps rc start's phases onto the thread phases. A
// registered link is visibility; the first response is not observable here.
func claudeThreadResult(rc apiv1.RCLaunchResult) apiv1.ThreadLaunchResult {
	perm := threadlaunch.PermissionFor("claude")
	res := apiv1.ThreadLaunchResult{Link: rc.Link, Version: apiv1.ThreadLaunchVersion, Provider: "claude", Permission: &perm, SessionName: rc.SessionName, RequestedModel: rc.RequestedModel, TotalMs: rc.TotalMs, DeadlineMs: rc.DeadlineMs, Phases: map[string]apiv1.RCLaunchPhase{}}
	from := map[string]string{
		apiv1.ThreadPhaseHostReady:     "pod_ready",
		apiv1.ThreadPhasePaired:        "pairing_required",
		apiv1.ThreadPhaseClientStarted: "agent_started",
		apiv1.ThreadPhaseThreadCreated: "agent_started",
		apiv1.ThreadPhaseSourceVisible: "link_available",
		apiv1.ThreadPhaseHandoff:       "handoff",
	}
	for phase, src := range from {
		p, ok := rc.Phases[src]
		if !ok {
			p = apiv1.RCLaunchPhase{State: "unknown", EvidenceSource: "client", Reason: "not_observed"}
		}
		res.Phases[phase] = p
	}
	res.Phases[apiv1.ThreadPhaseFirstResponse] = apiv1.RCLaunchPhase{State: "unknown", EvidenceSource: "client", Reason: "provider_observation_unavailable"}
	phase := func(p string) string {
		switch p {
		case "agent_started":
			return apiv1.ThreadPhaseClientStarted
		case "rc_ready", "link_available":
			return apiv1.ThreadPhaseSourceVisible
		case "handoff":
			return apiv1.ThreadPhaseHandoff
		}
		return apiv1.ThreadPhaseHostReady
	}
	if rc.Outcome == "link_available" {
		res.Outcome = apiv1.ThreadVisible
		res.Error = &apiv1.RCLaunchError{Code: "response_not_observable", Message: threadlaunch.Message("response_not_observable"), Phase: apiv1.ThreadPhaseFirstResponse}
		return res
	}
	switch rc.Outcome {
	case "failed", "blocked", "timed_out", "unconfirmed", "action_required":
		res.Outcome = rc.Outcome
	default: // "accepted" ends only at a zero wait, which thread start refuses
		res.Outcome = apiv1.ThreadTimedOut
	}
	res.Error = &apiv1.RCLaunchError{Code: "session_launch_failed", Message: threadlaunch.Message("session_launch_failed")}
	if rc.Error != nil {
		res.Error = &apiv1.RCLaunchError{Code: rc.Error.Code, Message: rc.Error.Message, Phase: phase(rc.Error.Phase)}
	}
	return res
}
