package controller

// The HOME expiry of a private Session, up to Archived (DESIGN-025 section 5,
// unit H5). A Session is eligible once its recorded executor has a confirmed
// terminal record (H1), and triggered while it is Reaping, or Suspended past
// its archive deadline. Then the operator records an expiry record, waits out
// the grace (30 days, or until the owner's release-now), removes the ended
// executor Pod, makes a hold pod bound to the HOME's UID with its intent
// recorded first, rescues the Git work in it, exports the provider history
// from it straight into the shelf's history archive, verifies the entry there
// and records Archived. Every step starts from uncached reads and re-checks
// the Session, its retention identity, the terminal record, the expiry record
// and the HOME claim. A resume, an archive timer no longer due or a new Pod
// cancels; an owner hold stops every step. Canary originals never enter it.
// Default off (--enable-home-history-archive). Nothing here deletes a HOME:
// Deleting and Expired are H6's, behind their own flag.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/templates"
)

// ConditionHomeExpiry reports the HOME expiry's phase, refusal or hold, and
// when its grace ends (DESIGN-025 5.1).
const ConditionHomeExpiry = "HomeExpiry"

// DefaultHomeExpiryGrace is Q-31's 30 days; --home-expiry-grace may only
// lengthen it.
const DefaultHomeExpiryGrace = 720 * time.Hour

const (
	// homeExpiryUnknownRetry: a step refused for Unknown is tried again in a
	// minute; homeExpiryRefusalRetry: any other refusal is re-checked, and
	// counted and recorded again, every 15 minutes (DESIGN-025 5.2).
	homeExpiryUnknownRetry = time.Minute
	homeExpiryRefusalRetry = 15 * time.Minute
	// homeExpiryPodWait: a hold pod that starts or terminates is looked at
	// again this soon; its own events reconcile the Session too.
	homeExpiryPodWait = 30 * time.Second
	// homeExpiryMaxWait caps a requeue while the grace runs.
	homeExpiryMaxWait = 24 * time.Hour
)

// The expiry's annotations on its hold pod (DESIGN-025 6.1), beside
// privateHistoryHomeAnnotation, which carries the HOME UID.
const (
	homeExpiryExecutorAnnotation   = v1alpha1.LabelPrefix + "home-expiry-executor"
	homeExpiryHoldIntentAnnotation = v1alpha1.LabelPrefix + "home-expiry-hold-intent"
)

// The refusal reasons (DESIGN-025 section 10). An export refusal keeps
// agentd's code.
const (
	expiryUnknown            = "unknown"
	expiryIdentityChanged    = "identity_changed"
	expiryHomeReplaced       = "home_replaced"
	expiryClaimInUse         = "claim_in_use"
	expiryCanary             = "canary"
	expiryOverrideInvalid    = "override_invalid"
	expiryShelfUnavailable   = "shelf_unavailable"
	expiryArchiveConflict    = "archive_conflict"
	expiryArchiveMissing     = "archive_missing"
	expiryArchiveMismatch    = "archive_mismatch"
	expiryExportInvalid      = "export_invalid"
	expiryPreconditionFailed = "precondition_failed"
	expiryForbidden          = "forbidden"
)

// homeExpiryReasons are every refusal reason, each series present at 0.
var homeExpiryReasons = []string{
	expiryUnknown, expiryIdentityChanged, expiryHomeReplaced, expiryClaimInUse, expiryCanary, expiryOverrideInvalid,
	protocol.HistoryErrLayoutUnknown, protocol.HistoryErrUnknownEntry, protocol.HistoryErrUnexpectedEntry,
	protocol.HistoryErrCredentialInTree, protocol.HistoryErrTooLarge, protocol.HistoryErrChanged,
	expiryShelfUnavailable, expiryArchiveConflict, expiryArchiveMissing, expiryArchiveMismatch, expiryExportInvalid,
	expiryPreconditionFailed, expiryForbidden,
}

// homeExpiryPhaseOrder is the only order an expiry record moves in.
var homeExpiryPhaseOrder = map[string]int{
	v1alpha1.HomeExpiryWaiting: 0, v1alpha1.HomeExpiryRescuing: 1, v1alpha1.HomeExpiryExporting: 2,
	v1alpha1.HomeExpiryArchived: 3, v1alpha1.HomeExpiryDeleting: 4, v1alpha1.HomeExpiryExpired: 5,
}

// expiryPhaseAtLeast reports whether phase is at or after min.
func expiryPhaseAtLeast(phase, min string) bool {
	p, ok := homeExpiryPhaseOrder[phase]
	return ok && p >= homeExpiryPhaseOrder[min]
}

// homeExpiryOverride is the owner's override as the expiry reads it: hold,
// release-now, "" for none, and anything else is invalid (override_invalid),
// which the expiry ignores. The stop request reads any value but release-now
// as a hold (homeExpiryHeld, fail closed); the expiry's steps need no such
// rule, because an invalid value is counted and alerts nothing away.
func homeExpiryOverride(s *v1alpha1.AgentSession) string {
	v, ok := s.Annotations[v1alpha1.AnnotationHomeExpiry]
	switch {
	case !ok:
		return ""
	case v == v1alpha1.HomeExpiryHold, v == v1alpha1.HomeExpiryReleaseNow:
		return v
	default:
		return expiryOverrideInvalid
	}
}

func (r *Reconciler) homeExpiryOn() bool { return r.HomeHistoryArchive && r.PrivateTerminalRecord }

func (r *Reconciler) homeExpiryGrace() time.Duration {
	return max(r.HomeExpiryGrace, DefaultHomeExpiryGrace)
}

// homeExpiryEligible is DESIGN-025 section 2's "eligible": private, an intact
// retention record with a Started admission and a bound HOME, and a confirmed
// terminal record of the recorded executor.
func homeExpiryEligible(s *v1alpha1.AgentSession) error {
	h := s.Status.PrivateHistory
	switch {
	case s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil:
		return errors.New("only a private Session's HOME expires")
	case privateHistoryIdentity(s) != nil:
		return privateHistoryIdentity(s)
	case h.Admission != sharedStarted || h.HomeUID == "":
		return errors.New("the private HOME was never bound")
	case !executorTerminalRecorded(h):
		return errors.New("the recorded executor has no terminal record")
	}
	return nil
}

