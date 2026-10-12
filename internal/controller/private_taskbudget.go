package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

// privateBudgetOwnership is an observation of retained original identity, not
// admission or stop evidence. It must never authorize another executor.
type privateBudgetOwnership struct {
	Binding                taskbudget.Binding
	WorkerID               string
	Worker                 taskbudget.ManagedWorkerRef
	SessionResourceVersion string
	SessionGeneration      int64
	HomeUID                string
	ExecutorIntent         string
	DirectoryNamespace     string
	DirectoryName          string
	DirectoryUID           string
	DirectorySpecDigest    string
	StopState              string
}

// provePrivateBudgetOwnership reads only original server-owned resources. The
// current directory helper still requires the accepted canary binding: using
// that metadata here does not relax the canary's prohibition on task budgets.
func provePrivateBudgetOwnership(ctx context.Context, reader client.Reader, directory *sessiondirectory.KubeStore, l *taskbudget.Ledger, ref taskbudget.ManagedWorkerRef) (*privateBudgetOwnership, error) {
	if reader == nil || directory == nil || l == nil || l.Version != 1 || ref.SessionUID == "" {
		return nil, taskbudget.ErrUnavailable
	}
	var s v1alpha1.AgentSession
	if reader.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, &s) != nil {
		return nil, taskbudget.ErrUnavailable
	}
	b, err := taskbudget.BindingFromAnnotations(s.Annotations)
	id := s.Annotations[taskbudget.WorkerAnnotation]
	if err != nil || b != l.Binding || string(s.UID) != ref.SessionUID || s.ResourceVersion == "" || s.Generation < 1 ||
		!s.DeletionTimestamp.IsZero() || s.Spec.Workspace != nil || s.Spec.Mode != v1alpha1.ModeTask || s.Spec.Parent != l.Parent ||
		(s.Spec.Agent != v1alpha1.AgentClaude && s.Spec.Agent != v1alpha1.AgentCodex) || !strings.HasPrefix(id, "child:") {
		return nil, taskbudget.ErrDenied
	}
	workers := 0
	for _, w := range l.Workers {
		if w.ID == id {
			workers++
			if !w.Active || w.Managed == nil || *w.Managed != ref || w.StartedAt.Before(l.CreatedAt) || w.StartedAt.After(l.AccountedAt) ||
				!w.StoppedAt.IsZero() || w.StopEvidence != "" {
				return nil, taskbudget.ErrDenied
			}
		}
	}
	if workers != 1 {
		return nil, taskbudget.ErrDenied
	}
	home, err := (&Reconciler{APIReader: reader}).privateHistoryHome(ctx, &s)
	if err != nil {
		return nil, taskbudget.ErrUnavailable
	}
	h := s.Status.PrivateHistory
	if !h.ExecutorAttempted || h.ExecutorIntent == "" || h.LastExecutor == nil || h.LastExecutor.PodUID == "" || ref.PodUID == "" ||
		h.LastExecutor.PodUID != ref.PodUID || h.LastExecutor.PodName != s.Name || h.LastExecutor.Generation < h.BindingGeneration || h.LastExecutor.Generation > s.Generation {
		return nil, taskbudget.ErrUnavailable
	}
	receipt := s.Status.SessionDirectory
	if receipt == nil || receipt.State != "Confirmed" || receipt.UID == "" {
		return nil, taskbudget.ErrUnavailable
	}
	d, err := directory.Get(ctx, receipt.Name)
	if err != nil || string(d.UID) != receipt.UID {
		return nil, taskbudget.ErrUnavailable
	}
	spec, err := sessiondirectory.Spec(&s)
	if err != nil || !reflect.DeepEqual(spec, d.Spec) {
		return nil, taskbudget.ErrDenied
	}
	raw, err := json.Marshal(spec)
	if err != nil || commonrules.Digest(raw) != receipt.SpecDigest {
		return nil, taskbudget.ErrDenied
	}
	var pod corev1.Pod
	if reader.Get(ctx, client.ObjectKeyFromObject(&s), &pod) != nil {
		return nil, taskbudget.ErrUnavailable
	}
	if string(pod.UID) != ref.PodUID || !controlledBySession(&pod, &s) || !pod.DeletionTimestamp.IsZero() || isHoldPod(&pod) ||
		pod.Annotations[privateHistoryHomeAnnotation] != h.HomeUID || pod.Annotations[privateHistoryExecutorAnnotation] != h.ExecutorIntent ||
		!privateExecutorUsesHome(&pod, h.HomeClaim) || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != string(d.Spec.Revisions.Image) ||
		pod.Labels[v1alpha1.LabelRevision] != string(d.Spec.Revisions.Template) || h.LastExecutor.Revision != string(d.Spec.Revisions.Template) ||
		validateProjectCanaryExecutor(&s, &pod) != nil {
		return nil, taskbudget.ErrDenied
	}
	// A round trip refuses changes during the independent observations. This
	// freshness check is not a one-use launch fence or termination proof.
	confirmedHome, err := (&Reconciler{APIReader: reader}).privateHistoryHome(ctx, &s)
	if err != nil || !reflect.DeepEqual(confirmedHome, home) {
		return nil, taskbudget.ErrUnavailable
	}
	confirmedDirectory, err := directory.Get(ctx, receipt.Name)
	if err != nil || !reflect.DeepEqual(confirmedDirectory, d) {
		return nil, taskbudget.ErrUnavailable
	}
	var confirmedPod corev1.Pod
	if reader.Get(ctx, client.ObjectKeyFromObject(&s), &confirmedPod) != nil || !reflect.DeepEqual(confirmedPod, pod) {
		return nil, taskbudget.ErrUnavailable
	}
	var confirmed v1alpha1.AgentSession
	if reader.Get(ctx, client.ObjectKeyFromObject(&s), &confirmed) != nil || confirmed.UID != s.UID || confirmed.ResourceVersion != s.ResourceVersion ||
		confirmed.Generation != s.Generation || !confirmed.DeletionTimestamp.IsZero() || !reflect.DeepEqual(confirmed.Spec, s.Spec) ||
		!reflect.DeepEqual(confirmed.Status.PrivateHistory, h) || !reflect.DeepEqual(confirmed.Status.SessionDirectory, receipt) {
		return nil, taskbudget.ErrUnavailable
	}
	return &privateBudgetOwnership{Binding: b, WorkerID: id, Worker: ref, SessionResourceVersion: s.ResourceVersion, SessionGeneration: s.Generation,
		HomeUID: string(home.UID), ExecutorIntent: h.ExecutorIntent, DirectoryNamespace: d.Namespace, DirectoryName: d.Name,
		DirectoryUID: string(d.UID), DirectorySpecDigest: receipt.SpecDigest, StopState: "Unknown"}, nil
}

// PrivateManagedAdmissionInspector is a separate read-only reservation seam.
// It has no production constructor or wiring. Original ownership is necessary
// evidence, but this slice supplies no private child admission capability and
// therefore grants no reservation, including an attempted-but-absent executor.
type PrivateManagedAdmissionInspector struct {
	Reader    client.Reader
	Directory *sessiondirectory.KubeStore
}

var _ taskbudget.ManagedAdmissionInspector = PrivateManagedAdmissionInspector{}

func (v PrivateManagedAdmissionInspector) InspectManagedAdmission(ctx context.Context, l *taskbudget.Ledger, ref taskbudget.ManagedWorkerRef) error {
	if _, err := provePrivateBudgetOwnership(ctx, v.Reader, v.Directory, l, ref); err != nil {
		return err
	}
	return taskbudget.ErrDenied
}
