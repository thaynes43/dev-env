// Package threadlaunch is the provider-neutral core of `agent-run thread
// start` (DESIGN-057): the provider registry, the Adapter interface, a phase
// recorder and the one driver every adapter runs under. It is pure (standard
// library and apiv1 only), so agent-run stays free of Kubernetes, and it
// performs no I/O of its own: adapters do, behind their own fakes.
package threadlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Route says where a provider's adapter runs.
type Route string

const (
	// RouteHost: the adapter runs inside an existing v2 host session, reached
	// through the exec route `agent-run pair` uses.
	RouteHost Route = "host"
	// RouteSession: a fresh conversation is a fresh session through the API.
	RouteSession Route = "session"
	// RouteUnqualified: the provider has no qualified phone-visible route yet.
	RouteUnqualified Route = "unqualified"
)

// Provider is one registry row. Adding a provider is one row plus its adapter.
type Provider struct {
	Name  string
	Route Route
	// Unqualified explains, in fixed text, why the provider is refused.
	Unqualified string
	// Permission is the provider's launch permission contract.
	Permission apiv1.ThreadPermission
}

// Always-allow is the launch contract (DESIGN-057 section 6a): the adapter
// passes the provider's one bypass flag and the caller never picks one.
var (
	codexPermission = apiv1.ThreadPermission{Mode: apiv1.ThreadPermissionAlwaysAllow, Flag: "--dangerously-bypass-approvals-and-sandbox", Source: "wrapper_argv", NotBypassed: []string{}}
	// Claude's critical-path check (rm inside sh -c and the like) is not
	// covered by bypass mode, and it stays on (Tom, 2026-11-09: "Keep the
	// check").
	claudePermission = apiv1.ThreadPermission{Mode: apiv1.ThreadPermissionAlwaysAllow, Flag: "--dangerously-skip-permissions", Source: "agentd_launch_argv", NotBypassed: []string{"claude_critical_path_check"}}
	// The Cursor helpers never approve: ACP permission requests are answered
	// reject_once only, and cursor-agent gets no --force or --yolo.
	cursorPermission = apiv1.ThreadPermission{Mode: apiv1.ThreadPermissionNotQualified, Source: "cursor_helpers", NotBypassed: []string{"acp_permission_request"}}
)

var providers = []Provider{
	{Name: "codex", Route: RouteHost, Permission: codexPermission},
	{Name: "claude", Route: RouteSession, Permission: claudePermission},
	{Name: "cursor", Route: RouteUnqualified, Unqualified: cursorUnqualified, Permission: cursorPermission},
	// Grok runs through cursor-agent, so it is the Cursor route.
	{Name: "grok", Route: RouteUnqualified, Unqualified: cursorUnqualified, Permission: cursorPermission},
}

// PermissionFor is a provider's permission contract; an unknown provider is
// not qualified.
func PermissionFor(name string) apiv1.ThreadPermission {
	p, ok := Lookup(name)
	if !ok {
		return apiv1.ThreadPermission{Mode: apiv1.ThreadPermissionNotQualified, Source: "registry", NotBypassed: []string{}}
	}
	perm := p.Permission
	perm.NotBypassed = append([]string{}, perm.NotBypassed...)
	return perm
}

const cursorUnqualified = "Cursor (and Grok, which runs through cursor-agent) is not yet qualified: it is not a v2 AgentSession provider and has no phone-visible thread route; use the v1 Cursor worker until its adapter is qualified"

// Lookup finds a provider by its exact name.
func Lookup(name string) (Provider, bool) {
	for _, p := range providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// Names lists the registered providers, for usage text.
func Names() []string {
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.Name)
	}
	return out
}

