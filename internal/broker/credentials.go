package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The broker pins the first accepted execution object's UID before trusting any
// receipt. A replacement cannot mint again or finish the old object's cleanup.
const credentialJobUIDAnnotation = v1alpha1.LabelPrefix + "credential-job-uid"

// This broker-owned receipt survives deletion of the keeper's execution object
// so a conflicting grant-finalizer update can retry without losing proof of
// revocation. It is cleanup evidence, never permission to install or remint.
const credentialCleanupReceiptAnnotation = v1alpha1.LabelPrefix + "credential-cleanup-receipt"

type credentialCleanupReceipt struct {
	GrantUID  types.UID   `json:"grantUID"`
	JobUID    types.UID   `json:"jobUID"`
	RevokedAt metav1.Time `json:"revokedAt"`
}

func credentialCleanupConfirmed(g *v1alpha1.AccessGrant, now time.Time) bool {
	pinned := g.Annotations[credentialJobUIDAnnotation]
	if g.UID == "" || pinned == "" {
		return false
	}
	var receipt credentialCleanupReceipt
	if err := json.Unmarshal([]byte(g.Annotations[credentialCleanupReceiptAnnotation]), &receipt); err != nil {
		return false
	}
	return receipt.GrantUID == g.UID && string(receipt.JobUID) == pinned &&
		!receipt.RevokedAt.IsZero() && !receipt.RevokedAt.Before(&g.CreationTimestamp) && !receipt.RevokedAt.After(now)
}

func (b *Broker) recordCredentialCleanup(ctx context.Context, g *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob) error {
	receipt := credentialCleanupReceipt{GrantUID: g.UID, JobUID: j.UID, RevokedAt: *j.Status.RevokedAt}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode the credential cleanup receipt: %w", err)
	}
	if g.Annotations[credentialCleanupReceiptAnnotation] == string(encoded) {
		return nil
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[credentialCleanupReceiptAnnotation] = string(encoded)
	if err := b.Client.Patch(ctx, g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("record the credential cleanup receipt: %w", err)
	}
	return nil
}

var errCredentialCleanupPending = errors.New("keeper credential cleanup is pending")

func credentialApproval(g *v1alpha1.AccessGrant) bool {
	return strings.HasPrefix(g.Status.ApprovedBy, PolicyApprover) && strings.TrimPrefix(g.Status.ApprovedBy, PolicyApprover) != "" &&
		g.Status.ApprovedAt != nil && g.Status.ApprovedTTL != nil && g.Status.ExpiresAt != nil &&
		g.Status.ApprovedTTL.Duration >= MinTTL && g.Status.ApprovedTTL.Duration <= g.Spec.TTL.Duration &&
		g.Status.ApprovedTTL.Duration <= MaxCredentialTTL &&
		g.Status.ExpiresAt.Time.Equal(g.Status.ApprovedAt.Add(g.Status.ApprovedTTL.Duration))
}

func (b *Broker) credentialJob(ctx context.Context, g *v1alpha1.AccessGrant) (*v1alpha1.CredentialJob, error) {
	var j v1alpha1.CredentialJob
	key := types.NamespacedName{Namespace: b.PolicyNamespace, Name: v1alpha1.CredentialJobName(g.UID)}
	if err := b.APIReader.Get(ctx, key, &j); err != nil {
		if apierrors.IsNotFound(err) && g.Annotations[credentialJobUIDAnnotation] == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("read the credential execution job: %w", err)
	}
	if !b.ownCredentialJob(g, &j) {
		return nil, permanent{errors.New("credential execution job does not belong to this grant and requesting session")}
	}
	if uid := g.Annotations[credentialJobUIDAnnotation]; uid != "" && uid != string(j.UID) {
		return nil, permanent{errors.New("credential execution job was replaced; its receipt cannot finish the original job")}
	}
	return &j, nil
}

