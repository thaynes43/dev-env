package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	ConditionPrivateHistory           = "PrivateHistoryRetained"
	privateHistoryAdmissionAnnotation = v1alpha1.LabelPrefix + "private-history-generation"
	privateHistoryExecutorAnnotation  = v1alpha1.LabelPrefix + "private-history-executor-intent"
	privateHistoryHomeAnnotation      = v1alpha1.LabelPrefix + "private-history-home-uid"
)

var errPrivateHistoryRetained = errors.New("private provider HOME and durable Session identity remain retained; this slice authorizes no archive or finalizer release")

// The persisted latch remains authoritative across operator reconstruction and
// flag changes. An invalid record is still protected, never a legacy fallback.
func privateHistoryProtected(s *v1alpha1.AgentSession) bool {
	// The immutable canary binding protects its original HOME even when a
	// status read/schema is uncertain or the global retention flag is disabled.
	return s.Spec.ProjectCanary != nil || s.Spec.Workspace == nil && s.Status.PrivateHistory != nil
}

func (r *Reconciler) privateHistoryEnabled(s *v1alpha1.AgentSession) bool {
	return s.Spec.Workspace == nil && (r.PrivateHistoryRetention || privateHistoryProtected(s))
}

func (r *Reconciler) privateHistorySession(ctx context.Context, s *v1alpha1.AgentSession) (*v1alpha1.AgentSession, error) {
	if r.APIReader == nil {
		return nil, errors.New("private retention requires an uncached Session reader")
	}
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		return nil, err
	}
	if fresh.UID == "" || fresh.UID != s.UID || fresh.Generation != s.Generation || fresh.Spec.Workspace != nil {
		return nil, errors.New("private retention Session identity or generation changed")
	}
	return &fresh, nil
}

// errPrivateHistoryUnacknowledged wraps a status write the API server did not
// acknowledge. A Conflict proves the write did not land; any other error leaves
// its result unknown.
var errPrivateHistoryUnacknowledged = errors.New("private retention write is unconfirmed")

// A status write must be confirmed independently before any resource write.
// The confirmation compares the stored record with the record this call meant
// to write, not with the server's response: Update copies the response into
// fresh, so a field a stale CRD schema prunes would otherwise confirm as written.
func (r *Reconciler) writePrivateHistory(ctx context.Context, s, fresh *v1alpha1.AgentSession) error {
	_, err := r.writePrivateHistoryAcknowledged(ctx, s, fresh)
	return err
}

// writePrivateHistoryAcknowledged is writePrivateHistory that also reports
// whether the API server acknowledged the update with the intended record: its
// response, which Update copies into fresh, holds exactly that record. Only
// the confirming re-read lets the caller act on the record. The acknowledgement
// says the record was stored, even when another write to the Session lands
// before the re-read and the confirmation fails; the terminal counter counts
// from it (DESIGN-025 3.6).
func (r *Reconciler) writePrivateHistoryAcknowledged(ctx context.Context, s, fresh *v1alpha1.AgentSession) (acknowledged bool, err error) {
	intended := fresh.Status.PrivateHistory.DeepCopy()
	if err := r.Client.Status().Update(ctx, fresh); err != nil {
		return false, fmt.Errorf("%w: %w", errPrivateHistoryUnacknowledged, err)
	}
	acknowledged = apiequality.Semantic.DeepEqual(fresh.Status.PrivateHistory, intended)
	confirmed, err := r.privateHistorySession(ctx, fresh)
	if err != nil {
		return acknowledged, err
	}
	if confirmed.ResourceVersion != fresh.ResourceVersion || !apiequality.Semantic.DeepEqual(confirmed.Status.PrivateHistory, intended) {
		return acknowledged, errors.New("private retention acknowledgement changed, dropped a field or is unconfirmed")
	}
	*s = *confirmed
	return acknowledged, nil
}

