package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

// The scheduled-work releaser (DESIGN-054 "Occurrence lifecycle", S3a). A
// session the executor dispatches carries v1alpha1.ScheduledWorkFinalizer from
// its atomic Create, so it never vanishes before its occurrence's outcome is in
// the ScheduledWork's status. The releaser is the one path that removes that
// finalizer. It runs in the AgentSession reconciler, which always runs: it is
// not behind --enable-scheduled-work and it is not the executor. It acts only
// on a session that carries the finalizer and has ended by the session-end rule
// (scheduledwork.SessionEnded), or that is being deleted. It reads the
// ScheduledWork with the uncached API reader only, so the operator starts no
// watch or informer on a kind S6 may not have installed yet. It writes the
// occurrence's terminal record with a compare-and-set status Update, then lifts
// only its own finalizer. It never deletes, suspends or otherwise changes a
// session, and never touches another finalizer; the rescue and archive keep
// theirs, and neither waits on the other.

// The releaser's metrics. MetricScheduledWorkReleaseBlocked is DESIGN-054's
// page source: a gauge of the sessions, labelled or not, whose finalizer is
// still on although they are being deleted or have ended, read from the cache
// at scrape time (ScheduledWorkReleaseCollector), so every replica reports the
// same value and a deposed leader leaves nothing stale. The releaser lifts the
// finalizer within seconds, so a session that stays counted for long is
// blocked; a haynes-ops PrometheusRule (S6) pages on it with a `for:` longer
// than the expected waits (the reported-outcome wait, scheduledwork.ReportWait,
// and an unserved kind's wait for the start deadline).
// MetricScheduledWorkReleaseHolds counts each held attempt by reason, for the
// diagnosis; it is not the page.
const (
	MetricScheduledWorkReleaseBlocked = "dev_env_scheduled_work_release_blocked"
	MetricScheduledWorkReleaseHolds   = "dev_env_scheduled_work_release_holds_total"
	// MetricScheduledWorkLateSessions counts the releases of a session whose
	// occurrence was already final without naming it.
	MetricScheduledWorkLateSessions = "dev_env_scheduled_work_late_sessions_total"
)

// NewScheduledWorkLateSessionsCounter returns the counter of
// MetricScheduledWorkLateSessions. Register it once per process.
func NewScheduledWorkLateSessionsCounter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Name: MetricScheduledWorkLateSessions,
		Help: "Scheduled sessions released whose occurrence was already final without naming them: a create that landed after another leader's final record (DESIGN-054). The launch gate refuses such a session's first pod; one that started anyway ran unrecorded.",
	})
}

// The blocked counter's reason label values.
const (
	// releaseBlockedMetadata: the session's label or annotations are missing or
	// unreadable, or its name is not the one they derive, so the releaser
	// cannot tell which occurrence it holds.
	releaseBlockedMetadata = "metadata"
	// releaseBlockedRead: a read of the session, its pod, the ScheduledWork or
	// its CustomResourceDefinition failed.
	releaseBlockedRead = "read"
	// releaseBlockedKindUnserved: the kind is not served, or a NotFound does
	// not name the item, and either its CRD still exists (discovery lags) or
	// the session's start deadline has not passed, so an executor could still
	// create for it.
	releaseBlockedKindUnserved = "kind_unserved"
	// releaseBlockedStatusWrite: the terminal record could not be written.
	releaseBlockedStatusWrite = "status_write"
	// releaseBlockedPatch: the record is stored but the finalizer patch failed.
	releaseBlockedPatch = "finalizer_patch"
)

var releaseBlockedReasons = []string{releaseBlockedMetadata, releaseBlockedRead, releaseBlockedKindUnserved, releaseBlockedStatusWrite, releaseBlockedPatch}

// NewScheduledWorkReleaseHoldsCounter returns the counter of
// MetricScheduledWorkReleaseHolds, with each reason's series present at 0.
// Register it once per process.
func NewScheduledWorkReleaseHoldsCounter() *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricScheduledWorkReleaseHolds,
		Help: "Release attempts the scheduled-work releaser held back although the session ended or is being deleted (DESIGN-054 S3a), by reason: metadata, read, kind_unserved, status_write, finalizer_patch. Each held attempt counts once; page on dev_env_scheduled_work_release_blocked instead.",
	}, []string{"reason"})
	for _, reason := range releaseBlockedReasons {
		c.WithLabelValues(reason)
	}
	return c
}

