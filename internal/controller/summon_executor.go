package controller

// The remote executor latch (DESIGN-064 5.9, F3, OP780, slice d of item 741).
// A summoned remote session has no private task latch, so it gets the same
// shape in status.summon.executor:
//
//   - Before its one Pod create, ensure records the attempt and an intent,
//     confirmed by an uncached re-read, under the optimistic lock of the read
//     that checked the session is admitted and open (reserveSummonExecutor),
//     as reservePrivateExecutor does for a task. A close that lands after
//     the gate's read conflicts with that write, so no Pod is created.
//   - Once the pod appears with that intent, its UID is recorded once
//     (observeSummonExecutor). It never changes.
//   - A known non-attempt (DESIGN-019: ensure returned before calling Create
//     because an observation failed) re-arms the same intent once per mark
//     (recordSummonNotCreated). Any other attempt is final: a recreated pod
//     after an eviction, a resume, an image move or a lost create response
//     never gets a second pod (summonBuildRefusal).
//   - The leader's watchdog reads the recorded pod uncached: gone, or another
//     UID under its name, is session-lost (summonWatchdogInputs).
//
// agentd's own half is in internal/agentd: a summoned session with a launch
// record starts nothing on boot, and run-agent refuses a spawn past the start
// deadline. podCarriesStartFence tells release which pods carry that fence.
//
// Every branch keys on spec.caller and remote mode, so an ordinary session is
// built exactly as before.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// summonExecutorAnnotation carries the remote latch's intent on the pod its
// one create made, so the observation records only that pod's UID.
const summonExecutorAnnotation = v1alpha1.LabelPrefix + "summon-executor-intent"

// summonStartFenceMinor is the first agent release, 2.<minor>.0, that carries
// story 780's start fence: run-agent refuses a summoned provider spawn past
// the start deadline, and agentd refuses every boot of a summoned session
// that has a launch record (coordinator answer op780-q-1). Only a pod whose
// agent image is that release or later may have its reservation released at
// the start deadline plus one minute with a launch possibly in flight; any
// other pod holds until it is gone. It is right only while no agent release
// at or above it was cut before this code merged: the coordinator checks
// that at merge, and raises it here if not.
const summonStartFenceMinor = 108

// releasedAgentImage is a released agent image as the templates pin it.
var releasedAgentImage = regexp.MustCompile(`^ghcr\.io/thaynes43/dev-env:2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)@sha256:[a-f0-9]{64}$`)

// podCarriesStartFence reports whether the pod's agent container runs a
// released agent image at or above summonStartFenceMinor. The image is the
// one the operator put in the pod (a summoned pod always runs the templates
// image, sessionPod), which the session cannot change. A digest-only image, a
// sha- tag, an unreleased tag or a missing container carries no fence.
func podCarriesStartFence(p *corev1.Pod) bool {
	if p == nil {
		return false
	}
	for _, c := range p.Spec.Containers {
		if c.Name != ContainerName {
			continue
		}
		m := releasedAgentImage.FindStringSubmatch(c.Image)
		if m == nil {
			return false
		}
		minor, err := strconv.Atoi(m[1])
		return err == nil && minor >= summonStartFenceMinor
	}
	return false
}

// summonRemoteLatched reports whether a session uses the remote executor
// latch: a summoned remote session. A summoned task uses the private task
// latch (status.privateHistory) instead.
func summonRemoteLatched(s *v1alpha1.AgentSession) bool {
	return s.Spec.Caller != "" && s.Spec.Mode == v1alpha1.ModeRemote && !privateHistoryProtected(s)
}

// summonExecutorAttempted reports whether the remote latch forbids another
// pod: it records an attempt that no rule lets re-arm (any attempt but a
// known non-attempt with no recorded pod), or it is absent on a session that
// already had a pod or an agent. Only a session admitted before this latch
// existed, or written past the operator, could be the second: it never gets
// another pod either.
func summonExecutorAttempted(s *v1alpha1.AgentSession) bool {
	st := s.Status.Summon
	if st == nil || st.Executor == nil {
		return s.Status.PodName != "" || s.Status.Agent != nil || s.Status.Rescue != nil
	}
	ex := st.Executor
	return ex.Attempted && (!ex.NotCreated || ex.PodUID != "")
}

// summonExecutorSession is the uncached read every latch write is locked on.
func (r *Reconciler) summonExecutorSession(ctx context.Context, s *v1alpha1.AgentSession) (*v1alpha1.AgentSession, error) {
	if r.APIReader == nil {
		return nil, errors.New("the summoned executor latch requires the uncached API reader")
	}
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		return nil, err
	}
	if fresh.UID == "" || fresh.UID != s.UID {
		return nil, errors.New("summoned executor latch: the Session was replaced")
	}
	return &fresh, nil
}

// writeSummonExecutor writes fresh's status under its resourceVersion and
// confirms by an uncached re-read that the stored latch is the one intended,
// as writePrivateHistory does. errPrivateHistoryUnacknowledged wraps a write
// the API server did not acknowledge.
func (r *Reconciler) writeSummonExecutor(ctx context.Context, s, fresh *v1alpha1.AgentSession) error {
	intended := fresh.Status.Summon.Executor.DeepCopy()
	if err := r.Client.Status().Update(ctx, fresh); err != nil {
		return fmt.Errorf("%w: %w", errPrivateHistoryUnacknowledged, err)
	}
	confirmed, err := r.summonExecutorSession(ctx, fresh)
	if err != nil {
		return err
	}
	if confirmed.ResourceVersion != fresh.ResourceVersion || confirmed.Status.Summon == nil ||
		!apiequality.Semantic.DeepEqual(confirmed.Status.Summon.Executor, intended) {
		return errors.New("summoned executor latch acknowledgement changed, dropped a field or is unconfirmed")
	}
	*s = *confirmed
	return nil
}

