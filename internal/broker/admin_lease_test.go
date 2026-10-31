package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

type adminUnitClient struct {
	client.Client
	f                 *dgFixture
	mints             int
	issuedNamespace   string
	requestSeconds    int64
	expiryOffset      time.Duration
	responseDelay     time.Duration
	releaseDuringMint bool
	deletes           []string
	failDelete        string
	failCreate        bool
}

func (c *adminUnitClient) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if o.GetNamespace() == AdminIssuanceNamespace {
		if c.failCreate {
			return apierrors.NewNotFound(corev1.Resource("namespaces"), AdminIssuanceNamespace)
		}
		o.SetUID(types.UID("original-sa-uid"))
	}
	if _, ok := o.(*rbacv1.ClusterRoleBinding); ok {
		o.SetUID(types.UID("original-crb-uid"))
	}
	return c.Client.Create(ctx, o, opts...)
}
func (c *adminUnitClient) Delete(ctx context.Context, o client.Object, opts ...client.DeleteOption) error {
	key := kindOf(o)
	c.deletes = append(c.deletes, key)
	if key == c.failDelete {
		return errors.New("synthetic cleanup transport failure")
	}
	return c.Client.Delete(ctx, o, opts...)
}
func (c *adminUnitClient) SubResource(name string) client.SubResourceClient {
	return &adminUnitSub{SubResourceClient: c.Client.SubResource(name), owner: c, name: name}
}

type adminUnitSub struct {
	client.SubResourceClient
	owner *adminUnitClient
	name  string
}