// scheduledWorkCRD is the CustomResourceDefinition the releaser reads,
// uncached, before it takes a missing ScheduledWork or an unserved kind as
// gone. S6 grants get on this one object.
const scheduledWorkCRD = "scheduledworks.dev-env.haynesops.com"

var crdKind = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

// scheduledWorkReleaseRetry is how soon a held release is tried again.
const scheduledWorkReleaseRetry = 30 * time.Second

// scheduledWorkProofRetry is how soon a session whose agent reports an end
// that its pod cannot prove yet (a pod younger than standby.PodGrace) is
// judged again, should no heartbeat come first.
const scheduledWorkProofRetry = time.Minute

// ScheduledWorkSweepEvery is how often the sweep lists the labelled sessions
// from the API server, so that a missed event cannot strand one.
const ScheduledWorkSweepEvery = 5 * time.Minute

// itemRead is what the uncached read of a session's ScheduledWork found.
type itemRead int

const (
	// itemFound: the item of that name exists (its UID may differ).
	itemFound itemRead = iota + 1
	// itemMissing: no item of that name, while its CRD exists. A person
	// deleted it (the API offers only cancel), so nothing can redispatch it.
	itemMissing
	// itemKindGone: the kind is not served, or the item is not found, and an
	// uncached read of its CRD is NotFound.
	itemKindGone
)

// scheduledWorkReleaseWhy is why the releaser may lift the finalizer.
type scheduledWorkReleaseWhy string

const (
	// swReleaseRecorded: the occurrence's terminal state is in the item's
	// status, written now or before.
	swReleaseRecorded scheduledWorkReleaseWhy = "the occurrence's terminal record is stored"
	// swReleaseNotTracked: the item records no occurrence for this session.
	swReleaseNotTracked scheduledWorkReleaseWhy = "the ScheduledWork records no occurrence for this session"
	// swReleaseReplaced: an item of that name exists with another UID: the one
	// that dispatched the session was deleted and recreated.
	swReleaseReplaced scheduledWorkReleaseWhy = "the ScheduledWork of that name has another UID"
	// swReleaseItemGone: the item is gone while its kind is served.
	swReleaseItemGone scheduledWorkReleaseWhy = "the ScheduledWork is gone"
	// swReleaseKindGone: the kind is not served, its CRD is gone, and the
	// session's start deadline has passed, so no executor creates for it again.
	swReleaseKindGone scheduledWorkReleaseWhy = "the ScheduledWork kind is not served, its CRD is gone and the start deadline has passed"
)

// scheduledWorkRelease is the evidence the guard judges a release by: why the
// caller says the finalizer may go, and the reads that must show it.
type scheduledWorkRelease struct {
	why scheduledWorkReleaseWhy
	// pod is the uncached pod of the session's name for the session-end
	// rule's pod-age proof; nil when none was read or there is none.
	pod *corev1.Pod
	// read and work are the uncached read of the item; work is nil unless
	// read is itemFound, and holds the stored record after a write.
	read itemRead
	work *v1alpha1.ScheduledWork
	// at is the occurrence instant from the session's annotation.
	at time.Time
	// now is the releaser's clock, for the session-end rule and the start
	// deadline.
	now time.Time
}

