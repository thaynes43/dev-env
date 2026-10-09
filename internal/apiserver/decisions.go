package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

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
	cmd := []string{"agentd", "ctl", "decision-read"}
	var stdin io.Reader
	if input != nil {
		cmd[2] = "decision-answer"
		body, err := json.Marshal(input)
		if err != nil {
			return 0, nil, internal("decision answer encoding failed")
		}
		stdin = bytes.NewReader(body)
	}
	if err := s.authorizeChildExec(ctx, sess, pod, c); err != nil {
		return 0, nil, err
	}
	stdout := &decisionOutput{limit: protocol.MaxDecisionRecordBytes}
	err = s.Exec.Run(ctx, pod.Namespace, pod.Name, controller.ContainerName, coordinatorExecArgs(cmd, sess, pod, c), stdin, stdout, io.Discard)
	if err != nil || stdout.overflow {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "exact child decision is unavailable or uncertain")
	}
	var result protocol.DecisionResult
	d := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF {
		return 0, nil, internal("child decision response is invalid")
	}
	if input != nil && result.Decision == nil {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "child decision answer recording is unconfirmed")
	}
	if decision := result.Decision; decision != nil {
		if decision.Validate() != nil || decision.Session != sess.Name || decision.SessionUID != string(sess.UID) || decision.PodUID != string(pod.UID) ||
			(input != nil && (decision.ID != input.ID || decision.Answer != input.Text || decision.State == "Open")) {
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "child decision identity changed")
		}
	}
	status := http.StatusOK
	if input != nil {
		status = http.StatusAccepted
	}
	return status, result, nil
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
