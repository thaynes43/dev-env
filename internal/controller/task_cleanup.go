package controller

// The task-session clean-up (ADR-020, DESIGN-061): a second policy on the HOME
// expiry pipeline of DESIGN-025, for spec.mode task only. It reuses the
// expiry record, the hold pod bound to the HOME UID, the Git rescue, the
// guarded delete and the release, and changes four things. The trigger is
// Reaping, or the confirmed terminal record, whatever operatingMode says, so a
// resume cancels nothing. The wait is none after a reap, and
// --task-cleanup-grace (168h) after any other end, both from the record's
// graceStart when recorded; the stored dueAt is obeyed until a reap. There is no history
// export: a valid rescue in the hold pod (agentd's --strict-home), with a
// Verified bundle re-hashed on the shelf, goes straight from Rescuing to
// Archived with the record's bundle. And once Expired, a finished Session is
// deleted (UID and resourceVersion preconditions), so it is released as a
// reaped one is. Before any hold mounts HOME and before the claim is deleted,
// all Nodes must show the volume released (DESIGN-061 1.1). At most --task-cleanup-max-active
// run at once, purged first, and the clean-up pauses itself (section 4).
// Default off (--enable-task-session-cleanup).

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	utilexec "k8s.io/client-go/util/exec"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
)

// The task clean-up's bounds and defaults (DESIGN-061 section 2, item 8).
const (
	DefaultTaskCleanupGrace = 168 * time.Hour
	MinTaskCleanupGrace     = time.Hour
	MaxTaskCleanupGrace     = 168 * time.Hour
	// DefaultTaskCleanupMaxActive leaves two of the four reconcile workers
	// free; MaxTaskCleanupMaxActive leaves one (reconciler.go's
	// MaxConcurrentReconciles).
	DefaultTaskCleanupMaxActive     = 2
	MaxTaskCleanupMaxActive         = 3
	DefaultTaskCleanupMinSharedFree = int64(2) << 30
)

const (
	// taskReleaseWait: how long a clean-up holds its slot waiting for the old
	// node to release the HOME's volume (DESIGN-061 1.1), looking every
	// taskReleasePoll; then it records volume_not_released and frees it.
	taskReleaseWait = 30 * time.Minute
	taskReleasePoll = 30 * time.Second
	// taskRescueRefusedRetry: a refused rescue is tried again once a day,
	// last in the queue, in case a newer agent image handles it (section 3).
	taskRescueRefusedRetry = 24 * time.Hour
	// taskQueuePoll: queued and paused records are judged from the cache
	// this often (section 4).
	taskQueuePoll = time.Minute
	// taskBreakerTrips and taskBreakerPause: three failed rescues in a row
	// pause new clean-ups for an hour; a valid rescue resets the count.
	taskBreakerTrips = 3
	taskBreakerPause = time.Hour
	// taskFreeCacheTTL: the shelf's free-space answer is reused this long.
	taskFreeCacheTTL = time.Minute
	// taskReservationTTL bounds an admission the cache has not shown yet.
	taskReservationTTL = 2 * time.Minute
	// taskQueueSnapshotTTL: one cache list of the queue serves every
	// reconcile for this long, so a backlog of hundreds does not list the
	// whole namespace once per Session per minute.
	taskQueueSnapshotTTL = 10 * time.Second
	// taskChildrenRetry: a finished parent whose descendants run is looked
	// at again this soon.
	taskChildrenRetry = 15 * time.Minute
	// taskAttemptDeadline bounds total attempt age, including failed reports,
	// replacement holds and termination. It records attempt_stuck, frees the
	// slot and keeps HOME; queue admission may retry an hour later.
	taskAttemptDeadline = 30 * time.Minute
	taskAttemptRetry    = time.Hour
	// taskHoldsEndedMax: an attempt whose hold pods ended this many times
	// without a rescue graded is stuck too.
	taskHoldsEndedMax = 3
	// taskMemoryKeep bounds how long the leader remembers a Session it no
	// longer hears from.
	taskMemoryKeep = 8 * 24 * time.Hour
)

// The task clean-up's own refusal reasons (DESIGN-061 sections 1.1 and 3).
// Every refusal keeps the HOME and frees the record's slot (taskActive); one
// at Rescuing also ends the attempt, so its hold pod goes and it comes back
// in through the queue.
const (
	expiryVolumeNotReleased = "volume_not_released"
	expiryRescueRefused     = "rescue_refused"
	expiryAttemptStuck      = "attempt_stuck"
	// expiryOwnerHold: the owner held a clean-up at Rescuing; its hold pod
	// went, and once the hold is lifted it comes back in through the queue.
	expiryOwnerHold      = "owner_hold"
	expiryBundleMissing  = protocol.RescueVerifyBundleMissing
	expiryBundleMismatch = protocol.RescueVerifyBundleMismatch
)

// The pause reasons of DESIGN-061 section 4.
const (
	taskPauseSharedVolumeLow  = "shared_volume_low"
	taskPauseShelfUnavailable = "shelf_unavailable"
	taskPauseRescueFailures   = "rescue_failures"
	taskPauseMaxActiveZero    = "max_active_zero"
)

var taskCleanupPauseReasons = []string{taskPauseSharedVolumeLow, taskPauseShelfUnavailable, taskPauseRescueFailures, taskPauseMaxActiveZero}

// The HomeExpiry condition's reasons for a task record (DESIGN-061 section 6);
// a task record never says Archived.
const (
	taskReasonWaiting  = "Waiting"
	taskReasonQueued   = "Queued"
	taskReasonPaused   = "Paused"
	taskReasonSaving   = "Saving"
	taskReasonSaved    = "Saved"
	taskReasonDeleting = "Deleting"
	taskReasonDeleted  = "Deleted"
	taskReasonRefused  = "Refused"
	taskReasonHeld     = "Held"
)

// RescueVerifier re-hashes a rescue's bundles in the shelf, and with an empty
// id reports the shared volume's free bytes (`agentd ctl rescue-verify`,
// DESIGN-061 3.1). shelf.RescueVerifier is the operator's.
type RescueVerifier interface {
	Verify(ctx context.Context, id string) (protocol.RescueVerifyResult, error)
}

// TaskCleanupRescuer runs the clean-up's two commands in its hold pod: the
// rescue with --strict-home and drop-log (DESIGN-061 3.1). ExecRescuer is the
// operator's.
type TaskCleanupRescuer interface {
	RescueStrictHome(ctx context.Context, pod *corev1.Pod) (protocol.RescueReport, error)
	DropLog(ctx context.Context, pod *corev1.Pod) (protocol.DropLogResult, error)
}

// taskCleanupState is the leader's memory of the clean-up: the admissions the
// cache does not show yet, the release waits of 1.1, the parents whose
// descendants run, the breaker, the shelf's last free-space answer and the
// queue's last snapshot. A new leader starts empty: it waits longer, never
// shorter, and its cache is synced before it reconciles.
type taskCleanupState struct {
	mu          sync.Mutex
	reserved    map[types.UID]time.Time
	admittedAt  map[types.UID]time.Time
	releaseWait map[types.UID]time.Time
	children    map[types.UID]time.Time
	// attempt: by hold pod, its first observation (older intents only)
	// (taskAttemptDeadline); ended: by Session, how many of this attempt's
	// hold pods ended with none graded; graded: the hold
	// pods whose rescue this operator ran with --strict-home and graded, by
	// stamp. A graded entry goes where that hold's attempt entry goes.
	attempt map[types.UID]time.Time
	graded  map[types.UID]string
	ended   map[types.UID]int
	// removalNoted: by Session, the reason already said for an Expired record
	// that cannot store a refusal. The warning is once per reason.
	removalNoted map[types.UID]string
	failures     int
	lastFailure  time.Time
	freeAt       time.Time
	free         protocol.RescueVerifyResult
	freeErr      error
	snap         *taskQueueSnapshot
}

func (st *taskCleanupState) init() {
	if st.reserved == nil {
		st.reserved, st.admittedAt = map[types.UID]time.Time{}, map[types.UID]time.Time{}
		st.releaseWait, st.children = map[types.UID]time.Time{}, map[types.UID]time.Time{}
		st.attempt, st.graded = map[types.UID]time.Time{}, map[types.UID]string{}
		st.ended = map[types.UID]int{}
	}
}

// prune forgets what the leader remembers of Sessions it has not heard from
// for taskMemoryKeep. The caller holds the lock.
func (st *taskCleanupState) prune(now time.Time) {
	for uid, at := range st.admittedAt {
		if now.Sub(at) > taskMemoryKeep {
			delete(st.admittedAt, uid)
		}
	}
	for uid, at := range st.releaseWait {
		if now.Sub(at) > taskMemoryKeep {
			delete(st.releaseWait, uid)
		}
	}
	for uid, at := range st.attempt {
		if now.Sub(at) > taskMemoryKeep {
			delete(st.attempt, uid)
			delete(st.graded, uid)
		}
	}
	for uid, until := range st.children {
		if now.After(until) {
			delete(st.children, uid)
		}
	}
}

func (r *Reconciler) taskCleanupOn() bool {
	return r.TaskSessionCleanup && r.PrivateTerminalRecord && r.PrivateExecutorStop
}

func (r *Reconciler) taskCleanupGrace() time.Duration {
	if r.TaskCleanupGrace <= 0 {
		return DefaultTaskCleanupGrace
	}
	return min(max(r.TaskCleanupGrace, MinTaskCleanupGrace), MaxTaskCleanupGrace)
}

// taskCleanupScope is DESIGN-061 section 2's static scope, the terminal task
// reclaim's: spec.mode task, no workspace, canary, pilot or original-HOME
// binding, with a private retention record. Every other Session keeps
// DESIGN-025.
func taskCleanupScope(s *v1alpha1.AgentSession) bool {
	return terminalTaskReclaimScope(s) && s.Status.PrivateHistory != nil
}

// taskPolicy reports whether the task clean-up, not DESIGN-025, owns the
// Session's HOME expiry. A record holding a bundle is always the clean-up's,
// whatever the flag (with it off, its hold pod goes and the record stays); one
// holding an archive, or that exported history, keeps DESIGN-025's delete. Any
// other in-scope Session is the clean-up's while the flag is on.
func (r *Reconciler) taskPolicy(s *v1alpha1.AgentSession) bool {
	return taskPolicyOf(s, r.taskCleanupOn())
}

