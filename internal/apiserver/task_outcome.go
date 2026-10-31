package apiserver

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// recordTaskOutcome is independent of liveness. An optimistic lock fences two
// simultaneous candidates; omission and replay can never clear the first record.
func (s *Server) recordTaskOutcome(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, st protocol.Status) error {
	incoming := st.TaskOutcome
	if incoming == nil || incoming.Validate() != nil || pod == nil {
		return nil
	}
	var fresh v1alpha1.AgentSession
	if err := s.Live.Get(ctx, client.ObjectKeyFromObject(sess), &fresh); err != nil {
		return err
	}
	if fresh.UID != sess.UID || fresh.Status.TaskOutcome != nil || fresh.Spec.Mode != v1alpha1.ModeTask || incoming.SessionUID != string(fresh.UID) || incoming.PodUID != string(pod.UID) {
		return nil
	}
	// The authenticated exit and the initial invocation observation must agree.
	// A replacement pod cannot relay the original pod's file.
	task := fresh.Status.Agent
	usage := fresh.Status.UsageObservation
	if st.Agent.State != protocol.AgentExited || task == nil || task.Task == nil || task.Task.ExitSource == nil || usage == nil ||
		task.Task.ExitSource.SessionUID != incoming.SessionUID || task.Task.ExitSource.PodUID != incoming.PodUID ||
		usage.Source.SessionUID != incoming.SessionUID || usage.Source.PodUID != incoming.PodUID || *task.Task.ExitSource != usage.Source {
		return nil
	}
	if h := fresh.Status.PrivateHistory; h != nil && (h.SessionUID != incoming.SessionUID || h.LastExecutor == nil || h.LastExecutor.PodUID != incoming.PodUID) {
		return nil
	}
	base := fresh.DeepCopy()
	fresh.Status.TaskOutcome = &v1alpha1.TaskOutcomeStatus{SessionUID: incoming.SessionUID, PodUID: incoming.PodUID, Record: v1alpha1.TaskOutcomeRecordStatus{PRURLs: append([]string(nil), incoming.Record.PRURLs...), HeadSHAs: append([]string(nil), incoming.Record.HeadSHAs...), Summary: incoming.Record.Summary}}
	err := s.Client.Status().Patch(ctx, &fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		return nil
	}
	return err
}

func sessionTaskOutcome(s *v1alpha1.AgentSession) *protocol.TaskOutcome {
	o := s.Status.TaskOutcome
	if o == nil || s.Spec.Mode != v1alpha1.ModeTask || o.SessionUID != string(s.UID) {
		return nil
	}
	out := &protocol.TaskOutcome{SessionUID: o.SessionUID, PodUID: o.PodUID, Record: protocol.TaskOutcomeRecord{PRURLs: append([]string(nil), o.Record.PRURLs...), HeadSHAs: append([]string(nil), o.Record.HeadSHAs...), Summary: o.Record.Summary}}
	if out.Validate() != nil {
		return nil
	}
	return out
}

// withoutTaskOutcome preserves the old strict Session DTO until a reader opts in.
func withoutTaskOutcome(body any) any {
	switch b := body.(type) {
	case apiv1.Session:
		b.TaskOutcome = nil
		return b
	case *apiv1.Session:
		c := *b
		c.TaskOutcome = nil
		return &c
	case apiv1.SessionList:
		c := b
		c.Sessions = append([]apiv1.Session(nil), b.Sessions...)
		for i := range c.Sessions {
			c.Sessions[i].TaskOutcome = nil
		}
		return c
	}
	return body
}