type expiryTrigger int

const (
	expiryNotTriggered expiryTrigger = iota
	expiryTriggered
	expiryTriggerUnknown
)

// homeExpiryTriggered is section 2's "triggered": Reaping, or Suspended past
// the D-62 archive deadline as archiveDue computes it. Templates that do not
// load, when the Session has no archiveAfter of its own, are Unknown, never
// "not due". A record that reached Deleting is triggered whatever the timer
// says. wait is how long until a Suspended Session's deadline.
func homeExpiryTriggered(s *v1alpha1.AgentSession, t *templates.Templates, now time.Time) (expiryTrigger, time.Duration) {
	if e := s.Status.PrivateHistory.Expiry; e != nil && expiryPhaseAtLeast(e.Phase, v1alpha1.HomeExpiryDeleting) {
		return expiryTriggered, 0
	}
	if !s.DeletionTimestamp.IsZero() {
		return expiryTriggered, 0
	}
	if s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || s.Status.SuspendedAt == nil {
		return expiryNotTriggered, 0
	}
	var after time.Duration
	switch l := s.Spec.Lifecycle; {
	case l != nil && l.ArchiveAfter != nil:
		after = l.ArchiveAfter.Duration
	case t != nil:
		after = t.ArchiveAfter()
	default:
		return expiryTriggerUnknown, 0
	}
	if at := s.Status.SuspendedAt.Add(after); now.Before(at) {
		return expiryNotTriggered, at.Sub(now) + time.Second
	}
	return expiryTriggered, 0
}

// recordedExecutorEnded reports whether the Pod of a private Session's name is
// its recorded executor, ended in a terminal phase with its terminal record
// confirmed.
func recordedExecutorEnded(s *v1alpha1.AgentSession, obs observation) bool {
	h := s.Status.PrivateHistory
	return privateHistoryProtected(s) && s.Spec.Workspace == nil && !obs.pod.missing && obs.pod.foreign == "" &&
		executorTerminalRecorded(h) && string(obs.pod.obj.UID) == h.ExecutorTerminal.PodUID && podPhaseTerminal(obs.pod.obj)
}

// expiryView is one reconcile's uncached reads for the HOME expiry: the
// Session, the Pod of its name, its HOME claim and the Pods of other names
// that mount that claim.
type expiryView struct {
	s      *v1alpha1.AgentSession
	pod    *corev1.Pod
	claim  *corev1.PersistentVolumeClaim
	mounts []corev1.Pod
	now    time.Time
}

func (v *expiryView) record() *v1alpha1.PrivateHomeExpiryStatus {
	return v.s.Status.PrivateHistory.Expiry
}

// ownHold is the Pod of the Session's name when it is the hold pod the expiry
// record names.
func (v *expiryView) ownHold() *corev1.Pod {
	e := v.record()
	if v.pod == nil || e == nil || e.HoldPodUID == "" || string(v.pod.UID) != e.HoldPodUID {
		return nil
	}
	return v.pod
}

// awaitingAdoption reports whether p is the hold pod this record's intent
// created, not yet recorded as holdPodUID (DESIGN-025 6.1): an operator can
// restart between the create and that write. The controller reference names
// the Session's UID, which a Job's Pod cannot carry.
func (v *expiryView) awaitingAdoption(p *corev1.Pod) bool {
	e := v.record()
	return e != nil && e.HoldIntent != "" && e.HoldPodUID == "" && p.UID != "" && isHoldPod(p) && controlledBySession(p, v.s) &&
		expiryHoldAnnotated(p, e)
}

// expiryHoldAnnotated reports whether p carries the record's three hold pod
// annotations.
func expiryHoldAnnotated(p *corev1.Pod, e *v1alpha1.PrivateHomeExpiryStatus) bool {
	return e.HoldIntent != "" && p.Annotations[homeExpiryHoldIntentAnnotation] == e.HoldIntent &&
		p.Annotations[homeExpiryExecutorAnnotation] == e.ExecutorUID && p.Annotations[privateHistoryHomeAnnotation] == e.HomeUID
}

// newPod is a Pod of the Session's name, or one that mounts its HOME claim,
// that is neither the recorded executor, nor the hold pod the record names,
// nor the record's hold pod awaiting adoption (DESIGN-025 5.3).
func (v *expiryView) newPod() *corev1.Pod {
	h := v.s.Status.PrivateHistory
	e := h.Expiry
	known := func(p *corev1.Pod) bool {
		switch {
		case h.LastExecutor != nil && string(p.UID) == h.LastExecutor.PodUID:
			return true
		case e != nil && e.HoldPodUID != "" && string(p.UID) == e.HoldPodUID:
			return true
		default:
			return v.awaitingAdoption(p)
		}
	}
	if v.pod != nil && !known(v.pod) {
		return v.pod
	}
	for i := range v.mounts {
		if !known(&v.mounts[i]) {
			return &v.mounts[i]
		}
	}
	return nil
}

// errExpiryBehind: the uncached Session is gone, or another one has the name;
// the cache is behind, and its event reconciles again.
var errExpiryBehind = errors.New("the Session changed under the HOME expiry")

// readExpiryView makes the uncached reads every step starts from. An error
// other than errExpiryBehind is Unknown.
func (r *Reconciler) readExpiryView(ctx context.Context, s *v1alpha1.AgentSession) (*expiryView, error) {
	if r.APIReader == nil {
		return nil, errors.New("the HOME expiry needs an uncached reader")
	}
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errExpiryBehind
		}
		return nil, fmt.Errorf("read the Session: %w", err)
	}
	if fresh.UID != s.UID || fresh.Status.PrivateHistory == nil {
		return nil, errExpiryBehind
	}
	v := &expiryView{s: &fresh, now: r.clock()}
	var pod corev1.Pod
	switch err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(&fresh), &pod); {
	case err == nil:
		v.pod = &pod
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("read the Pod of the Session's name: %w", err)
	}
	var claim corev1.PersistentVolumeClaim
	switch err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: fresh.Namespace, Name: fresh.Status.PrivateHistory.HomeClaim}, &claim); {
	case err == nil:
		v.claim = &claim
	case !apierrors.IsNotFound(err):
		return nil, fmt.Errorf("read the HOME claim: %w", err)
	}
	var pods corev1.PodList
	if err := r.APIReader.List(ctx, &pods, client.InNamespace(fresh.Namespace)); err != nil {
		return nil, fmt.Errorf("list the Pods that may mount the HOME: %w", err)
	}
	for _, p := range pods.Items {
		if p.Name != fresh.Name && podMountsClaim(&p, fresh.Status.PrivateHistory.HomeClaim) {
			v.mounts = append(v.mounts, p)
		}
	}
	return v, nil
}

