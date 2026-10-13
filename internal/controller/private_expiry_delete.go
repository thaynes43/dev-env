package controller

// The HOME expiry past Archived (DESIGN-025 6.5 and 6.6, unit H6), behind
// --enable-home-expiry-delete, which the operator refuses to start without the
// archive and the archived-history reads. Every attempt that can remove the
// HOME runs, in its own call path and in this order: the archive's verify
// under .shelf.lock (historyArchiveVerified), the common re-check on fresh
// uncached reads made after it (expiryRechecked), and the check that no Pod
// of the Session's name and no Pod that mounts the claim exists
// (noPodUsesHome). From Archived the first attempt records Deleting (with
// status.archivedAt for a suspended Session, in the same write); each attempt
// in Deleting then deletes the claim with its UID and resourceVersion as
// preconditions and lifts the operator's finalizer from a fresh read. Deleting
// is never a licence to delete on its own. Once the claim is gone the record
// is Expired, and a reaped Session is released when nothing of it is left.

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// homeExpiryDeleteOn reports whether the expiry may go past Archived: the
// delete flag, with the archive (and so the terminal record) on.
func (r *Reconciler) homeExpiryDeleteOn() bool { return r.HomeExpiryDelete && r.homeExpiryOn() }

// homeGone reports whether the uncached reads show the bound HOME claim gone:
// NotFound, or another claim of its name, which the bound one cannot be.
func homeGone(v *expiryView) bool {
	return v.claim == nil || string(v.claim.UID) != v.s.Status.PrivateHistory.HomeUID
}

// homeExpiryRemovingHome reports whether the Session's expiry record has
// reached Deleting: from there the HOME's absence is the expiry's own doing.
func homeExpiryRemovingHome(s *v1alpha1.AgentSession) bool {
	h := s.Status.PrivateHistory
	return h != nil && h.Expiry != nil && expiryPhaseAtLeast(h.Expiry.Phase, v1alpha1.HomeExpiryDeleting)
}

// stepExpiryDelete runs a record at Deleting or Expired. Nothing cancels it
// any more; an owner hold still stops it, and with the delete off it waits.
func (r *Reconciler) stepExpiryDelete(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	e := v.record()
	if !r.homeExpiryDeleteOn() {
		obs.homeExpiry = &homeExpiryNote{reason: "DeleteOff", message: "the HOME delete is off (--enable-home-expiry-delete); the record waits at " + e.Phase + " and the HOME, if any, stays"}
		return ctrl.Result{}, false, nil
	}
	if homeExpiryHeld(v.s) {
		obs.homeExpiry = heldNote(v.s)
		if homeExpiryOverride(v.s) == expiryOverrideInvalid {
			return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
		}
		return ctrl.Result{}, false, nil
	}
	if e.Phase == v1alpha1.HomeExpiryExpired {
		return r.releaseExpiredSession(ctx, v, obs)
	}
	if e.Refusal != "" && e.RefusalAt != nil && v.now.Before(e.RefusalAt.Add(homeExpiryRefusalRetry)) && !v.now.Before(e.RefusalAt.Time) {
		return ctrl.Result{RequeueAfter: e.RefusalAt.Add(homeExpiryRefusalRetry).Sub(v.now) + time.Second}, false, nil
	}
	return r.removeExpiredHome(ctx, v, obs)
}