// taskPolicyOf is taskPolicy for a pure caller, with the flag as on.
func taskPolicyOf(s *v1alpha1.AgentSession, on bool) bool {
	if !taskCleanupScope(s) {
		return false
	}
	switch e := s.Status.PrivateHistory.Expiry; {
	case e != nil && e.Bundle != nil:
		return true
	case e != nil && (e.Archive != nil || e.Phase == v1alpha1.HomeExpiryExporting):
		return false
	}
	return on
}

// taskCleanupOwns reports whether the clean-up has a record for the Session:
// from then on it is never given a Pod again and its rescue is never
// superseded, whatever operatingMode says.
func (r *Reconciler) taskCleanupOwns(s *v1alpha1.AgentSession) bool {
	return r.taskPolicy(s) && s.Status.PrivateHistory.Expiry != nil
}

// taskCleanupTriggered is the task trigger: Reaping, or the terminal record of
// the recorded executor, whatever operatingMode says (DESIGN-061 1). A resume
// only patches operatingMode, and a private task never gets a second
// executor, so neither moves it.
func taskCleanupTriggered(s *v1alpha1.AgentSession) expiryTrigger {
	h := s.Status.PrivateHistory
	switch {
	case h != nil && h.Expiry != nil && expiryPhaseAtLeast(h.Expiry.Phase, v1alpha1.HomeExpiryDeleting):
		return expiryTriggered
	case !s.DeletionTimestamp.IsZero(), executorTerminalRecorded(h):
		return expiryTriggered
	}
	return expiryNotTriggered
}

// expiryTrigger is the trigger of the Session's policy.
func (r *Reconciler) expiryTrigger(s *v1alpha1.AgentSession, obs *observation, now time.Time) (expiryTrigger, time.Duration) {
	if r.taskPolicy(s) {
		return taskCleanupTriggered(s), 0
	}
	return homeExpiryTriggered(s, obs.templates, now)
}

// taskCleanupPurged: a reap is the cause, so there is no wait.
func taskCleanupPurged(s *v1alpha1.AgentSession) bool { return !s.DeletionTimestamp.IsZero() }

// taskCleanupDueAt obeys the stored deadline for a non-reaped task. A changed
// grace applies only to new records; a reap still makes this one due at once.
func (r *Reconciler) taskCleanupDueAt(s *v1alpha1.AgentSession) time.Time {
	e := s.Status.PrivateHistory.Expiry
	if taskCleanupPurged(s) {
		return e.GraceStart.Time
	}
	return e.DueAt.Time
}

// taskCleanupAllowed is --task-cleanup-sessions: names allowed at every step,
// including both deletes, empty for all.
func (r *Reconciler) taskCleanupAllowed(name string) bool {
	return len(r.TaskCleanupSessions) == 0 || slices.Contains(r.TaskCleanupSessions, name)
}

// taskRetryRefusal reports whether an inactive record needs queue admission
// before work resumes. Every refusal in an active phase frees its slot.
func taskRetryRefusal(e *v1alpha1.PrivateHomeExpiryStatus) bool {
	if e == nil || e.Refusal == "" {
		return false
	}
	switch e.Phase {
	case v1alpha1.HomeExpiryRescuing, v1alpha1.HomeExpiryArchived, v1alpha1.HomeExpiryDeleting:
		return true
	}
	return false
}

// taskCleanupParked checks the stop switch and allowlist at a step boundary.
// Callers repeat it after slow verification and before either delete.
func (r *Reconciler) taskCleanupParked(s *v1alpha1.AgentSession, obs *observation) bool {
	if !r.taskCleanupOn() {
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonPaused, message: "the task clean-up is stopped (--enable-task-session-cleanup=false)"}
		return true
	}
	if !r.taskCleanupAllowed(s.Name) {
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonPaused, message: "the task clean-up is parked: this session is not on --task-cleanup-sessions"}
		return true
	}
	return false
}

// taskRefusalRetry is how long a recorded refusal waits for its re-check: a
// refused rescue a day, a stuck attempt an hour, anything else 15 minutes.
func taskRefusalRetry(reason string) time.Duration {
	switch reason {
	case expiryRescueRefused:
		return taskRescueRefusedRetry
	case expiryAttemptStuck:
		return taskAttemptRetry
	}
	return homeExpiryRefusalRetry
}

// taskAttemptRefusalRetry is taskRefusalRetry for a refusal that ended an
// attempt at Rescuing: one of the shelf or its bundle waits an hour too, so a
// broken shared volume does not rerun every rescue every 15 minutes.
func taskAttemptRefusalRetry(e *v1alpha1.PrivateHomeExpiryStatus) time.Duration {
	if taskRetryRefusal(e) && e.Phase == v1alpha1.HomeExpiryRescuing {
		switch e.Refusal {
		case expiryShelfUnavailable, expiryBundleMissing, expiryBundleMismatch:
			return taskAttemptRetry
		}
	}
	return taskRefusalRetry(e.Refusal)
}

// taskActive reports whether a record holds a slot: Rescuing, Archived or
// Deleting and not refused. An owner hold frees its slot only once the
// refusal is durable; removing an unobserved hold cannot over-admit work.
// An inactive record must pass through the queue again before resuming.
func taskActive(s *v1alpha1.AgentSession) bool {
	h := s.Status.PrivateHistory
	if h == nil || h.Expiry == nil || h.Expiry.Refusal != "" {
		return false
	}
	switch h.Expiry.Phase {
	case v1alpha1.HomeExpiryRescuing, v1alpha1.HomeExpiryArchived, v1alpha1.HomeExpiryDeleting:
		return true
	}
	return false
}

