package apiserver

import (
	"context"
	"errors"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/codexworkbench"
	"github.com/thaynes43/dev-env/internal/templates"
)

type codexHostAdmissionKey struct{}
type codexHostAdmission struct {
	profiles  map[string]*codexworkbench.Resolution
	loaded    bool
	templates *templates.Templates
}

func withCodexHostAdmission(ctx context.Context) context.Context {
	if _, ok := ctx.Value(codexHostAdmissionKey{}).(*codexHostAdmission); ok {
		return ctx
	}
	return context.WithValue(ctx, codexHostAdmissionKey{}, &codexHostAdmission{profiles: make(map[string]*codexworkbench.Resolution)})
}

// codexHostProfile pins the account answer once per effective profile per HTTP
// request, including final admission and optimistic resume patch retries.
func (s *Server) codexHostProfile(ctx context.Context, profile string) *codexworkbench.Resolution {
	memo, ok := ctx.Value(codexHostAdmissionKey{}).(*codexHostAdmission)
	if !ok {
		memo = &codexHostAdmission{profiles: make(map[string]*codexworkbench.Resolution)}
	}
	if r, ok := memo.profiles[profile]; ok {
		return r
	}
	if !memo.loaded {
		memo.loaded = true
		if s.Templates != nil {
			t, err := s.Templates(ctx)
			if err == nil {
				memo.templates = t
			}
		}
	}
	r := codexworkbench.Resolve(memo.templates, profile)
	memo.profiles[profile] = r
	return r
}

func (s *Server) admitCodexHost(ctx context.Context, sess *v1alpha1.AgentSession, resolved *codexworkbench.Resolution, create bool) error {
	p := s.CodexWorkbenchHosts
	if !p.Applies(sess) {
		return nil
	}
	if resolved == nil {
		resolved = s.codexHostProfile(ctx, sess.Spec.Profile)
	}
	guardErr := p.Check(sess, resolved)
	// API creation is always git-only, even for a trusted caller and even
	// if the eventual name could match. T30 runs before this at newSession.
	if create && codexworkbench.IsHost(sess) && resolved.Workbench() {
		return newError(http.StatusForbidden, apiv1.CodeCodexWorkbenchHostGitOnly, "A Codex host on the workbench account must be named by the operator and declared in git.")
	}
	if err := guardErr; err != nil {
		var refusal *codexworkbench.Refusal
		if errors.As(err, &refusal) {
			status, code := http.StatusForbidden, apiv1.CodeForbidden
			if refusal.Reason == codexworkbench.TemplatesUnavailable {
				status, code = http.StatusServiceUnavailable, apiv1.CodeUnavailable
			}
			e := newError(status, code, "%s", refusal.Message)
			e.body.Reason = refusal.Reason
			return e
		}
		return err
	}
	return nil
}

// createAdmittedSession is the final write boundary for every API-created
// AgentSession, including fixed-name canaries, standby and summoned dispatch.
func (s *Server) createAdmittedSession(ctx context.Context, sess *v1alpha1.AgentSession, opts ...client.CreateOption) error {
	if err := s.admitCodexHost(ctx, sess, nil, true); err != nil {
		return err
	}
	return s.Client.Create(ctx, sess, opts...)
}