// scheduledWorkReleaseAllowed is nil only when the releaser may lift
// v1alpha1.ScheduledWorkFinalizer from s: s carries it, is being deleted or has
// ended by the session-end rule (judged here, on s and the evidence's pod),
// and the evidence shows the reason the caller gives. A recorded or
// untracked occurrence needs the item read with the UID of the session's
// label; a replaced item another UID; a gone item the missing read; an
// unserved kind its CRD gone and the start deadline in s passed.
func scheduledWorkReleaseAllowed(s *v1alpha1.AgentSession, ev scheduledWorkRelease) error {
	uid := s.Labels[v1alpha1.LabelScheduledWork]
	switch {
	case !controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer):
		return errors.New("the session does not carry the scheduled-work finalizer")
	case s.DeletionTimestamp.IsZero() && !sessionEnded(s, ev.pod, ev.now):
		return errors.New("the session has not ended and is not being deleted, so the scheduled-work finalizer stays")
	case uid == "":
		return errors.New("the session has no scheduled-work label")
	}
	// Whatever the reason, the finalizer holds the derived name until the
	// start deadline plus the margin, so a late create of the name cannot
	// launch an agent (DESIGN-054, scheduledwork.ReleaseMargin).
	if until, err := scheduledwork.ReleaseHeldUntil(s); err != nil || !ev.now.After(until) {
		return errors.New("the scheduled-work finalizer stays until the session's start deadline plus the release margin has passed")
	}
	matching := ev.read == itemFound && ev.work != nil && string(ev.work.UID) == uid
	switch ev.why {
	case swReleaseRecorded:
		if matching {
			if o := scheduledwork.Find(ev.work, ev.at); o != nil && v1alpha1.OccurrenceTerminal(o.State) {
				return nil
			}
		}
	case swReleaseNotTracked:
		if matching {
			if o := scheduledwork.Find(ev.work, ev.at); o == nil || (o.Session != "" && o.Session != s.Name) {
				return nil
			}
		}
	case swReleaseReplaced:
		if ev.read == itemFound && ev.work != nil && string(ev.work.UID) != uid {
			return nil
		}
	case swReleaseItemGone:
		if ev.read == itemMissing && ev.work == nil {
			return nil
		}
	case swReleaseKindGone:
		deadline, err := scheduledwork.StartDeadline(s)
		if ev.read == itemKindGone && ev.work == nil && err == nil && ev.now.After(deadline) {
			return nil
		}
	}
	return fmt.Errorf("the scheduled-work finalizer stays: the reads do not show that %s", ev.why)
}

// releaseScheduledWorkFinalizer lifts v1alpha1.ScheduledWorkFinalizer, and no
// other, from s if the guard allows it, by a merge patch that carries s's
// resourceVersion, so a session changed since the releaser's read is never
// released on stale evidence. Only releaseScheduledWork calls it
// (TestOnlyTheGuardDeletes, TestOnlyTheReleaserRemovesTheScheduledWorkFinalizer).
func releaseScheduledWorkFinalizer(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, ev scheduledWorkRelease) error {
	if err := scheduledWorkReleaseAllowed(s, ev); err != nil {
		return err
	}
	orig := s.DeepCopy()
	controllerutil.RemoveFinalizer(s, v1alpha1.ScheduledWorkFinalizer)
	return c.Patch(ctx, s, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{}))
}

