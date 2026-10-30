package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The guard of DESIGN-001 5.1 (D-45, D-51). The operator deletes a session's pod
// in two transitions only, Draining and Suspended, and suspends a pod that ran
// only after a rescue in it (D-10). Drain (DESIGN-001 5.2) may delete the pod
// only after prepare-restart has recorded the conversation for that pod; it
// does not lift the volume finalizer. It deletes a session's volume only when a
// deleted session is archived, after a verified rescue of the volume's last pod.
// deletePod and deleteVolume are the only places in this package that delete
// anything, besides the HOME expiry's three exceptions (DESIGN-025 5.2, 6.1,
// 6.5): deleteExpiryExecutor, the ended executor of a private Session in
// Rescuing, deleteExpiryHold, the expiry's own hold pod, and deleteExpiryHome,
// a private HOME in Deleting after its archive verified again. Each deletes by
// UID; releaseSession, releaseVolume, releaseExpiryHome and a few named
// release helpers are the only ones that lift a finalizer or rewrite owner
// references, and releaseSession lifts a private Session's only for an
// Expired record (expiryReleaseAllowed). A test (TestOnlyTheGuardDeletes)
// keeps it that way.

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

// errDrainNeedsRestart is why a Draining session keeps its pod: prepare-restart
// has not recorded the conversation this pod resumes (DESIGN-001 5.2).
var errDrainNeedsRestart = errors.New("the pod stays until prepare-restart has recorded the conversation this pod resumes (DESIGN-001 5.2)")

// errSummonedNotDrained is why a summoned session is never rolled onto a new
// revision (DESIGN-001 3.7, V-13, 5.2).
var errSummonedNotDrained = errors.New("a summoned session is never drained; it keeps the revision it started on (DESIGN-001 3.7, 5.2)")

// wantsPodGone reports whether the session asks for its pod to go: suspended, or
// deleted (a reap, which suspends first).
func wantsPodGone(s *v1alpha1.AgentSession) bool {
	return s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero()
}

// podRemovalAllowed is nil only when 5.1 allows deleting the session's pod now:
// in the Suspended transition (asked by a suspend or a delete), when no agent
// process runs in the pod or a rescue ran in it since the session last changed,
// or in the Draining transition (DESIGN-001 5.2) when the session still wants
// a pod and prepare-restart has recorded the conversation for this pod. A
// suspend or a delete still needs its rescue; a stale Draining phase does not
// skip that. A pod runs no agent when it never started (never scheduled, or
// its containers never ran) or when it ended; then no rescue can run in it,
// and none is needed for the pod, because the volume stays and archive asks
// for its own rescue. Every other state keeps the pod, whatever else asks.
//
// A hold pod (D-55) runs no agent, so holdRemovalAllowed judges it instead.
// Private history protection retains the original pod regardless of legacy
// rescue or process state; neither is private stop or preservation proof.
// A summoned session is not drained (DESIGN-001 3.7, 5.2).
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
	case s.Status.Phase == v1alpha1.PhaseDraining && !wantsPodGone(s):
		// Drain deletes this pod only. The volume finalizer stays, and the
		// next ensure starts a pod on the current revision (DESIGN-001 5.2).
		if summonedSession(s) {
			return errSummonedNotDrained
		}
		if restartProofFor(s, pod) {
			return nil
		}
		return errDrainNeedsRestart
	case !wantsPodGone(s):
		return fmt.Errorf("the session is neither draining nor suspending (operatingMode %s); its pod stays (DESIGN-001 5.1)", s.Spec.OperatingMode)
	case podNeverStarted(pod), originalHomeAgentNeverStarted(s, pod), podEnded(pod), rescueRanIn(s, pod):
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