// Fixed diagnostics. Messages are client text, never reflected native output.
var messages = map[string]string{
	"invalid_input":                    "Invalid launch input",
	"provider_not_qualified":           cursorUnqualified,
	"not_codex_host":                   "The host is not a Codex --interactive session",
	"host_not_running":                 "The host session is not Running",
	"host_unsupported":                 "The host's agentd has no thread-start; move the host to an image that ships it",
	"login_required":                   "The keeper's Codex access projection is not current on the host; renew the keeper login",
	"daemon_unavailable":               "The host's Codex daemon is not live; agentd restarts it with backoff",
	"remote_disconnected":              "The host's Codex daemon is not connected to Remote Control",
	"pairing_required":                 "This host never issued a pairing code, so no phone is paired: run agent-run pair <host> and have Tom type the code",
	"repo_unavailable":                 "The repository is not a directory on the host",
	"name_consumed":                    "This thread name was already used on the host; choose another name",
	"client_unavailable":               "The Codex client, tmux or the packaged wrapper is unavailable on the host; nothing was launched",
	"launch_unconfirmed":               "The client launch outcome is unknown; inspect the named tmux session on the host and never retry automatically",
	"client_not_running":               "The client did not survive its first observation; inspect the named tmux session on the host and never retry automatically",
	"thread_not_observed":              "The client runs but no thread appeared; it may be waiting at a folder-trust or other prompt in the named tmux session. An authorized person decides; never send keys while a native question is pending",
	"thread_ambiguous":                 "More than one new thread matches this launch; inspect the host before any retry",
	"hidden_source":                    "The thread was created with a source the phone's default list hides",
	"turn_failed":                      "The thread's first turn failed or was interrupted",
	"response_pending":                 "The thread is visible; its first response was not observed before the wait ended. Check again later and never resend",
	"response_not_observable":          "The conversation is visible; this provider's first response is not observable by this command",
	"observation_unavailable":          "The host's thread observation failed",
	"launch_deadline":                  "The launch wait ended; phases show how far it got. Never resend",
	"unauthenticated":                  "API authentication is unavailable or was refused",
	"caller_not_allowed":               "thread start for a Codex host runs from the workbench, the v1 pod or a laptop: an ordinary session pod has no exec into another pod (D-19)",
	"exec_failed":                      "The exec into the host failed before its thread-start ran; nothing was launched",
	"host_result_unconfirmed":          "The host returned no valid result; the launch may have happened. Inspect the host before any retry",
	"provider_readiness_unavailable":   "Provider readiness is unavailable",
	"session_launch_failed":            "The session launch did not reach a registered link",
	"provider_observation_unavailable": "This provider's first response is not observable by this command",
}

// Message is the fixed diagnostic for a code; an unknown code has none.
func Message(code string) string { return messages[code] }

// Stop ends a run with an outcome and a fixed code at a phase.
type Stop struct {
	Outcome, Code, Phase string
}

func (s *Stop) Error() string { return s.Code }

// Halt builds a Stop.
func Halt(outcome, code, phase string) *Stop {
	return &Stop{Outcome: outcome, Code: code, Phase: phase}
}

// ErrNotObservable from FirstResponse ends the run as visible: the provider
// offers no observation of its first response.
var ErrNotObservable = errors.New("first response not observable")

// Adapter is one provider's launch. Each method records its phases on the
// Recorder and returns a *Stop to end the run. Any other error ends it as
// unconfirmed at the current phase: nothing is ever retried after Create.
type Adapter interface {
	// Preflight checks the host and pairing. It creates nothing.
	Preflight(context.Context, *Recorder) error
	// Create starts the provider client at most once.
	Create(context.Context, *Recorder) error
	// Visible observes thread_created and source_visible; false polls again.
	Visible(context.Context, *Recorder) (bool, error)
	// FirstResponse observes first_response; false polls again.
	FirstResponse(context.Context, *Recorder) (bool, error)
}

// Options bound one run. Sleep is the poll clock; tests fake it.
type Options struct {
	Wait  time.Duration
	Poll  time.Duration
	Sleep func(context.Context, time.Duration) error
}

// Recorder holds the result while an adapter fills it.
type Recorder struct {
	Result  apiv1.ThreadLaunchResult
	started time.Time
	now     func() time.Time
}

// NewRecorder starts a result with every phase unknown and unobserved.
func NewRecorder(provider string, now func() time.Time) *Recorder {
	r := &Recorder{now: now, started: now()}
	perm := PermissionFor(provider)
	r.Result = apiv1.ThreadLaunchResult{Version: apiv1.ThreadLaunchVersion, Provider: provider, Permission: &perm, Phases: make(map[string]apiv1.RCLaunchPhase)}
	for _, p := range apiv1.ThreadLaunchPhases {
		r.Result.Phases[p] = apiv1.RCLaunchPhase{State: "unknown", EvidenceSource: "client", Reason: "not_observed"}
	}
	return r
}

// Elapsed is the time since the run began, never negative.
func (r *Recorder) Elapsed() time.Duration { return max(0, r.now().Sub(r.started)) }

// Started is when the run began.
func (r *Recorder) Started() time.Time { return r.started }

// Phase records a state the first time it is observed; repeating the same
// state and reason keeps the first observation time.
func (r *Recorder) Phase(name, state, source, reason string) {
	old, ok := r.Result.Phases[name]
	if ok && old.State == state && old.Reason == reason && old.FirstObservedAt != nil {
		return
	}
	now := r.now().UTC()
	since := r.Elapsed().Milliseconds()
	r.Result.Phases[name] = apiv1.RCLaunchPhase{State: state, FirstObservedAt: &now, SinceRequestMs: &since, EvidenceSource: source, Reason: reason}
}