// podMountsClaim reports whether any volume of p names the claim.
func podMountsClaim(p *corev1.Pod, claim string) bool {
	for _, vol := range p.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == claim {
			return true
		}
	}
	return false
}

// expiryRefusal is a step refused with its section 10 reason.
type expiryRefusal struct {
	reason string
	err    error
}

func (e *expiryRefusal) Error() string { return e.reason + ": " + e.err.Error() }

func refusal(reason, format string, args ...any) *expiryRefusal {
	return &expiryRefusal{reason: reason, err: fmt.Errorf(format, args...)}
}

// expiryRecheck is the common re-check of DESIGN-025 5.2 on the uncached view:
// the Session is still eligible, the record rests on its terminal record and
// names its HOME, and the HOME claim is the bound one, controlled by the
// Session, with the operator's finalizer and admission annotation, and not
// being deleted.
func expiryRecheck(v *expiryView) *expiryRefusal {
	s, h := v.s, v.s.Status.PrivateHistory
	e := h.Expiry
	if err := homeExpiryEligible(s); err != nil {
		return refusal(expiryIdentityChanged, "the Session is no longer eligible: %v", err)
	}
	if e.ExecutorUID != h.LastExecutor.PodUID || e.ExecutorUID != h.ExecutorTerminal.PodUID || e.HomeUID != h.HomeUID {
		return refusal(expiryIdentityChanged, "the expiry record no longer names the recorded executor and HOME")
	}
	c := v.claim
	switch {
	case c == nil:
		return refusal(expiryHomeReplaced, "the HOME claim %s is gone", h.HomeClaim)
	case string(c.UID) != h.HomeUID:
		return refusal(expiryHomeReplaced, "the HOME claim %s has UID %s, not the bound %s", c.Name, c.UID, h.HomeUID)
	case !c.DeletionTimestamp.IsZero():
		return refusal(expiryHomeReplaced, "the HOME claim %s is being deleted", c.Name)
	case !controlledBySessionObject(c, s) || !controllerutil.ContainsFinalizer(c, Finalizer) ||
		c.Annotations[privateHistoryAdmissionAnnotation] != strconv.FormatInt(h.BindingGeneration, 10):
		return refusal(expiryIdentityChanged, "the HOME claim's owner, finalizer or admission is not the Session's")
	}
	return nil
}

// homeExpiryNote is what this reconcile found about the expiry, for the
// HomeExpiry condition when no record says it.
type homeExpiryNote struct {
	reason  string
	message string
	unknown bool
}

