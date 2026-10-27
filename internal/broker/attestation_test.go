package broker

// Agent attestation tests (DESIGN-001 D-88): a claim by the requesting agent,
// not verified human identity. Fake-client units on the credentialCase fixture;
// the keeper's side is the CredentialJob receipt, as in credentials_test.go.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

const attestedPolicy = "pve-attested"

// attestedCase is a credential grant for profile full and an attested policy that
// names profile full under spec.attested only. The standing pve-full policy is
// removed, so only the attestation can approve.
func attestedCase(t *testing.T) *credentialFixture {
	t.Helper()
	f := credentialCase(t)
	ctx := context.Background()
	var old v1alpha1.GrantPolicy
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: systemNS, Name: "pve-full"}, &old); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &old); err != nil {
		t.Fatal(err)
	}
	p := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: attestedPolicy}, Spec: v1alpha1.GrantPolicySpec{
		Attested: &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}, Type: v1alpha1.GrantCredential,
		Credential: &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialProxmox}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
	if err := f.c.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.b.EnableAttestedGrants = true
	// credentialCase leaves the grant without a creation-time UID problem: it has one.
	return f
}

// statement is what the operator's route would have written for f's grant.
func (f *credentialFixture) statement(edit ...func(*grantattest.Statement)) grantattest.Statement {
	g := f.getGrant()
	a, _ := grantattest.ParseApprover("coordinator:meatball")
	st := grantattest.Statement{V: grantattest.Version, Attestor: grantattest.Attestor{Session: f.s.Name, SessionUID: string(f.s.UID), PodUID: string(f.pod.UID)},
		Approver: a, Ref: "msg-1", GrantUID: string(g.UID), ScopeDigest: grantattest.ScopeDigest(g), TTL: g.Spec.TTL.Duration.String(), AttestedAt: f.clk.Now().Truncate(time.Second)}
	for _, e := range edit {
		e(&st)
	}
	return st
}

func (f *credentialFixture) annotate(st grantattest.Statement) {
	f.t.Helper()
	raw, err := st.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	g := f.getGrant()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[grantattest.Annotation] = raw
	if err := f.c.Update(context.Background(), g); err != nil {
		f.t.Fatal(err)
	}
}

func (f *credentialFixture) stepN(n int) {
	f.t.Helper()
	for range n {
		if _, err := f.step(); err != nil {
			f.t.Fatal(err)
		}
	}
}

