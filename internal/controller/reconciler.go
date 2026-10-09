// Package controller reconciles AgentSession resources into their pods and
// volumes (DESIGN-001 3.1, 3.2, 7; D-44).
//
// The rules of DESIGN-001 5.1 shape everything here, and the envtest suite in
// this package proves them:
//
//   - The pod and the volume have one owner reference, to their AgentSession,
//     and never one to the operator's Deployment (D-03).
//   - The operator never updates a session's pod or volume, and deletes a pod
//     only through deletePod, whose guard (guard.go) allows the Draining and
//     Suspended transitions only, and a suspend of a pod that ran only after a
//     rescue in it (D-51). Drain is plan 04, so the guard refuses it today.
//   - A finalizer holds every session, and its volume, until rescue: deleting
//     an AgentSession is a reap, never a cascade (D-45). deleteVolume, the
//     other guarded delete, archives a reaped session's volume only after a
//     verified rescue of its last pod (D-10, D-51).
//   - Reconcile is level-based: everything comes from the AgentSession, its pod,
//     its volume and the templates, so a fresh operator resumes where the last
//     one stopped. Nothing lives only in memory.
package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Condition types the reconciler sets.
const (
	// ConditionPodReady says whether the session's pod is Ready, and if not,
	// why: the scheduler's reason, a container's waiting reason, the
	// templates, or a pod that ended.
	ConditionPodReady = "PodReady"
	// ConditionOutdated is True when the pod runs an older template revision
	// than the current one (DESIGN-001 5.2). Plan 04 drains on it; until then
	// it is reported only.
	ConditionOutdated = "Outdated"
	// ConditionRemovalBlocked is True while the session asks for its pod or
	// volume to go (suspend, or delete, which is a reap) and 5.1's guard keeps
	// them: no rescue yet, or none that makes the volume safe (D-45, D-51).
	ConditionRemovalBlocked = "RemovalBlocked"
	// ConditionRescueFailed is D-10's rescueFailed mark: True when the newest
	// rescue ran and could not make the work safe, which blocks archive and
	// keeps the volume for a human; False when it succeeded (D-51).
	ConditionRescueFailed = "RescueFailed"
)

// rescueRetry is how soon a rescue that could not run is tried again.
const rescueRetry = time.Minute

// HoldRescueRetry is how soon a failed rescue in a hold pod runs again (D-55).
// A failure can pass on its own (CephFS or GitHub was down) or wait for a
// human to fix the worktree in the pod; either way the next try finds out.
const HoldRescueRetry = 15 * time.Minute

// MaxConcurrentReconciles lets a rescue, which can take minutes, run while other
// sessions are reconciled. Reconciles of one session never overlap.
const MaxConcurrentReconciles = 4

// CacheOptions scopes the manager's cache to what the operator may read
// (DESIGN-001 6.11, D-44): the sessions' namespace, and the one templates
// ConfigMap in its own namespace.
func CacheOptions(sessionNamespace string, tmpl types.NamespacedName) cache.Options {
	return cache.Options{
		DefaultNamespaces: map[string]cache.Config{sessionNamespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&corev1.ConfigMap{}: {
				Namespaces: map[string]cache.Config{tmpl.Namespace: {}},
				Field:      fields.OneTermEqualSelector("metadata.name", tmpl.Name),
			},
		},
	}
}

// Reconciler builds and watches session pods and volumes.
type Reconciler struct {
	// Client reads from the manager's cache and writes to the API server.
	Client client.Client
	// Templates is the dev-env-templates ConfigMap.
	Templates types.NamespacedName
	// APIURL is the operator API's base URL, given to agentd for its heartbeat
	// (D-41). Empty until the API is served (plan 01 step 3).
	APIURL            string
	ManagedCodexTasks bool
	// APIReader reads from the API server, past the cache. The decisions that
	// must not trust a cache that may lag use it: that a rescued pod is still
	// the one the rescue ran in, that no pod exists before archive deletes a
	// volume, and that nothing is left before a deleted session is released.
	APIReader client.Reader
	// Rescuer runs agentd's rescue in a pod (D-51). Nil means no rescue can
	// run, so a pod that ran is never removed.
	Rescuer Rescuer
	// WorkspaceStopper requests exit only for shared executors. StopVerifier
	// supplies uncached typed Pod/Node/Lease reads; nil uses APIReader.
	WorkspaceStopper WorkspaceStopper
	StopVerifier     WorkspaceStopVerifier
	// Recorder emits the rescue's and the archive's events on the session; nil
	// emits none.
	Recorder events.EventRecorder
	// HoldRetry is how soon a failed rescue in a hold pod runs again; zero
	// means HoldRescueRetry (D-55).
	HoldRetry time.Duration
}

func (r *Reconciler) holdRetry() time.Duration {
	if r.HoldRetry > 0 {
		return r.HoldRetry
	}
	return HoldRescueRetry
}

// SetupWithManager registers the reconciler. It watches sessions, the pods and
// volumes they control, and the templates ConfigMap, whose changes reach every
// session that still needs a pod.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, opts ...func(*ctrl.Builder)) error {
	b := ctrl.NewControllerManagedBy(mgr).
		Named("agentsession").
		For(&v1alpha1.AgentSession{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.sessionsForTemplates)).
		WithOptions(controller.Options{MaxConcurrentReconciles: MaxConcurrentReconciles})
	for _, o := range opts {
		o(b)
	}
	return b.Complete(r)
}