// reconcileHomeExpiry runs DESIGN-025 section 5 up to Archived for a protected
// private Session. It runs after the private history observation found
// nothing to write, so the terminal record it rests on is stored and
// confirmed. stop means the reconcile ends here with res: a step wrote or
// deleted something, and the next reconcile judges from the API server again.
func (r *Reconciler) reconcileHomeExpiry(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (ctrl.Result, bool, error) {
	if s.Spec.Workspace != nil || s.Status.PrivateHistory == nil {
		return ctrl.Result{}, false, nil
	}
	if !r.homeExpiryOn() {
		return r.homeExpiryOff(ctx, s, obs)
	}
	if s.Spec.ProjectCanary != nil {
		// Canary originals never expire, and release-now does not reach them
		// (DESIGN-025 2): the fixed result reader reads their records live.
		if s.Annotations[v1alpha1.AnnotationHomeExpiry] == v1alpha1.HomeExpiryReleaseNow {
			r.countExpiryOnce(s, expiryCanary)
		}
		return ctrl.Result{}, false, nil
	}
	h := s.Status.PrivateHistory
	if h.Expiry == nil && !executorTerminalRecorded(h) {
		// Not eligible, and nothing to cancel: no read needed.
		return ctrl.Result{}, false, nil
	}
	v, err := r.readExpiryView(ctx, s)
	if errors.Is(err, errExpiryBehind) {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	if err != nil {
		return r.expiryUnknown(ctx, obs, err)
	}
	if homeExpiryOverride(v.s) == expiryOverrideInvalid {
		r.countExpiryOnce(v.s, expiryOverrideInvalid)
	}
	trig, wait := homeExpiryTriggered(v.s, obs.templates, v.now)
	if v.record() == nil {
		return r.startHomeExpiry(ctx, v, obs, trig, wait)
	}
	return r.stepHomeExpiry(ctx, s, v, obs, trig)
}

// startHomeExpiry creates the expiry record of an eligible, triggered Session
// (5.1). While anything but the recorded executor holds the HOME it waits, as
// a new Pod would cancel the record at once.
func (r *Reconciler) startHomeExpiry(ctx context.Context, v *expiryView, obs *observation, trig expiryTrigger, wait time.Duration) (ctrl.Result, bool, error) {
	if homeExpiryEligible(v.s) != nil {
		return ctrl.Result{}, false, nil
	}
	switch trig {
	case expiryTriggerUnknown:
		return r.expiryUnknown(ctx, obs, errors.New("the archive deadline is unknown: the Session has no archiveAfter of its own and the templates do not load"))
	case expiryNotTriggered:
		if wait > 0 {
			return ctrl.Result{RequeueAfter: min(wait, homeExpiryMaxWait)}, false, nil
		}
		return ctrl.Result{}, false, nil
	}
	if p := v.newPod(); p != nil {
		obs.privateHistoryUnknown = fmt.Errorf("private executor was replaced: Pod %s (uid %s) holds the private HOME, so the HOME expiry does not start", p.Name, p.UID)
		return ctrl.Result{}, false, nil
	}
	h := v.s.Status.PrivateHistory
	start := h.ExecutorTerminal.ObservedAt
	if h.ExpiryClearedAt != nil {
		// After a cancel, a new 30 days from when the Session is found
		// triggered again (5.1).
		start = recordTime(v.now)
	}
	fresh := v.s.DeepCopy()
	e := &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID,
		GraceStart: start, DueAt: metav1.NewTime(start.Add(r.homeExpiryGrace())), Phase: v1alpha1.HomeExpiryWaiting}
	if homeExpiryOverride(v.s) == v1alpha1.HomeExpiryReleaseNow {
		at := recordTime(v.now)
		e.ReleaseNowAt = &at
	}
	fresh.Status.PrivateHistory.Expiry = e
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("started the HOME expiry of a private Session", "executor", e.ExecutorUID, "dueAt", e.DueAt.UTC().Format(time.RFC3339))
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HomeExpiryStarted",
		fmt.Sprintf("the HOME expiry started for the ended executor %s; its grace ends at %s (DESIGN-025 5.1)", e.ExecutorUID, e.DueAt.UTC().Format(time.RFC3339)))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// stepHomeExpiry runs the record's next step, after the cancels and the hold.
func (r *Reconciler) stepHomeExpiry(ctx context.Context, s *v1alpha1.AgentSession, v *expiryView, obs *observation, trig expiryTrigger) (ctrl.Result, bool, error) {
	e := v.record()
	if expiryPhaseAtLeast(e.Phase, v1alpha1.HomeExpiryDeleting) {
		// Deleting and Expired are H6's: nothing cancels them, and this
		// operator deletes no HOME.
		return ctrl.Result{}, false, nil
	}
	// A hold pod made before a restart is adopted first, then judged (6.1).
	if v.pod != nil && v.awaitingAdoption(v.pod) {
		return r.adoptExpiryHold(ctx, v)
	}
	if p := v.newPod(); p != nil {
		return r.cancelHomeExpiry(ctx, v, obs, fmt.Sprintf("a new Pod %s (uid %s) holds the HOME", p.Name, p.UID), true)
	}
	if trig == expiryNotTriggered {
		return r.cancelHomeExpiry(ctx, v, obs, "the Session is no longer triggered: resumed, or its archive deadline moved", false)
	}
	if homeExpiryOverride(v.s) == v1alpha1.HomeExpiryHold {
		// Nothing moves on while the owner holds it, and the hold pod goes,
		// because nothing needs it; the grace keeps running (5.3).
		if p := v.ownHold(); p != nil && p.DeletionTimestamp.IsZero() {
			return r.removeExpiryHold(ctx, v, p, holdReleaseOwnerHold)
		}
		obs.homeExpiry = &homeExpiryNote{reason: "Held", message: "the owner holds the HOME expiry; nothing moves on until the hold is removed"}
		return ctrl.Result{}, false, nil
	}
	if trig == expiryTriggerUnknown {
		return r.expiryUnknown(ctx, obs, errors.New("the archive deadline is unknown: the Session has no archiveAfter of its own and the templates do not load"))
	}
	if e.Refusal != "" && e.RefusalAt != nil && v.now.Before(e.RefusalAt.Add(homeExpiryRefusalRetry)) {
		// A refused step is re-checked every 15 minutes, not on every event.
		return ctrl.Result{RequeueAfter: e.RefusalAt.Add(homeExpiryRefusalRetry).Sub(v.now) + time.Second}, false, nil
	}
	if ref := expiryRecheck(v); ref != nil {
		return r.refuseHomeExpiry(ctx, v, obs, ref)
	}
	switch e.Phase {
	case v1alpha1.HomeExpiryWaiting:
		return r.expiryWait(ctx, v)
	case v1alpha1.HomeExpiryRescuing, v1alpha1.HomeExpiryExporting:
		return r.expiryRescueAndExport(ctx, v, obs)
	case v1alpha1.HomeExpiryArchived:
		// The archive is verified and recorded; the hold pod goes. With
		// deletion off (H5 has none) the expiry stops here, HOME intact.
		if p := v.ownHold(); p != nil && p.DeletionTimestamp.IsZero() {
			return r.removeExpiryHold(ctx, v, p, holdReleaseArchived)
		}
		return ctrl.Result{}, false, nil
	}
	return ctrl.Result{}, false, nil
}

// expiryWait is the Waiting step: on to Rescuing once the grace is over, or
// once the owner asked for release-now, which skips the grace and nothing else.
func (r *Reconciler) expiryWait(ctx context.Context, v *expiryView) (ctrl.Result, bool, error) {
	e := v.record()
	release := homeExpiryOverride(v.s) == v1alpha1.HomeExpiryReleaseNow
	if !release && v.now.Before(e.DueAt.Time) {
		return ctrl.Result{RequeueAfter: min(e.DueAt.Sub(v.now)+time.Second, homeExpiryMaxWait)}, false, nil
	}
	fresh := v.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	next.Phase = v1alpha1.HomeExpiryRescuing
	if release && next.ReleaseNowAt == nil {
		at := recordTime(v.now)
		next.ReleaseNowAt = &at
	}
	clearExpiryRefusal(next)
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	why := "its grace ended at " + e.DueAt.UTC().Format(time.RFC3339)
	if release && v.now.Before(e.DueAt.Time) {
		why = "the owner asked for release-now"
	}
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HomeExpiryRescuing", "the HOME expiry moves to its rescue: "+why)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// expiryRescueAndExport is the Rescuing and Exporting steps: the ended
// executor goes, a hold pod bound to the HOME UID takes its name, the Git
// rescue runs in it, then the history is exported from it into the archive.
// Exporting needs a valid rescue in the current hold pod too: a hold pod made
// again (after an owner hold, or archive off and on) is rescued again first.
func (r *Reconciler) expiryRescueAndExport(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	e := v.record()
	pod := v.pod
	switch {
	case pod != nil && string(pod.UID) == e.ExecutorUID:
		if e.Phase != v1alpha1.HomeExpiryRescuing {
			return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryIdentityChanged, "the recorded executor Pod stands at %s", e.Phase))
		}
		if !pod.DeletionTimestamp.IsZero() {
			return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
		}
		return r.removeExpiredExecutor(ctx, v, obs, pod)
	case pod != nil && v.ownHold() == pod:
		// The hold pod the record names.
	case pod != nil:
		// newPod would have cancelled; a Pod here is not the expiry's.
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryIdentityChanged, "the Pod %s is neither the executor nor the expiry's hold pod", pod.Name))
	case len(v.mounts) > 0:
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryClaimInUse, "the Pod %s mounts the HOME claim", v.mounts[0].Name))
	case e.HoldPodUID != "" || e.HoldIntent == "":
		// No hold pod yet, or the old one is gone: a new intent, by a
		// confirmed write, before any create (6.1).
		fresh := v.s.DeepCopy()
		next := fresh.Status.PrivateHistory.Expiry
		next.HoldPodUID, next.HoldIntent = "", string(uuid.NewUUID())
		clearExpiryRefusal(next)
		if err := r.writeExpiry(ctx, v, fresh); err != nil {
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	default:
		return r.createExpiryHold(ctx, v, obs)
	}

	hold := pod
	if !hold.DeletionTimestamp.IsZero() {
		// A removed hold pod still terminating; the record keeps naming it
		// until it is gone, so it is never a new Pod.
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}
	if !expiryHoldAnnotated(hold, e) || !isHoldPod(hold) || !controlledBySession(hold, v.s) || !privateExecutorUsesHome(hold, v.s.Status.PrivateHistory.HomeClaim) {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryIdentityChanged, "the hold pod %s is not bound to the HOME and the record", hold.Name))
	}
	if podEnded(hold) {
		return r.removeExpiryHold(ctx, v, hold, holdReleaseEnded)
	}
	if hold.Status.Phase != corev1.PodRunning {
		obs.homeExpiry = &homeExpiryNote{reason: "HoldPodStarting", message: "the hold pod " + hold.Name + " is starting"}
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}

	rec := v.s.Status.Rescue
	if !rescued(v.s) || !rescueRanIn(v.s, hold) {
		if rec != nil && rec.Result == v1alpha1.RescueFailed && rescueRanIn(v.s, hold) && !holdRetryDue(v.s, v.now, r.holdRetry()) {
			next := v.now.Add(r.holdRetry())
			if rec.At != nil {
				next = rec.At.Add(r.holdRetry())
			}
			obs.homeExpiry = &homeExpiryNote{reason: "RescueFailed", message: "the rescue in the hold pod failed and runs again at " + next.UTC().Format(time.RFC3339) + ": " + rec.Message}
			return ctrl.Result{RequeueAfter: max(time.Second, next.Sub(v.now))}, false, nil
		}
		return r.expiryRescue(ctx, v, obs, hold)
	}
	if e.Phase == v1alpha1.HomeExpiryRescuing || e.RescueStamp != rec.Stamp {
		fresh := v.s.DeepCopy()
		next := fresh.Status.PrivateHistory.Expiry
		next.Phase, next.RescueStamp = v1alpha1.HomeExpiryExporting, rec.Stamp
		clearExpiryRefusal(next)
		if err := r.writeExpiry(ctx, v, fresh); err != nil {
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	return r.exportHomeHistory(ctx, v, hold, obs)
}

// expiryRescue runs the Git rescue in the hold pod and records its verdict in
// status.rescue, as D-51 and D-55 do (6.2). A failure keeps the hold pod and
// runs again after HoldRetry; RescueFailed pages as for any rescue (D-57).
func (r *Reconciler) expiryRescue(ctx context.Context, v *expiryView, obs *observation, hold *corev1.Pod) (ctrl.Result, bool, error) {
	rec, reason, _, err := r.rescue(ctx, v.s, hold)
	if err != nil {
		return r.expiryUnknown(ctx, obs, fmt.Errorf("the rescue could not run in the hold pod %s: %w", hold.Name, err))
	}
	written, err := r.recordRescue(ctx, v.s, rec, reason)
	if err != nil {
		return r.expiryUnknown(ctx, obs, fmt.Errorf("record the rescue in the hold pod %s: %w", hold.Name, err))
	}
	if !written {
		// The Session changed during the rescue: judge it again.
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	r.event(v.s, eventType(rec), "Rescue", reason, rec.Message)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// historyExportArgs are the export's flags for the Session: the executor's
// provider directories from its terminal record, and the Git rescue's bundle
// directory when the rescue Verified one. An empty directory makes agentd
// refuse layout_unknown rather than read a default (6.3).
func historyExportArgs(s *v1alpha1.AgentSession) (HistoryExportArgs, error) {
	t, rec := s.Status.PrivateHistory.ExecutorTerminal, s.Status.Rescue
	args := HistoryExportArgs{Session: s.Name, ClaudeConfigDir: t.ClaudeConfigDir, CodexHome: t.CodexHome}
	if rec.Result != v1alpha1.RescueVerified {
		return args, nil
	}
	dir, file := path.Split(rec.LastBundle)
	id, ok := strings.CutPrefix(path.Clean(dir), protocol.RescueRoot+"/")
	session, name, err := protocol.ParseRescueID(id)
	if !ok || file != "manifest.json" || err != nil || session != s.Name || (name != rec.Stamp && !strings.HasPrefix(name, rec.Stamp+"-")) {
		return args, fmt.Errorf("the Verified rescue %s names no bundle directory of its own (%q)", rec.Stamp, rec.LastBundle)
	}
	args.Rescue = id
	return args, nil
}

// historyProject is the Session's saved project (the claim snapshot), or ""
// when it has none: a history with no project is readable by no API caller.
func historyProject(s *v1alpha1.AgentSession) string {
	raw := s.Annotations[v1alpha1.AnnotationProjectSnapshot]
	if raw == "" {
		return ""
	}
	snap, err := projectcatalog.ParseSnapshot([]byte(raw))
	if err != nil || snap.Selected().Name != s.Spec.Repo || !protocol.ValidHistoryProject(snap.Project()) {
		return ""
	}
	return snap.Project()
}

// historyContent joins what the operator knows with the hold pod's plan
// (6.4): content.json's canonical bytes and their SHA-256. Nothing in it
// changes on a retry, so an identical retry gives the same digest.
func historyContent(s *v1alpha1.AgentSession) HistoryArchiveContent {
	h, rec := s.Status.PrivateHistory, s.Status.Rescue
	t := h.ExecutorTerminal
	c := protocol.HistoryContent{Format: protocol.HistoryArchiveFormat,
		Session: protocol.HistorySession{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)},
		Project: historyProject(s), Provider: string(s.Spec.Agent), HomeUID: h.HomeUID,
		Terminal: protocol.HistoryTerminal{PodUID: t.PodUID, Phase: t.Phase, Reason: t.Reason, NodeName: t.NodeName, ObservedAt: t.ObservedAt.UTC()},
		Rescue:   protocol.HistoryRescue{Stamp: rec.Stamp, Result: string(rec.Result), PodUID: rec.PodUID}}
	return func(plan protocol.HistoryExportPlan, planSHA256 string) ([]byte, string, error) {
		content := c
		protocol.HistoryContentFromPlan(&content, plan, planSHA256)
		sum, data, err := protocol.CanonicalSHA256(content)
		return data, sum, err
	}
}

// exportHomeHistory is the Exporting step (6.3, 6.4): the export in the hold
// pod piped into put in the shelf, then verify under the archive's shared
// .shelf.lock, then Archived with the archive recorded.
func (r *Reconciler) exportHomeHistory(ctx context.Context, v *expiryView, hold *corev1.Pod, obs *observation) (ctrl.Result, bool, error) {
	if r.HistoryRelay == nil {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryShelfUnavailable, "this operator has no history relay"))
	}
	args, err := historyExportArgs(v.s)
	if err != nil {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryExportInvalid, "%v", err))
	}
	build := historyContent(v.s)
	var contentSHA string
	put, err := r.HistoryRelay.Archive(ctx, hold, args, func(plan protocol.HistoryExportPlan, planSHA256 string) ([]byte, string, error) {
		data, sum, err := build(plan, planSHA256)
		contentSHA = sum
		return data, sum, err
	})
	if errors.Is(err, errHistoryRelayBusy) {
		return ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
	}
	if err != nil {
		return r.refuseHomeExpiry(ctx, v, obs, historyRefusal(err))
	}
	if put.ContentSHA256 != contentSHA || !protocol.ValidSHA256(contentSHA) {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryArchiveMismatch, "put stored %s, not the content manifest's %s", put.ContentSHA256, contentSHA))
	}
	ver, err := r.HistoryRelay.Verify(ctx, string(v.s.UID), contentSHA)
	if errors.Is(err, errHistoryRelayBusy) {
		return ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
	}
	if err != nil {
		return r.refuseHomeExpiry(ctx, v, obs, historyRefusal(err))
	}
	if ver.ContentSHA256 != contentSHA || ver.Entry != put.Entry {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryArchiveMismatch, "verify found %s at %s, not %s at %s", ver.ContentSHA256, ver.Entry, contentSHA, put.Entry))
	}
	return r.recordHomeArchive(ctx, v, hold, put, obs)
}