// originalHomeAgentNeverStarted lets suspend remove an owned executor stuck
// in the read-only verifier, still running or failed, never succeeded. It
// proves nothing about earlier Pods' HOME data:
// archive and its rescue requirements remain separate. deletePod still pins
// the observed Pod UID and resourceVersion, including these container states.
func originalHomeAgentNeverStarted(s *v1alpha1.AgentSession, p *corev1.Pod) bool {
	if s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero() ||
		p.Name != s.Name || p.Namespace != s.Namespace || p.UID == "" || p.ResourceVersion == "" ||
		!controlledBySession(p, s) || isHoldPod(p) || p.Status.Phase != corev1.PodPending {
		return false
	}
	b, err := homeguard.Select(nil, s.Namespace, s.Name, string(s.UID), s.Annotations)
	if err != nil || b == nil || len(p.Spec.Containers) == 0 ||
		len(p.Status.ContainerStatuses) != len(p.Spec.Containers) ||
		len(p.Spec.InitContainers) != 1 || len(p.Status.InitContainerStatuses) != 1 ||
		len(p.Spec.EphemeralContainers) != 0 || len(p.Status.EphemeralContainerStatuses) != 0 {
		return false
	}
	// Require the operator's verifier command, rather than trusting an init
	// container's name alone. Admission also adds read-only token mounts.
	init := p.Spec.InitContainers[0]
	if init.Name != "original-home-verify" || p.Status.InitContainerStatuses[0].Name != init.Name ||
		!slices.Equal(init.Command, []string{"/usr/local/bin/agentd"}) || !slices.Equal(init.Args, []string{"home-verify"}) ||
		init.RestartPolicy != nil || init.Lifecycle != nil || len(init.VolumeDevices) != 0 {
		return false
	}
	// The verifier must be the agent image with the same security context,
	// and explicitly unable to escalate or add capabilities. A changed or
	// uncertain verifier cannot establish that HOME was only read.
	agent := slices.IndexFunc(p.Spec.Containers, func(c corev1.Container) bool { return c.Name == ContainerName })
	security := init.SecurityContext
	if agent < 0 || init.Image == "" || init.Image != p.Spec.Containers[agent].Image || security == nil ||
		!reflect.DeepEqual(security, p.Spec.Containers[agent].SecurityContext) ||
		security.Privileged != nil && *security.Privileged ||
		security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
		security.Capabilities != nil && len(security.Capabilities.Add) != 0 {
		return false
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == corev1.PodInitialized && condition.Status == corev1.ConditionTrue {
			return false
		}
	}
	status := p.Status.InitContainerStatuses[0]
	if status.Ready {
		return false
	}
	// A successful verifier can release main boot before its status becomes
	// visible. Only an exclusive running or failed init state is evidence
	// that main is still gated; Waiting alone is not failure evidence. The
	// kubelet keeps only the latest earlier termination: in every branch it
	// must be absent or an exclusive failure, never a visible success.
	last := status.LastTerminationState
	lastEmpty := last.Running == nil && last.Waiting == nil && last.Terminated == nil
	lastFailed := last.Terminated != nil && last.Terminated.ExitCode != 0 && last.Running == nil && last.Waiting == nil
	switch state := status.State; {
	case state.Running != nil && state.Waiting == nil && state.Terminated == nil:
		if !lastEmpty && !lastFailed {
			return false
		}
	case state.Waiting != nil && state.Running == nil && state.Terminated == nil:
		if !lastFailed {
			return false
		}
	case state.Terminated != nil && state.Running == nil && state.Waiting == nil:
		if state.Terminated.ExitCode == 0 || !lastEmpty && !lastFailed {
			return false
		}
	default:
		return false
	}
	home := false
	for _, mount := range init.VolumeMounts {
		if !mount.ReadOnly {
			return false
		}
		if mount.Name == "home" && mount.MountPath == "/home/dev" && mount.SubPath == "" && mount.SubPathExpr == "" {
			if home {
				return false
			}
			home = true
		}
	}
	if !home {
		return false
	}
	// Match by name as well as count: duplicate or unknown status entries
	// must not hide a main container that might have run.
	waiting := make(map[string]bool, len(p.Status.ContainerStatuses))
	for _, cs := range p.Status.ContainerStatuses {
		if waiting[cs.Name] || cs.Ready || cs.Started != nil && *cs.Started || cs.ContainerID != "" ||
			cs.State.Waiting == nil || cs.State.Running != nil || cs.State.Terminated != nil ||
			cs.RestartCount != 0 || cs.LastTerminationState.Terminated != nil || cs.LastTerminationState.Running != nil {
			return false
		}
		waiting[cs.Name] = true
	}
	for _, c := range p.Spec.Containers {
		if !waiting[c.Name] {
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
	case podNeverStarted(pod) || originalHomeAgentNeverStarted(s, pod) || podEnded(pod):
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
	if _, exists := s.Annotations[homeguard.Annotation]; exists {
		return homeguard.ErrUnsafe
	}
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
	// holdReleaseStaleImage: the export refused for a reason only a source
	// change clears, and the templates name another agent image than the hold
	// pod runs; a hold pod of the new image re-checks it.
	holdReleaseStaleImage expiryHoldRelease = "the export needs a source change and the hold pod runs an agent image the templates no longer name"
)

// expiryHoldEvidence is what the HOME expiry's hold pod guard judges a
// release by: why the caller says the hold pod is no longer needed, and the
// reads that must show it. The guard checks the reason against them itself.
type expiryHoldEvidence struct {
	why expiryHoldRelease
	// view is the reconcile's uncached reads (the Session, the Pod of its
	// name, the Pods that mount the HOME); a cancel is judged from it.
	view *expiryView
	// templates is the archive window's source, for a cancel because the
	// Session is no longer triggered; nil when the templates do not load.
	templates *templates.Templates
	// archiveOn is the operator's --enable-home-history-archive (with the
	// terminal record), for archive off.
	archiveOn bool
}

// expiryHoldRemovalAllowed is nil only when the HOME expiry may delete its own
// hold pod (DESIGN-025 6.1, ADR-006's fifth D-45 exception): the Pod carries
// the hold label, the Session controls it, its UID is the expiry record's
// holdPodUID, its three annotations match the record, and the expiry no longer
// needs it, which the guard checks from the evidence: a cancel needs a view
// that shows the Session no longer triggered or a new Pod holding the HOME;
// archive off needs the archive flag off; an owner hold needs an override
// that holds (any value but release-now, homeExpiryHeld); Archived needs the
// record at Archived or later; ended needs the hold pod in a terminal phase;
// a stale image needs the record before Archived with a refusal only a
// source change clears, and loaded templates that name another agent image
// than the hold pod runs. podRemovalAllowed refuses every Pod of a
// private Session, and holdRemovalAllowed would keep a Running, unrescued hold
// pod, so neither can remove it.
func expiryHoldRemovalAllowed(pod *corev1.Pod, ev expiryHoldEvidence) error {
	if ev.view == nil || ev.view.s == nil {
		return errors.New("the HOME expiry's hold pod guard has no uncached reads to judge by")
	}
	s := ev.view.s
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
	switch ev.why {
	case holdReleaseCancel:
		trig, _ := homeExpiryTriggered(s, ev.templates, ev.view.now)
		if !expiryPhaseAtLeast(h.Expiry.Phase, v1alpha1.HomeExpiryDeleting) && (trig == expiryNotTriggered || ev.view.newPod() != nil) {
			return nil
		}
	case holdReleaseArchiveOff:
		if !ev.archiveOn {
			return nil
		}
	case holdReleaseOwnerHold:
		if homeExpiryHeld(s) {
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
	case holdReleaseStaleImage:
		if !expiryPhaseAtLeast(h.Expiry.Phase, v1alpha1.HomeExpiryArchived) && sourceChangeRefusal(h.Expiry.Refusal) && staleHoldImage(pod, ev.templates) {
			return nil
		}
	}
	return fmt.Errorf("the HOME expiry still needs its hold pod: the reads do not show that %s", ev.why)
}

// deleteExpiryHold deletes the HOME expiry's hold pod if the guard allows it,
// with the Pod's UID and resourceVersion as preconditions.
func deleteExpiryHold(ctx context.Context, c client.Client, pod *corev1.Pod, ev expiryHoldEvidence) error {
	if err := expiryHoldRemovalAllowed(pod, ev); err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}))
}

