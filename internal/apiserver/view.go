package apiserver

import (
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// view is a session as the API shows it. The prompt is in a single session's
// view only, never in a list.
func view(s *v1alpha1.AgentSession, withPrompt bool) apiv1.Session {
	v := apiv1.Session{
		Name:           s.Name,
		UID:            string(s.UID),
		Repo:           s.Spec.Repo,
		Base:           s.Spec.Base,
		Agent:          string(s.Spec.Agent),
		Mode:           string(s.Spec.Mode),
		Model:          s.Spec.Model,
		Effort:         s.Spec.Effort,
		Size:           string(s.Spec.Size),
		Profile:        s.Spec.Profile,
		Tools:          s.Spec.Tools,
		Parent:         s.Spec.Parent,
		Caller:         s.Spec.Caller,
		Lane:           string(s.Spec.Lane),
		IdempotencyKey: s.Labels[v1alpha1.LabelIdempotencyKey],
		OperatingMode:  string(s.Spec.OperatingMode),
		CreatedAt:      s.CreationTimestamp.UTC(),
		Reaping:        !s.DeletionTimestamp.IsZero(),
		SuspendedBy:    s.Annotations[v1alpha1.AnnotationSuspendedBy],
		Restore:        s.Spec.Restore,
		Phase:          phaseOf(s),
		Pending:        s.Status.PendingReason,
		Node:           s.Status.NodeName,
		Revision:       s.Status.Revision,
		Outdated:       meta.IsStatusConditionTrue(s.Status.Conditions, controller.ConditionOutdated),
	}
	if h := s.Status.PrivateHistory; h != nil {
		v.PrivateHistory = &apiv1.PrivateHistory{Version: h.Version, SessionUID: h.SessionUID, HomeClaim: h.HomeClaim,
			HomeUID: h.HomeUID, BindingGeneration: h.BindingGeneration, Admission: h.Admission, Provider: h.Provider, StopState: h.StopState, ExecutorAttempted: h.ExecutorAttempted, ExecutorIntent: h.ExecutorIntent}
		if e := h.LastExecutor; e != nil {
			v.PrivateHistory.LastExecutor = &apiv1.PrivateExecutor{PodUID: e.PodUID, PodName: e.PodName, NodeName: e.NodeName,
				Revision: e.Revision, Generation: e.Generation, ConversationID: e.ConversationID}
		}
	}
	if withPrompt {
		v.Prompt = s.Spec.Prompt
	}
	if t := s.Status.SuspendedAt; t != nil {
		at := t.UTC()
		v.SuspendedAt = &at
	}
	if t := s.Status.ArchivedAt; t != nil {
		at := t.UTC()
		v.ArchivedAt = &at
	}
	if l := s.Spec.Limits; l != nil {
		v.Limits = &apiv1.Limits{MaxTurns: l.MaxTurns}
		if l.Timeout != nil {
			v.Limits.Timeout = l.Timeout.Duration.String()
		}
	}
	if l := s.Spec.Lifecycle; l != nil && (l.IdleSuspendAfter != nil || l.ArchiveAfter != nil) {
		v.Lifecycle = &apiv1.Lifecycle{}
		if l.IdleSuspendAfter != nil {
			v.Lifecycle.IdleSuspendAfter = l.IdleSuspendAfter.Duration.String()
		}
		if l.ArchiveAfter != nil {
			v.Lifecycle.ArchiveAfter = l.ArchiveAfter.Duration.String()
		}
	}
	if a := s.Status.Agent; a != nil {
		v.AgentStatus = &apiv1.AgentStatus{
			State:                  a.Status,
			Boot:                   a.Boot,
			Problems:               a.Problems,
			Branch:                 a.Branch,
			Head:                   a.Head,
			ConversationID:         a.ConversationID,
			LastActivity:           timeOf(a.LastActivity),
			LastHeartbeat:          timeOf(a.LastHeartbeat),
			Message:                a.Message,
			Agentd:                 a.Agentd,
			RemoteControlSessionID: a.RemoteControlSessionID,
			StartedAt:              timeOf(a.StartedAt),
		}
		v.AgentStatus.ProjectBootstrap = projectBootstrapView(a.ProjectBootstrap)
		if t := a.Task; t != nil {
			v.AgentStatus.Task = &apiv1.TaskResult{
				ExitCode: t.ExitCode, FinishedAt: t.FinishedAt.UTC(), TimedOut: t.TimedOut,
				Subtype: t.Subtype, IsError: t.IsError, NumTurns: t.NumTurns,
			}
		}
	}
	if rc := s.Status.RemoteControl; rc != nil {
		v.RemoteControl = &apiv1.RemoteControl{Name: rc.Name, SessionID: rc.SessionID, URL: rc.URL, State: rc.State}
	}
	if o := s.Status.Outcome; o != nil {
		v.Outcome = &apiv1.Outcome{State: string(o.State), Note: o.Note, At: timeOf(o.At)}
	}
	// Raw legacy values remain in the CRD. Emitting them without provenance
	// makes already-deployed clients invent zero or present an estimate as paid.
	v.UsageObservation = sessionUsage(s)
	if issue := s.Status.UsageConflict; issue != nil {
		v.UsageConflict = &apiv1.UsageConflict{State: issue.State, Reason: issue.Reason, IncomingSHA256: issue.IncomingSHA256, ObservedAt: issue.ObservedAt.UTC()}
	}
	for _, c := range s.Status.Conditions {
		v.Conditions = append(v.Conditions, apiv1.Condition{
			Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message,
		})
	}
	return v
}

func timeOf(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// projectBootstrapView is status.agent.projectBootstrap for the API.
func projectBootstrapView(pb *v1alpha1.ProjectBootstrapStatus) *apiv1.ProjectBootstrap {
	if pb == nil {
		return nil
	}
	out := &apiv1.ProjectBootstrap{State: pb.State, Reason: pb.Reason, CatalogRevision: pb.CatalogRevision}
	for _, repo := range pb.Repositories {
		out.Repositories = append(out.Repositories, apiv1.ProjectBootstrapRepository{Name: repo.Name, State: repo.State, Step: repo.Step, Reason: repo.Reason})
	}
	for _, project := range pb.Projects {
		out.Projects = append(out.Projects, apiv1.ProjectBootstrapProject{Name: project.Name, State: project.State, Reason: project.Reason, Repository: project.Repository, Cause: project.Cause})
	}
	return out
}