// recordHomeArchive records Archived on the Session as the API server has it
// after the export, which took minutes: the record must still be at Exporting
// with the same hold pod and rescue, the Session still eligible, triggered and
// unheld. Otherwise the next reconcile judges again, and a repeated put
// answers exists.
func (r *Reconciler) recordHomeArchive(ctx context.Context, v *expiryView, hold *corev1.Pod, put protocol.HistoryPutResult, obs *observation) (ctrl.Result, bool, error) {
	after, err := r.readExpiryView(ctx, v.s)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	e, before := after.record(), v.record()
	trig, _ := homeExpiryTriggered(after.s, obs.templates, after.now)
	if e == nil || e.Phase != v1alpha1.HomeExpiryExporting || e.HoldPodUID != before.HoldPodUID || e.RescueStamp != before.RescueStamp ||
		after.ownHold() == nil || after.newPod() != nil || trig != expiryTriggered || homeExpiryOverride(after.s) == v1alpha1.HomeExpiryHold ||
		!rescued(after.s) || !rescueRanIn(after.s, hold) || after.s.Status.Rescue.Stamp != e.RescueStamp || expiryRecheck(after) != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	fresh := after.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	next.Phase = v1alpha1.HomeExpiryArchived
	next.Archive = &v1alpha1.PrivateHomeArchiveStatus{Entry: put.Entry, ContentSHA256: put.ContentSHA256, Bytes: put.Bytes,
		Files: int32(min(put.Files, protocol.MaxHistoryExportFiles)), GitFiles: int32(min(put.GitFiles, protocol.MaxHistoryGitFiles)), ArchivedAt: recordTime(after.now)}
	clearExpiryRefusal(next)
	acknowledged, err := r.writePrivateHistoryAcknowledged(ctx, v.s, fresh)
	if acknowledged && r.HomeExpiryCounters != nil {
		r.HomeExpiryCounters.ArchivedBytes.Add(float64(put.Bytes))
	}
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("archived a private Session's history", "entry", put.Entry, "contentSHA256", put.ContentSHA256, "bytes", put.Bytes, "status", put.Status)
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HomeExpiryArchived",
		fmt.Sprintf("archived the history and Git rescue %s as %s (%d files, %d bytes) and verified it; the HOME stays (DESIGN-025 6.4)", e.RescueStamp, put.Entry, put.Files, put.Bytes))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// historyRefusal maps a relay error to its section 10 reason. agentd's export