// reconcileTaskCleanup runs the task policy for a protected private Session
// in its scope. stop means the reconcile ends here with res.
func (r *Reconciler) reconcileTaskCleanup(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (ctrl.Result, bool, error) {
	if !r.taskCleanupOn() {
		// A record holding a bundle with the clean-up off: no new hold pod,
		// the record stays where it is, and its hold pod goes (DESIGN-061 8).
		return r.homeExpiryOff(ctx, s, obs)
	}
	h := s.Status.PrivateHistory
	if h.Expiry == nil && !executorTerminalRecorded(h) {
		return ctrl.Result{}, false, nil
	}
	mounts, mountsErr := r.cachedHomeMounts(ctx, s)
	if h.Expiry == nil && len(mounts) > 0 {
		obs.privateHistoryUnknown = fmt.Errorf("private executor was replaced: Pod %s mounts the private HOME %s; the task clean-up does not start while it is there", mounts[0], h.HomeClaim)
		return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
	}
	if h.Expiry != nil {
		if res, quiet := r.taskCleanupQuiet(ctx, s, obs, len(mounts) > 0 || mountsErr != nil); quiet {
			return res, false, nil
		}
	}
	v, err := r.readExpiryView(ctx, s)
	if errors.Is(err, errExpiryBehind) {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	if err != nil {
		return r.expiryUnknown(ctx, s, obs, err)
	}
	if homeExpiryOverride(v.s) == expiryOverrideInvalid {
		r.countExpiryOnce(v.s, expiryOverrideInvalid, homeExpiryRefusalRetry)
	}
	if v.record() == nil {
		return r.startHomeExpiry(ctx, v, obs, taskCleanupTriggered(v.s), 0)
	}
	return r.stepTaskCleanup(ctx, v, obs)
}

// taskCleanupQuiet reports whether the cache shows a task record at Waiting
// where no step can act, so the reconcile makes none of the uncached reads:
// before its due time, held, or due but not admitted (not allowed by the
// allowlist, its descendants running, paused, or behind others in the queue).
// The rest are as expiryQuiet's: no refusal, eligible, the HOME claim bound,
// and no Pod but the ended executor of the name or mounting the claim.
func (r *Reconciler) taskCleanupQuiet(ctx context.Context, s *v1alpha1.AgentSession, obs *observation, otherMounts bool) (ctrl.Result, bool) {
	e := s.Status.PrivateHistory.Expiry
	now := r.clock()
	override := homeExpiryOverride(s)
	switch {
	case e.Phase != v1alpha1.HomeExpiryWaiting || e.Refusal != "" || otherMounts || homeExpiryEligible(s) != nil:
		return ctrl.Result{}, false
	case override != "" && override != v1alpha1.HomeExpiryHold:
		return ctrl.Result{}, false
	case obs.claim.missing || obs.claim.foreign != "" || homeClaimBound(s, obs.claim.obj) != nil:
		return ctrl.Result{}, false
	}
	switch {
	case obs.pod.missing:
	case obs.pod.foreign == "" && string(obs.pod.obj.UID) == e.ExecutorUID && podPhaseTerminal(obs.pod.obj):
	default:
		return ctrl.Result{}, false
	}
	if override == v1alpha1.HomeExpiryHold {
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonHeld, message: "the owner holds the task clean-up; nothing moves on until the hold is removed"}
		return ctrl.Result{RequeueAfter: homeExpiryMaxWait}, true
	}
	due := r.taskCleanupDueAt(s)
	if now.Before(due) {
		return ctrl.Result{RequeueAfter: min(due.Sub(now)+time.Second, homeExpiryMaxWait)}, true
	}
	if note, wait, quiet := r.taskCleanupNotAdmitted(ctx, s, now, true); quiet {
		obs.homeExpiry = note
		return ctrl.Result{RequeueAfter: wait}, true
	}
	return ctrl.Result{}, false
}

// taskCleanupNotAdmitted is what keeps a due record from starting, judged from
// the cache and the leader's memory: the allowlist, a finished parent's
// running descendants (as last found), a pause, or the queue. quiet is false
// when nothing keeps it, or when cacheOnly and the shelf's free space must be
// read again first.
func (r *Reconciler) taskCleanupNotAdmitted(ctx context.Context, s *v1alpha1.AgentSession, now time.Time, cacheOnly bool) (note *homeExpiryNote, wait time.Duration, quiet bool) {
	if !r.taskCleanupAllowed(s.Name) {
		return &homeExpiryNote{reason: taskReasonWaiting, message: "the task clean-up is due but waits: this session is not on --task-cleanup-sessions"}, homeExpiryMaxWait, true
	}
	if !taskCleanupPurged(s) && r.taskChildrenBlocked(s.UID, now) {
		return taskChildrenNote(), taskChildrenRetry, true
	}
	pause, known := r.taskCleanupPause(ctx, now, cacheOnly)
	if !known {
		return nil, 0, false
	}
	if pause != "" {
		return taskPauseNote(pause, r.TaskCleanupMinSharedFree), taskQueuePoll, true
	}
	if !r.taskCleanupAdmittable(ctx, s, now, false) {
		return taskQueuedNote(r.TaskCleanupMaxActive), taskQueuePoll, true
	}
	return nil, 0, false
}

func taskChildrenNote() *homeExpiryNote {
	return &homeExpiryNote{reason: taskReasonWaiting, message: "the task clean-up is due but waits while this task's descendants still run"}
}

func taskQueuedNote(maxActive int) *homeExpiryNote {
	return &homeExpiryNote{reason: taskReasonQueued, message: fmt.Sprintf("the clean-up is queued: at most %d run at once, purged sessions first, then the oldest", maxActive)}
}

func taskPauseNote(reason string, minFree int64) *homeExpiryNote {
	var why string
	switch reason {
	case taskPauseSharedVolumeLow:
		why = "the shared volume has less than " + formatBytes(minFree) + " free"
	case taskPauseShelfUnavailable:
		why = "the shelf does not answer"
	case taskPauseRescueFailures:
		why = fmt.Sprintf("%d rescues in a row failed, so new clean-ups wait an hour", taskBreakerTrips)
	case taskPauseMaxActiveZero:
		why = "--task-cleanup-max-active is 0"
	}
	return &homeExpiryNote{reason: taskReasonPaused, message: "the clean-up is paused: " + why}
}

// stepTaskCleanup runs the task record's next step, in DESIGN-025 5.4's order:
// adoption, a new Pod's cancel, the owner hold, a refusal's re-check time, the
// common re-check, then the phase's step. The task trigger ignores
// operatingMode, so nothing but a new Pod cancels.
func (r *Reconciler) stepTaskCleanup(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if r.taskCleanupParked(v.s, obs) {
		return ctrl.Result{RequeueAfter: homeExpiryMaxWait}, false, nil
	}
	e := v.record()
	if expiryPhaseAtLeast(e.Phase, v1alpha1.HomeExpiryDeleting) {
		return r.stepExpiryDelete(ctx, v, obs)
	}
	if v.pod != nil && v.awaitingAdoption(v.pod) {
		return r.adoptExpiryHold(ctx, v)
	}
	if p := v.newPod(); p != nil {
		return r.cancelHomeExpiry(ctx, v, obs, fmt.Sprintf("a new Pod %s (uid %s) holds the HOME", p.Name, p.UID), true)
	}
	if taskCleanupTriggered(v.s) != expiryTriggered {
		return r.cancelHomeExpiry(ctx, v, obs, "the Session has no terminal record and is not reaped", false)
	}
	if homeExpiryHeld(v.s) {
		if e.Phase != v1alpha1.HomeExpiryWaiting && e.Refusal == "" {
			// Persist inactivity before removing its hold, so lifting the
			// owner's override cannot resume outside queue admission.
			return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryOwnerHold, "the owner holds the clean-up"))
		}
		if p := v.ownHold(); p != nil && p.DeletionTimestamp.IsZero() {
			return r.removeExpiryHold(ctx, v, obs, p, holdReleaseOwnerHold)
		}
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonHeld, message: heldNote(v.s).message}
		if homeExpiryOverride(v.s) == expiryOverrideInvalid {
			return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
		}
		return ctrl.Result{}, false, nil
	}
	if p := v.ownHold(); taskRetryRefusal(e) && p != nil && p.DeletionTimestamp.IsZero() {
		// The refusal ended the attempt: its hold pod goes, and the HOME
		// stays.
		return r.removeExpiryHold(ctx, v, obs, p, holdReleaseRefused)
	}
	if retry := taskAttemptRefusalRetry(e); e.Refusal != "" && e.RefusalAt != nil && v.now.Before(e.RefusalAt.Add(retry)) && !v.now.Before(e.RefusalAt.Time) {
		return ctrl.Result{RequeueAfter: min(e.RefusalAt.Add(retry).Sub(v.now)+time.Second, homeExpiryMaxWait)}, false, nil
	}
	if ref := expiryRecheck(v); ref != nil {
		return r.refuseHomeExpiry(ctx, v, obs, ref)
	}
	if taskRetryRefusal(e) {
		return r.taskCleanupRetry(ctx, v, obs)
	}
	var (
		res  ctrl.Result
		stop bool
		err  error
	)
	switch e.Phase {
	case v1alpha1.HomeExpiryWaiting:
		res, stop, err = r.taskCleanupWait(ctx, v, obs)
	case v1alpha1.HomeExpiryRescuing:
		res, stop, err = r.taskCleanupRescue(ctx, v, obs)
	case v1alpha1.HomeExpiryArchived:
		res, stop, err = r.taskCleanupArchived(ctx, v, obs)
	default:
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryIdentityChanged, "a task clean-up record cannot be at %s", e.Phase))
	}
	if err != nil || stop || e.Refusal == "" || obs.homeExpiry != nil && obs.homeExpiry.pending {
		return res, stop, err
	}
	// The re-check and the step passed with nothing to write: the refusal's
	// cause is gone (DESIGN-025 5.2).
	fresh := v.s.DeepCopy()
	clearExpiryRefusal(fresh.Status.PrivateHistory.Expiry)
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("the task clean-up's refusal is resolved", "refusal", e.Refusal)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// taskCleanupWait is the Waiting step: on to Rescuing once the record is due
// (purged: at once; otherwise its stored dueAt; or the owner's
// release-now), allowed, its descendants stopped (finished only), nothing
// pauses the clean-up and the queue admits it.
func (r *Reconciler) taskCleanupWait(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	due := r.taskCleanupDueAt(v.s)
	release := homeExpiryOverride(v.s) == v1alpha1.HomeExpiryReleaseNow
	if !release && v.now.Before(due) {
		return ctrl.Result{RequeueAfter: min(due.Sub(v.now)+time.Second, homeExpiryMaxWait)}, false, nil
	}
	// The allowlist, a pause and the queue first, from the cache and memory:
	// the descendants' check below lists every Session uncached.
	if note, wait, quiet := r.taskCleanupNotAdmitted(ctx, v.s, v.now, false); quiet {
		obs.homeExpiry = note
		return ctrl.Result{RequeueAfter: wait}, false, nil
	}
	if !taskCleanupPurged(v.s) {
		// A finished task waits while its descendants run; a purged one does
		// not (DESIGN-061 2).
		if err := terminalTaskChildrenStopped(ctx, r.APIReader, v.s); err != nil {
			r.taskBlockChildren(v.s.UID, v.now)
			obs.homeExpiry = taskChildrenNote()
			log.FromContext(ctx).Info("the task clean-up waits for descendants", "reason", err.Error())
			return ctrl.Result{RequeueAfter: taskChildrenRetry}, false, nil
		}
		r.taskUnblockChildren(v.s.UID)
	}
	if !r.taskCleanupAdmittable(ctx, v.s, v.now, true) {
		obs.homeExpiry = taskQueuedNote(r.TaskCleanupMaxActive)
		return ctrl.Result{RequeueAfter: taskQueuePoll}, false, nil
	}
	fresh := v.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	next.Phase = v1alpha1.HomeExpiryRescuing
	if release && next.ReleaseNowAt == nil {
		at := recordTime(v.now)
		next.ReleaseNowAt = &at
	}
	clearStepRefusal(next)
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		r.taskUnreserve(v.s.UID)
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	r.taskAdmitted(v.s.UID, v.now)
	cause := "finished"
	if taskCleanupPurged(v.s) {
		cause = "purged"
	}
	r.event(v.s, corev1.EventTypeNormal, "TaskCleanup", "TaskCleanupStarted",
		fmt.Sprintf("the task clean-up starts (%s, due %s): it saves any work that never reached GitHub, then deletes the HOME and the session (DESIGN-061)", cause, due.UTC().Format(time.RFC3339)))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// taskCleanupRetry re-admits an inactive record in any active phase once
// its refusal is due and the common re-check passed: volume_not_released looks at the
// volume once more (no slot), then the record goes back into the queue, a
// refused or stuck attempt last. The write that re-admits it clears the
// refusal. Only Rescuing gets a new hold intent; later phases keep their
// verified bundle and resume their next step after admission.
func (r *Reconciler) taskCleanupRetry(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	e := v.record()
	endedExecutor := e.Phase == v1alpha1.HomeExpiryRescuing && v.pod != nil &&
		string(v.pod.UID) == e.ExecutorUID && podPhaseTerminal(v.pod) && v.newPod() == nil
	if (v.pod != nil || len(v.mounts) > 0) && !endedExecutor {
		// A Pod still holds the name or the claim: the clean-up's own hold
		// pod terminating is looked at again soon; one that stays (an ended
		// executor its guard keeps, a hold pod its guard keeps) every 15
		// minutes.
		if v.pod != nil && !v.pod.DeletionTimestamp.IsZero() {
			return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
		}
		return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
	}
	if e.Phase == v1alpha1.HomeExpiryRescuing && e.Refusal == expiryVolumeNotReleased {
		if why := r.taskVolumeReleased(ctx, v); why != "" {
			return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryVolumeNotReleased, "%s", why))
		}
	}
	if note, wait, quiet := r.taskCleanupNotAdmitted(ctx, v.s, v.now, false); quiet {
		obs.homeExpiry = &homeExpiryNote{reason: note.reason, message: note.message, pending: true}
		return ctrl.Result{RequeueAfter: wait}, false, nil
	}
	if !r.taskCleanupAdmittable(ctx, v.s, v.now, true) {
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonQueued, message: taskQueuedNote(r.TaskCleanupMaxActive).message, pending: true}
		return ctrl.Result{RequeueAfter: taskQueuePoll}, false, nil
	}
	fresh := v.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	if e.Phase == v1alpha1.HomeExpiryRescuing {
		next.HoldPodUID, next.HoldIntent = "", taskHoldIntent(v.now, "")
	}
	clearExpiryRefusal(next)
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		r.taskUnreserve(v.s.UID)
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	r.taskAdmitted(v.s.UID, v.now)
	log.FromContext(ctx).Info("the task clean-up tries again after its refusal", "refusal", e.Refusal)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// taskHoldIntent keeps the attempt's start across replacement holds, with a
