package apiserver

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Length caps on what a heartbeat may write into status. agentd's values are
// far shorter; the caps keep a confused pod from bloating its object.
const (
	maxShort    = 128
	maxMessage  = 1024
	maxProblems = 16
	maxProblem  = 256
)

// heartbeat serves POST /v1/sessions/{name}/heartbeat (D-41): agentd's status,
// from the session's own pod only. It copies the status into status.agent and
// status.usage and answers 204.
func (s *Server) heartbeat(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	key, err := s.sessionKey(r)
	if err != nil {
		return 0, nil, err
	}
	if c.kind != kindSession || c.session.Name != key.Name {
		return 0, nil, forbidden("only session %s's own pod may post its heartbeat", key.Name)
	}
	var st protocol.Status
	if err := decodeJSON(w, r, maxHeartbeatBody, &st, false); err != nil {
		return 0, nil, err
	}
	if st.Session != key.Name {
		return 0, nil, invalid(fieldError("session", "%q is not %s, the session in the path", st.Session, key.Name))
	}

	// The base is read from the API server, so the patch clears exactly what
	// the last heartbeat set and this one does not.
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, key, &sess); err != nil {
		return 0, nil, fromKubeError(err, "session "+key.Name)
	}
	if sess.UID != c.session.UID {
		return 0, nil, forbidden("session %s was replaced; the pod belongs to the earlier one", key.Name)
	}
	base := sess.DeepCopy()
	decisionOutcomePatch := false
	if s.ManagedChildDecisions && sess.Spec.Agent == v1alpha1.AgentCodex && sess.Spec.Mode == v1alpha1.ModeTask && sess.Spec.Workspace != nil {
		if outcome := st.Decision; outcome != nil {
			if c.pod == nil || outcome.SessionUID != string(sess.UID) || outcome.PodUID != string(c.pod.UID) || outcome.WriterGeneration == 0 ||
				!protocol.ValidDecisionID(outcome.ID) || outcome.At.IsZero() {
				return 0, nil, invalid(fieldError("decision", "decision outcome must bind this exact session and pod"))
			}
			observedAt := outcome.At
			if observedAt.After(s.now()) {
				observedAt = s.now()
			}
			note := "decision/" + outcome.ID
			if sess.Status.Outcome == nil || sess.Status.Outcome.Note == note {
				decisionOutcomePatch = true
				at := metav1.NewTime(observedAt)
				sess.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, Note: note, At: &at}
				if answer := sess.Status.DecisionAnswer; answer != nil && answer.Phase == "Confirmed" &&
					answer.Session == sess.Name && answer.SessionUID == outcome.SessionUID && answer.PodUID == outcome.PodUID &&
					answer.DecisionID == outcome.ID && answer.ThreadID == outcome.ThreadID && outcome.WriterGeneration <= math.MaxInt64 && answer.WriterGeneration == int64(outcome.WriterGeneration) &&
					answer.Digest == outcome.AnswerDigest && outcome.State == "Delivered" {
					sess.Status.Outcome = nil
				}
			}

		} // Omission cannot erase a pending or locally fabricated decision.

	}
	sess.Status.Agent = agentStatus(st, metav1.NewTime(s.now()))
	if st.Usage != nil {
		sess.Status.Usage = usageStatus(*st.Usage)
	}
	patch := client.MergeFrom(base)
	// An outcome acquired by another writer after this read must not be replaced
	// or cleared by a stale decision heartbeat. Ordinary heartbeat fields keep
	// their existing merge behavior when no decision outcome is being changed.
	if decisionOutcomePatch {
		patch = client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	}
	if err := s.Client.Status().Patch(ctx, &sess, patch); err != nil {
		return 0, nil, fromKubeError(err, "session "+key.Name+" status")
	}
	return http.StatusNoContent, nil, nil
}

// agentStatus is status.agent from a heartbeat, received at now.
func agentStatus(st protocol.Status, now metav1.Time) *v1alpha1.AgentStatus {
	a := &v1alpha1.AgentStatus{
		Status:         clip(st.Agent.State, maxShort),
		LastHeartbeat:  &now,
		Agentd:         clip(st.Agentd, maxShort),
		Boot:           clip(st.Boot, maxShort),
		ConversationID: clip(st.Agent.ConversationID, maxShort),
		Message:        clip(st.Agent.Error, maxMessage),
	}
	if t := st.Agent.LastActivity; t != nil && !t.IsZero() {
		lt := metav1.NewTime(*t)
		a.LastActivity = &lt
	}
	if ws := st.Workspace; ws != nil {
		a.Branch = clip(ws.Branch, maxShort*2)
		a.Head = clip(ws.Head, maxShort)
	}
	for _, p := range st.Problems {
		if len(a.Problems) == maxProblems {
			break
		}
		line := p.Name + ": " + p.State
		if len(p.Notes) > 0 {
			line += ": " + p.Notes[0]
		}
		a.Problems = append(a.Problems, clip(line, maxProblem))
	}
	if t := st.Agent.Task; t != nil {
		a.Task = &v1alpha1.TaskStatus{
			ExitCode:   clampInt32(int64(t.ExitCode)),
			FinishedAt: metav1.NewTime(t.FinishedAt),
			TimedOut:   t.TimedOut,
			Subtype:    clip(t.Subtype, maxShort),
			IsError:    t.IsError,
			NumTurns:   clampInt32(int64(t.NumTurns)),
		}
	}
	return a
}

// usageStatus is status.usage from agentd's cost record (V-16). The cost is a
// decimal string because CRDs avoid floats.
func usageStatus(u protocol.Usage) *v1alpha1.UsageStatus {
	cost := u.CostUSD
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		cost = 0
	}
	return &v1alpha1.UsageStatus{
		CostUSD:      strconv.FormatFloat(cost, 'f', -1, 64),
		InputTokens:  max(u.InputTokens, 0),
		OutputTokens: max(u.OutputTokens, 0),
	}
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s) + "…"
}

func clampInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}
	return int32(v)
}
