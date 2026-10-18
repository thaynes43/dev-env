package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
)

const ConditionTaskBudget = "TaskBudget"
const taskBudgetPoll = 5 * time.Second

func budgetBound(s *v1alpha1.AgentSession) bool { return s.Annotations[taskbudget.UIDAnnotation] != "" }

func canonicalCheckpointBlocker(b taskbudget.Binding) string {
	h := sha256.Sum256([]byte(b.TaskUID))
	return "checkpoint:" + hex.EncodeToString(h[:16]) + ":" + strconv.FormatUint(b.Epoch, 10)
}

func managedBudgetReference(ref taskbudget.ManagedWorkerRef) string {
	return strings.Join([]string{ref.Namespace, ref.Name, ref.SessionUID, ref.PodUID}, "/")
}

// managedNeverLaunchedEvidence marks a stop receipt for a child whose one Pod
// create was never reserved in the ledger, so it cannot have run.
const managedNeverLaunchedEvidence = "managed-never-launched"

// ManagedBudgetEvidenceValidator uses only uncached server/resource observations.
// State files, heartbeat and Outcome are never completion or failure receipts.
// It deliberately cannot validate native progress, notification or owner answers.
type ManagedBudgetEvidenceValidator struct {
	Reader client.Reader
	Now    func() time.Time
}

func (v ManagedBudgetEvidenceValidator) ValidateOwnerDecision(context.Context, *taskbudget.Ledger, apiv1.TaskBudgetExtension, string) error {
	return taskbudget.ErrUnavailable
}

func (v ManagedBudgetEvidenceValidator) Validate(ctx context.Context, l *taskbudget.Ledger, e apiv1.TaskBudgetEvent) error {
	if v.Reader == nil || (e.Kind != "failure" && e.Kind != "worker-stop") {
		return taskbudget.ErrUnavailable
	}
	var ref *taskbudget.ManagedWorkerRef
	for _, w := range l.Workers {
		if w.ID == e.WorkerID {
			ref = w.Managed
			break
		}
	}
	neverLaunched := e.Kind == "worker-stop" && e.Evidence.Kind == managedNeverLaunchedEvidence
	if ref == nil || e.Evidence.Reference != managedBudgetReference(*ref) ||
		neverLaunched != (ref.PodUID == "") || neverLaunched && ref.LaunchRequested {
		return taskbudget.ErrDenied
	}
	var s v1alpha1.AgentSession
	if err := v.Reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &s); err != nil {
		return taskbudget.ErrUnavailable
	}
	b, err := taskbudget.BindingFromAnnotations(s.Annotations)
	if err != nil || b != l.Binding || string(s.UID) != ref.SessionUID || s.Spec.Parent != l.Parent || s.Annotations[taskbudget.WorkerAnnotation] != e.WorkerID {
		return taskbudget.ErrDenied
	}
	if neverLaunched {
		return v.validateNeverLaunched(ctx, &s)
	}
	var pod *corev1.Pod
	if s.Spec.Workspace == nil {
		if !ref.LaunchRequested {
			return taskbudget.ErrDenied
		}
		pod, err = privateBudgetStop(ctx, v.Reader, &s, types.UID(ref.PodUID), v.Now)
	} else {
		_, pod, err = PodWorkspaceStopVerifier(v).Verify(ctx, &s, types.UID(ref.PodUID))
	}
	if err != nil {
		return fmt.Errorf("%w: %v", taskbudget.ErrUnavailable, err)
	}
	if e.Kind == "failure" {
		if e.AttemptID != "managed:"+ref.SessionUID || e.BlockerID != canonicalCheckpointBlocker(l.Binding) || e.Evidence.Kind != "managed-container-exit" {
			return taskbudget.ErrDenied
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == ContainerName && status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
				return nil
			}
		}
		return taskbudget.ErrDenied
	}
	if s.Spec.Workspace == nil {
		if e.Evidence.Kind != "private-retained-stop" {
			return taskbudget.ErrDenied
		}
		return nil
	}
	if e.Evidence.Kind != "managed-rescue-stop" || !sharedRescued(&s) || s.Status.Rescue.SourcePodUID != ref.PodUID {
		return taskbudget.ErrDenied
	}
	return nil
}

// validateNeverLaunched proves a reserved child never ran. Every managed Pod
// create is reserved in the ledger first, and the ledger being validated shows
// none for this worker. The session must also be stopping, must never have
// recorded a Pod, and an uncached read must find no Pod by its name. The
// ledger compare-and-swap orders this receipt against a launch reservation:
// whichever commits first wins.
func (v ManagedBudgetEvidenceValidator) validateNeverLaunched(ctx context.Context, s *v1alpha1.AgentSession) error {
	if !wantsPodGone(s) || s.Status.PodName != "" {
		return taskbudget.ErrDenied
	}
	var pod corev1.Pod
	err := v.Reader.Get(ctx, client.ObjectKeyFromObject(s), &pod)
	if err == nil {
		return taskbudget.ErrDenied
	}
	if !apierrors.IsNotFound(err) {
		return taskbudget.ErrUnavailable
	}
	return nil
}

