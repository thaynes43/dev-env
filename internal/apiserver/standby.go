package apiserver

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// StandbyParent is the spec.parent of the standby session: the operator's own
// identity, in the "<namespace>/<name>" form a client's parent takes.
const StandbyParent = "dev-env-system/dev-env-operator-standby"

// CreateStandby creates the operator's standby remote session (Story110 T25).
// It takes the API's own creation path, newSession's checks and the numbered
// name, as a trusted client at depth 0, so the result is an ordinary
// AgentSession and not a second kind. It adds LabelStandby, which is how the
// standby runnable finds it again, and returns the session's name. It does no
// idempotency: the runnable's label lookup and circuit breaker decide whether
// to call.
func (s *Server) CreateStandby(ctx context.Context, req apiv1.CreateSessionRequest) (string, error) {
	c := &caller{kind: kindClient, parent: StandbyParent}
	sess, err := s.newSession(ctx, req, c)
	if err != nil {
		return "", err
	}
	// The standby never runs as a named coordinator identity (DESIGN-042 6.4):
	// the operator recreates it on its own, and the lead is launched by Tom.
	if sess.Labels[templates.CoordinatorIdentityLabel] != "" {
		return "", forbidden("the standby's profile %s runs as a named coordinator identity; the standby never does", sess.Spec.Profile)
	}
	sess.Labels[v1alpha1.LabelStandby] = "true"

	s.createMu.Lock()
	defer s.createMu.Unlock()
	base := generatedName(sess.Spec.Repo, s.now())
	for i := 1; i <= nameAttempts; i++ {
		sess.Name = base
		if i > 1 {
			sess.Name = fmt.Sprintf("%s-%d", base, i)
		}
		if err := checkAgentd(sess); err != nil {
			return "", err
		}
		err := s.Client.Create(ctx, sess)
		if apierrors.IsAlreadyExists(err) {
			sess.ResourceVersion = ""
			continue
		}
		if err != nil {
			return "", fromKubeError(err, "create the standby session")
		}
		s.Log.Info("standby session created", "session", sess.Name, "repo", sess.Spec.Repo,
			"agent", sess.Spec.Agent, "mode", sess.Spec.Mode, "model", sess.Spec.Model)
		return sess.Name, nil
	}
	return "", internal("no free session name after %s-%d", base, nameAttempts)
}
