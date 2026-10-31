package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func usageObservationStatus(u protocol.UsageObservation) *v1alpha1.UsageObservationStatus {
	out := &v1alpha1.UsageObservationStatus{Version: u.Version, ID: u.ID, Source: v1alpha1.UsageSourceStatus(u.Source),
		ObservedAt: metav1.NewTime(u.ObservedAt.UTC().Truncate(time.Second)), Coverage: u.Coverage, CounterSemantics: u.CounterSemantics, ResultState: u.ResultState,
		InputTokens: v1alpha1.TokenObservationStatus(u.InputTokens), OutputTokens: v1alpha1.TokenObservationStatus(u.OutputTokens),
		CacheReadInputTokens: v1alpha1.TokenObservationStatus(u.CacheReadInputTokens), CacheCreationInputTokens: v1alpha1.TokenObservationStatus(u.CacheCreationInputTokens),
		Cost: v1alpha1.CostObservationStatus(u.Cost), AggregationScope: u.AggregationScope, ActualModelState: u.ActualModelState,
		SubscriptionQuotaState: u.SubscriptionQuotaState, APIBillingState: u.APIBillingState, LocalResourceState: u.LocalResourceState,
		Provenance: u.Provenance, BillingBasis: u.BillingBasis,
		LogicalTaskUID: u.LogicalTaskUID, LogicalTaskEpoch: u.LogicalTaskEpoch, WorkerID: u.WorkerID}
	for _, m := range u.ServedModels {
		out.ServedModels = append(out.ServedModels, v1alpha1.ModelUsageStatus{Model: m.Model, InputTokens: v1alpha1.TokenObservationStatus(m.InputTokens), OutputTokens: v1alpha1.TokenObservationStatus(m.OutputTokens),
			CacheReadInputTokens: v1alpha1.TokenObservationStatus(m.CacheReadInputTokens), CacheCreationInputTokens: v1alpha1.TokenObservationStatus(m.CacheCreationInputTokens), Cost: v1alpha1.CostObservationStatus(m.Cost)})
	}
	return out
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
		Provenance: u.Provenance, BillingBasis: u.BillingBasis,
		LogicalTaskUID: u.LogicalTaskUID, LogicalTaskEpoch: u.LogicalTaskEpoch, WorkerID: u.WorkerID}
	for _, m := range u.ServedModels {
		v.ServedModels = append(v.ServedModels, protocol.ModelUsage{Model: m.Model, InputTokens: protocol.TokenObservation(m.InputTokens), OutputTokens: protocol.TokenObservation(m.OutputTokens),
			CacheReadInputTokens: protocol.TokenObservation(m.CacheReadInputTokens), CacheCreationInputTokens: protocol.TokenObservation(m.CacheCreationInputTokens), Cost: protocol.CostObservation(m.Cost)})
	}
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

// sessionUsage is a Session's own retained observation, when it is valid and
// its source is this Session (the same checks as the API view).
func sessionUsage(s *v1alpha1.AgentSession) *protocol.UsageObservation {
	u := usageObservationView(s.Status.UsageObservation)
	if u == nil || u.Source.SessionUID != string(s.UID) || u.Source.Provider != string(s.Spec.Agent) || u.Source.RequestedModel != s.Spec.Model || s.Spec.Mode != v1alpha1.ModeTask {
		return nil
	}
	return u
}

// maxRollupSessions bounds a rollup's walk. A larger tree is Partial.
const maxRollupSessions = 64

