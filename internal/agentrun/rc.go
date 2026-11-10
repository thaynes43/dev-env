package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const rcMaxHandoff = 8192

var rcLinkPattern = regexp.MustCompile(apiv1.PortalRemoteControlURLPattern)
var rcKeyPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)

var rcStages = []struct {
	phase, timeout string
	budget         time.Duration
}{
	{"pod_scheduled", "scheduling_timeout", 60 * time.Second},
	{"pod_ready", "pod_ready_timeout", 120 * time.Second},
	// agent_started follows --wait. The zero is not a timeout; stageDeadline
	// explains why a fixed budget cannot see a healthy start.
	{"agent_started", "agent_start_timeout", 0},
	{"rc_ready", "rc_registration_timeout", 90 * time.Second},
	{"link_available", "link_unavailable", 10 * time.Second},
}

// rcAgentStarted reports that the session's agent process is up and owns a
// conversation. agentd reports a started agent as busy, idle or waiting
// (internal/agentd/protocol/status.go). It has never reported "running": a
// check for that word waited out every launch on a healthy session and never
// delivered the handoff (OP531).
func rcAgentStarted(a *apiv1.AgentStatus) bool {
	if a == nil || strings.TrimSpace(a.ConversationID) == "" {
		return false
	}
	switch a.State {
	case protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting:
		return true
	}
	return false
}

type rcRun struct {
	a        *app
	started  time.Time
	accepted time.Duration
	reused   bool
	wait     time.Duration
	result   apiv1.RCLaunchResult
	// sink, when set, receives the finished result instead of stdout:
	// agent-run thread start translates it (DESIGN-057).
	sink func(apiv1.RCLaunchResult)
}

func newRCRun(a *app) *rcRun {
	r := &rcRun{a: a, started: a.env.Now()}
	r.result = apiv1.RCLaunchResult{Version: apiv1.RCLaunchVersion, Outcome: "accepted", PollIntervalMs: 2000, Phases: make(map[string]apiv1.RCLaunchPhase)}
	for _, phase := range []string{"api_accepted", "project_inventory_ok", "pod_scheduled", "pod_ready", "agent_started", "provider_auth_ok", "rc_ready", "link_available", "pairing_required", "handoff", "cli_recorded"} {
		r.unobserved(phase, "client", "not_observed")
	}
	for _, phase := range []string{"provider_auth_ok", "rc_ready", "cli_recorded"} {
		reason := "provider_readiness_unavailable"
		if phase == "cli_recorded" {
			reason = "input_record_unavailable"
		}
		r.unobserved(phase, "session_view", reason)
	}
	r.result.Phases["pairing_required"] = apiv1.RCLaunchPhase{State: "not_applicable", EvidenceSource: "client", Reason: "claude_remote"}
	return r
}

func (r *rcRun) elapsed() time.Duration { return max(0, r.a.env.Now().Sub(r.started)) }

func (r *rcRun) unobserved(name, source, reason string) {
	r.result.Phases[name] = apiv1.RCLaunchPhase{State: "unknown", EvidenceSource: source, Reason: reason}
}

func (r *rcRun) phase(name, state, source, reason string) {
	old, exists := r.result.Phases[name]
	if exists && old.State == state && (state != "unknown" || old.Reason == reason && old.EvidenceSource == source) {
		return
	}
	now := r.a.env.Now().UTC()
	since := r.elapsed().Milliseconds()
	r.result.Phases[name] = apiv1.RCLaunchPhase{State: state, FirstObservedAt: &now, SinceRequestMs: &since, EvidenceSource: source, Reason: reason}
}