// A valid attestation through an attested policy reaches installation, and the
// record says what it is.
func TestAttestedProxmoxReachesInstallation(t *testing.T) {
	f := attestedCase(t)
	f.stepN(1) // Pending
	f.annotate(f.statement())
	f.stepN(1)
	g := f.getGrant()
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "policy/"+attestedPolicy || !credentialApproval(g) {
		t.Fatalf("approval: %+v", g.Status)
	}
	f.stepN(1)
	if f.creates != 1 {
		t.Fatalf("execution job not created: %d", f.creates)
	}
	f.receipt(v1alpha1.CredentialInstalled)
	f.stepN(1)
	if g := f.getGrant(); g.Status.InstalledPodUID != string(f.pod.UID) || g.Status.Phase != v1alpha1.GrantActive {
		t.Fatalf("not installed: %+v", g.Status)
	}
	// The message is the audit text; it never claims a human approved.
	msg := grantattest.Message(attestedPolicy, f.statement())
	for _, want := range []string{"agent attestation, not verified human identity", "session/" + f.s.Name, "coordinator:meatball", "ref msg-1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

// Each case leaves the grant Pending (or ended) and creates no job.
func TestAttestationFailuresNeverApprove(t *testing.T) {
	other := "99999999-9999-9999-9999-999999999999"
	for _, tc := range []struct {
		name  string
		setup func(*credentialFixture)
		want  v1alpha1.GrantPhase
	}{
		{"no attestation", func(f *credentialFixture) {}, v1alpha1.GrantPending},
		{"forged actor session", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.Attestor.Session = "someone-else" }))
		}, v1alpha1.GrantPending},
		{"forged actor session UID", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.Attestor.SessionUID = other }))
		}, v1alpha1.GrantPending},
		{"stale pod", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.Attestor.PodUID = other }))
		}, v1alpha1.GrantPending},
		{"widened scope or ttl", func(f *credentialFixture) {
			st := f.statement()
			g := f.getGrant()
			g.Spec.TTL.Duration = 2 * time.Hour // the grant now asks for more than was attested
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			f.annotate(st)
		}, v1alpha1.GrantPending},
		{"attested scope differs from the grant's", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.ScopeDigest = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }))
		}, v1alpha1.GrantPending},
		{"replayed from another grant", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.GrantUID = other }))
		}, v1alpha1.GrantPending},
		{"predates the grant", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.AttestedAt = f.clk.Now().Add(-time.Hour) }))
		}, v1alpha1.GrantPending},
		{"dated in the future", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.AttestedAt = f.clk.Now().Add(time.Hour) }))
		}, v1alpha1.GrantPending},
		{"malformed", func(f *credentialFixture) {
			g := f.getGrant()
			g.Annotations = map[string]string{grantattest.Annotation: `{"v":1`}
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
		}, v1alpha1.GrantPending},
		{"ineligible session: profile not named by the policy", func(f *credentialFixture) {
			var p v1alpha1.GrantPolicy
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: attestedPolicy}, &p); err != nil {
				t.Fatal(err)
			}
			p.Spec.Attested.Profiles = []string{"workbench"}
			if err := f.c.Update(context.Background(), &p); err != nil {
				t.Fatal(err)
			}
			f.annotate(f.statement())
		}, v1alpha1.GrantPending},
		{"released grant", func(f *credentialFixture) {
			f.annotate(f.statement())
			g := f.getGrant()
			g.Spec.Release = true
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
		}, v1alpha1.GrantReleased},
		{"denied grant stays denied", func(f *credentialFixture) {
			f.annotate(f.statement())
			g := f.getGrant()
			g.Status.Phase, g.Status.DeniedBy = v1alpha1.GrantDenied, "authentik/tom"
			if err := f.c.Status().Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
		}, v1alpha1.GrantDenied},
		{"expired: no answer in 30 minutes", func(f *credentialFixture) {
			f.annotate(f.statement())
			f.clk.SetTime(f.clk.Now().Add(PendingTimeout))
		}, v1alpha1.GrantDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := attestedCase(t)
			f.stepN(1) // Pending
			tc.setup(f)
			f.stepN(2)
			if g := f.getGrant(); g.Status.Phase != tc.want || g.Status.ApprovedBy != "" || f.creates != 0 {
				t.Fatalf("phase %s approvedBy %q creates %d", g.Status.Phase, g.Status.ApprovedBy, f.creates)
			}
		})
	}
}

// A second statement is the route's refusal; here, a statement that is replaced
// by another claim after approval does not start a second execution.
func TestAttestationIsReverifiedAtExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*credentialFixture)
	}{
		{"statement removed", func(f *credentialFixture) {
			g := f.getGrant()
			delete(g.Annotations, grantattest.Annotation)
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
		}},
		{"statement swapped for another session's", func(f *credentialFixture) {
			f.annotate(f.statement(func(s *grantattest.Statement) { s.Attestor.SessionUID = "99999999-9999-9999-9999-999999999999" }))
		}},
		{"flag turned off before the job exists", func(f *credentialFixture) { f.b.EnableAttestedGrants = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := attestedCase(t)
			f.stepN(1)
			f.annotate(f.statement())
			f.stepN(1)
			if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantActive {
				t.Fatalf("setup: %+v", g.Status)
			}
			tc.edit(f)
			f.stepN(1)
			if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantFailed || f.creates != 0 {
				t.Fatalf("phase %s creates %d: %s", g.Status.Phase, f.creates, g.Status.Message)
			}
		})
	}
}