func (b *Broker) ownCredentialJob(g *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob) bool {
	return g.UID != "" && g.Spec.Requester.SessionUID != "" && g.Status.ExpiresAt != nil &&
		g.Spec.Credential != nil && g.Spec.Credential.Name == v1alpha1.CredentialProxmox &&
		j.Namespace == b.PolicyNamespace && j.Name == v1alpha1.CredentialJobName(g.UID) && j.UID != "" &&
		j.Spec.Grant == (v1alpha1.CredentialObjectReference{Namespace: g.Namespace, Name: g.Name, UID: g.UID}) &&
		j.Spec.Session == (v1alpha1.CredentialObjectReference{Namespace: b.SessionNamespace, Name: g.Spec.Requester.Session, UID: g.Spec.Requester.SessionUID}) &&
		j.Spec.Credential == v1alpha1.CredentialProxmox && j.Spec.ExpiresAt.Equal(g.Status.ExpiresAt) &&
		j.Labels[v1alpha1.LabelGrant] == g.Name && j.Labels[v1alpha1.LabelSession] == g.Spec.Requester.Session &&
		j.Labels[v1alpha1.LabelManagedBy] == ManagedBy
}

func (b *Broker) pinCredentialJob(ctx context.Context, g *v1alpha1.AccessGrant, j *v1alpha1.CredentialJob) error {
	if uid := g.Annotations[credentialJobUIDAnnotation]; uid != "" {
		if uid != string(j.UID) {
			return errors.New("credential execution job UID changed")
		}
		return nil
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[credentialJobUIDAnnotation] = string(j.UID)
	if err := b.Client.Patch(ctx, g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("pin the credential execution job UID: %w", err)
	}
	return nil
}

func (b *Broker) ensureCredentialJob(ctx context.Context, g *v1alpha1.AccessGrant) (*v1alpha1.CredentialJob, error) {
	j, err := b.credentialJob(ctx, g)
	if err != nil {
		return nil, err
	}
	if j == nil {
		if !b.EnableProxmoxGrants {
			return nil, permanent{errors.New("new Proxmox credential execution is disabled")}
		}
		// A job is execution, never authority. Confirm the exact approved policy
		// still permits this request before making its first execution object.
		var p v1alpha1.GrantPolicy
		key := types.NamespacedName{Namespace: b.PolicyNamespace, Name: strings.TrimPrefix(g.Status.ApprovedBy, PolicyApprover)}
		if err := b.APIReader.Get(ctx, key, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, permanent{errors.New("the credential approving policy is absent")}
			}
			return nil, fmt.Errorf("read the credential approving policy: %w", err)
		}
		if !p.DeletionTimestamp.IsZero() || !Matches(&p, g) {
			return nil, permanent{errors.New("the credential approving policy does not permit this request")}
		}
		j = &v1alpha1.CredentialJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: b.PolicyNamespace, Name: v1alpha1.CredentialJobName(g.UID), Labels: grantLabels(g)},
			Spec: v1alpha1.CredentialJobSpec{
				Grant:      v1alpha1.CredentialObjectReference{Namespace: g.Namespace, Name: g.Name, UID: g.UID},
				Session:    v1alpha1.CredentialObjectReference{Namespace: b.SessionNamespace, Name: g.Spec.Requester.Session, UID: g.Spec.Requester.SessionUID},
				Credential: v1alpha1.CredentialProxmox, ExpiresAt: *g.Status.ExpiresAt,
			},
		}
		if err := b.Client.Create(ctx, j); err != nil {
			if apierrors.IsAlreadyExists(err) {
				j, err = b.credentialJob(ctx, g)
				if err != nil {
					return nil, err
				}
				if j == nil {
					return nil, errors.New("credential execution job disappeared after create conflict")
				}
			} else {
				// A lost response must be recovered by reading the accepted object.
				// Never create another name, expiry or provider identity on retry.
				return nil, fmt.Errorf("create the credential execution job: %w", err)
			}
		}
	}
	if err := b.pinCredentialJob(ctx, g, j); err != nil {
		return nil, err
	}
	return j, nil
}

