package broker

import (
	"context"
	"fmt"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// CheckHardwareIntentLease reads metadata only. It reuses the existing C4
// ownership, pinned keeper receipt and standing-policy approval contract; it
// cannot approve, create, extend, install, use or rotate a credential.
func (b *Broker) CheckHardwareIntentLease(ctx context.Context, g *v1alpha1.AccessGrant, podUID string, now time.Time) error {
	if g.Spec.Type != v1alpha1.GrantCredential || !credentialApproval(g) || g.Status.Phase != v1alpha1.GrantActive ||
		g.Spec.Release || !g.DeletionTimestamp.IsZero() || !now.Before(g.Status.ExpiresAt.Time) || g.Status.ApprovedAt.After(now) ||
		podUID == "" || g.Status.InstalledPodUID != podUID || g.Status.InstalledAt == nil ||
		g.Status.InstalledAt.Before(g.Status.ApprovedAt) || g.Status.InstalledAt.After(now) || g.Annotations[credentialJobUIDAnnotation] == "" {
		return fmt.Errorf("%w: hardware intent requires a valid installed Active C4 lease", ErrRefused)
	}
	j, err := b.credentialJob(ctx, g)
	if err != nil {
		return err
	}
	if j == nil || j.Spec.Release || !j.DeletionTimestamp.IsZero() || j.Status.Phase != v1alpha1.CredentialInstalled ||
		j.Status.FailureCode != "" || j.Status.RevokedAt != nil || j.Status.ProviderID != v1alpha1.CredentialProviderID(g.Spec.Credential.Name, g.UID) ||
		j.Status.InstalledPodUID != podUID || j.Status.InstalledAt == nil || !j.Status.InstalledAt.Equal(g.Status.InstalledAt) {
		return fmt.Errorf("%w: hardware intent lease has no current pinned keeper installation receipt", ErrRefused)
	}
	return nil
}
