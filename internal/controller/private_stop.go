package controller

// The stop request for a Reaping private Session's recorded executor
// (DESIGN-025 section 4, unit H3, as ADR-006 narrowed it). An ordinary private
// executor never ends on its own, so a reap asks the kubelet to end it: the
// operator records a stop intent in the Session's status by a confirmed write
// that carries the resourceVersion of the uncached read that found no owner
// hold, then sets the running Pod's activeDeadlineSeconds by one JSON patch
// with the Pod's UID and resourceVersion as tests. The kubelet stops the
// containers and marks the Pod Failed (DeadlineExceeded); the Pod object stays,
// and H1's terminal record proves the stop. Nothing is deleted, and no HOME
// byte changes. Default off (--enable-private-executor-stop).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// MetricPrivateExecutorStopRequests counts the stop requests: sent (the Pod
// patch landed), conflict (the intent write or the patch found the Session or
// the Pod changed; the next reconcile judges again) and held (an owner hold
// refused the stop).
const MetricPrivateExecutorStopRequests = "dev_env_private_executor_stop_requests_total"

// The result label's values.
const (
	stopSent     = "sent"
	stopConflict = "conflict"
	stopHeld     = "held"
)

// privateStopGrace is how long after the stored intent the kubelet's deadline
// falls: activeDeadlineSeconds counts from the Pod's start, so the operator
// sets it to the Pod's age at the intent's requestedAt plus this. A patch sent
// later (a retry, or a restart between the intent and the patch) gets less of
// it, or none; the kubelet still stops the containers with the Pod's
// termination grace.
const privateStopGrace = 30 * time.Second

// EventExecutorStopRequested is the event reason of a sent stop request.
const EventExecutorStopRequested = "ExecutorStopRequested"

// NewPrivateExecutorStopCounter returns the counter of
// MetricPrivateExecutorStopRequests, with each result's series present at 0.
// Register it once per process.
func NewPrivateExecutorStopCounter() *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricPrivateExecutorStopRequests,
		Help: "Stop requests for Reaping private executors (DESIGN-025 4): sent (activeDeadlineSeconds patched), conflict (the Session or the Pod changed under the intent write or the patch) and held (an owner hold refused the stop).",
	}, []string{"result"})
	for _, result := range []string{stopSent, stopConflict, stopHeld} {
		c.WithLabelValues(result)
	}
	return c
}

func (r *Reconciler) countStop(result string) {
	if r.PrivateExecutorStops != nil {
		r.PrivateExecutorStops.WithLabelValues(result).Inc()
	}
}

// errStopHeld, errStopNotNeeded and errStopChanged are why stopIntentConfirmed
// returns no intent: an owner hold, nothing to stop (the Session or the Pod is
// no longer in the shape a stop needs), or a Session that changed under the
// intent write. errStopPodChanged is why stopPrivateExecutor sent nothing: the
// Pod changed since its read (a Conflict, or a failed test).
var (
	errStopHeld       = errors.New("an owner hold on the HOME expiry blocks the stop request")
	errStopNotNeeded  = errors.New("the Session or its recorded executor no longer needs a stop request")
	errStopChanged    = errors.New("the Session changed under the stop intent write")
	errStopPodChanged = errors.New("the recorded executor changed since it was read for the stop")
)

// privateStopRetry is how soon a stop request that failed for any reason but a
// change is tried again. The reconcile still writes the Session's status, and
// the RemovalBlocked condition names the failure.
const privateStopRetry = 30 * time.Second

// homeExpiryHeld reports whether the owner override blocks the stop and the
// HOME expiry's steps. Any value but release-now counts as a hold, so a
// mistyped hold never lets a stop or a step through (fail closed, DESIGN-025
// 9); the expiry counts such a value as override_invalid.
func homeExpiryHeld(s *v1alpha1.AgentSession) bool {
	v, ok := s.Annotations[v1alpha1.AnnotationHomeExpiry]
	return ok && v != v1alpha1.HomeExpiryReleaseNow
}

