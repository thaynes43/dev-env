package controller

// Summoned-session admission (DESIGN-064 5.9 and 6.7, OP777, slice a of
// item 741). A session with spec.caller gets no volume and no pod until the
// operator leader has admitted it, and never another once it is closed:
//
//   - The gate (summonGate) runs inside ensure, on every resource-building path. It
//     reads the session uncached and builds nothing unless status.summon.
//     admittedAt is set, the outcome is not terminal and, for a task, the
//     private task latch applies to it. It applies whatever
//     --enable-summoned-lanes says. reservePrivateExecutor repeats the check
//     on the Session it latches, under that write's optimistic lock, so a
//     close before the latch write commits still stops the pod. A first Pod
//     Create already sent after that write is handled by release, below.
//   - The leader's admission (summonAdmission, summonAdmissionPass) runs in
//     the leader only, every SummonAdmissionEvery and on a nudge after a
//     leader's own close or release, and only with --enable-summoned-lanes.
//     API reports wait for the next pass (within 60 seconds). Each pass
//     reads every session uncached, counts admitted, unreleased Sessions and
//     open grants in the create ledger, once per Session UID,
//     per single-flight lane and per pool, and takes the Queued ones oldest
//     first (creationTimestamp, then UID). A session is admitted only when an
//     uncached read of the create ledger binds its dispatch id to its own name
//     (item 741 comment 7228), its CallerPolicy still names its account, its
//     latch applies, its lane is free, its pool has a slot and the quota hook
//     (story 743's seam) lets it. A grant is committed by the ledger's shared
//     CAS before admittedAt is written on the Session. A conflict starts a
//     fresh decision; an unknown write result stops the pass. A grant without
//     admittedAt is completed, never decided again. A session the ledger does
//     not bind is closed by its onUnreported
//     with reason not-charged (ruling D5) and never admitted.
//   - Release (reconcileSummonRelease) sets status.summon.closed.released:
//     at once for a report (summonoutcome.Write), for an operator close once
//     no launch can follow: no attempt, the latch's recorded pod gone by an
//     uncached read, or a stored task result and a pod carrying
//     PrivateHistoryRetained. An uncertain attempt with no recorded executor
//     needs uncached absence and admittedAt + 15 minutes + one minute.
//
// Every branch keys on spec.caller, so an ordinary session is built exactly
// as before.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

const (
	// ConditionSummonAdmitted is True once the leader admitted a summoned
	// session, and False with the reason it is held while it is Queued
	// (DESIGN-064 6.7).
	ConditionSummonAdmitted = "SummonAdmitted"

	SummonReasonAdmitted   = "Admitted"
	SummonReasonLaneBusy   = "LaneBusy"
	SummonReasonPoolBusy   = "PoolBusy"
	SummonReasonPolicyGone = "PolicyGone"
	SummonReasonNotCharged = "NotCharged"
	// SummonReasonRemoteLatchMissing holds every summoned remote session until
	// slice d of item 741 builds the remote latch.
	SummonReasonRemoteLatchMissing = "RemoteLatchMissing"
	// SummonReasonTaskLatchMissing holds a summoned task while the operator
	// runs without both latch flags (--enable-private-history-retention and
	// --enable-terminal-task-pod-reclaim).
	SummonReasonTaskLatchMissing = "TaskLatchMissing"
	// SummonReasonQuotaHold is story 743's hold; its hook admits today.
	SummonReasonQuotaHold = "QuotaHold"

	// SummonAdmissionEvery is the leader's admission interval.
	SummonAdmissionEvery = time.Minute
	// SummonStartWindow is how long after admittedAt an admitted summoned
	// pod's agentd may make its first launch (AGENTD_START_DEADLINE).
	SummonStartWindow = 15 * time.Minute

	// summonPolicyNamespace is where CallerPolicies live: the API's
	// callerPolicyNamespace.
	summonPolicyNamespace = "dev-env-system"
)

// summonTaskLatchOn reports whether a summoned task gets both halves of the
// private task latch: the controller's executor latch
// (--enable-private-history-retention) and agentd's refusal to continue a
// saved launch, which a pod carries only with terminal task reclaim on
// (sessionDocument's retentionLatch; DESIGN-064 5.9 and its correction D10).
func (r *Reconciler) summonTaskLatchOn() bool {
	return r.PrivateHistoryRetention && r.terminalTaskReclaimOn()
}

