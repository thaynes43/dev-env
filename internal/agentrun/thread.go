package agentrun

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

// thread status (section 11) is the observe-only recheck of a Codex thread that
// thread start left visible; it is below, and it never launches or resends.
//
// thread start (DESIGN-057) starts one fresh interactive thread or session that
// Tom can see in his provider's phone app, behind one adapter per provider:
// Codex in an existing host (agentd ctl thread-start over the exec route pair
// uses, or through the operator's API from an ordinary session pod, section
// 10), Claude as a new remote session (rc start's flow), Cursor and Grok
// refused until qualified. It prints exactly one thread-launch.v1 line and
// exits with its outcome's code; only an observed first response exits 0.

// threadAPIGrace is how much longer than --wait agent-run waits for the API's
// answer: the operator stops the exec a minute after the wait and keeps half a
// minute to write its answer.
const threadAPIGrace = 2 * time.Minute

type threadArgs struct {
	agent, host, project, repo, model, effort, handoff, name, key string
	wait                                                          time.Duration
}

func (a *app) thread(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		return a.help([]string{"thread"})
	}
	if len(args) > 0 {
		switch args[0] {
		case "start":
			return a.threadStart(ctx, args[1:])
		case "status":
			return a.threadStatus(ctx, args[1:])
		}
	}
	return a.threadRefuse("", apiv1.ThreadFailed, "invalid_input", ExitUsage)
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
	if len(pos) != 0 || (cmd.c.output != "" && cmd.c.output != outputJSON) || !known || t.wait <= 0 || t.wait > apiv1.ThreadMaxWait {
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

// threadReadHandoff reads the handoff and applies the one rule for every
// provider (apiv1.ValidThreadHandoff): the Codex wrapper's 1 to 4096 UTF-8
// bytes, not blank, and rc start's rule of no control character but newline
// and tab.
func threadReadHandoff(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, apiv1.ThreadMaxHandoffBytes+1))
	if err != nil || !apiv1.ValidThreadHandoff(string(b)) {
		return nil, false
	}
	return b, true
}

