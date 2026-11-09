package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

// Transient rescue retry (DESIGN-001 4.4, OpenProject #486). A rescue that
// failed only because origin could not be fetched (a DNS or router outage,
// GitHub down) runs again by itself, with a wait that doubles from
// HoldRescueRetry up to transientRetryCapDoublings doublings. It reruns the
// one rescue path (D-51): in a hold pod (D-55) when the session has no pod, as
// a suspend leaves it after a failed rescue. The retry only creates a hold pod
// and execs the rescue; it deletes and archives nothing, and every guard of
// DESIGN-001 5.1 still decides what may go.

// transientRescueReasons are the verdict reasons a retry can clear on its
// own. A reason is here only if the cause lies outside the volume and a rerun
// of the rescue can neither lose work nor change the volume beyond what the
// first run did: the rescue commits through a copy of the index, only adds
// rescue branches and a new bundle stamp, and deletes nothing (D-43, D-48).
//
//   - FetchFailed: `git fetch origin` failed (DNS, network, GitHub, an expired
//     token). A rerun fetches again.
//
// Every other reason stays permanent: WorktreeRefused, NotProven and
// BootstrapInterrupted need a human or a source change; ReportMismatch,
// AgentNotStopped and the bare RescueFailed mean agentd and the operator
// disagree; AgentStillRunning means the agent may have written after the
// rescue; RepoError is a local git failure; BundleFailed can be a full or
// broken shared volume as easily as a passing outage. When in doubt, a reason
// is permanent.
var transientRescueReasons = map[string]bool{"FetchFailed": true}

// transientRescueReason reports whether reason is one a retry can clear.
func transientRescueReason(reason string) bool { return transientRescueReasons[reason] }

// transientRetryCapDoublings caps the backoff: HoldRescueRetry (15 minutes)
// doubles twice, to an hour. So retries run 15, 45 and 105 minutes after the
// first failure and then every hour: an outage that ends is cleared within the
// hour, and a long one costs one fetch per clone an hour.
const transientRetryCapDoublings = 2

// transientRetryDelay is the wait after the failures-th transient failure in a
// row: base, doubled per earlier failure, at most transientRetryCapDoublings
// times. A count below one is treated as one.
func transientRetryDelay(base time.Duration, failures int32) time.Duration {
	doublings := min(max(failures, 1)-1, transientRetryCapDoublings)
	return base << doublings
}

// transientRetryApplies reports whether the transient retry covers the
// session: an ordinary session that is suspended, not reaped, whose suspend
// runs the rescue path of D-51. A reap keeps its hold pod's fixed retry
// (D-55), and a shared workspace task and a private Session with retained
// history keep their own rescue paths and fixed retry (D-77, DESIGN-025).
func transientRetryApplies(s *v1alpha1.AgentSession) bool {
	return s.DeletionTimestamp.IsZero() && s.Spec.Workspace == nil && !privateHistoryProtected(s)
}

// transientRetryRefused says why a session the retry covers gets no hold pod
// for it, as archive gives it none (DESIGN-001 5.1, OP252): a private
// lifecycle the retention flag marks, or an original-HOME restriction. Nil
// means none. The record of a refused session gets no retry time, so its
// message never promises one.
func (r *Reconciler) transientRetryRefused(s *v1alpha1.AgentSession) error {
	if r.privateHistoryEnabled(s) {
		return errPrivateHistoryRetained
	}
	if _, exists := s.Annotations[homeguard.Annotation]; exists {
		return homeguard.ErrUnsafe
	}
	if b, err := homeguard.Select(r.OriginalHome, s.Namespace, s.Name, string(s.UID), s.Annotations); b != nil || err != nil {
		return homeguard.ErrUnsafe
	}
	return nil
}

// transientFailures is how many rescues in a row, the newest included, failed
// for a transient reason only; zero when the newest did not. A Failed record
// without the count, from an operator before #486 or with the field pruned by
// a stale CRD, counts as one when its RescueFailed condition names a transient
// reason.
func transientFailures(s *v1alpha1.AgentSession) int32 {
	r := s.Status.Rescue
	switch {
	case r == nil || r.Result != v1alpha1.RescueFailed || r.Superseded || !transientRetryApplies(s):
		return 0
	case r.TransientFailures > 0:
		return r.TransientFailures
	case r.NextRetryAt == nil && transientRescueReason(rescueReason(&s.Status)):
		return 1
	}
	return 0
}