func (r *rcRun) finish(outcome, code, phase string, exit int) error {
	r.result.Outcome = outcome
	r.result.TotalMs = r.elapsed().Milliseconds()
	r.result.DeadlineMs = max(0, r.wait.Milliseconds())
	if code != "" {
		r.result.Error = &apiv1.RCLaunchError{Code: code, Message: rcErrorMessages[code], Phase: phase}
	}
	if r.sink != nil {
		r.sink(r.result)
		return &exitError{code: exit}
	}
	b, err := json.Marshal(r.result)
	if err != nil || len(b)+1 > 32<<10 {
		return fail(ExitFailed, "rc result unavailable")
	}
	b = append(b, '\n')
	if _, err := r.a.env.Stdout.Write(b); err != nil {
		return fail(ExitFailed, "rc output unavailable")
	}
	return &exitError{code: exit}
}

// Diagnostics are fixed client text, never reflected API messages or inputs.
var rcErrorMessages = map[string]string{
	"invalid_input":                 "Invalid launch input",
	"codex_adapter_not_implemented": "Codex remote launch is not implemented in 2a; no inventory was probed",
	"unauthenticated":               "API authentication is unavailable or was refused",
	"launch_forbidden":              "The caller is not allowed to launch this session",
	"project_unavailable":           "Project admission is unavailable",
	"request_conflict":              "The idempotency key conflicts with the launch request",
	"scheduling_timeout":            "Pod scheduling exceeded its budget",
	"pod_ready_timeout":             "Pod readiness exceeded its budget",
	"agent_start_timeout":           "Agent startup exceeded its budget",
	"login_conflict":                "A login conflict requires owner action",
	"auth_projection_unavailable":   "The keeper authentication projection remained unavailable for 30 seconds during agent startup",
	"auth_refresh_pending":          "The keeper's next login generation was still pending when the wait for agent start ended; no sign-in was requested",
	"claude_login_required":         "The session's profile has no usable Claude login, so the agent will not start until a person sets one up",
	"rc_registration_timeout":       "Remote Control registration exceeded its budget",
	"link_unavailable":              "A valid Remote Control link was not observed within its budget",
	"session_ended":                 "The session has ended or is being reaped",
	"handoff_rejected":              "The handoff was refused",
	"handoff_unconfirmed":           "Handoff submission is unconfirmed; it will not be resent",
	"launch_deadline":               "The launch wait ended; retain the session handle",
	"status_unavailable":            "The session exchange is unavailable or unconfirmed",
}

