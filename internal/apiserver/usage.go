package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func usageObservationStatus(u protocol.UsageObservation) *v1alpha1.UsageObservationStatus {
	return &v1alpha1.UsageObservationStatus{Version: u.Version, ID: u.ID, Source: v1alpha1.UsageSourceStatus(u.Source),
		ObservedAt: metav1.NewTime(u.ObservedAt.UTC().Truncate(time.Second)), Coverage: u.Coverage, CounterSemantics: u.CounterSemantics, ResultState: u.ResultState,
		InputTokens: v1alpha1.TokenObservationStatus(u.InputTokens), OutputTokens: v1alpha1.TokenObservationStatus(u.OutputTokens),
		CacheReadInputTokens: v1alpha1.TokenObservationStatus(u.CacheReadInputTokens), CacheCreationInputTokens: v1alpha1.TokenObservationStatus(u.CacheCreationInputTokens),
		Cost: v1alpha1.CostObservationStatus(u.Cost), AggregationScope: u.AggregationScope, ActualModelState: u.ActualModelState,
		SubscriptionQuotaState: u.SubscriptionQuotaState, APIBillingState: u.APIBillingState, LocalResourceState: u.LocalResourceState,
		LogicalTaskUID: u.LogicalTaskUID, LogicalTaskEpoch: u.LogicalTaskEpoch, WorkerID: u.WorkerID}
}

func usageObservationView(u *v1alpha1.UsageObservationStatus) *protocol.UsageObservation {
	if u == nil {
		return nil
	}
	v := &protocol.UsageObservation{Version: u.Version, ID: u.ID, Source: protocol.UsageSource(u.Source),
		ObservedAt: u.ObservedAt.UTC(), Coverage: u.Coverage, CounterSemantics: u.CounterSemantics, ResultState: u.ResultState,
		InputTokens: protocol.TokenObservation(u.InputTokens), OutputTokens: protocol.TokenObservation(u.OutputTokens),
		CacheReadInputTokens: protocol.TokenObservation(u.CacheReadInputTokens), CacheCreationInputTokens: protocol.TokenObservation(u.CacheCreationInputTokens),
		Cost: protocol.CostObservation(u.Cost), AggregationScope: u.AggregationScope, ActualModelState: u.ActualModelState,
		SubscriptionQuotaState: u.SubscriptionQuotaState, APIBillingState: u.APIBillingState, LocalResourceState: u.LocalResourceState,
		LogicalTaskUID: u.LogicalTaskUID, LogicalTaskEpoch: u.LogicalTaskEpoch, WorkerID: u.WorkerID}
	if v.Validate() != nil {
		return nil
	}
	return v
}

func applyUsageObservation(sess *v1alpha1.AgentSession, pod *corev1.Pod, incoming protocol.UsageObservation, now time.Time) {
	reason := ""
	if incoming.Validate() != nil || incoming.ObservedAt.After(now) || (incoming.Source.Provider == protocol.AgentCodex && incoming.Cost.State != protocol.UsageUnknown) {
		reason = "InvalidPayload"
	} else if pod == nil || sess.Spec.Mode != v1alpha1.ModeTask || incoming.Source.SessionUID != string(sess.UID) ||
		incoming.Source.Provider != string(sess.Spec.Agent) || incoming.Source.RequestedModel != sess.Spec.Model {
		reason = "InvalidIdentity"
	}
	// Only the existing server-written complete binding may classify a task.
	u := incoming
	u.LogicalTaskUID, u.LogicalTaskEpoch, u.WorkerID = "", "", ""
	if b, err := taskbudget.BindingFromAnnotations(sess.Annotations); err == nil && protocol.ValidTaskBudgetIdentifier(sess.Annotations[taskbudget.WorkerAnnotation]) {
		u.LogicalTaskUID, u.LogicalTaskEpoch, u.WorkerID = b.TaskUID, strconv.FormatUint(b.Epoch, 10), sess.Annotations[taskbudget.WorkerAnnotation]
	}
	if incoming.LogicalTaskUID != "" && incoming.LogicalTaskUID != u.LogicalTaskUID || incoming.LogicalTaskEpoch != "" && incoming.LogicalTaskEpoch != u.LogicalTaskEpoch || incoming.WorkerID != "" && incoming.WorkerID != u.WorkerID {
		reason = "InvalidIdentity"
	}
	candidate := usageObservationStatus(u)
	if prior := sess.Status.UsageObservation; reason == "" && prior != nil {
		if apiequality.Semantic.DeepEqual(prior, candidate) {
			return
		}
		reason = "ConflictingSnapshot"
	}
	if reason == "" && incoming.Source.PodUID != string(pod.UID) {
		reason = "InvalidIdentity"
	}
	if h := sess.Status.PrivateHistory; reason == "" && h != nil && (h.SessionUID != string(sess.UID) || h.LastExecutor == nil || h.LastExecutor.PodUID != incoming.Source.PodUID) {
		reason = "InvalidIdentity"
	}
	if reason == "" {
		sess.Status.UsageObservation = candidate
		return
	}
	if sess.Status.UsageConflict == nil {
		digest := incoming.MalformedDigest()
		if digest == "" {
			raw, _ := json.Marshal(incoming)
			h := sha256.Sum256(raw)
			digest = hex.EncodeToString(h[:])
		}
		sess.Status.UsageConflict = &v1alpha1.UsageConflictStatus{State: protocol.UsageUnknown, Reason: reason, IncomingSHA256: digest, ObservedAt: metav1.NewTime(now.UTC().Truncate(time.Second))}
	}
}

// Usage retention follows ordinary liveness processing. Its conditional patch
// cannot replace another writer's first snapshot or roll back that heartbeat.
func (s *Server) recordUsageObservation(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, incoming *protocol.UsageObservation) error {
	if incoming == nil {
		return nil
	}
	var fresh v1alpha1.AgentSession
	if err := s.Live.Get(ctx, client.ObjectKeyFromObject(sess), &fresh); err != nil {
		return err
	}
	if fresh.UID != sess.UID {
		return nil
	}
	base := fresh.DeepCopy()
	applyUsageObservation(&fresh, pod, *incoming, s.now())
	if apiequality.Semantic.DeepEqual(base.Status.UsageObservation, fresh.Status.UsageObservation) && apiequality.Semantic.DeepEqual(base.Status.UsageConflict, fresh.Status.UsageConflict) {
		return nil
	}
	err := s.Client.Status().Patch(ctx, &fresh, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		return nil
	} // next retained replay observes the concurrent original.
	return err
}