// transientRetryAt is when the newest rescue, which failed for a transient
// reason only, runs again; ok is false for any other record. A record without
// NextRetryAt (see transientFailures) is due one base wait after it ran.
func transientRetryAt(s *v1alpha1.AgentSession, base time.Duration) (at time.Time, ok bool) {
	r := s.Status.Rescue
	n := transientFailures(s)
	switch {
	case n == 0:
		return time.Time{}, false
	case r.NextRetryAt != nil:
		return r.NextRetryAt.Time, true
	case r.At == nil:
		return time.Time{}, true
	}
	return r.At.Add(transientRetryDelay(base, n)), true
}

// scheduleTransientRetry fills a new rescue record's retry fields when its
// verdict failed for a transient reason only, and says when the rescue runs
// again in its message (which the RescueFailed condition and the event
// carry). reason is the verdict's: verdict names a transient reason only when
// every problem was transient. The count continues the session's newest
// record when that one failed transiently too.
func scheduleTransientRetry(s *v1alpha1.AgentSession, rec *v1alpha1.RescueStatus, reason string, base time.Duration) {
	if rec == nil || rec.Result != v1alpha1.RescueFailed || rec.At == nil || !transientRescueReason(reason) || !transientRetryApplies(s) {
		return
	}
	n := int32(min(int64(transientFailures(s))+1, math.MaxInt32))
	next := metav1.NewTime(rec.At.Add(transientRetryDelay(base, n)))
	rec.TransientFailures, rec.NextRetryAt = n, &next
	rec.Message = withSuffix(rec.Message, fmt.Sprintf("; every problem is transient, so the rescue runs again at %s (transient failure %d in a row)",
		next.UTC().Format(time.RFC3339), n))
}

// withSuffix appends suffix to msg within maxMessage, cutting msg rather than
// the suffix.
func withSuffix(msg, suffix string) string {
	if len(msg)+len(suffix) <= maxMessage {
		return msg + suffix
	}
	keep := max(0, maxMessage-len(suffix)-3)
	return truncate(msg[:keep] + "..." + suffix)
}

// nextRescueRetry is when a failed rescue in a hold pod runs again: at its
// transient retry time, else retry after it ran (D-55). ok is false when the
// newest rescue did not fail.
func nextRescueRetry(s *v1alpha1.AgentSession, retry time.Duration) (time.Time, bool) {
	r := s.Status.Rescue
	if r == nil || r.Result != v1alpha1.RescueFailed {
		return time.Time{}, false
	}
	if at, ok := transientRetryAt(s, retry); ok {
		return at, true
	}
	if r.At == nil {
		return time.Time{}, true
	}
	return r.At.Add(retry), true
}

// retryTransientRescue gives a suspended session with no pod, whose newest
// rescue failed for a transient reason only, a hold pod once the retry is due,
// before its archive timer would (D-55, D-62). The hold pod runs the rescue as
// any hold pod does. It makes the checks archive makes before a hold pod, on
// the session and the pod as the API server has them, and creates nothing
// otherwise. It never deletes or archives anything.
func (r *Reconciler) retryTransientRescue(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (ctrl.Result, error) {
	at, ok := transientRetryAt(s, r.holdRetry())
	if !ok || s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero() ||
		obs.claim.missing || obs.claim.foreign != "" || r.APIReader == nil {
		return ctrl.Result{}, nil
	}
	if wait := at.Sub(obs.now); wait > 0 {
		return ctrl.Result{RequeueAfter: wait + time.Second}, nil
	}
	// The sessions archive gives no hold pod get none here either, and
	// RemovalBlocked says why an older record's retry does not run.
	if why := r.transientRetryRefused(s); why != nil {
		obs.removalBlocked = fmt.Errorf("%w only in a hold pod, which this session does not get: %w", errTransientRetry, why)
		return ctrl.Result{}, nil
	}
	// Judge the newest session, and ask the API server that no pod exists: a
	// lagging cache must not make a second hold pod, or one for a session that
	// a resume or a reap changed meanwhile.
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if fresh.UID != s.UID || fresh.ResourceVersion != s.ResourceVersion {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Name}, &corev1.Pod{})
	switch {
	case err == nil:
		return ctrl.Result{RequeueAfter: time.Second}, nil
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}
	n := transientFailures(s)
	obs.removalBlocked = fmt.Errorf("%w: the newest rescue failed for a transient reason only (transient failure %d in a row), so a hold pod runs it again (D-55): %s",
		errTransientRetry, n, s.Status.Rescue.Message)
	log.FromContext(ctx).Info("retrying a transiently failed rescue in a hold pod", "failures", n, "stamp", s.Status.Rescue.Stamp)
	return ctrl.Result{}, r.startHoldPod(ctx, s, obs)
}

// errTransientRetry is why a suspended session with no pod gets a hold pod
// before its archive timer: its newest rescue failed for a transient reason
// only, and the retry is due.
var errTransientRetry = errors.New("a transient rescue failure is retried")