// expiryVolumeRemovalAllowed is nil only when the HOME expiry may delete a
// private Session's HOME claim, or lift the operator's finalizer from it
// (DESIGN-025 6.5; ADR-005's D-45 exception): the Session is private and no
// canary, its retention identity holds, its HOME was bound, and its expiry
// record is Deleting, rests on the confirmed terminal record of the recorded
// executor, names the bound HOME and holds the archive it verified; the claim
// is that HOME claim (its name and UID), the Session controls it, and it has
// the resourceVersion the delete's precondition carries. A Session that is
// not Reaping must have status.archivedAt, written with Deleting, so it can
// never be resumed onto a new HOME (D-62). The caller runs the archive's
// verify and the Pod checks in the same call path first (removeExpiredHome).
func expiryVolumeRemovalAllowed(s *v1alpha1.AgentSession, claim *corev1.PersistentVolumeClaim) error {
	if _, exists := s.Annotations[homeguard.Annotation]; exists {
		return homeguard.ErrUnsafe
	}
	h := s.Status.PrivateHistory
	switch {
	case s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil:
		return errors.New("only a private Session's HOME expires; canary originals and shared Sessions never do (DESIGN-025 2)")
	case privateHistoryIdentity(s) != nil:
		return privateHistoryIdentity(s)
	case h.Admission != sharedStarted || h.HomeUID == "":
		return errors.New("the private HOME was never bound")
	case h.Expiry == nil || h.Expiry.Phase != v1alpha1.HomeExpiryDeleting || h.Expiry.DeletingAt == nil:
		return errors.New("the HOME expiry deletes a HOME only once Deleting is recorded (DESIGN-025 6.5)")
	case h.Expiry.Archive == nil || !protocol.ValidSHA256(h.Expiry.Archive.ContentSHA256) || h.Expiry.Archive.Entry == "":
		return errors.New("the expiry record holds no archive of the history")
	case !executorTerminalRecorded(h) || h.Expiry.ExecutorUID != h.ExecutorTerminal.PodUID || h.Expiry.HomeUID != h.HomeUID:
		return errors.New("the expiry record rests on no confirmed terminal record of the recorded executor, or names another HOME")
	case claim == nil || claim.UID == "" || string(claim.UID) != h.HomeUID || claim.Name != h.HomeClaim || claim.ResourceVersion == "":
		return errors.New("the claim is not the bound HOME claim the expiry record names")
	case !controlledBySessionObject(claim, s):
		return errors.New("the Session does not control the HOME claim")
	case s.DeletionTimestamp.IsZero() && s.Status.ArchivedAt == nil:
		return errArchiveNotRecorded
	}
	return nil
}