// privateStopApplies reports whether the Session may have its executor
// stopped, judged on its own fields: the stop flag is on, a terminal record
// may still be written for its recorded executor (terminalRecordApplies: the
// record flag, an intact retention record, a Started admission, a bound HOME,
// a recorded executor, no terminal record or missed marker yet), and it is
// Reaping and private (no workspace, no canary binding). A Suspended Session is
// never stopped for its archive deadline (ADR-006). DESIGN-025 section 24's
// separately gated original terminal task is the compute-only exception.
func (r *Reconciler) privateStopApplies(s *v1alpha1.AgentSession) bool {
	if !r.PrivateExecutorStop || !r.terminalRecordApplies(s) || s.DeletionTimestamp.IsZero() && !r.terminalTaskStopDue(s) || s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil {
		return false
	}
	h := s.Status.PrivateHistory
	return h.StopRequest == nil || h.StopRequest.PodUID == h.LastExecutor.PodUID
}

// privateStopDeadline is the activeDeadlineSeconds the stop sets: the Pod's
// age at at, rounded up, plus privateStopGrace. ok is false when the Pod has no
// start time, so no deadline can be computed.
func privateStopDeadline(pod *corev1.Pod, at time.Time) (int64, bool) {
	if pod.Status.StartTime == nil {
		return 0, false
	}
	age := max(0, math.Ceil(at.Sub(pod.Status.StartTime.Time).Seconds()))
	return int64(age) + int64(privateStopGrace/time.Second), true
}

// privateStopNeeded reports whether pod is the Session's recorded executor and
// still needs the stop: the Session controls it, it is not a hold pod, it is
// not terminal or deleting, it has started, and it has no deadline at or
// below the one the stop would set. A Pod that already has one is left alone,
// which is what keeps the patch to one per executor across reconciles,
// restarts and replicas. Once the intent is stored, the deadline counts from
// its requestedAt, not from now: every operator then computes the same
// deadline from stored fields, whatever its own clock says, so a replica or a
// restarted operator whose clock lags the one that patched never finds the
// Pod's deadline above its own and never patches again. Before the intent, now
// stands in for the requestedAt the intent will store.
func privateStopNeeded(s *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) (int64, bool) {
	h := s.Status.PrivateHistory
	if h == nil || h.LastExecutor == nil || pod.UID == "" || string(pod.UID) != h.LastExecutor.PodUID || !controlledBySession(pod, s) || isHoldPod(pod) ||
		podPhaseTerminal(pod) || !pod.DeletionTimestamp.IsZero() {
		return 0, false
	}
	at := now
	if sr := h.StopRequest; sr != nil {
		if sr.PodUID != string(pod.UID) {
			return 0, false
		}
		at = sr.RequestedAt.Time
	}
	deadline, ok := privateStopDeadline(pod, at)
	if !ok || pod.Spec.ActiveDeadlineSeconds != nil && *pod.Spec.ActiveDeadlineSeconds <= deadline {
		return 0, false
	}
	return deadline, true
}

// confirmedStopIntent is what stopIntentConfirmed returns once the intent is
// stored and confirmed: the confirmed Session and the recorded executor as read
// for it. Only stopIntentConfirmed builds one (TestOnlyStopPrivateExecutorPatchesPods),
// and stopPrivateExecutor refuses the zero value, so no patch can be sent
// without a confirmed intent.
type confirmedStopIntent struct {
	confirmedSession *v1alpha1.AgentSession
	readExecutor     *corev1.Pod
}

