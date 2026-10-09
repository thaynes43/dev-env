package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	privateHomeUIDAnnotation     = v1alpha1.LabelPrefix + "original-private-home-uid"
	privateHomeReceiptAnnotation = v1alpha1.LabelPrefix + "private-home-retention"
	retainedPrivateHomeLabel     = v1alpha1.LabelPrefix + "retained-private-home"
	retainedSessionUIDLabel      = v1alpha1.LabelPrefix + "retained-session-uid"
)

// This receipt is retention evidence, not an export of provider files or a
// backup. It binds only the verified task rescue and unchanged private claim.
type privateHomeRetentionReceipt struct {
	Version               int                   `json:"version"`
	Namespace             string                `json:"namespace"`
	Session               string                `json:"session"`
	SessionUID            string                `json:"sessionUID"`
	Workspace             string                `json:"workspace"`
	Repo                  string                `json:"repo"`
	SourcePodUID          string                `json:"sourcePodUID"`
	HoldPodUID            string                `json:"holdPodUID"`
	PrivateHomeUID        string                `json:"privateHomeUID"`
	WriterGeneration      int64                 `json:"writerGeneration"`
	NoOwner               bool                  `json:"noOwner"`
	RescueGeneration      int64                 `json:"rescueGeneration"`
	RescueResult          v1alpha1.RescueResult `json:"rescueResult"`
	PreservationKind      string                `json:"preservationKind"`
	RescueStamp           string                `json:"rescueStamp"`
	RescueRecordedAt      metav1.Time           `json:"rescueRecordedAt"`
	RescueManifest        string                `json:"rescueManifest,omitempty"`
	PreservedOwnersSHA256 string                `json:"preservedOwnersSHA256"`
}

// workspacePrivateHome reads the API server and never adopts a replacement,
// deleting claim or a claim with an inexact controller reference.
func (r *Reconciler) workspacePrivateHome(ctx context.Context, s *v1alpha1.AgentSession, expected types.UID) (*corev1.PersistentVolumeClaim, error) {
	var home corev1.PersistentVolumeClaim
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: HomeClaimName(s.Name)}, &home); err != nil {
		return nil, fmt.Errorf("shared original private home cannot be verified: %w", err)
	}
	if home.UID == "" || expected != "" && home.UID != expected || !home.DeletionTimestamp.IsZero() || !controlledBySessionObject(&home, s) {
		return nil, errors.New("shared original private home is replaced, foreign or deleting; preservation required")
	}
	return &home, nil
}

func controlledBySessionObject(o metav1.Object, s *v1alpha1.AgentSession) bool {
	owner := metav1.GetControllerOf(o)
	return owner != nil && exactSessionController(*owner, s)
}

func exactSessionController(owner metav1.OwnerReference, s *v1alpha1.AgentSession) bool {
	return owner.Controller != nil && *owner.Controller && owner.APIVersion == v1alpha1.GroupVersion.String() &&
		owner.Kind == "AgentSession" && owner.Name == s.Name && owner.UID == s.UID
}

func (r *Reconciler) sharedPodsAbsent(ctx context.Context, s *v1alpha1.AgentSession) error {
	for _, name := range []string{s.Name, workspaceHoldName(s)} {
		var pod corev1.Pod
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &pod)
		if err == nil {
			return fmt.Errorf("shared Pod %s (UID %s) exists; private home and Session stay", name, pod.UID)
		}
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("shared Pod absence is unconfirmed: %w", err)
		}
	}
	return nil
}