func (r *Reconciler) sessionsForTemplates(ctx context.Context, o client.Object) []reconcile.Request {
	if o.GetNamespace() != r.Templates.Namespace || o.GetName() != r.Templates.Name {
		return nil
	}
	var list v1alpha1.AgentSessionList
	if err := r.Client.List(ctx, &list); err != nil {
		log.FromContext(ctx).Error(err, "list sessions after a templates change")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// Reconcile creates what a session lacks and records what it sees. It never
// updates a pod or a volume, and deletes one only through the guard: a pod in
// the Suspended transition after its rescue, a volume at archive (D-51).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var s v1alpha1.AgentSession
	if err := r.Client.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The finalizer goes on before the session has anything to lose, so no
	// pod or volume of it ever exists without it (D-45).
	if s.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(&s, Finalizer) {
		if s.Spec.Workspace != nil {
			if err := r.initializeSharedAdmission(ctx, &s); err != nil {
				return ctrl.Result{}, err
			}
		}
		orig := s.DeepCopy()
		controllerutil.AddFinalizer(&s, Finalizer)
		if err := r.Client.Patch(ctx, &s, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
			if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, fmt.Errorf("add the finalizer: %w", err)
		}
	}

	pod, err := getOwned(ctx, r.Client, &s, s.Name, &corev1.Pod{})
	if err != nil {
		return ctrl.Result{}, err
	}
	claim, err := getOwned(ctx, r.Client, &s, HomeClaimName(s.Name), &corev1.PersistentVolumeClaim{})
	if err != nil {
		return ctrl.Result{}, err
	}

	t, tErr := r.loadTemplates(ctx)
	obs := observation{pod: pod, claim: claim, templates: t, templatesErr: tErr, now: time.Now()}
	if s.Spec.Workspace != nil {
		result, err := r.reconcileSharedWorkspace(ctx, &s, &obs)
		if err != nil {
			return result, err
		}
		next := s.Status.DeepCopy()
		observe(&s, obs, next)
		// A disappeared shared executor is uncertain. Preserve the durable
		// memory that it existed so a later reconcile cannot treat it as a
		// fresh session merely because observation cleared PodName.
		if obs.pod.missing && !sharedRescued(&s) && s.Status.PodName != "" {
			next.PodName = s.Status.PodName
		}
		if !apiequality.Semantic.DeepEqual(&s.Status, next) {
			s.Status = *next
			return r.writeStatus(ctx, &s, result)
		}
		return result, nil
	}

	// A hold pod (D-55) is never the pod a session wants: when the session
	// wants its pod again, the guard lets the hold pod go first.
	hold := !obs.pod.missing && obs.pod.foreign == "" && isHoldPod(obs.pod.obj)

	var result ctrl.Result
	switch {
	case !wantsPodGone(&s) && !hold && s.Status.ArchivedAt != nil:
		// An archived session's volume is gone, so a new pod would start its
		// task again on a new volume: it is never resumed (D-62). It stays
		// Archived until it is reaped or restored from its bundle.
	case !wantsPodGone(&s) && !hold:
		// A session that wants its pod can change its volume again: through
		// a new pod, or through the rescued pod itself when a resume came
		// before its delete. So the last rescue stops counting, and the
		// record says so before anything else happens here, a new pod
		// included (D-10: an old bundle never counts).
		if rec := s.Status.Rescue; rec != nil && rec.Result != "" && !rec.Superseded {
			rec.Superseded = true
			setRescueCondition(&s, &s.Status, "")
			return r.writeStatus(ctx, &s, ctrl.Result{RequeueAfter: time.Second})
		}
		if tErr != nil {
			break
		}
		if err := r.ensure(ctx, &s, t, &obs); err != nil {
			return ctrl.Result{}, err
		}
		res, stop, err := r.idleTimer(ctx, &s, t, &obs)
		if err != nil || stop {
			return res, err
		}
		result = res
	case !obs.pod.missing && obs.pod.foreign == "":
		// Suspend or delete asks for the pod to go, or a hold pod is no longer
		// needed: rescue first, then 5.1's guard decides.
		res, stop, err := r.removePod(ctx, &s, &obs)
		if err != nil || stop {
			return res, err
		}
		result = res
	case !s.DeletionTimestamp.IsZero():
		// A deleted session with no pod: archive its volume after a
		// verified rescue, then let it go.
		res, done, err := r.archive(ctx, &s, &obs, true)
		if err != nil || done {
			return res, err
		}
		result = res
	case !obs.claim.missing:
		// A suspended session with no pod: its archive timer (D-09, D-62).
		// Once the archive is recorded it is due whatever the clock says, so
		// a delete that an operator restart interrupted is finished.
		due, wait := r.archiveDue(&s, t, obs)
		if !due && s.Status.ArchivedAt == nil {
			if wait > 0 {
				result = ctrl.Result{RequeueAfter: wait}
			}
			break
		}
		res, done, err := r.archive(ctx, &s, &obs, true)
		if err != nil || done {
			return res, err
		}
		result = res
	}

	next := s.Status.DeepCopy()
	observe(&s, obs, next)
	if !apiequality.Semantic.DeepEqual(&s.Status, next) {
		s.Status = *next
		return r.writeStatus(ctx, &s, result)
	}
	return result, nil
}

// writeStatus writes the session's status. A conflict or a missing session
// means a newer version is on its way through the cache; its event reconciles
// again.
func (r *Reconciler) writeStatus(ctx context.Context, s *v1alpha1.AgentSession, result ctrl.Result) (ctrl.Result, error) {
	if err := r.Client.Status().Update(ctx, s); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	return result, nil
}

// removePod rescues the session's pod if an agent may run in it, records the
// verdict before anything else happens, then deletes the pod if 5.1's guard
// allows it (D-51). stop means the reconcile ends here with res.
func (r *Reconciler) removePod(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (res ctrl.Result, stop bool, err error) {
	pod := obs.pod.obj
	if !pod.DeletionTimestamp.IsZero() {
		// Going already; its last event reconciles again.
		return ctrl.Result{}, false, nil
	}
	if needsRescue(s, pod, time.Now(), r.holdRetry()) {
		// A rescue takes minutes and stops the agent: run it only on the
		// newest session and pod, not on a cache that has not caught up with
		// the operator's own writes, so its record does not conflict.
		if behind, err := r.cacheBehind(ctx, s, pod); err != nil || behind {
			return ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
		}
		rec, reason, cur, err := r.rescue(ctx, s, pod)
		if err != nil {
			obs.removalBlocked = fmt.Errorf("the rescue could not run in pod %s, so the pod stays; retrying in %s: %w", pod.Name, rescueRetry, err)
			return ctrl.Result{RequeueAfter: rescueRetry}, false, nil
		}
		// The record goes to the API server before the pod is deleted, so a
		// fresh operator never deletes a pod on a rescue it cannot see.
		if written, err := r.recordRescue(ctx, s, rec, reason); err != nil || !written {
			// The session changed during the rescue in a way that matters:
			// judge it again.
			return ctrl.Result{RequeueAfter: time.Second}, true, err
		}
		r.event(s, eventType(rec), "Rescue", reason, rec.Message)
		// Delete the pod as the API server has it now, so a status update the
		// kubelet made during the rescue does not void the precondition.
		pod = cur
	}
	if obs.removalBlocked = podRemovalAllowed(s, pod); obs.removalBlocked != nil {
		if isHoldPod(pod) {
			return r.holdWait(s, pod, obs), false, nil
		}
		return ctrl.Result{}, false, nil
	}
	if err := deletePod(ctx, r.Client, s, pod); err != nil {
		if apierrors.IsConflict(err) {
			// The pod changed since the read; judge it again.
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{}, true, err
	}
	log.FromContext(ctx).Info("deleted the session pod", "pod", pod.Name, "uid", pod.UID, "hold", isHoldPod(pod), "suspend", s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended, "deleted", !s.DeletionTimestamp.IsZero())
	return ctrl.Result{}, false, nil
}

// IdleTimerSuspender is the value of AnnotationSuspendedBy when the idle timer
// suspended the session (D-60).
const IdleTimerSuspender = "idle-timer"

// idleTimer suspends a session that has been idle past its window (D-09,
// D-60): it writes spec.operatingMode Suspended, as the API's suspend does,
// and the usual rescue and pod delete follow. Otherwise it asks to be called
// back at the deadline; heartbeats, which update the session every minute,
// also reconcile it. stop means the reconcile ends here with res.
func (r *Reconciler) idleTimer(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates, obs *observation) (res ctrl.Result, stop bool, err error) {
	deadline, why, ok := idleDeadline(s, t, obs.pod)
	if !ok {
		return ctrl.Result{}, false, nil
	}
	now := time.Now()
	if now.Before(deadline) {
		return ctrl.Result{RequeueAfter: deadline.Sub(now) + time.Second}, false, nil
	}
	orig := s.DeepCopy()
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[v1alpha1.AnnotationSuspendedBy] = IdleTimerSuspender
	if err := r.Client.Patch(ctx, s, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{}, true, fmt.Errorf("suspend an idle session: %w", err)
	}
	log.FromContext(ctx).Info("suspended an idle session", "why", why)
	r.event(s, corev1.EventTypeNormal, "Suspend", "IdleSuspend", why)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// idleDeadline is when the idle timer suspends the session, and why (D-60). It
// runs only while the session's own pod runs and is Ready, and its agent has
// reported a state that is not busy: a busy agent, or one that has not
// reported yet, is never suspended. The window is spec.lifecycle's, else the
// templates' for the mode (D-09: a task after 1h, the others after 72h). It
// counts from the newest of the agent's lastActivity, the pod's start and the
// API's last resume, so a resumed session gets a whole window.
func idleDeadline(s *v1alpha1.AgentSession, t *templates.Templates, pod owned[*corev1.Pod]) (time.Time, string, bool) {
	a := s.Status.Agent
	if pod.missing || pod.foreign != "" || isHoldPod(pod.obj) || !pod.obj.DeletionTimestamp.IsZero() ||
		pod.obj.Status.Phase != corev1.PodRunning || !podReady(pod.obj) || a == nil || a.Status == "" || a.Status == protocol.AgentBusy {
		return time.Time{}, "", false
	}
	// Shared boot and final run-agent admission may queue for the common Git
	// lock. Their observational heartbeat is progress, not idle activity.
	if s.Spec.Workspace != nil && (a.Boot == protocol.BootBooting || a.Status == protocol.AgentPending) {
		return time.Time{}, "", false
	}
	window := t.IdleSuspendAfter(s.Spec.Mode)
	if l := s.Spec.Lifecycle; l != nil && l.IdleSuspendAfter != nil {
		window = l.IdleSuspendAfter.Duration
	}
	last := s.CreationTimestamp.Time
	if st := pod.obj.Status.StartTime; st != nil && st.After(last) {
		last = st.Time
	}
	if a.LastActivity != nil && a.LastActivity.After(last) {
		last = a.LastActivity.Time
	}
	// A resume that landed while an idle suspend's rescue ran keeps the old
	// pod, whose start and activity are old: count from the resume as well.
	if at, err := time.Parse(time.RFC3339, s.Annotations[v1alpha1.AnnotationResumedAt]); err == nil && at.After(last) {
		last = at
	}
	why := fmt.Sprintf("the agent was %s and nothing happened since %s, %s ago; the idle window is %s (D-09)",
		a.Status, last.UTC().Format(time.RFC3339), time.Since(last).Round(time.Minute), window)
	return last.Add(window), why, true
}

// holdWait says why a hold pod stays and when to look at it again (D-55). A
// pod that is starting is looked at when it changes; a failed rescue runs
// again HoldRetry after it ended.
func (r *Reconciler) holdWait(s *v1alpha1.AgentSession, pod *corev1.Pod, obs *observation) ctrl.Result {
	rec := s.Status.Rescue
	switch {
	case errors.Is(obs.removalBlocked, errHoldStarting):
		_, msg := pendingReason(pod)
		obs.removalBlocked = fmt.Errorf("%w: %s", errHoldStarting, msg)
		if why := volumeRemovalAllowed(s, false, true); why != nil {
			obs.removalBlocked = fmt.Errorf("%w; it is there because %w", obs.removalBlocked, why)
		}
	case rec != nil && rec.Result == v1alpha1.RescueFailed && rec.At != nil && rescueRanIn(s, pod):
		next := rec.At.Add(r.holdRetry())
		obs.removalBlocked = fmt.Errorf("%w; the rescue in it failed and runs again at %s: %s", errHoldRescuing, next.UTC().Format(time.RFC3339), rec.Message)
		return ctrl.Result{RequeueAfter: max(time.Second, time.Until(next))}
	}
	return ctrl.Result{}
}

// startHoldPod gives a session whose volume needs a rescue, and that has no pod
// to run it in, a hold pod (D-55): its pod was preempted, evicted or never
// started, or the rescue in it failed. The operator then rescues in the hold pod
// as in any session pod, and the guard lets it go once the volume is safe.
func (r *Reconciler) startHoldPod(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) error {
	why := obs.removalBlocked
	switch {
	case volumeTerminating(*obs):
		obs.removalBlocked = fmt.Errorf("%w; the volume is being deleted, so no hold pod can mount it to rescue it, and a human decides (D-45)", why)
		return nil
	case obs.templatesErr != nil:
		obs.removalBlocked = fmt.Errorf("%w; no hold pod can start until the templates are fixed: %w", why, obs.templatesErr)
		return nil
	}
	pod, err := buildHoldPod(s, obs.templates)
	if err != nil {
		obs.removalBlocked = fmt.Errorf("%w; the hold pod cannot be built: %w", why, err)
		return nil
	}
	if err := r.create(ctx, "hold pod", pod); err != nil {
		return err
	}
	log.FromContext(ctx).Info("created a hold pod to rescue the session volume", "pod", pod.Name, "why", why.Error())
	r.event(s, corev1.EventTypeNormal, "Rescue", "HoldPod", "created hold pod "+pod.Name+" to rescue the volume: "+why.Error())
	obs.pod = owned[*corev1.Pod]{obj: pod}
	obs.removalBlocked = fmt.Errorf("%w: created it, because %w", errHoldStarting, why)
	return nil
}

// recordRescue writes the rescue record onto the newest version of the session
// and, on success, makes s that version. agentd's heartbeat patches the
// session's status every minute, so a session read before a rescue that took
// minutes is out of date by the time the record is written. The record is
// written only while the session is the one the rescue ran for: same UID, same
// generation, still asking for its pod to go. Otherwise it reports false and
// the next reconcile judges the session again.
func (r *Reconciler) recordRescue(ctx context.Context, s *v1alpha1.AgentSession, rec *v1alpha1.RescueStatus, reason string) (bool, error) {
	for range 5 {
		var fresh v1alpha1.AgentSession
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if fresh.UID != s.UID || fresh.Generation != rec.Generation || (!wantsPodGone(&fresh) && fresh.Spec.Workspace == nil) {
			return false, nil
		}
		fresh.Status.Rescue = rec
		setRescueCondition(&fresh, &fresh.Status, reason)
		err := r.Client.Status().Update(ctx, &fresh)
		if err == nil {
			*s = fresh
			return true, nil
		}
		if !apierrors.IsConflict(err) {
			return false, client.IgnoreNotFound(err)
		}
	}
	return false, nil
}

// cacheBehind reports whether the API server has a newer session or pod than
// the cache gave this reconcile.
func (r *Reconciler) cacheBehind(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod) (bool, error) {
	var fs v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fs); err != nil {
		return false, err
	}
	var fp corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(pod), &fp); err != nil {
		return false, err
	}
	return fs.ResourceVersion != s.ResourceVersion || fp.ResourceVersion != pod.ResourceVersion, nil
}

// rescue runs agentd's rescue in the pod and returns its verdict and the pod
// as the API server has it afterwards. An error means it did not run, or ran
// in a pod that has since been replaced.
func (r *Reconciler) rescue(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod) (*v1alpha1.RescueStatus, string, *corev1.Pod, error) {
	return r.rescueWithProof(ctx, s, pod, nil)
}

func (r *Reconciler) rescueWithProof(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod, proof *protocol.WorkspaceStopProof) (*v1alpha1.RescueStatus, string, *corev1.Pod, error) {
	if r.Rescuer == nil {
		return nil, "", nil, errors.New("the operator has no rescuer")
	}
	log.FromContext(ctx).Info("rescuing the session pod", "pod", pod.Name, "uid", pod.UID)
	var rep protocol.RescueReport
	var err error
	if s.Spec.Workspace != nil {
		shared, ok := r.Rescuer.(WorkspaceRescuer)
		if !ok || proof == nil {
			return nil, "", nil, errors.New("shared rescue has no fresh-proof executor")
		}
		rep, err = shared.RescueWorkspace(ctx, pod, proof)
	} else {
		rep, err = r.Rescuer.Rescue(ctx, pod)
	}
	if err != nil {
		return nil, "", nil, err
	}
	// exec reaches a pod by name: make sure the report came from this one.
	var cur corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(pod), &cur); err != nil {
		return nil, "", nil, fmt.Errorf("read the pod after the rescue: %w", err)
	}
	if cur.UID != pod.UID {
		return nil, "", nil, fmt.Errorf("the pod was replaced during the rescue (uid %s, now %s)", pod.UID, cur.UID)
	}
	if s.Spec.Workspace != nil {
		currentProof, err := workspaceHoldProof(&cur)
		if err != nil || currentProof == nil || currentProof.Validate(s.Spec.Workspace.ID, s.Name, string(s.UID), proof.PodUID) != nil ||
			!controlledBySession(&cur, s) || !cur.DeletionTimestamp.IsZero() ||
			cur.Annotations[privateHomeUIDAnnotation] != s.Status.SharedPrivateHomeUID || cur.Annotations[privateHomeUIDAnnotation] != pod.Annotations[privateHomeUIDAnnotation] {
			return nil, "", nil, errors.New("shared hold ownership or original home binding changed during rescue")
		}
	}
	rec, reason := verdict(s, pod, rep, metav1.Now())
	log.FromContext(ctx).Info("rescued the session pod", "pod", pod.Name, "result", rec.Result, "reason", reason, "bundle", rec.LastBundle)
	return rec, reason, &cur, nil
}

// archiveDue reports whether a suspended session's archive timer is due, and
// otherwise how long until it is (D-62): status.suspendedAt plus
// spec.lifecycle.archiveAfter, else the templates'. A session the operator has
// not yet seen suspended has no timer yet. Without a spec override and with
// templates that do not load, the timer is not due: their window may be longer
// than D-09's default, and archive never runs early.
func (r *Reconciler) archiveDue(s *v1alpha1.AgentSession, t *templates.Templates, obs observation) (bool, time.Duration) {
	if s.Status.SuspendedAt == nil {
		return false, time.Second
	}
	var after time.Duration
	switch l := s.Spec.Lifecycle; {
	case l != nil && l.ArchiveAfter != nil:
		after = l.ArchiveAfter.Duration
	case t != nil:
		after = t.ArchiveAfter()
	default:
		return false, time.Minute
	}
	at := s.Status.SuspendedAt.Add(after)
	if obs.now.Before(at) {
		return false, at.Sub(obs.now) + time.Second
	}
	return true, 0
}

// archive deletes a session's volume once the guard allows it: a reaped
// session's, which it then lets go when nothing of it is left (D-10, D-51), or
// a suspended session's whose archive timer is due, which stays as an Archived
// record (D-62). With no valid rescue and no pod, a hold pod rescues the volume
// first (D-55). done means the reconcile ends here with res.
func (r *Reconciler) archive(ctx context.Context, s *v1alpha1.AgentSession, obs *observation, archiveDue bool) (res ctrl.Result, done bool, err error) {
	if s.Spec.Workspace != nil {
		// Even a missing/foreign claim must not reach legacy empty-session
		// release. Shared reap has one independent retention proof path.
		obs.removalBlocked = errors.New("shared task rescue does not archive its private home; the home and workspace claim stay retained")
		return ctrl.Result{}, false, nil
	}
	if (obs.claim.missing || obs.claim.foreign != "") && s.DeletionTimestamp.IsZero() {
		if obs.claim.foreign != "" {
			obs.removalBlocked = errors.New("a volume of the session's name is not the session's; the operator leaves it alone")
		}
		return ctrl.Result{}, false, nil
	}
	if obs.claim.missing || obs.claim.foreign != "" {
		// Nothing of the session's to archive: release it if the API server
		// has no pod or volume of its names either.
		released, err := r.releaseIfEmpty(ctx, s)
		if err != nil || released {
			return ctrl.Result{}, true, err
		}
		if obs.claim.foreign == "" {
			// The cache has not seen the claim, or a pod, the API server
			// has; their events reconcile again.
			return ctrl.Result{RequeueAfter: time.Second}, false, nil
		}
		obs.removalBlocked = errors.New("a volume of the session's name is not the session's; the operator leaves it alone, and the session stays until a human removes it")
		return ctrl.Result{}, false, nil
	}
	// Archive must not trust a cache that may not have seen a new pod yet.
	podExists := true
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Name}, &corev1.Pod{}); apierrors.IsNotFound(err) {
		podExists = false
	} else if err != nil {
		return ctrl.Result{}, true, err
	}
	obs.removalBlocked = volumeRemovalAllowed(s, podExists, archiveDue)
	if errors.Is(obs.removalBlocked, errArchiveNotRecorded) {
		// Record the archive before the volume goes, so a restart or a
		// resume in between can never find the volume gone and the session
		// resumable (D-62): a resume of an archived session is refused.
		if written, err := r.recordArchive(ctx, s, obs.now); err != nil || !written {
			return ctrl.Result{RequeueAfter: time.Second}, true, err
		}
		obs.removalBlocked = volumeRemovalAllowed(s, podExists, archiveDue)
	}
	if obs.removalBlocked != nil {
		if !podExists && !rescued(s) && s.Spec.Workspace == nil {
			// No valid rescue and no pod to run one in: a hold pod (D-55).
			return ctrl.Result{}, false, r.startHoldPod(ctx, s, obs)
		}
		return ctrl.Result{}, false, nil
	}
	claim := obs.claim.obj
	lifting := controllerutil.ContainsFinalizer(claim, Finalizer)
	if err := deleteVolume(ctx, r.Client, s, claim, podExists, archiveDue); err != nil {
		if apierrors.IsConflict(err) {
			// The claim changed (the delete itself does that); lift the
			// finalizer from its new version.
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{}, true, err
	}
	obs.archived = true
	if lifting {
		log.FromContext(ctx).Info("archived the session volume", "claim", claim.Name, "rescue", s.Status.Rescue.Stamp, "bundle", s.Status.Rescue.LastBundle)
		r.event(s, corev1.EventTypeNormal, "Archive", "Archived",
			fmt.Sprintf("deleted volume %s after the rescue %s (%s): %s", claim.Name, s.Status.Rescue.Stamp, s.Status.Rescue.Result, s.Status.Rescue.Message))
	}
	// The claim goes once its other finalizers do; its delete event comes
	// back here and releases the session.
	return ctrl.Result{}, false, nil
}

