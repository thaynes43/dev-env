package broker

// hw-ssh credential grants (D-89): the static shared key leased into the pod. The
// broker side is the same credential path as proxmox, behind its own default-off
// flag. A keeper receipt names a lease, not a provider token.

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// hwsshCase is credentialCase for hw-ssh: the grant asks for it, the standing
// policy names it, and only the hw-ssh flag is on (the PVE flag stays off).
func hwsshCase(t *testing.T) *credentialFixture {
	t.Helper()
	f := credentialCase(t)
	ctx := context.Background()
	g := f.getGrant()
	g.Spec.Credential.Name = v1alpha1.CredentialHWSSH
	if err := f.c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: systemNS, Name: "pve-full"}, &p); err != nil {
		t.Fatal(err)
	}
	p.Spec.Credential.Names = []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH}
	if err := f.c.Update(ctx, &p); err != nil {
		t.Fatal(err)
	}
	f.b.EnableProxmoxGrants = false
	f.b.EnableHWSSHGrants = true
	return f
}

// leaseReceipt is what the keeper writes after the lease is installed.
func (f *credentialFixture) leaseReceipt(phase v1alpha1.CredentialJobPhase) {
	f.t.Helper()
	j := f.job()
	now := metav1.NewTime(f.clk.Now())
	j.Status = v1alpha1.CredentialJobStatus{Phase: phase, ProviderID: v1alpha1.CredentialProviderID(v1alpha1.CredentialHWSSH, f.g.UID), InstalledPodUID: string(f.pod.UID), InstalledAt: &now}
	if phase == v1alpha1.CredentialRevoked {
		j.Status.RevokedAt = &now
	}
	if err := f.c.Status().Update(context.Background(), j); err != nil {
		f.t.Fatal(err)
	}
}

func TestHWSSHStandingPolicyReachesTheLeaseReceipt(t *testing.T) {
	f := hwsshCase(t)
	f.stepN(3)
	g := f.getGrant()
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "policy/pve-full" || f.creates != 1 {
		t.Fatalf("grant %+v creates %d", g.Status, f.creates)
	}
	if j := f.job(); j.Spec.Credential != v1alpha1.CredentialHWSSH {
		t.Fatalf("job names %q", j.Spec.Credential)
	}
	// A proxmox-shaped receipt is not accepted for an hw-ssh grant.
	f.receipt(v1alpha1.CredentialInstalled)
	f.stepN(1)
	if g := f.getGrant(); g.Status.InstalledPodUID != "" {
		t.Fatal("a PVE token receipt was accepted for an hw-ssh lease")
	}
	f.leaseReceipt(v1alpha1.CredentialInstalled)
	f.stepN(1)
	if g := f.getGrant(); g.Status.InstalledPodUID != string(f.pod.UID) || g.Status.Phase != v1alpha1.GrantActive {
		t.Fatalf("lease receipt not accepted: %+v", g.Status)
	}
}

// Default off: without the flag the request is denied before anything is made.
func TestHWSSHIsDeniedWithoutItsFlag(t *testing.T) {
	f := hwsshCase(t)
	f.b.EnableHWSSHGrants = false
	f.b.EnableProxmoxGrants = true // the PVE flag does not enable hw-ssh
	f.stepN(2)
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantDenied || f.creates != 0 {
		t.Fatalf("grant %+v creates %d", g.Status, f.creates)
	}
}

// The hw-ssh flag does not enable proxmox either.
func TestHWSSHFlagDoesNotEnableProxmox(t *testing.T) {
	f := credentialCase(t)
	f.b.EnableProxmoxGrants = false
	f.b.EnableHWSSHGrants = true
	f.stepN(2)
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantDenied || f.creates != 0 {
		t.Fatalf("grant %+v creates %d", g.Status, f.creates)
	}
}

// An approved hw-ssh grant whose flag is turned off before a job exists fails.
func TestHWSSHFlagOffBeforeTheJobFailsTheGrant(t *testing.T) {
	f := hwsshCase(t)
	f.stepN(2)
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantActive {
		t.Fatalf("setup %+v", g.Status)
	}
	f.b.EnableHWSSHGrants = false
	f.stepN(1)
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantFailed || f.creates != 0 {
		t.Fatalf("grant %+v creates %d", g.Status, f.creates)
	}
}

// Attestation (D-88) works for hw-ssh with no new framework: an attested policy
// naming hw-ssh, the same statement, the same flags.
func TestHWSSHAttestedPolicyApprovesAndReachesTheLease(t *testing.T) {
	f := attestedCase(t)
	ctx := context.Background()
	g := f.getGrant()
	g.Spec.Credential.Name = v1alpha1.CredentialHWSSH
	if err := f.c.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: systemNS, Name: attestedPolicy}, &p); err != nil {
		t.Fatal(err)
	}
	p.Spec.Credential.Names = []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH}
	if err := f.c.Update(ctx, &p); err != nil {
		t.Fatal(err)
	}
	f.b.EnableProxmoxGrants, f.b.EnableHWSSHGrants = false, true
	f.stepN(1)
	f.annotate(f.statement())
	f.stepN(2)
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "policy/"+attestedPolicy || f.creates != 1 {
		t.Fatalf("grant %+v creates %d", g.Status, f.creates)
	}
	f.leaseReceipt(v1alpha1.CredentialInstalled)
	f.stepN(1)
	if g := f.getGrant(); g.Status.InstalledPodUID != string(f.pod.UID) {
		t.Fatalf("not installed: %+v", g.Status)
	}
	// With the attested flag off the same statement approves nothing.
	off := attestedCase(t)
	og := off.getGrant()
	og.Spec.Credential.Name = v1alpha1.CredentialHWSSH
	if err := off.c.Update(ctx, og); err != nil {
		t.Fatal(err)
	}
	var op v1alpha1.GrantPolicy
	if err := off.c.Get(ctx, types.NamespacedName{Namespace: systemNS, Name: attestedPolicy}, &op); err != nil {
		t.Fatal(err)
	}
	op.Spec.Credential.Names = []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH}
	if err := off.c.Update(ctx, &op); err != nil {
		t.Fatal(err)
	}
	off.b.EnableProxmoxGrants, off.b.EnableHWSSHGrants, off.b.EnableAttestedGrants = false, true, false
	off.stepN(1)
	off.annotate(off.statement())
	off.stepN(3)
	if g := off.getGrant(); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || off.creates != 0 {
		t.Fatalf("approved with the attested flag off: %+v", g.Status)
	}
}

// Release, expiry and session end revoke the job exactly as for proxmox; the
// keeper's receipt then ends the grant. Nothing here reaches the hosts.
func TestHWSSHReleaseAndExpiryRevokeTheJob(t *testing.T) {
	for name, end := range map[string]func(*credentialFixture){
		"release": func(f *credentialFixture) {
			g := f.getGrant()
			g.Spec.Release = true
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
		},
		"expiry": func(f *credentialFixture) { f.clk.SetTime(f.clk.Now().Add(2 * time.Hour)) },
	} {
		t.Run(name, func(t *testing.T) {
			f := hwsshCase(t)
			f.stepN(3)
			f.leaseReceipt(v1alpha1.CredentialInstalled)
			f.stepN(1)
			end(f)
			f.stepN(1)
			if j := f.job(); !j.Spec.Release {
				t.Fatal("the job was not released")
			}
			f.leaseReceipt(v1alpha1.CredentialRevoked)
			f.stepN(1)
			if g := f.getGrant(); !g.Status.Phase.Ended() {
				t.Fatalf("grant %+v", g.Status)
			}
		})
	}
}