// releaseScheduledWork runs the releaser for one session that carries the
// finalizer. released means the finalizer is gone; otherwise res says when to
// look again (zero: when the session next changes). It returns no error: a
// held release is counted, logged and retried, and the rest of the reconcile
// goes on, so the rescue and archive never wait on it.
func (r *Reconciler) releaseScheduledWork(ctx context.Context, cached *v1alpha1.AgentSession) (released bool, res ctrl.Result) {
	if r.releaseJudged != nil {
		defer func() { r.releaseJudged(cached, released, res) }()
	}
	lg := log.FromContext(ctx).WithValues("scheduledWork", cached.Annotations[v1alpha1.AnnotationScheduledWorkName])
	now := r.clock()
	// The cached copy screens out the sessions with nothing to release, so a
	// running session costs no read. The decision itself rests on the API
	// server's session and pod.
	if cached.DeletionTimestamp.IsZero() {
		if ended, _ := scheduledwork.SessionEnded(cached, nil, now); !ended && !scheduledwork.NeedsPodProof(cached) {
			return false, ctrl.Result{}
		}
	}
	if r.APIReader == nil {
		return false, r.releaseBlocked(lg, releaseBlockedRead, errors.New("the operator has no uncached reader"))
	}
	var s v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(cached), &s); err != nil {
		if apierrors.IsNotFound(err) {
			return false, ctrl.Result{}
		}
		return false, r.releaseBlocked(lg, releaseBlockedRead, fmt.Errorf("read the session: %w", err))
	}
	if s.UID != cached.UID || !controllerutil.ContainsFinalizer(&s, v1alpha1.ScheduledWorkFinalizer) {
		// The cache is behind; the newer version's event reconciles again.
		return false, ctrl.Result{}
	}
	ev := scheduledWorkRelease{now: now}
	ended, why := scheduledwork.SessionEnded(&s, nil, now)
	switch {
	case !s.DeletionTimestamp.IsZero():
		// A deletion alone allows the release: no pod proof is needed.
		why = "the session is being deleted"
	case !ended && scheduledwork.NeedsPodProof(&s):
		pod, err := r.uncachedSessionPod(ctx, &s)
		if err != nil {
			return false, r.releaseBlocked(lg, releaseBlockedRead, err)
		}
		if ended, why = scheduledwork.SessionEnded(&s, pod, now); !ended {
			return false, ctrl.Result{RequeueAfter: scheduledWorkProofRetry}
		}
		ev.pod = pod
	case !ended:
		return false, ctrl.Result{}
	}

	name, uid := s.Annotations[v1alpha1.AnnotationScheduledWorkName], s.Labels[v1alpha1.LabelScheduledWork]
	at, err := scheduledwork.OccurrenceAt(&s)
	switch {
	case err != nil:
		return false, r.releaseBlocked(lg, releaseBlockedMetadata, err)
	case name == "" || uid == "":
		return false, r.releaseBlocked(lg, releaseBlockedMetadata, fmt.Errorf("the session lacks annotation %s or label %s", v1alpha1.AnnotationScheduledWorkName, v1alpha1.LabelScheduledWork))
	}

	// The name is the one the label and occurrence derive, so the occurrence
	// this session holds is the one its name says.
	if want, err := scheduledwork.SessionName(uid, at); err != nil || s.Name != want {
		if err == nil {
			err = fmt.Errorf("the session's name is not %s, the one its label and occurrence annotation derive", want)
		}
		return false, r.releaseBlocked(lg, releaseBlockedMetadata, err)
	}

	ev.at = at
	var result scheduledwork.RecordResult
	for range 5 {
		result = ""
		w, read, err := r.readScheduledWork(ctx, r.scheduledWorkNamespace(), name)
		if err != nil {
			reason := releaseBlockedRead
			if errors.Is(err, errKindUnservedCRDPresent) {
				reason = releaseBlockedKindUnserved
			}
			return false, r.releaseBlocked(lg, reason, err)
		}
		ev.read, ev.work = read, w
		switch {
		case read == itemMissing:
			ev.why = swReleaseItemGone
		case read == itemKindGone:
			deadline, err := scheduledwork.StartDeadline(&s)
			if err != nil {
				return false, r.releaseBlocked(lg, releaseBlockedMetadata, fmt.Errorf("the ScheduledWork kind is not served and its CRD is gone, but the start deadline is unknown: %w", err))
			}
			if !now.After(deadline) {
				held := r.releaseBlocked(lg, releaseBlockedKindUnserved, fmt.Errorf("the ScheduledWork kind is not served and its CRD is gone; the release waits for the start deadline %s", deadline.UTC().Format(time.RFC3339)))
				return false, sooner(held, ctrl.Result{RequeueAfter: deadline.Sub(now) + time.Second})
			}
			ev.why = swReleaseKindGone
		case string(w.UID) != uid:
			ev.why = swReleaseReplaced
		default:
			result = scheduledwork.Record(w, at, s.Name, scheduledwork.Outcome(&s, now))
			switch result {
			case scheduledwork.Recorded, scheduledwork.ReportedAdded:
				if err := r.Client.Status().Update(ctx, w); err != nil {
					if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
						continue
					}
					return false, r.releaseBlocked(lg, releaseBlockedStatusWrite, fmt.Errorf("write the occurrence's terminal record: %w", err))
				}
				ev.why = swReleaseRecorded
			case scheduledwork.AlreadyTerminal:
				ev.why = swReleaseRecorded
			default:
				ev.why = swReleaseNotTracked
			}
		}
		break
	}
	if ev.why == "" {
		return false, r.releaseBlocked(lg, releaseBlockedStatusWrite, errors.New("the terminal record met a conflict five times in a row"))
	}
	if ev.why == swReleaseRecorded {
		// The record is in; the finalizer stays a little longer while the
		// agent's retained outcome may still land, so it is copied.
		if wait, until := scheduledwork.AwaitsReport(&s, scheduledwork.Find(ev.work, at), now); wait {
			lg.V(1).Info("the occurrence is recorded; waiting for the agent's reported outcome", "until", until.UTC().Format(time.RFC3339))
			return false, ctrl.Result{RequeueAfter: until.Sub(now) + time.Second}
		}
	}
	// The record is in (or there is none to write). The finalizer still
	// holds the derived name until the start deadline plus the margin, so a
	// stalled or deposed leader's late create of the same name cannot
	// succeed before agentd would refuse its first launch. The wait is
	// expected: it is not counted as a hold, and the blocked gauge leaves
	// it out.
	until, err := scheduledwork.ReleaseHeldUntil(&s)
	if err != nil {
		return false, r.releaseBlocked(lg, releaseBlockedMetadata, fmt.Errorf("the start deadline is unknown, so the finalizer keeps the derived name: %w", err))
	}
	if !now.After(until) {
		lg.V(1).Info("the occurrence is recorded; the finalizer keeps the derived name until the start deadline plus the margin", "until", until.UTC().Format(time.RFC3339))
		return false, ctrl.Result{RequeueAfter: until.Sub(now) + time.Second}
	}
	if err := releaseScheduledWorkFinalizer(ctx, r.Client, &s, ev); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return true, ctrl.Result{}
		case apierrors.IsConflict(err):
			return false, ctrl.Result{RequeueAfter: time.Second}
		}
		return false, r.releaseBlocked(lg, releaseBlockedPatch, err)
	}
	state := "none"
	if ev.work != nil {
		if o := scheduledwork.Find(ev.work, at); o != nil {
			state = o.State
			if result == scheduledwork.AlreadyTerminal && o.Session == "" {
				// A create that landed after another leader's final record:
				// the occurrence never names this session. The launch gate
				// refuses its first pod; one that started anyway ran
				// unrecorded.
				lg.Info("released a scheduled session whose occurrence was already final without it; its run, if any, is not recorded", "occurrence", at.Format(time.RFC3339), "state", o.State)
				if r.ScheduledWorkLateSessions != nil {
					r.ScheduledWorkLateSessions.Inc()
				}
			}
		}
	}
	lg.Info("released a scheduled session", "ended", why, "release", string(ev.why), "record", string(result), "occurrence", at.Format(time.RFC3339), "state", state)
	r.event(&s, corev1.EventTypeNormal, "Release", "ScheduledWorkReleased",
		fmt.Sprintf("%s; %s (occurrence %s, %s)", why, ev.why, at.Format(time.RFC3339), state))
	return true, ctrl.Result{}
}