func (a *app) rc(ctx context.Context, args []string) error {
	r := newRCRun(a)
	if len(args) == 0 {
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	if args[0] == "-h" || args[0] == "--help" {
		return a.help([]string{"rc"})
	}
	switch args[0] {
	case "start":
		return r.start(ctx, args[1:])
	case "status":
		return r.status(ctx, args[1:])
	case "fresh":
		return a.rcFresh(ctx, args[1:])
	default:
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
}

// rc start deliberately does not use createSession: its project+remote path is
// allowed here only. The existing run/restore --project refusals stay intact.
func (r *rcRun) start(ctx context.Context, args []string) error {
	cmd := newCommand("rc", outputJSON)
	var project, repo, agent, model, effort, handoff, key string
	cmd.fs.StringVar(&project, "project", "", "")
	cmd.fs.StringVar(&repo, "repo", "", "")
	cmd.fs.StringVar(&agent, "agent", "", "")
	cmd.fs.StringVar(&model, "model", "", "")
	cmd.fs.StringVar(&effort, "effort", "", "")
	cmd.fs.StringVar(&handoff, "handoff", "", "")
	cmd.fs.StringVar(&key, "idempotency-key", "", "")
	cmd.fs.DurationVar(&r.wait, "wait", 5*time.Minute, "")
	pos, err := cmd.parse(r.a, args)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitOK {
			return err
		}
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	if r.wait < 0 || r.wait > 10*time.Minute {
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	if agent == "codex" {
		return r.finish("blocked", "codex_adapter_not_implemented", "", ExitFailed)
	}
	if len(pos) != 0 || cmd.c.output != outputJSON {
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	return r.startClaude(ctx, cmd.c.conn, project, repo, agent, model, effort, handoff, key)
}

// startClaude is rc start after its flags: validation, the create, the follow
// and the one handoff. agent-run thread start's Claude adapter calls it too.
func (r *rcRun) startClaude(ctx context.Context, conn connOpts, project, repo, agent, model, effort, handoff, key string) error {
	if !projectName.MatchString(project) || (repo != "" && !apiv1.ValidRepoName(repo)) || agent != "claude" || apiv1.CheckClaudeModel(model) != apiv1.ClaudeModelOK || len(model) > 128 || (effort != "" && !apiv1.ClaudeEffortAccepted(model, effort)) || handoff == "" || (key != "" && !rcKeyPattern.MatchString(key)) {
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	text, ok := rcReadHandoff(handoff)
	if !ok {
		return r.finish("failed", "invalid_input", "handoff", ExitUsage)
	}
	if key == "" {
		key = r.a.env.NewKey()
	}
	if !rcKeyPattern.MatchString(key) {
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	r.result.Project, r.result.Agent, r.result.RequestedModel, r.result.IdempotencyKey = project, agent, model, key
	// A zero wait still submits the create and returns its handle, but never
	// polls or sends a message. Other waits cap connection setup and every call.
	if r.wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, max(0, r.wait-r.elapsed()))
		defer cancel()
	}
	c, err := r.connection(ctx, conn)
	if err != nil {
		return r.finish("failed", "unauthenticated", "api_accepted", ExitAuth)
	}
	req := apiv1.CreateSessionRequest{Project: project, Repo: repo, Agent: agent, Mode: "remote", Model: model, Effort: effort, IdempotencyKey: key}
	payload, _ := json.Marshal(req)
	status, raw, err := r.a.send(ctx, c, http.MethodPost, c.url+apiv1.SessionsPath, payload)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return r.finish("failed", "unauthenticated", "api_accepted", ExitAuth)
		}
		if nothingSent(err) {
			return r.finish("failed", "status_unavailable", "api_accepted", ExitRetry)
		}
		return r.finish("unconfirmed", "status_unavailable", "api_accepted", ExitUnconfirmed)
	}
	if status >= 500 {
		return r.finish("unconfirmed", "status_unavailable", "api_accepted", ExitUnconfirmed)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return r.refusal(status, raw, "api_accepted")
	}
	reason := "created"
	if status == http.StatusOK {
		reason = "reused_by_key"
		r.reused = true
		r.phase("handoff", "unknown", "create_response", "reused_by_key_not_resent")
	}
	r.accepted = r.elapsed()
	r.phase("api_accepted", "ok", "create_response", reason)
	var view apiv1.Session
	decodeErr := json.Unmarshal(raw, &view)
	if decodeErr == nil && projectName.MatchString(view.Name) {
		r.result.SessionName = view.Name
	}
	if decodeErr != nil || !rcValidView(view) || view.Mode != "remote" || view.Agent != agent || view.Model != model || (effort != "" && view.Effort != effort) || view.IdempotencyKey != key || (repo != "" && view.Repo != repo) {
		return r.finish("unconfirmed", "status_unavailable", "project_inventory_ok", ExitUnconfirmed)
	}
	r.result.SessionName, r.result.Repo, r.result.Base = view.Name, view.Repo, view.Base
	r.phase("project_inventory_ok", "ok", "create_response", "accepted_repo_and_base_echoed")
	return r.follow(ctx, c, view, text)
}

func rcReadHandoff(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, rcMaxHandoff+1))
	if err != nil || len(b) > rcMaxHandoff || !utf8.Valid(b) || strings.TrimSpace(string(b)) == "" || !rcNoControl(string(b)) {
		return "", false
	}
	return string(b), true
}

// Same C0, DEL and C1 rule as apiserver/podcmd.stripControl.
func rcNoControl(s string) bool {
	for _, c := range s {
		if c != '\n' && c != '\t' && (c < 0x20 || c == 0x7f || (c >= 0x80 && c <= 0x9f)) {
			return false
		}
	}
	return true
}

func (r *rcRun) connection(ctx context.Context, opts connOpts) (*conn, error) {
	c, err := r.a.connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http, one.rejectOversizeResponse = &hc, true
	return &one, nil
}

