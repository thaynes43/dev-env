package apiserver

import (
	"context"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// Length caps on what a heartbeat may write into status. agentd's values are
// far shorter; the caps keep a confused pod from bloating its object.
const (
	maxShort    = 128
	maxMessage  = 1024
	maxProblems = 16
	maxProblem  = 256
)

// agentStartSkew is how far an agent-reported start may sit ahead of the
// operator clock and still be stored. A later report is not a substitute
// for the agent's own time.
const agentStartSkew = 5 * time.Second

// remoteControlSessionIDPattern is apiv1.RemoteControlSessionIDPattern.
var remoteControlSessionIDPattern = regexp.MustCompile(apiv1.RemoteControlSessionIDPattern)

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
	if rc := st.RemoteControl; rc != nil && rc.SessionID != "" &&
		(len(rc.SessionID) > apiv1.RemoteControlSessionIDMaxLength || !remoteControlSessionIDPattern.MatchString(rc.SessionID)) {
		return 0, nil, invalid(fieldError("remoteControl.sessionId", "must match %s and contain at most %d characters", apiv1.RemoteControlSessionIDPattern, apiv1.RemoteControlSessionIDMaxLength))
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
			prior, authority := sess.Status.Outcome, sess.Status.DecisionAnswer
			// A new question from this same writer may supersede its earlier
			// escalation even if the earlier Delivered heartbeat was not observed.
			// Only the operator's confirmed authority permits that transition.
			supersedesConfirmed := prior != nil && prior.State == v1alpha1.OutcomeEscalated && authority != nil && authority.Phase == "Confirmed" &&
				prior.Note == "decision/"+authority.DecisionID && authority.DecisionID != outcome.ID &&
				authority.Session == sess.Name && authority.SessionUID == outcome.SessionUID && authority.PodUID == outcome.PodUID &&
				authority.ThreadID == outcome.ThreadID && outcome.WriterGeneration <= math.MaxInt64 && authority.WriterGeneration == int64(outcome.WriterGeneration)
			if prior == nil || prior.Note == note || supersedesConfirmed {
				// Keep the first escalation observation and its age stable across
				// repeated heartbeats, including future-clock clamps. metav1.Time
				// serializes at whole-second precision.
				if prior == nil || prior.State != v1alpha1.OutcomeEscalated || prior.Note != note {
					at := metav1.NewTime(observedAt.Truncate(time.Second))
					sess.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, Note: note, At: &at}
				}
				if answer := sess.Status.DecisionAnswer; answer != nil && answer.Phase == "Confirmed" &&
					answer.Session == sess.Name && answer.SessionUID == outcome.SessionUID && answer.PodUID == outcome.PodUID &&
					answer.DecisionID == outcome.ID && answer.ThreadID == outcome.ThreadID && outcome.WriterGeneration <= math.MaxInt64 && answer.WriterGeneration == int64(outcome.WriterGeneration) &&
					answer.Digest == outcome.AnswerDigest && outcome.State == "Delivered" {
					sess.Status.Outcome = nil
				}
			}

		} // Omission cannot erase a pending or locally fabricated decision.

	}
	now := metav1.NewTime(s.now())
	sess.Status.Agent = agentStatus(st, now, base.Status.Agent)
	sess.Status.RemoteControl = remoteControlStatus(st.RemoteControl)
	// The conversation's usage is its own field. An absent or unusable one leaves
	// the stored value; usageObservation is never touched here.
	if cu := conversationUsageStatus(st.ConversationUsage, &sess, s.now()); cu != nil {
		sess.Status.ConversationUsage = cu
	}
	if st.Usage != nil && st.UsageObservation == nil && sess.Status.UsageObservation == nil {
		sess.Status.Usage = usageStatus(*st.Usage)
	}
	patch := client.MergeFrom(base)
	// An outcome acquired by another writer after this read must not be replaced
	// or cleared by a stale decision heartbeat. StartedAt is not part of this
	// patch: persistAgentStart writes it from the snapshot after this merge.
	if !apiequality.Semantic.DeepEqual(base.Status.Outcome, sess.Status.Outcome) {
		patch = client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	}
	if err := s.Client.Status().Patch(ctx, &sess, patch); err != nil {
		return 0, nil, fromKubeError(err, "session "+key.Name+" status")
	}
	if err := s.persistAgentStart(ctx, key, c, st.Agent.StartedAt, now.Time); err != nil {
		return 0, nil, err
	}
	if err := s.recordUsageObservation(ctx, &sess, c.pod, st.UsageObservation); err != nil {
		return 0, nil, fromKubeError(err, "session "+key.Name+" usage observation")
	}
	// Capture is best-effort and must not change the heartbeat result.
	if err := s.captureChildUsage(ctx, &sess, c.pod, st.UsageObservation); err != nil {
		s.Log.Error(err, "capture child usage", "session", key.Name)
	}
	return http.StatusNoContent, nil, nil
}