// threadOnHost is the Codex adapter's client half: check the host, then exec
// agentd ctl thread-start in it with the handoff on stdin. Authorization is
// sessionExecTarget's: RBAC decides where kubectl exec runs (the v1 pod, a
// workbench session, a laptop). An ordinary session pod has no exec (D-19), so
// it asks the operator's API to run the same command (threadViaAPI). No grant
// is added on either path.
func (a *app) threadOnHost(ctx context.Context, cmd *command, t threadArgs) error {
	invalid := func() error { return a.threadRefuse("codex", apiv1.ThreadFailed, "invalid_input", ExitUsage) }
	if t.project != "" || t.key != "" || (t.repo != "" && !apiv1.ValidRepoName(t.repo)) || protocol.ValidateCodexEffort(t.model, t.effort) != nil || t.effort == "" {
		return invalid()
	}
	if t.host == "" {
		return a.threadChooseHost(ctx, cmd, t)
	}
	if !projectName.MatchString(t.host) {
		return invalid()
	}
	if t.name == "" {
		t.name = "thread-" + a.env.Now().UTC().Format("0102-150405")
	}
	handoff, ok := threadReadHandoff(t.handoff)
	if !apiv1.ValidThreadName(t.name) || !ok {
		return invalid()
	}
	rec := threadlaunch.NewRecorder("codex", a.env.Now)
	rec.Result.Host, rec.Result.SessionName, rec.Result.Name, rec.Result.RequestedModel = t.host, t.host, t.name, t.model
	if a.inSessionPod() && !a.inWorkbenchPod() {
		return a.threadViaAPI(ctx, cmd, t, handoff, rec)
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

// threadViaAPI is the Codex route from an ordinary session pod (DESIGN-057
// section 10): one POST /v1/sessions/<host>/threads, which the operator serves
// by running agentd ctl thread-start in the host, as it delivers a message
// (D-65). The operator checks the host and relays the host's result; this side
// checks that result again. It is sent at most once: a lost answer or a 5xx is
// unconfirmed and never repeated, and an operator without the route is a clear
// refusal, never a fallback.
func (a *app) threadViaAPI(ctx context.Context, cmd *command, t threadArgs, handoff []byte, rec *threadlaunch.Recorder) error {
	refuse := func(code string, exit int) error {
		rec.Phase(apiv1.ThreadPhaseHostReady, "error", "api", code)
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, code, apiv1.ThreadPhaseHostReady, t.wait), exit)
	}
	unconfirmed := func() error {
		return a.threadEmit(rec.Finish(apiv1.ThreadUnconfirmed, "api_result_unconfirmed", apiv1.ThreadPhaseClientStarted, t.wait), apiv1.ThreadLaunchExit(apiv1.ThreadUnconfirmed))
	}
	// Fixed agent-run text and the operator's error text; never the handoff.
	say := func(format string, args ...any) {
		_, _ = fmt.Fprintf(a.env.Stderr, "agent-run: thread start: "+format+"\n", args...)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		say("%v", err)
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitAuth {
			return refuse("unauthenticated", ExitFailed)
		}
		return refuse("api_unreachable", ExitFailed)
	}
	payload, err := json.Marshal(apiv1.ThreadStartRequest{Model: t.model, Effort: t.effort, Name: t.name, Repo: t.repo, Wait: t.wait.String(), Handoff: string(handoff)})
	if err != nil {
		return refuse("invalid_input", ExitUsage)
	}
	// This one request may run for the whole wait; the API's usual 30 s
	// client timeout would cut it short.
	long := *c
	long.http = &http.Client{Transport: c.http.Transport, Timeout: t.wait + threadAPIGrace}
	status, raw, err := a.send(ctx, &long, http.MethodPost, c.url+apiv1.SessionThreadsPath(url.PathEscape(t.host)), payload)
	var ee *exitError
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.As(err, &ee):
		// No token could be read: nothing was sent.
		say("%v", err)
		return refuse("unauthenticated", ExitFailed)
	case errors.As(err, &certErr) || (err != nil && nothingSent(err)):
		say("could not reach the API at %s: %v; nothing was sent", c.url, err)
		return refuse("api_unreachable", ExitFailed)
	case err != nil:
		say("no confirmed answer from the API at %s: %v; agent-run sent the request once and will not repeat it", c.url, err)
		return unconfirmed()
	case status == http.StatusOK:
		res, err := apiv1.DecodeThreadLaunch(bytes.TrimSpace(raw))
		if err != nil || res.Provider != "codex" || (res.Host != "" && res.Host != t.host) {
			say("the API's answer is not one thread-launch.v1 result for %s", t.host)
			return unconfirmed()
		}
		return a.threadEmit(res, apiv1.ThreadLaunchExit(res.Outcome))
	}
	var doc apiv1.ErrorResponse
	isAPIError := json.Unmarshal(raw, &doc) == nil && doc.Error.Code != ""
	if isAPIError {
		say("the API answered %d %s: %s", status, doc.Error.Code, doc.Error.Message)
	} else {
		say("the API answered %d %s", status, http.StatusText(status))
	}
	switch {
	case status == http.StatusNotFound && (!isAPIError || strings.HasPrefix(doc.Error.Message, "no route ")):
		// An operator without the route answers its catch-all 404 before it
		// reads the token.
		return refuse("route_unsupported", ExitFailed)
	case status == http.StatusNotFound:
		return refuse("host_not_found", ExitFailed)
	case status == http.StatusUnauthorized:
		return refuse("unauthenticated", ExitFailed)
	case status == http.StatusForbidden:
		return refuse("caller_not_allowed", ExitFailed)
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnsupportedMediaType || status == http.StatusUnprocessableEntity:
		return refuse("invalid_input", ExitUsage)
	case status >= 500:
		// agent-run cannot tell an operator refusal from a failure after the
		// operator took the request, so it is a lost answer.
		return unconfirmed()
	}
	return refuse("api_refused", ExitFailed)
}

// threadStatusBound stops a stuck status exec. The host bounds its own reads
// at a minute.
const threadStatusBound = 90 * time.Second

