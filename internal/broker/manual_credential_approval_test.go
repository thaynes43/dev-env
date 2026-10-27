package broker

// These tests pin what the broker does TODAY with a manual (human or
// administrative) approval of a credential grant, so that a later correction
// changes exactly the assertions named "FLIPS" and nothing else. They document
// current behaviour; they do not assert that it is right. The live outcome
// they reproduce (2026-10-27): kube and egress grants approved by hand went
// Active and ran, the credential grant approved by hand went Failed with
// "credential execution has no valid standing-policy approval", because the
// only live credential policy (credential-proxmox) matches profile workbench
// and the requester ran profile full.
//
// They use the unit fixtures of credentials_test.go (fake client, fake clock),
// so they need no envtest assets.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// liveShapedAdministrativeBy is the free-form approvedBy string the live
// administrative decision wrote (the broker never inspects it for kube and
// egress).
const liveShapedAdministrativeBy = "chat/Tom via haynes-m5max-local-agent"

// liveShapedPolicy makes the only standing credential policy shape that exists
// live: credential proxmox for profile workbench, 4h. The credentialCase
// requester is profile full, so it does not match.
func (f *credentialFixture) useLiveShapedPolicy() {
	f.t.Helper()
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: "pve-full"}, &p); err != nil {
		f.t.Fatal(err)
	}
	p.Spec.Profiles = []string{"workbench"}
	p.Spec.MaxTTL = metav1.Duration{Duration: 4 * time.Hour}
	if err := f.c.Update(context.Background(), &p); err != nil {
		f.t.Fatal(err)
	}
}

// pendingCredentialGrant leaves the credential grant Pending with no matching
// standing policy.
func pendingCredentialGrant(t *testing.T) *credentialFixture {
	t.Helper()
	f := credentialCase(t)
	f.useLiveShapedPolicy()
	f.step() // "" -> Pending
	f.step() // no policy matches: waits for Tom
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || f.creates != 0 {
		t.Fatalf("setup: %+v creates=%d", g.Status, f.creates)
	}
	return f
}

// TestManualCredentialApprovalIsRefusedToday pins the decision half: Decide
// refuses every approval of a GrantCredential, whoever signs it. FLIPS: with a
// future verified-human credential path enabled, an approval carrying that
// verified decision must be accepted; every case here without it must still be
// refused, and the whole test must keep passing with the path default-off.
func TestManualCredentialApprovalIsRefusedToday(t *testing.T) {
	for _, by := range []string{"authentik/tom", liveShapedAdministrativeBy, "human/tom", "system:admin"} {
		t.Run(by, func(t *testing.T) {
			f := pendingCredentialGrant(t)
			for _, ttl := range []time.Duration{0, time.Hour, MinTTL} {
				err := f.b.Decide(context.Background(), f.g.Name, Decision{Approve: true, By: by, TTL: ttl})
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "credential approvals require a matching standing policy") {
					t.Fatalf("ttl %s: manual credential approval was not refused as today: %v", ttl, err)
				}
			}
			g := f.getGrant()
			if g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || g.Status.ExpiresAt != nil || f.creates != 0 {
				t.Fatalf("a refused approval changed the grant: %+v creates=%d", g.Status, f.creates)
			}
		})
	}
}

// TestManualCredentialDenialAndPolicyApproverStillWorkToday pins what must not
// change: Tom may still deny a credential grant, and a "policy/" By is never an
// acceptable human answer, for approvals or denials.
func TestManualCredentialDenialAndPolicyApproverStillWorkToday(t *testing.T) {
	f := pendingCredentialGrant(t)
	for _, d := range []Decision{
		{Approve: true, By: "policy/pve-full"},
		{Approve: false, By: "policy/pve-full", Reason: "x"},
		{Approve: true},
		{Approve: false},
	} {
		if err := f.b.Decide(context.Background(), f.g.Name, d); !errors.Is(err, ErrRefused) {
			t.Fatalf("%+v: %v", d, err)
		}
	}
	if err := f.b.Decide(context.Background(), f.g.Name, Decision{By: "authentik/tom", Reason: "not today"}); err != nil {
		t.Fatalf("denial refused: %v", err)
	}
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != "authentik/tom" || f.creates != 0 {
		t.Fatalf("denial: %+v creates=%d", g.Status, f.creates)
	}
}

