package apiserver

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// conversationUsageStatus is status.conversationUsage from a heartbeat, or nil
// when the heartbeat carries none the operator will keep: nothing sent (an agentd
// that does not measure it, a task session), a value that is not valid, an
// Unknown one, another provider than the session's, a session that is not
// interactive, or an older observation than the one already stored. nil leaves
// the stored status as it is. It never touches status.usageObservation, the
// headless task's first-invocation record, and never adds to it.
func conversationUsageStatus(in *protocol.ConversationUsage, sess *v1alpha1.AgentSession, now time.Time) *v1alpha1.ConversationUsageStatus {
	if in == nil || !in.Valid() || in.ResultState == protocol.ConversationUnknown ||
		in.Provider != string(sess.Spec.Agent) || (sess.Spec.Mode != v1alpha1.ModeLocal && sess.Spec.Mode != v1alpha1.ModeRemote) {
		return nil
	}
	// A clock ahead of the operator's is clamped to now; status times are whole seconds.
	observed := in.ObservedAt.UTC()
	if observed.After(now) {
		observed = now.UTC()
	}
	observed = observed.Truncate(time.Second)
	if old := sess.Status.ConversationUsage; old != nil && old.ObservedAt.Time.After(observed) {
		return nil
	}
	out := &v1alpha1.ConversationUsageStatus{
		Version: in.Version, Provider: in.Provider, ObservedAt: metav1.NewTime(observed),
		Coverage: in.Coverage, CounterSemantics: in.CounterSemantics, ResultState: in.ResultState, ReasonCode: in.ReasonCode,
		Turns: in.Turns, Skipped: in.Skipped,
	}
	for _, m := range in.Models {
		out.Models = append(out.Models, v1alpha1.ConversationModelUsageStatus{
			Model:                    m.Model,
			InputTokens:              tokenStatus(m.InputTokens),
			OutputTokens:             tokenStatus(m.OutputTokens),
			CacheReadInputTokens:     tokenStatus(m.CacheReadInputTokens),
			CacheCreationInputTokens: tokenStatus(m.CacheCreationInputTokens),
		})
	}
	return out
}

func tokenStatus(m protocol.TokenObservation) v1alpha1.TokenObservationStatus {
	status := v1alpha1.TokenObservationStatus{State: m.State}
	if m.Amount != nil {
		amount := *m.Amount
		status.Amount = &amount
	}
	return status
}
