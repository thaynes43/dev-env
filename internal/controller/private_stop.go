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

// privateStopGrace is how long after the patch the kubelet's deadline falls:
// activeDeadlineSeconds counts from the Pod's start, so the operator sets it
// to the Pod's age plus this.
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
// no longer in the shape a stop needs), or a Conflict.
var (
	errStopHeld      = errors.New("an owner hold on the HOME expiry blocks the stop request")
	errStopNotNeeded = errors.New("the Session or its recorded executor no longer needs a stop request")
	errStopChanged   = errors.New("the Session changed under the stop intent write")
)

// homeExpiryHeld reports whether the owner override blocks the stop. Any
// value but release-now counts as a hold, so a mistyped hold never lets a stop
// through (fail closed); section 9's override_invalid count belongs to the
// expiry (H5).
func homeExpiryHeld(s *v1alpha1.AgentSession) bool {
	v, ok := s.Annotations[v1alpha1.AnnotationHomeExpiry]
	return ok && v != v1alpha1.HomeExpiryReleaseNow
}

// privateStopShape reports whether the Session may have its executor stopped,
// judged on its own fields: it is Reaping, private (no workspace, no canary
// binding), its retention record is intact with a Started admission, a bound
// HOME and a recorded executor, and the executor has no terminal record or
// missed marker yet. A Suspended Session is never stopped, before or after its
// archive deadline (ADR-006; plan 11 follow-up F3 is not planned).
func privateStopShape(s *v1alpha1.AgentSession) bool {
	h := s.Status.PrivateHistory
	return !s.DeletionTimestamp.IsZero() && s.Spec.Workspace == nil && s.Spec.ProjectCanary == nil &&
		h != nil && privateHistoryIdentity(s) == nil && h.Admission == sharedStarted && h.HomeUID != "" &&
		h.LastExecutor != nil && h.LastExecutor.PodUID != "" && h.ExecutorTerminal == nil && h.ExecutorMissed == nil &&
		(h.StopRequest == nil || h.StopRequest.PodUID == h.LastExecutor.PodUID)
}

// privateStopDeadline is the activeDeadlineSeconds the stop sets: the Pod's
// age at now, rounded up, plus privateStopGrace. ok is false when the Pod has
// no start time, so no deadline can be computed.
func privateStopDeadline(pod *corev1.Pod, now time.Time) (int64, bool) {
	if pod.Status.StartTime == nil {
		return 0, false
	}
	age := max(0, math.Ceil(now.Sub(pod.Status.StartTime.Time).Seconds()))
	return int64(age) + int64(privateStopGrace/time.Second), true
}

// privateStopNeeded reports whether pod is the Session's recorded executor and
// still needs the stop: the Session controls it, it is not a hold pod, it is
// not terminal or deleting, it has started, and it has no deadline at or
// below the one the stop would set. A Pod that already has one is left alone,
// which is what keeps the patch to one per executor across reconciles,
// restarts and replicas.
func privateStopNeeded(s *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) (int64, bool) {
	h := s.Status.PrivateHistory
	if h == nil || h.LastExecutor == nil || pod.UID == "" || string(pod.UID) != h.LastExecutor.PodUID || !controlledBySession(pod, s) || isHoldPod(pod) ||
		podPhaseTerminal(pod) || !pod.DeletionTimestamp.IsZero() {
		return 0, false
	}
	deadline, ok := privateStopDeadline(pod, now)
	if !ok || pod.Spec.ActiveDeadlineSeconds != nil && *pod.Spec.ActiveDeadlineSeconds <= deadline {
		return 0, false
	}
	return deadline, true
}

