package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/templates"
)

// ScheduledWorkParent is the spec.parent of a scheduled session dispatched on
// a human basis (the human owner, or a human approval of the exact spec): the
// operator's fixed client identity for scheduled work, which logs as
// client/dev-env-system/scheduled-work (DESIGN-054 "Authority"). It is in the
// "<namespace>/<name>" form a client's parent takes, like StandbyParent.
const ScheduledWorkParent = "dev-env-system/scheduled-work"

// CreateScheduledSession is the fixed-name creation entry of DESIGN-054's
// executor (S3b): it creates the session of one occurrence, named
// scheduledwork.SessionName(d.ItemUID, d.At), through the same admission as
// POST /v1/sessions (createSessionMode): the retained-executor refusal,
// newSession, the instruction image and private project gates, the named
// coordinator rule, a session caller's depth and child cap, and agentd's own
// check (controller.CheckAgentdSession, through checkAgentd). It sets, in the
// one atomic Create, the finalizer v1alpha1.ScheduledWorkFinalizer, the label
// v1alpha1.LabelScheduledWork (the item's full UID) and the annotations of the
// item's name, the occurrence (Unix seconds) and the start deadline (RFC 3339
// UTC), so the session can never vanish before its occurrence is recorded.
//
// A session of the derived name that already exists comes first, as a repeated
// idempotency key does: one that carries this occurrence's label and
// annotations is adopted (adopted true), so a create whose response was lost,
// or a second leader's create, gets the same session back even when that
// session is the one that now fills a limit. One that does not is never
// adopted: the error wraps scheduledwork.ErrCollision. A definite refusal of
// the checks is a *scheduledwork.RefusedError; any other error is a failed read
// or write, and nothing is known to have been created.
//
// With d.Owner nil the caller is the fixed client ScheduledWorkParent, at depth
// 0. With d.Owner set it is that live owner session, resolved from its own pod
// as resolveCaller resolves the pod's projected token, so the session is the
// owner's child, on the owner's profile, within the owner's depth and child
// cap, exactly as if the owner created it now. A scheduled session never runs
// as a named coordinator identity, carries no idempotency key, restore,
// project, canary or coordinator instructions, and is never charged to a task
// budget: a budget-bound caller is refused rather than dispatched outside its
// reservation.
func (s *Server) CreateScheduledSession(ctx context.Context, req apiv1.CreateSessionRequest, d scheduledwork.Dispatch) (name string, adopted bool, err error) {
	name, err = scheduledwork.SessionName(string(d.ItemUID), d.At)
	if err != nil {
		return "", false, &scheduledwork.RefusedError{Reason: err.Error()}
	}
	switch {
	case d.ItemName == "":
		return "", false, &scheduledwork.RefusedError{Reason: "the ScheduledWork's name is empty"}
	case !d.StartDeadline.After(d.At):
		return "", false, &scheduledwork.RefusedError{Reason: "the start deadline is not after the occurrence"}
	case req.IdempotencyKey != "" || req.Restore != "" || req.Project != "" || req.CanaryID != "" ||
		req.CoordinatorInstructions || req.CoordinatorInstructionPublisher:
		return "", false, &scheduledwork.RefusedError{Reason: "a scheduled session takes no idempotency key, restore, project, canary or coordinator instructions: its derived name is its key"}
	}
	if s.Live == nil {
		return "", false, errors.New("the API has no uncached reader")
	}

	s.createMu.Lock()
	defer s.createMu.Unlock()

	if existing, err := s.scheduledSession(ctx, name); err != nil || existing != nil {
		if err != nil {
			return "", false, err
		}
		return adoptScheduledSession(existing, d)
	}
	c, err := s.scheduledCaller(ctx, d)
	if err != nil {
		return "", false, scheduledRefusal(err)
	}
	if err := refuseRetainedExecutor(c); err != nil {
		return "", false, scheduledRefusal(err)
	}
	sess, err := s.newSession(ctx, req, c)
	if err != nil {
		return "", false, scheduledRefusal(err)
	}
	if sess.Labels[templates.CoordinatorIdentityLabel] != "" {
		return "", false, &scheduledwork.RefusedError{Reason: fmt.Sprintf("profile %s runs as a named coordinator identity; a scheduled session never does", sess.Spec.Profile)}
	}
	if s.callerHasTaskBudget(c) {
		return "", false, &scheduledwork.RefusedError{Reason: fmt.Sprintf("%s is bound to a task budget; scheduled work is not dispatched outside its reservation", c)}
	}
	sess.Name = name
	controllerutil.AddFinalizer(sess, v1alpha1.ScheduledWorkFinalizer)
	sess.Labels[v1alpha1.LabelScheduledWork] = string(d.ItemUID)
	if sess.Annotations == nil {
		sess.Annotations = map[string]string{}
	}
	sess.Annotations[v1alpha1.AnnotationScheduledWorkName] = d.ItemName
	sess.Annotations[v1alpha1.AnnotationScheduledWorkOccurrence] = scheduledwork.OccurrenceAnnotation(d.At)
	sess.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = scheduledwork.StartDeadlineAnnotation(d.StartDeadline)
	if err := s.admitCreate(ctx, c, sess); err != nil {
		return "", false, scheduledRefusal(err)
	}
	if err := checkAgentd(sess); err != nil {
		return "", false, scheduledRefusal(err)
	}
	err = s.Client.Create(ctx, sess)
	if apierrors.IsAlreadyExists(err) {
		existing, err := s.scheduledSession(ctx, name)
		switch {
		case err != nil:
			return "", false, err
		case existing == nil:
			return "", false, fmt.Errorf("session %s already existed at the create and is gone now", name)
		}
		return adoptScheduledSession(existing, d)
	}
	if err != nil {
		return "", false, scheduledRefusal(fromKubeError(err, "create the scheduled session"))
	}
	s.Log.Info("scheduled session created", "session", sess.Name, "caller", c.String(), "scheduledWork", d.ItemName,
		"occurrence", d.At.UTC().Format("2006-01-02T15:04:05Z"), "repo", sess.Spec.Repo, "agent", sess.Spec.Agent,
		"mode", sess.Spec.Mode, "model", sess.Spec.Model, "depth", c.depth)
	return name, false, nil
}