// recordArchive writes status.archivedAt onto the newest version of a
// suspended session, before its volume is deleted, and on success makes s that
// version (D-62). It writes only while the session is the one the archive was
// judged for: same UID, still suspended and not deleted, and the same valid
// rescue. Otherwise it reports false, and the next reconcile judges it again.
func (r *Reconciler) recordArchive(ctx context.Context, s *v1alpha1.AgentSession, now time.Time) (bool, error) {
	for range 5 {
		var fresh v1alpha1.AgentSession
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		switch {
		case fresh.UID != s.UID, !fresh.DeletionTimestamp.IsZero(), fresh.Spec.OperatingMode != v1alpha1.OperatingModeSuspended,
			!rescued(&fresh), s.Status.Rescue == nil || fresh.Status.Rescue.PodUID != s.Status.Rescue.PodUID || fresh.Status.Rescue.Stamp != s.Status.Rescue.Stamp:
			return false, nil
		case fresh.Status.ArchivedAt != nil:
			*s = fresh
			return true, nil
		}
		t := metav1.NewTime(now)
		fresh.Status.ArchivedAt = &t
		err := r.Client.Status().Update(ctx, &fresh)
		if err == nil {
			*s = fresh
			return true, nil
		}
		if !apierrors.IsConflict(err) {
			return false, client.IgnoreNotFound(err)
		}
	}
	return false, nil
}