// Default off: with the flag off the broker never approves through an attested
// policy, even with a perfectly valid statement.
func TestAttestedPolicyNeverApprovesWhenTheFlagIsOff(t *testing.T) {
	f := attestedCase(t)
	f.b.EnableAttestedGrants = false
	f.stepN(1)
	f.annotate(f.statement())
	f.stepN(3)
	if g := f.getGrant(); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || f.creates != 0 {
		t.Fatalf("approved with the flag off: %+v creates %d", g.Status, f.creates)
	}
	var list v1alpha1.GrantPolicyList
	if err := f.c.List(context.Background(), &list); err != nil {
		t.Fatal(err)
	}
	if p := Match(f.getGrant(), list.Items); p != nil {
		t.Fatalf("Match returned the attested policy: %s", p.Name)
	}
	if p := MatchAttested(f.getGrant(), list.Items, nil); p != nil {
		t.Fatal("MatchAttested without a verifier returned an attested policy")
	}
}

// legacyMatches is the matcher as it stood before attestation (main b59880d,
// internal/broker/match.go Matches), copied verbatim. A broker image that
// predates this change runs exactly this against every GrantPolicy.
func legacyMatches(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) bool {
	ps, gs := &p.Spec, &g.Spec
	if gs.Type == v1alpha1.GrantBreakglass || ps.Type == v1alpha1.GrantBreakglass || gs.Type != ps.Type {
		return false
	}
	r := gs.Requester
	if r.Profile == ProfileOps {
		return false
	}
	if len(ps.Profiles) == 0 && len(ps.Repos) == 0 {
		return false
	}
	if len(ps.Profiles) > 0 && !slices.Contains(ps.Profiles, r.Profile) {
		return false
	}
	if len(ps.Repos) > 0 && !slices.Contains(ps.Repos, r.Repo) {
		return false
	}
	if len(ps.Agents) > 0 && !slices.Contains(ps.Agents, r.Agent) {
		return false
	}
	if gs.TTL.Duration <= 0 || gs.TTL.Duration > ps.MaxTTL.Duration {
		return false
	}
	switch gs.Type {
	case v1alpha1.GrantKube:
		return kubeInside(gs.Kube, ps.Kube)
	case v1alpha1.GrantEgress:
		return egressInside(gs.Egress, ps.Egress)
	case v1alpha1.GrantCredential:
		return gs.Credential != nil && ps.Credential != nil && slices.Contains(ps.Credential.Names, gs.Credential.Name)
	}
	return false
}

// Fail closed on a rollback: a broker that predates attestation matches nobody
// through an attested policy, because the policy names its requesters only under
// spec.attested and leaves the legacy lists empty.
func TestAttestedPolicyMatchesNothingForABrokerThatPredatesAttestation(t *testing.T) {
	f := attestedCase(t)
	var attested, standing v1alpha1.GrantPolicy
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: attestedPolicy}, &attested); err != nil {
		t.Fatal(err)
	}
	if len(attested.Spec.Profiles)+len(attested.Spec.Repos)+len(attested.Spec.Agents) != 0 {
		t.Fatalf("legacy selectors set on an attested policy: %+v", attested.Spec)
	}
	g := f.getGrant()
	if legacyMatches(&attested, g) {
		t.Fatal("the pre-attestation matcher matches an attested policy")
	}
	// The copy is the real matcher: it does match the same request through a standing policy.
	standing = *attested.DeepCopy()
	standing.Spec.Attested, standing.Spec.Profiles = nil, []string{"full"}
	if !legacyMatches(&standing, g) || !Matches(&standing, g) {
		t.Fatal("the frozen matcher no longer matches a standing policy; the copy is wrong")
	}
	// This broker's own standing matcher and the attested one are disjoint.
	if Matches(&attested, g) || !MatchesAttested(&attested, g) {
		t.Fatal("matcher split is wrong")
	}
	// A malformed policy that sets both is approved by nobody.
	mixed := *attested.DeepCopy()
	mixed.Spec.Profiles = []string{"full"}
	if MatchesAttested(&mixed, g) || Matches(&mixed, g) {
		t.Fatal("a mixed policy approved")
	}
}
