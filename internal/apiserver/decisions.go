package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// A decision route is available only to the live configured direct parent.
// The API supplies both target UIDs; the private target checks them before
// reading question context or recording an answer.
func (s *Server) childDecision(ctx context.Context, r *http.Request, c *caller, input *protocol.DecisionAnswer) (int, any, error) {
	if !s.ManagedChildDecisions || c.kind != kindCoordinator {
		return 0, nil, coordinatorDenied()
	}
	sess, err := s.liveSession(ctx, r, c)
	if err != nil {
		return 0, nil, err
	}
	if sess.Spec.Agent != v1alpha1.AgentCodex || sess.Spec.Mode != v1alpha1.ModeTask || sess.Spec.Workspace == nil {
		return 0, nil, coordinatorDenied()
	}
	pod, err := s.runningPod(ctx, sess)
	if err != nil {
		return 0, nil, err
	}
	if s.Exec == nil {
		return 0, nil, newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "decision transport is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	result, err := s.execChildDecision(ctx, sess, pod, c, nil)
	if err != nil || input == nil {
		return http.StatusOK, result, err
	}
	if result.Decision == nil || result.Decision.ID != input.ID {
		return 0, nil, decisionConflict("child decision answer recording is unconfirmed")
	}
	record := result.Decision
	authority := protocol.AnswerAuthority(*record, input.Text)
	if prior := sess.Status.DecisionAnswer; prior != nil {
		saved := protocol.DecisionAuthority(*prior)
		if saved.Phase != "Confirmed" {
			return 0, nil, decisionConflict("prior decision answer reservation is uncertain; no replay")
		}
		if saved.DecisionID == record.ID {
			if saved != authority || !saved.Confirms(*record) {
				return 0, nil, decisionConflict("recorded answer differs from confirmed authority")
			}
			return http.StatusAccepted, result, nil
		}
	}
	if record.State != "Open" {
		return 0, nil, decisionConflict("local answer has no coordinator authority")
	}
	authority.Phase = "Reserved"
	if err := s.writeDecisionAuthority(ctx, sess, pod, c, nil, authority); err != nil {
		return 0, nil, err
	}
	// A reservation is never repeated after an unknown exec or response. The local
	// daemon cannot dispatch it until the operator confirms the durable response.
	result, err = s.execChildDecision(ctx, sess, pod, c, input)
	if err != nil {
		return 0, nil, err
	}
	if result.Decision == nil || !protocol.AnswerAuthority(*result.Decision, result.Decision.Answer).Confirms(*result.Decision) ||
		result.Decision.ID != input.ID || result.Decision.Answer != input.Text ||
		protocol.AnswerAuthority(*result.Decision, input.Text).Digest != authority.Digest {
		return 0, nil, decisionConflict("child decision answer recording is unconfirmed")
	}
	expected := authority
	authority.Phase = "Confirmed"
	if err := s.writeDecisionAuthority(ctx, sess, pod, c, &expected, authority); err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, result, nil
}

func decisionConflict(message string) error {
	return newError(http.StatusConflict, apiv1.CodeConflict, "%s", message)
}

func (s *Server) execChildDecision(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, c *caller, input *protocol.DecisionAnswer) (protocol.DecisionResult, error) {
	var result protocol.DecisionResult
	cmd := []string{"agentd", "ctl", "decision-read"}
	var stdin io.Reader
	if input != nil {
		cmd[2] = "decision-answer"
		body, _ := json.Marshal(input)
		stdin = bytes.NewReader(body)
	}
	if err := s.authorizeChildExec(ctx, sess, pod, c); err != nil {
		return result, err
	}
	stdout := &decisionOutput{limit: protocol.MaxDecisionRecordBytes}
	err := s.Exec.Run(ctx, pod.Namespace, pod.Name, controller.ContainerName, coordinatorExecArgs(cmd, sess, pod, c), stdin, stdout, io.Discard)
	if err != nil || stdout.overflow {
		return result, decisionConflict("exact child decision is unavailable or uncertain")
	}
	d := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF {
		return result, internal("child decision response is invalid")
	}
	if r := result.Decision; r != nil && (r.Validate() != nil || r.Session != sess.Name || r.SessionUID != string(sess.UID) || r.PodUID != string(pod.UID)) {
		return protocol.DecisionResult{}, decisionConflict("child decision identity changed")
	}
	return result, nil
}

// The optimistic status write, uncached readback and direct-parent recheck
// precede every exec/confirmation. Unrelated status writes may change the old
// snapshot RV; compare only the expected authority, then patch from fresh live
// state. No caller-provided heartbeat writes this field.
func (s *Server) writeDecisionAuthority(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, c *caller, expected *protocol.DecisionAuthority, next protocol.DecisionAuthority) error {
	if err := s.liveDecisionCoordinator(ctx, c); err != nil {
		return err
	}
	var live v1alpha1.AgentSession
	if err := s.Live.Get(ctx, client.ObjectKeyFromObject(sess), &live); err != nil {
		return fromKubeError(err, "decision authority")
	}
	if live.UID != sess.UID || live.Generation != sess.Generation {
		return coordinatorDenied()
	}
	if expected != nil {
		if live.Status.DecisionAnswer == nil || protocol.DecisionAuthority(*live.Status.DecisionAnswer) != *expected {
			return decisionConflict("decision authority changed")
		}
	} else {
		prior, current := sess.Status.DecisionAnswer, live.Status.DecisionAnswer
		if (prior == nil) != (current == nil) || (prior != nil && *prior != *current) || (current != nil && current.Phase != "Confirmed") {
			return decisionConflict("decision authority changed or is already reserved")
		}
	}
	*sess = live
	if err := s.authorizeChildExec(ctx, sess, pod, c); err != nil {
		return err
	}
	base := sess.DeepCopy()
	converted := v1alpha1.DecisionAnswerStatus(next)
	sess.Status.DecisionAnswer = &converted
	if err := s.Client.Status().Patch(ctx, sess, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fromKubeError(err, "decision authority write")
	}
	if err := s.Live.Get(ctx, client.ObjectKeyFromObject(sess), &live); err != nil {
		return fromKubeError(err, "decision authority confirmation")
	}
	if live.UID != sess.UID || live.Generation != sess.Generation || live.Status.DecisionAnswer == nil || protocol.DecisionAuthority(*live.Status.DecisionAnswer) != next {
		return decisionConflict("decision authority write is unconfirmed")
	}
	*sess = live
	if err := s.liveDecisionCoordinator(ctx, c); err != nil {
		return err
	}
	return s.authorizeChildExec(ctx, sess, pod, c)
}

// Recheck the configured parent's own live Pod at the authority write/readback,
// rather than relying on the TokenReview's earlier classification alone.
func (s *Server) liveDecisionCoordinator(ctx context.Context, c *caller) error {
	if !s.ManagedChildDecisions || !s.Policy.CoordinatorEnabled || c.kind != kindCoordinator {
		return coordinatorDenied()
	}
	for _, host := range s.Policy.Coordinators {
		if host.ServiceAccount == c.parent {
			if _, err := s.resolveCoordinator(ctx, c.identity, host); err != nil {
				return err
			}
			return nil
		}
	}
	return coordinatorDenied()
}

// This narrowly scoped read needs no Session status-write grant and never
// returns private question/answer text. Both identities are freshly rechecked.
func (s *Server) ownDecisionAuthority(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return 0, nil, err
	}
	if !s.ManagedChildDecisions || c.kind != kindSession || c.session.Name != key.Name || c.pod == nil {
		return 0, nil, forbidden("only this decision task's own pod may read answer authority")
	}
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, key, &sess); err != nil {
		return 0, nil, fromKubeError(err, "decision authority")
	}
	pod, err := s.runningPod(ctx, &sess)
	if err != nil {
		return 0, nil, err
	}
	if !sess.DeletionTimestamp.IsZero() || pod.Spec.ServiceAccountName != s.Policy.SessionServiceAccount || sess.UID != c.session.UID || pod.UID != c.pod.UID || string(pod.UID) != c.identity.PodUID || sess.Spec.Agent != v1alpha1.AgentCodex || sess.Spec.Mode != v1alpha1.ModeTask || sess.Spec.Workspace == nil {
		return 0, nil, forbidden("decision authority identity changed")
	}
	result := protocol.DecisionAuthorityResult{}
	if sess.Status.DecisionAnswer != nil {
		authority := protocol.DecisionAuthority(*sess.Status.DecisionAnswer)
		result.Authority = &authority
	}
	return http.StatusOK, result, nil
}

func (s *Server) readChildDecision(ctx context.Context, _ http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	return s.childDecision(ctx, r, c, nil)
}

func (s *Server) answerChildDecision(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	var answer protocol.DecisionAnswer
	if err := decodeJSON(w, r, protocol.MaxDecisionRecordBytes, &answer, true); err != nil {
		return 0, nil, err
	}
	if answer.Validate() != nil {
		return 0, nil, invalid(fieldError("answer", "invalid decision identity or bounded text"))
	}
	return s.childDecision(ctx, r, c, &answer)
}

// Unlike log tails, decision transport refuses overflow rather than truncating
// question context or turning a partial JSON document into a valid response.
type decisionOutput struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *decisionOutput) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *decisionOutput) Bytes() []byte { return b.buf.Bytes() }