// reserveSummonExecutor records the remote latch's attempt before the one
// Pod create, or re-arms a known non-attempt's same intent. It refuses unless
// the uncached session is admitted, open and not stopping, its latch records
// no final attempt and no pod exists under its name.
func (r *Reconciler) reserveSummonExecutor(ctx context.Context, s *v1alpha1.AgentSession) (executorReservation, error) {
	fresh, err := r.summonExecutorSession(ctx, s)
	if err != nil {
		return executorReservation{}, err
	}
	if wantsPodGone(fresh) || summonExecutorAttempted(fresh) {
		return executorReservation{}, errors.New("summoned executor attempt is already recorded or the Session stopped")
	}
	// This read is the one the latch write below is locked on, so a close
	// that lands after the gate's read conflicts here and no Pod is created.
	if why := summonBuildRefusal(fresh, r.summonTaskLatchOn()); why != "" {
		return executorReservation{}, fmt.Errorf("summoned executor is refused: %s", why)
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(fresh), &pod); !apierrors.IsNotFound(err) {
		return executorReservation{}, errors.New("summoned executor absence is unconfirmed before its attempt")
	}
	if fresh.Status.Summon.Executor == nil {
		fresh.Status.Summon.Executor = &v1alpha1.SummonExecutorStatus{Attempted: true, Intent: string(uuid.NewUUID())}
	} else {
		fresh.Status.Summon.Executor.NotCreated = false // the one re-arm of this mark
	}
	intent := fresh.Status.Summon.Executor.Intent
	if err := r.writeSummonExecutor(ctx, s, fresh); err != nil {
		if errors.Is(err, errPrivateHistoryUnacknowledged) {
			return executorReservation{}, err
		}
		return executorReservation{intent: intent, resourceVersion: fresh.ResourceVersion, summon: true}, err
	}
	return executorReservation{intent: intent, resourceVersion: s.ResourceVersion, summon: true}, nil
}

// recordSummonNotCreated marks the reserved intent as a known non-attempt
// (DESIGN-019), only over the reservation's own write, as
// recordExecutorNotCreated does for a task. Best effort: when the mark cannot
// be written and confirmed, the attempt stays latched.
func (r *Reconciler) recordSummonNotCreated(ctx context.Context, s *v1alpha1.AgentSession, res executorReservation) {
	if res.intent == "" || res.resourceVersion == "" {
		return
	}
	fresh, err := r.summonExecutorSession(ctx, s)
	if err != nil || fresh.ResourceVersion != res.resourceVersion {
		log.FromContext(ctx).Info("the summoned executor attempt stays latched: the Session changed after this reconcile's own latch write", "intent", res.intent)
		return
	}
	st := fresh.Status.Summon
	if st == nil || st.Executor == nil || !st.Executor.Attempted || st.Executor.Intent != res.intent || st.Executor.NotCreated || st.Executor.PodUID != "" {
		return
	}
	st.Executor.NotCreated = true
	if err := r.writeSummonExecutor(ctx, s, fresh); err != nil {
		log.FromContext(ctx).Info("the summoned executor attempt stays latched: its non-attempt record is unconfirmed", "error", err.Error())
		return
	}
	log.FromContext(ctx).Info("recorded a known non-attempt: no Pod create was called for the summoned executor intent", "intent", res.intent)
}

// observeSummonExecutor records, once, the UID of the pod the latch's one
// create made: an uncached read finds a pod the session controls under its
// name carrying the latch's intent. An observed pod wins over a non-attempt
// mark. A hold pod, a foreign pod or one with another intent is never
// recorded. It reports whether it wrote.
func (r *Reconciler) observeSummonExecutor(ctx context.Context, s *v1alpha1.AgentSession) (bool, error) {
	if !summonRemoteLatched(s) {
		return false, nil
	}
	if st := s.Status.Summon; st == nil || st.Executor == nil || !st.Executor.Attempted || st.Executor.PodUID != "" {
		return false, nil
	}
	fresh, err := r.summonExecutorSession(ctx, s)
	if err != nil {
		return false, client.IgnoreNotFound(err)
	}
	ex := fresh.Status.Summon.Executor
	if ex == nil || !ex.Attempted || ex.PodUID != "" {
		return false, nil
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(fresh), &pod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if !controlledBySession(&pod, fresh) || pod.UID == "" || isHoldPod(&pod) || pod.Annotations[summonExecutorAnnotation] != ex.Intent {
		return false, nil
	}
	ex.PodUID, ex.NotCreated = string(pod.UID), false
	if err := r.writeSummonExecutor(ctx, s, fresh); err != nil {
		if apierrors.IsConflict(err) {
			return true, nil
		}
		return false, err
	}
	log.FromContext(ctx).Info("recorded the summoned executor's pod", "session", s.Name, "podUID", ex.PodUID)
	return true, nil
}