// fresh unique suffix for each create/adopt intent. The 53-byte value fits the
// existing opaque 64-byte field; retry admission passes an empty prior intent.
const taskAttemptTimeFormat = "20060102T150405Z"

func taskHoldIntent(now time.Time, prior string) string {
	if start, ok := taskIntentStarted(prior); ok {
		now = start
	}
	return now.UTC().Format(taskAttemptTimeFormat) + "/" + string(uuid.NewUUID())
}

func taskIntentStarted(intent string) (time.Time, bool) {
	stamp, _, ok := strings.Cut(intent, "/")
	at, err := time.Parse(taskAttemptTimeFormat, stamp)
	return at, ok && err == nil
}

// taskCleanupRescue is the Rescuing step: the ended executor goes, the old
// node's release of the HOME's volume is shown (1.1), a hold pod bound to the
// HOME UID takes the name, the rescue runs in it with --strict-home, and a
// valid one is kept: a Verified bundle re-hashed on the shelf, or every clone
// clean and pushed. A refused rescue removes the hold pod and records
// rescue_refused; a transient failure keeps it and runs again after HoldRetry.
func (r *Reconciler) taskCleanupRescue(ctx context.Context, v *expiryView, obs *observation) (res ctrl.Result, stop bool, err error) {
	e := v.record()
	pod := v.pod
	start, timed := taskIntentStarted(e.HoldIntent)
	if hold := v.ownHold(); hold != nil {
		start, timed = r.taskAttemptStarted(v, hold), true
	}
	if timed {
		deadline := start.Add(taskAttemptDeadline)
		if !v.now.Before(deadline) {
			if pod == nil {
				if why := r.taskVolumeReleased(ctx, v); why != "" {
					return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryVolumeNotReleased, "%s at the attempt's %s bound", why, taskAttemptDeadline))
				}
			}
			return r.taskRefuseStuckAttempt(ctx, v, obs)
		}
		// A failed report or an unreleased volume must not schedule the
		// next check later than the durable attempt deadline.
		defer func() {
			if taskActive(v.s) && res.RequeueAfter > 0 {
				res.RequeueAfter = min(res.RequeueAfter, max(time.Nanosecond, deadline.Sub(r.clock())))
			}
		}()
	}
	switch {
	case pod != nil && string(pod.UID) == e.ExecutorUID:
		if !pod.DeletionTimestamp.IsZero() {
			return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
		}
		return r.removeExpiredExecutor(ctx, v, obs, pod)
	case pod != nil && v.ownHold() == pod:
		// The hold pod the record names.
	case pod != nil, len(v.mounts) > 0:
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryClaimInUse, "a Pod other than the executor and the clean-up's hold pod holds the HOME claim"))
	case taskRescueRefusedIn(v.s, e.HoldPodUID) && e.Refusal != expiryRescueRefused:
		// The last hold pod's rescue refused and that pod is gone: the HOME
		// is kept, the slot freed, and it is tried again in a day.
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryRescueRefused, "%s", v.s.Status.Rescue.Message))
	case e.HoldPodUID != "" || e.HoldIntent == "":
		fresh := v.s.DeepCopy()
		next := fresh.Status.PrivateHistory.Expiry
		next.HoldPodUID, next.HoldIntent = "", taskHoldIntent(v.now, e.HoldIntent)
		clearStepRefusal(next)
		if err := r.writeExpiry(ctx, v, fresh); err != nil {
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	default:
		// DESIGN-061 1.1: nothing mounts HOME until every Node is shown
		// live and no longer using its volume.
		if why := r.taskVolumeReleased(ctx, v); why != "" {
			first := r.taskReleaseWaitSince(v.s.UID, v.now)
			if v.now.Sub(first) < taskReleaseWait {
				obs.homeExpiry = &homeExpiryNote{reason: taskReasonSaving, message: "the clean-up waits for node " + v.s.Status.PrivateHistory.ExecutorTerminal.NodeName + " to release the HOME's volume"}
				log.FromContext(ctx).Info("the task clean-up waits for the HOME's volume to be released", "reason", why)
				return ctrl.Result{RequeueAfter: taskReleasePoll}, false, nil
			}
			r.taskForgetReleaseWait(v.s.UID)
			return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryVolumeNotReleased, "%s for %s", why, taskReleaseWait))
		}
		r.taskForgetReleaseWait(v.s.UID)
		return r.createExpiryHold(ctx, v, obs)
	}

	hold := pod
	if !expiryHoldAnnotated(hold, e) || !isHoldPod(hold) || !controlledBySession(hold, v.s) || !privateExecutorUsesHome(hold, v.s.Status.PrivateHistory.HomeClaim) {
		return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryIdentityChanged, "the hold pod %s is not bound to the HOME and the record", hold.Name))
	}
	if !hold.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}
	rec := v.s.Status.Rescue
	ours := rec != nil && rescueRanIn(v.s, hold) && r.taskGraded(hold.UID, rec.Stamp)
	if podEnded(hold) {
		if r.taskHoldEnded(v.s.UID, hold.UID) >= taskHoldsEndedMax {
			r.taskEndAttempt(v.s.UID, hold.UID)
			r.taskBreaker(true, v.now)
			return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryAttemptStuck, "%d hold pods ended without a rescue; the last is %s", taskHoldsEndedMax, hold.Name))
		}
		return r.removeExpiryHold(ctx, v, obs, hold, holdReleaseEnded)
	}
	if hold.Status.Phase != corev1.PodRunning {
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonSaving, message: "the hold pod " + hold.Name + " is starting"}
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}
	if !ours {
		// No rescue in this hold pod yet, or one this operator did not run
		// and grade with --strict-home (an earlier operator, or DESIGN-025):
		// it runs, so only the strict rescue's verdict is ever kept.
		return r.taskRescue(ctx, v, obs, hold)
	}
	if !rescued(v.s) {
		if rec.Result == v1alpha1.RescueFailed {
			if taskRescueRefusedIn(v.s, string(hold.UID)) {
				// A refusal: the hold pod goes first, then the refusal is
				// recorded, and the HOME stays (DESIGN-061 3).
				return r.removeExpiryHold(ctx, v, obs, hold, holdReleaseRescueRefused)
			}
			if !holdRetryDue(v.s, v.now, r.holdRetry()) {
				next := v.now.Add(r.holdRetry())
				if rec.At != nil {
					next = rec.At.Add(r.holdRetry())
				}
				obs.homeExpiry = &homeExpiryNote{reason: taskReasonSaving, message: "the rescue in the hold pod failed and runs again at " + next.UTC().Format(time.RFC3339) + ": " + rec.Message}
				return ctrl.Result{RequeueAfter: max(time.Second, next.Sub(v.now))}, false, nil
			}
		}
		return r.taskRescue(ctx, v, obs, hold)
	}
	return r.saveTaskBundle(ctx, v, hold, obs)
}

func (r *Reconciler) taskRefuseStuckAttempt(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	r.taskEndAttempt(v.s.UID, types.UID(v.record().HoldPodUID))
	v.now = r.clock()
	r.taskBreaker(true, v.now)
	return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryAttemptStuck, "the clean-up attempt reached %s; the HOME stays", taskAttemptDeadline))
}

// taskAttemptStarted reads the durable attempt clock. For older intents it
// uses the hold's creation time, or its first observation as a last resort.
func (r *Reconciler) taskAttemptStarted(v *expiryView, hold *corev1.Pod) time.Time {
	since := r.taskAttemptSince(hold.UID, v.now)
	if start, ok := taskIntentStarted(v.record().HoldIntent); ok {
		return start
	}
	if !hold.CreationTimestamp.IsZero() && hold.CreationTimestamp.Time.Before(since) {
		return hold.CreationTimestamp.Time
	}
	return since
}

// taskAttemptSince is the first observation of an older hold whose intent
// has no durable timestamp. Failed reports never reset it.
func (r *Reconciler) taskAttemptSince(hold types.UID, now time.Time) time.Time {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	at, ok := st.attempt[hold]
	if !ok || now.Before(at) {
		st.attempt[hold] = now
		return now
	}
	return at
}

// taskHoldEnded counts one more of the attempt's hold pods ended with no
// rescue graded in it, once per hold pod, and returns the count.
func (r *Reconciler) taskHoldEnded(uid, hold types.UID) int {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	if _, counted := st.attempt[hold]; counted {
		delete(st.attempt, hold)
		if _, graded := st.graded[hold]; !graded {
			st.ended[uid]++
		}
		delete(st.graded, hold)
	}
	return st.ended[uid]
}

// taskEndAttempt forgets an attempt's clocks: at its end, or when a new
// admission starts the next.
func (r *Reconciler) taskEndAttempt(uid, hold types.UID) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	delete(st.ended, uid)
	if hold != "" {
		delete(st.attempt, hold)
		delete(st.graded, hold)
	}
}

// taskGraded reports whether this operator ran the clean-up's rescue in the
// hold pod and graded it, for that stamp.
func (r *Reconciler) taskGraded(hold types.UID, stamp string) bool {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	return stamp != "" && st.graded[hold] == stamp
}

// taskMarkGraded remembers the strict grading, without restarting the attempt
// clock or forgetting ended holds after a failed report.
func (r *Reconciler) taskMarkGraded(hold types.UID, stamp string) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	st.graded[hold] = stamp
}

// taskRescueRefusedIn reports whether the Session's newest rescue ran in the
// hold pod holdUID and refused: a failure no retry clears (DESIGN-061 3).
func taskRescueRefusedIn(s *v1alpha1.AgentSession, holdUID string) bool {
	rec := s.Status.Rescue
	return holdUID != "" && rec != nil && rec.PodUID == holdUID && !rec.Superseded && rec.Result == v1alpha1.RescueFailed &&
		!transientTaskRescueReason(rescueReason(&s.Status))
}

// transientTaskRescueReason: the clean-up's rescue failures that keep the hold
// pod and run again (a fetch or a bundle write, section 3); the breaker counts
// them. Every other failure is a refusal: the hold pod goes, the HOME stays,
// and a new hold pod tries again in a day.
func transientTaskRescueReason(reason string) bool {
	return reason == "FetchFailed" || reason == "BundleFailed"
}

// taskBreakerReason: the failures the breaker counts, the transient ones and
// an agent image without the strict check, which fails every clean-up alike.
func taskBreakerReason(reason string) bool {
	return transientTaskRescueReason(reason) || reason == taskRescueUnchecked
}