// requestPrivateExecutorStop is DESIGN-025 section 4. It runs from removePod,
// for a Session that wants its Pod gone and still has one, after this
// reconcile's private history observation changed nothing. stop means the
// reconcile ends here with res.
func (r *Reconciler) requestPrivateExecutorStop(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (res ctrl.Result, stop bool, err error) {
	if !r.PrivateExecutorStop || !r.PrivateTerminalRecord || !privateStopShape(s) || obs.privateHistoryUnknown != nil ||
		obs.pod.missing || obs.pod.foreign != "" {
		return ctrl.Result{}, false, nil
	}
	if _, ok := privateStopNeeded(s, obs.pod.obj, r.clock()); !ok {
		return ctrl.Result{}, false, nil
	}
	if homeExpiryHeld(s) {
		r.countStop(stopHeld)
		return ctrl.Result{}, false, nil
	}
	confirmed, err := r.stopIntentConfirmed(ctx, s)
	switch {
	case errors.Is(err, errStopHeld):
		r.countStop(stopHeld)
		return ctrl.Result{}, false, nil
	case errors.Is(err, errStopNotNeeded):
		return ctrl.Result{}, false, nil
	case errors.Is(err, errStopChanged):
		r.countStop(stopConflict)
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	case err != nil:
		return ctrl.Result{RequeueAfter: time.Second}, true, err
	}
	sent, err := r.stopPrivateExecutor(ctx, confirmed)
	switch {
	case apierrors.IsConflict(err) || apierrors.IsInvalid(err):
		// The Pod changed since the read (a failed test is Invalid): judge
		// again from the start.
		r.countStop(stopConflict)
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	case err != nil:
		return ctrl.Result{RequeueAfter: time.Second}, true, err
	case sent:
		r.countStop(stopSent)
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	return ctrl.Result{}, false, nil
}

// stopIntentConfirmed records the stop intent and confirms it (section 4,
// points 1 and 2). It reads the Session uncached, checks again that it is
// Reaping, private and unheld, and that its recorded executor still needs the
// stop, then writes status.privateHistory.stopRequest by a status update that
// carries the resourceVersion of that read, and confirms it by an uncached
// re-read. When the intent is already stored (a retry, or an operator restart
// between the intent and the patch), the same update carries it unchanged:
// every patch follows a read that found no hold and a write that nothing
// overtook. A hold written after the read changes the resourceVersion, so the
// write conflicts and nothing is sent. It returns the confirmed Session.
func (r *Reconciler) stopIntentConfirmed(ctx context.Context, s *v1alpha1.AgentSession) (*v1alpha1.AgentSession, error) {
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		return nil, err
	}
	if !privateStopShape(fresh) {
		return nil, errStopNotNeeded
	}
	if homeExpiryHeld(fresh) {
		return nil, errStopHeld
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(fresh), &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errStopNotNeeded
		}
		return nil, fmt.Errorf("the recorded executor read before the stop intent: %w", err)
	}
	if _, ok := privateStopNeeded(fresh, &pod, r.clock()); !ok {
		return nil, errStopNotNeeded
	}
	h := fresh.Status.PrivateHistory
	if h.StopRequest == nil {
		h.StopRequest = &v1alpha1.PrivateExecutorStopRequestStatus{Version: 1, PodUID: h.LastExecutor.PodUID, RequestedAt: recordTime(r.clock())}
	}
	confirmed := fresh.DeepCopy()
	if err := r.writePrivateHistory(ctx, confirmed, fresh); err != nil {
		if apierrors.IsConflict(err) {
			return nil, fmt.Errorf("%w: %w", errStopChanged, err)
		}
		return nil, fmt.Errorf("the stop intent is unconfirmed: %w", err)
	}
	if sr := confirmed.Status.PrivateHistory.StopRequest; sr == nil || sr.PodUID != confirmed.Status.PrivateHistory.LastExecutor.PodUID {
		return nil, errors.New("the stop intent is not stored for the recorded executor")
	}
	*s = *confirmed
	return confirmed, nil
}

// stopPrivateExecutor sends the one stop patch (section 4, point 3) to the
// recorded executor of s, which stopIntentConfirmed returned. It reads the Pod
// uncached and, when it still needs the stop, patches exactly a test of its
// UID, a test of its resourceVersion and the activeDeadlineSeconds. It is the
// only Pod write in the module besides create and the guarded deletes
// (TestOnlyStopPrivateExecutorPatchesPods). sent is false when the Pod no
// longer needs the stop.
func (r *Reconciler) stopPrivateExecutor(ctx context.Context, s *v1alpha1.AgentSession) (sent bool, err error) {
	h := s.Status.PrivateHistory
	if h == nil || h.StopRequest == nil || h.LastExecutor == nil || h.StopRequest.PodUID != h.LastExecutor.PodUID {
		return false, errors.New("no confirmed stop intent for the recorded executor")
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &pod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	deadline, ok := privateStopNeeded(s, &pod, r.clock())
	if !ok || string(pod.UID) != h.StopRequest.PodUID || pod.ResourceVersion == "" {
		return false, nil
	}
	patch, err := json.Marshal(privateStopPatch(pod.UID, pod.ResourceVersion, deadline))
	if err != nil {
		return false, err
	}
	if err := r.Client.Patch(ctx, &pod, client.RawPatch(types.JSONPatchType, patch)); err != nil {
		return false, err
	}
	log.FromContext(ctx).Info("requested the stop of a reaped private executor", "podUID", string(pod.UID), "activeDeadlineSeconds", deadline)
	r.event(s, corev1.EventTypeNormal, "StopRequest", EventExecutorStopRequested,
		fmt.Sprintf("set activeDeadlineSeconds %d on the recorded private executor %s (DESIGN-025 4); the kubelet ends it and its terminal record follows", deadline, pod.UID))
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