// refusals keep their codes; io_error and a failed exec into the hold pod are
// Unknown; a shelf that is missing, unready, unmounted or unreachable is
// shelf_unavailable.
func historyRefusal(err error) *expiryRefusal {
	var he *protocol.HistoryError
	if errors.As(err, &he) {
		reason := expiryUnknown
		switch he.Code {
		case protocol.HistoryErrLayoutUnknown, protocol.HistoryErrUnknownEntry, protocol.HistoryErrUnexpectedEntry,
			protocol.HistoryErrCredentialInTree, protocol.HistoryErrTooLarge, protocol.HistoryErrChanged:
			reason = he.Code
		case protocol.HistoryErrInvalid:
			reason = expiryExportInvalid
		case protocol.HistoryErrConflict:
			reason = expiryArchiveConflict
		case protocol.HistoryErrCorrupt:
			reason = expiryArchiveMismatch
		case protocol.HistoryErrNotFound:
			reason = expiryArchiveMissing
		case protocol.HistoryErrNoArchive:
			reason = expiryShelfUnavailable
		}
		return &expiryRefusal{reason: reason, err: err}
	}
	if errors.Is(err, errShelfUnavailable) {
		return &expiryRefusal{reason: expiryShelfUnavailable, err: err}
	}
	return &expiryRefusal{reason: expiryUnknown, err: err}
}

