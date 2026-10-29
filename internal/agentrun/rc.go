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
	{"agent_started", "agent_start_timeout", 90 * time.Second},
	{"rc_ready", "rc_registration_timeout", 90 * time.Second},
	{"link_available", "link_unavailable", 10 * time.Second},
}

type rcRun struct {
	a       *app
	started time.Time
	wait    time.Duration
	result  apiv1.RCLaunchResult
}

func newRCRun(a *app) *rcRun {
	r := &rcRun{a: a, started: a.env.Now()}
	r.result = apiv1.RCLaunchResult{Version: apiv1.RCLaunchVersion, Outcome: "accepted", PollIntervalMs: 2000, Phases: make(map[string]apiv1.RCLaunchPhase)}
	for _, phase := range []string{"api_accepted", "project_inventory_ok", "pod_scheduled", "pod_ready", "agent_started", "provider_auth_ok", "rc_ready", "link_available", "pairing_required", "handoff", "cli_recorded"} {
		r.phase(phase, "unknown", "client", "not_observed")
	}
	r.phase("provider_auth_ok", "unknown", "session_view", "provider_readiness_unavailable")
	r.phase("rc_ready", "unknown", "session_view", "provider_readiness_unavailable")
	r.phase("pairing_required", "not_applicable", "client", "claude_remote")
	r.phase("cli_recorded", "unknown", "session_view", "input_record_unavailable")
	return r
}

func (r *rcRun) elapsed() time.Duration { return max(0, r.a.env.Now().Sub(r.started)) }

func (r *rcRun) phase(name, state, source, reason string) {
	old, exists := r.result.Phases[name]
	if exists && old.State == state && (state != "unknown" || old.Reason == reason && old.EvidenceSource == source) {
		return
	}
	r.result.Phases[name] = apiv1.RCLaunchPhase{State: state, FirstObservedAt: r.a.env.Now().UTC(), SinceRequestMs: r.elapsed().Milliseconds(), EvidenceSource: source, Reason: reason}
}

func (r *rcRun) finish(outcome, code, phase string, exit int) error {
	r.result.Outcome = outcome
	r.result.TotalMs = r.elapsed().Milliseconds()
	r.result.DeadlineMs = max(0, r.wait.Milliseconds())
	if code != "" {
		r.result.Error = &apiv1.RCLaunchError{Code: code, Phase: phase}
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
	if agent == "codex" {
		return r.finish("blocked", "codex_inventory_unavailable", "", ExitFailed)
	}
	if len(pos) != 0 || cmd.c.output != outputJSON || !projectName.MatchString(project) || (repo != "" && !apiv1.ValidRepoName(repo)) || agent != "claude" || apiv1.CheckClaudeModel(model) != apiv1.ClaudeModelOK || len(model) > 128 || (effort != "" && !apiv1.ClaudeEffortAccepted(model, effort)) || handoff == "" || r.wait < 0 || (key != "" && !rcKeyPattern.MatchString(key)) {
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
	c, err := r.connection(ctx, cmd.c.conn)
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
	}
	r.phase("api_accepted", "succeeded", "create_response", reason)
	var view apiv1.Session
	decodeErr := json.Unmarshal(raw, &view)
	if decodeErr == nil && projectName.MatchString(view.Name) {
		r.result.SessionName = view.Name
	}
	if decodeErr != nil || !rcValidView(view) || view.Mode != "remote" || view.Agent != agent || view.Model != model || (effort != "" && view.Effort != effort) || view.IdempotencyKey != key || (repo != "" && view.Repo != repo) {
		return r.finish("unconfirmed", "status_unavailable", "project_inventory_ok", ExitUnconfirmed)
	}
	r.result.SessionName, r.result.Repo, r.result.Base = view.Name, view.Repo, view.Base
	r.phase("project_inventory_ok", "succeeded", "create_response", "accepted_repo_and_base_echoed")
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
			r.phase("pod_scheduled", "succeeded", "session_conditions", "PodScheduled")
		case c.Type == "PodReady" && c.Status == "False" && rcScheduledCondition(c, v.Node):
			r.phase("pod_scheduled", "succeeded", "session_conditions", "node_bound_pod_condition")
		case c.Type == "PodReady" && c.Status == "True":
			r.phase("pod_ready", "succeeded", "session_conditions", "PodReady")
			// A ready Pod necessarily passed scheduling, even on older views
			// that report only the PodReady condition.
			r.phase("pod_scheduled", "succeeded", "session_conditions", "PodReady")
		}
	}
	authCode := ""
	if v.AgentStatus != nil {
		msg := strings.ToLower(v.AgentStatus.Message)
		if strings.Contains(msg, "login-conflict") || strings.Contains(msg, "login_conflict") {
			authCode = "login_conflict"
		}
		if strings.Contains(msg, "auth_projection_unavailable") || strings.Contains(msg, "claude authentication requires a current keeper projection") || strings.Contains(msg, "login-required: waiting for the keeper's claude projection") {
			authCode = "auth_projection_unavailable"
		}
		if v.AgentStatus.State == "running" && strings.TrimSpace(v.AgentStatus.ConversationID) != "" {
			r.phase("agent_started", "succeeded", "session_agent_status", "running_with_conversation_id")
		}
	}
	if v.RemoteControl != nil {
		if v.RemoteControl.State == "registered" {
			r.result.RCRegistered = true
			// Registration is evidence of registration, never provider readiness.
			r.phase("rc_ready", "unknown", "session_remote_control", "provider_readiness_unavailable")
		}
		if rcLinkPattern.MatchString(v.RemoteControl.URL) {
			link := v.RemoteControl.URL
			r.result.Link = &link
			r.phase("link_available", "succeeded", "session_remote_control", "validated_rc_url")
		}
	}
	return authCode
}