// summonBuildRefusal is why a summoned session may not get a volume or a pod
// now, or "" when it may. Only a session with spec.caller is ever refused.
func summonBuildRefusal(s *v1alpha1.AgentSession, taskLatch bool) string {
	if s.Spec.Caller == "" {
		return ""
	}
	st := s.Status.Summon
	switch {
	case st == nil || st.AdmittedAt == nil:
		return "not admitted"
	case summonoutcome.Terminal(s.Status.Outcome) || st.Closed != nil:
		return "closed"
	case s.Spec.Mode == v1alpha1.ModeTask && !summonTaskLatchApplies(s, taskLatch):
		return "the private task latch does not apply"
	}
	return ""
}

// summonTaskLatchApplies reports whether both halves of the private task
// latch hold for this task: the flags are on, the controller's latch protects
// it, and its pod would tell agentd the task is retained.
func summonTaskLatchApplies(s *v1alpha1.AgentSession, taskLatch bool) bool {
	return taskLatch && privateHistoryProtected(s) && terminalTaskReclaimScope(s)
}

// summonGate reports whether ensure may build the session's volume and pod.
// An ordinary session always may. A summoned one may only when both the
// cached copy (which the pod is built from, so an admitted pod gets its start
// deadline) and an uncached read allow it; a failed read builds nothing.
func (r *Reconciler) summonGate(ctx context.Context, s *v1alpha1.AgentSession) (bool, error) {
	if s.Spec.Caller == "" {
		return true, nil
	}
	if why := summonBuildRefusal(s, r.summonTaskLatchOn()); why != "" {
		log.FromContext(ctx).V(1).Info("summoned session gets no volume or pod", "why", why)
		return false, nil
	}
	if r.APIReader == nil {
		return false, errors.New("the summoned session gate requires the uncached API reader")
	}
	var live v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &live); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if live.UID != s.UID {
		return false, nil
	}
	if why := summonBuildRefusal(&live, r.summonTaskLatchOn()); why != "" {
		log.FromContext(ctx).Info("summoned session gets no volume or pod", "why", why, "read", "uncached")
		return false, nil
	}
	return true, nil
}