// A bundle's result, as the record's string field holds it.
const (
	bundleVerified       = string(v1alpha1.RescueVerified)
	bundleCleanAndPushed = string(v1alpha1.RescueCleanAndPushed)
)

// The verdict reasons only the clean-up's grading gives.
const (
	taskRescueStrayRefused = "StrictHomeRefused"
	taskRescueUnchecked    = "StrictHomeUnchecked"
)

// gradeTaskRescue is verdict for the clean-up's rescue (DESIGN-061 3.1, "What
// part 2 relies on"): a non-empty strictHome.refused is a refusal, graded
// before verdict's rules, which would also flag its empty repos; a report
// without strictHome came from an agentd without the check and fails.
func gradeTaskRescue(s *v1alpha1.AgentSession, pod *corev1.Pod, rep protocol.RescueReport, now metav1.Time) (*v1alpha1.RescueStatus, string) {
	if sh := rep.StrictHome; sh != nil && sh.Refused != "" {
		rec := &v1alpha1.RescueStatus{Stamp: rep.Stamp, At: &now, PodUID: string(pod.UID), Generation: s.Generation, Result: v1alpha1.RescueFailed}
		if s.Status.Rescue != nil {
			rec.LastBundle = s.Status.Rescue.LastBundle
		}
		rec.Message = truncate("the rescue " + rep.Stamp + " refused: " + sh.Refused + "; nothing was committed or bundled")
		if rep.Session != s.Name {
			rec.Message = truncate(rec.Message + fmt.Sprintf("; the report is for session %q", rep.Session))
		}
		return rec, taskRescueStrayRefused
	}
	rec, reason := verdict(s, pod, rep, now)
	if rep.StrictHome == nil {
		rec.Result, rec.SharedProof = v1alpha1.RescueFailed, nil
		rec.Message = truncate("the rescue " + rep.Stamp + " ran without the strict HOME check (an agent image without --strict-home), so it proves nothing for the clean-up")
		return rec, taskRescueUnchecked
	}
	return rec, reason
}

// taskRescue runs agentd's rescue with --strict-home in the hold pod, grades
// it and records it in status.rescue while the record still names that hold
// pod. The breaker counts a transient failure and a valid rescue resets it.
func (r *Reconciler) taskRescue(ctx context.Context, v *expiryView, obs *observation, hold *corev1.Pod) (ctrl.Result, bool, error) {
	tr, ok := r.Rescuer.(TaskCleanupRescuer)
	if !ok || r.Rescuer == nil {
		return r.expiryUnknown(ctx, v.s, obs, errors.New("this operator's rescuer cannot run the clean-up's rescue (--strict-home)"))
	}
	log.FromContext(ctx).Info("running the task clean-up's rescue", "pod", hold.Name, "uid", hold.UID)
	deadline := r.taskAttemptStarted(v, hold).Add(taskAttemptDeadline)
	remaining := deadline.Sub(r.clock())
	if remaining <= 0 {
		return r.taskRefuseStuckAttempt(ctx, v, obs)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, min(remaining, RescueTimeout))
	defer cancel()
	rep, err := tr.RescueStrictHome(attemptCtx, hold)
	if !r.clock().Before(deadline) || remaining <= RescueTimeout && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		return r.taskRefuseStuckAttempt(ctx, v, obs)
	}
	var exit utilexec.ExitError
	unchecked := err != nil && errors.As(err, &exit) && exit.ExitStatus() == 2
	if err != nil && !unchecked {
		return r.expiryUnknown(ctx, v.s, obs, fmt.Errorf("the rescue could not run in the hold pod %s: %w", hold.Name, err))
	}
	// exec reaches a pod by name: the report counts only from this one.
	var cur corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(hold), &cur); err != nil {
		return r.expiryUnknown(ctx, v.s, obs, fmt.Errorf("read the hold pod %s after the rescue: %w", hold.Name, err))
	} else if cur.UID != hold.UID {
		return r.expiryUnknown(ctx, v.s, obs, fmt.Errorf("the hold pod %s was replaced during the rescue (uid %s, now %s)", hold.Name, hold.UID, cur.UID))
	}
	rec, reason := gradeTaskRescue(v.s, hold, rep, recordTime(r.clock()))
	if unchecked {
		// agentd refused --strict-home as a usage error: an agent image from
		// before it (DESIGN-061 3.1). A refusal, so the hold pod of that image
		// goes, and one the breaker counts, so a stale image pauses the
		// clean-up.
		at := recordTime(r.clock())
		rec = &v1alpha1.RescueStatus{Stamp: at.UTC().Format("20060102-1504"), At: &at, PodUID: string(hold.UID), Generation: v.s.Generation, Result: v1alpha1.RescueFailed,
			Message: truncate("the hold pod's agent image does not know --strict-home, so no clean-up rescue ran: " + err.Error())}
		if v.s.Status.Rescue != nil {
			rec.LastBundle = v.s.Status.Rescue.LastBundle
		}
		reason = taskRescueUnchecked
	}
	written, err := r.recordTaskRescue(ctx, v, rec, reason)
	if err != nil {
		return r.expiryUnknown(ctx, v.s, obs, fmt.Errorf("record the rescue in the hold pod %s: %w", hold.Name, err))
	}
	if !written {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	r.taskMarkGraded(hold.UID, rec.Stamp)
	switch {
	case rec.Result != v1alpha1.RescueFailed:
		r.taskBreaker(false, v.now)
	case taskBreakerReason(reason):
		r.taskBreaker(true, v.now)
	}
	r.event(v.s, eventType(rec), "Rescue", reason, rec.Message)
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// recordTaskRescue writes the rescue record onto the newest Session, as
// recordRescue does, while it is the one the rescue ran for: same UID and
// generation, and its expiry record still at Rescuing naming the hold pod the
// rescue ran in. A finished task may still want its Pod (a Failed task's
// operatingMode is Running), which recordRescue would refuse; the record is
// the clean-up's binding instead.
func (r *Reconciler) recordTaskRescue(ctx context.Context, v *expiryView, rec *v1alpha1.RescueStatus, reason string) (bool, error) {
	for range 5 {
		var fresh v1alpha1.AgentSession
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(v.s), &fresh); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		h := fresh.Status.PrivateHistory
		if fresh.UID != v.s.UID || fresh.Generation != rec.Generation || h == nil || h.Expiry == nil ||
			h.Expiry.Phase != v1alpha1.HomeExpiryRescuing || h.Expiry.HoldPodUID == "" || h.Expiry.HoldPodUID != rec.PodUID {
			return false, nil
		}
		fresh.Status.Rescue = rec
		setRescueCondition(&fresh, &fresh.Status, reason)
		err := r.Client.Status().Update(ctx, &fresh)
		if err == nil {
			*v.s = fresh
			return true, nil
		}
		if !apierrors.IsConflict(err) {
			return false, client.IgnoreNotFound(err)
		}
	}
	return false, nil
}

// taskRescueID is the rescue's directory under rescue/, from its manifest
// (report.bundle.manifest, kept as status.rescue.lastBundle), which can be
// <stamp>-2: never from the stamp alone (DESIGN-061 3.1).
func taskRescueID(s *v1alpha1.AgentSession, manifest, stamp string) (string, error) {
	dir, file := path.Split(manifest)
	id, ok := strings.CutPrefix(path.Clean(dir), protocol.RescueRoot+"/")
	session, name, err := protocol.ParseRescueID(id)
	if !ok || file != "manifest.json" || err != nil || session != s.Name || (name != stamp && !strings.HasPrefix(name, stamp+"-")) {
		return "", fmt.Errorf("the Verified rescue %s names no bundle directory of its own (%q)", stamp, manifest)
	}
	return id, nil
}

// verifyTaskBundle runs rescue-verify for the rescue id on the shelf: ok when
// the shelf answers ok with a digest and at least one bundle. Otherwise the
// step's answer: busy waits a minute, an error is shelf_unavailable, and a
// refusal is bundle_missing or bundle_mismatch, the HOME kept.
func (r *Reconciler) verifyTaskBundle(ctx context.Context, v *expiryView, obs *observation, id, manifest string) (protocol.RescueVerifyResult, *expiryRefusal, bool) {
	if r.RescueVerifier == nil {
		return protocol.RescueVerifyResult{}, refusal(expiryShelfUnavailable, "this operator has no rescue verifier"), false
	}
	res, err := r.RescueVerifier.Verify(ctx, id)
	switch {
	case errors.Is(err, shelf.ErrSlotsBusy):
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonSaving, message: "no shelf exec slot is free for the bundle's verify; trying again in a minute", pending: true}
		return res, nil, false
	case err != nil:
		return res, refusal(expiryShelfUnavailable, "rescue-verify %s: %v", id, err), false
	case !res.OK:
		reason := res.Refusal
		if reason != expiryBundleMissing {
			reason = expiryBundleMismatch
		}
		return res, refusal(reason, "rescue-verify %s refused %s: %s", id, res.Refusal, res.Detail), false
	case res.ID != id || res.Manifest != manifest || res.Bundles < 1 || !protocol.ValidSHA256(res.ManifestSHA256):
		return res, refusal(expiryBundleMismatch, "rescue-verify %s answered manifest %q with %d bundles and digest %q", id, res.Manifest, res.Bundles, res.ManifestSHA256), false
	}
	return res, nil, true
}

