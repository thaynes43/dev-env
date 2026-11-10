package frontdoor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// session_id() writes this form. Requiring the branch and worktree to agree
// avoids treating an arbitrary mention of a session as execution ownership.
var executionOwner = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30} \| worktree ([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?) \| branch agent/([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)$`)

func (h *handler) closeCoordinatorItem(ctx context.Context, id int64, evidence string) (string, error) {
	item := strconv.FormatInt(id, 10)
	text, err := h.work.call(ctx, "complete", item, opt("evidence", evidence), "--guard-json")
	if err == nil {
		return text, nil
	}
	// Preserve the initial refusal and diagnostic verbatim. A failed proof adds
	// one sentence and never falls through to a mutation or alternate lookup.
	refuse := func(reason string) (string, error) {
		sentence := "Takeover not attempted: " + reason + "."
		return text, fmt.Errorf("%w\n%s", err, sentence)
	}
	var failure *opworkFailure
	if !errors.As(err, &failure) || failure.guard == nil {
		return refuse("opwork did not report a structured ownership refusal")
	}
	g := failure.guard
	if g.Reason != "ownership" || g.Hold == nil || *g.Hold {
		return refuse("the item is held or its hold status is unproven")
	}
	if g.ID != id || g.LockVersion == nil || *g.LockVersion < 0 || g.Status == "" {
		return refuse("the refused item's identity, status or lock is unproven")
	}
	owner := executionOwner.FindStringSubmatch(g.Owner)
	if owner == nil || owner[1] != owner[2] {
		return refuse("the execution owner does not name a session in opwork's format")
	}
	out, lookupErr := agentrun.ExecuteCoordinatorStatus(ctx, h.op, agentrun.CoordinatorStatusInput{Item: id})
	if lookupErr != nil {
		return refuse("the operator could not prove this caller has one coordinator for the item")
	}
	sess := out.Session
	if sess.Name != owner[1] || sess.IdempotencyKey != "coordinator-"+item {
		return refuse("the caller's coordinator for the item does not match the execution owner")
	}
	if sess.Mode != "task" || sess.AgentStatus == nil || sess.AgentStatus.State != "exited" || sess.AgentStatus.Task == nil || sess.AgentStatus.Task.FinishedAt.IsZero() {
		return refuse("the coordinator's task is not proven finished")
	}
	verdict := sess.AgentStatus.Task.Verdict().State
	if verdict != apiv1.TaskSucceeded && verdict != apiv1.TaskFailed {
		return refuse("the coordinator's task did not end with Succeeded or Failed")
	}
	note := fmt.Sprintf("Taking over from this caller's own finished coordinator %s for item #%s; task %s.", sess.Name, item, verdict)
	claimText, claimErr := h.work.call(ctx, "claim", item, "--force", opt("expect-lock", strconv.FormatInt(*g.LockVersion, 10)), opt("note", note))
	if claimErr != nil {
		var claimFailure *opworkFailure
		if errors.As(claimErr, &claimFailure) && claimFailure.code == 3 {
			return refuse("the guarded claim was refused because the item changed or could not be claimed")
		}
		return h.closeReadBack(ctx, item, claimText, fmt.Errorf("takeover was not confirmed; completion was not attempted: %w", claimErr))
	}
	text, err = h.work.call(ctx, "complete", item, opt("evidence", evidence))
	if err != nil {
		return h.closeReadBack(ctx, item, text, fmt.Errorf("the claim succeeded; completion was not confirmed: %w", err))
	}
	return claimText + "\n" + text, nil
}

// A mutation may already have landed. Read only, report the actual state and
// never undo the claim or repeat either mutation when its outcome is uncertain.
func (h *handler) closeReadBack(ctx context.Context, item, text string, err error) (string, error) {
	state, readErr := h.work.call(ctx, "show", item)
	if readErr != nil {
		return text, fmt.Errorf("%w; current tracker state is unconfirmed: %w", err, readErr)
	}
	return text + "\nCurrent tracker state (read back):\n" + state, err
}
