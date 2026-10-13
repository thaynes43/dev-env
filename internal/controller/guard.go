package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The guard of DESIGN-001 5.1 (D-45, D-51). The operator deletes a session's pod
// in two transitions only, Draining and Suspended, and suspends a pod that ran
// only after a rescue in it (D-10). It deletes a session's volume only when a
// deleted session is archived, after a verified rescue of the volume's last pod.
// deletePod and deleteVolume are the only places in this package that delete
// anything, besides the HOME expiry's two exceptions (DESIGN-025 5.2, 6.1):
// deleteExpiryExecutor, the ended executor of a private Session in Rescuing,
// and deleteExpiryHold, the expiry's own hold pod. Each deletes by UID;
// releaseSession, releaseVolume and a few
// named release helpers are the only ones that lift a finalizer or rewrite owner
// references. A test (TestOnlyTheGuardDeletes) keeps it that way.

// Finalizer holds a session, and its volume, until the session is rescued. A
// deleted AgentSession is a reap (D-45): the API server keeps the object while the
// finalizer is on it, and with the default (background) propagation the garbage
// collector deletes dependents only once their owner is gone, so it never
// reaches the pod or the volume. A foreground cascade is different: the garbage
// collector deletes the dependents first, while the session still exists, so
// it deletes the pod, which carries no finalizer. Only a human can ask for one
// (`kubectl delete --cascade=foreground`, D-45); the API's reap route uses
// background propagation. The volume carries the same finalizer, so a direct or
// foreground delete of the volume leaves its data until the operator archives
// it (TestPrivateHistoryForegroundAndBackgroundDeleteKeepProtectiveFinalizers).
const Finalizer = v1alpha1.LabelPrefix + "rescue"

// errNeedsRescue is why a suspend or a delete keeps a pod that ran: no rescue
// has run in it since the session last changed.
var errNeedsRescue = errors.New("the pod stays until a rescue has run in it (DESIGN-001 4.4, D-51)")

// errHoldStarting is why a hold pod that has not started yet stays: it is
// there to run the rescue (D-55).
var errHoldStarting = errors.New("the hold pod has not started yet; it stays to run the volume's rescue (D-55)")

// errHoldRescuing is why a hold pod stays while the volume has no valid rescue:
// the operator rescues in it and retries a failed rescue, and a human may exec
// in to fix the worktree meanwhile (D-55).
var errHoldRescuing = errors.New("the hold pod stays until a rescue in it makes the volume safe (D-55)")

// errDrainNotBuilt is why a Draining session keeps its pod today: drain needs
// agentd's prepare-restart, plan 04 (DESIGN-001 5.2).
var errDrainNotBuilt = errors.New("drain is not built yet (plan 04, DESIGN-001 5.2); the pod stays")

// wantsPodGone reports whether the session asks for its pod to go: suspended, or
// deleted (a reap, which suspends first).
func wantsPodGone(s *v1alpha1.AgentSession) bool {
	return s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero()
}

// podRemovalAllowed is nil only when 5.1 allows deleting the session's pod now:
// in the Suspended transition (asked by a suspend or a delete), when no agent
// process runs in the pod or a rescue ran in it since the session last changed.
// A pod runs no agent when it never started (never scheduled, or its containers
// never ran) or when it ended; then no rescue can run in it, and none is needed
// for the pod, because the volume stays and archive asks for its own rescue.
// Every other state keeps the pod, whatever else asks. Draining (plan 04)
// keeps it too, until drain is built.
//
// A hold pod (D-55) runs no agent, so holdRemovalAllowed judges it instead.
// Private history protection retains the original pod regardless of legacy
// rescue or process state; neither is private stop or preservation proof.
func podRemovalAllowed(s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if privateHistoryProtected(s) {
		return errPrivateHistoryRetained
	}
	if s.Spec.Workspace != nil {
		rec := s.Status.Rescue
		if !sharedRescued(s) || !controlledBySession(pod, s) {
			return errors.New("shared Pod stays until its owned rescue and retained executor proof are recorded")
		}
		if isHoldPod(pod) {
			proof, err := workspaceHoldProof(pod)
			if err != nil || proof == nil || rec.PodUID != string(pod.UID) || proof.Validate(s.Spec.Workspace.ID, s.Name, string(s.UID), rec.SourcePodUID) != nil {
				return errors.New("shared hold Pod does not match the verified rescue")
			}
		} else if rec.SourcePodUID != string(pod.UID) {
			return errors.New("shared executor does not match the rescued source Pod UID")
		}
		return nil
	}
	switch {
	case isHoldPod(pod):
		return holdRemovalAllowed(s, pod)
	case s.Status.Phase == v1alpha1.PhaseDraining:
		return errDrainNotBuilt
	case !wantsPodGone(s):
		return fmt.Errorf("the session is neither draining nor suspending (operatingMode %s); its pod stays (DESIGN-001 5.1)", s.Spec.OperatingMode)
	case podNeverStarted(pod), podEnded(pod), rescueRanIn(s, pod):
		return nil
	default:
		return errNeedsRescue
	}
}