func (r *Reconciler) observeTaskBudget(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod) (*taskbudget.Ledger, error) {
	if r.TaskBudgets == nil || r.APIReader == nil || s.Spec.Workspace == nil && !privateHistoryProtected(s) || s.UID == "" {
		return nil, taskbudget.ErrUnavailable
	}
	if s.Spec.Workspace == nil && (s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil) {
		return nil, taskbudget.ErrUnavailable
	}
	b, err := taskbudget.BindingFromAnnotations(s.Annotations)
	if err != nil {
		return nil, err
	}
	l, err := r.TaskBudgets.Observe(ctx, b)
	if err != nil {
		return nil, err
	}
	if l.Parent != s.Spec.Parent {
		return nil, taskbudget.ErrDenied
	}
	id := s.Annotations[taskbudget.WorkerAnnotation]
	var known *taskbudget.Worker
	for i := range l.Workers {
		if l.Workers[i].ID == id {
			known = &l.Workers[i]
			break
		}
	}
	if known == nil {
		return nil, taskbudget.ErrDenied
	}
	ref := taskbudget.ManagedWorkerRef{Namespace: s.Namespace, Name: s.Name, SessionUID: string(s.UID)}
	if pod != nil {
		if !controlledBySession(pod, s) || pod.UID == "" || pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
			return nil, taskbudget.ErrDenied
		}
		ref.PodUID = string(pod.UID)
	} else if known.Managed != nil && known.Managed.PodUID != "" {
		// A lost executor is uncertain; retain its binding rather than letting
		// an empty observation reset this worker's immutable Pod identity.
		return l, taskbudget.ErrUnavailable
	}
	l, err = r.TaskBudgets.BindManagedWorker(ctx, b, id, ref)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// enforceTaskBudget persists suspend intent after the off-Pod latch. It does
// not label a stop request as proof or release a writer on missing resources.
func (r *Reconciler) enforceTaskBudget(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod) (bool, error) {
	if !budgetBound(s) {
		return false, nil
	}
	l, err := r.observeTaskBudget(ctx, s, pod)
	if errors.Is(err, taskbudget.ErrConflict) {
		return false, err
	}
	stop := err != nil || l == nil || l.Latched
	if !stop {
		for _, w := range l.Workers {
			if w.ID == s.Annotations[taskbudget.WorkerAnnotation] && !w.Active {
				stop = true
			}
		}
	}
	if !stop {
		return false, nil
	}
	return r.suspendTaskBudget(ctx, s, "budget latched or authority unavailable; waiting for whole executor stop and workspace rescue")
}

func (r *Reconciler) suspendTaskBudget(ctx context.Context, s *v1alpha1.AgentSession, message string) (bool, error) {
	changed := false
	if s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended && s.DeletionTimestamp.IsZero() {
		before := s.DeepCopy()
		s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if err := r.Client.Patch(ctx, s, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return true, err
		}
		changed = true
	}
	before := s.DeepCopy()
	taskBudgetStopping(s, message)
	if !apiequality.Semantic.DeepEqual(before.Status, s.Status) {
		if err := r.Client.Status().Patch(ctx, s, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return changed, err
		}
	}
	// A status-only observation must not delay the existing stop/rescue flow.
	return changed, nil
}

func (r *Reconciler) recordTaskBudgetStop(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if !budgetBound(s) {
		return nil
	}
	l, err := r.observeTaskBudget(ctx, s, pod)
	if err != nil {
		return err
	}
	id := s.Annotations[taskbudget.WorkerAnnotation]
	var ref *taskbudget.ManagedWorkerRef
	for _, w := range l.Workers {
		if w.ID == id {
			ref = w.Managed
			break
		}
	}
	if ref == nil {
		return taskbudget.ErrUnavailable
	}
	e := apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "managed-stop:" + ref.SessionUID, Kind: "worker-stop", WorkerID: id,
		Evidence: apiv1.TaskBudgetEvidence{ID: "stop:" + ref.PodUID, Kind: "managed-rescue-stop", Reference: managedBudgetReference(*ref)}}
	if s.Spec.Workspace == nil {
		e.Evidence.Kind = "private-retained-stop"
	}
	_, err = r.TaskBudgets.Record(ctx, e)
	return err
}

func managedNeverLaunchedEvent(b taskbudget.Binding, worker string, ref taskbudget.ManagedWorkerRef) apiv1.TaskBudgetEvent {
	return apiv1.TaskBudgetEvent{Binding: b, ID: "managed-unlaunched:" + ref.SessionUID, Kind: "worker-stop", WorkerID: worker,
		Evidence: apiv1.TaskBudgetEvidence{ID: "unlaunched:" + ref.SessionUID, Kind: managedNeverLaunchedEvidence, Reference: managedBudgetReference(ref)}}
}