// Bind before creating the first executor. Lost acknowledgements require an
// uncached confirmation; an existing executor or rescue cannot be migrated by
// inferring which same-named claim it originally used.
func (r *Reconciler) bindSharedPrivateHome(ctx context.Context, s *v1alpha1.AgentSession) error {
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		return err
	}
	if fresh.UID != s.UID || fresh.Generation != s.Generation || fresh.Spec.Workspace == nil || wantsPodGone(&fresh) || !sharedAdmissionIs(&fresh, sharedStarted) {
		return errors.New("shared Session changed before private home admission")
	}
	home, err := r.workspacePrivateHome(ctx, &fresh, types.UID(fresh.Status.SharedPrivateHomeUID))
	if err != nil {
		return err
	}
	if fresh.Status.SharedPrivateHomeUID == "" {
		if fresh.Status.PodName != "" || fresh.Status.Rescue != nil {
			return errors.New("shared lifecycle has no original private home binding; migration is refused")
		}
		if err := r.sharedPodsAbsent(ctx, &fresh); err != nil {
			return err
		}
		fresh.Status.SharedPrivateHomeUID = string(home.UID)
		if err := r.Client.Status().Update(ctx, &fresh); err != nil {
			return fmt.Errorf("private home admission write is unconfirmed: %w", err)
		}
	}
	var confirmed v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &confirmed); err != nil {
		return err
	}
	if confirmed.UID != s.UID || confirmed.Generation != s.Generation || wantsPodGone(&confirmed) || confirmed.Status.SharedPrivateHomeUID != string(home.UID) {
		return errors.New("private home admission changed or is unconfirmed")
	}
	if err := r.sharedPodsAbsent(ctx, &confirmed); err != nil {
		return err
	}
	if _, err := r.workspacePrivateHome(ctx, &confirmed, home.UID); err != nil {
		return err
	}
	*s = confirmed
	return nil
}

func sharedRetentionProof(s *v1alpha1.AgentSession) error {
	rec := s.Status.Rescue
	if s.Spec.Workspace == nil || s.Spec.Mode != v1alpha1.ModeTask || s.DeletionTimestamp.IsZero() || s.Status.ArchivedAt != nil ||
		!controllerutil.ContainsFinalizer(s, Finalizer) || !sharedAdmissionIs(s, sharedStarted) || !sharedRescued(s) || rec.At == nil || rec.At.IsZero() || !protocol.ValidRescueName(rec.Stamp) || rec.PodUID == rec.SourcePodUID {
		return errors.New("shared reap requires a durable current-generation task rescue and deleting Session")
	}
	p := rec.SharedProof
	if p == nil || p.Version != 1 || p.Workspace != s.Spec.Workspace.ID || p.Task != s.Name || p.Repo != s.Spec.Repo ||
		p.SessionUID != string(s.UID) || p.SourcePodUID != rec.SourcePodUID || p.PrivateHomeUID == "" || p.PrivateHomeUID != s.Status.SharedPrivateHomeUID ||
		p.WriterGeneration < 0 || p.NoOwner != (p.WriterGeneration == 0) {
		return errors.New("shared rescue identity, original home or writer proof is incomplete or mismatched")
	}
	switch p.Kind {
	case "NoWorkAdmitted":
		if rec.Result != v1alpha1.RescueNoWorkAdmitted || rec.PreservationKind != p.Kind || p.Manifest != "" || len(rec.UnpushedRefs) != 0 || rec.OmittedRefs != 0 {
			return errors.New("no-work admission is contradictory; it cannot prove an empty private home")
		}
	case "OwnedRefsPreserved":
		if rec.Result != v1alpha1.RescueVerified || rec.PreservationKind != p.Kind || p.NoOwner || p.Manifest == "" {
			return errors.New("owned preparation rescue proof is contradictory")
		}
	case "TaskWorkPreserved":
		if p.NoOwner || rec.PreservationKind != "" || rec.Result != v1alpha1.RescueVerified && rec.Result != v1alpha1.RescueCleanAndPushed ||
			rec.Result == v1alpha1.RescueVerified && p.Manifest == "" {
			return errors.New("admitted task rescue proof is contradictory")
		}
	default:
		return errors.New("shared rescue has no supported typed preservation result")
	}
	if p.Manifest != "" {
		prefix := protocol.RescueRoot + "/"
		session, name, err := protocol.ParseRescueID(strings.TrimPrefix(path.Dir(p.Manifest), prefix))
		if err != nil || session != s.Name || name != rec.Stamp && !strings.HasPrefix(name, rec.Stamp+"-") ||
			!strings.HasPrefix(p.Manifest, prefix) || path.Base(p.Manifest) != protocol.ManifestFile || path.Clean(p.Manifest) != p.Manifest || rec.LastBundle != p.Manifest {
			return errors.New("shared rescue manifest locator does not match this verified result")
		}
	}
	return nil
}

