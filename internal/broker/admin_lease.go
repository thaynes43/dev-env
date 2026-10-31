package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// AdminIssuanceNamespace is a once-reviewed security boundary, not a session
// placement setting. Changing it requires new security fixtures and approval.
const AdminIssuanceNamespace = "dev-env-admin-issuance"

func (b *Broker) adminLease(g *v1alpha1.AccessGrant) bool {
	return g.Status.AdminCredential != nil || (b.EnableAdminLeases && g.Spec.Type == v1alpha1.GrantBreakglass)
}

func (b *Broker) adminAuthority(ctx context.Context, g *v1alpha1.AccessGrant) error {
	if !b.EnableAdminLeases || b.AdminIssuerNamespace != AdminIssuanceNamespace || b.AdminIssuerNamespace == b.SessionNamespace {
		return permanent{errors.New("admin issuer is disabled or unconfigured")}
	}
	if g.UID == "" || g.Namespace != b.SessionNamespace || ReservedClass(g) != grantattest.ClassRD2 ||
		g.Status.Reserved == nil || !strings.HasPrefix(g.Status.ApprovedBy, DelegateApprover) || g.Status.Phase != v1alpha1.GrantActive || g.Spec.Release || !g.DeletionTimestamp.IsZero() ||
		g.Status.ApprovedAt == nil || g.Status.ApprovedTTL == nil || g.Status.ApprovedTTL.Duration != 30*time.Minute ||
		g.Status.ExpiresAt == nil || !g.Status.ExpiresAt.Time.Equal(g.Status.ApprovedAt.Add(30*time.Minute)) || !b.Clock.Now().Before(g.Status.ExpiresAt.Time) {
		return permanent{errors.New("admin lease lacks a current bound 30-minute reserved approval")}
	}
	if a := g.Status.AdminCredential; a != nil && (a.Namespace != b.AdminIssuerNamespace || a.Name != g.Name || a.CleanupStarted) {
		return permanent{errors.New("original admin credential differs or cleanup has started")}
	}
	// Reuse the existing actor, decision, requester UID and live reach semantics.
	return b.delegatedHolds(ctx, g)
}

func (b *Broker) saveAdmin(ctx context.Context, g *v1alpha1.AccessGrant) error {
	written, err := b.writeStatus(ctx, g)
	if err != nil {
		return err
	}
	if !written {
		return errors.New("admin credential status changed; retry from the original record")
	}
	return nil
}

func (b *Broker) ensureAdmin(ctx context.Context, g *v1alpha1.AccessGrant) ([]string, error) {
	if err := b.adminAuthority(ctx, g); err != nil {
		return nil, err
	}
	if g.Status.AdminCredential == nil {
		g.Status.AdminCredential = &v1alpha1.AdminCredentialStatus{Namespace: b.AdminIssuerNamespace, Name: g.Name}
		if err := b.saveAdmin(ctx, g); err != nil {
			return nil, err
		}
	}
	a := g.Status.AdminCredential
	labels := grantLabels(g)
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: a.Name, Labels: labels}, AutomountServiceAccountToken: ptr.To(false)}
	if err := b.ensureAdminObject(ctx, g, sa, &corev1.ServiceAccount{}, a.ServiceAccountUID, func(o client.Object) bool {
		have := o.(*corev1.ServiceAccount)
		return have.AutomountServiceAccountToken != nil && !*have.AutomountServiceAccountToken
	}); err != nil {
		return nil, err
	}
	if a.ServiceAccountUID == "" {
		a.ServiceAccountUID = string(sa.UID)
		if err := b.saveAdmin(ctx, g); err != nil {
			return nil, err
		}
		a = g.Status.AdminCredential
	}
	if err := b.adminAuthority(ctx, g); err != nil {
		return nil, err
	}
	crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: g.Name, Labels: labels},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleBreakglass},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: a.Namespace, Name: a.Name}}}
	if err := b.ensureAdminObject(ctx, g, crb, &rbacv1.ClusterRoleBinding{}, a.BindingUID, func(o client.Object) bool {
		have := o.(*rbacv1.ClusterRoleBinding)
		return have.RoleRef == crb.RoleRef && apiequality.Semantic.DeepEqual(have.Subjects, crb.Subjects)
	}); err != nil {
		return nil, err
	}
	if a.BindingUID == "" {
		a.BindingUID = string(crb.UID)
		if err := b.saveAdmin(ctx, g); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// A persisted UID is never replaced, including after deletion. Before the first
// UID receipt, recover only an exact owned object; no token can have been minted.
func (b *Broker) ensureAdminObject(ctx context.Context, g *v1alpha1.AccessGrant, want, have client.Object, uid string, same func(client.Object) bool) error {
	err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(want), have)
	if apierrors.IsNotFound(err) && uid != "" {
		return permanent{errors.New("original admin object disappeared; request a new lease")}
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && uid != "" && string(have.GetUID()) != uid {
		return permanent{errors.New("original admin object UID changed")}
	}
	if _, err := b.ensureOne(ctx, g, want, have, func(o client.Object) bool { return adminOwned(g, o) && same(o) }); err != nil {
		return err
	}
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(want), want); err != nil {
		return err
	}
	if want.GetUID() == "" || (uid != "" && string(want.GetUID()) != uid) || !adminOwned(g, want) || !want.GetDeletionTimestamp().IsZero() {
		return permanent{errors.New("admin object identity is missing or changed")}
	}
	return nil
}

