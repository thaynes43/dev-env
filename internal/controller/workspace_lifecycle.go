package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

type WorkspaceStopper interface {
	StopWorkspace(context.Context, *corev1.Pod) error
}

func workspaceHoldName(s *v1alpha1.AgentSession) string {
	key := sha256.Sum256([]byte(s.UID))
	prefix := s.Name
	if len(prefix) > 45 {
		prefix = prefix[:45]
	}
	return prefix + "-rescue-" + hex.EncodeToString(key[:5])
}

func buildWorkspaceHoldPod(s *v1alpha1.AgentSession, t *templates.Templates, proof *protocol.WorkspaceStopProof) (*corev1.Pod, error) {
	if proof == nil || s.Spec.Workspace == nil {
		return nil, errors.New("shared hold Pod requires an explicit stop proof")
	}
	if err := proof.Validate(s.Spec.Workspace.ID, s.Name, string(s.UID), proof.PodUID); err != nil {
		return nil, err
	}
	pod, err := buildHoldPod(s, t)
	if err != nil {
		return nil, err
	}
	pod.Name = workspaceHoldName(s)
	constrainWorkspaceHoldNode(pod, proof.NodeName)
	for i, e := range pod.Spec.Containers[0].Env {
		if e.Name != protocol.SessionEnv {
			continue
		}
		doc, err := protocol.ParseSession([]byte(e.Value))
		if err != nil {
			return nil, err
		}
		doc.Workspace.StopProof = proof
		data, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		pod.Spec.Containers[0].Env[i].Value = string(data)
		return pod, nil
	}
	return nil, errors.New("shared hold Pod lacks its operator session document")
}

// Retained RWO homes remain attached to the old executor's healthy node.
// Add a conjunct to every existing required OR term; keep normal scheduling,
// worker/GPU preferences, selectors, tolerations and resource admission.
func constrainWorkspaceHoldNode(pod *corev1.Pod, node string) {
	if pod.Spec.Affinity == nil {
		pod.Spec.Affinity = &corev1.Affinity{}
	}
	if pod.Spec.Affinity.NodeAffinity == nil {
		pod.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	affinity := pod.Spec.Affinity.NodeAffinity
	match := corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}
	if affinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		affinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{match}}}}
		return
	}
	for i := range affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		term := &affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i]
		// An existing empty term matches no nodes; never broaden it.
		if len(term.MatchFields) == 0 && len(term.MatchExpressions) == 0 {
			continue
		}
		term.MatchFields = append(term.MatchFields, match)
	}
}

func workspaceHoldProof(pod *corev1.Pod) (*protocol.WorkspaceStopProof, error) {
	if !isHoldPod(pod) || len(pod.Spec.Containers) != 1 {
		return nil, errors.New("not a bounded shared hold Pod")
	}
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name != protocol.SessionEnv {
			continue
		}
		doc, err := protocol.ParseSession([]byte(e.Value))
		if err != nil {
			return nil, err
		}
		if doc.Workspace == nil || doc.Workspace.StopProof == nil {
			return nil, errors.New("shared hold Pod has no stop proof")
		}
		return doc.Workspace.StopProof, nil
	}
	return nil, errors.New("shared hold Pod lacks a session document")
}

func (r *Reconciler) verifyWorkspaceStop(ctx context.Context, s *v1alpha1.AgentSession, uid types.UID) (*protocol.WorkspaceStopProof, *corev1.Pod, error) {
	v := r.StopVerifier
	if v == nil {
		v = PodWorkspaceStopVerifier{Reader: r.APIReader}
	}
	return v.Verify(ctx, s, uid)
}