// scheduledWorkNamespace is where the ScheduledWork objects live: the
// configured namespace, else the templates' (the operator's own, where the
// Reminders live too). The session's annotation carries only the name.
func (r *Reconciler) scheduledWorkNamespace() string {
	if r.ScheduledWorkNamespace != "" {
		return r.ScheduledWorkNamespace
	}
	return r.Templates.Namespace
}

// errKindUnservedCRDPresent is a read of an unserved kind, or a NotFound that
// does not name the item, while the CRD still exists: discovery lags, or the
// version is not served, so the objects are not proven gone.
var errKindUnservedCRDPresent = errors.New("the ScheduledWork kind is not served, or the NotFound does not name the item, but its CustomResourceDefinition exists")

// readScheduledWork reads the session's ScheduledWork from the API server. A
// NotFound counts as a missing item only when it is the API server's answer
// for this item (itemNotFound) and the CRD exists. With the CRD present, any
// other NotFound is an unserved kind; with the CRD gone, every NotFound is,
// like a no-match or discovery error. Any other error is returned.
func (r *Reconciler) readScheduledWork(ctx context.Context, namespace, name string) (*v1alpha1.ScheduledWork, itemRead, error) {
	var w v1alpha1.ScheduledWork
	err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &w)
	switch {
	case err == nil:
		return &w, itemFound, nil
	case !apierrors.IsNotFound(err) && !kindUnserved(err):
		return nil, 0, fmt.Errorf("read ScheduledWork %s: %w", name, err)
	}
	crd := &metav1.PartialObjectMetadata{}
	crd.SetGroupVersionKind(crdKind)
	crdErr := r.APIReader.Get(ctx, types.NamespacedName{Name: scheduledWorkCRD}, crd)
	switch {
	case crdErr == nil && itemNotFound(err, name):
		return nil, itemMissing, nil
	case crdErr == nil:
		return nil, 0, fmt.Errorf("%w: %w", errKindUnservedCRDPresent, err)
	case apierrors.IsNotFound(crdErr):
		return nil, itemKindGone, nil
	}
	return nil, 0, fmt.Errorf("read CustomResourceDefinition %s after %w: %w", scheduledWorkCRD, err, crdErr)
}