// remoteControlStatus is status.remoteControl from a heartbeat: the entry's
// name, link and state, each clipped (Story110 T03). A bridge id that matches
// the bounded pattern is stored whole.
func remoteControlStatus(rc *protocol.RemoteControl) *v1alpha1.RemoteControlStatus {
	if rc == nil {
		return nil
	}
	return &v1alpha1.RemoteControlStatus{
		Name:      clip(rc.Name, maxShort),
		SessionID: acceptedRCSessionID(rc),
		URL:       clip(rc.URL, maxMessage),
		State:     clip(rc.State, maxShort),
	}
}

// agentStatus is status.agent from a heartbeat, received at now.
// prev is the status already stored for this session. This copy keeps the
// stored StartedAt so the merge does not replace a start that lands while
// the patch is in flight. The RC session id is copied only when this
// heartbeat reports one that matches the bridge id pattern.
func agentStatus(st protocol.Status, now metav1.Time, prev *v1alpha1.AgentStatus) *v1alpha1.AgentStatus {
	a := &v1alpha1.AgentStatus{
		Status:                 clip(st.Agent.State, maxShort),
		LastHeartbeat:          &now,
		Attached:               st.Agent.Attached,
		Agentd:                 clip(st.Agentd, maxShort),
		Boot:                   clip(st.Boot, maxShort),
		ConversationID:         clip(st.Agent.ConversationID, maxShort),
		Message:                clip(st.Agent.Error, maxMessage),
		RemoteControlSessionID: acceptedRCSessionID(st.RemoteControl),
	}
	if prev != nil {
		a.StartedAt = copyMetaTime(prev.StartedAt)
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
	a.ProjectBootstrap = projectBootstrapStatus(st.ProjectBootstrap)
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

// acceptedAgentStart stores a heartbeat's agent start. A missing report, a
// report more than agentStartSkew ahead of now, or a report earlier than the
// stored time leaves the stored time unchanged. The stored time is the agent's
// report, truncated to the second precision metav1.Time keeps.
func acceptedAgentStart(reported *time.Time, now time.Time, prev *metav1.Time) *metav1.Time {
	if reported == nil || reported.IsZero() {
		return copyMetaTime(prev)
	}
	if reported.After(now.Add(agentStartSkew)) {
		return copyMetaTime(prev)
	}
	rt := reported.UTC().Truncate(time.Second)
	if prev != nil && !prev.IsZero() && rt.Before(prev.Time.UTC().Truncate(time.Second)) {
		return copyMetaTime(prev)
	}
	t := metav1.NewTime(rt)
	return &t
}

func copyMetaTime(t *metav1.Time) *metav1.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	c := *t
	return &c
}

// acceptedRCSessionID copies the bridge session id only when this heartbeat
// reports one that matches the apiv1 pattern. Anything else is omitted so the
// status write stays within the CRD.
func acceptedRCSessionID(rc *protocol.RemoteControl) string {
	if rc == nil || !remoteControlSessionIDPattern.MatchString(rc.SessionID) {
		return ""
	}
	return rc.SessionID
}

// persistAgentStart stores this heartbeat's start from a fresh read. A missing
// report, a time more than agentStartSkew ahead of now, or a time earlier than
// the time now stored leaves that stored time in place. The write is a status
// update with the fresh resourceVersion as a CAS precondition. On conflict,
// reread and recompute the accepted time; never reuse the stale candidate.
func (s *Server) persistAgentStart(ctx context.Context, key client.ObjectKey, c *caller, reported *time.Time, now time.Time) error {
	var writeErr error
	for attempt := 0; attempt < 3; attempt++ {
		var fresh v1alpha1.AgentSession
		if err := s.Live.Get(ctx, key, &fresh); err != nil {
			return fromKubeError(err, "session "+key.Name)
		}
		if fresh.UID != c.session.UID {
			return forbidden("session %s was replaced; the pod belongs to the earlier one", key.Name)
		}
		var prev *metav1.Time
		if fresh.Status.Agent != nil {
			prev = fresh.Status.Agent.StartedAt
		}
		accepted := acceptedAgentStart(reported, now, prev)
		if apiequality.Semantic.DeepEqual(prev, accepted) {
			return nil
		}
		if fresh.Status.Agent == nil {
			fresh.Status.Agent = &v1alpha1.AgentStatus{}
		}
		fresh.Status.Agent.StartedAt = accepted
		writeErr = s.Client.Status().Update(ctx, &fresh)
		if writeErr == nil || !apierrors.IsConflict(writeErr) {
			break
		}
	}
	if writeErr != nil {
		return fromKubeError(writeErr, "session "+key.Name+" status")
	}
	return nil
}

// projectBootstrapStatus is status.agent.projectBootstrap from agentd's
// report (DESIGN-006, reason codes). It copies only values the CRD accepts:
// an unknown state drops the report, an item with an unknown state or an
// invalid name is left out, and an unknown step, reason or cause is cleared,
// so a newer agentd never fails an older operator's heartbeat.
func projectBootstrapStatus(pb *protocol.ProjectBootstrap) *v1alpha1.ProjectBootstrapStatus {
	if pb == nil || !protocol.InSet(protocol.BootstrapStates, pb.State) {
		return nil
	}
	known := func(set []string, v string) string {
		if protocol.InSet(set, v) {
			return v
		}
		return ""
	}
	out := &v1alpha1.ProjectBootstrapStatus{State: pb.State, Reason: known(protocol.CatalogReasons, pb.Reason)}
	if catalogRevision.MatchString(pb.CatalogRevision) {
		out.CatalogRevision = pb.CatalogRevision
	}
	for _, repo := range pb.Repositories {
		if len(out.Repositories) == protocol.MaxBootstrapRepositories {
			break
		}
		if !projectcatalog.ValidName(repo.Name) || !protocol.InSet(protocol.BootstrapRepositoryStates, repo.State) {
			continue
		}
		out.Repositories = append(out.Repositories, v1alpha1.ProjectBootstrapRepositoryStatus{
			Name: repo.Name, State: repo.State,
			Step: known(protocol.BootstrapSteps, repo.Step), Reason: known(protocol.RepositoryReasons, repo.Reason),
		})
	}
	for _, project := range pb.Projects {
		if len(out.Projects) == protocol.MaxBootstrapProjects {
			break
		}
		if !projectcatalog.ValidName(project.Name) || !protocol.InSet(protocol.BootstrapProjectStates, project.State) {
			continue
		}
		p := v1alpha1.ProjectBootstrapProjectStatus{
			Name: project.Name, State: project.State,
			Reason: known(protocol.RootReasons, project.Reason), Cause: known(protocol.RepositoryReasons, project.Cause),
		}
		if projectcatalog.ValidName(project.Repository) {
			p.Repository = project.Repository
		}
		out.Projects = append(out.Projects, p)
	}
	return out
}

var catalogRevision = regexp.MustCompile(`^[a-f0-9]{64}$`)

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