// historyArchiveVerified is point 1 of 6.5: `history-archive verify` of the
// recorded entry, under the archive's shared .shelf.lock, re-hashes every
// file, every block list and content.json. ok only when it answers the
// recorded digest and entry; otherwise the step's answer: a busy relay waits
// a minute, and a refusal is recorded (archive_missing, archive_mismatch,
// shelf_unavailable) or, for Unknown, counted.
func (r *Reconciler) historyArchiveVerified(ctx context.Context, v *expiryView, obs *observation) (ok bool, res ctrl.Result, stop bool, err error) {
	e := v.record()
	if e.Archive == nil {
		res, stop, err = r.refuseHomeExpiry(ctx, v, obs, refusal(expiryArchiveMissing, "the expiry record holds no archive"))
		return false, res, stop, err
	}
	if r.HistoryRelay == nil {
		res, stop, err = r.refuseHomeExpiry(ctx, v, obs, refusal(expiryShelfUnavailable, "this operator has no history relay"))
		return false, res, stop, err
	}
	ver, verr := r.HistoryRelay.Verify(ctx, string(v.s.UID), e.Archive.ContentSHA256)
	switch {
	case errors.Is(verr, errHistoryRelayBusy):
		obs.homeExpiry = &homeExpiryNote{reason: "VerifyBusy", message: "no shelf exec slot is free for the archive's verify; trying again in a minute", pending: true}
		return false, ctrl.Result{RequeueAfter: homeExpiryUnknownRetry}, false, nil
	case verr != nil:
		res, stop, err = r.refuseHomeExpiry(ctx, v, obs, historyRefusal(verr))
		return false, res, stop, err
	case ver.ContentSHA256 != e.Archive.ContentSHA256 || ver.Entry != e.Archive.Entry:
		res, stop, err = r.refuseHomeExpiry(ctx, v, obs, refusal(expiryArchiveMismatch, "verify found %s at %s, not the recorded %s at %s", ver.ContentSHA256, ver.Entry, e.Archive.ContentSHA256, e.Archive.Entry))
		return false, res, stop, err
	}
	return true, ctrl.Result{}, false, nil
}