// itemNotFound reports a NotFound the API server gave for the item itself:
// its status details name the item, in the scheduledworks resource of this
// group, and it came as a Status. A NotFound without those details is not
// proof, and neither is the plain-text 404 the API server answers for a
// version it does not serve: client-go fills that one's details from the
// request, so only its UnexpectedServerResponse cause tells it apart.
func itemNotFound(err error, name string) bool {
	var status apierrors.APIStatus
	if !apierrors.IsNotFound(err) || !errors.As(err, &status) {
		return false
	}
	d := status.Status().Details
	if d == nil || d.Name != name || d.Group != v1alpha1.GroupVersion.Group || d.Kind != "scheduledworks" {
		return false
	}
	for _, c := range d.Causes {
		if c.Type == metav1.CauseTypeUnexpectedServerResponse {
			return false
		}
	}
	return true
}

// kindUnserved reports a read that failed because the API server does not
// serve the kind (now, or as discovery last saw it).
func kindUnserved(err error) bool {
	var resourceDiscovery *apiutil.ErrResourceDiscoveryFailed
	var groupDiscovery *discovery.ErrGroupDiscoveryFailed
	return meta.IsNoMatchError(err) || errors.As(err, &resourceDiscovery) || errors.As(err, &groupDiscovery)
}

// uncachedSessionPod reads the pod of the session's name from the API server
// for the pod-age proof; nil when there is none or the session does not
// control it.
func (r *Reconciler) uncachedSessionPod(ctx context.Context, s *v1alpha1.AgentSession) (*corev1.Pod, error) {
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the session's pod: %w", err)
	}
	if !metav1.IsControlledBy(&pod, s) {
		return nil, nil
	}
	return &pod, nil
}

// releaseBlocked counts and logs a held release and asks to be called back.
func (r *Reconciler) releaseBlocked(lg logr.Logger, reason string, err error) ctrl.Result {
	if r.ScheduledWorkReleaseHolds != nil {
		r.ScheduledWorkReleaseHolds.WithLabelValues(reason).Inc()
	}
	lg.Info("the scheduled-work finalizer stays", "reason", reason, "error", err.Error())
	return ctrl.Result{RequeueAfter: scheduledWorkReleaseRetry}
}

// scheduledWorkSweep lists, from the API server, the sessions that carry the
// scheduled-work label and finalizer, and queues each for the reconciler, so a
// missed event cannot strand one (DESIGN-054). It runs in the leader only, as
// the reconciler does, and reads nothing else. A finalizer holder that lost
// its label is not listed: its own events still bring it to the releaser,
// which holds it for its metadata, and the blocked gauge counts it.
type scheduledWorkSweep struct {
	reader    client.Reader
	namespace string
	every     time.Duration
	events    chan<- event.GenericEvent
	log       logr.Logger
}

// NeedLeaderElection makes the sweep run in the leader only.
func (w *scheduledWorkSweep) NeedLeaderElection() bool { return true }

// Start runs a pass every interval until ctx ends. The first pass waits an
// interval: the reconciler sees every session when it starts.
func (w *scheduledWorkSweep) Start(ctx context.Context) error {
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := w.pass(ctx); err != nil {
			w.log.Error(err, "list the scheduled sessions")
		}
	}
}