func retentionOwners(home *corev1.PersistentVolumeClaim, s *v1alpha1.AgentSession, detached bool) ([]metav1.OwnerReference, error) {
	var owners []metav1.OwnerReference
	found := 0
	for _, owner := range home.OwnerReferences {
		if exactSessionController(owner, s) {
			found++
			continue
		}
		// Keeping another reference to this Session would still admit GC after
		// finalization. Preserve it, and refuse rather than remove extra owners.
		if owner.UID == s.UID || owner.Controller != nil && *owner.Controller {
			return nil, errors.New("private home has a changed or additional Session/controller owner; preservation required")
		}
		owners = append(owners, owner)
	}
	if !detached && found != 1 || detached && found != 0 {
		return nil, errors.New("private home controller detach state does not match its retention receipt")
	}
	return owners, nil
}

func retentionReceipt(s *v1alpha1.AgentSession, owners []metav1.OwnerReference) (string, error) {
	rec, p := s.Status.Rescue, s.Status.Rescue.SharedProof
	data, err := json.Marshal(owners)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	receipt := privateHomeRetentionReceipt{Version: 1, Namespace: s.Namespace, Session: s.Name, SessionUID: string(s.UID),
		Workspace: p.Workspace, Repo: p.Repo, SourcePodUID: rec.SourcePodUID, HoldPodUID: rec.PodUID, PrivateHomeUID: p.PrivateHomeUID,
		WriterGeneration: p.WriterGeneration, NoOwner: p.NoOwner, RescueGeneration: rec.Generation, RescueResult: rec.Result,
		PreservationKind: p.Kind, RescueStamp: rec.Stamp, RescueRecordedAt: *rec.At, RescueManifest: p.Manifest,
		PreservedOwnersSHA256: hex.EncodeToString(sum[:])}
	data, err = json.Marshal(receipt)
	return string(data), err
}

func (r *Reconciler) readRetainedHome(ctx context.Context, s *v1alpha1.AgentSession) (*corev1.PersistentVolumeClaim, error) {
	var home corev1.PersistentVolumeClaim
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: HomeClaimName(s.Name)}, &home); err != nil {
		return nil, err
	}
	if home.UID == "" || string(home.UID) != s.Status.SharedPrivateHomeUID || !home.DeletionTimestamp.IsZero() {
		return nil, errors.New("private home is missing, replaced or deleting; retention is unconfirmed")
	}
	return &home, nil
}

func confirmedRetention(home *corev1.PersistentVolumeClaim, s *v1alpha1.AgentSession, receipt string) error {
	if home.Annotations[privateHomeReceiptAnnotation] != receipt || home.Labels[retainedPrivateHomeLabel] != "true" ||
		home.Labels[retainedSessionUIDLabel] != string(s.UID) {
		return errors.New("private home retention receipt or discovery labels are unconfirmed")
	}
	owners, err := retentionOwners(home, s, true)
	if err != nil {
		return err
	}
	actual, err := retentionReceipt(s, owners)
	if err != nil || actual != receipt {
		return errors.New("private home retention evidence or preserved owners changed")
	}
	return nil
}