// scheduledSession reads the session of that name from the API server; nil when
// there is none.
func (s *Server) scheduledSession(ctx context.Context, name string) (*v1alpha1.AgentSession, error) {
	var sess v1alpha1.AgentSession
	err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: name}, &sess)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read session %s: %w", name, err)
	}
	return &sess, nil
}

// adoptScheduledSession returns an existing session of the derived name when
// it carries this occurrence's label and annotations, and a collision
// otherwise.
func adoptScheduledSession(existing *v1alpha1.AgentSession, d scheduledwork.Dispatch) (string, bool, error) {
	if !scheduledwork.CreatedFor(existing, d.ItemName, d.ItemUID, d.At) {
		return "", false, fmt.Errorf("session %s: %w", existing.Name, scheduledwork.ErrCollision)
	}
	return existing.Name, true, nil
}

// scheduledCaller is who a scheduled session is created as: the fixed client
// for scheduled work, or the live owner session d.Owner as its own pod's
// token would resolve (resolveCaller), so every rule that applies to the
// owner's own create applies here.
func (s *Server) scheduledCaller(ctx context.Context, d scheduledwork.Dispatch) (*caller, error) {
	if d.Owner == nil {
		return &caller{kind: kindClient, parent: ScheduledWorkParent}, nil
	}
	ns := s.Policy.SessionNamespace
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: ns, Name: d.Owner.Name}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, forbidden("the owner session %s has no pod, so it is not live", d.Owner.Name)
		}
		return nil, fmt.Errorf("read the owner session's pod: %w", err)
	}
	id := Identity{
		Username:  serviceAccountPrefix + pod.Namespace + ":" + pod.Spec.ServiceAccountName,
		Namespace: pod.Namespace, ServiceAccount: pod.Spec.ServiceAccountName,
		PodName: pod.Name, PodUID: string(pod.UID),
	}
	c, err := s.resolveCaller(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.kind != kindSession || c.session == nil || c.session.Name != d.Owner.Name || c.session.UID != d.Owner.UID {
		return nil, forbidden("pod %s does not speak for the owner session %s", pod.Name, d.Owner.Name)
	}
	return c, nil
}

// scheduledRefusal turns a definite refusal of the creation checks (a 4xx of
// the API) into a *scheduledwork.RefusedError; any other error is returned as
// it is, a failed read or write the executor tries again.
func scheduledRefusal(err error) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.status >= http.StatusBadRequest && ae.status < http.StatusInternalServerError {
		return &scheduledwork.RefusedError{Reason: ae.body.Message}
	}
	return err
}