func (r *Reconciler) privateResourcesAbsent(ctx context.Context, s *v1alpha1.AgentSession) error {
	for _, obj := range []client.Object{&corev1.Pod{}, &corev1.PersistentVolumeClaim{}} {
		name := s.Name
		if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
			name = HomeClaimName(s.Name)
		}
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, obj)
		if !apierrors.IsNotFound(err) {
			return errors.New("fresh private resource absence is unconfirmed")
		}
	}
	return nil
}

func (r *Reconciler) initializePrivateHistory(ctx context.Context, s *v1alpha1.AgentSession) error {
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		return err
	}
	if fresh.Status.PrivateHistory != nil {
		*s = *fresh
		return nil
	}
	admission := "Unknown"
	// No historical lifecycle gets a newly guessed original HOME binding.
	if fresh.DeletionTimestamp.IsZero() && !wantsPodGone(fresh) && !controllerutil.ContainsFinalizer(fresh, Finalizer) &&
		sharedNoResourceHistory(fresh) && fresh.Status.Phase == "" && fresh.Status.SuspendedAt == nil && len(fresh.Status.Conditions) == 0 &&
		r.privateResourcesAbsent(ctx, fresh) == nil {
		admission = sharedNeverStarted
	}
	fresh.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(fresh.UID),
		HomeClaim: HomeClaimName(fresh.Name), BindingGeneration: fresh.Generation, Admission: admission,
		Provider: string(fresh.Spec.Agent), StopState: "Unknown"}
	return r.writePrivateHistory(ctx, s, fresh)
}

func privateHistoryIdentity(s *v1alpha1.AgentSession) error {
	h := s.Status.PrivateHistory
	if !privateHistoryProtected(s) || h == nil || h.Version != 1 || h.SessionUID != string(s.UID) || h.HomeClaim != HomeClaimName(s.Name) ||
		h.BindingGeneration < 1 || h.Provider != string(s.Spec.Agent) || h.StopState != "Unknown" || !controllerutil.ContainsFinalizer(s, Finalizer) {
		return errors.New("private retention identity or protective Session finalizer is unconfirmed")
	}
	return nil
}

func (r *Reconciler) admitPrivateHistory(ctx context.Context, s *v1alpha1.AgentSession) error {
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		return err
	}
	if err := privateHistoryIdentity(fresh); err != nil {
		return err
	}
	h := fresh.Status.PrivateHistory
	// A recorded known non-attempt may resume its same intent (DESIGN-019).
	if wantsPodGone(fresh) || h.Admission == "Unknown" || h.ExecutorAttempted && (!h.ExecutorNotCreated || h.LastExecutor != nil) {
		return errors.New("private executor admission is unknown or stopped; original HOME is not inferred")
	}
	if h.Admission == sharedNeverStarted {
		if err := r.privateResourcesAbsent(ctx, fresh); err != nil {
			return err
		}
		h.Admission = sharedStarted
		return r.writePrivateHistory(ctx, s, fresh)
	}
	if h.Admission != sharedStarted {
		return errors.New("private resource admission is inconsistent")
	}
	if h.HomeUID != "" {
		if _, err := r.privateHistoryHome(ctx, fresh); err != nil {
			return err
		}
	}
	*s = *fresh
	return nil
}

func (r *Reconciler) privateHistoryHome(ctx context.Context, s *v1alpha1.AgentSession) (*corev1.PersistentVolumeClaim, error) {
	if err := privateHistoryIdentity(s); err != nil {
		return nil, err
	}
	h := s.Status.PrivateHistory
	var home corev1.PersistentVolumeClaim
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: h.HomeClaim}, &home); err != nil {
		return nil, errors.New("original private HOME is missing or its observation is unconfirmed")
	}
	if h.Admission != sharedStarted || home.UID == "" || h.HomeUID == "" || string(home.UID) != h.HomeUID ||
		!home.DeletionTimestamp.IsZero() || !controlledBySessionObject(&home, s) || !controllerutil.ContainsFinalizer(&home, Finalizer) ||
		home.Annotations[privateHistoryAdmissionAnnotation] != strconv.FormatInt(h.BindingGeneration, 10) {
		return nil, errors.New("original private HOME binding, owner or protective finalizer is unconfirmed, replaced or deleting")
	}
	return &home, nil
}