// saveTaskBundle keeps a valid rescue in the record (DESIGN-061 3): for a
// Verified one the shelf re-hashes every bundle first, and its manifest's
// digest, bundles and bytes go into bundle; CleanAndPushed names none. It
// writes Archived on the Session as the API server has it after the verify:
// still at Rescuing with the same hold pod and the same rescue in it, eligible,
// unheld, and nothing new holding the HOME.
func (r *Reconciler) saveTaskBundle(ctx context.Context, v *expiryView, hold *corev1.Pod, obs *observation) (ctrl.Result, bool, error) {
	rec := v.s.Status.Rescue
	deadline := r.taskAttemptStarted(v, hold).Add(taskAttemptDeadline)
	b := &v1alpha1.PrivateHomeBundleStatus{Stamp: rec.Stamp, Result: bundleCleanAndPushed}
	if rec.Result == v1alpha1.RescueVerified {
		id, err := taskRescueID(v.s, rec.LastBundle, rec.Stamp)
		if err != nil {
			return r.refuseHomeExpiry(ctx, v, obs, refusal(expiryBundleMismatch, "%v", err))
		}
		verifyCtx, cancel := context.WithTimeout(ctx, max(time.Nanosecond, deadline.Sub(r.clock())))
		defer cancel()
		res, ref, ok := r.verifyTaskBundle(verifyCtx, v, obs, id, rec.LastBundle)
		if !r.clock().Before(deadline) || errors.Is(verifyCtx.Err(), context.DeadlineExceeded) {
			return r.taskRefuseStuckAttempt(ctx, v, obs)
		}
		if ref != nil {
			return r.refuseHomeExpiry(ctx, v, obs, ref)
		}
		if !ok {
			return ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
		}
		b.Result, b.Manifest, b.ManifestSHA256, b.Bundles, b.Bytes = bundleVerified, res.Manifest, res.ManifestSHA256, int32(min(res.Bundles, 1<<30)), res.Bytes
	}
	after, err := r.readExpiryView(ctx, v.s)
	if err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	e, before := after.record(), v.record()
	if e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.HoldPodUID != before.HoldPodUID || after.ownHold() == nil || after.newPod() != nil ||
		homeExpiryHeld(after.s) || r.taskCleanupParked(after.s, obs) || !rescued(after.s) || !rescueRanIn(after.s, hold) || after.s.Status.Rescue.Stamp != rec.Stamp || expiryRecheck(after) != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	if !after.now.Before(deadline) {
		return r.taskRefuseStuckAttempt(ctx, after, obs)
	}
	fresh := after.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	b.VerifiedAt = recordTime(after.now)
	next.Phase, next.RescueStamp, next.Bundle = v1alpha1.HomeExpiryArchived, rec.Stamp, b
	clearExpiryRefusal(next)
	if err := r.writeExpiry(ctx, after, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	*v.s = *after.s
	log.FromContext(ctx).Info("the task clean-up saved its rescue", "stamp", b.Stamp, "result", b.Result, "manifest", b.Manifest, "bundles", b.Bundles, "bytes", b.Bytes)
	r.event(v.s, corev1.EventTypeNormal, "TaskCleanup", "TaskCleanupSaved", "the task clean-up "+bundleSavedText(v.s.Name, b, rescueRefs(rec))+"; the HOME is deleted next")
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// taskCleanupArchived is Archived's step: drop-log in the hold pod, then the
// hold pod goes, then the delete's first attempt (prepareExpiryDelete), which
// verifies the bundle again and records Deleting.
func (r *Reconciler) taskCleanupArchived(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if p := v.ownHold(); p != nil {
		remaining := r.taskAttemptStarted(v, p).Add(taskAttemptDeadline).Sub(v.now)
		if remaining <= 0 {
			return r.taskRefuseStuckAttempt(ctx, v, obs)
		}
		if !p.DeletionTimestamp.IsZero() {
			return ctrl.Result{RequeueAfter: min(homeExpiryPodWait, remaining)}, false, nil
		}
		r.taskDropLog(ctx, v, p)
		return r.removeExpiryHold(ctx, v, obs, p, holdReleaseArchived)
	}
	return r.prepareExpiryDelete(ctx, v, obs)
}

// taskDropLog removes the Session's own log copy from the shared volume
// (`agentd ctl drop-log` in the hold pod) before the hold pod goes. A failure
// does not keep the HOME: the shelf's 30-day prune is the fallback
// (DESIGN-061 3.1).
func (r *Reconciler) taskDropLog(ctx context.Context, v *expiryView, hold *corev1.Pod) {
	tr, ok := r.Rescuer.(TaskCleanupRescuer)
	if !ok || hold.Status.Phase != corev1.PodRunning {
		log.FromContext(ctx).Info("the task clean-up leaves the log copy to the shelf's prune", "pod", hold.Name)
		return
	}
	res, err := tr.DropLog(ctx, hold)
	if err != nil {
		log.FromContext(ctx).Info("drop-log failed; the shelf's prune removes the log copy later", "pod", hold.Name, "error", err.Error())
		return
	}
	log.FromContext(ctx).Info("dropped the session's log copy from the shared volume", "path", res.Path, "removed", res.Removed, "bytes", res.Bytes)
}

// keptWorkVerified is point 1 of DESIGN-025 6.5 for the record's kept work,
// on every attempt that can remove the HOME: the archive's verify for a
// DESIGN-025 record, and for a task record its bundle's (DESIGN-061 3): a
// Verified bundle re-hashed on the shelf must answer the recorded manifest,
// digest, bundles and bytes; CleanAndPushed needs status.rescue still naming
// the hold pod and stamp of that verdict. ok only then; otherwise the step's
// answer is recorded. An Expired record cannot store that answer:
// removeCleanedTaskSession uses keptWorkRefusal and does not write.
func (r *Reconciler) keptWorkVerified(ctx context.Context, v *expiryView, obs *observation) (ok bool, res ctrl.Result, stop bool, err error) {
	if v.record().Bundle == nil && !r.taskPolicy(v.s) {
		return r.historyArchiveVerified(ctx, v, obs)
	}
	ok, ref, unknown := r.keptWorkRefusal(ctx, v, obs)
	switch {
	case ok:
		return true, ctrl.Result{}, false, nil
	case unknown || ref == nil:
		return false, ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
	}
	res, stop, err = r.refuseHomeExpiry(ctx, v, obs, ref)
	return false, res, stop, err
}

// keptWorkRefusal judges the record's kept work and writes nothing. ok only
// when it verifies. unknown means the verify did not finish (no shelf slot):
// obs.homeExpiry may already say so, and the caller retries after
// homeExpiryUnknownRetry. Otherwise ref is the refusal keptWorkVerified
// would record, including a shelf that could not be reached.
func (r *Reconciler) keptWorkRefusal(ctx context.Context, v *expiryView, obs *observation) (ok bool, ref *expiryRefusal, unknown bool) {
	e := v.record()
	if e.Bundle == nil {
		return false, refusal(expiryBundleMissing, "the clean-up record holds no bundle"), false
	}
	b := e.Bundle
	if b.Result == bundleCleanAndPushed {
		rec := v.s.Status.Rescue
		if rec == nil || rec.Superseded || rec.Result != v1alpha1.RescueCleanAndPushed || rec.Stamp != b.Stamp || e.HoldPodUID == "" || rec.PodUID != e.HoldPodUID {
			return false, refusal(expiryBundleMismatch, "status.rescue no longer names the clean and pushed rescue %s in the hold pod %s", b.Stamp, e.HoldPodUID), false
		}
		return true, nil, false
	}
	id, idErr := taskRescueID(v.s, b.Manifest, b.Stamp)
	if idErr != nil {
		return false, refusal(expiryBundleMismatch, "%v", idErr), false
	}
	got, bundleRef, verified := r.verifyTaskBundle(ctx, v, obs, id, b.Manifest)
	switch {
	case bundleRef != nil:
		return false, bundleRef, false
	case !verified:
		return false, nil, true
	case got.ManifestSHA256 != b.ManifestSHA256 || int64(got.Bundles) != int64(b.Bundles) || got.Bytes != b.Bytes:
		return false, refusal(expiryBundleMismatch, "rescue-verify %s found digest %s, %d bundles and %d bytes, not the recorded %s, %d and %d",
			id, got.ManifestSHA256, got.Bundles, got.Bytes, b.ManifestSHA256, b.Bundles, b.Bytes), false
	}
	return true, nil, false
}

// keptName names a record's kept work for texts and events: the archive entry,
// the bundle's rescue id, or "nothing" for a clean and pushed one. It is safe
// for every record (DESIGN-061 2, item 6).
func keptName(s *v1alpha1.AgentSession, e *v1alpha1.PrivateHomeExpiryStatus) string {
	switch {
	case e == nil:
		return ""
	case e.Archive != nil:
		return "the archive " + e.Archive.Entry
	case e.Bundle != nil && e.Bundle.Result == bundleVerified:
		if id, err := taskRescueID(s, e.Bundle.Manifest, e.Bundle.Stamp); err == nil {
			return "rescue " + id
		}
		return "rescue " + s.Name + "/" + e.Bundle.Stamp
	case e.Bundle != nil:
		return "the clean and pushed rescue " + e.Bundle.Stamp
	}
	return "no kept work"
}

// taskVolumeReleased is DESIGN-061 1.1 on uncached reads: the terminal
// record's node has the same name and UID; all Nodes are Ready with fresh
// leases and none lists the HOME's volume in status.volumesInUse
// (kubernetes.io/csi/<driver>^<volumeHandle> from the claim's
// PersistentVolume). It returns why the release is not shown, or "". A node
// that is gone or has a new UID never shows it. The caller's view shows no
// Pod of the name and none mounting the claim.
func (r *Reconciler) taskVolumeReleased(ctx context.Context, v *expiryView) string {
	t := v.s.Status.PrivateHistory.ExecutorTerminal
	if t == nil || t.NodeName == "" {
		return "the terminal record names no node"
	}
	node, _, _, err := executorNodeLive(ctx, r.APIReader, t.NodeName, r.clock)
	switch {
	case err != nil:
		return fmt.Sprintf("node %s is not shown Ready with a fresh lease (%v)", t.NodeName, err)
	case string(node.UID) != t.NodeUID:
		return fmt.Sprintf("node %s has the UID %s, not the recorded %s", t.NodeName, node.UID, t.NodeUID)
	case v.claim == nil || v.claim.Spec.VolumeName == "":
		return "the HOME claim names no volume"
	}
	var pv corev1.PersistentVolume
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: v.claim.Spec.VolumeName}, &pv); err != nil {
		return fmt.Sprintf("the HOME's volume %s cannot be read (%v)", v.claim.Spec.VolumeName, err)
	}
	switch {
	case pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != v.claim.UID:
		return fmt.Sprintf("the volume %s is not bound to the HOME claim", pv.Name)
	case pv.Spec.CSI == nil || pv.Spec.CSI.Driver == "" || pv.Spec.CSI.VolumeHandle == "":
		return fmt.Sprintf("the volume %s is not a CSI volume, so its release cannot be shown", pv.Name)
	}
	unique := corev1.UniqueVolumeName("kubernetes.io/csi/" + pv.Spec.CSI.Driver + "^" + pv.Spec.CSI.VolumeHandle)
	if slices.Contains(node.Status.VolumesInUse, unique) {
		return fmt.Sprintf("node %s still lists the HOME's volume %s as in use", t.NodeName, unique)
	}
	// No remembered hold node: a new leader must also prove release on
	// every possible previous mounting node after its Pod disappeared.
	var nodes corev1.NodeList
	if err := r.APIReader.List(ctx, &nodes); err != nil {
		return fmt.Sprintf("all nodes cannot be read (%v)", err)
	}
	foundExecutor := false
	for _, listed := range nodes.Items {
		other, _, _, err := executorNodeLive(ctx, r.APIReader, listed.Name, r.clock)
		if err != nil {
			return fmt.Sprintf("node %s is not shown Ready with a fresh lease (%v)", listed.Name, err)
		}
		if other.UID != listed.UID {
			return fmt.Sprintf("node %s changed UID during the release check", listed.Name)
		}
		if other.Name == t.NodeName {
			foundExecutor = true
			if string(other.UID) != t.NodeUID {
				return fmt.Sprintf("executor node %s changed UID during the release check", t.NodeName)
			}
		}
		if slices.Contains(other.Status.VolumesInUse, unique) {
			return fmt.Sprintf("node %s still lists the HOME's volume %s as in use", other.Name, unique)
		}
	}
	if !foundExecutor {
		return "the executor node is missing from the node list"
	}
	if v.pod != nil || len(v.mounts) > 0 {
		return "a Pod still mounts the HOME claim"
	}
	return ""
}