// holdRemovalAllowed is nil when the session's hold pod (D-55) may go. No agent
// runs in a hold pod, so it goes as soon as nothing needs it: the session wants
// its own pod again (a resume), the hold pod ended (a new one replaces it), or
// a rescue in it made the volume safe. It stays while it starts, because it is
// there to run the rescue, and while its rescue fails, so the operator can
// retry and a human can exec in.
func holdRemovalAllowed(s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	switch {
	case !wantsPodGone(s), podEnded(pod), rescueRanIn(s, pod) && rescued(s):
		return nil
	case podNeverStarted(pod):
		return errHoldStarting
	default:
		return errHoldRescuing
	}
}

// podNeverStarted reports whether no container of the pod has ever run: it was
// never scheduled, or the kubelet reports every container waiting with no
// restart and no earlier run. deletePod's resourceVersion precondition makes the
// delete fail if that changed after the read.
func podNeverStarted(p *corev1.Pod) bool {
	if p.Spec.NodeName == "" {
		return true
	}
	if p.Status.Phase != corev1.PodPending || len(p.Status.ContainerStatuses) != len(p.Spec.Containers) {
		return false
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		if cs.State.Waiting == nil || cs.RestartCount > 0 || cs.LastTerminationState.Terminated != nil || cs.LastTerminationState.Running != nil {
			return false
		}
	}
	return true
}

// podEnded reports whether every container of the pod has stopped for good,
// for example after an eviction.
func podEnded(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded
}

// needsRescue reports whether the operator must run a rescue in the pod before
// it may go: an agent may run in it, and no rescue has since the session last
// changed. In a hold pod (D-55) it is the volume's rescue: it runs while the
// session still wants its pod gone, once the pod has started, and again every
// retry while the last one failed.
func needsRescue(s *v1alpha1.AgentSession, pod *corev1.Pod, now time.Time, retry time.Duration) bool {
	switch {
	case podNeverStarted(pod) || podEnded(pod):
		return false
	case !isHoldPod(pod):
		return !rescueRanIn(s, pod)
	case !wantsPodGone(s):
		return false
	case !rescueRanIn(s, pod):
		return true
	default:
		return holdRetryDue(s, now, retry)
	}
}

// holdRetryDue reports whether the newest rescue failed at least retry ago,
// so a hold pod's rescue runs again (D-55).
func holdRetryDue(s *v1alpha1.AgentSession, now time.Time, retry time.Duration) bool {
	r := s.Status.Rescue
	return r != nil && r.Result == v1alpha1.RescueFailed && (r.At == nil || !now.Before(r.At.Add(retry)))
}