func (r *Reconciler) bindPrivateHistoryHome(ctx context.Context, s *v1alpha1.AgentSession) error {
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		return err
	}
	if err := privateHistoryIdentity(fresh); err != nil {
		return err
	}
	h := fresh.Status.PrivateHistory
	if wantsPodGone(fresh) || h.Admission != sharedStarted {
		return errors.New("private Session changed before original HOME binding")
	}
	if h.HomeUID == "" {
		var pod corev1.Pod
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(fresh), &pod); !apierrors.IsNotFound(err) || fresh.Status.PodName != "" || fresh.Status.Rescue != nil || fresh.Status.Agent != nil {
			return errors.New("existing private executor/history has no original HOME binding; migration is refused")
		}
		var home corev1.PersistentVolumeClaim
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: fresh.Namespace, Name: h.HomeClaim}, &home); err != nil {
			return err
		}
		if home.UID == "" || !home.DeletionTimestamp.IsZero() || !controlledBySessionObject(&home, fresh) ||
			!controllerutil.ContainsFinalizer(&home, Finalizer) || home.Annotations[privateHistoryAdmissionAnnotation] != strconv.FormatInt(h.BindingGeneration, 10) {
			return errors.New("fresh original HOME admission is unconfirmed")
		}
		h.HomeUID = string(home.UID)
		if err := r.writePrivateHistory(ctx, s, fresh); err != nil {
			return err
		}
		fresh = s
	}
	if _, err := r.privateHistoryHome(ctx, fresh); err != nil {
		return err
	}
	*s = *fresh
	return nil
}

// executorReservation is the executor latch one reconcile wrote: its intent and
// the Session resourceVersion that its own acknowledged write produced. The
// zero value means this reconcile has no write of its own to mark.
type executorReservation struct {
	intent          string
	resourceVersion string
}

// Record the attempt before Pod creation. A failed/lost create is uncertain,
// and cannot be retried as an implicit new provider execution by this slice.
// A recorded known non-attempt (executorNotCreated) re-arms its same intent
// instead: the mark is cleared by a confirmed write before the one Create.
// It returns the latch it wrote, so a caller that returns before Create can
// record the non-attempt over exactly that write and nothing later.
func (r *Reconciler) reservePrivateExecutor(ctx context.Context, s *v1alpha1.AgentSession) (executorReservation, error) {
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		return executorReservation{}, err
	}
	h := fresh.Status.PrivateHistory
	if wantsPodGone(fresh) || h == nil || h.ExecutorAttempted && (!h.ExecutorNotCreated || h.ExecutorIntent == "" || h.LastExecutor != nil) {
		return executorReservation{}, errors.New("private executor attempt is already recorded or Session stopped")
	}
	if _, err := r.privateHistoryHome(ctx, fresh); err != nil {
		return executorReservation{}, err
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(fresh), &pod); !apierrors.IsNotFound(err) {
		return executorReservation{}, errors.New("private executor absence is unconfirmed before first attempt")
	}
	if h.ExecutorAttempted {
		h.ExecutorNotCreated = false
	} else {
		h.ExecutorAttempted = true
		h.ExecutorIntent = string(uuid.NewUUID())
	}
	intent := h.ExecutorIntent
	if err := r.writePrivateHistory(ctx, s, fresh); err != nil {
		if errors.Is(err, errPrivateHistoryUnacknowledged) {
			// A Conflict proves this write did not land: another reconciler
			// may have written the same latch and be creating its Pod. Any
			// other unacknowledged error leaves the result unknown. Either
			// way this reconcile has no write of its own to mark.
			return executorReservation{}, err
		}
		// The server acknowledged the write (Update copied its response into
		// fresh); only the confirmation failed.
		return executorReservation{intent: intent, resourceVersion: fresh.ResourceVersion}, err
	}
	return executorReservation{intent: intent, resourceVersion: s.ResourceVersion}, nil
}