// activeWithApprover forces an Active credential grant as if an administrative
// writer set its status, with every other approval field well formed. The
// broker's own Decide cannot produce this today; a cluster-admin status write
// can.
func activeWithApprover(t *testing.T, by string) *credentialFixture {
	t.Helper()
	f := pendingCredentialGrant(t)
	g := f.getGrant()
	now := metav1.NewTime(f.clk.Now())
	approve(g, by, time.Hour, now)
	if err := f.c.Status().Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestNonPolicyCredentialApprovalIsRefusedAtExecutionToday pins the execution
// half: a credential grant that is Active with a well-formed approval by
// anything but "policy/<name>" ends Failed and never creates an execution job.
// FLIPS: this is the assertion a verified-human execution path changes, and only
// for an approval that carries the verified decision; the live-shaped string
// here must stay refused.
func TestNonPolicyCredentialApprovalIsRefusedAtExecutionToday(t *testing.T) {
	for _, by := range []string{liveShapedAdministrativeBy, "authentik/tom", "policy/", "policy"} {
		t.Run(by, func(t *testing.T) {
			f := activeWithApprover(t, by)
			if credentialApproval(f.getGrant()) {
				t.Fatalf("credentialApproval accepted %q", by)
			}
			if _, err := f.step(); err != nil {
				t.Fatal(err)
			}
			g := f.getGrant()
			if g.Status.Phase != v1alpha1.GrantFailed || g.Status.Message != "credential execution has no valid standing-policy approval" || f.creates != 0 {
				t.Fatalf("execution: %+v creates=%d", g.Status, f.creates)
			}
			var j v1alpha1.CredentialJob
			err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: v1alpha1.CredentialJobName(f.g.UID)}, &j)
			if err == nil {
				t.Fatal("an execution job exists for a non-policy approval")
			}
		})
	}
}

// TestEnsureCredentialJobRequiresTheNamedPolicyToStillMatchToday pins the second
// execution check: even a "policy/<name>" approval creates no job when that
// exact policy is absent or no longer matches the request.
func TestEnsureCredentialJobRequiresTheNamedPolicyToStillMatchToday(t *testing.T) {
	t.Run("named policy absent", func(t *testing.T) {
		f := activeWithApprover(t, "policy/gone")
		if _, err := f.b.ensureCredentialJob(context.Background(), f.getGrant()); err == nil || !strings.Contains(err.Error(), "approving policy is absent") {
			t.Fatalf("absent policy: %v", err)
		}
	})
	t.Run("named policy exists but does not match the requester profile", func(t *testing.T) {
		f := activeWithApprover(t, "policy/pve-full") // pve-full is the workbench-only shape here
		if _, err := f.b.ensureCredentialJob(context.Background(), f.getGrant()); err == nil || !strings.Contains(err.Error(), "does not permit this request") {
			t.Fatalf("non-matching policy: %v", err)
		}
		if f.creates != 0 {
			t.Fatalf("job created: %d", f.creates)
		}
	})
	t.Run("a non-policy approver names no policy at all", func(t *testing.T) {
		f := activeWithApprover(t, liveShapedAdministrativeBy)
		if _, err := f.b.ensureCredentialJob(context.Background(), f.getGrant()); err == nil || f.creates != 0 {
			t.Fatalf("ensureCredentialJob accepted a non-policy approver: err=%v creates=%d", err, f.creates)
		}
	})
}

// TestStandingPolicyCredentialPathStillSucceeds pins the path that must never
// change: a matching standing policy approves, one job is created for the exact
// grant and session, and a keeper receipt is accepted.
func TestStandingPolicyCredentialPathStillSucceeds(t *testing.T) {
	f := credentialCase(t) // pve-full matches profile full
	f.start()
	g := f.getGrant()
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "policy/pve-full" || !credentialApproval(g) || f.creates != 1 {
		t.Fatalf("standing policy path: %+v creates=%d", g.Status, f.creates)
	}
	j := f.job()
	if j.Spec.Grant.UID != f.g.UID || j.Spec.Session.UID != f.s.UID || j.Spec.Credential != v1alpha1.CredentialProxmox || !j.Spec.ExpiresAt.Equal(g.Status.ExpiresAt) {
		t.Fatalf("job is not bound to the grant: %+v", j.Spec)
	}
	f.receipt(v1alpha1.CredentialInstalled)
	if _, err := f.step(); err != nil {
		t.Fatal(err)
	}
	if g := f.getGrant(); g.Status.InstalledPodUID != string(f.pod.UID) || g.Status.Phase != v1alpha1.GrantActive {
		t.Fatalf("receipt: %+v", g.Status)
	}
}