func (r *Reconciler) event(s *v1alpha1.AgentSession, typ, action, reason, note string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(s, nil, typ, reason, action, "%s", truncate(note))
}

func eventType(rec *v1alpha1.RescueStatus) string {
	if rec.Result == v1alpha1.RescueFailed {
		return corev1.EventTypeWarning
	}
	return corev1.EventTypeNormal
}

// rescueReason is the RescueFailed reason to keep: the one the verdict gave,
// while the condition still matches the record, else one from the record.
func rescueReason(st *v1alpha1.AgentSessionStatus) string {
	r := st.Rescue
	if r == nil {
		return ""
	}
	if c := meta.FindStatusCondition(st.Conditions, ConditionRescueFailed); c != nil &&
		(c.Status == metav1.ConditionTrue) == (r.Result == v1alpha1.RescueFailed) {
		return c.Reason
	}
	if r.Result == v1alpha1.RescueFailed {
		return "RescueFailed"
	}
	return string(r.Result)
}

// setRescueCondition sets RescueFailed from the newest rescue record.
func setRescueCondition(s *v1alpha1.AgentSession, st *v1alpha1.AgentSessionStatus, reason string) {
	r := st.Rescue
	if r == nil || r.Result == "" || r.Superseded {
		meta.RemoveStatusCondition(&st.Conditions, ConditionRescueFailed)
		return
	}
	c := metav1.Condition{Type: ConditionRescueFailed, Status: metav1.ConditionFalse, Reason: conditionReason(reason),
		Message: truncate(r.Message), ObservedGeneration: s.Generation}
	if r.Result == v1alpha1.RescueFailed {
		c.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&st.Conditions, c)
}

