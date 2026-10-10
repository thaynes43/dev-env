package agentd

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const decisionStartupBound = 5 * time.Second

// Only this currently owning daemon starts a continuation. A recorded answer
// can wait for its current native invocation's own Wait result; no PID/lock
// observation ever supplies a missing causal result.
func (d *Daemon) dispatchDecision(ctx context.Context) error {
	if !d.S.ManagedChildDecisions || d.writerRefused || d.S.writer == nil {
		return nil
	}
	record, err := readPrivateDecision(d.S)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.State != "Answered" {
		return nil
	}
	current, binding, err := decisionAuthority(d.S)
	if err != nil || !decisionBindingMatches(record, binding) || record.Source != binding {
		return d.uncertainDecision(ctx, record.ID)
	}
	result, err := readNativeInvocation(d.S)
	if err != nil {
		return d.uncertainDecision(ctx, record.ID)
	}
	if result.Phase == "Running" {
		prior := binding
		prior.ThreadID = result.Binding.ThreadID
		if result.Binding == prior && ownedNativeStarted(d.S, current) == nil {
			if current.TUI && current.Resume && current.Prompt == "" {
				if err := d.reserveDecisionResume(ctx, record, current); err != nil {
					return err
				}
				return d.pasteDecisionAnswer(ctx, record.ID, current)
			}
			return nil
		}
		return d.uncertainDecision(ctx, record.ID)
	}
	if !validNativeExit(result, binding) {
		return d.uncertainDecision(ctx, record.ID)
	}
	// The owned Wait receipt precedes the old wrapper's final cleanup. Wait
	// before reserving the answer; no ambiguous new start has happened yet.
	if err := waitDecisionTeardown(ctx, d.R, d.S); err != nil {
		return err
	}
	ws := protocol.Workspace{Clone: d.S.ClonePath(d.Session.Repo), Worktree: current.Dir, Branch: "agent/" + d.Session.Name}
	next, err := BuildResume(d.S, d.Session, ws, current, current.BootID, d.now())
	if err != nil {
		return d.uncertainDecision(ctx, record.ID)
	}
	if err := admitNativeContinuation(ctx, d.S, d.Session, current, &next); err != nil {
		return d.uncertainDecision(ctx, record.ID)
	}
	if err := d.reserveDecisionResume(ctx, record, next); err != nil {
		return err
	}
	// Do not hold the supervisor gate while starting run-agent: its final proof
	// needs common Git -> that same gate. Any ambiguous start remains one-shot.
	if err := StartAgent(ctx, d.R, d.S, next, d.Self); err != nil {
		return d.uncertainDecision(ctx, record.ID)
	}
	if err := waitDecisionNativeStarted(ctx, d.S, next); err != nil {
		return d.uncertainDecision(ctx, record.ID)
	}
	return d.pasteDecisionAnswer(ctx, record.ID, next)
}

// Lock availability and exact tmux absence prove teardown readiness only.
// They never replace the causal receipt checked before and after this wait.
func waitDecisionTeardown(ctx context.Context, r Runner, s Settings) error {
	ctx, cancel := context.WithTimeout(ctx, decisionStartupBound)
	defer cancel()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return errors.New("previous native teardown did not finish within its bound; answer remains recorded")
		}
		unlock, err := nativeInvocationLock(s)
		if err == nil {
			unlock()
			result, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"has-session", "-t", "=" + TmuxSession}})
			if err != nil {
				if ctx.Err() != nil || !decisionTmuxAbsent(result, err) {
					return errors.New("previous native tmux teardown is unconfirmed; answer remains recorded")
				}
				return nil
			}
		} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return errors.New("previous native lifetime teardown is unconfirmed; answer remains recorded")
		}
		select {
		case <-ctx.Done():
			return errors.New("previous native teardown did not finish within its bound; answer remains recorded")
		case <-tick.C:
		}
	}
}

func decisionTmuxAbsent(result Result, err error) bool {
	var ce *CmdError
	if !errors.As(err, &ce) || ce.ExitCode != 1 {
		return false
	}
	detail := strings.TrimSpace(string(result.Stderr))
	if detail == "" {
		detail = strings.TrimSpace(ce.Stderr)
	}
	return strings.HasPrefix(detail, "can't find session:") || strings.HasPrefix(detail, "no server running on ") ||
		(strings.HasPrefix(detail, "error connecting to ") && strings.HasSuffix(detail, "(No such file or directory)"))
}

