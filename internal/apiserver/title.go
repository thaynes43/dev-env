package apiserver

import (
	"context"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
)

const maxTitleBody = 4096

// setTitle serves PUT /v1/sessions/{name}/title (DESIGN-065 rules 8.1 and 8.2).
// It is the only writer of spec.title after create.
func (s *Server) setTitle(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return 0, nil, err
	}
	var req struct {
		Title  *string `json:"title"`
		Expect *string `json:"expect"`
	}
	if err := decodeJSON(w, r, maxTitleBody, &req, true); err != nil {
		return 0, nil, err
	}
	if req.Title == nil || req.Expect == nil {
		return 0, nil, invalid(fieldError("title", "title and expect are required"))
	}
	title := *req.Title
	if reason := sessiontitle.Validate(title); reason != "" {
		fe := fieldError("title", "%s", sessiontitle.Explain(reason))
		fe.Reason = reason
		return 0, nil, invalid(fe)
	}
	for range 5 {
		var sess v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &sess); err != nil {
			if c.kind == kindCoordinator {
				return 0, nil, coordinatorDenied()
			}
			return 0, nil, fromKubeError(err, "session "+key.Name)
		}
		if err := s.allowTitle(ctx, c, &sess); err != nil {
			return 0, nil, err
		}
		if !sess.DeletionTimestamp.IsZero() {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
				"session %s is being reaped: rescued, suspended and archived; a reap is final", sess.Name)
		}
		if sess.Spec.Caller != "" {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
				"session %s is summoned and takes no title", sess.Name)
		}
		if sess.Spec.Title != *req.Expect {
			return 0, nil, conflictReason("title_changed",
				"title_changed: the stored title is not the one this request read, so nothing was written")
		}
		if sess.Spec.Title == title {
			return http.StatusOK, view(&sess, false), nil
		}
		if at, ok := titleSetAt(sess.Annotations); ok && s.now().Sub(at) < time.Minute {
			return 0, nil, tooManyRequests("a session title can change once a minute")
		}
		orig := sess.DeepCopy()
		if sess.Annotations == nil {
			sess.Annotations = map[string]string{}
		}
		sess.Annotations[v1alpha1.AnnotationTitleSetBy] = c.String()
		sess.Annotations[v1alpha1.AnnotationTitleSetAt] = s.now().UTC().Format(time.RFC3339)
		sess.Spec.Title = title
		err := s.Client.Patch(ctx, &sess, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return 0, nil, fromKubeError(err, "session "+key.Name)
		}
		s.Log.Info("session title set", "session", sess.Name, "caller", c.String(), "titleHash", sessiontitle.HashPrefix(titleTarget(sess.Spec.Model, title, sess.Name)))
		return http.StatusOK, view(&sess, false), nil
	}
	return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s kept changing; try again", key.Name)
}

// titleTarget is the composed title, or the session name when the role text
// is cleared. The audit hash is of this string, never of a log line's text.
func titleTarget(model, title, name string) string {
	if title == "" {
		return name
	}
	return sessiontitle.Compose(model, title)
}

func titleSetAt(ann map[string]string) (time.Time, bool) {
	raw := ann[v1alpha1.AnnotationTitleSetAt]
	if raw == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// allowTitle is the access table of DESIGN-065 rule 8.2. The target itself
// is refused before any other session rule.
func (s *Server) allowTitle(ctx context.Context, c *caller, sess *v1alpha1.AgentSession) error {
	if c == nil {
		return forbidden("this caller may not set a session title")
	}
	if c.kind == kindSession && c.session != nil && c.session.Name == sess.Name {
		return forbidden("a session may not set its own title")
	}
	switch c.kind {
	case kindHuman, kindClient:
		return nil
	case kindSession:
		if s.isWorkbenchSession(c) {
			return nil
		}
		if c.session != nil && sess.Spec.Parent == c.session.Name {
			return nil
		}
		return forbidden("a session may set the title of its own children only")
	case kindCoordinator:
		if sess.Spec.Parent != c.parent || sess.UID == "" {
			return coordinatorDenied()
		}
		return s.authorizeChildMutation(ctx, sess, c)
	default:
		return forbidden("this caller may not set a session title")
	}
}