func adminOwned(g *v1alpha1.AccessGrant, o client.Object) bool {
	labels := o.GetLabels()
	return labels[v1alpha1.LabelGrant] == g.Name && labels[v1alpha1.LabelPrefix+"grant-uid"] == string(g.UID) && labels[v1alpha1.LabelManagedBy] == ManagedBy
}

func (b *Broker) checkAdminObjects(ctx context.Context, g *v1alpha1.AccessGrant) error {
	a := g.Status.AdminCredential
	if a == nil || a.ServiceAccountUID == "" || a.BindingUID == "" {
		return errors.New("original admin identity was not persisted")
	}
	for _, item := range []struct {
		o   client.Object
		uid string
	}{
		{&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: a.Name}}, a.ServiceAccountUID},
		{&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: g.Name}}, a.BindingUID},
	} {
		if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(item.o), item.o); err != nil {
			return err
		}
		if string(item.o.GetUID()) != item.uid || !adminOwned(g, item.o) || !item.o.GetDeletionTimestamp().IsZero() {
			return errors.New("original admin UID or ownership changed")
		}
		if binding, ok := item.o.(*rbacv1.ClusterRoleBinding); ok {
			expected := []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: a.Namespace, Name: a.Name}}
			if binding.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleBreakglass}) || !apiequality.Semantic.DeepEqual(binding.Subjects, expected) {
				return errors.New("original admin root binding subject or role changed")
			}
		}

	}
	return nil
}