func rcValidView(v apiv1.Session) bool {
	return projectName.MatchString(v.Name) && apiv1.ValidRepoName(v.Repo) && len(v.Base) > 0 && len(v.Base) <= 256 && rcNoControl(v.Base)
}

// The current operator projects scheduling through PodReady. A node-bound
// Starting/container condition follows scheduling; Creating, Scheduling and
// Unschedulable (and unrelated failure conditions) do not establish it.
func rcScheduledCondition(c apiv1.Condition, node string) bool {
	if node == "" {
		return false
	}
	switch c.Reason {
	case "Starting", "ContainerCreating", "PodInitializing", "ErrImagePull", "ImagePullBackOff", "CrashLoopBackOff", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "ContainerTerminated":
		return true
	}
	return false
}

func (r *rcRun) observe(v apiv1.Session) string {
	for _, c := range v.Conditions {
		switch {
		case c.Type == "PodScheduled" && c.Status == "True":
			r.phase("pod_scheduled", "ok", "session_conditions", "PodScheduled")
		case c.Type == "PodReady" && c.Status == "False" && rcScheduledCondition(c, v.Node):
			r.phase("pod_scheduled", "ok", "session_conditions", "node_bound_pod_condition")
		case c.Type == "PodReady" && c.Status == "True":
			r.phase("pod_ready", "ok", "session_conditions", "PodReady")
			// A ready Pod necessarily passed scheduling, even on older views
			// that report only the PodReady condition.
			r.phase("pod_scheduled", "ok", "session_conditions", "PodReady")
		}
	}
	authCode := ""
	if v.AgentStatus != nil {
		msg := strings.ToLower(v.AgentStatus.Message)
		if strings.Contains(msg, "login-conflict") || strings.Contains(msg, "login_conflict") {
			authCode = "login_conflict"
		}
		// The pod reports a just-expired access token as a pending refresh (the
		// keeper's next generation is on its way). Not an owner action.
		if authCode == "" && strings.HasPrefix(msg, "refreshing:") {
			authCode = "auth_refresh_pending"
		}
		if authCode == "" && (strings.Contains(msg, "auth_projection_unavailable") || strings.Contains(msg, "claude authentication requires a current keeper projection") || strings.Contains(msg, "login-required: waiting for the keeper's claude projection")) {
			authCode = "auth_projection_unavailable"
		}
		if rcAgentStarted(v.AgentStatus) {
			r.phase("agent_started", "ok", "session_agent_status", "running_with_conversation_id")
		}
	}
	r.observeRejectedLogin(v.AgentStatus)
	if v.RemoteControl != nil {
		if v.RemoteControl.State == "registered" {
			r.result.RCRegistered = true
			// Registration is evidence of registration, never provider readiness.
			r.phase("rc_ready", "unknown", "session_remote_control", "provider_readiness_unavailable")
		}
		if rcLinkPattern.MatchString(v.RemoteControl.URL) {
			link := v.RemoteControl.URL
			r.result.Link = &link
			r.phase("link_available", "ok", "session_remote_control", "validated_rc_url")
		}
	}
	return authCode
}

// observeRejectedLogin sets the provider_auth_ok reason while the latest agent
// message starts with agentd's fixed prefix for a login the provider rejected
// while its access token was unexpired (or for a record it cannot believe), and
// returns it to the default once a later message no longer does. Information
// only: no gate, no new outcome.
func (r *rcRun) observeRejectedLogin(a *apiv1.AgentStatus) {
	msg := ""
	if a != nil {
		msg = strings.ToLower(strings.TrimSpace(a.Message))
	}
	switch {
	case strings.HasPrefix(msg, "login-rejected:"):
		r.unobserved("provider_auth_ok", "session_agent_status", "login_rejected_pending")
	case strings.HasPrefix(msg, "login-unverified:"):
		r.unobserved("provider_auth_ok", "session_agent_status", "login_rejection_unverifiable")
	default:
		switch r.result.Phases["provider_auth_ok"].Reason {
		case "login_rejected_pending", "login_rejection_unverifiable":
			r.unobserved("provider_auth_ok", "session_view", "provider_readiness_unavailable")
		}
	}
}