// recordExecutorNotCreated marks the reserved intent as a known non-attempt
// (DESIGN-019). Call it only when this reconcile returned before calling Create
// for that intent because an observation failed. It writes only over the
// reservation's own write: when the Session's resourceVersion has moved since,
// another write may have re-armed the same intent, so the mark could cover a
// Create in flight. It is best effort: when the mark cannot be written and
// confirmed, the attempt stays latched, as it was before.
func (r *Reconciler) recordExecutorNotCreated(ctx context.Context, s *v1alpha1.AgentSession, res executorReservation) {
	if res.intent == "" || res.resourceVersion == "" {
		return
	}
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		log.FromContext(ctx).Info("the private executor attempt stays latched: the Session read for its non-attempt record failed", "error", err.Error())
		return
	}
	if fresh.ResourceVersion != res.resourceVersion {
		log.FromContext(ctx).Info("the private executor attempt stays latched: the Session changed after this reconcile's own latch write", "intent", res.intent)
		return
	}
	h := fresh.Status.PrivateHistory
	if h == nil || !h.ExecutorAttempted || h.ExecutorIntent != res.intent || h.ExecutorNotCreated || h.LastExecutor != nil {
		return
	}
	h.ExecutorNotCreated = true
	if err := r.writePrivateHistory(ctx, s, fresh); err != nil {
		log.FromContext(ctx).Info("the private executor attempt stays latched: its non-attempt record is unconfirmed", "error", err.Error())
		return
	}
	log.FromContext(ctx).Info("recorded a known non-attempt: no Pod create was called for the private executor intent", "intent", res.intent)
}

// Observe only uncached exact owners. Absence never erases an executor or turns
// legacy rescue, heartbeat expiry or a PID boolean into private stop proof.
func (r *Reconciler) observePrivateHistory(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) {
	if s.Status.PrivateHistory == nil || r.APIReader == nil {
		obs.privateHistoryUnknown = errors.New("private retention or executor observation is unconfirmed")
		return
	}
	if _, err := r.privateHistoryHome(ctx, s); err != nil && (!homeExpiryRemovingHome(s) || privateHistoryIdentity(s) != nil) {
		// From Deleting on, the HOME expiry deletes this HOME after its
		// archive verified (DESIGN-025 6.5): its absence is expected then.
		obs.privateHistoryUnknown = err
	}
	// DESIGN-025 3: whether a terminal record or missed marker may be added,
	// judged on the record as stored, before this observation changes it.
	applies := r.terminalRecordApplies(s)
	recorded := s.Status.PrivateHistory.LastExecutor
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &pod); err != nil {
		if !apierrors.IsNotFound(err) {
			obs.privateHistoryUnknown = errors.New("private executor observation is unconfirmed")
		} else {
			r.observeExecutorAbsent(s, applies, obs)
		}
		return
	}
	if !controlledBySession(&pod, s) || pod.UID == "" {
		obs.privateHistoryUnknown = errors.New("private executor owner is foreign or unknown")
		return
	}
	if isHoldPod(&pod) {
		return
	}
	h := s.Status.PrivateHistory
	// An observed executor wins over a non-attempt mark: clear it, so the
	// attempt is latched as usual and can never re-arm.
	h.ExecutorNotCreated = false
	if h.LastExecutor != nil && h.LastExecutor.PodUID != string(pod.UID) {
		obs.privateHistoryUnknown = errors.New("private executor was replaced; original executor provenance remains retained")
		return
	}
	if h.Admission != "Unknown" && (!h.ExecutorAttempted || h.ExecutorIntent == "" ||
		pod.Annotations[privateHistoryExecutorAnnotation] != h.ExecutorIntent || pod.Annotations[privateHistoryHomeAnnotation] != h.HomeUID || !privateExecutorUsesHome(&pod, h.HomeClaim)) {
		obs.privateHistoryUnknown = errors.New("private executor does not match the single causal admission intent and original HOME")
		return
	}
	last := &v1alpha1.PrivateExecutorStatus{PodUID: string(pod.UID), PodName: pod.Name, NodeName: pod.Spec.NodeName,
		Revision: pod.Labels[v1alpha1.LabelRevision], Generation: s.Generation}
	if h.LastExecutor != nil && h.LastExecutor.PodUID == last.PodUID {
		last.ConversationID = h.LastExecutor.ConversationID
	}
	// Heartbeat provenance belongs to the recorded current executor only.
	boundExecutor := h.LastExecutor != nil && h.LastExecutor.PodUID == last.PodUID ||
		h.LastExecutor == nil && h.HomeUID != "" && pod.Annotations[privateHistoryHomeAnnotation] == h.HomeUID
	if boundExecutor && s.Status.PodName == pod.Name && s.Status.Agent != nil && len(s.Status.Agent.ConversationID) <= 256 {
		last.ConversationID = s.Status.Agent.ConversationID
	}
	h.LastExecutor = last
	r.observeExecutorTerminal(ctx, s, applies, recorded, &pod, obs)
}