func (b *Broker) mintAdmin(ctx context.Context, g *v1alpha1.AccessGrant) (string, time.Time, error) {
	var live v1alpha1.AccessGrant
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(g), &live); err != nil {
		return "", time.Time{}, err
	}
	if live.UID != g.UID || !apiequality.Semantic.DeepEqual(live.Status.AdminCredential, g.Status.AdminCredential) {
		return "", time.Time{}, errors.New("admin lease identity changed before mint")
	}
	if err := b.adminAuthority(ctx, &live); err != nil {
		return "", time.Time{}, err
	}
	if err := b.checkAdminObjects(ctx, &live); err != nil {
		return "", time.Time{}, err
	}
	// Never let TokenRequest's 600-second minimum extend the fixed approval.
	secs := int64(live.Status.ExpiresAt.Sub(b.Clock.Now()) / time.Second)
	if secs < MinTokenSeconds {
		return "", time.Time{}, errors.New("admin lease has less than the minimum token lifetime remaining")
	}
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &secs}}
	a := live.Status.AdminCredential
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: a.Name, UID: types.UID(a.ServiceAccountUID)}}
	if err := b.Client.SubResource("token").Create(ctx, sa, tr); err != nil {
		return "", time.Time{}, errors.New("admin TokenRequest failed")
	}
	expiry := tr.Status.ExpirationTimestamp.Time
	if tr.Status.Token == "" || !b.Clock.Now().Before(expiry) || expiry.After(live.Status.ExpiresAt.Time) {
		return "", time.Time{}, errors.New("admin TokenRequest expiry is outside the fixed approval deadline")
	}
	// Recheck after mint: a concurrent release or replacement must never deliver.
	var latest v1alpha1.AccessGrant
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(g), &latest); err != nil {
		return "", time.Time{}, err
	}
	if latest.UID != live.UID || !apiequality.Semantic.DeepEqual(latest.Status.AdminCredential, live.Status.AdminCredential) {
		return "", time.Time{}, errors.New("admin lease changed during mint")
	}
	if err := b.adminAuthority(ctx, &latest); err != nil {
		return "", time.Time{}, err
	}
	if err := b.checkAdminObjects(ctx, &latest); err != nil {
		return "", time.Time{}, err
	}
	return tr.Status.Token, expiry, nil
}

func (b *Broker) revokeAdmin(ctx context.Context, g *v1alpha1.AccessGrant) (retErr error) {
	a := g.Status.AdminCredential
	if a.Namespace == "" || a.Name != g.Name {
		return errors.New("original admin cleanup identity is incomplete")
	}
	if !a.CleanupStarted {
		a.CleanupStarted = true
		g.Status.Message = "admin credential cleanup pending"
		if err := b.saveAdmin(ctx, g); err != nil {
			return err
		}
		a = g.Status.AdminCredential
	}
	defer func() {
		a.CleanupError = ""
		if retErr != nil {
			a.CleanupError = truncate(retErr.Error())
			g.Status.Message = truncate("admin credential cleanup pending: " + a.CleanupError)
		}
		if err := b.saveAdmin(ctx, g); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	// Confirm the original root binding is gone before touching the account.
	if err := b.deleteAdminObject(ctx, g, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: g.Name}}, a.BindingUID); err != nil {
		return err
	}
	if err := b.deleteAdminObject(ctx, g, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: a.Name}}, a.ServiceAccountUID); err != nil {
		return err
	}
	// Use the grant's original namespace, never a changed SessionNamespace.
	var pod corev1.Pod
	err := b.APIReader.Get(ctx, client.ObjectKey{Namespace: g.Namespace, Name: g.Spec.Requester.Session}, &pod)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read original requester Pod for admin file cleanup: %w", err)
	}
	if b.Installer == nil {
		return errors.New("admin token-file cleanup installer is unavailable")
	}
	if !adminRequesterPod(&pod, g) {
		return errors.New("requester Pod ownership differs during admin file cleanup")
	}
	return b.Installer.Remove(ctx, &pod, g)
}

func adminRequesterPod(p *corev1.Pod, g *v1alpha1.AccessGrant) bool {
	for _, ref := range p.OwnerReferences {
		if ref.UID == g.Spec.Requester.SessionUID && ref.Name == g.Spec.Requester.Session && ref.APIVersion == v1alpha1.GroupVersion.String() && ref.Controller != nil && *ref.Controller && ref.Kind == "AgentSession" {
			return true
		}
	}
	return false
}

func (b *Broker) deleteAdminObject(ctx context.Context, g *v1alpha1.AccessGrant, o client.Object, uid string) error {
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !adminOwned(g, o) || (uid != "" && string(o.GetUID()) != uid) {
		return fmt.Errorf("admin cleanup refused changed UID/ownership for %s", describe(o))
	}
	original := o.GetUID()
	if err := b.Client.Delete(ctx, o, client.Preconditions{UID: &original}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := b.APIReader.Get(ctx, client.ObjectKeyFromObject(o), o); err == nil {
		return fmt.Errorf("admin cleanup waits for deletion of %s", describe(o))
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