// Unknown records a phase as unknown without an observation time.
func (r *Recorder) Unknown(name, source, reason string) {
	r.Result.Phases[name] = apiv1.RCLaunchPhase{State: "unknown", EvidenceSource: source, Reason: reason}
}

// Finish sets the outcome and its fixed error. A responded outcome has none.
func (r *Recorder) Finish(outcome, code, phase string, wait time.Duration) apiv1.ThreadLaunchResult {
	r.Result.Outcome = outcome
	r.Result.TotalMs = r.Elapsed().Milliseconds()
	r.Result.DeadlineMs = max(0, wait.Milliseconds())
	r.Result.Error = nil
	if outcome != apiv1.ThreadResponded {
		if code == "" {
			code = "launch_deadline"
		}
		r.Result.Error = &apiv1.RCLaunchError{Code: code, Message: Message(code), Phase: phase}
	}
	return r.Result
}

// Encode writes one result line within apiv1.ThreadLaunchMaxBytes.
func Encode(r apiv1.ThreadLaunchResult) ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil || len(b)+1 > apiv1.ThreadLaunchMaxBytes {
		return nil, errors.New("thread launch result unavailable")
	}
	return append(b, '\n'), nil
}

// errDeadline is the poll loop's own end: the wait ran out between polls.
var errDeadline = errors.New("wait ended")

func (r *Recorder) ok(phase string) bool { return r.Result.Phases[phase].State == "ok" }

// Run drives one adapter: preflight, one create, then polls visibility and the
// first response until done or the wait ends. It never repeats Create.
func Run(ctx context.Context, a Adapter, rec *Recorder, o Options) apiv1.ThreadLaunchResult {
	if o.Poll <= 0 {
		o.Poll = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, max(0, o.Wait-rec.Elapsed()))
	defer cancel()
	deadline := func(phase string) apiv1.ThreadLaunchResult {
		switch {
		case phase == apiv1.ThreadPhaseFirstResponse:
			return rec.Finish(apiv1.ThreadVisible, "response_pending", phase, o.Wait)
		case phase == apiv1.ThreadPhaseThreadCreated && rec.ok(apiv1.ThreadPhaseClientStarted) && !rec.ok(apiv1.ThreadPhaseThreadCreated):
			// The client runs but no thread appeared: say what may hold it.
			return rec.Finish(apiv1.ThreadTimedOut, "thread_not_observed", phase, o.Wait)
		}
		return rec.Finish(apiv1.ThreadTimedOut, "launch_deadline", phase, o.Wait)
	}
	end := func(phase string, err error, unexpected string) apiv1.ThreadLaunchResult {
		var stop *Stop
		switch {
		case errors.As(err, &stop):
			return rec.Finish(stop.Outcome, stop.Code, firstNonEmpty(stop.Phase, phase), o.Wait)
		case errors.Is(err, errDeadline) || ctx.Err() != nil:
			return deadline(phase)
		}
		return rec.Finish(unexpected, "observation_unavailable", phase, o.Wait)
	}
	// Nothing exists before Create returns: an unexpected preflight error is a
	// plain failure. After Create, the launch may have happened.
	if err := a.Preflight(ctx, rec); err != nil {
		return end(apiv1.ThreadPhaseHostReady, err, apiv1.ThreadFailed)
	}
	if ctx.Err() != nil {
		return deadline(apiv1.ThreadPhaseHostReady)
	}
	if err := a.Create(ctx, rec); err != nil {
		return end(apiv1.ThreadPhaseClientStarted, err, apiv1.ThreadUnconfirmed)
	}
	poll := func(step func(context.Context, *Recorder) (bool, error)) error {
		for {
			done, err := step(ctx, rec)
			if err != nil || done {
				return err
			}
			left := o.Wait - rec.Elapsed()
			if ctx.Err() != nil || left <= 0 || o.Sleep(ctx, min(o.Poll, left)) != nil {
				return errDeadline
			}
		}
	}
	if err := poll(a.Visible); err != nil {
		return end(apiv1.ThreadPhaseThreadCreated, err, apiv1.ThreadUnconfirmed)
	}
	if err := poll(a.FirstResponse); err != nil {
		if errors.Is(err, ErrNotObservable) {
			return rec.Finish(apiv1.ThreadVisible, "response_not_observable", apiv1.ThreadPhaseFirstResponse, o.Wait)
		}
		return end(apiv1.ThreadPhaseFirstResponse, err, apiv1.ThreadUnconfirmed)
	}
	return rec.Finish(apiv1.ThreadResponded, "", "", o.Wait)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