// nudgeSummonAdmission asks the leader's admission for a pass now. It never
// blocks: one pending nudge is enough.
func (r *Reconciler) nudgeSummonAdmission() {
	if r.summonNudge == nil {
		return
	}
	select {
	case r.summonNudge <- struct{}{}:
	default:
	}
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// summonTaskFinished requires a stored run-agent result, or its durable
// terminal copy. Busy/Interrupted can follow launch.json before the first
// provider spawn (agent 2.105.0); no heartbeat state proves completion.
func summonTaskFinished(s *v1alpha1.AgentSession) bool {
	if h := s.Status.PrivateHistory; h != nil && h.TaskTerminal != nil {
		return true
	}
	return s.Status.Agent != nil && s.Status.Agent.Task != nil
}

// podRetainsPrivateHistory reports whether the pod's agentd was told the task
// is a retained private task, so it never continues a saved launch.
func podRetainsPrivateHistory(p *corev1.Pod) bool {
	for _, c := range p.Spec.Containers {
		if c.Name != ContainerName {
			continue
		}
		for _, e := range c.Env {
			if e.Name != protocol.SessionEnv || e.Value == "" {
				continue
			}
			var doc protocol.Session
			if json.Unmarshal([]byte(e.Value), &doc) != nil {
				return false
			}
			return doc.PrivateHistoryRetained
		}
	}
	return false
}

// summonReleasable decides, on an uncached Session, whether its reservation
// may be released now (DESIGN-064 5.9, "When a reservation is released").
// Until run-agent's pre-spawn check is built (slice d), an operator close
// whose launch may be in flight releases only once the pod is confirmed gone.
func (r *Reconciler) summonReleasable(ctx context.Context, s *v1alpha1.AgentSession) (bool, error) {
	st := s.Status.Summon
	if s.Spec.Caller == "" || st == nil || st.Closed == nil || st.Closed.Released {
		return false, nil
	}
	if st.Closed.Source == summonoutcome.Report || st.AdmittedAt == nil {
		// A report comes from the session's own pod; a session never admitted
		// never had a volume or a pod, and the gate builds none once closed.
		return true, nil
	}
	h := s.Status.PrivateHistory
	latchedTask := s.Spec.Mode == v1alpha1.ModeTask && privateHistoryProtected(s) && h != nil
	if latchedTask && (!h.ExecutorAttempted || h.ExecutorNotCreated && h.LastExecutor == nil) {
		// No attempt, or a known non-attempt: the closed record now refuses
		// every new latch write (reservePrivateExecutor).
		return true, nil
	}
	podKey := client.ObjectKeyFromObject(s)
	if latchedTask && h.LastExecutor != nil {
		podKey.Name = h.LastExecutor.PodName
	}
	var pod corev1.Pod
	gone := false
	if err := r.APIReader.Get(ctx, podKey, &pod); apierrors.IsNotFound(err) {
		gone = true
	} else if err != nil {
		return false, err
	}
	if latchedTask {
		if h.LastExecutor != nil {
			if gone || string(pod.UID) != h.LastExecutor.PodUID {
				return true, nil
			}
			return summonTaskFinished(s) && podRetainsPrivateHistory(&pod), nil
		}
		// A NotFound is not disappearance of a recorded UID: the first
		// Create may still be in flight. Past the deadline plus one minute,
		// a late first pod boots an agent that refuses its initial launch.
		// A present pod can have a pending first spawn and always holds.
		return gone && !r.now().Before(st.AdmittedAt.Add(SummonStartWindow+time.Minute)), nil
	}
	// Otherwise only when the session's agent pod is confirmed gone. A hold
	// pod under its name is the rescue pod (D-55) and runs no agent.
	return gone || isHoldPod(&pod), nil
}

// reconcileSummonRelease runs in the leader's reconciler after the outcome
// step. It writes closed.released under the optimistic lock once
// summonReleasable allows it, then nudges the admission.
func (r *Reconciler) reconcileSummonRelease(ctx context.Context, s *v1alpha1.AgentSession) (bool, error) {
	if st := s.Status.Summon; s.Spec.Caller == "" || st == nil || st.Closed == nil || st.Closed.Released {
		return false, nil
	}
	if r.APIReader == nil {
		return false, errors.New("summoned release requires the uncached API reader")
	}
	var live v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &live); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if live.UID != s.UID {
		return false, nil
	}
	ok, err := r.summonReleasable(ctx, &live)
	if err != nil || !ok {
		return false, err
	}
	base := live.DeepCopy()
	live.Status.Summon.Closed.Released = true
	if err := r.Client.Status().Patch(ctx, &live, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	log.FromContext(ctx).Info("released a summoned session's reservation", "caller", live.Spec.Caller, "lane", live.Spec.Lane, "reason", live.Status.Summon.Closed.Reason)
	r.nudgeSummonAdmission()
	return true, nil
}

// summonAdmission is the leader-only runnable that admits Queued summoned
// sessions. It runs a pass at start, so a new leader recomputes from the
// records at once, then every interval and on each nudge.
type summonAdmission struct {
	r     *Reconciler
	every time.Duration
	nudge <-chan struct{}
	log   logr.Logger
	// tick makes the interval's ticker; nil is time.NewTicker. Tests inject one.
	tick func(time.Duration) (<-chan time.Time, func())
	// passed, when set, sees the end of every pass. Tests set it.
	passed func(error)
}

// NeedLeaderElection makes the admission run in the leader only.
func (a *summonAdmission) NeedLeaderElection() bool { return true }

func (a *summonAdmission) Start(ctx context.Context) error {
	tick := a.tick
	if tick == nil {
		tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	ticks, stop := tick(a.every)
	defer stop()
	for {
		err := a.r.SummonAdmissionPass(ctx)
		if err != nil && ctx.Err() == nil {
			a.log.Error(err, "summoned admission pass")
		}
		if a.passed != nil {
			a.passed(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
		case <-a.nudge:
		}
	}
}

// summonPool is one caller's pool: its escalations, or every other entry
// (DESIGN-064 5.9, "The pools").
type summonPool struct {
	caller      string
	escalations bool
}

func summonPoolOf(s *v1alpha1.AgentSession) summonPool {
	return summonPool{caller: s.Spec.Caller, escalations: s.Spec.Lane == v1alpha1.LaneEscalation}
}

// summonSingleFlight reports whether a lane holds at most one admitted,
// unreleased session across callers. Escalation is never held for its lane.
func summonSingleFlight(l v1alpha1.Lane) bool {
	return l == v1alpha1.LaneRemediation || l == v1alpha1.LaneUpgrade || l == v1alpha1.LaneCuration
}

// summonReserved is an admitted, unreleased record: it holds its lane and
// one slot of its pool.
func summonReserved(s *v1alpha1.AgentSession) bool {
	st := s.Status.Summon
	return st != nil && st.AdmittedAt != nil && (st.Closed == nil || !st.Closed.Released)
}

// summonQueued is a session the leader may admit: never admitted, not closed,
// and neither suspended nor being deleted (slice c decides what those stops
// become).
func summonQueued(s *v1alpha1.AgentSession) bool {
	st := s.Status.Summon
	return s.Spec.Summon != nil && (st == nil || st.AdmittedAt == nil && st.Closed == nil) &&
		!summonoutcome.Terminal(s.Status.Outcome) && !wantsPodGone(s)
}

// summonTaskLatchStartable reports whether a Queued task's latch record can
// admit its one executor: the private latch protects it, its scope is the
// terminal task reclaim's (so its pod tells agentd), and its record is not an
// Unknown lifecycle, which never starts.
func summonTaskLatchStartable(s *v1alpha1.AgentSession) bool {
	h := s.Status.PrivateHistory
	return privateHistoryProtected(s) && terminalTaskReclaimScope(s) && h != nil && h.Admission != "Unknown" && !h.ExecutorAttempted
}

// summonQuotaAdmits is story 743's seam: the quota gate. It admits today.
func (r *Reconciler) summonQuotaAdmits(s *v1alpha1.AgentSession) bool {
	if r.SummonQuota != nil {
		return r.SummonQuota(s)
	}
	return true
}

// summonLedgerRead is one pass's uncached read of the create ledger.
type summonLedgerRead struct {
	cm *corev1.ConfigMap
}

// entry decodes the caller's key. ok is false when the ledger or the entry
// cannot be read: then nothing is admitted or closed by it this pass.
func (l summonLedgerRead) entry(caller string) (summonledger.Entry, bool) {
	if l.cm == nil {
		return summonledger.Entry{}, false
	}
	raw, present := l.cm.Data[caller]
	e, err := summonledger.Decode(raw, present)
	return e, err == nil
}

// summonPolicy reads the session's CallerPolicy uncached. It is nil when the
// policy is gone, is being deleted, has no summon block or no longer names
// the session's account (spec.parent); err is a read that failed otherwise.
func (r *Reconciler) summonPolicy(ctx context.Context, s *v1alpha1.AgentSession) (*v1alpha1.CallerPolicy, error) {
	var p v1alpha1.CallerPolicy
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: summonPolicyNamespace, Name: s.Spec.Caller}, &p)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if p.UID == "" || !p.DeletionTimestamp.IsZero() || p.Spec.Summon == nil || p.Spec.Summon.ServiceAccount != s.Spec.Parent ||
		strings.ReplaceAll(s.Spec.Parent, "/", ".") != s.Spec.Caller {
		return nil, nil
	}
	return &p, nil
}

func summonPoolCap(p *v1alpha1.CallerPolicy, pool summonPool) int32 {
	if pool.escalations {
		if p.Spec.Summon.Limits.Escalations == nil {
			return 0
		}
		return *p.Spec.Summon.Limits.Escalations
	}
	return p.Spec.Summon.Limits.Concurrent
}

// SummonAdmissionPass serializes grants on the create ledger's resourceVersion.
// Leader election chooses the driver; the shared CAS also fences requests
// already sent by a deposed leader. Conflicts restart the whole decision.
func (r *Reconciler) SummonAdmissionPass(ctx context.Context) error {
	if !r.SummonedLanes {
		return nil
	}
	if r.APIReader == nil || r.SessionNamespace == "" {
		return errors.New("summoned admission requires the uncached API reader and the session namespace")
	}
	// As in the API's ledger writer, bound contention and leave the next
	// interval to retry. Never spin indefinitely against API writes.
	for range 5 {
		retry, err := r.summonAdmissionAttempt(ctx)
		if !retry {
			return err
		}
	}
	return errors.New("summoned admission ledger is busy")
}

func (r *Reconciler) summonAdmissionAttempt(ctx context.Context) (bool, error) {
	var list v1alpha1.AgentSessionList
	if err := r.APIReader.List(ctx, &list, client.InNamespace(r.SessionNamespace)); err != nil {
		return false, fmt.Errorf("list the sessions: %w", err)
	}
	listed := map[types.UID]bool{}
	for i := range list.Items {
		if list.Items[i].Spec.Caller != "" {
			listed[list.Items[i].UID] = true
		}
	}
	if len(listed) == 0 {
		// Coordinator op777-fix1-q-1: retain the one-list idle fast path.
		// Gone grants are cleaned on the next active pass, before admitting.
		return false, nil
	}
	ledger := summonLedgerRead{cm: &corev1.ConfigMap{}}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: summonledger.Namespace, Name: summonledger.Name}, ledger.cm); err != nil {
		log.FromContext(ctx).Info("summoned admission holds every Queued session: the create ledger cannot be read", "error", err.Error())
		return false, nil
	}
	if ledger.cm.ResourceVersion == "" || !ledger.cm.DeletionTimestamp.IsZero() || ledger.cm.Immutable != nil && *ledger.cm.Immutable {
		return false, errors.New("summoned admission ledger cannot be written")
	}
	raw, present := ledger.cm.Data[summonledger.GrantsKey]
	grants, err := summonledger.DecodeGrants(raw, present)
	if err != nil {
		return false, fmt.Errorf("read admission grants: %w", err)
	}
	// Read Sessions AFTER the ledger: a grant's Session may have been
	// created after the initial list but before this ledger read. That
	// initial absence must never prune a live grant. If a grant changes
	// after this read, the ConfigMap CAS below conflicts.
	if err := r.APIReader.List(ctx, &list, client.InNamespace(r.SessionNamespace)); err != nil {
		return false, fmt.Errorf("list sessions for the ledger snapshot: %w", err)
	}
	byName := make(map[string]*v1alpha1.AgentSession, len(list.Items))
	lanes := map[v1alpha1.Lane]bool{}
	pools := map[summonPool]int32{}
	counted := map[types.UID]bool{}
	occupy := func(uid types.UID, lane v1alpha1.Lane, pool summonPool) {
		if counted[uid] {
			return
		}
		counted[uid] = true
		if summonSingleFlight(lane) {
			lanes[lane] = true
		}
		pools[pool]++
	}
	for i := range list.Items {
		s := &list.Items[i]
		byName[s.Name] = s
		if s.Spec.Caller != "" && summonReserved(s) {
			occupy(s.UID, s.Spec.Lane, summonPoolOf(s))
		}
	}
	kept := make([]summonledger.Grant, 0, len(grants.Items))
	granted := map[types.UID]bool{}
	for _, g := range grants.Items {
		s := byName[g.Name]
		if s == nil || string(s.UID) != g.UID || s.Status.Summon != nil && s.Status.Summon.Closed != nil && s.Status.Summon.Closed.Released {
			continue
		}
		kept = append(kept, g)
		granted[s.UID] = true
		occupy(s.UID, v1alpha1.Lane(g.Lane), summonPool{caller: g.Caller, escalations: g.Pool == summonledger.PoolEscalations})
		if s.Status.Summon == nil || s.Status.Summon.AdmittedAt == nil {
			// Already granted. Complete it using its original time, without
			// rechecking policy, charge, quota or current task eligibility.
			admitted, err := r.admitSummon(ctx, s, g.At)
			if err != nil {
				return false, err
			}
			if !admitted {
				return true, nil
			}
		}
	}
	dirty := len(kept) != len(grants.Items)
	grants.Items = kept
	var queued []*v1alpha1.AgentSession
	for i := range list.Items {
		s := &list.Items[i]
		// Only the first list's candidates may be judged against this
		// ledger: a later Session could have been charged after its read.
		// The second list exists to validate grants, not to widen that set.
		if s.Spec.Caller != "" && listed[s.UID] && !granted[s.UID] && summonQueued(s) {
			queued = append(queued, s)
		}
	}
	// D6: oldest first in each lane and pool; two lanes never hold each other.
	sort.SliceStable(queued, func(i, j int) bool {
		a, b := queued[i], queued[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		return a.UID < b.UID
	})
	now := r.now().UTC().Truncate(time.Second)
	var errs []error
	for _, s := range queued {
		entry, ok := ledger.entry(s.Spec.Caller)
		if !ok {
			continue
		}
		if !entry.Binds(s.Spec.Summon.DispatchID, s.Name) {
			if err := r.closeSummonNotCharged(ctx, s, now); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if s.Spec.Mode == v1alpha1.ModeTask && r.summonTaskLatchOn() && s.Status.PrivateHistory == nil {
			// Recording a hold before the reconciler initializes the task
			// latch would turn that fresh lifecycle into Unknown.
			continue
		}
		p, err := r.summonPolicy(ctx, s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pool := summonPoolOf(s)
		reason := ""
		switch {
		case p == nil:
			reason = SummonReasonPolicyGone
		case s.Spec.Mode == v1alpha1.ModeRemote && !r.summonRemoteLatch:
			reason = SummonReasonRemoteLatchMissing
		case s.Spec.Mode == v1alpha1.ModeTask && (!r.summonTaskLatchOn() || !summonTaskLatchStartable(s)):
			reason = SummonReasonTaskLatchMissing
		case summonSingleFlight(s.Spec.Lane) && lanes[s.Spec.Lane]:
			reason = SummonReasonLaneBusy
		case pools[pool] >= summonPoolCap(p, pool):
			reason = SummonReasonPoolBusy
		case !r.summonQuotaAdmits(s):
			reason = SummonReasonQuotaHold
		}
		if reason != "" {
			if err := r.holdSummon(ctx, s, reason); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		poolName := summonledger.PoolConcurrent
		if pool.escalations {
			poolName = summonledger.PoolEscalations
		}
		grants.Items = append(grants.Items, summonledger.Grant{Name: s.Name, UID: string(s.UID), Lane: string(s.Spec.Lane), Caller: s.Spec.Caller, Pool: poolName, At: now})
		if err := r.writeSummonGrants(ctx, ledger.cm, grants); err != nil {
			if apierrors.IsConflict(err) {
				return true, nil
			}
			// The request may have committed. No further grant in this pass;
			// a subsequent pass recovers the durable reservation, if any.
			return false, errors.Join(append(errs, err)...)
		}
		dirty = false // stale grants were removed in this same confirmed CAS
		occupy(s.UID, s.Spec.Lane, pool)
		admitted, err := r.admitSummon(ctx, s, now)
		if err != nil {
			return false, errors.Join(append(errs, err)...)
		}
		if !admitted {
			return true, nil
		}
		log.FromContext(ctx).Info("admitted a summoned session", "session", s.Name, "caller", s.Spec.Caller, "lane", s.Spec.Lane, "pool", poolName, "inPool", pools[pool])
	}
	if dirty {
		if err := r.writeSummonGrants(ctx, ledger.cm, grants); err != nil {
			if apierrors.IsConflict(err) {
				return true, nil
			}
			return false, errors.Join(append(errs, err)...)
		}
	}
	return false, errors.Join(errs...)
}

// writeSummonGrants updates the same object and resourceVersion read for
// this decision. Caller entries remain byte-for-byte unchanged.
func (r *Reconciler) writeSummonGrants(ctx context.Context, cm *corev1.ConfigMap, grants summonledger.Grants) error {
	// The only callers pass this attempt's uncached ledger snapshot (or
	// its confirmed Update response). Never use a fabricated object or an
	// unconditional Update; this exception to the lifecycle source guard
	// is solely the named ledger's data CAS, not a resource release path.
	if cm == nil || cm.Name != summonledger.Name || cm.Namespace != summonledger.Namespace || cm.ResourceVersion == "" {
		return errors.New("summoned grant write requires the named ledger's uncached resourceVersion")
	}
	raw, err := summonledger.EncodeGrants(grants)
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[summonledger.GrantsKey] = raw
	return r.Client.Update(ctx, cm)
}

// admitSummon writes admittedAt and SummonAdmitted=True under the optimistic
// lock of the uncached read. It reports false, with no error, on a conflict.
func (r *Reconciler) admitSummon(ctx context.Context, s *v1alpha1.AgentSession, now time.Time) (bool, error) {
	base := s.DeepCopy()
	if s.Status.Summon == nil {
		s.Status.Summon = &v1alpha1.SummonStatus{}
	}
	at := metav1.NewTime(now.UTC().Truncate(time.Second))
	s.Status.Summon.AdmittedAt = &at
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: ConditionSummonAdmitted, Status: metav1.ConditionTrue,
		Reason: SummonReasonAdmitted, Message: "admitted by the operator leader: charged, lane free, pool slot taken",
		ObservedGeneration: s.Generation, LastTransitionTime: at})
	if err := r.Client.Status().Patch(ctx, s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("admit %s: %w", s.Name, err)
	}
	return true, nil
}

var summonHoldMessages = map[string]string{
	SummonReasonLaneBusy:           "held: another session holds its single-flight lane",
	SummonReasonPoolBusy:           "held: its caller's pool is full",
	SummonReasonPolicyGone:         "held: its CallerPolicy is gone or no longer names its account",
	SummonReasonNotCharged:         "never admitted: the create ledger does not bind its dispatch id to this name",
	SummonReasonRemoteLatchMissing: "held: a summoned remote session waits for the remote latch (item 741 slice d)",
	SummonReasonTaskLatchMissing:   "held: the private task latch does not apply (both task latch flags, and a startable latch record)",
	SummonReasonQuotaHold:          "held: quota",
}

// holdSummon records why a Queued session waits, once per reason.
func (r *Reconciler) holdSummon(ctx context.Context, s *v1alpha1.AgentSession, reason string) error {
	if c := meta.FindStatusCondition(s.Status.Conditions, ConditionSummonAdmitted); c != nil && c.Status == metav1.ConditionFalse && c.Reason == reason {
		return nil
	}
	base := s.DeepCopy()
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: ConditionSummonAdmitted, Status: metav1.ConditionFalse,
		Reason: reason, Message: summonHoldMessages[reason], ObservedGeneration: s.Generation, LastTransitionTime: metav1.NewTime(r.now().UTC().Truncate(time.Second))})
	if err := r.Client.Status().Patch(ctx, s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil && !apierrors.IsConflict(err) && !apierrors.IsNotFound(err) {
		return fmt.Errorf("hold %s: %w", s.Name, err)
	}
	return nil
}