// expiryRechecked is the common re-check of 5.2 on fresh uncached reads made
// after the verify, which can take minutes: the same record at the same phase
// with the same archive, still eligible, triggered and unheld, and the HOME
// claim the bound one. In Deleting a claim of the bound UID that is being
// deleted is this step's own delete in progress (6.5), and a claim that is
// gone is the delete done; the caller looks at both. A nil view ends the
// step with the answer given.
func (r *Reconciler) expiryRechecked(ctx context.Context, v *expiryView, obs *observation) (*expiryView, ctrl.Result, bool, error) {
	after, err := r.readExpiryView(ctx, v.s)
	if errors.Is(err, errExpiryBehind) {
		return nil, ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	if err != nil {
		res, stop, err := r.expiryUnknown(ctx, v.s, obs, err)
		return nil, res, stop, err
	}
	e, before := after.record(), v.record()
	if e == nil || e.Phase != before.Phase || e.Archive == nil || before.Archive == nil || *e.Archive != *before.Archive || homeExpiryHeld(after.s) {
		// The record or the owner's override changed during the verify:
		// judge again from the start.
		return nil, ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	if trig, _ := homeExpiryTriggered(after.s, obs.templates, after.now); trig != expiryTriggered {
		return nil, ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	if ref := expiryDeleteRecheck(after); ref != nil {
		res, stop, err := r.refuseHomeExpiry(ctx, after, obs, ref)
		return nil, res, stop, err
	}
	return after, ctrl.Result{}, false, nil
}

// expiryDeleteRecheck is expiryRecheck for the delete's steps: in Deleting it
// accepts the bound claim while it is being deleted, if the Session still
// controls it, and a claim that is gone.
func expiryDeleteRecheck(v *expiryView) *expiryRefusal {
	s, h := v.s, v.s.Status.PrivateHistory
	e := h.Expiry
	if e.Phase != v1alpha1.HomeExpiryDeleting {
		return expiryRecheck(v)
	}
	if err := homeExpiryEligible(s); err != nil {
		return refusal(expiryIdentityChanged, "the Session is no longer eligible: %v", err)
	}
	if e.ExecutorUID != h.LastExecutor.PodUID || e.ExecutorUID != h.ExecutorTerminal.PodUID || e.HomeUID != h.HomeUID {
		return refusal(expiryIdentityChanged, "the expiry record no longer names the recorded executor and HOME")
	}
	switch c := v.claim; {
	case homeGone(v):
		return nil
	case !c.DeletionTimestamp.IsZero():
		if !controlledBySessionObject(c, s) {
			return refusal(expiryIdentityChanged, "the HOME claim being deleted is not the Session's")
		}
		return nil
	}
	return homeClaimBound(s, v.claim)
}

// noPodUsesHome is point 2 of 6.5, on the reads expiryRechecked made: no Pod
// of the Session's name and no other Pod that mounts the claim. The hold pod
// went when the archive was recorded.
func noPodUsesHome(v *expiryView) *expiryRefusal {
	switch {
	case v.pod != nil:
		return refusal(expiryClaimInUse, "the Pod %s (uid %s) of the Session's name stands", v.pod.Name, v.pod.UID)
	case len(v.mounts) > 0:
		return refusal(expiryClaimInUse, "the Pod %s mounts the HOME claim", v.mounts[0].Name)
	}
	return nil
}

// prepareExpiryDelete is Archived's step with the delete on (6.5 points 1 to
// 3): verify, the re-check and the Pod check, then one confirmed write of
// Deleting and deletingAt, with status.archivedAt for a suspended Session, so
// a Session never holds archivedAt without Deleting. A resume is refused from
// here on, and nothing cancels the record any more.
func (r *Reconciler) prepareExpiryDelete(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if ref := noPodUsesHome(after); ref != nil {
		return r.refuseHomeExpiry(ctx, after, obs, ref)
	}
	fresh := after.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	at := recordTime(after.now)
	next.Phase, next.DeletingAt = v1alpha1.HomeExpiryDeleting, &at
	clearExpiryRefusal(next)
	if fresh.DeletionTimestamp.IsZero() && fresh.Status.ArchivedAt == nil {
		fresh.Status.ArchivedAt = &at
	}
	if err := r.writeExpiry(ctx, after, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("the HOME expiry is Deleting: the archive verified again", "entry", next.Archive.Entry, "contentSHA256", next.Archive.ContentSHA256)
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HomeExpiryDeleting",
		fmt.Sprintf("the archive %s verified again; the HOME is deleted next, and the Session can no longer be resumed (DESIGN-025 6.5)", next.Archive.Entry))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// recheckArchivedRefusal re-runs the verify for a refusal recorded at
// Archived while the delete is off, so a fixed archive stops its alert. Only
// the delete's own checks refuse at Archived, so the refusal is theirs.
func (r *Reconciler) recheckArchivedRefusal(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	fresh := v.s.DeepCopy()
	clearExpiryRefusal(fresh.Status.PrivateHistory.Expiry)
	if err := r.writeExpiry(ctx, v, fresh); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("the HOME expiry's archive verifies again; its refusal is resolved")
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// removeExpiredHome is Deleting's step, on every attempt (6.5 points 1, 2, 4
// and 5): the verify, the re-check and the Pod check, in this call path, then
// the claim's delete with its UID and resourceVersion and the finalizer's lift
// (deleteExpiryHome). A claim that is gone makes the record Expired. A claim
// whose delete has started and whose finalizer is already lifted waits for
// Kubernetes, with nothing left for the operator to remove.
func (r *Reconciler) removeExpiredHome(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if homeGone(v) {
		return r.recordHomeExpired(ctx, v, obs)
	}
	if c := v.claim; !c.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(c, Finalizer) {
		obs.homeExpiry = &homeExpiryNote{reason: "HomeDeleting", message: "the HOME claim " + c.Name + " is being deleted and the operator's finalizer is lifted; waiting for it to go"}
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}
	if ok, res, stop, err := r.historyArchiveVerified(ctx, v, obs); !ok {
		return res, stop, err
	}
	after, res, stop, err := r.expiryRechecked(ctx, v, obs)
	if after == nil {
		return res, stop, err
	}
	if homeGone(after) {
		return r.recordHomeExpired(ctx, after, obs)
	}
	if ref := noPodUsesHome(after); ref != nil {
		return r.refuseHomeExpiry(ctx, after, obs, ref)
	}
	if err := expiryVolumeRemovalAllowed(after.s, after.claim); err != nil {
		return r.refuseHomeExpiry(ctx, after, obs, refusal(expiryIdentityChanged, "the HOME stays: %v", err))
	}
	if err := deleteExpiryHome(ctx, r.Client, r.APIReader, after.s, after.claim); err != nil {
		switch {
		case apierrors.IsConflict(err), apierrors.IsInvalid(err):
			// The claim changed since the read (the delete itself does
			// that): the next reconcile starts again from the verify.
			r.countExpiryRefused(expiryPreconditionFailed)
			return ctrl.Result{RequeueAfter: time.Second}, true, nil
		case apierrors.IsForbidden(err):
			return r.refuseHomeExpiry(ctx, after, obs, refusal(expiryForbidden, "delete the HOME claim: %v", err))
		}
		return r.expiryUnknown(ctx, after.s, obs, fmt.Errorf("delete the HOME claim: %w", err))
	}
	claim := after.claim
	log.FromContext(ctx).Info("deleted the private HOME after its archive verified again", "claim", claim.Name, "uid", claim.UID, "entry", after.record().Archive.Entry)
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HomeDeleted",
		fmt.Sprintf("deleted the HOME %s (uid %s) after its archive %s verified again; the archive is the copy that remains (DESIGN-025 6.5)", claim.Name, claim.UID, after.record().Archive.Entry))
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// recordHomeExpired is point 5 of 6.5: the uncached reads found the HOME
// claim gone, so the record is Expired with homeDeletedAt, by a confirmed
// write, counted once when the API server acknowledges it.
func (r *Reconciler) recordHomeExpired(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	fresh := v.s.DeepCopy()
	next := fresh.Status.PrivateHistory.Expiry
	at := recordTime(v.now)
	next.Phase, next.HomeDeletedAt = v1alpha1.HomeExpiryExpired, &at
	clearExpiryRefusal(next)
	target := v.s.DeepCopy()
	acknowledged, err := r.writePrivateHistoryAcknowledged(ctx, target, fresh)
	if acknowledged && r.HomeExpiryCounters != nil {
		r.HomeExpiryCounters.Expired.Inc()
	}
	if err != nil {
		log.FromContext(ctx).Info("the HOME expiry's Expired write is unconfirmed; judging again", "error", err.Error())
		return ctrl.Result{RequeueAfter: time.Second}, true, nil
	}
	log.FromContext(ctx).Info("the HOME expiry is Expired: the private HOME is gone", "entry", next.Archive.Entry)
	r.event(v.s, corev1.EventTypeNormal, "HomeExpiry", "HomeExpiryExpired",
		"the private HOME is gone; its history and Git rescue stay in the archive "+next.Archive.Entry+" (DESIGN-025 6.5)")
	return ctrl.Result{RequeueAfter: time.Second}, true, nil
}

// releaseExpiredSession is 6.6: a reaped Session whose record is Expired is
// released by releaseIfEmpty's one exception, once uncached reads find no Pod
// and no claim of its names. A suspended Session stays, phase Archived, until
// it is reaped.
func (r *Reconciler) releaseExpiredSession(ctx context.Context, v *expiryView, obs *observation) (ctrl.Result, bool, error) {
	if v.s.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, false, nil
	}
	if err := expiryReleaseAllowed(v.s); err != nil {
		// An Expired record never changes, so nothing is recorded; the
		// condition says why the Session stays.
		obs.homeExpiry = &homeExpiryNote{reason: "Releasing", message: "the Session is not released: " + err.Error()}
		return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
	}
	released, err := r.releaseIfEmpty(ctx, v.s)
	if err != nil {
		return r.expiryUnknown(ctx, v.s, obs, fmt.Errorf("release the Session: %w", err))
	}
	if !released {
		obs.homeExpiry = &homeExpiryNote{reason: "Releasing", message: "a Pod or claim of the Session's names is still there; the Session is released once none is"}
		return ctrl.Result{RequeueAfter: homeExpiryPodWait}, false, nil
	}
	log.FromContext(ctx).Info("released a reaped private Session whose HOME expired")
	return ctrl.Result{}, true, nil
}
