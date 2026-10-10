package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	if ref == nil || ref.PodUID == "" || e.Evidence.Reference != managedBudgetReference(*ref) {
		return taskbudget.ErrDenied
	}
	var s v1alpha1.AgentSession
	if err := v.Reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &s); err != nil {
		return taskbudget.ErrUnavailable
	}
	b, err := taskbudget.BindingFromAnnotations(s.Annotations)
	if err != nil || b != l.Binding || string(s.UID) != ref.SessionUID || s.Spec.Parent != l.Parent || s.Annotations[taskbudget.WorkerAnnotation] != e.WorkerID || s.Spec.Workspace == nil {
		return taskbudget.ErrDenied
	}
	_, pod, err := PodWorkspaceStopVerifier(v).Verify(ctx, &s, types.UID(ref.PodUID))
	if err != nil {
		return taskbudget.ErrUnavailable
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
	if e.Evidence.Kind != "managed-rescue-stop" || !sharedRescued(&s) || s.Status.Rescue.SourcePodUID != ref.PodUID {
		return taskbudget.ErrDenied
	}
	return nil
}

func (r *Reconciler) observeTaskBudget(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod) (*taskbudget.Ledger, error) {
	if r.TaskBudgets == nil || r.APIReader == nil || s.Spec.Workspace == nil || s.UID == "" {
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
	_, err = r.TaskBudgets.Record(ctx, e)
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
	if err != nil || b != l.Binding || string(s.UID) != ref.SessionUID || s.Spec.Parent != l.Parent || s.Spec.Workspace == nil || s.Spec.Mode != v1alpha1.ModeTask || s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !s.DeletionTimestamp.IsZero() {
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
	var pod corev1.Pod
	if err := v.Reader.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Name}, &pod); !apierrors.IsNotFound(err) {
		return taskbudget.ErrUnavailable
	}
	return nil
}