// closeSummonNotCharged closes a Queued session the ledger does not bind by
// its onUnreported, reason not-charged (ruling D5), releasing at once: it was
// never admitted. The condition follows in its own write.
func (r *Reconciler) closeSummonNotCharged(ctx context.Context, s *v1alpha1.AgentSession, now time.Time) error {
	state := v1alpha1.OutcomeState(s.Spec.Summon.OnUnreported)
	if state != v1alpha1.OutcomeFailed && state != v1alpha1.OutcomeEscalated {
		log.FromContext(ctx).Error(nil, "summoned session has an invalid onUnreported snapshot", "session", s.Name)
		return nil
	}
	reason := summonoutcome.FirstReason(summonoutcome.Inputs{NotCharged: true})
	d := summonoutcome.Decision{State: state, Note: "unreported: " + reason, Source: summonoutcome.Unreported, Reason: reason, Released: true}
	changed, err := summonoutcome.Write(ctx, r.Client, s, d, now)
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("close %s not-charged: %w", s.Name, err)
	}
	if changed {
		log.FromContext(ctx).Info("closed a summoned session the create ledger does not bind", "session", s.Name, "caller", s.Spec.Caller, "lane", s.Spec.Lane)
		r.nudgeSummonAdmission()
	}
	return r.holdSummon(ctx, s, SummonReasonNotCharged)
}
