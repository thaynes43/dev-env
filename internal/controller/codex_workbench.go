package controller

import (
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The same resolved profile controls admission and the account selected by the
// pod builder. It lives only for one reconcile, never on the AgentSession.
type codexHostBuild struct {
	policy   codexworkbench.Policy
	resolved *codexworkbench.Resolution
}

func (r *Reconciler) resolveCodexHost(s *v1alpha1.AgentSession, t *templates.Templates) *codexHostBuild {
	a := &codexHostBuild{policy: r.CodexWorkbenchHosts}
	if a.policy.Applies(s) {
		a.resolved = codexworkbench.Resolve(t, s.Spec.Profile)
	}
	return a
}

func (a *codexHostBuild) check(s *v1alpha1.AgentSession) error {
	if a == nil {
		return &codexworkbench.Refusal{Reason: codexworkbench.TemplatesUnavailable, Message: "Codex host build admission is required."}
	}
	return a.policy.Check(s, a.resolved)
}