func (r *rcRun) stageDone(name string) bool {
	if name == "rc_ready" {
		return r.result.RCRegistered
	}
	return r.result.Phases[name].State == "ok"
}

func rcSessionEnded(v apiv1.Session) bool {
	return v.Reaping || v.Phase == "Failed" || v.Phase == "Suspended" || v.Phase == "Archived"
}

// rcAgentBlocked reports an agent state that ends the follow at once.
// agentd reports pending, busy, idle, waiting, exited, failed, interrupted
// and login-required (protocol.Agent*). login-required means the pod has no
// usable login, so the agent has not started and will not without a person
// or a credential. Two messages that share that state stay on the paths that
// already classify them: refreshing: (auth_refresh_pending; the keeper's next
// generation can land with no person) and the projection warning
// (auth_projection_unavailable after 30s, because that projection can still
// land). failed means agentd could not start the agent. busy, idle and
// waiting are a started agent. pending, exited and interrupted stay on the
// wait. A login_conflict message is decided before this runs.
func rcAgentBlocked(a *apiv1.AgentStatus, authCode string) (outcome, code string, stop bool) {
	if a == nil {
		return "", "", false
	}
	switch a.State {
	case protocol.AgentFailed:
		return "failed", "session_ended", true
	case protocol.AgentLoginRequired:
		switch authCode {
		case "auth_refresh_pending", "auth_projection_unavailable":
			return "", "", false
		}
		return "action_required", "claude_login_required", true
	default:
		return "", "", false
	}
}

func (r *rcRun) follow(ctx context.Context, c *conn, view apiv1.Session, handoff string) error {
	stage := 0
	stageStart := r.accepted
	var projectionSince *time.Duration
	for {
		authCode := r.observe(view)
		if rcSessionEnded(view) {
			return r.finish("failed", "session_ended", rcStages[stage].phase, ExitFailed)
		}
		if authCode == "login_conflict" {
			r.phase("agent_started", "blocked", "session_agent_status", authCode)
			return r.finish("action_required", authCode, "agent_started", ExitPending)
		}
		if outcome, code, stop := rcAgentBlocked(view.AgentStatus, authCode); stop {
			if outcome == "action_required" {
				r.phase("agent_started", "blocked", "session_agent_status", code)
				return r.finish(outcome, code, "agent_started", ExitPending)
			}
			return r.finish(outcome, code, "agent_started", ExitFailed)
		}
		if r.wait == 0 {
			return r.finish("accepted", "launch_deadline", rcStages[stage].phase, ExitPending)
		}
		if r.elapsed() >= r.wait || ctx.Err() != nil {
			return r.finish("timed_out", rcStageTimeoutCode(stage, authCode, true), rcStages[stage].phase, ExitPending)
		}
		for r.stageDone(rcStages[stage].phase) {
			stage++
			stageStart = r.elapsed()
			if stage == len(rcStages) {
				return r.handoff(ctx, c, handoff)
			}
			if rcStages[stage].phase == "pod_ready" {
				stageStart = r.accepted
			}
		}
		// The keeper projection can recover while agentd waits. Count only
		// continuous observations within the agent-start stage, not pod startup.
		if rcStages[stage].phase == "agent_started" && authCode == "auth_projection_unavailable" {
			if projectionSince == nil {
				now := r.elapsed()
				projectionSince = &now
			}
			if r.elapsed()-*projectionSince >= 30*time.Second {
				r.phase("agent_started", "blocked", "session_agent_status", authCode)
				return r.finish("action_required", authCode, "agent_started", ExitPending)
			}
		} else {
			projectionSince = nil
		}
		end := r.stageDeadline(stage, stageStart)
		if r.elapsed() >= end {
			return r.finish("timed_out", rcStageTimeoutCode(stage, authCode, end == r.wait), rcStages[stage].phase, ExitPending)
		}
		if r.a.env.Sleep(ctx, min(pollInterval, end-r.elapsed())) != nil {
			return r.finish("timed_out", rcStageTimeoutCode(stage, authCode, true), rcStages[stage].phase, ExitPending)
		}
		// No extra read at the deadline; a slow request is bounded by the same
		// remaining phase/global budget, not the ordinary 30s call timeout.
		if r.elapsed() >= end {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, end-r.elapsed())
		r.result.Polls++
		status, raw, err := r.a.send(callCtx, c, http.MethodGet, c.url+apiv1.SessionPath(r.result.SessionName), nil)
		callErr := callCtx.Err()
		cancel()
		if callErr != nil {
			// A request still in flight when the budget ends is classified like the
			// deadline check above, so the last observed status decides the reason.
			return r.finish("timed_out", rcStageTimeoutCode(stage, authCode, end == r.wait || ctx.Err() != nil), rcStages[stage].phase, ExitPending)
		}
		if r.elapsed() >= end || ctx.Err() != nil {
			continue
		}
		if err != nil {
			var ee *exitError
			if errors.As(err, &ee) {
				return r.finish("failed", "unauthenticated", rcStages[stage].phase, ExitAuth)
			}
			return r.finish("failed", "status_unavailable", rcStages[stage].phase, ExitFailed)
		}
		if status != http.StatusOK {
			return r.refusal(status, raw, rcStages[stage].phase)
		}
		var next apiv1.Session
		if json.Unmarshal(raw, &next) != nil || !rcValidView(next) || next.Name != r.result.SessionName || next.Repo != r.result.Repo || next.Base != r.result.Base || next.Agent != "claude" || next.Mode != "remote" || next.Model != r.result.RequestedModel {
			return r.finish("failed", "status_unavailable", rcStages[stage].phase, ExitFailed)
		}
		view = next
	}
}

