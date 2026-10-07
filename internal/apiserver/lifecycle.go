package apiserver

import (
	"context"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Suspend and resume (DESIGN-001 3.4, D-60). Each sets spec.operatingMode, the
// one spec field a client changes (D-39), and the reconciler does the rest:
// a suspend rescues the pod and deletes it, keeping the volume (D-51); a
// resume marks the last rescue superseded and starts a new pod on the volume,
// whose agentd resumes the conversation (D-58). Any caller may suspend or
// resume any session, as any caller may reap one (D-46): no work is lost
// either way.

func (s *Server) suspendSession(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	return s.setOperatingMode(ctx, r, c, v1alpha1.OperatingModeSuspended)
}

func (s *Server) resumeSession(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	return s.setOperatingMode(ctx, r, c, v1alpha1.OperatingModeRunning)
}

// setOperatingMode patches the session's operatingMode with its
// resourceVersion as a precondition, retrying a few times when the operator's
// own writes race it. It answers 200 when the session is already in that
// mode, and 202 once the change is made. A session being reaped is a 409: a
// reap is final.
func (s *Server) setOperatingMode(ctx context.Context, r *http.Request, c *caller, mode v1alpha1.OperatingMode) (int, any, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return 0, nil, err
	}
	for range 5 {
		var sess v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &sess); err != nil {
			return 0, nil, fromKubeError(err, "session "+key.Name)
		}
		if !sess.DeletionTimestamp.IsZero() {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
				"session %s is being reaped: rescued, suspended and archived (D-45); a reap is final", sess.Name)
		}
		if mode == v1alpha1.OperatingModeRunning && sess.Status.ArchivedAt != nil {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict,
				"session %s was archived at %s: its volume is gone, so a resume would start its task again on a new one; restore it from its bundle instead (D-62)",
				sess.Name, sess.Status.ArchivedAt.UTC().Format("2006-01-02T15:04:05Z"))
		}
		cur := sess.Spec.OperatingMode
		if cur == "" {
			cur = v1alpha1.OperatingModeRunning
		}
		if cur == mode {
			return http.StatusOK, view(&sess, false), nil
		}
		orig := sess.DeepCopy()
		sess.Spec.OperatingMode = mode
		if mode == v1alpha1.OperatingModeSuspended {
			if sess.Annotations == nil {
				sess.Annotations = map[string]string{}
			}
			sess.Annotations[v1alpha1.AnnotationSuspendedBy] = c.String()
		} else {
			delete(sess.Annotations, v1alpha1.AnnotationSuspendedBy)
			if sess.Annotations == nil {
				sess.Annotations = map[string]string{}
			}
			sess.Annotations[v1alpha1.AnnotationResumedAt] = s.now().UTC().Format(time.RFC3339)
		}
		err := s.Client.Patch(ctx, &sess, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return 0, nil, fromKubeError(err, "session "+key.Name)
		}
		s.Log.Info("session operating mode set", "session", sess.Name, "mode", mode, "caller", c.String())
		return http.StatusAccepted, view(&sess, false), nil
	}
	return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s kept changing; try again", key.Name)
}