// usageRollup sums root's retained descendants' own initial invocations
// (DESIGN-018). Descendants are found by spec.parent name among the Sessions
// that still exist, created no earlier than their parent (an older Session
// names an earlier holder of the name), and walked once each by UID. A reaped
// descendant whose Session was deleted is not in all and is not counted.
// Reaping and compute-reclaimed Sessions still retained in all are counted. Each
// contributes at most its own observation, keyed by its Session UID and
// observation ID, so no invocation is counted twice. It returns nil when root
// has no retained descendants: an empty sum is not a zero.
func usageRollup(root *v1alpha1.AgentSession, all []v1alpha1.AgentSession) *apiv1.UsageRollup {
	children := map[string][]*v1alpha1.AgentSession{}
	for i := range all {
		if all[i].UID != "" && all[i].Spec.Parent != "" {
			children[all[i].Spec.Parent] = append(children[all[i].Spec.Parent], &all[i])
		}
	}
	visited := map[types.UID]bool{root.UID: true}
	r := &apiv1.UsageRollup{Scope: apiv1.UsageRollupScope, Coverage: "Complete", Invocations: []apiv1.UsageRollupKey{}}
	var observed []protocol.UsageObservation
	queue := []*v1alpha1.AgentSession{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent.Name] {
			if visited[child.UID] || child.CreationTimestamp.Before(&parent.CreationTimestamp) {
				continue
			}
			if r.Sessions == maxRollupSessions {
				r.Truncated, r.Coverage = true, "Partial"
				queue = nil
				break
			}
			visited[child.UID] = true
			r.Sessions++
			queue = append(queue, child)
			u := sessionUsage(child)
			if u == nil {
				r.Coverage = "Partial"
				continue
			}
			r.Counted++
			r.Invocations = append(r.Invocations, apiv1.UsageRollupKey{SessionUID: string(child.UID), ObservationID: u.ID})
			observed = append(observed, *u)
		}
	}
	if r.Sessions == 0 {
		return nil
	}
	slices.SortFunc(r.Invocations, func(a, b apiv1.UsageRollupKey) int { return strings.Compare(a.SessionUID, b.SessionUID) })
	complete := r.Coverage == "Complete"
	// A total past the token bound is Unknown: a reader validates every
	// Reported amount against the same bound and would refuse it.
	sum := func(pick func(protocol.UsageObservation) protocol.TokenObservation) protocol.TokenObservation {
		var total int64
		for _, u := range observed {
			m := pick(u)
			if !complete || m.State != protocol.UsageReported || m.Amount == nil || *m.Amount > protocol.MaxUsageTokens-total {
				return protocol.TokenObservation{State: protocol.UsageUnknown}
			}
			total += *m.Amount
		}
		return protocol.TokenObservation{State: protocol.UsageReported, Amount: &total}
	}
	r.InputTokens = sum(func(u protocol.UsageObservation) protocol.TokenObservation { return u.InputTokens })
	r.OutputTokens = sum(func(u protocol.UsageObservation) protocol.TokenObservation { return u.OutputTokens })
	r.CacheReadInputTokens = sum(func(u protocol.UsageObservation) protocol.TokenObservation { return u.CacheReadInputTokens })
	r.CacheCreationInputTokens = sum(func(u protocol.UsageObservation) protocol.TokenObservation { return u.CacheCreationInputTokens })
	r.Cost = protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}
	r.BillingBasis, r.ObservedModelState = protocol.UsageUnknown, protocol.UsageUnknown
	if !complete || len(observed) == 0 {
		return r
	}
	cost, billing, models := new(big.Rat), true, map[string]bool{}
	costKnown, modelsKnown := true, true
	for _, u := range observed {
		billing = billing && u.Version == protocol.UsageVersion && u.BillingBasis == protocol.UsageSubscription
		if amount, ok := new(big.Rat).SetString(u.Cost.Amount); ok && u.Cost.State == protocol.UsageReported && u.Cost.Kind == protocol.UsageCLIEstimate {
			cost.Add(cost, amount)
		} else {
			costKnown = false
		}
		if u.ActualModelState != protocol.UsageProviderReported {
			modelsKnown = false
		}
		for _, m := range u.ServedModels {
			models[m.Model] = true
		}
	}
	if billing {
		r.BillingBasis = protocol.UsageSubscription
	}
	if amount, ok := canonicalDecimal(cost); costKnown && ok {
		r.Cost = protocol.CostObservation{State: protocol.UsageReported, Amount: amount, Unit: "USD", Kind: protocol.UsageCLIEstimate}
	}
	if modelsKnown {
		r.ObservedModelState = protocol.UsageProviderReported
		for m := range models {
			r.ObservedModels = append(r.ObservedModels, m)
		}
		slices.Sort(r.ObservedModels)
	}
	return r
}

// canonicalDecimal formats an exact nonnegative sum of canonical decimals in
// the same canonical form: no exponent, no trailing zeros, at most 64 bytes.
func canonicalDecimal(r *big.Rat) (string, bool) {
	if r.Sign() < 0 {
		return "", false
	}
	// Every addend has a terminating decimal, so the sum's denominator is
	// 2^a·5^b; 64 places hold any amount the bounded fields can express.
	s := strings.TrimRight(strings.TrimRight(r.FloatString(64), "0"), ".")
	check, ok := new(big.Rat).SetString(s)
	if !ok || check.Cmp(r) != 0 || len(s) > 64 {
		return "", false
	}
	return s, (protocol.CostObservation{State: protocol.UsageReported, Amount: s, Unit: "USD", Kind: protocol.UsageCLIEstimate}).Valid()
}

// legacyUsageShape keeps a Session response in the shape an older reader's
// strict decoder accepts when the reader did not ask for provider-reported
// usage (apiv1.AttributionHeader): a version 2 observation and the rollup are
// left out, so that reader shows Unknown rather than refusing the Session.
// Version 1 observations are unchanged.
func legacyUsageShape(body any) any {
	legacy := func(v *apiv1.Session) {
		if v.UsageObservation != nil && v.UsageObservation.Version != 1 {
			v.UsageObservation = nil
		}
		v.UsageRollup = nil
	}
	switch b := body.(type) {
	case apiv1.Session:
		legacy(&b)
		return b
	case *apiv1.Session:
		c := *b
		legacy(&c)
		return &c
	case apiv1.SessionList:
		c := b
		c.Sessions = append([]apiv1.Session(nil), b.Sessions...)
		for i := range c.Sessions {
			legacy(&c.Sessions[i])
		}
		return c
	}
	return body
}