// manualGrantFixture is a plain fake-client broker for kube and egress grants.
type manualGrantFixture struct {
	t   *testing.T
	b   *Broker
	c   client.WithWatch
	g   *v1alpha1.AccessGrant
	clk *clocktesting.FakePassiveClock
}

func manualGrantCase(t *testing.T, spec v1alpha1.AccessGrantSpec) *manualGrantFixture {
	t.Helper()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	clk := clocktesting.NewFakePassiveClock(now)
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "s-manual", UID: "11111111-1111-1111-1111-111111111111"}}
	spec.Requester = v1alpha1.GrantRequester{Session: s.Name, SessionUID: s.UID, Profile: "full", Repo: "haynes-ops", Agent: v1alpha1.AgentClaude}
	spec.TTL = metav1.Duration{Duration: time.Hour}
	spec.Reason = "test"
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "grant-manual", UID: "22222222-2222-2222-2222-222222222222", CreationTimestamp: metav1.NewTime(now), Finalizers: []string{Finalizer}, Labels: map[string]string{v1alpha1.LabelSession: s.Name}},
		Spec:       spec,
	}
	sc := runtime.NewScheme()
	if err := errors.Join(v1alpha1.AddToScheme(sc), corev1.AddToScheme(sc), rbacv1.AddToScheme(sc)); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}).WithObjects(s, g).Build()
	return &manualGrantFixture{t: t, c: c, g: g, clk: clk,
		b: &Broker{Client: c, APIReader: c, SessionNamespace: sessionNS, PolicyNamespace: systemNS, EnableProxmoxGrants: true, Clock: clk}}
}

func (f *manualGrantFixture) step() {
	f.t.Helper()
	if _, err := f.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.g)}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *manualGrantFixture) grant() *v1alpha1.AccessGrant {
	f.t.Helper()
	var g v1alpha1.AccessGrant
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.g), &g); err != nil {
		f.t.Fatal(err)
	}
	return &g
}

// TestManualKubeAndEgressApprovalsStillSucceed pins that manual approvals of
// kube and egress grants are decided by Decide, recorded under the signer's
// string, and executed with no check of who approved. This is the live outcome
// for the two grants that went Active. Nothing about it changes.
func TestManualKubeAndEgressApprovalsStillSucceed(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec v1alpha1.AccessGrantSpec
	}{
		{"kube", v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantKube, Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleNodes}}},
		{"egress", v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantEgress, Egress: &v1alpha1.EgressGrant{
			FQDNs: []string{"pvedash.haynesnetwork"}, Ports: []v1alpha1.GrantPort{{Port: 443, Protocol: v1alpha1.ProtocolTCP}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := manualGrantCase(t, tc.spec)
			f.step() // -> Pending
			f.step() // no policy: waits
			if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending {
				t.Fatalf("setup: %+v", g.Status)
			}
			if err := f.b.Decide(context.Background(), f.g.Name, Decision{Approve: true, By: liveShapedAdministrativeBy}); err != nil {
				t.Fatalf("manual approval refused: %v", err)
			}
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != liveShapedAdministrativeBy || g.Status.ApprovedTTL == nil || g.Status.ApprovedTTL.Duration != time.Hour || g.Status.ExpiresAt == nil {
				t.Fatalf("approval: %+v", g.Status)
			}
			f.step() // Active: the broker makes what the grant gives
			if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive {
				t.Fatalf("a %s grant approved by hand did not stay Active: %+v", tc.name, g.Status)
			}
			if tc.name == "kube" {
				var sa corev1.ServiceAccount
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: f.g.Name}, &sa); err != nil {
					t.Fatalf("the approved kube grant was not made: %v", err)
				}
			}
		})
	}
}
