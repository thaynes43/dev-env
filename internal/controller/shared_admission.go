package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const (
	sharedNeverStarted = "NeverStarted"
	sharedStarted      = "Started"
)

func sharedAdmissionIs(s *v1alpha1.AgentSession, state string) bool {
	a := s.Status.SharedAdmission
	return s.UID != "" && a != nil && a.Version == 1 && a.SessionUID == string(s.UID) && a.State == state
}

// Control-state observations (Pending, build failures and suspension) can
// exist before admission. Resource, provider and rescue history cannot.
func sharedNoResourceHistory(s *v1alpha1.AgentSession) bool {
	st := &s.Status
	return st.PodName == "" && st.SharedPrivateHomeUID == "" && st.Revision == "" && st.NodeName == "" &&
		st.Agent == nil && st.RemoteControl == nil && st.Outcome == nil && st.Usage == nil && st.Rescue == nil && st.ArchivedAt == nil
}

func (r *Reconciler) sharedResourcesAbsent(ctx context.Context, s *v1alpha1.AgentSession) error {
	if err := r.sharedPodsAbsent(ctx, s); err != nil {
		return err
	}
	var home corev1.PersistentVolumeClaim
	err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: HomeClaimName(s.Name)}, &home)
	if err == nil {
		return fmt.Errorf("private home %s (UID %s) exists; never-started release is refused", home.Name, home.UID)
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("private home absence is unconfirmed: %w", err)
	}
	return nil
}

// D-45's explicit fresh path precedes the finalizer and every resource write.
// Deleting, finalized or observed legacy Sessions are never backfilled.
func (r *Reconciler) initializeSharedAdmission(ctx context.Context, s *v1alpha1.AgentSession) error {
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		return err
	}
	if fresh.UID != s.UID || fresh.Generation != s.Generation || fresh.Spec.Workspace == nil || wantsPodGone(&fresh) ||
		controllerutil.ContainsFinalizer(&fresh, Finalizer) || !sharedNoResourceHistory(&fresh) || fresh.Status.Phase != "" ||
		fresh.Status.PendingReason != "" || fresh.Status.SuspendedAt != nil || len(fresh.Status.Conditions) != 0 {
		return errors.New("shared admission cannot be initialized outside the fresh pre-finalizer path")
	}
	if fresh.Status.SharedAdmission != nil && !sharedAdmissionIs(&fresh, sharedNeverStarted) {
		return errors.New("fresh shared admission identity is inconsistent")
	}
	if err := r.sharedResourcesAbsent(ctx, &fresh); err != nil {
		return err
	}
	if fresh.Status.SharedAdmission == nil {
		fresh.Status.SharedAdmission = &v1alpha1.SharedAdmissionStatus{Version: 1, SessionUID: string(fresh.UID), State: sharedNeverStarted}
		if err := r.Client.Status().Update(ctx, &fresh); err != nil {
			return fmt.Errorf("fresh shared admission write is unconfirmed: %w", err)
		}
	}
	var confirmed v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &confirmed); err != nil {
		return err
	}
	if confirmed.UID != fresh.UID || confirmed.Generation != fresh.Generation || confirmed.ResourceVersion != fresh.ResourceVersion ||
		wantsPodGone(&confirmed) || controllerutil.ContainsFinalizer(&confirmed, Finalizer) || !sharedAdmissionIs(&confirmed, sharedNeverStarted) || !sharedNoResourceHistory(&confirmed) {
		return errors.New("fresh shared admission changed or is unconfirmed before finalizer")
	}
	if err := r.sharedResourcesAbsent(ctx, &confirmed); err != nil {
		return err
	}
	*s = confirmed
	return nil
}

// A one-way durable Started acknowledgement precedes PVC, executor and hold
// creation. Even a committed write with a lost acknowledgement stops this turn.
func (r *Reconciler) startSharedAdmission(ctx context.Context, s *v1alpha1.AgentSession) error {
	var fresh v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
		return err
	}
	if fresh.UID != s.UID || fresh.Generation != s.Generation || fresh.Spec.Workspace == nil || !controllerutil.ContainsFinalizer(&fresh, Finalizer) {
		return errors.New("shared Session changed before resource admission")
	}
	if !sharedAdmissionIs(&fresh, sharedStarted) {
		if !sharedAdmissionIs(&fresh, sharedNeverStarted) || wantsPodGone(&fresh) || !sharedNoResourceHistory(&fresh) {
			return errors.New("shared resource admission is unknown or inconsistent; migration is refused")
		}
		if err := r.sharedResourcesAbsent(ctx, &fresh); err != nil {
			return err
		}
		fresh.Status.SharedAdmission.State = sharedStarted
		if err := r.Client.Status().Update(ctx, &fresh); err != nil {
			return fmt.Errorf("shared Started write is unconfirmed: %w", err)
		}
	}
	var confirmed v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &confirmed); err != nil {
		return err
	}
	if confirmed.UID != fresh.UID || confirmed.Generation != fresh.Generation || confirmed.ResourceVersion != fresh.ResourceVersion ||
		!controllerutil.ContainsFinalizer(&confirmed, Finalizer) || !sharedAdmissionIs(&confirmed, sharedStarted) {
		return errors.New("shared Started acknowledgement changed or is unconfirmed")
	}
	// A deleting/suspended lifecycle may create a hold, but never its executor.
	if !wantsPodGone(s) && wantsPodGone(&confirmed) {
		return errors.New("shared Session stopped before resource admission")
	}
	*s = confirmed
	return nil
}

func sharedNeverStartedReleaseAllowed(s *v1alpha1.AgentSession) error {
	if s.Spec.Workspace == nil || s.Spec.Mode != v1alpha1.ModeTask || s.DeletionTimestamp.IsZero() ||
		!controllerutil.ContainsFinalizer(s, Finalizer) || !sharedAdmissionIs(s, sharedNeverStarted) || !sharedNoResourceHistory(s) {
		return errors.New("shared reap has no authoritative never-started lifecycle")
	}
	return nil
}

// This releases no volume and writes no retention receipt. NeverStarted means
// no resource attempt, not NoWorkAdmitted and never an empty provider home.
func (r *Reconciler) releaseNeverStartedSharedSession(ctx context.Context, s *v1alpha1.AgentSession) error {
	if err := sharedNeverStartedReleaseAllowed(s); err != nil {
		return err
	}
	if err := r.sharedResourcesAbsent(ctx, s); err != nil {
		return err
	}
	var latest v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &latest); err != nil {
		return err
	}
	if latest.UID != s.UID || latest.Generation != s.Generation || latest.ResourceVersion != s.ResourceVersion {
		return errors.New("never-started Session changed before release")
	}
	if err := sharedNeverStartedReleaseAllowed(&latest); err != nil {
		return err
	}
	if err := r.sharedResourcesAbsent(ctx, &latest); err != nil {
		return err
	}
	finalizers := slices.DeleteFunc(slices.Clone(latest.Finalizers), func(v string) bool { return v == Finalizer })
	data, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(latest.UID)},
		{"op": "test", "path": "/metadata/resourceVersion", "value": latest.ResourceVersion},
		{"op": "replace", "path": "/metadata/finalizers", "value": finalizers},
	})
	if err != nil {
		return err
	}
	if err := r.Client.Patch(ctx, &latest, client.RawPatch(types.JSONPatchType, data)); err != nil {
		return err
	}
	*s = latest
	return nil
}