func (s *adminUnitSub) Create(ctx context.Context, o, sub client.Object, opts ...client.SubResourceCreateOption) error {
	if s.name != "token" {
		return s.SubResourceClient.Create(ctx, o, sub, opts...)
	}
	c := s.owner
	live := c.f.grant()
	a := live.Status.AdminCredential
	if a == nil || a.ServiceAccountUID == "" || a.BindingUID == "" {
		c.f.t.Fatal("mint before durable original identity")
	}
	c.mints++
	c.issuedNamespace = o.GetNamespace()
	tr := sub.(*authenticationv1.TokenRequest)
	c.requestSeconds = *tr.Spec.ExpirationSeconds
	tr.Status.Token = "synthetic-admin-token"
	tr.Status.ExpirationTimestamp = metav1.NewTime(c.f.clk.Now().Add(time.Duration(c.requestSeconds)*time.Second + c.expiryOffset))
	if c.releaseDuringMint {
		live.Spec.Release = true
		if err := c.Update(ctx, live); err != nil {
			return err
		}
	}
	c.f.clk.Step(c.responseDelay)

	return nil
}
func adminCase(t *testing.T) (*dgFixture, MaintenanceDecision, *adminUnitClient) {
	t.Helper()
	f, _ := reservedBrokerCase(t, grantattest.ClassRD2)
	g := f.grant()
	g.Spec.TTL.Duration = 30 * time.Minute
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = f.grant()
	d := f.decision(true)
	var err error
	d.Reserved, err = NewReservedBinding(context.Background(), f.c, sessionNS, g, d, f.b.ReservedDecisionClasses, nil, "msg-tom-1")
	if err != nil {
		t.Fatal(err)
	}
	d.Reach, err = NewReservedDelegateReach(context.Background(), f.c, sessionNS, g, d.Delegate, d.By, nil, ReservedAuthority(d.Reserved))
	if err != nil {
		t.Fatal(err)
	}
	f.b.EnableAdminLeases = true
	f.b.AdminIssuerNamespace = AdminIssuanceNamespace
	c := &adminUnitClient{Client: f.c, f: f}
	f.b.Client = c
	f.b.Installer = &recorder{}
	return f, d, c
}
func TestAdminLeaseOriginalIssuerAndReleaseExpiry(t *testing.T) {
	for _, mode := range []string{"release", "expiry", "config-change", "issuer-change", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c := adminCase(t)
			f.record(d)
			f.mustStep(3)
			g := f.grant()
			a := g.Status.AdminCredential
			if g.Status.Phase != v1alpha1.GrantActive || a == nil || a.Namespace != AdminIssuanceNamespace || a.ServiceAccountUID != "original-sa-uid" || a.BindingUID != "original-crb-uid" || c.mints != 1 || c.issuedNamespace != AdminIssuanceNamespace || c.requestSeconds > 1800 {
				t.Fatalf("incorrect original issuance: %+v mints%d", a, c.mints)
			}
			var crb rbacv1.ClusterRoleBinding
			if err := f.c.Get(context.Background(), client.ObjectKey{Name: g.Name}, &crb); err != nil || len(crb.Subjects) != 1 || crb.Subjects[0].Namespace != AdminIssuanceNamespace {
				t.Fatal("wrong root subject", err)
			}
			switch mode {
			case "expiry":
				f.clk.Step(30 * time.Minute)
			case "release":
				g.Spec.Release = true
				if err := f.c.Update(context.Background(), g); err != nil {
					t.Fatal(err)
				}
			case "issuer-change":
				f.b.AdminIssuerNamespace = "changed-issuer"
			case "disabled":
				f.b.EnableAdminLeases = false
			case "config-change":
				f.b.SessionNamespace = "changed-sessions"
				f.b.AdminIssuerNamespace = "changed-issuer"
				f.b.EnableAdminLeases = false
			}
			f.mustStep(2)
			g = f.grant()
			if !g.Status.Phase.Ended() || !g.Status.AdminCredential.CleanupStarted || g.Status.AdminCredential.CleanupError != "" || c.mints != 1 {
				t.Fatal("ended lease reminted or cleanup missing", g.Status)
			}
			if fmt.Sprint(c.deletes) != "[clusterrolebinding serviceaccount]" {
				t.Fatal("wrong revoke order", c.deletes)
			}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: AdminIssuanceNamespace, Name: g.Name}, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
				t.Fatal("original identity remains", err)
			}
		})
	}
}
func TestAdminLeaseNoIssuanceWithoutAuthority(t *testing.T) {
	for _, mode := range []string{"pending", "unconfigured", "wrong-issuer", "missing-namespace", "wrong-grant", "wrong-requester", "wrong-decision", "self", "ordinary-approval", "wrong-ttl"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c := adminCase(t)
			switch mode {
			case "unconfigured":
				f.b.AdminIssuerNamespace = ""
			case "wrong-issuer":
				f.b.AdminIssuerNamespace = "other"
			case "missing-namespace":
				c.failCreate = true
			case "wrong-grant":
				d.Reserved.GrantUID = "other"
			case "wrong-requester":
				d.Reserved.RequesterUID = "other"
			case "wrong-decision":
				d.Reserved.EffectiveDigest = "other"
			case "self":
				d.Delegate.Session = "req"
				d.Delegate.SessionUID = string(f.grant().Spec.Requester.SessionUID)
			case "wrong-ttl":
				d.TTL = time.Hour
			}
			if mode != "pending" {
				f.record(d)
			}
			if mode == "ordinary-approval" {
				f.mustStep(2)
				g := f.grant()
				g.Status.ApprovedBy = "authentik/synthetic"
				if err := f.c.Status().Update(context.Background(), g); err != nil {
					t.Fatal(err)
				}
			}
			f.mustStep(3)
			if c.mints != 0 {
				t.Fatal("unauthorized token mint")
			}
			var accounts corev1.ServiceAccountList
			if err := f.c.List(context.Background(), &accounts); err != nil || len(accounts.Items) != 0 {
				t.Fatal("unauthorized account issuance", err)
			}
			var all rbacv1.ClusterRoleBindingList
			if err := f.c.List(context.Background(), &all); err != nil || len(all.Items) != 0 {
				t.Fatal("unauthorized root binding", err)
			}
		})
	}
}
func TestAdminLeaseFixedTokenDeadline(t *testing.T) {
	for _, mode := range []string{"floor", "returned-too-late", "returned-expired", "shorter-valid", "fractional"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c := adminCase(t)
			f.b.Installer = nil
			f.record(d)
			f.mustStep(3)
			switch mode {
			case "floor":
				f.clk.Step(20*time.Minute + time.Second)
			case "returned-too-late":
				c.expiryOffset = time.Second
			case "returned-expired":
				c.expiryOffset = -time.Hour
			case "shorter-valid":
				c.expiryOffset = -time.Minute
			case "fractional":
				f.clk.Step(500 * time.Millisecond)
			}
			g := f.grant()
			token, expiry, err := f.b.mint(context.Background(), g, f.b.now())
			if mode == "shorter-valid" || mode == "fractional" {
				if err != nil || token == "" || expiry.After(g.Status.ExpiresAt.Time) {
					t.Fatal("valid bounded token refused", err)
				}
			} else if err == nil || token != "" {
				t.Fatal("unbounded token returned", mode)
			}
			if mode == "floor" && c.mints != 0 {
				t.Fatal("600s floor extended near-expiry lease")
			}
		})
	}
}
func TestAdminCleanupFailureCannotReissue(t *testing.T) {
	for _, mode := range []string{"binding-delete", "binding-owner", "binding-uid", "sa-uid", "sa-delete", "file-remove"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c := adminCase(t)
			f.record(d)
			f.mustStep(3)
			g := f.grant()
			switch mode {
			case "binding-delete":
				c.failDelete = "clusterrolebinding"
			case "sa-delete":
				c.failDelete = "serviceaccount"
			case "file-remove":
				f.b.Installer = adminRemoveFailure{}
			case "binding-owner", "binding-uid":
				var o rbacv1.ClusterRoleBinding
				if err := f.c.Get(context.Background(), client.ObjectKey{Name: g.Name}, &o); err != nil {
					t.Fatal(err)
				}
				if mode == "binding-owner" {
					o.Labels[v1alpha1.LabelPrefix+"grant-uid"] = "other"
				} else {
					o.UID = "replacement"
				}
				if err := f.c.Update(context.Background(), &o); err != nil {
					t.Fatal(err)
				}
			case "sa-uid":
				var o corev1.ServiceAccount
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: AdminIssuanceNamespace, Name: g.Name}, &o); err != nil {
					t.Fatal(err)
				}
				o.UID = "replacement"
				if err := f.c.Update(context.Background(), &o); err != nil {
					t.Fatal(err)
				}
			}
			g.Spec.Release = true
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			if _, err := f.step(1); err == nil {
				t.Fatal("cleanup failure reported success")
			}
			g = f.grant()
			if g.Status.Phase != v1alpha1.GrantActive || !g.Status.AdminCredential.CleanupStarted || g.Status.AdminCredential.CleanupError == "" || !strings.Contains(g.Status.Message, "cleanup pending") {
				t.Fatal("cleanup failure not retained", g.Status)
			}
			g.Spec.Release = false
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			_, _ = f.step(1)
			if c.mints != 1 {
				t.Fatal("cleanup failure reminted")
			}
			if strings.HasPrefix(mode, "binding-") && len(c.deletes) > 0 && strings.Contains(fmt.Sprint(c.deletes), "serviceaccount") {
				t.Fatal("SA deleted before CRB removal")
			}
		})
	}
}