// deleteExpiryHome deletes the HOME claim if the expiry's guard allows it, with
// the claim's UID and resourceVersion as preconditions, then lifts the
// operator's finalizer from a fresh uncached read of it (releaseExpiryHome). A
// claim already deleting (an earlier attempt's delete) only has its finalizer
// lifted. Its one caller, removeExpiredHome, verifies the archive and checks
// for Pods first, on every attempt (TestExpiryDeleteNeedsVerify).
func deleteExpiryHome(ctx context.Context, c client.Client, live client.Reader, s *v1alpha1.AgentSession, claim *corev1.PersistentVolumeClaim) error {
	if err := expiryVolumeRemovalAllowed(s, claim); err != nil {
		return err
	}
	if claim.DeletionTimestamp.IsZero() {
		if err := c.Delete(ctx, claim, client.Preconditions{UID: &claim.UID, ResourceVersion: &claim.ResourceVersion}); err != nil {
			return client.IgnoreNotFound(err)
		}
	}
	var fresh corev1.PersistentVolumeClaim
	if err := live.Get(ctx, client.ObjectKeyFromObject(claim), &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}
	return releaseExpiryHome(ctx, c, s, &fresh)
}

// releaseExpiryHome lifts the operator's finalizer from a HOME claim the
// expiry deleted, by a patch that carries the resourceVersion of the caller's
// fresh read, so a claim changed since, or another object of its name, is
// never released. Only deleteExpiryHome calls it.
func releaseExpiryHome(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, claim *corev1.PersistentVolumeClaim) error {
	if err := expiryVolumeRemovalAllowed(s, claim); err != nil {
		return err
	}
	if claim.DeletionTimestamp.IsZero() {
		return errors.New("the HOME claim is not being deleted, so its finalizer stays")
	}
	if !controllerutil.ContainsFinalizer(claim, Finalizer) {
		return nil
	}
	orig := claim.DeepCopy()
	controllerutil.RemoveFinalizer(claim, Finalizer)
	return client.IgnoreNotFound(c.Patch(ctx, claim, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})))
}

// expiryReleaseAllowed is nil only for the one protected Session that
// releaseIfEmpty and releaseSession may let go (DESIGN-025 6.6): a Reaping
// private Session, no canary, whose expiry record is Expired for its bound
// HOME. The caller confirms by uncached reads that no Pod and no claim of its
// names is left.
func expiryReleaseAllowed(s *v1alpha1.AgentSession) error {
	h := s.Status.PrivateHistory
	switch {
	case s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil:
		return errors.New("only a private Session is released by its HOME expiry; canary originals never are")
	case s.DeletionTimestamp.IsZero():
		return errors.New("only a reaped Session is released")
	case privateHistoryIdentity(s) != nil:
		return privateHistoryIdentity(s)
	case h.HomeUID == "" || h.Expiry == nil || h.Expiry.Phase != v1alpha1.HomeExpiryExpired || h.Expiry.HomeDeletedAt == nil || h.Expiry.HomeUID != h.HomeUID:
		return errors.New("the HOME expiry record is not Expired for the bound HOME")
	}
	return nil
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