func (r *rcRun) handoff(ctx context.Context, c *conn, text string) error {
	// The create request hash excludes the handoff. A reused key therefore
	// cannot authorize another message, even if a previous attempt sent none.
	if r.reused {
		return r.finish("unconfirmed", "handoff_unconfirmed", "handoff", ExitUnconfirmed)
	}
	payload, _ := json.Marshal(apiv1.MessageRequest{Text: text})
	status, _, err := r.a.send(ctx, c, http.MethodPost, c.url+apiv1.SessionMessagesPath(r.result.SessionName), payload)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			r.phase("handoff", "error", "client", "not_sent")
			return r.finish("failed", "unauthenticated", "handoff", ExitAuth)
		}
		if nothingSent(err) {
			r.phase("handoff", "error", "client", "not_sent")
			return r.finish("failed", "status_unavailable", "handoff", ExitPending)
		}
	}
	if err != nil || status >= 500 || status/100 == 2 && status != http.StatusAccepted {
		r.phase("handoff", "unknown", "message_response", "handoff_unconfirmed")
		return r.finish("unconfirmed", "handoff_unconfirmed", "handoff", ExitUnconfirmed)
	}
	if status != http.StatusAccepted {
		r.phase("handoff", "error", "message_response", "handoff_rejected")
		return r.finish("failed", "handoff_rejected", "handoff", ExitFailed)
	}
	r.phase("handoff", "ok", "message_response", "transport_submitted")
	return r.finish("link_available", "", "", ExitOK)
}

func (r *rcRun) refusal(status int, raw []byte, phase string) error {
	code, exit := "status_unavailable", ExitFailed
	switch status {
	case http.StatusBadRequest:
		code = "invalid_input"
	case http.StatusUnauthorized:
		code, exit = "unauthenticated", ExitAuth
	case http.StatusForbidden:
		code, exit = "launch_forbidden", ExitAuth
	case http.StatusConflict:
		code = "request_conflict"
	case http.StatusUnprocessableEntity:
		var e apiv1.ErrorResponse
		if json.Unmarshal(raw, &e) == nil {
			for _, f := range e.Error.Fields {
				if f.Field == "project" {
					code = "project_unavailable"
				}
			}
		}
	}
	if code == "project_unavailable" {
		phase = "project_inventory_ok"
		r.phase(phase, "error", "create_response", code)
	}
	return r.finish("failed", code, phase, exit)
}

