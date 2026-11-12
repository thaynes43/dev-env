package apiserver

import (
	"context"
	"net/http"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// reportOutcome is exclusively the summoned session's own pod-bound report.
// resolveSession checks the token/pod/controller chain; every live read below
// repeats the heartbeat's Session UID fence, including after a CAS conflict.
func (s *Server) reportOutcome(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return 0, nil, err
	}
	if c.kind != kindSession || c.session == nil || c.pod == nil || c.session.Name != key.Name || c.session.Spec.Caller == "" {
		return 0, nil, forbidden("only session %s's own pod may report its outcome", key.Name)
	}
	var req apiv1.ReportOutcomeRequest
	if err := decodeJSON(w, r, 16<<10, &req, true); err != nil {
		return 0, nil, err
	}
	var state v1alpha1.OutcomeState
	switch req.State {
	case "working":
		state = v1alpha1.OutcomeRunning
	case "done":
		state = v1alpha1.OutcomeDone
	case "failed":
		state = v1alpha1.OutcomeFailed
	case "escalate":
		state = v1alpha1.OutcomeEscalated
	default:
		return 0, nil, invalid(fieldError("state", "must be working, done, failed or escalate"))
	}
	// Retry only a typed optimistic conflict, at most three writes. A fresh
	// terminal winner returns its stored outcome immediately, without another write.
	for attempt := 0; attempt <= 3; attempt++ {
		var sess v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &sess); err != nil {
			return 0, nil, fromKubeError(err, "session "+key.Name)
		}
		if sess.UID != c.session.UID {
			return 0, nil, forbidden("session %s was replaced; the pod belongs to the earlier one", key.Name)
		}
		if sess.Spec.Caller == "" {
			return 0, nil, forbidden("session %s was not summoned by a CallerPolicy", key.Name)
		}
		next := summonoutcome.Decision{State: state, Note: req.Note, Source: summonoutcome.Report, Reason: req.State}
		if req.State == "failed" && sess.Spec.Lane == v1alpha1.LaneRemediation {
			next.State = v1alpha1.OutcomeEscalated
			next.Note = summonoutcome.FailedPrefix + req.Note
		}
		for _, ch := range next.Note {
			if unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch) {
				return 0, nil, invalid(fieldError("note", "must not contain control or format characters (C0, C1, DEL or Unicode Cf)"))
			}
		}
		if len(next.Note) > summonoutcome.MaxNoteBytes {
			return 0, nil, invalid(fieldError("note", "stored note must be at most 1024 bytes (1007 before a remediation failed prefix)"))
		}
		if summonoutcome.Terminal(sess.Status.Outcome) {
			code := http.StatusConflict
			if sess.Status.Summon != nil && sess.Status.Summon.Closed != nil && sess.Status.Summon.Closed.Source == summonoutcome.Report && sess.Status.Outcome.State == next.State && sess.Status.Outcome.Note == next.Note {
				code = http.StatusOK
			}
			return code, outcomeView(sess.Status.Outcome), nil
		}
		if attempt == 3 {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "outcome write kept conflicting; read the session before retrying")
		}
		if _, err := summonoutcome.Write(ctx, s.Client, &sess, next, s.now()); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return 0, nil, fromKubeError(err, "session "+key.Name+" outcome")
		}
		return http.StatusOK, outcomeView(sess.Status.Outcome), nil
	}
	panic("unreachable outcome retry")
}

func outcomeView(o *v1alpha1.OutcomeStatus) *apiv1.Outcome {
	if o == nil {
		return nil
	}
	return &apiv1.Outcome{State: string(o.State), Note: o.Note, At: timeOf(o.At)}
}