type adminRemoveFailure struct{}

func (adminRemoveFailure) Install(context.Context, *corev1.Pod, *v1alpha1.AccessGrant, string, time.Time) error {
	return nil
}
func (adminRemoveFailure) Remove(context.Context, *corev1.Pod, *v1alpha1.AccessGrant) error {
	return errors.New("synthetic token-file removal failure")
}
func TestAdminLeaseNeverAdoptsReplacement(t *testing.T) {
	for _, binding := range []bool{false, true} {
		t.Run(fmt.Sprint(binding), func(t *testing.T) {
			f, d, c := adminCase(t)
			f.record(d)
			f.mustStep(3)
			g := f.grant()
			var o client.Object = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: g.Name}}
			if binding {
				o = &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: g.Name}}
			}
			if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(o), o); err != nil {
				t.Fatal(err)
			}
			if err := f.c.Delete(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			o.SetUID("replacement")
			o.SetResourceVersion("")
			if err := f.c.Create(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			_, _ = f.step(1)
			if c.mints != 1 {
				t.Fatal("replacement acquired a broker token")
			}
			a := f.grant().Status.AdminCredential
			if a.ServiceAccountUID != "original-sa-uid" || a.BindingUID != "original-crb-uid" {
				t.Fatal("adopted replacement", a)
			}
		})
	}
}