// threadStatus (DESIGN-057 section 11) is the observe-only recheck of a thread
// that thread start left visible: one read-only pass on the host, printed as
// one thread-launch.v1 line with that outcome's exit code. It takes no
// handoff, model or wait, sends nothing on the exec's stdin, and never runs
// thread-start: it cannot launch or resend.
func (a *app) threadStatus(ctx context.Context, args []string) error {
	cmd := newCommand("thread", outputJSON)
	var agent, host, thread, repo string
	cmd.fs.StringVar(&agent, "agent", "", "")
	cmd.fs.StringVar(&host, "host", "", "")
	cmd.fs.StringVar(&thread, "thread", "", "")
	cmd.fs.StringVar(&repo, "repo", "", "")
	// early prints a result for a status that never reached a host.
	early := func(provider, outcome, code string, exit int) error {
		rec := threadlaunch.NewStatusRecorder(firstOf(provider, "unknown"), a.env.Now)
		return a.threadEmit(rec.Finish(outcome, code, apiv1.ThreadPhaseHostReady, 0), exit)
	}
	pos, err := cmd.parse(a, args)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitOK {
			return err
		}
		return early(agent, apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	p, known := threadlaunch.Lookup(agent)
	if len(pos) != 0 || (cmd.c.output != "" && cmd.c.output != outputJSON) || !known {
		if !known {
			_, _ = io.WriteString(a.env.Stderr, "agent-run: thread status --agent is one of "+strings.Join(threadlaunch.Names(), ", ")+"\n")
		}
		return early(agent, apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	switch p.Route {
	case threadlaunch.RouteSession:
		// Claude's first response is not observable here; rc status reads its session.
		return early(p.Name, apiv1.ThreadFailed, "provider_observation_unavailable", ExitFailed)
	case threadlaunch.RouteUnqualified:
		return early(p.Name, apiv1.ThreadBlocked, "provider_not_qualified", apiv1.ThreadLaunchExit(apiv1.ThreadBlocked))
	}
	if !projectName.MatchString(host) || !apiv1.ValidThreadID(thread) || (repo != "" && !apiv1.ValidRepoName(repo)) {
		return early("codex", apiv1.ThreadFailed, "invalid_input", ExitUsage)
	}
	rec := threadlaunch.NewStatusRecorder("codex", a.env.Now)
	rec.Result.Host, rec.Result.SessionName = host, host
	refuse := func(source, code string) error {
		rec.Phase(apiv1.ThreadPhaseHostReady, "error", source, code)
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, code, apiv1.ThreadPhaseHostReady, 0), ExitFailed)
	}
	if a.inSessionPod() && !a.inWorkbenchPod() {
		// Thread start's API route (section 10) carries a launch only. Status
		// stays on the exec route, which an ordinary session pod does not have.
		return refuse("client", "status_route_unavailable")
	}
	argv, s, err := a.sessionExecTarget(ctx, cmd, "thread status", host)
	if err != nil {
		_, _ = io.WriteString(a.env.Stderr, "agent-run: "+err.Error()+"\n")
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitAuth {
			return refuse("client", "unauthenticated")
		}
		return refuse("client", "exec_failed")
	}
	switch {
	case s.Agent != "codex" || s.Mode != "remote":
		return refuse("session_view", "not_codex_host")
	case s.Phase != "Running":
		return refuse("session_view", "host_not_running")
	case a.env.RunCapture == nil:
		return refuse("client", "exec_failed")
	}
	// No -i: the status exec has no stdin, so it can carry no thread input.
	argv = append(argv, "--", "agentd", "ctl", "thread-status", "--thread", thread)
	if repo != "" {
		argv = append(argv, "--repo", repo)
	}
	execCtx, cancel := context.WithTimeout(ctx, threadStatusBound)
	defer cancel()
	errCopy := &cappedOutput{}
	code, out, err := a.env.RunCapture(execCtx, argv, strings.NewReader(""), io.MultiWriter(a.env.Stderr, errCopy))
	// Nothing on this path launches, so a lost or invalid answer is a failed
	// observation, never an unconfirmed launch: checking again is safe.
	unavailable := func() error {
		return a.threadEmit(rec.Finish(apiv1.ThreadFailed, "status_unavailable", apiv1.ThreadPhaseHostReady, 0), ExitFailed)
	}
	if err != nil {
		return unavailable()
	}
	empty := len(bytes.TrimSpace(out)) == 0
	res, decodeErr := apiv1.DecodeThreadLaunch(bytes.TrimSuffix(out, []byte("\n")))
	switch {
	case decodeErr != nil && empty && code == 2 && bytes.Contains(errCopy.data, []byte("usage: ctl")):
		// An agentd without thread-status answers its ctl usage and exit 2.
		return refuse("client", "status_unsupported")
	case decodeErr != nil && empty && code == 1:
		// kubectl's own refusal: nothing ran on the host.
		return refuse("client", "exec_failed")
	case decodeErr != nil || code != apiv1.ThreadLaunchExit(res.Outcome) || res.Provider != "codex" || (res.Host != "" && res.Host != host) || !statusAnswerFor(res, thread):
		return unavailable()
	}
	return a.threadEmit(res, code)
}

// statusAnswerFor reports whether a host's result is one a status pass can
// give for this thread. A pass that found the thread names it; one that
// stopped earlier names none. It never ends unconfirmed, timed out or blocked:
// those are a launch's ends, and status launched nothing.
func statusAnswerFor(res apiv1.ThreadLaunchResult, thread string) bool {
	switch res.Outcome {
	case apiv1.ThreadResponded, apiv1.ThreadVisible, apiv1.ThreadHidden:
		return res.ThreadID == thread
	case apiv1.ThreadFailed, apiv1.ThreadActionRequired:
		return res.ThreadID == "" || res.ThreadID == thread
	}
	return false
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