func (r *Reconciler) reconcileSharedWorkspace(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (ctrl.Result, error) {
	retry := ctrl.Result{RequeueAfter: 5 * time.Second}
	block := func(err error) (ctrl.Result, error) { obs.removalBlocked = err; return retry, nil }
	// Shared lifecycle decisions never infer disappearance from the cache.
	oldObserved, err := getOwned(ctx, r.APIReader, s, s.Name, &corev1.Pod{})
	if err != nil {
		return retry, err
	}
	obs.pod = oldObserved
	hold, err := getOwned(ctx, r.APIReader, s, workspaceHoldName(s), &corev1.Pod{})
	if err != nil {
		return retry, err
	}
	if obs.pod.foreign != "" || hold.foreign != "" {
		return block(errors.New("shared executor or hold Pod has a foreign controller owner; preservation required"))
	}
	if obs.pod.missing {
		if !hold.missing {
			return block(errors.New("shared executor is missing while its hold Pod remains; stop proof is uncertain"))
		}
		if !sharedCleanupRescueRecorded(s) && s.Status.PodName != "" {
			return block(errors.New("shared executor is missing without a verified owned rescue; deleted or partitioned owners cannot be taken over"))
		}
		if wantsPodGone(s) {
			if !s.DeletionTimestamp.IsZero() {
				res, _, err := r.archive(ctx, s, obs, true)
				return res, err
			}
			due, wait := r.archiveDue(s, obs.templates, *obs)
			if due || s.Status.ArchivedAt != nil {
				res, _, err := r.archive(ctx, s, obs, true)
				return res, err
			}
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		if s.Status.ArchivedAt != nil {
			return ctrl.Result{}, nil
		}
		if rec := s.Status.Rescue; rec != nil && !rec.Superseded {
			rec.Superseded = true
			// Both old Pods are genuinely absent after verified cleanup. Clear
			// that old executor's remembered name atomically with supersession,
			// so the next reconcile can create the requested new generation.
			s.Status.PodName = ""
			setRescueCondition(s, &s.Status, "")
			return r.writeStatus(ctx, s, ctrl.Result{RequeueAfter: time.Second})
		}
		if obs.templatesErr != nil {
			return block(obs.templatesErr)
		}
		return ctrl.Result{}, r.ensure(ctx, s, obs.templates, obs)
	}
	old := obs.pod.obj
	if !old.DeletionTimestamp.IsZero() {
		return block(errors.New("shared executor is being deleted; deletion cannot prove stop"))
	}
	if old.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return block(errors.New("shared executor must have RestartPolicyNever before stop-and-preserve"))
	}
	if err := allContainersTerminated(old); err != nil {
		if !hold.missing {
			return block(errors.New("shared hold exists while executor termination is uncertain; both Pods stay"))
		}
		if !wantsPodGone(s) {
			res, _, err := r.idleTimer(ctx, s, obs.templates, obs)
			return res, err
		}
		if r.WorkspaceStopper == nil {
			return block(errors.New("shared supervisor stop requester is unavailable"))
		}
		var fresh corev1.Pod
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(old), &fresh); err != nil {
			return block(err)
		}
		if fresh.UID != old.UID || !controlledBySession(&fresh, s) || !fresh.DeletionTimestamp.IsZero() || fresh.Spec.RestartPolicy != corev1.RestartPolicyNever {
			return block(errors.New("shared executor changed before stop request"))
		}
		if err := r.WorkspaceStopper.StopWorkspace(ctx, &fresh); err != nil {
			return block(fmt.Errorf("stop-and-preserve request: %w", err))
		}
		return block(errors.New("shared supervisor stop requested; awaiting genuine termination of every admitted container"))
	}
	proof, freshOld, err := r.verifyWorkspaceStop(ctx, s, old.UID)
	if err != nil {
		return block(err)
	}
	obs.pod.obj = freshOld
	if hold.missing {
		if sharedRescued(s) && s.Status.Rescue.SourcePodUID == string(old.UID) {
			if err := removeSharedPod(ctx, r.Client, s, freshOld); err != nil {
				return retry, err
			}
			return retry, nil
		}
		if obs.templatesErr != nil {
			return block(obs.templatesErr)
		}
		if obs.claim.missing || obs.claim.foreign != "" || volumeTerminating(*obs) {
			return block(errors.New("shared rescue's private home is missing, foreign or deleting"))
		}
		pod, err := buildWorkspaceHoldPod(s, obs.templates, proof)
		if err != nil {
			return block(err)
		}
		if _, _, err := r.verifyWorkspaceStop(ctx, s, old.UID); err != nil {
			return block(err)
		}
		if err := r.create(ctx, "shared hold Pod", pod); err != nil {
			return retry, err
		}
		return block(errors.New("shared hold Pod created; old executor and both volumes are retained for owned rescue"))
	}
	hp := hold.obj
	holdProof, err := workspaceHoldProof(hp)
	if err != nil || holdProof.PodUID != string(old.UID) || holdProof.SessionUID != string(s.UID) || holdProof.Workspace != s.Spec.Workspace.ID {
		return block(errors.New("shared hold proof does not match this retained executor"))
	}
	if !hp.DeletionTimestamp.IsZero() {
		return block(errors.New("shared hold Pod is deleting; both Pods stay until cleanup can be verified"))
	}
	if !sharedRescued(s) || s.Status.Rescue.SourcePodUID != string(old.UID) || s.Status.Rescue.PodUID != string(hp.UID) {
		if !podReady(hp) {
			reason, message := pendingReason(hp)
			return block(fmt.Errorf("shared hold Pod is not ready for bounded rescue: %s: %s", reason, message))
		}
		if rescueRanIn(s, hp) && s.Status.Rescue.Result == v1alpha1.RescueFailed && !holdRetryDue(s, obs.now, r.holdRetry()) {
			next := s.Status.Rescue.At.Add(r.holdRetry())
			obs.removalBlocked = fmt.Errorf("shared owned rescue failed; retained Pods stay and rescue retries at %s", next.UTC().Format(time.RFC3339))
			return ctrl.Result{RequeueAfter: max(time.Second, next.Sub(obs.now))}, nil
		}
		freshProof, _, err := r.verifyWorkspaceStop(ctx, s, old.UID)
		if err != nil {
			return block(err)
		}
		rec, reason, _, err := r.rescueWithProof(ctx, s, hp, freshProof)
		if err != nil {
			obs.removalBlocked = fmt.Errorf("shared owned rescue could not finish; both Pods stay and retry in %s: %w", rescueRetry, err)
			return ctrl.Result{RequeueAfter: rescueRetry}, nil
		}
		if _, _, err := r.verifyWorkspaceStop(ctx, s, old.UID); err != nil {
			return block(err)
		}
		if rec.SourcePodUID != string(old.UID) {
			return block(errors.New("owned rescue did not preserve the exact source Pod UID"))
		}
		written, err := r.recordRescue(ctx, s, rec, reason)
		if err != nil || !written {
			return retry, err
		}
		if !sharedRescued(s) {
			return block(errors.New("shared owned rescue failed; executor, hold Pod and both volumes stay"))
		}
		return retry, nil
	}
	// The hold Pod goes only after its exact rescue is durable; keep the
	// old executor until the hold is actually absent. Every deletion remains
	// in deletePod with UID/resourceVersion preconditions.
	if err := removeSharedPod(ctx, r.Client, s, hp); err != nil && !apierrors.IsNotFound(err) {
		return retry, err
	}
	return retry, nil
}