func TestAdminTokenOverrunRevokesOriginalAuthority(t *testing.T) {
	for _, mode := range []string{"returned-too-late", "response-after-deadline", "released-during-mint"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c := adminCase(t)
			switch mode {
			case "returned-too-late":
				c.expiryOffset = time.Second
			case "response-after-deadline":
				c.responseDelay = 31 * time.Minute
			case "released-during-mint":
				c.releaseDuringMint = true
			}
			f.record(d)
			// Concurrent release can conflict with the first cleanup status write;
			// the next reconcile must continue cleanup without delivering/reminting.
			_, _ = f.step(3)
			f.mustStep(2)
			g := f.grant()
			if !g.Status.Phase.Ended() || c.mints != 1 || !g.Status.AdminCredential.CleanupStarted {
				t.Fatal("overrun did not end original authority", g.Status, c.mints)
			}
			installs, _ := f.b.Installer.(*recorder).of(g.Name)
			if len(installs) != 0 {
				t.Fatal("out-of-bounds token was installed")
			}
			if fmt.Sprint(c.deletes) != "[clusterrolebinding serviceaccount]" {
				t.Fatal("overrun cleanup was not CRB first", c.deletes)
			}
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: AdminIssuanceNamespace, Name: g.Name}, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
				t.Fatal("overrun retained minted identity", err)
			}
		})
	}
}

func TestAdminLateRefreshKeepsOnlyValidOriginalLease(t *testing.T) {
	for _, mode := range []string{"same-pod", "requester-pod", "sa-uid", "binding-subject", "approval"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c := adminCase(t)
			c.expiryOffset = -time.Minute
			f.record(d)
			f.mustStep(3)
			// Server shortened the first token to29m; the normal refresh at28m
			// now has less than600s left in the fixed30m lease. No real sleep.
			f.clk.Step(28*time.Minute + 30*time.Second)
			g := f.grant()
			switch mode {
			case "requester-pod":
				var p corev1.Pod
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: g.Spec.Requester.Session}, &p); err != nil {
					t.Fatal(err)
				}
				p.UID = "changed-requester-pod"
				if err := f.c.Update(context.Background(), &p); err != nil {
					t.Fatal(err)
				}
			case "sa-uid":
				var sa corev1.ServiceAccount
				if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: AdminIssuanceNamespace, Name: g.Name}, &sa); err != nil {
					t.Fatal(err)
				}
				sa.UID = "replacement"
				if err := f.c.Update(context.Background(), &sa); err != nil {
					t.Fatal(err)
				}
			case "binding-subject":
				var crb rbacv1.ClusterRoleBinding
				if err := f.c.Get(context.Background(), client.ObjectKey{Name: g.Name}, &crb); err != nil {
					t.Fatal(err)
				}
				crb.Subjects[0].Namespace = "other"
				if err := f.c.Update(context.Background(), &crb); err != nil {
					t.Fatal(err)
				}
			case "approval":
				g.Annotations[MaintenanceDecisionAnnotation] = "{}"
				if err := f.c.Update(context.Background(), g); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.step(1)
			g = f.grant()
			if mode == "same-pod" {
				if err != nil || g.Status.Phase != v1alpha1.GrantActive || g.Status.AdminCredential.CleanupStarted || c.mints != 1 || len(c.deletes) != 0 {
					t.Fatal("normal late refresh revoked or reminted valid lease", err, g.Status)
				}
				f.clk.Step(90 * time.Second)
				f.mustStep(1)
				if f.grant().Status.Phase != v1alpha1.GrantExpired || c.mints != 1 {
					t.Fatal("fixed deadline was extended")
				}
			} else {
				if c.mints != 1 {
					t.Fatal("changed identity acquired a refresh token")
				}
				if err := f.c.Get(context.Background(), client.ObjectKey{Name: g.Name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
					t.Fatal("late refresh preserved mismatched root authority", err)
				}
				if !g.Status.AdminCredential.CleanupStarted {
					t.Fatal("mismatched lease did not start cleanup")
				}
			}
		})
	}
}