// taskSessionRemovalAllowed is nil only for a finished task Session the
// clean-up may delete (DESIGN-061 2, item 5): in scope, private with its
// retention identity, not Reaping, and its record Expired for the bound HOME
// with a bundle. The delete then makes it Reaping, and expiryReleaseAllowed
// releases it once uncached reads find no Pod and no claim.
func taskSessionRemovalAllowed(s *v1alpha1.AgentSession) error {
	h := s.Status.PrivateHistory
	switch {
	case !taskCleanupScope(s):
		return errors.New("only a task Session in the clean-up's scope is deleted by it (DESIGN-061 2)")
	case !s.DeletionTimestamp.IsZero():
		return errors.New("a reaped Session is released, not deleted again")
	case homeExpiryHeld(s):
		return errors.New("the owner holds the task clean-up")
	case privateHistoryIdentity(s) != nil:
		return privateHistoryIdentity(s)
	case s.UID == "" || s.ResourceVersion == "":
		return errors.New("the Session has no UID or resourceVersion to delete by")
	case h.HomeUID == "" || h.Expiry == nil || h.Expiry.Phase != v1alpha1.HomeExpiryExpired || h.Expiry.HomeDeletedAt == nil ||
		h.Expiry.HomeUID != h.HomeUID || h.Expiry.Bundle == nil:
		return errors.New("the task clean-up record is not Expired for the bound HOME with its bundle")
	}
	return nil
}

// deleteCleanedTaskSession deletes a finished task Session whose HOME the
// clean-up deleted, if the guard allows it, with its UID and resourceVersion
// as preconditions and background propagation, as the API's reap does. Its
// one caller, removeCleanedTaskSession, reads it uncached first.
func deleteCleanedTaskSession(ctx context.Context, c client.Client, s *v1alpha1.AgentSession) error {
	if err := taskSessionRemovalAllowed(s); err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, s, client.Preconditions{UID: &s.UID, ResourceVersion: &s.ResourceVersion},
		client.PropagationPolicy(metav1.DeletePropagationBackground)))
}

// removeCleanedTaskSession is the finished Session's end (DESIGN-061 2): once
// its record is Expired, uncached reads find no Pod of its name and no HOME
// claim, and the guard allows it, the kept work verifies again and then it
// is deleted. The record is already Expired, so a failed verify is not
// written (the schema forbids any change to it). The note, one warning per
// reason and a 15-minute retry say why the Session stays. A shelf that
// could not be reached retries after a minute and also writes nothing. The
// next reconcile releases a deleted Session like a reaped one (6.6).
func (r *Reconciler) removeCleanedTaskSession(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if r.taskCleanupParked(v.s, obs) {
		return ctrl.Result{RequeueAfter: homeExpiryMaxWait}, false, nil
	}
	if err := taskSessionRemovalAllowed(v.s); err != nil {
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonDeleted, message: "the session is not removed: " + err.Error()}
		return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
	}
	if v.pod != nil || len(v.mounts) > 0 || !homeGone(v) {
		obs.homeExpiry = &homeExpiryNote{reason: taskReasonDeleted, message: "a Pod or the HOME claim of the session is still there; the session is removed once none is"}
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}
	// The claim is already gone, so there is no volume left to check.
	// keptWorkVerified would write the refusal; an Expired record cannot change.
	ok, ref, unknown := r.keptWorkRefusal(ctx, v, obs)
	if !ok {
		if unknown || ref == nil || ref.reason == expiryShelfUnavailable || ref.reason == expiryUnknown {
			if ref != nil && obs.homeExpiry == nil {
				obs.homeExpiry = &homeExpiryNote{reason: "Unknown", message: truncate(ref.Error()), unknown: true, pending: true}
			}
			return ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
		}
		return r.keepExpiredTaskSession(v.s, obs, ref)
	}
	if err := deleteCleanedTaskSession(ctx, r.Client, v.s); err != nil {
		switch {
		case apierrors.IsConflict(err), apierrors.IsInvalid(err):
			r.countTaskRefused(expiryPreconditionFailed)
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		case apierrors.IsForbidden(err):
			// A denial: an Expired record never changes, so it is said and
			// looked at again in 15 minutes, not retried every minute.
			r.countTaskRefused(expiryForbidden)
			obs.homeExpiry = &homeExpiryNote{reason: taskReasonDeleted, message: "the session cannot be deleted: " + truncate(err.Error())}
			r.event(v.s, corev1.EventTypeWarning, "TaskCleanup", "TaskCleanupRefused", "the task clean-up cannot delete the session: "+err.Error())
			return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
		}
		return r.expiryUnknown(ctx, v.s, obs, fmt.Errorf("delete the cleaned task Session: %w", err))
	}
	log.FromContext(ctx).Info("deleted a finished task Session after its HOME was cleaned up; it is released next", "kept", keptName(v.s, v.record()))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// keepExpiredTaskSession says why a finished Session stays. The Expired
// record cannot change, so nothing is written: the note, one warning per
// reason, and a 15-minute retry are what a person sees.
func (r *Reconciler) keepExpiredTaskSession(s *v1alpha1.AgentSession, obs *observation, ref *expiryRefusal) (ctrl.Result, bool, error) {
	note := "the session is not removed: " + ref.Error()
	obs.homeExpiry = &homeExpiryNote{reason: taskReasonDeleted, message: truncate(note)}
	if r.markExpiredRemoval(s.UID, ref.reason) {
		r.countTaskRefused(ref.reason)
		r.event(s, corev1.EventTypeWarning, "TaskCleanup", "TaskCleanupRefused", note)
	}
	return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
}

// markExpiredRemoval is true the first time this operator says this reason
// for the Session. Later retries of the same reason stay quiet.
func (r *Reconciler) markExpiredRemoval(uid types.UID, reason string) bool {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.removalNoted == nil {
		st.removalNoted = map[types.UID]string{}
	}
	if st.removalNoted[uid] == reason {
		return false
	}
	st.removalNoted[uid] = reason
	return true
}

// countTaskRefused counts a clean-up refusal in its own counter only.
func (r *Reconciler) countTaskRefused(reason string) {
	if r.TaskCleanupCounters != nil {
		r.TaskCleanupCounters.Refused.WithLabelValues(reason).Inc()
	}
}

// taskSessionRemovedEvent is TaskCleanupSessionRemoved: the rescue and its
// keep date.
func (r *Reconciler) taskSessionRemovedEvent(s *v1alpha1.AgentSession, cause string) {
	e := s.Status.PrivateHistory.Expiry
	r.event(s, corev1.EventTypeNormal, "TaskCleanup", "TaskCleanupSessionRemoved",
		fmt.Sprintf("the %s task session is removed; %s", cause, bundleKeptText(s, e)))
	if r.TaskCleanupCounters != nil {
		r.TaskCleanupCounters.Deleted.WithLabelValues(cause).Inc()
		if at, ok := r.taskAdmittedAt(s.UID); ok {
			r.TaskCleanupCounters.Duration.Observe(r.clock().Sub(at).Seconds())
		}
	}
}

// bundleKeptText says what of the Session is kept after it is gone.
func bundleKeptText(s *v1alpha1.AgentSession, e *v1alpha1.PrivateHomeExpiryStatus) string {
	if e == nil || e.Bundle == nil || e.Bundle.Result != bundleVerified {
		return "every repository was clean and pushed, so nothing is kept"
	}
	return fmt.Sprintf("%s is kept until about %s (30 days)", keptName(s, e), e.Bundle.VerifiedAt.Add(30*24*time.Hour).UTC().Format(time.RFC3339))
}

// bundleSavedText says what the rescue saved.
func bundleSavedText(name string, b *v1alpha1.PrivateHomeBundleStatus, refs int) string {
	if b.Result != bundleVerified {
		return "found every repository clean and pushed, so nothing was saved"
	}
	id := name + "/" + b.Stamp
	if dir, _ := path.Split(b.Manifest); dir != "" {
		id = strings.TrimPrefix(path.Clean(dir), protocol.RescueRoot+"/")
	}
	return fmt.Sprintf("saved work that never reached GitHub as rescue %s (%d refs, %s), kept 30 days after this session is gone", id, refs, formatBytes(b.Bytes))
}

// rescueRefs counts the refs a rescue bundled.
func rescueRefs(rec *v1alpha1.RescueStatus) int {
	if rec == nil {
		return 0
	}
	return len(rec.UnpushedRefs) + int(rec.OmittedRefs)
}