// cancelHomeExpiry is DESIGN-025 5.3: the expiry's own hold pod goes first and
// is confirmed gone by an uncached read while the record still names it, then
// the record is removed by a confirmed write and expiryClearedAt set. The HOME
// stays, and so does any archive entry already written. replaced: a new Pod
// cancelled it, and the Session gets today's PreservationUnknown.
func (r *Reconciler) cancelHomeExpiry(ctx context.Context, v *expiryView, obs *observation, why string, replaced bool) (ctrl.Result, bool, error) {
	if replaced {
		obs.privateHistoryUnknown = errors.New("private executor was replaced: " + why + "; the HOME expiry is cancelled and the HOME stays")
	}
	if p := v.ownHold(); p != nil {
		if p.DeletionTimestamp.IsZero() {
			return r.removeExpiryHold(ctx, v, p, holdReleaseCancel)
		}
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}
	fresh := v.s.DeepCopy()
	cleared := recordTime(v.now)
	fresh.Status.PrivateHistory.Expiry, fresh.Status.PrivateHistory.ExpiryClearedAt = nil, &cleared
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("cancelled the HOME expiry of a private Session", "why", why)
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HomeExpiryCancelled", "the HOME expiry is cancelled and the HOME stays: "+why)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// adoptExpiryHold records the hold pod this record's intent created as
// holdPodUID, by a confirmed write (6.1).
func (r *Reconciler) adoptExpiryHold(ctx context.Context, v *expiryView) (ctrl.Result, bool, error) {
	fresh := v.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	next.HoldPodUID = string(v.pod.UID)
	clearExpiryRefusal(next)
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("adopted the HOME expiry's hold pod", "pod", v.pod.Name, "uid", v.pod.UID, "intent", next.HoldIntent)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// createExpiryHold makes the hold pod for the recorded intent (6.1): D-55's
// hold pod with the HOME UID, the executor UID and the intent as annotations.
// The intent is stored, the Session's Pod name is NotFound and no Pod mounts
// the claim, by this step's uncached reads.
func (r *Reconciler) createExpiryHold(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if obs.templatesErr != nil || obs.templates == nil {
		return r.expiryUnknown(ctx, obs, fmt.Errorf("no hold pod can be built until the templates load: %w", obs.templatesErr))
	}
	pod, err := buildExpiryHoldPod(v.s, obs.templates)
	if err != nil {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryIdentityChanged, "the hold pod cannot be built: %v", err))
	}
	if err := r.Client.Create(ctx, pod); err != nil {
		switch {
		case apierrors.IsAlreadyExists(err):
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		case apierrors.IsForbidden(err):
			return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryForbidden, "create the hold pod: %v", err))
		}
		return r.expiryUnknown(ctx, obs, fmt.Errorf("create the hold pod: %w", err))
	}
	log.FromContext(ctx).Info("created the HOME expiry's hold pod", "pod", pod.Name, "uid", pod.UID, "intent", v.record().HoldIntent)
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HoldPod", "created hold pod "+pod.Name+" bound to the HOME "+v.record().HomeUID+" for the expiry's rescue and export (DESIGN-025 6.1)")
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// buildExpiryHoldPod is D-55's hold pod for a private Session's expiry, bound
// to its HOME UID by annotation (6.1).
func buildExpiryHoldPod(s *v1alpha1.AgentSession, t *templates.Templates) (*corev1.Pod, error) {
	e := s.Status.PrivateHistory.Expiry
	pod, err := buildHoldPod(s, t)
	if err != nil {
		return nil, err
	}
	pod.Annotations[privateHistoryHomeAnnotation] = e.HomeUID
	pod.Annotations[homeExpiryExecutorAnnotation] = e.ExecutorUID
	pod.Annotations[homeExpiryHoldIntentAnnotation] = e.HoldIntent
	if !privateExecutorUsesHome(pod, s.Status.PrivateHistory.HomeClaim) {
		return nil, errors.New("the hold pod does not mount the bound HOME claim")
	}
	return pod, nil
}

// removeExpiredExecutor deletes the recorded executor's ended Pod in Rescuing,
// so the hold pod can take its name: the terminal record is the proof, not the
// Pod (ADR-005).
func (r *Reconciler) removeExpiredExecutor(ctx context.Context, v *expiryView, obs *observation, pod *corev1.Pod) (ctrl.Result, bool, error) {
	if err := expiryPodRemovalAllowed(v.s, pod); err != nil {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryIdentityChanged, "the ended executor stays: %v", err))
	}
	if err := deleteExpiryExecutor(ctx, r.Client, v.s, pod); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
			r.countExpiryRefused(expiryPreconditionFailed)
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{RequeueAfter: time.Second}, true, err
	}
	log.FromContext(ctx).Info("deleted the ended private executor for the HOME expiry", "pod", pod.Name, "uid", pod.UID)
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "ExecutorRemoved",
		fmt.Sprintf("deleted the ended executor %s, whose terminal record is the stop proof, so a hold pod can take its name (DESIGN-025 5.2)", pod.UID))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// removeExpiryHold deletes the expiry's hold pod when nothing needs it (6.1).