func privateExecutorUsesHome(pod *corev1.Pod, claim string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "home" {
			return volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == claim
		}
	}
	return false
}

func setPrivateHistoryConditions(s *v1alpha1.AgentSession, obs observation, st *v1alpha1.AgentSessionStatus) {
	if !privateHistoryProtected(s) {
		return
	}
	c := metav1.Condition{Type: ConditionPrivateHistory, Status: metav1.ConditionTrue, Reason: "HomeProtected",
		Message: "original private HOME and durable Session identity remain protected; private stop state is Unknown", ObservedGeneration: s.Generation}
	if wantsPodGone(s) {
		c.Reason = "RetentionPending"
	}
	removal := c
	if obs.privateExecutorEnded {
		// DESIGN-025 3.4: with the terminal record confirmed, a missing
		// executor Pod is expected. The HOME stays protected.
		c.Reason = "ExecutorEnded"
		c.Message = "original private HOME and durable Session identity remain protected; the recorded private executor ended (executorTerminal); private stop state is Unknown"
	}
	if h := s.Status.PrivateHistory; homeExpiryRemovingHome(s) {
		// DESIGN-025 6.5: the HOME goes, or went, after its history was
		// archived; the Session's identity stays.
		c.Reason = map[string]string{v1alpha1.HomeExpiryDeleting: "HomeDeleting", v1alpha1.HomeExpiryExpired: "HomeExpired"}[h.Expiry.Phase]
		c.Message = "the HOME expiry deletes the private HOME after archiving its history"
		if h.Expiry.Archive != nil {
			c.Message += " as " + h.Expiry.Archive.Entry
		}
		c.Message += "; durable Session identity remains protected; private stop state is Unknown"
		removal = c
	}
	if obs.privateHistoryUnknown != nil {
		c.Status, c.Reason, c.Message = metav1.ConditionUnknown, "PreservationUnknown", truncate(obs.privateHistoryUnknown.Error())
		removal = c
	}
	meta.SetStatusCondition(&st.Conditions, c)
	if obs.privateStopErr != nil {
		removal.Message = truncate(removal.Message + "; the stop request for the recorded executor (DESIGN-025 4) failed and is retried: " + obs.privateStopErr.Error())
	}
	if wantsPodGone(s) {
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: ConditionRemovalBlocked, Status: metav1.ConditionTrue,
			Reason: removal.Reason, Message: removal.Message, ObservedGeneration: s.Generation})
	}
}