// releaseIfEmpty removes the finalizer from a deleted session that has no pod and
// no volume, so the API server can finish the delete. It asks the API server, not
// the cache: a volume the cache has not seen yet would otherwise lose its owner
// (D-45). Any pod or volume of the session's names keeps the finalizer, whoever
// controls it.
func (r *Reconciler) releaseIfEmpty(ctx context.Context, s *v1alpha1.AgentSession) (bool, error) {
	if !controllerutil.ContainsFinalizer(s, Finalizer) {
		return false, nil
	}
	objects := []struct {
		name string
		obj  client.Object
	}{{s.Name, &corev1.Pod{}}, {HomeClaimName(s.Name), &corev1.PersistentVolumeClaim{}}}
	if s.Spec.Workspace != nil {
		objects = append(objects, struct {
			name string
			obj  client.Object
		}{workspaceHoldName(s), &corev1.Pod{}})
	}
	for _, o := range objects {
		err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: o.name}, o.obj)
		if err == nil {
			return false, nil
		}
		if !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return true, releaseSession(ctx, r.Client, s)
}

// releaseSession removes the session's finalizer. With releaseVolume it is the
// only place that lifts a finalizer, and only releaseIfEmpty calls it
// (TestOnlyTheGuardDeletes).
func releaseSession(ctx context.Context, c client.Client, s *v1alpha1.AgentSession) error {
	orig := s.DeepCopy()
	controllerutil.RemoveFinalizer(s, Finalizer)
	if err := c.Patch(ctx, s, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return client.IgnoreNotFound(err)
	}
	log.FromContext(ctx).Info("released a deleted session that has no pod and no volume left")
	return nil
}

