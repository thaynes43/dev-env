package agentd

// This disabled follow-up has no production caller. Consistency with an
// admitted host task is not admission of Cursor, a credential or a new kind.

import (
	"context"
	"errors"
	"os"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

var errCursorTaskBinding = errors.New("cursor task identity is missing, changed or unconfirmed")

// The future owning daemon supplies its current trusted objects, never helper
// output or task-authored identity JSON. This slice does not acquire that proof.
type cursorTaskContext struct {
	settings Settings
	session  protocol.Session
	launch   Launch
	bootID   string
}

type cursorTaskBinding struct {
	native                            nativeBinding
	workspace, task, repo, worktree   string
	client, stateRoot, requestedModel string
}

type cursorBoundTask struct {
	plan    cursorScaffoldPlan
	binding cursorTaskBinding
}

type cursorBoundTaskResult struct {
	binding cursorTaskBinding
	helper  cursorScaffoldResult // helper session ID is not a platform Session ID.
}

func bindCursorTask(p cursorScaffoldPlan, current cursorTaskContext) (cursorTaskBinding, error) {
	s, sess, l := current.settings, current.session, current.launch
	if _, err := newCursorScaffoldPlan(p.invocation, p.client, p.worktree, p.stateRoot, p.model, p.prompt, p.timeout); err != nil {
		return cursorTaskBinding{}, errCursorTaskBinding
	}
	if s.Home == "" || p.stateRoot != s.StateDir || privateManagedState(s) != nil {
		return cursorTaskBinding{}, errCursorTaskBinding
	}
	if !s.ManagedCodexTasks || !s.ManagedChildDecisions || sess.Agent != protocol.AgentCodex ||
		sess.Mode != protocol.ModeTask || l.Provider != sess.Agent || l.TUI || l.Resume ||
		l.Continuation != nil || l.NativeThreadConfirmed || l.ConversationID != "" ||
		!cursorScaffoldLabel.MatchString(sess.Name) || !protocol.ValidKubeUID(sess.SessionUID) ||
		!protocol.ValidKubeUID(s.PodUID) || !cursorScaffoldLabel.MatchString(current.bootID) ||
		l.Session != sess.Name || l.SessionUID != sess.SessionUID || l.PodUID != s.PodUID || l.BootID != current.bootID ||
		l.NativeInvocationID != p.invocation || l.Dir != p.worktree || l.Prompt != p.prompt || l.Timeout != p.timeout {
		return cursorTaskBinding{}, errCursorTaskBinding
	}
	o := l.WorkspaceOwner
	if o == nil || o.Version != workspaceVersion || o.State != "owned" || o.Launched ||
		o.Generation == 0 || o.Task != sess.Name || o.SessionUID != sess.SessionUID || o.PodUID != s.PodUID ||
		o.Worktree != p.worktree || o.Repo != sess.Repo || sess.Repo == "" ||
		o.Workspace == "" || o.Workspace != s.WorkspaceID || sess.Workspace == nil ||
		sess.Workspace.ID != o.Workspace || sess.Workspace.SessionUID != sess.SessionUID {
		return cursorTaskBinding{}, errCursorTaskBinding
	}
	// Reuse the existing platform invocation/digest contract. Its Codex kind
	// describes the host, not the Cursor server or independently served model.
	native, err := invocationBinding(l)
	if err != nil {
		return cursorTaskBinding{}, errCursorTaskBinding
	}
	return cursorTaskBinding{native: native, workspace: o.Workspace, task: o.Task, repo: o.Repo,
		worktree: p.worktree, client: p.client, stateRoot: p.stateRoot, requestedModel: p.model}, nil
}

func newCursorBoundTask(p cursorScaffoldPlan, current cursorTaskContext) (cursorBoundTask, error) {
	binding, err := bindCursorTask(p, current)
	if err != nil {
		return cursorBoundTask{}, err
	}
	return cursorBoundTask{plan: p, binding: binding}, nil
}

// runCursorBoundTask compares fresh context both before the first attempt and
// before attaching a successful helper result. Replaced Pod/task/boot/owner
// context can never relabel that receipt. It never retries a failed attempt.
func runCursorBoundTask(ctx context.Context, task cursorBoundTask, current func() (cursorTaskContext, error),
	decisions *os.File, start cursorScaffoldStart) (cursorBoundTaskResult, error) {
	if current == nil || ctx.Err() != nil {
		return cursorBoundTaskResult{}, errCursorTaskBinding
	}
	check := func() bool {
		observed, err := current()
		if err != nil || ctx.Err() != nil {
			return false
		}
		binding, err := bindCursorTask(task.plan, observed)
		return err == nil && binding == task.binding
	}
	if !check() {
		return cursorBoundTaskResult{}, errCursorTaskBinding
	}
	result, err := runCursorScaffold(ctx, task.plan, decisions, start)
	if err != nil {
		return cursorBoundTaskResult{}, err
	}
	if !check() {
		return cursorBoundTaskResult{}, errCursorTaskBinding
	}
	return cursorBoundTaskResult{binding: task.binding, helper: result}, nil
}