// It is the one caller of deleteExpiryHold.
func (r *Reconciler) removeExpiryHold(ctx context.Context, v *expiryView, pod *corev1.Pod, why expiryHoldRelease) (ctrl.Result, bool, error) {
	if err := expiryHoldRemovalAllowed(v.s, pod, why); err != nil {
		log.FromContext(ctx).Info("the HOME expiry keeps its hold pod", "pod", pod.Name, "reason", err.Error())
		return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
	}
	if err := deleteExpiryHold(ctx, r.Client, v.s, pod, why); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsInvalid(err) {
			r.countExpiryRefused(expiryPreconditionFailed)
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{RequeueAfter: time.Second}, true, err
	}
	log.FromContext(ctx).Info("deleted the HOME expiry's hold pod", "pod", pod.Name, "uid", pod.UID, "why", string(why))
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HoldPodRemoved", fmt.Sprintf("deleted the expiry's hold pod %s: %s (DESIGN-025 6.1)", pod.Name, why))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// homeExpiryOff is section 14's archive off: no new hold pods or exports, a
// record stays in its phase, and the expiry's hold pod goes because nothing
// needs it. A hold pod awaiting adoption is adopted first, then removed.
func (r *Reconciler) homeExpiryOff(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (ctrl.Result, bool, error) {
	if s.Status.PrivateHistory.Expiry == nil || obs.pod.missing || obs.pod.foreign != "" || !isHoldPod(obs.pod.obj) {
		return ctrl.Result{}, false, nil
	}
	v, err := r.readExpiryView(ctx, s)
	if errors.Is(err, errExpiryBehind) {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	if err != nil || v.record() == nil {
		return ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
	}
	if v.pod != nil && v.awaitingAdoption(v.pod) {
		return r.adoptExpiryHold(ctx, v)
	}
	if p := v.ownHold(); p != nil && p.DeletionTimestamp.IsZero() {
		return r.removeExpiryHold(ctx, v, p, holdReleaseArchiveOff)
	}
	return ctrl.Result{}, false, nil
}

// writeExpiry stores fresh, read uncached in v, by the confirmed write; on
// success the reconcile's Session is the confirmed one.
func (r *Reconciler) writeExpiry(ctx context.Context, v *expiryView, fresh *v1alpha1.AgentSession) error {
	target := v.s.DeepCopy()
	if err := r.writePrivateHistory(ctx, target, fresh); err != nil {
		log.FromContext(ctx).Info("the HOME expiry's status write is unconfirmed; judging again", "error", err.Error())
		return err
	}
	*v.s = *target
	return nil
}

func clearExpiryRefusal(e *v1alpha1.PrivateHomeExpiryStatus) { e.Refusal, e.RefusalAt = "", nil }

// refuseHomeExpiry counts a refused step. Unknown changes nothing and asks
// again in a minute. Any other reason is recorded in the record's refusal by
// a confirmed write, re-checked every 15 minutes, and named by the condition
// and the refusing gauge until a step passes.
func (r *Reconciler) refuseHomeExpiry(ctx context.Context, v *expiryView, obs *observation, ref *expiryRefusal) (ctrl.Result, bool, error) {
	if ref.reason == expiryUnknown {
		return r.expiryUnknown(ctx, obs, ref.err)
	}
	r.countExpiryRefused(ref.reason)
	log.FromContext(ctx).Info("the HOME expiry is refused; the HOME stays", "reason", ref.reason, "error", ref.err.Error())
	obs.homeExpiry = &homeExpiryNote{reason: "Refused", message: ref.Error()}
	fresh := v.s.DeepCopy()
	at := recordTime(v.now)
	next := fresh.Status.PrivateHistory.Expiry
	next.Refusal, next.RefusalAt = ref.reason, &at
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	r.event(v.s, corev1.EventTypeWarning, "HomeExpiry", "HomeExpiryRefused", "the HOME expiry is refused and the HOME stays: "+ref.Error())
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// expiryUnknown counts a read, exec or deadline that could not be judged:
// nothing happens, and the reconcile asks again in a minute.
func (r *Reconciler) expiryUnknown(ctx context.Context, obs *observation, err error) (ctrl.Result, bool, error) {
	r.countExpiryRefused(expiryUnknown)
	log.FromContext(ctx).Info("the HOME expiry cannot judge this step; asking again", "retry", homeExpiryUnknownRetry, "error", err.Error())
	obs.homeExpiry = &homeExpiryNote{reason: "Unknown", message: truncate(err.Error()), unknown: true}
	return ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
}

// setHomeExpiryCondition is the HomeExpiry condition, from the record and what
// this reconcile found; it is gone while no record exists and nothing was
// found.
func setHomeExpiryCondition(s *v1alpha1.AgentSession, obs observation, st *v1alpha1.AgentSessionStatus) {
	var e *v1alpha1.PrivateHomeExpiryStatus
	if st.PrivateHistory != nil {
		e = st.PrivateHistory.Expiry
	}
	n := obs.homeExpiry
	if e == nil && n == nil {
		meta.RemoveStatusCondition(&st.Conditions, ConditionHomeExpiry)
		return
	}
	c := metav1.Condition{Type: ConditionHomeExpiry, Status: metav1.ConditionTrue, ObservedGeneration: s.Generation}
	where := ""
	if e != nil {
		c.Reason = e.Phase
		where = fmt.Sprintf("the HOME expiry is at %s; its grace ends at %s", e.Phase, e.DueAt.UTC().Format(time.RFC3339))
		if e.Archive != nil {
			where += fmt.Sprintf("; archived as %s (%s)", e.Archive.Entry, e.Archive.ContentSHA256)
		}
	}
	switch {
	case n != nil && n.unknown:
		c.Status, c.Reason = metav1.ConditionUnknown, "Unknown"
		where = strings.TrimPrefix(where+"; ", "; ") + "this step cannot be judged now: " + n.message
	case e != nil && e.Refusal != "":
		c.Reason = "Refused"
		where += fmt.Sprintf("; refused since %s: %s; the HOME stays", e.RefusalAt.UTC().Format(time.RFC3339), e.Refusal)
	case n != nil:
		c.Reason = n.reason
		where = strings.TrimPrefix(where+"; ", "; ") + n.message
	}
	c.Reason = conditionReason(c.Reason)
	c.Message = truncate(where)
	meta.SetStatusCondition(&st.Conditions, c)
}

// countExpiryRefused counts one refusal (dev_env_home_expiry_refused_total).
func (r *Reconciler) countExpiryRefused(reason string) {
	if r.HomeExpiryCounters != nil {
		r.HomeExpiryCounters.Refused.WithLabelValues(reason).Inc()
	}
}

// countExpiryOnce counts a refusal that no record holds (canary,
// override_invalid) at most once per 15 minutes per Session, the cadence of a
// recorded refusal, not on every event.
func (r *Reconciler) countExpiryOnce(s *v1alpha1.AgentSession, reason string) {
	key := string(s.UID) + "/" + reason
	now := r.clock()
	r.expiryMu.Lock()
	if r.expiryCounted == nil {
		r.expiryCounted = map[string]time.Time{}
	}
	last, seen := r.expiryCounted[key]
	if seen && now.Before(last.Add(homeExpiryRefusalRetry)) {
		r.expiryMu.Unlock()
		return
	}
	r.expiryCounted[key] = now
	r.expiryMu.Unlock()
	r.countExpiryRefused(reason)
}