// ensure creates the volume and the pod when the session has no pod. It builds
// the pod first, so a session that can never start (one agentd would refuse, or
// one the templates cannot serve) gets no volume either. A pod or volume of the
// session's names that the session does not control is left alone and reported.
func (r *Reconciler) ensure(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates, obs *observation) error {
	if !obs.pod.missing {
		return nil
	}
	if s.Spec.Workspace != nil && s.Status.SharedPrivateHomeUID != "" {
		if _, err := r.workspacePrivateHome(ctx, s, types.UID(s.Status.SharedPrivateHomeUID)); err != nil {
			obs.removalBlocked = err
			return nil
		}
	}
	pod, err := buildPod(s, t, r.APIURL, r.ManagedCodexTasks)
	if err != nil {
		obs.buildErr = err
		return nil
	}
	if obs.claim.missing {
		claim, err := buildHomeClaim(s, t)
		if err != nil {
			obs.buildErr = err
			return nil
		}
		if s.Spec.Workspace != nil {
			if err := r.startSharedAdmission(ctx, s); err != nil {
				obs.removalBlocked = err
				return nil
			}
		}
		if err := r.create(ctx, "volume", claim); err != nil {
			return err
		}
		log.FromContext(ctx).Info("created the session volume", "claim", claim.Name, "storageClass", *claim.Spec.StorageClassName)
		obs.claim = owned[*corev1.PersistentVolumeClaim]{obj: claim}
	}
	if obs.claim.foreign != "" || volumeTerminating(*obs) {
		return nil
	}
	if s.Spec.Workspace != nil {
		if err := r.startSharedAdmission(ctx, s); err != nil {
			obs.removalBlocked = err
			return nil
		}
		if err := r.bindSharedPrivateHome(ctx, s); err != nil {
			obs.removalBlocked = err
			return nil
		}
	}
	if err := r.create(ctx, "pod", pod); err != nil {
		return err
	}
	log.FromContext(ctx).Info("created the session pod", "pod", pod.Name, "revision", t.Revision())
	obs.pod = owned[*corev1.Pod]{obj: pod}
	return nil
}