// pass queues every labelled session that carries the finalizer.
func (w *scheduledWorkSweep) pass(ctx context.Context) error {
	var list v1alpha1.AgentSessionList
	if err := w.reader.List(ctx, &list, client.InNamespace(w.namespace), client.HasLabels{v1alpha1.LabelScheduledWork}); err != nil {
		return err
	}
	for i := range list.Items {
		s := &list.Items[i]
		if !controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) {
			continue
		}
		select {
		case w.events <- event.GenericEvent{Object: s}:
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

// sessionEnded is the session-end rule the guard judges by.
func sessionEnded(s *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time) bool {
	ended, _ := scheduledwork.SessionEnded(s, pod, now)
	return ended
}

var scheduledWorkReleaseBlockedDesc = prometheus.NewDesc(MetricScheduledWorkReleaseBlocked,
	"Sessions that still carry the scheduled-work finalizer, with or without the scheduled-work label, although they are being deleted or have ended by the session-end rule judged with their cached pod (DESIGN-054), from the cache at scrape time. The releaser lifts it within seconds, or after scheduledwork.ReportWait for a headless agent's reported outcome; a session that stays counted is blocked.",
	nil, nil)

// ScheduledWorkReleaseCollector is a prometheus.Collector of
// MetricScheduledWorkReleaseBlocked over the sessions in one namespace. It
// lists every session and counts each finalizer holder, labelled or not, so a
// holder that lost its label still counts. It judges the session-end rule
// with the cached pod of the session's name when the rule needs one, so an
// agent end the pod-age proof shows counts as the releaser would judge it.
// Register it with controller-runtime's metrics registry.
type ScheduledWorkReleaseCollector struct {
	// Reader lists sessions and reads their pods; the manager's cache in the
	// operator.
	Reader client.Reader
	// Namespace is where the sessions live (dev-agents).
	Namespace string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Describe implements prometheus.Collector.
func (c *ScheduledWorkReleaseCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- scheduledWorkReleaseBlockedDesc
}

// Collect implements prometheus.Collector. A failed list or pod read reports
// an invalid metric, which fails the scrape, as the session metrics do (D-57).
func (c *ScheduledWorkReleaseCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()
	var list v1alpha1.AgentSessionList
	if err := c.Reader.List(ctx, &list, client.InNamespace(c.Namespace)); err != nil {
		ch <- prometheus.NewInvalidMetric(scheduledWorkReleaseBlockedDesc, err)
		return
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	held := 0
	for i := range list.Items {
		s := &list.Items[i]
		if !controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) {
			continue
		}
		var pod *corev1.Pod
		if s.DeletionTimestamp.IsZero() && scheduledwork.NeedsPodProof(s) {
			p, err := c.cachedPod(ctx, s)
			if err != nil {
				ch <- prometheus.NewInvalidMetric(scheduledWorkReleaseBlockedDesc, err)
				return
			}
			pod = p
		}
		if until, err := scheduledwork.ReleaseHeldUntil(s); err == nil && !now.After(until) {
			// The expected hold of the derived name until the start
			// deadline plus the margin is not blocked.
			continue
		}
		if !s.DeletionTimestamp.IsZero() || sessionEnded(s, pod, now) {
			held++
		}
	}
	ch <- prometheus.MustNewConstMetric(scheduledWorkReleaseBlockedDesc, prometheus.GaugeValue, float64(held))
}

// cachedPod is the pod of the session's name from the collector's reader,
// when the session controls it; nil when there is none.
func (c *ScheduledWorkReleaseCollector) cachedPod(ctx context.Context, s *v1alpha1.AgentSession) (*corev1.Pod, error) {
	var pod corev1.Pod
	if err := c.Reader.Get(ctx, client.ObjectKeyFromObject(s), &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the pod of session %s: %w", s.Name, err)
	}
	if !metav1.IsControlledBy(&pod, s) {
		return nil, nil
	}
	return &pod, nil
}

// scheduledLaunchGate is DESIGN-054's launch gate: before the first pod of a
// session that carries the scheduled-work label (its volume does not exist
// yet), it reads the ScheduledWork uncached and refuses the session, as one
// agentd would refuse (sessionInvalidError: phase Failed, no pod, no volume),
// when its occurrence would never record it (scheduledwork.LaunchRefusal). A
// session without the label is not read and its pod is unchanged. A read that
// fails, or a kind whose CRD still exists but is not served, is returned so
// the reconcile retries; nothing starts in the meantime.
func (r *Reconciler) scheduledLaunchGate(ctx context.Context, s *v1alpha1.AgentSession) error {
	if _, ok := s.Labels[v1alpha1.LabelScheduledWork]; !ok {
		return nil
	}
	name := s.Annotations[v1alpha1.AnnotationScheduledWorkName]
	if name == "" {
		return sessionInvalidError{fmt.Errorf("a scheduled session needs annotation %s", v1alpha1.AnnotationScheduledWorkName)}
	}
	if r.APIReader == nil {
		return errors.New("the launch gate of a scheduled session needs the uncached reader")
	}
	w, read, err := r.readScheduledWork(ctx, r.scheduledWorkNamespace(), name)
	if err != nil {
		return fmt.Errorf("the launch gate of a scheduled session: %w", err)
	}
	if read != itemFound {
		w = nil
	}
	if reason, refuse := scheduledwork.LaunchRefusal(s, w); refuse {
		log.FromContext(ctx).Info("refused a scheduled session's first pod", "scheduledWork", name, "reason", reason)
		return sessionInvalidError{fmt.Errorf("the scheduled session cannot start: %s", reason)}
	}
	return nil
}
