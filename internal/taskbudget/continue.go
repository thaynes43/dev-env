package taskbudget

import (
	"context"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type Continuation struct {
	Prior      Binding   `json:"prior"`
	DecisionID string    `json:"decisionID"`
	Owner      string    `json:"owner"`
	At         time.Time `json:"at"`
}

// NativeContinueAuthorizer independently verifies the original still-live
// supervisor's proven stop and the explicit default-off policy opt-in.
type NativeContinueAuthorizer interface {
	AuthorizeNativeContinue(context.Context, *Ledger, Binding) error
}

// Continue permits one explicit authenticated-owner continuation within the
// ORIGINAL budget, on the ORIGINAL guard/Pod/HOME. It grants no extra quota,
// clears no prior stop/attempt/receipt and cannot resume an exhausted campaign.
func (s *Service) Continue(ctx context.Context, r apiv1.ContinueTaskBudgetRequest, owner string) (*Ledger, error) {
	if owner == "" || len(owner) > 256 || !protocol.ValidTaskBudgetIdentifier(r.DecisionID) {
		return nil, ErrDenied
	}
	return s.mutate(ctx, r.Binding, func(l *Ledger, _ time.Time) error {
		if !l.Latched || l.Reason != "NativeRootStopped" || !l.stopped() || l.Binding.Epoch != 1 || len(l.Continuations) != 0 {
			return ErrDenied
		}
		authorizer, ok := s.NativeInspector.(NativeContinueAuthorizer)
		if !ok {
			return ErrUnavailable
		}
		if err := authorizer.AuthorizeNativeContinue(ctx, l, r.Binding); err != nil {
			return err
		}
		now, err := l.account(s.now())
		if err != nil {
			return err
		}
		check := *l
		check.Latched = false
		check.Reason = ""
		check.evaluate(now)
		if check.Latched {
			return ErrDenied
		}
		l.Continuations = append(l.Continuations, Continuation{Prior: l.Binding, DecisionID: r.DecisionID, Owner: owner, At: now})
		if l.Escalation != nil {
			l.Escalations = append(l.Escalations, *l.Escalation)
		}
		l.Binding.Epoch++
		l.Latched = false
		l.Reason = ""
		l.Escalation = nil
		return nil
	})
}