// create creates an object. AlreadyExists means the cache had not caught up, or
// a foreign object took the name; the next reconcile sees which.
func (r *Reconciler) create(ctx context.Context, kind string, o client.Object) error {
	if err := r.Client.Create(ctx, o); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%s %s already exists; retrying once the cache has it", kind, o.GetName())
		}
		return fmt.Errorf("create %s %s: %w", kind, o.GetName(), err)
	}
	return nil
}

func (r *Reconciler) loadTemplates(ctx context.Context) (*templates.Templates, error) {
	var cm corev1.ConfigMap
	if err := r.Client.Get(ctx, r.Templates, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("templates: ConfigMap %s not found", r.Templates)
		}
		return nil, fmt.Errorf("templates: %w", err)
	}
	t, err := templates.Parse(cm.Data)
	if err != nil {
		return nil, fmt.Errorf("templates %s: %w", r.Templates, err)
	}
	return t, nil
}

// owned is the result of looking up a session's pod or volume by name.
type owned[T client.Object] struct {
	obj T
	// missing: no object has the name.
	missing bool
	// foreign: an object has the name but the session does not control it;
	// the text says what owns it.
	foreign string
}

func getOwned[T client.Object](ctx context.Context, c client.Reader, s *v1alpha1.AgentSession, name string, obj T) (owned[T], error) {
	if err := c.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return owned[T]{missing: true}, nil
		}
		return owned[T]{}, err
	}
	if !metav1.IsControlledBy(obj, s) {
		return owned[T]{foreign: describeOwner(obj)}, nil
	}
	return owned[T]{obj: obj}, nil
}

func describeOwner(o metav1.Object) string {
	if ref := metav1.GetControllerOf(o); ref != nil {
		return fmt.Sprintf("%s %s (uid %s)", ref.Kind, ref.Name, ref.UID)
	}
	return "no controller"
}

// observation is what one reconcile found.
type observation struct {
	pod          owned[*corev1.Pod]
	claim        owned[*corev1.PersistentVolumeClaim]
	templates    *templates.Templates
	templatesErr error
	buildErr     error
	// removalBlocked is why the guard keeps a pod or volume the session asks
	// to be rid of (suspend or delete).
	removalBlocked error
	// archived: this reconcile deleted the session's volume, or lifted its
	// finalizer, after a verified rescue.
	archived bool
	// now is the reconcile's clock, for the timers' status fields.
	now time.Time
}

// observe writes the session's status from what the reconcile found. It is a
// pure function of its inputs, so a fresh operator computes the same status.
func observe(s *v1alpha1.AgentSession, obs observation, st *v1alpha1.AgentSessionStatus) {
	st.PodName, st.NodeName, st.Revision = "", "", ""
	pod := obs.pod.obj
	hasPod := obs.pod.foreign == "" && !obs.pod.missing
	if hasPod {
		st.PodName = pod.Name
		st.NodeName = pod.Spec.NodeName
		st.Revision = pod.Labels[v1alpha1.LabelRevision]
	}

	// The archive timer's clock (D-62): it starts when the operator first sees
	// the session suspended with no pod of its own, and stops on a resume.
	switch {
	case s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero():
		st.SuspendedAt = nil
	case st.SuspendedAt == nil && obs.pod.missing:
		t := metav1.NewTime(obs.now)
		st.SuspendedAt = &t
	}
	if obs.archived && s.DeletionTimestamp.IsZero() && st.ArchivedAt == nil {
		t := metav1.NewTime(obs.now)
		st.ArchivedAt = &t
	}

	phase, ready := podPhase(s, obs)
	ready.Reason = conditionReason(ready.Reason)
	ready.Message = truncate(ready.Message)
	st.Phase = phase
	st.PendingReason = ""
	if phase == v1alpha1.PhasePending {
		st.PendingReason = ready.Message
	}
	ready.Type = ConditionPodReady
	ready.ObservedGeneration = s.Generation
	meta.SetStatusCondition(&st.Conditions, ready)

	setRescueCondition(s, st, rescueReason(st))
	if obs.removalBlocked != nil {
		reason, what := "SuspendNeedsRescue", "suspend waits"
		if !s.DeletionTimestamp.IsZero() {
			reason, what = "DeleteNeedsRescue", "delete is a reap and waits"
		}
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: ConditionRemovalBlocked, Status: metav1.ConditionTrue,
			Reason: reason, Message: truncate(what + ": " + obs.removalBlocked.Error()), ObservedGeneration: s.Generation})
	} else {
		meta.RemoveStatusCondition(&st.Conditions, ConditionRemovalBlocked)
	}

	// A hold pod runs no agent, so no drain is due for it (D-55).
	agentPod := hasPod && !isHoldPod(pod)
	switch {
	case agentPod && obs.templates != nil:
		cur := obs.templates.Revision()
		c := metav1.Condition{Type: ConditionOutdated, Status: metav1.ConditionFalse, Reason: "CurrentRevision",
			Message: "the pod runs the current template revision " + cur, ObservedGeneration: s.Generation}
		if st.Revision != cur {
			c.Status, c.Reason = metav1.ConditionTrue, "OlderRevision"
			c.Message = fmt.Sprintf("the pod runs template revision %s; the current one is %s", st.Revision, cur)
		}
		meta.SetStatusCondition(&st.Conditions, c)
	case agentPod:
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: ConditionOutdated, Status: metav1.ConditionUnknown,
			Reason: "TemplatesInvalid", Message: truncate("the current template revision is unknown: " + obs.templatesErr.Error()), ObservedGeneration: s.Generation})
	default:
		meta.RemoveStatusCondition(&st.Conditions, ConditionOutdated)
	}
}

// reasonPattern is metav1.Condition's rule for a reason. Reasons from the
// kubelet and the scheduler are CamelCase words, but a status the API server
// refuses would stall the session's every update, so a stray one is replaced.
var reasonPattern = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)