// formatBytes is a size for a person: B, KB, MB or GiB.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/(1<<30)), ".0") + " GiB"
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// taskCleanupPause is the first of section 4's pause reasons that holds, or
// "": the manual pause, the breaker, then the shelf's free space (checked
// before each start, cached a minute). known is false only when cacheOnly and
// the shelf must be asked again.
func (r *Reconciler) taskCleanupPause(ctx context.Context, now time.Time, cacheOnly bool) (reason string, known bool) {
	if r.TaskCleanupMaxActive <= 0 {
		return taskPauseMaxActiveZero, true
	}
	st := &r.taskCleanup
	st.mu.Lock()
	st.init()
	tripped := st.failures >= taskBreakerTrips && now.Before(st.lastFailure.Add(taskBreakerPause))
	fresh := !st.freeAt.IsZero() && now.Sub(st.freeAt) < taskFreeCacheTTL && !now.Before(st.freeAt)
	free, freeErr := st.free, st.freeErr
	st.mu.Unlock()
	if tripped {
		return taskPauseRescueFailures, true
	}
	if !fresh {
		if cacheOnly {
			return "", false
		}
		if r.RescueVerifier == nil {
			free, freeErr = protocol.RescueVerifyResult{}, errors.New("this operator has no rescue verifier")
		} else {
			// A busy shelf is no answer either; it is kept for the minute too,
			// so a busy shelf does not send every queued Session to the
			// uncached reads.
			free, freeErr = r.RescueVerifier.Verify(ctx, "")
		}
		st.mu.Lock()
		st.freeAt, st.free, st.freeErr = now, free, freeErr
		st.mu.Unlock()
	}
	switch {
	case freeErr != nil || !free.OK:
		return taskPauseShelfUnavailable, true
	case free.SharedFreeBytes < r.TaskCleanupMinSharedFree:
		return taskPauseSharedVolumeLow, true
	}
	return "", true
}

// TaskCleanupPaused reports the pause reasons that hold now, for the gauge
// (dev_env_task_cleanup_paused). The shelf's answer counts for five minutes.
func (r *Reconciler) TaskCleanupPaused(now time.Time) map[string]bool {
	out := map[string]bool{}
	if !r.taskCleanupOn() {
		return out
	}
	if r.TaskCleanupMaxActive <= 0 {
		out[taskPauseMaxActiveZero] = true
	}
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.failures >= taskBreakerTrips && now.Before(st.lastFailure.Add(taskBreakerPause)) {
		out[taskPauseRescueFailures] = true
	}
	if !st.freeAt.IsZero() && now.Sub(st.freeAt) < 5*time.Minute {
		switch {
		case st.freeErr != nil || !st.free.OK:
			out[taskPauseShelfUnavailable] = true
		case st.free.SharedFreeBytes < r.TaskCleanupMinSharedFree:
			out[taskPauseSharedVolumeLow] = true
		}
	}
	return out
}

// taskBreaker counts a failed rescue in a row, or resets the count after a
// valid one.
func (r *Reconciler) taskBreaker(failed bool, now time.Time) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	if !failed {
		st.failures = 0
		return
	}
	if st.failures >= taskBreakerTrips && !now.Before(st.lastFailure.Add(taskBreakerPause)) {
		// The pause is over: three more in a row pause it again.
		st.failures = 0
	}
	st.failures++
	st.lastFailure = now
}

func (r *Reconciler) taskReleaseWaitSince(uid types.UID, now time.Time) time.Time {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	at, ok := st.releaseWait[uid]
	if !ok || now.Before(at) {
		st.releaseWait[uid] = now
		return now
	}
	return at
}

func (r *Reconciler) taskForgetReleaseWait(uid types.UID) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	delete(st.releaseWait, uid)
}

func (r *Reconciler) taskBlockChildren(uid types.UID, now time.Time) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	st.children[uid] = now.Add(taskChildrenRetry)
	st.snap = nil
}

func (r *Reconciler) taskUnblockChildren(uid types.UID) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	if _, ok := st.children[uid]; ok {
		delete(st.children, uid)
		st.snap = nil
	}
}

func (r *Reconciler) taskChildrenBlocked(uid types.UID, now time.Time) bool {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	until, ok := st.children[uid]
	return ok && now.Before(until)
}

func (r *Reconciler) taskAdmitted(uid types.UID, now time.Time) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	st.prune(now)
	delete(st.ended, uid)
	if _, ok := st.admittedAt[uid]; !ok {
		// The first start this leader saw: a retry does not restart the
		// duration.
		st.admittedAt[uid] = now
	}
	st.snap = nil
}

func (r *Reconciler) taskAdmittedAt(uid types.UID) (time.Time, bool) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	at, ok := st.admittedAt[uid]
	delete(st.admittedAt, uid)
	return at, ok
}

func (r *Reconciler) taskUnreserve(uid types.UID) {
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	delete(st.reserved, uid)
	st.snap = nil
}

// taskQueueSnapshot is the queue as one cache list showed it: the records that
// hold a slot, and the order of those waiting for one.
type taskQueueSnapshot struct {
	at     time.Time
	active map[types.UID]bool
	order  []types.UID
}

type taskQueued struct {
	uid    types.UID
	name   string
	retry  bool
	reaped time.Time
	due    time.Time
}

// taskQueueOrder is section 4's order: first the records due at Waiting,
// purged ones first by their reap, then by effective due time, then by name;
// then the refused ones due for their retry, by their refusal and name.
func taskQueueOrder(q []taskQueued) {
	sort.SliceStable(q, func(i, j int) bool {
		a, b := q[i], q[j]
		switch {
		case a.retry != b.retry:
			return !a.retry
		case a.reaped.IsZero() != b.reaped.IsZero():
			return !a.reaped.IsZero()
		case !a.reaped.Equal(b.reaped):
			return a.reaped.Before(b.reaped)
		case !a.due.Equal(b.due):
			return a.due.Before(b.due)
		}
		return a.name < b.name
	})
}

// taskQueueFromCache lists the namespace's Sessions from the cache, without
// copying them, and builds the snapshot.
func (r *Reconciler) taskQueueFromCache(ctx context.Context, ns string, now time.Time) (*taskQueueSnapshot, error) {
	var list v1alpha1.AgentSessionList
	if err := r.Client.List(ctx, &list, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		return nil, err
	}
	snap := &taskQueueSnapshot{at: now, active: map[types.UID]bool{}}
	var waiting []taskQueued
	for i := range list.Items {
		s := &list.Items[i]
		if !r.taskPolicy(s) || s.Status.PrivateHistory.Expiry == nil {
			continue
		}
		e := s.Status.PrivateHistory.Expiry
		if taskActive(s) {
			snap.active[s.UID] = true
			continue
		}
		if homeExpiryHeld(s) || !r.taskCleanupAllowed(s.Name) {
			continue
		}
		q := taskQueued{uid: s.UID, name: s.Name, due: r.taskCleanupDueAt(s)}
		if taskCleanupPurged(s) {
			q.reaped = s.DeletionTimestamp.Time
		}
		switch {
		case e.Phase == v1alpha1.HomeExpiryWaiting && e.Refusal == "":
			release := homeExpiryOverride(s) == v1alpha1.HomeExpiryReleaseNow
			if now.Before(q.due) && !release || !taskCleanupPurged(s) && r.taskChildrenBlockedLocked(s.UID, now) {
				continue
			}
		case taskRetryRefusal(e) && e.RefusalAt != nil:
			if now.Before(e.RefusalAt.Add(taskAttemptRefusalRetry(e))) {
				continue
			}
			q.retry, q.reaped, q.due = e.Refusal == expiryRescueRefused || e.Refusal == expiryAttemptStuck, time.Time{}, e.RefusalAt.Time
			if !q.retry {
				// Any other refused attempt goes back in its place.
				q.due = r.taskCleanupDueAt(s)
				if taskCleanupPurged(s) {
					q.reaped = s.DeletionTimestamp.Time
				}
			}
		default:
			continue
		}
		waiting = append(waiting, q)
	}
	taskQueueOrder(waiting)
	for _, q := range waiting {
		snap.order = append(snap.order, q.uid)
	}
	return snap, nil
}

// taskChildrenBlockedLocked is taskChildrenBlocked for a caller that holds
// the state's lock.
func (r *Reconciler) taskChildrenBlockedLocked(uid types.UID, now time.Time) bool {
	until, ok := r.taskCleanup.children[uid]
	return ok && now.Before(until)
}

// taskCleanupAdmittable reports whether the queue admits s now (section 4):
// fewer than --task-cleanup-max-active records hold a slot, counting the
// cache's and this leader's reservations the cache does not show yet, and s
// is among the first of the queue for the slots left. With reserve, an
// admitted s is reserved until the cache shows its record active, so the four
// reconcile workers never overshoot.
func (r *Reconciler) taskCleanupAdmittable(ctx context.Context, s *v1alpha1.AgentSession, now time.Time, reserve bool) bool {
	cap := min(r.TaskCleanupMaxActive, MaxTaskCleanupMaxActive)
	if cap <= 0 {
		return false
	}
	st := &r.taskCleanup
	st.mu.Lock()
	defer st.mu.Unlock()
	st.init()
	// An admission always reads the cache again: the snapshot serves only the
	// judgement of the records that wait, so this leader does not reuse its own
	// stale snapshot. Overlapping leaders with lagging caches can still
	// exceed the cap (DESIGN-061 4); startup requires leader election.
	if reserve || st.snap == nil || now.Sub(st.snap.at) >= taskQueueSnapshotTTL || now.Before(st.snap.at) {
		snap, err := r.taskQueueFromCache(ctx, s.Namespace, now)
		if err != nil {
			return false
		}
		st.snap = snap
	}
	snap := st.snap
	held := 0
	for uid := range snap.active {
		if uid != s.UID {
			held++
		}
	}
	for uid, at := range st.reserved {
		switch {
		case now.Sub(at) >= taskReservationTTL || now.Before(at) || snap.active[uid] && snap.at.After(at):
			delete(st.reserved, uid)
		case uid != s.UID && !snap.active[uid]:
			held++
		}
	}
	free := cap - held
	if free <= 0 {
		return false
	}
	rank, found := 0, false
	for _, uid := range snap.order {
		if uid == s.UID {
			found = true
			break
		}
		if _, reserved := st.reserved[uid]; !reserved {
			rank++
		}
	}
	if !found {
		// The cache has not seen s due yet (or s is a retry it ranks last):
		// it waits behind every record the cache shows waiting.
		rank = len(snap.order)
		for _, uid := range snap.order {
			if _, reserved := st.reserved[uid]; reserved {
				rank--
			}
		}
	}
	if rank >= free {
		return false
	}
	if reserve {
		st.reserved[s.UID] = now
		st.snap = nil
	}
	return true
}