func TestAdminFlagLeavesOrdinaryGrantInSessionNamespace(t *testing.T) {
	b, g, _, _, mints := installationFixture(t, &recorder{}, 0)
	b.EnableAdminLeases = true
	b.AdminIssuerNamespace = AdminIssuanceNamespace
	reconcileInstallation(t, b, g)
	if *mints != 1 || g.Status.AdminCredential != nil {
		t.Fatal("ordinary class entered admin issuance")
	}
	if err := b.APIReader.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: g.Name}, &corev1.ServiceAccount{}); err != nil {
		t.Fatal("ordinary identity left session namespace", err)
	}
	if err := b.APIReader.Get(context.Background(), client.ObjectKey{Namespace: AdminIssuanceNamespace, Name: g.Name}, &corev1.ServiceAccount{}); !apierrors.IsNotFound(err) {
		t.Fatal("ordinary grant created issuer account", err)
	}
	if err := b.APIReader.Get(context.Background(), client.ObjectKey{Name: g.Name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatal("ordinary grant gained cluster authority", err)
	}
}

func TestAdminLeaseRejectsForeignOrIncorrectPreexistingObjects(t *testing.T) {
	for _, mode := range []string{"foreign-sa", "foreign-binding", "wrong-subject", "wrong-role"} {
		t.Run(mode, func(t *testing.T) {
			f, d, c := adminCase(t)
			g := f.grant()
			labels := map[string]string{v1alpha1.LabelGrant: g.Name, v1alpha1.LabelPrefix + "grant-uid": string(g.UID), v1alpha1.LabelManagedBy: ManagedBy}
			var o client.Object
			if mode == "foreign-sa" {
				labels[v1alpha1.LabelPrefix+"grant-uid"] = "foreign-grant"
				o = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: AdminIssuanceNamespace, Name: g.Name, UID: "foreign", Labels: labels}}
			} else {
				crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: g.Name, UID: "foreign", Labels: labels}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: v1alpha1.RoleBreakglass}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: AdminIssuanceNamespace, Name: g.Name}}}
				switch mode {
				case "foreign-binding":
					labels[v1alpha1.LabelPrefix+"grant-uid"] = "foreign-grant"
				case "wrong-subject":
					crb.Subjects[0].Namespace = "other"
				case "wrong-role":
					crb.RoleRef.Name = v1alpha1.RoleNodes
				}
				o = crb
			}
			if err := f.c.Create(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			f.record(d)
			_, _ = f.step(3)
			if c.mints != 0 {
				t.Fatal("incorrect preexisting object authorized token mint")
			}
			a := f.grant().Status.AdminCredential
			if a == nil || a.ServiceAccountUID == "foreign" || a.BindingUID == "foreign" {
				t.Fatal("adopted incorrect original identity", a)
			}
		})
	}
}