func (r *rcRun) stageDone(name string) bool {
	if name == "rc_ready" {
		return r.result.RCRegistered
	}
	return r.result.Phases[name].State == "succeeded"
}

func (r *rcRun) follow(ctx context.Context, c *conn, view apiv1.Session, handoff string) error {
	stage := 0
	stageStart := r.elapsed()
	for {
		if code := r.observe(view); code != "" {
			return r.finish("action_required", code, "agent_started", ExitPending)
		}
		if r.wait == 0 || r.elapsed() >= r.wait || ctx.Err() != nil {
			return r.finish("accepted", "launch_deadline", rcStages[stage].phase, ExitPending)
		}
		for r.stageDone(rcStages[stage].phase) {
			stage++
			stageStart = r.elapsed()
			if stage == len(rcStages) {
				return r.handoff(ctx, c, handoff)
			}
		}
		end := min(r.wait, stageStart+rcStages[stage].budget)
		if r.elapsed() >= end {
			code := rcStages[stage].timeout
			if end == r.wait {
				code = "launch_deadline"
			}
			return r.finish("timed_out", code, rcStages[stage].phase, ExitPending)
		}
		if r.a.env.Sleep(ctx, min(pollInterval, end-r.elapsed())) != nil {
			return r.finish("accepted", "launch_deadline", rcStages[stage].phase, ExitPending)
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
			code := rcStages[stage].timeout
			if end == r.wait || ctx.Err() != nil {
				code = "launch_deadline"
			}
			return r.finish("timed_out", code, rcStages[stage].phase, ExitPending)
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
	payload, _ := json.Marshal(apiv1.MessageRequest{Text: text})
	status, _, err := r.a.send(ctx, c, http.MethodPost, c.url+apiv1.SessionMessagesPath(r.result.SessionName), payload)
	if err != nil || status >= 500 || status/100 == 2 && status != http.StatusAccepted {
		r.phase("handoff", "unknown", "message_response", "handoff_unconfirmed")
		return r.finish("unconfirmed", "handoff_unconfirmed", "handoff", ExitUnconfirmed)
	}
	if status != http.StatusAccepted {
		r.phase("handoff", "rejected", "message_response", "handoff_rejected")
		return r.finish("failed", "handoff_rejected", "handoff", ExitFailed)
	}
	r.phase("handoff", "transport_submitted", "message_response", "http_202")
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
	r.phase("api_accepted", "unknown", "session_view", "create_receipt_unavailable")
	r.phase("project_inventory_ok", "unknown", "session_view", "create_echo_unavailable")
	if code := r.observe(v); code != "" {
		return r.finish("action_required", code, "agent_started", ExitPending)
	}
	if r.result.Link != nil {
		return r.finish("link_available", "", "", ExitOK)
	}
	return r.finish("accepted", "", "", ExitPending)
}