func (b *Broker) activeCredential(ctx context.Context, g *v1alpha1.AccessGrant, s *v1alpha1.AgentSession, now metav1.Time) (ctrl.Result, error) {
	if !credentialApproval(g) {
		return b.end(ctx, g, v1alpha1.GrantFailed, "", "credential execution has no valid standing-policy approval", now)
	}
	j, err := b.ensureCredentialJob(ctx, g)
	if err != nil {
		if p := (permanent{}); errors.As(err, &p) {
			return b.end(ctx, g, v1alpha1.GrantFailed, "", err.Error(), now)
		}
		return ctrl.Result{}, err
	}
	if j.Spec.Release || !j.DeletionTimestamp.IsZero() || j.Status.Phase == v1alpha1.CredentialCleanupPending || j.Status.Phase == v1alpha1.CredentialRevoked {
		msg := "keeper credential execution ended"
		if j.Status.FailureCode != "" {
			msg += ": " + string(j.Status.FailureCode)
		}
		return b.end(ctx, g, v1alpha1.GrantFailed, "", msg, now)
	}
	res := ctrl.Result{RequeueAfter: min(installRetry, g.Status.ExpiresAt.Sub(now.Time))}
	pod := b.sessionPod(ctx, s, true)
	installed := j.Status.Phase == v1alpha1.CredentialInstalled && j.Status.FailureCode == "" &&
		pod != nil && j.Status.InstalledPodUID == string(pod.UID) && j.Status.InstalledAt != nil &&
		!j.Status.InstalledAt.Before(g.Status.ApprovedAt) && !j.Status.InstalledAt.After(b.Clock.Now()) &&
		j.Status.InstalledAt.Before(g.Status.ExpiresAt) && j.Status.ProviderID == "dev-env@pve!grant-"+string(g.UID)
	if installed {
		if g.Status.InstalledPodUID != j.Status.InstalledPodUID || !g.Status.InstalledAt.Equal(j.Status.InstalledAt) {
			g.Status.InstalledPodUID, g.Status.InstalledAt = j.Status.InstalledPodUID, j.Status.InstalledAt.DeepCopy()
			if _, err := b.writeStatus(ctx, g); err != nil {
				return ctrl.Result{}, err
			}
		}
		res.RequeueAfter = g.Status.ExpiresAt.Sub(now.Time)
	} else if g.Status.InstalledPodUID != "" || g.Status.InstalledAt != nil {
		g.Status.InstalledPodUID, g.Status.InstalledAt = "", nil
		if _, err := b.writeStatus(ctx, g); err != nil {
			return ctrl.Result{}, err
		}
	}
	return res, nil
}

func (b *Broker) revokeCredential(ctx context.Context, g *v1alpha1.AccessGrant) error {
	j, err := b.credentialJob(ctx, g)
	if apierrors.IsNotFound(err) && !g.DeletionTimestamp.IsZero() && credentialCleanupConfirmed(g, b.Clock.Now()) {
		return nil
	}
	if err != nil || j == nil {
		return err
	}
	if err := b.pinCredentialJob(ctx, g, j); err != nil {
		return err
	}
	if !j.Spec.Release {
		orig := j.DeepCopy()
		j.Spec.Release = true
		if err := b.Client.Patch(ctx, j, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("release the credential execution job: %w", err)
		}
	}
	if j.Status.Phase != v1alpha1.CredentialRevoked || j.Status.RevokedAt == nil ||
		j.Status.RevokedAt.Before(&j.CreationTimestamp) || j.Status.RevokedAt.Before(&g.CreationTimestamp) || j.Status.RevokedAt.After(b.Clock.Now()) {
		return errCredentialCleanupPending
	}
	if !g.DeletionTimestamp.IsZero() {
		if err := b.recordCredentialCleanup(ctx, g, j); err != nil {
			return err
		}
		if err := b.Client.Delete(ctx, j, client.Preconditions{UID: &j.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete the revoked credential execution job: %w", err)
		}
	}
	return nil
}