// requestPrivateExecutorStop is DESIGN-025 section 4. It runs from removePod,
// for a Session that wants its Pod gone and still has one, after this
// reconcile's private history observation changed nothing. It never ends the
// reconcile early: the status is written whatever happens here. A failure is
// logged, named in the RemovalBlocked condition and tried again in
// privateStopRetry; a change is judged again in a second.
func (r *Reconciler) requestPrivateExecutorStop(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (ctrl.Result, bool, error) {
	if !r.privateStopApplies(s) || obs.privateHistoryUnknown != nil || obs.pod.missing || obs.pod.foreign != "" {
		return ctrl.Result{}, false, nil
	}
	if _, ok := privateStopNeeded(s, obs.pod.obj, r.clock()); !ok {
		return ctrl.Result{}, false, nil
	}
	if homeExpiryHeld(s) {
		r.countStop(stopHeld)
		return ctrl.Result{}, false, nil
	}
	failed := func(err error) (ctrl.Result, bool, error) {
		obs.privateStopErr = err
		log.FromContext(ctx).Error(err, "the stop request for a reaped private executor failed; retrying", "retry", privateStopRetry)
		return ctrl.Result{RequeueAfter: privateStopRetry}, false, nil
	}
	intent, err := r.stopIntentConfirmed(ctx, s)
	switch {
	case errors.Is(err, errStopHeld):
		r.countStop(stopHeld)
		return ctrl.Result{}, false, nil
	case errors.Is(err, errStopNotNeeded):
		return ctrl.Result{}, false, nil
	case errors.Is(err, errStopChanged):
		r.countStop(stopConflict)
		return ctrl.Result{RequeueAfter: time.Second}, false, nil
	case err != nil:
		return failed(err)
	}
	sent, err := r.stopPrivateExecutor(ctx, intent)
	switch {
	case errors.Is(err, errStopPodChanged):
		r.countStop(stopConflict)
		return ctrl.Result{RequeueAfter: time.Second}, false, nil
	case err != nil:
		return failed(err)
	case sent:
		r.countStop(stopSent)
		return ctrl.Result{RequeueAfter: time.Second}, false, nil
	}
	return ctrl.Result{}, false, nil
}

// stopIntentConfirmed records the stop intent and confirms it (section 4,
// points 1 and 2). It reads the Session uncached, checks again that it is
// Reaping, private and unheld, reads its recorded executor uncached and checks
// that it still needs the stop, then writes status.privateHistory.stopRequest
// by a status update that carries the resourceVersion of that read, and
// confirms it by an uncached re-read. When the intent is already stored (a
// retry, or an operator restart between the intent and the patch), the same
// update carries it unchanged: every patch follows a read that found no hold
// and a write that nothing overtook. A hold written before this read (after
// the reconcile's own read of s) is in fresh, and the hold check here refuses
// it; a hold written after this read changes the resourceVersion, so the write
// conflicts and nothing is sent. On success s is the confirmed Session.
func (r *Reconciler) stopIntentConfirmed(ctx context.Context, s *v1alpha1.AgentSession) (confirmedStopIntent, error) {
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		return confirmedStopIntent{}, err
	}
	if !r.privateStopApplies(fresh) {
		return confirmedStopIntent{}, errStopNotNeeded
	}
	if homeExpiryHeld(fresh) {
		return confirmedStopIntent{}, errStopHeld
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(fresh), &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return confirmedStopIntent{}, errStopNotNeeded
		}
		return confirmedStopIntent{}, fmt.Errorf("the recorded executor read before the stop intent: %w", err)
	}
	if _, ok := privateStopNeeded(fresh, &pod, r.clock()); !ok {
		return confirmedStopIntent{}, errStopNotNeeded
	}
	if fresh.DeletionTimestamp.IsZero() {
		// A terminal task is the narrow compute-only exception to reap-only
		// stop. Recheck hosts, HOME, children and proof before its CAS intent.
		v, err := r.readExpiryView(ctx, fresh)
		if err != nil || v.s.ResourceVersion != fresh.ResourceVersion {
			return confirmedStopIntent{}, errStopChanged
		}
		if _, err := r.terminalTaskViewAllowed(ctx, v); err != nil {
			return confirmedStopIntent{}, errStopNotNeeded
		}
	}
	h := fresh.Status.PrivateHistory
	if h.StopRequest == nil {
		h.StopRequest = &v1alpha1.PrivateExecutorStopRequestStatus{Version: 1, PodUID: h.LastExecutor.PodUID, RequestedAt: recordTime(r.clock())}
	}
	acknowledged, err := r.writePrivateHistoryAcknowledged(ctx, s, fresh)
	switch {
	case err != nil && (acknowledged || apierrors.IsConflict(err)):
		// A Conflict: the Session changed since the read, so the intent was
		// not written. Acknowledged: it was stored, but another write (a
		// heartbeat, say) overtook the confirming re-read. Either way the next
		// reconcile judges again, and adopts a stored intent.
		return confirmedStopIntent{}, fmt.Errorf("%w: %w", errStopChanged, err)
	case err != nil:
		return confirmedStopIntent{}, fmt.Errorf("the stop intent is unconfirmed: %w", err)
	}
	if sr := s.Status.PrivateHistory.StopRequest; sr == nil || sr.PodUID != s.Status.PrivateHistory.LastExecutor.PodUID {
		return confirmedStopIntent{}, errors.New("the stop intent is not stored for the recorded executor")
	}
	return confirmedStopIntent{confirmedSession: s.DeepCopy(), readExecutor: &pod}, nil
}