// volumeRemovalAllowed is nil only when archive may delete the session's volume
// (D-10, D-51, D-62): the session is deleted (a reap), or it is suspended and
// its archive timer is due (archiveDue, which the caller computes from
// status.suspendedAt and the window); no pod of it exists; and the newest
// rescue, of the volume's last pod, is verified or proved there was nothing to
// save. podExists must come from the API server, not the cache.
func volumeRemovalAllowed(s *v1alpha1.AgentSession, podExists, archiveDue bool) error {
	if privateHistoryProtected(s) {
		return errPrivateHistoryRetained
	}
	if s.Spec.Workspace != nil {
		return errors.New("shared task rescue does not archive its private home; the home and workspace claim stay retained")
	}
	r := s.Status.Rescue
	switch {
	case s.DeletionTimestamp.IsZero() && s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended:
		return errors.New("the session wants its pod: only a reap or the archive timer of a suspended session archives a volume")
	case s.DeletionTimestamp.IsZero() && !archiveDue:
		return errors.New("a suspended session keeps its volume until its archive timer is due (D-09, D-62)")
	case podExists:
		return errors.New("a pod of the session still exists, so the volume may still change")
	case r == nil || r.Result == "":
		return errors.New("no rescue has run on the volume; a pod that never ran or ended cannot run one, so a hold pod does (D-55)")
	case r.Superseded:
		return fmt.Errorf("the rescue %s was superseded: a pod started on the volume after it, and no rescue ran in that pod", r.Stamp)
	case !rescued(s):
		return fmt.Errorf("the newest rescue failed, so the volume is kept for a human: %s", r.Message)
	case s.DeletionTimestamp.IsZero() && s.Status.ArchivedAt == nil:
		return errArchiveNotRecorded
	default:
		return nil
	}
}

// errArchiveNotRecorded keeps a suspended session's volume until
// status.archivedAt is on the API server: the record comes first, so the
// session can never be resumed onto a new volume (D-62).
var errArchiveNotRecorded = errors.New("the archive is not recorded in the session's status yet; it is written before the volume goes (D-62)")

// deletePod deletes the session's pod if the guard allows it. With deleteVolume
// it is the only delete in the package.
func deletePod(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if err := podRemovalAllowed(s, pod); err != nil {
		return err
	}
	// The preconditions make the delete hit this pod, in the state the guard
	// judged, and no other: not one that replaced it, and not this one after
	// it was scheduled or started.
	return client.IgnoreNotFound(c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}))
}

// expiryPodRemovalAllowed is nil only when the HOME expiry may delete a
// private Session's recorded executor Pod (DESIGN-025 5.2; ADR-005's D-45
// exception): the Session is private, its expiry record is Rescuing and rests
// on a confirmed terminal record of exactly this Pod, the Session controls the
// Pod, it is no hold pod, and its phase is still terminal. A terminal phase
// never runs again, and the record is the stop proof, so the Pod object is
// needed no more; the hold pod takes its name. podRemovalAllowed keeps every
// other Pod of a private Session.
func expiryPodRemovalAllowed(s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	h := s.Status.PrivateHistory
	switch {
	case s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil:
		return errors.New("only a private Session's HOME expires; canary originals and shared Sessions never do (DESIGN-025 2)")
	case h == nil || h.Expiry == nil || h.Expiry.Phase != v1alpha1.HomeExpiryRescuing:
		return errors.New("the HOME expiry removes the ended executor only in its Rescuing step (DESIGN-025 5.2)")
	case !executorTerminalRecorded(h) || h.Expiry.ExecutorUID != h.ExecutorTerminal.PodUID:
		return errors.New("the expiry record rests on no confirmed terminal record of the recorded executor")
	case pod.UID == "" || string(pod.UID) != h.Expiry.ExecutorUID || pod.ResourceVersion == "":
		return errors.New("the Pod is not the recorded executor that the terminal record names")
	case !controlledBySession(pod, s) || isHoldPod(pod):
		return errors.New("the Session does not control the Pod, or it is a hold pod")
	case !podPhaseTerminal(pod):
		return errors.New("the recorded executor's phase is not terminal")
	}
	return nil
}

// deleteExpiryExecutor deletes the recorded executor's ended Pod if the expiry
// guard allows it, with the Pod's UID and resourceVersion as preconditions, so
// a Pod changed since the guard judged it is never deleted.
func deleteExpiryExecutor(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if err := expiryPodRemovalAllowed(s, pod); err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}))
}

// expiryHoldRelease is why the HOME expiry no longer needs its hold pod
// (DESIGN-025 6.1).
type expiryHoldRelease string