// recordTaskBudgetNeverLaunched closes a child reserved at dispatch whose one
// Pod create was never reserved, once its session is stopping without a Pod.
// Otherwise that worker stays charged and can never record a stop. A worker
// with a reserved launch, or one already closed, is left alone.
func (r *Reconciler) recordTaskBudgetNeverLaunched(ctx context.Context, s *v1alpha1.AgentSession) error {
	if !budgetBound(s) || !wantsPodGone(s) || s.Status.PodName != "" {
		return nil
	}
	if r.TaskBudgets == nil {
		return taskbudget.ErrUnavailable
	}
	b, err := taskbudget.BindingFromAnnotations(s.Annotations)
	if err != nil {
		return err
	}
	l, err := r.TaskBudgets.Observe(ctx, b)
	if err != nil {
		return err
	}
	id := s.Annotations[taskbudget.WorkerAnnotation]
	candidate := func(l *taskbudget.Ledger) *taskbudget.Worker {
		for i := range l.Workers {
			w := &l.Workers[i]
			if w.ID == id && w.Active && (w.Managed == nil || !w.Managed.LaunchRequested && w.Managed.PodUID == "") {
				return w
			}
		}
		return nil
	}
	w := candidate(l)
	if w == nil {
		return nil
	}
	if w.Managed == nil {
		// Bind the worker to this session first, as before any launch.
		if l, err = r.observeTaskBudget(ctx, s, nil); err != nil {
			return err
		}
		if w = candidate(l); w == nil || w.Managed == nil {
			return nil
		}
	}
	_, err = r.TaskBudgets.Record(ctx, managedNeverLaunchedEvent(l.Binding, id, *w.Managed))
	return err
}

func managedBudgetExecutorExited(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == ContainerName && status.State.Terminated != nil {
			return true
		}
	}
	return false
}

func (r *Reconciler) recordTaskBudgetFailure(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if !budgetBound(s) || pod == nil {
		return nil
	}
	failed := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == ContainerName && status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
			failed = true
		}
	}
	if !failed {
		return nil
	}
	l, err := r.observeTaskBudget(ctx, s, pod)
	if err != nil {
		return err
	}
	ref := taskbudget.ManagedWorkerRef{Namespace: s.Namespace, Name: s.Name, SessionUID: string(s.UID), PodUID: string(pod.UID)}
	e := apiv1.TaskBudgetEvent{Binding: l.Binding, ID: "managed-failure:" + ref.SessionUID, Kind: "failure", WorkerID: s.Annotations[taskbudget.WorkerAnnotation], AttemptID: "managed:" + ref.SessionUID, BlockerID: canonicalCheckpointBlocker(l.Binding),
		Evidence: apiv1.TaskBudgetEvidence{ID: "exit:" + ref.PodUID, Kind: "managed-container-exit", Reference: managedBudgetReference(ref)}}
	_, err = r.TaskBudgets.Record(ctx, e)
	return err
}

// TaskBudgetStopping records uncertainty without changing existing phase enums.
func taskBudgetStopping(s *v1alpha1.AgentSession, message string) {
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: ConditionTaskBudget, Status: metav1.ConditionFalse, Reason: "Stopping", Message: message, ObservedGeneration: s.Generation})
}

// ManagedPodAdmissionInspector validates live server-assigned identity and the
// reviewed immutable worker image before one-shot Pod launch. The image must
// contain agentd's pre-boot hard deadline; GitOps supplies its approved digest.
// Internal native usage is an estimate, not a prerequisite for time enforcement.
type ManagedPodAdmissionInspector struct {
	Reader    client.Reader
	Templates types.NamespacedName
	Image     string
}

func (v ManagedPodAdmissionInspector) InspectManagedAdmission(ctx context.Context, l *taskbudget.Ledger, ref taskbudget.ManagedWorkerRef) error {
	if v.Reader == nil || !strings.Contains(v.Image, "@sha256:") || ref.PodUID != "" {
		return taskbudget.ErrUnavailable
	}
	var s v1alpha1.AgentSession
	if err := v.Reader.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &s); err != nil {
		return taskbudget.ErrUnavailable
	}
	b, err := taskbudget.BindingFromAnnotations(s.Annotations)
	if err != nil || b != l.Binding || string(s.UID) != ref.SessionUID || s.Spec.Parent != l.Parent || s.Spec.Mode != v1alpha1.ModeTask || s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero() {
		return taskbudget.ErrDenied
	}
	if s.Spec.Workspace == nil && (s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil) {
		return taskbudget.ErrDenied
	}
	var cm corev1.ConfigMap
	if err := v.Reader.Get(ctx, v.Templates, &cm); err != nil {
		return taskbudget.ErrUnavailable
	}
	t, err := templates.Parse(cm.Data)
	if err != nil || t.Image != v.Image {
		return taskbudget.ErrDenied
	}
	if s.Spec.Workspace == nil {
		return inspectPrivateManagedLaunch(ctx, v.Reader, l, ref, &s)
	}
	var pod corev1.Pod
	if err := v.Reader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Name}, &pod); !apierrors.IsNotFound(err) {
		return taskbudget.ErrUnavailable
	}
	return nil
}