func conditionReason(r string) string {
	if len(r) > 1024 || !reasonPattern.MatchString(r) {
		return "NotReady"
	}
	return r
}

// maxMessage keeps a condition message well inside the API's limit (32768).
const maxMessage = 4096

func truncate(msg string) string {
	if len(msg) <= maxMessage {
		return msg
	}
	return msg[:maxMessage-3] + "..."
}

// volumeTerminating reports whether the session's volume exists and is being
// deleted. A new pod would never start on it.
func volumeTerminating(obs observation) bool {
	return !obs.claim.missing && obs.claim.foreign == "" && !obs.claim.obj.DeletionTimestamp.IsZero()
}

// podPhase maps the session's pod to a phase and the PodReady condition.
func podPhase(s *v1alpha1.AgentSession, obs observation) (v1alpha1.SessionPhase, metav1.Condition) {
	notReady := func(reason, msg string) metav1.Condition {
		return metav1.Condition{Status: metav1.ConditionFalse, Reason: reason, Message: msg}
	}
	pod := obs.pod.obj
	switch {
	case obs.pod.foreign != "":
		return v1alpha1.PhasePending, notReady("NameTaken", fmt.Sprintf("a pod named %s exists and its controller is %s, not this session; the operator leaves it alone", s.Name, obs.pod.foreign))
	case obs.claim.foreign != "":
		return v1alpha1.PhasePending, notReady("NameTaken", fmt.Sprintf("a volume named %s exists and its controller is %s, not this session; the operator leaves it alone", HomeClaimName(s.Name), obs.claim.foreign))
	case obs.pod.missing && !s.DeletionTimestamp.IsZero() && (obs.archived || (rescued(s) && volumeTerminating(obs))):
		return v1alpha1.PhaseArchived, notReady("Archived", fmt.Sprintf("the volume %s was deleted after the rescue %s; the session goes once the volume is gone", HomeClaimName(s.Name), s.Status.Rescue.Stamp))
	case obs.pod.missing && s.DeletionTimestamp.IsZero() && (s.Status.ArchivedAt != nil || obs.archived || (volumeTerminating(obs) && s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended && rescued(s))):
		stamp := ""
		if s.Status.Rescue != nil {
			stamp = s.Status.Rescue.Stamp
		}
		return v1alpha1.PhaseArchived, notReady("Archived", fmt.Sprintf("the archive timer deleted the volume %s after the rescue %s; an archived session is not resumed, because a new volume would start its task again: restore it from its bundle instead (D-62)", HomeClaimName(s.Name), stamp))
	case obs.pod.missing && s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended:
		return v1alpha1.PhaseSuspended, notReady("Suspended", "the session is suspended; its volume is kept")
	case obs.pod.missing && !s.DeletionTimestamp.IsZero():
		return v1alpha1.PhasePending, notReady("Deleting", "the session is being deleted and has no pod")
	case obs.pod.missing && volumeTerminating(obs):
		return v1alpha1.PhasePending, notReady("VolumeTerminating", fmt.Sprintf("the volume %s is being deleted, so no pod starts on it", HomeClaimName(s.Name)))
	case obs.pod.missing && obs.templatesErr != nil:
		return v1alpha1.PhasePending, notReady("TemplatesInvalid", obs.templatesErr.Error())
	case obs.pod.missing && errors.As(obs.buildErr, new(sessionInvalidError)):
		return v1alpha1.PhaseFailed, notReady("SessionInvalid", obs.buildErr.Error())
	case obs.pod.missing && obs.buildErr != nil:
		return v1alpha1.PhasePending, notReady("PodSpecInvalid", obs.buildErr.Error())
	case obs.pod.missing:
		return v1alpha1.PhasePending, notReady("Creating", "creating the pod")
	case isHoldPod(pod):
		// The 4.1 edge "Failed → Suspended: rescue what is on the volume": no
		// agent runs, and the pod holds the volume for its rescue (D-55).
		state := "it is starting"
		if pod.Status.Phase == corev1.PodRunning {
			state = "it runs on " + pod.Spec.NodeName
		}
		return v1alpha1.PhaseSuspended, notReady("HoldPod", "the hold pod holds the volume for a rescue and runs no agent (D-55); "+state)
	case !pod.DeletionTimestamp.IsZero():
		return v1alpha1.PhasePending, notReady("PodTerminating", "the pod is terminating; a new one starts on the same volume once it is gone")
	case pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded:
		reason := pod.Status.Reason
		if reason == "" {
			reason = string(pod.Status.Phase)
		}
		return v1alpha1.PhaseFailed, notReady("PodEnded", fmt.Sprintf("the pod ended (%s): %s; its volume is kept", reason, pod.Status.Message))
	case pod.Status.Phase == corev1.PodRunning && podReady(pod):
		return v1alpha1.PhaseRunning, metav1.Condition{Status: metav1.ConditionTrue, Reason: "PodReady", Message: "the pod is Ready on " + pod.Spec.NodeName}
	default:
		reason, msg := pendingReason(pod)
		return v1alpha1.PhasePending, notReady(reason, msg)
	}
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// pendingReason is the scheduler's reason when the pod is not placed (D-21),
// else the first container's waiting reason.
func pendingReason(p *corev1.Pod) (string, string) {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			reason := c.Reason
			if reason == "" {
				reason = "Unschedulable"
			}
			return reason, c.Message
		}
	}
	if p.Spec.NodeName == "" {
		return "Scheduling", "waiting for the scheduler to place the pod"
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if w := cs.State.Waiting; w != nil {
			msg := w.Reason
			if w.Message != "" {
				msg += ": " + w.Message
			}
			return w.Reason, msg
		}
		if t := cs.State.Terminated; t != nil {
			return "ContainerTerminated", fmt.Sprintf("container %s exited %d (%s); the kubelet restarts it", cs.Name, t.ExitCode, t.Reason)
		}
	}
	return "Starting", "the pod is placed on " + p.Spec.NodeName + " and starting"
}