// stopPrivateExecutor sends the one stop patch (section 4, point 3) to the
// recorded executor that stopIntentConfirmed read: exactly a test of its UID, a
// test of its resourceVersion and the activeDeadlineSeconds. The tests make a
// Pod changed since that read refuse the patch; the API server answers a failed
// test with Invalid, so an Invalid is checked against a fresh read before it
// counts as a change. It is the only Pod write in the module besides create and
// the guarded deletes (TestOnlyStopPrivateExecutorPatchesPods). sent is false
// when the Pod no longer needs the stop.
func (r *Reconciler) stopPrivateExecutor(ctx context.Context, intent confirmedStopIntent) (sent bool, err error) {
	s, pod := intent.confirmedSession, intent.readExecutor
	if s == nil || pod == nil || s.Status.PrivateHistory == nil || s.Status.PrivateHistory.StopRequest == nil ||
		s.Status.PrivateHistory.LastExecutor == nil || s.Status.PrivateHistory.StopRequest.PodUID != s.Status.PrivateHistory.LastExecutor.PodUID {
		return false, errors.New("no confirmed stop intent for the recorded executor")
	}
	deadline, ok := privateStopNeeded(s, pod, r.clock())
	if !ok || string(pod.UID) != s.Status.PrivateHistory.StopRequest.PodUID || pod.ResourceVersion == "" {
		return false, nil
	}
	uid, rv := pod.UID, pod.ResourceVersion
	patch, err := json.Marshal(privateStopPatch(uid, rv, deadline))
	if err != nil {
		return false, err
	}
	if err := r.Client.Patch(ctx, pod, client.RawPatch(types.JSONPatchType, patch)); err != nil {
		switch {
		case apierrors.IsNotFound(err), apierrors.IsConflict(err):
			return false, fmt.Errorf("%w: %w", errStopPodChanged, err)
		case apierrors.IsInvalid(err):
			// A failed test, or a refusal that will not clear by itself (an
			// admission policy, Pod validation). Only a Pod that changed is a
			// change; anything else is a failure, retried slowly.
			var now corev1.Pod
			if rerr := r.APIReader.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}, &now); apierrors.IsNotFound(rerr) || rerr == nil && (now.UID != uid || now.ResourceVersion != rv) {
				return false, fmt.Errorf("%w: %w", errStopPodChanged, err)
			}
			return false, fmt.Errorf("the stop patch was refused: %w", err)
		}
		return false, err
	}
	log.FromContext(ctx).Info("requested the stop of a reaped private executor", "podUID", string(uid), "activeDeadlineSeconds", deadline)
	r.event(s, corev1.EventTypeNormal, "StopRequest", EventExecutorStopRequested,
		fmt.Sprintf("set activeDeadlineSeconds %d on the recorded private executor %s (DESIGN-025 4); the kubelet ends it and its terminal record follows", deadline, uid))
	return true, nil
}

// jsonPatchOp is one RFC 6902 operation.
type jsonPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// privateStopPatch is the stop patch: exactly two tests and one add.
func privateStopPatch(uid types.UID, resourceVersion string, deadline int64) []jsonPatchOp {
	return []jsonPatchOp{
		{Op: "test", Path: "/metadata/uid", Value: string(uid)},
		{Op: "test", Path: "/metadata/resourceVersion", Value: resourceVersion},
		{Op: "add", Path: "/spec/activeDeadlineSeconds", Value: deadline},
	}
}