func (r *rcRun) status(ctx context.Context, args []string) error {
	cmd := newCommand("rc", outputJSON)
	pos, err := cmd.parse(r.a, args)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) && ee.code == ExitOK {
			return err
		}
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	if len(pos) != 1 || !projectName.MatchString(pos[0]) || cmd.c.output != outputJSON {
		return r.finish("failed", "invalid_input", "", ExitUsage)
	}
	r.result.SessionName = pos[0]
	c, err := r.connection(ctx, cmd.c.conn)
	if err != nil {
		return r.finish("failed", "unauthenticated", "", ExitAuth)
	}
	r.result.Polls++
	status, raw, err := r.a.send(ctx, c, http.MethodGet, c.url+apiv1.SessionPath(pos[0]), nil)
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return r.finish("failed", "unauthenticated", "", ExitAuth)
		}
		return r.finish("failed", "status_unavailable", "", ExitFailed)
	}
	if status != http.StatusOK {
		return r.refusal(status, raw, "")
	}
	var v apiv1.Session
	if json.Unmarshal(raw, &v) != nil || !rcValidView(v) || v.Name != pos[0] || v.Agent != "claude" || v.Mode != "remote" || len(v.Model) > 128 || apiv1.CheckClaudeModel(v.Model) != apiv1.ClaudeModelOK {
		return r.finish("failed", "status_unavailable", "", ExitFailed)
	}
	r.result.Repo, r.result.Base = v.Repo, v.Base
	r.result.Agent, r.result.RequestedModel = v.Agent, v.Model
	if rcKeyPattern.MatchString(v.IdempotencyKey) {
		r.result.IdempotencyKey = v.IdempotencyKey
	}
	r.unobserved("api_accepted", "session_view", "create_receipt_unavailable")
	r.unobserved("project_inventory_ok", "session_view", "create_echo_unavailable")
	authCode := r.observe(v)
	if rcSessionEnded(v) {
		return r.finish("failed", "session_ended", "", ExitFailed)
	}
	if authCode == "login_conflict" {
		r.phase("agent_started", "blocked", "session_agent_status", authCode)
		return r.finish("action_required", authCode, "agent_started", ExitPending)
	}
	if r.result.Link != nil {
		return r.finish("link_available", "", "", ExitOK)
	}
	return r.finish("accepted", "", "", ExitPending)
}

// stageDeadline is when follow stops waiting for this stage. agent_started
// follows --wait. Its evidence is the session status agentd publishes on a
// 60s heartbeat, so a process that is already up can stay invisible for
// almost a minute. On 2026-11-10 the fixed 90s budget that used to start
// when PodReady was observed expired at 120.6s: PodReady at 30.5s plus 90s.
// The conversation existed at 82s, inside that window, but the next
// heartbeat was the first status that could show it, and --wait was 6m.
// The handoff was never sent. Both the fixed budget and the heartbeat lag
// caused the miss. Other stages keep their budgets, still capped by --wait.
// An unconfirmed handoff is still never resent.
func (r *rcRun) stageDeadline(stage int, stageStart time.Duration) time.Duration {
	if rcStages[stage].phase == "agent_started" {
		return r.wait
	}
	return min(r.wait, stageStart+rcStages[stage].budget)
}

// rcStageTimeoutCode names why a stage budget ended. The wait wins, and the
// phase is still named, except during agent start while the status says
// "refreshing:" (the keeper's next login generation is pending). That expiry
// stays auth_refresh_pending, including when --wait is the deadline, so the
// reason is not dropped. No sign-in is requested.
func rcStageTimeoutCode(stage int, authCode string, atWait bool) string {
	if rcStages[stage].phase == "agent_started" && authCode == "auth_refresh_pending" {
		return "auth_refresh_pending"
	}
	if atWait {
		return "launch_deadline"
	}
	return rcStages[stage].timeout
}
