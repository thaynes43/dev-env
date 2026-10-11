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
	return s.Spec.Workspace == nil && s.Status.PrivateHistory != nil
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

// A status write must be confirmed independently before any resource write.
func (r *Reconciler) writePrivateHistory(ctx context.Context, s, fresh *v1alpha1.AgentSession) error {
	if err := r.Client.Status().Update(ctx, fresh); err != nil {
		return fmt.Errorf("private retention write is unconfirmed: %w", err)
	}
	confirmed, err := r.privateHistorySession(ctx, fresh)
	if err != nil {
		return err
	}
	if confirmed.ResourceVersion != fresh.ResourceVersion || !apiequality.Semantic.DeepEqual(confirmed.Status.PrivateHistory, fresh.Status.PrivateHistory) {
		return errors.New("private retention acknowledgement changed or is unconfirmed")
	}
	*s = *confirmed
	return nil
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
	if !privateHistoryProtected(s) || h.Version != 1 || h.SessionUID != string(s.UID) || h.HomeClaim != HomeClaimName(s.Name) ||
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
	if wantsPodGone(fresh) || h.Admission == "Unknown" || h.ExecutorAttempted {
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

// Record the attempt before Pod creation. A failed/lost create is uncertain,
// and cannot be retried as an implicit new provider execution by this slice.
func (r *Reconciler) reservePrivateExecutor(ctx context.Context, s *v1alpha1.AgentSession) error {
	fresh, err := r.privateHistorySession(ctx, s)
	if err != nil {
		return err
	}
	if wantsPodGone(fresh) || fresh.Status.PrivateHistory == nil || fresh.Status.PrivateHistory.ExecutorAttempted {
		return errors.New("private executor attempt is already recorded or Session stopped")
	}
	if _, err := r.privateHistoryHome(ctx, fresh); err != nil {
		return err
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(fresh), &pod); !apierrors.IsNotFound(err) {
		return errors.New("private executor absence is unconfirmed before first attempt")
	}
	fresh.Status.PrivateHistory.ExecutorAttempted = true
	fresh.Status.PrivateHistory.ExecutorIntent = string(uuid.NewUUID())
	return r.writePrivateHistory(ctx, s, fresh)
}

// Observe only uncached exact owners. Absence never erases an executor or turns
// legacy rescue, heartbeat expiry or a PID boolean into private stop proof.
func (r *Reconciler) observePrivateHistory(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) {
	if _, err := r.privateHistoryHome(ctx, s); err != nil {
		obs.privateHistoryUnknown = err
	}
	var pod corev1.Pod
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &pod); err != nil {
		if !apierrors.IsNotFound(err) {
			obs.privateHistoryUnknown = errors.New("private executor observation is unconfirmed")
		} else if s.Status.PrivateHistory.ExecutorAttempted {
			obs.privateHistoryUnknown = errors.New("the recorded private executor is absent; absence cannot authorize a retry or prove stop")
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
	if obs.privateHistoryUnknown != nil {
		c.Status, c.Reason, c.Message = metav1.ConditionUnknown, "PreservationUnknown", truncate(obs.privateHistoryUnknown.Error())
	}
	meta.SetStatusCondition(&st.Conditions, c)
	if wantsPodGone(s) {
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: ConditionRemovalBlocked, Status: metav1.ConditionTrue,
			Reason: c.Reason, Message: c.Message, ObservedGeneration: s.Generation})
	}
}