// The only shared cleanup entry asks the common guard, then uses its exact
// UID/resourceVersion deletion path. Fresh stop verification precedes it.
func removeSharedPod(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if err := podRemovalAllowed(s, pod); err != nil {
		return err
	}
	return deletePod(ctx, c, s, pod)
}

func sharedRescued(s *v1alpha1.AgentSession) bool {
	r := s.Status.Rescue
	return r != nil && !r.Superseded && r.PodUID != "" && r.SourcePodUID != "" && r.Generation == s.Generation &&
		(rescued(s) || r.Result == v1alpha1.RescueNoWorkAdmitted && r.PreservationKind == "NoWorkAdmitted")
}

// Only the uncached both-Pods-absent branch uses this historical record. The
// controller already verified the immutable workspace/task/session binding,
// exact permanently terminated source and owned preservation when recording
// it. A later spec generation can supersede that completed rescue after cleanup;
// disappearance alone never creates proof. Live-Pod guards still use the exact
// current-generation sharedRescued predicate.
func sharedCleanupRescueRecorded(s *v1alpha1.AgentSession) bool {
	r := s.Status.Rescue
	if s.Spec.Workspace == nil || r == nil || r.Superseded || r.Generation <= 0 || r.Generation > s.Generation ||
		r.SourcePodUID == "" || r.PodUID == "" || r.SourcePodUID == r.PodUID {
		return false
	}
	switch r.Result {
	case v1alpha1.RescueVerified:
		return r.PreservationKind == "" || r.PreservationKind == "OwnedRefsPreserved"
	case v1alpha1.RescueCleanAndPushed:
		return r.PreservationKind == ""
	case v1alpha1.RescueNoWorkAdmitted:
		return r.PreservationKind == "NoWorkAdmitted"
	default:
		return false
	}
}