func (d *Daemon) reserveDecisionResume(ctx context.Context, expected privateDecision, next Launch) error {
	gate, err := workspaceSupervisorLock(ctx, d.S)
	if err != nil {
		return err
	}
	defer gate()
	unlock, err := privateDecisionLock(d.S)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := readPrivateDecision(d.S)
	if err != nil || record.ID != expected.ID || record.State != "Answered" || record.Answer != expected.Answer || record.Source != expected.Source {
		return errors.New("answered decision changed before native resume")
	}
	if stop, err := workspaceStopRequested(d.S, d.Session); err != nil || stop {
		return errors.New("decision continuation refuses a requested or uncertain supervisor stop")
	}
	record.State, record.ResumeInvocationID = "ResumeStarting", next.NativeInvocationID
	return writePrivateDecision(d.S, record)
}

func waitDecisionNativeStarted(ctx context.Context, s Settings, expected Launch) error {
	ctx, cancel := context.WithTimeout(ctx, decisionStartupBound)
	defer cancel()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		current, err := privateCurrentLaunch(s)
		if err == nil && current.NativeInvocationID == expected.NativeInvocationID && current.SessionUID == expected.SessionUID &&
			current.PodUID == expected.PodUID && current.ConversationID == expected.ConversationID && current.Resume && current.TUI && current.Prompt == "" && ownedNativeStarted(s, current) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("exact owned native TUI startup is unconfirmed")
		case <-tick.C:
		}
	}
}

func (d *Daemon) uncertainDecision(ctx context.Context, id string) error {
	gate, err := workspaceSupervisorLock(ctx, d.S)
	if err != nil {
		return err
	}
	defer gate()
	unlock, err := privateDecisionLock(d.S)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := readPrivateDecision(d.S)
	if err != nil || record.ID != id {
		return errors.New("uncertain decision identity changed")
	}
	if record.State != "Delivered" {
		record.State = "Uncertain"
	}
	if err := writePrivateDecision(d.S, record); err != nil {
		return err
	}
	return errors.New("decision continuation is uncertain; answer will not be replayed")
}

// The final stop check, one-answer dispatch fence and exact-thread paste share
// the private supervisor gate. A failed or ambiguous acknowledgement never
// changes Dispatching back to an answer that may be retried.
func (d *Daemon) pasteDecisionAnswer(ctx context.Context, id string, expected Launch) error {
	gate, err := workspaceSupervisorLock(ctx, d.S)
	if err != nil {
		return err
	}
	defer gate()
	unlock, err := privateDecisionLock(d.S)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := readPrivateDecision(d.S)
	if err != nil || record.ID != id || record.State != "ResumeStarting" || record.ResumeInvocationID != expected.NativeInvocationID {
		return errors.New("decision dispatch is changed or already fenced")
	}
	current, binding, err := decisionAuthority(d.S)
	if err != nil || current.NativeInvocationID != expected.NativeInvocationID || !decisionBindingMatches(record, binding) || ownedNativeStarted(d.S, current) != nil {
		return refuseDecisionDispatch(d.S, record)
	}
	if stop, err := workspaceStopRequested(d.S, d.Session); err != nil || stop {
		return refuseDecisionDispatch(d.S, record)
	}
	record.State = "Dispatching"
	if err := writePrivateDecision(d.S, record); err != nil {
		return err
	}
	if err := Deliver(ctx, d.R, d.S, d.Session, "coordinator/recorded-owner-answer", decisionAnswerText(record)); err != nil {
		record.State = "Uncertain"
		if err := writePrivateDecision(d.S, record); err != nil {
			return err
		}
		return errors.New("decision delivery acknowledgement is uncertain; answer will not be replayed")
	}
	record.State = "Delivered"
	return writePrivateDecision(d.S, record)
}

// Called with both private supervisor and record locks held. A refused final
// check retains the answer but cannot return it to dispatchable Answered state.
func refuseDecisionDispatch(s Settings, record privateDecision) error {
	record.State = "Uncertain"
	if err := writePrivateDecision(s, record); err != nil {
		return err
	}
	return errors.New("decision native identity or supervisor stop is uncertain; answer will not be replayed")
}