// A shared task is reaped by retaining its original private PVC, never by
// archive/delete. Every uncertain write preserves the Session finalizer.
func (r *Reconciler) retainSharedPrivateHome(ctx context.Context, s *v1alpha1.AgentSession, obs *observation) (ctrl.Result, error) {
	retry := ctrl.Result{RequeueAfter: 5 * time.Second}
	block := func(err error) (ctrl.Result, error) { obs.removalBlocked = err; return retry, nil }
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		return block(err)
	}
	if fresh.UID != s.UID || fresh.Generation != s.Generation || !apiequality.Semantic.DeepEqual(fresh.Status.Rescue, s.Status.Rescue) ||
		!apiequality.Semantic.DeepEqual(fresh.Status.SharedAdmission, s.Status.SharedAdmission) || fresh.Status.SharedPrivateHomeUID != s.Status.SharedPrivateHomeUID {
		return block(errors.New("shared Session changed before retention"))
	}
	if sharedAdmissionIs(&fresh, sharedNeverStarted) {
		if err := r.releaseNeverStartedSharedSession(ctx, &fresh); err != nil {
			return block(err)
		}
		*s = fresh
		obs.removalBlocked = nil
		return ctrl.Result{}, nil
	}
	if err := sharedRetentionProof(&fresh); err != nil {
		return block(err)
	}
	if err := r.sharedPodsAbsent(ctx, &fresh); err != nil {
		return block(err)
	}
	home, err := r.readRetainedHome(ctx, &fresh)
	if err != nil {
		return block(err)
	}
	detached := home.Annotations[privateHomeReceiptAnnotation] != ""
	owners, err := retentionOwners(home, &fresh, detached)
	if err != nil {
		return block(err)
	}
	receipt, err := retentionReceipt(&fresh, owners)
	if err != nil {
		return block(err)
	}
	if detached {
		if err := confirmedRetention(home, &fresh, receipt); err != nil {
			return block(err)
		}
	} else {
		if home.ResourceVersion == "" || home.Labels[retainedPrivateHomeLabel] != "" || home.Labels[retainedSessionUIDLabel] != "" {
			return block(errors.New("private home retention metadata conflicts or has no write precondition"))
		}
		if err := r.sharedPodsAbsent(ctx, &fresh); err != nil {
			return block(err)
		}
		if err := r.detachPrivateHome(ctx, home, &fresh, owners, receipt); err != nil {
			return block(fmt.Errorf("private home retention write is unconfirmed: %w", err))
		}
	}
	// Confirm independently even after a successful acknowledgement. A lost
	// acknowledgement is recovered only on a later reconcile through this path.
	confirmed, err := r.readRetainedHome(ctx, &fresh)
	if err != nil {
		return block(err)
	}
	if err := confirmedRetention(confirmed, &fresh, receipt); err != nil {
		return block(err)
	}
	var latest v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(&fresh), &latest); err != nil {
		return block(err)
	}
	if latest.UID != fresh.UID || latest.Generation != fresh.Generation || latest.Status.SharedPrivateHomeUID != fresh.Status.SharedPrivateHomeUID ||
		!apiequality.Semantic.DeepEqual(latest.Status.Rescue, fresh.Status.Rescue) {
		return block(errors.New("shared Session changed after retention; finalization refused"))
	}
	if err := sharedRetentionProof(&latest); err != nil {
		return block(err)
	}
	if err := r.sharedPodsAbsent(ctx, &latest); err != nil {
		return block(err)
	}
	// Recheck the claim after both Pod names, immediately before Session release.
	confirmed, err = r.readRetainedHome(ctx, &latest)
	if err != nil {
		return block(err)
	}
	if err := confirmedRetention(confirmed, &latest, receipt); err != nil {
		return block(err)
	}
	if err := r.releaseRetainedSharedSession(ctx, &latest); err != nil {
		return block(err)
	}
	*s = latest
	obs.removalBlocked = nil
	return ctrl.Result{}, nil
}

// UID and resourceVersion tests prevent a replacement or metadata change from
// inheriting the conditional detach. The receipt, labels and one exact owner
// removal commit atomically; every other field is copied unchanged.
func (r *Reconciler) detachPrivateHome(ctx context.Context, home *corev1.PersistentVolumeClaim, s *v1alpha1.AgentSession, owners []metav1.OwnerReference, receipt string) error {
	if !controlledBySessionObject(home, s) || !home.DeletionTimestamp.IsZero() || string(home.UID) != s.Status.SharedPrivateHomeUID {
		return errors.New("private home ownership changed before detach")
	}
	annotations := home.DeepCopy().Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[privateHomeReceiptAnnotation] = receipt
	labels := home.DeepCopy().Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labels[retainedPrivateHomeLabel] = "true"
	labels[retainedSessionUIDLabel] = string(s.UID)
	patch := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(home.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": home.ResourceVersion},
		{"op": "replace", "path": "/metadata/ownerReferences", "value": owners},
		{"op": "add", "path": "/metadata/annotations", "value": annotations},
		{"op": "add", "path": "/metadata/labels", "value": labels},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return r.Client.Patch(ctx, home, client.RawPatch(types.JSONPatchType, data))
}

func (r *Reconciler) releaseRetainedSharedSession(ctx context.Context, s *v1alpha1.AgentSession) error {
	if err := sharedRetentionProof(s); err != nil {
		return err
	}
	finalizers := slices.DeleteFunc(slices.Clone(s.Finalizers), func(v string) bool { return v == Finalizer })
	data, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(s.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": s.ResourceVersion},
		{"op": "replace", "path": "/metadata/finalizers", "value": finalizers},
	})
	if err != nil {
		return err
	}
	if err := r.Client.Patch(ctx, s, client.RawPatch(types.JSONPatchType, data)); err != nil {
		return err
	}
	s.Finalizers = finalizers
	return nil
}