const (
	holdReleaseCancel     expiryHoldRelease = "the expiry record is being cancelled"
	holdReleaseOwnerHold  expiryHoldRelease = "the owner put a hold on the HOME expiry"
	holdReleaseArchiveOff expiryHoldRelease = "the history archive is turned off"
	holdReleaseArchived   expiryHoldRelease = "the history is archived"
	holdReleaseEnded      expiryHoldRelease = "the hold pod ended"
)

// expiryHoldRemovalAllowed is nil only when the HOME expiry may delete its own
// hold pod (DESIGN-025 6.1, ADR-006's fifth D-45 exception): the Pod carries
// the hold label, the Session controls it, its UID is the expiry record's
// holdPodUID, its three annotations match the record, and the expiry no longer
// needs it. podRemovalAllowed refuses every Pod of a private Session, and
// holdRemovalAllowed would keep a Running, unrescued hold pod, so neither can
// remove it. The guard checks the reasons it can see itself: an owner hold, a
// record Archived or later, a hold pod that ended. A cancel and archive turned
// off are the caller's findings.
func expiryHoldRemovalAllowed(s *v1alpha1.AgentSession, pod *corev1.Pod, why expiryHoldRelease) error {
	h := s.Status.PrivateHistory
	switch {
	case s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil:
		return errors.New("only a private Session's HOME expiry has a hold pod of its own")
	case h == nil || h.Expiry == nil:
		return errors.New("no HOME expiry record names a hold pod")
	case !isHoldPod(pod) || !controlledBySession(pod, s) || pod.UID == "" || pod.ResourceVersion == "":
		return errors.New("the Pod is not a hold pod the Session controls")
	case h.Expiry.HoldPodUID == "" || string(pod.UID) != h.Expiry.HoldPodUID || !expiryHoldAnnotated(pod, h.Expiry):
		return errors.New("the hold pod is not the one the expiry record names")
	}
	switch why {
	case holdReleaseCancel, holdReleaseArchiveOff:
		return nil
	case holdReleaseOwnerHold:
		if homeExpiryOverride(s) == v1alpha1.HomeExpiryHold {
			return nil
		}
	case holdReleaseArchived:
		if expiryPhaseAtLeast(h.Expiry.Phase, v1alpha1.HomeExpiryArchived) {
			return nil
		}
	case holdReleaseEnded:
		if podEnded(pod) {
			return nil
		}
	}
	return fmt.Errorf("the HOME expiry still needs its hold pod: %s does not hold", why)
}

// deleteExpiryHold deletes the HOME expiry's hold pod if the guard allows it,
// with the Pod's UID and resourceVersion as preconditions.
func deleteExpiryHold(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, pod *corev1.Pod, why expiryHoldRelease) error {
	if err := expiryHoldRemovalAllowed(s, pod, why); err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}))
}

// deleteVolume archives the session's volume if the guard allows it: it deletes
// the claim, then lifts the operator's finalizer from it, so the API server can
// remove it (D-45). With the storage class's Delete policy the RBD image goes
// with it, so this is where work is lost if the rescue was wrong.
func deleteVolume(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, claim *corev1.PersistentVolumeClaim, podExists, archiveDue bool) error {
	if err := volumeRemovalAllowed(s, podExists, archiveDue); err != nil {
		return err
	}
	if claim.DeletionTimestamp.IsZero() {
		if err := c.Delete(ctx, claim, client.Preconditions{UID: &claim.UID}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return releaseVolume(ctx, c, s, claim, podExists, archiveDue)
}

// releaseVolume lifts the operator's finalizer from the session's volume. Only
// deleteVolume calls it (TestOnlyTheGuardDeletes).
func releaseVolume(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, claim *corev1.PersistentVolumeClaim, podExists, archiveDue bool) error {
	if err := volumeRemovalAllowed(s, podExists, archiveDue); err != nil {
		return err
	}
	if !controllerutil.ContainsFinalizer(claim, Finalizer) {
		return nil
	}
	orig := claim.DeepCopy()
	controllerutil.RemoveFinalizer(claim, Finalizer)
	return client.IgnoreNotFound(c.Patch(ctx, claim, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})))
}
