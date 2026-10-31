package broker

// Delegate decision tests (DESIGN-042 6.2, mode (b)): a listed delegate's
// recorded decision, applied through a GrantPolicy with spec.delegated. A
// delegate decision is not a human decision. Fake-client units in the style of
// maintenance_attestation_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

const (
	dgDelegate = "dev-env-system/codex-host-a"
	dgBy       = DelegateApprover + dgDelegate
	dgPolicy   = "kube-workloads-delegated"
	dgRef      = "msg-delegate-1"
)

type dgFixture struct {
	t   *testing.T
	b   *Broker
	c   client.Client
	clk *clocktesting.FakeClock
	key types.NamespacedName
}

// dgCase builds a pending-to-be kube grant from a top-level full session, the
// session and its Running pod, and a delegated policy that covers the grant.
// The broker lists dgDelegate. edit changes the grant and the policy first.
func dgCase(t *testing.T, edit ...func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)) *dgFixture {
	t.Helper()
	now := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "grant-delegate-k", UID: "grant-uid-k", CreationTimestamp: metav1.NewTime(now)},
		Spec: v1alpha1.AccessGrantSpec{
			Type:      v1alpha1.GrantKube,
			Requester: v1alpha1.GrantRequester{Session: "req", SessionUID: "11111111-1111-1111-1111-111111111111", Repo: "haynes-ops", Profile: "full", Agent: v1alpha1.AgentClaude},
			Kube:      &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}},
			TTL:       metav1.Duration{Duration: time.Hour}, Reason: "restart frontend",
		},
	}
	p := &v1alpha1.GrantPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: dgPolicy},
		Spec: v1alpha1.GrantPolicySpec{
			Description: "test", Type: v1alpha1.GrantKube,
			Delegated: &v1alpha1.DelegatedRequesters{Profiles: []string{"full", "workbench", "coordinator"}},
			Kube:      &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: []string{"frontend"}},
			MaxTTL:    metav1.Duration{Duration: 3 * time.Hour},
		},
	}
	for _, e := range edit {
		e(g, p)
	}
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "req", UID: g.Spec.Requester.SessionUID, Labels: map[string]string{v1alpha1.LabelDepth: "0"}}}
	s.Spec.Parent = "dev-env-system/dev-env-human"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "req", UID: "req-pod", Labels: map[string]string{v1alpha1.LabelSession: "req"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	sc := runtime.NewScheme()
	if err := errors.Join(v1alpha1.AddToScheme(sc), corev1.AddToScheme(sc), rbacv1.AddToScheme(sc), networkingv1.AddToScheme(sc), coordinationv1.AddToScheme(sc)); err != nil {
		t.Fatal(err)
	}
	f := &dgFixture{t: t, key: client.ObjectKeyFromObject(g), clk: clocktesting.NewFakeClock(now)}
	f.c = fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}).WithObjects(g, p, s, pod).Build()
	f.b = &Broker{Client: f.c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: f.clk, GrantDelegates: []string{dgDelegate}}
	return f
}

func (f *dgFixture) step(n int) (ctrl.Result, error) {
	f.t.Helper()
	var res ctrl.Result
	var err error
	for range n {
		if res, err = f.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.key}); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (f *dgFixture) mustStep(n int) ctrl.Result {
	f.t.Helper()
	res, err := f.step(n)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *dgFixture) grant() *v1alpha1.AccessGrant {
	f.t.Helper()
	var g v1alpha1.AccessGrant
	if err := f.c.Get(context.Background(), f.key, &g); err != nil {
		f.t.Fatal(err)
	}
	return &g
}

// decision is what the operator's delegate route records for the stored grant.
func (f *dgFixture) decision(approve bool, edit ...func(*MaintenanceDecision)) MaintenanceDecision {
	g := f.grant()
	d := MaintenanceDecision{GrantUID: string(g.UID), Digest: grantattest.ScopeDigest(g), By: dgBy, Approve: approve, TTL: g.Spec.TTL.Duration,
		At: f.clk.Now(), Ref: dgRef, Reason: "fits the frontend runbook", Delegate: &DelegateIdentity{PodUID: "host-pod"}}
	for _, e := range edit {
		e(&d)
	}
	return d
}

func (f *dgFixture) record(d MaintenanceDecision) {
	f.t.Helper()
	g := f.grant()
	raw, _ := json.Marshal(d)
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[MaintenanceDecisionAnnotation] = string(raw)
	if err := f.c.Update(context.Background(), g); err != nil {
		f.t.Fatal(err)
	}
}

// pendingThenDecide takes the grant to Pending, records d, and runs the broker
// once more.
func (f *dgFixture) pendingThenDecide(d func() MaintenanceDecision) *v1alpha1.AccessGrant {
	f.t.Helper()
	f.mustStep(2)
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending {
		f.t.Fatalf("not pending: %+v", g.Status)
	}
	f.record(d())
	f.mustStep(1)
	return f.grant()
}

func wantDelegateApproved(t *testing.T, g *v1alpha1.AccessGrant, ttl time.Duration) {
	t.Helper()
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != dgBy || g.Status.ApprovedTTL == nil || g.Status.ApprovedTTL.Duration != ttl {
		t.Fatalf("not approved by the delegate: %+v", g.Status)
	}
	for _, want := range []string{DelegateWording, "ref " + dgRef, "policy/"} {
		if !strings.Contains(g.Status.Message, want) {
			t.Errorf("message %q lacks %q", g.Status.Message, want)
		}
	}
	if strings.Contains(g.Status.ApprovedBy+g.Status.Message, "human/") {
		t.Fatalf("a delegate approval wrote a human approver: %+v", g.Status)
	}
}

func wantStillPending(t *testing.T, g *v1alpha1.AccessGrant) {
	t.Helper()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || g.Status.DeniedBy != "" {
		t.Fatalf("decided: %+v", g.Status)
	}
}

// A listed delegate's bound approval, through a delegated policy, approves a
// kube grant as delegate/<ns>/<sa>, for the TTL it chose; the next pass makes
// the grant's identity. A denial ends it Denied by the delegate.
func TestDelegateDecisionApprovesAndDenies(t *testing.T) {
	f := dgCase(t)
	g := f.pendingThenDecide(func() MaintenanceDecision {
		return f.decision(true, func(d *MaintenanceDecision) { d.TTL = 30 * time.Minute })
	})
	wantDelegateApproved(t, g, 30*time.Minute)
	if !strings.Contains(g.Status.Message, "policy/"+dgPolicy) {
		t.Fatalf("message does not name the policy: %q", g.Status.Message)
	}
	f.mustStep(1)
	var sa corev1.ServiceAccount
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: g.Name}, &sa); err != nil {
		t.Fatalf("no grant identity after a delegate approval: %v", err)
	}

	f = dgCase(t)
	g = f.pendingThenDecide(func() MaintenanceDecision { return f.decision(false) })
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != dgBy {
		t.Fatalf("not denied by the delegate: %+v", g.Status)
	}
	for _, want := range []string{DelegateWording, "ref " + dgRef, "fits the frontend runbook"} {
		if !strings.Contains(g.Status.Message, want) {
			t.Errorf("message %q lacks %q", g.Status.Message, want)
		}
	}
}

// An egress grant approves the same way.
func TestDelegateDecisionApprovesEgress(t *testing.T) {
	f := dgCase(t, func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
		g.Spec.Type, g.Spec.Kube = v1alpha1.GrantEgress, nil
		g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{"pve1.haynesnetwork.com"}, Ports: []v1alpha1.GrantPort{{Port: 8006}}}
		p.Spec.Type, p.Spec.Kube = v1alpha1.GrantEgress, nil
		p.Spec.Egress = &v1alpha1.EgressPolicy{FQDNs: []string{"*.haynesnetwork.com"}, Ports: []v1alpha1.GrantPort{{Port: 8006}, {Port: 443}}}
	})
	wantDelegateApproved(t, f.pendingThenDecide(func() MaintenanceDecision { return f.decision(true) }), time.Hour)
}

// Every broken binding, separation or policy leaves the grant waiting: the
// 30-minute timeout ends it.
func TestDelegateDecisionRefusalsWait(t *testing.T) {
	for name, tc := range map[string]struct {
		edit   func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)
		decide func(*MaintenanceDecision)
		broker func(*Broker)
	}{
		"delegate not listed":       {decide: func(d *MaintenanceDecision) { d.By = DelegateApprover + "dev-env-system/codex-host-b" }},
		"list without the delegate": {broker: func(b *Broker) { b.GrantDelegates = []string{"dev-env-system/other"} }},
		"another grant":             {decide: func(d *MaintenanceDecision) { d.GrantUID = "grant-uid-other" }},
		"digest mismatch":           {decide: func(d *MaintenanceDecision) { d.Digest = "stale-digest" }},
		"empty digest":              {decide: func(d *MaintenanceDecision) { d.Digest = "" }},
		"no ref":                    {decide: func(d *MaintenanceDecision) { d.Ref = "" }},
		"no delegate pod":           {decide: func(d *MaintenanceDecision) { d.Delegate = nil }},
		"before the grant":          {decide: func(d *MaintenanceDecision) { d.At = d.At.Add(-time.Hour) }},
		"from the future":           {decide: func(d *MaintenanceDecision) { d.At = d.At.Add(time.Hour) }},
		"longer TTL":                {decide: func(d *MaintenanceDecision) { d.TTL = 2 * time.Hour }},
		"shorter than 10m":          {decide: func(d *MaintenanceDecision) { d.TTL = 5 * time.Minute }},
		"the requesting session":    {decide: func(d *MaintenanceDecision) { d.Delegate.Session = "req" }},
		"the requesting session UID": {decide: func(d *MaintenanceDecision) {
			d.Delegate.Session, d.Delegate.SessionUID = "renamed", "11111111-1111-1111-1111-111111111111"
		}},
		"the requesting pod": {decide: func(d *MaintenanceDecision) { d.Delegate.PodUID = "req-pod" }},
		"no delegated policy": {edit: func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Delegated, p.Spec.Attested = nil, &v1alpha1.AttestedRequesters{Profiles: []string{"full"}}
		}},
		"requester outside the policy": {edit: func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Delegated.Profiles = []string{"workbench"}
		}},
		"scope outside the policy": {edit: func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
			g.Spec.Kube.Namespaces = []string{"frontend", "media"}
		}},
		"TTL over maxTTL": {edit: func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.MaxTTL = metav1.Duration{Duration: 30 * time.Minute}
		}},
		"storage role": {edit: func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Kube.Role = v1alpha1.RoleStorage
			p.Spec.Kube.Roles = []string{v1alpha1.RoleStorage}
		}},
		"secrets-read role": {edit: func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			g.Spec.Kube.Role = v1alpha1.RoleSecretsRead
			p.Spec.Kube.Roles = []string{v1alpha1.RoleSecretsRead}
		}},
		"a mixed policy": {edit: func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Profiles = []string{"full"}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			var edits []func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)
			if tc.edit != nil {
				edits = append(edits, tc.edit)
			}
			f := dgCase(t, edits...)
			if tc.broker != nil {
				tc.broker(f.b)
			}
			var decide []func(*MaintenanceDecision)
			if tc.decide != nil {
				decide = append(decide, tc.decide)
			}
			wantStillPending(t, f.pendingThenDecide(func() MaintenanceDecision { return f.decision(true, decide...) }))
			f.clk.Step(PendingTimeout)
			f.mustStep(1)
			if g := f.grant(); g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByTimeout {
				t.Fatalf("not denied by the timeout: %+v", g.Status)
			}
		})
	}
}

// A delegate decision that cannot apply never silences Tom: a kube grant
// still notifies once, whether the decision came before the broker's first
// Pending pass or after it.
func TestDelegateDecisionThatWaitsStillNotifies(t *testing.T) {
	for name, early := range map[string]bool{"before the first pending pass": true, "after it": false} {
		t.Run(name, func(t *testing.T) {
			f := dgCase(t, func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
				p.Spec.Delegated.Profiles = []string{"workbench"}
			})
			n := &notes{}
			f.b.Notifier = n
			if early {
				f.record(f.decision(true))
				f.mustStep(3)
			} else {
				f.mustStep(2)
				if n.count(f.key.Name) != 1 {
					t.Fatalf("not notified before the decision: %d", n.count(f.key.Name))
				}
				f.record(f.decision(true))
				f.mustStep(2)
			}
			g := f.grant()
			wantStillPending(t, g)
			if n.count(f.key.Name) != 1 || g.Status.NotifiedAt == nil {
				t.Fatalf("notified %d times, notifiedAt %v", n.count(f.key.Name), g.Status.NotifiedAt)
			}
		})
	}
	// A decision that applies needs no notification.
	f := dgCase(t)
	n := &notes{}
	f.b.Notifier = n
	f.record(f.decision(true))
	f.mustStep(3)
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || n.count(f.key.Name) != 0 {
		t.Fatalf("phase %s, notified %d times", g.Status.Phase, n.count(f.key.Name))
	}
}

// Decide itself refuses what a delegate may never decide, whoever calls it:
// credential and break-glass grants, the refused roles, a delegate not on the
// list, an unbound decision and an approval naming no policy.
func TestDecideRefusesDelegateOutsideItsBounds(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		edit func(*v1alpha1.AccessGrant)
		dec  func(*Decision)
	}{
		"credential": {edit: func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantCredential, nil
			g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialHWSSH}
		}},
		"breakglass": {edit: func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantBreakglass, &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
		}},
		"storage":      {edit: func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleStorage }},
		"secrets-read": {edit: func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleSecretsRead }},
		"not listed":   {dec: func(d *Decision) { d.By = DelegateApprover + "dev-env-system/nobody" }},
		"unbound":      {dec: func(d *Decision) { d.ScopeDigest = "other" }},
		"no ref":       {dec: func(d *Decision) { d.Ref = "" }},
		"no policy":    {dec: func(d *Decision) { d.Policy = "" }},
	} {
		t.Run(name, func(t *testing.T) {
			var edits []func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)
			if tc.edit != nil {
				edits = append(edits, func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) { tc.edit(g) })
			}
			f := dgCase(t, edits...)
			g := f.grant()
			d := Decision{Approve: true, By: dgBy, GrantUID: string(g.UID), ScopeDigest: grantattest.ScopeDigest(g), Ref: dgRef, Policy: dgPolicy}
			if tc.dec != nil {
				tc.dec(&d)
			}
			if err := f.b.Decide(ctx, g.Name, d); !errors.Is(err, ErrRefused) {
				t.Fatalf("Decide = %v, want ErrRefused", err)
			}
			if got := f.grant(); got.Status.Phase != "" {
				t.Fatalf("decided: %+v", got.Status)
			}
		})
	}
}

// Default off: with no delegates the broker reads no delegate decision. A kube
// grant carrying one ends in exactly the state an identical grant without it
// does, and a delegated policy approves nothing; a maintenance grant carrying
// one is refused by the human route as today.
func TestDelegateDecisionDefaultOffIsANoOp(t *testing.T) {
	run := func(annotate bool) v1alpha1.AccessGrantStatus {
		f := dgCase(t)
		f.b.GrantDelegates = nil
		f.mustStep(2)
		if annotate {
			f.record(f.decision(true))
		}
		f.mustStep(2)
		return f.grant().Status
	}
	off, without := run(true), run(false)
	if !reflect.DeepEqual(off, without) {
		t.Fatalf("a delegate decision changed a broker without delegates:\n%+v\n%+v", off, without)
	}
	if off.Phase != v1alpha1.GrantPending {
		t.Fatalf("a delegated policy approved with no delegates: %+v", off)
	}

	p := dgCase(t).policy()
	g := dgCase(t).grant()
	if Match(g, []v1alpha1.GrantPolicy{*p}) != nil || MatchAttested(g, []v1alpha1.GrantPolicy{*p}, func(*v1alpha1.GrantPolicy) bool { return true }) != nil || Matches(p, g) {
		t.Fatal("a delegated policy matched as a standing or attested one")
	}

	m := amCase(t)
	m.step(1)
	gm := m.grant()
	d := MaintenanceDecision{GrantUID: string(gm.UID), Digest: grantattest.ScopeDigest(gm), By: dgBy, Approve: true, TTL: gm.Spec.TTL.Duration, At: m.clk.Now(), Ref: dgRef, Delegate: &DelegateIdentity{PodUID: "host-pod"}}
	raw, _ := json.Marshal(d)
	m.annotate(MaintenanceDecisionAnnotation, string(raw))
	if _, err := m.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: m.key}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a broker without delegates did not refuse the decision as today: %v", err)
	}
	if got := m.grant(); got.Status.Phase != v1alpha1.GrantPending || got.Status.ApprovedBy != "" {
		t.Fatalf("decided: %+v", got.Status)
	}
}

func (f *dgFixture) policy() *v1alpha1.GrantPolicy {
	f.t.Helper()
	var p v1alpha1.GrantPolicy
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: dgPolicy}, &p); err != nil {
		f.t.Fatal(err)
	}
	return &p
}

// While Active, a delegate approval is checked again: it ends Released when
// the delegate leaves the list, the policy goes, or the decision no longer
// binds, and the broker looks again within a minute.
func TestDelegateApprovalIsRecheckedWhileActive(t *testing.T) {
	for name, change := range map[string]func(*dgFixture){
		"delegate removed": func(f *dgFixture) { f.b.GrantDelegates = nil },
		"policy deleted": func(f *dgFixture) {
			if err := f.c.Delete(context.Background(), f.policy()); err != nil {
				f.t.Fatal(err)
			}
		},
		"policy narrowed": func(f *dgFixture) {
			p := f.policy()
			p.Spec.Kube.Namespaces = []string{"media"}
			if err := f.c.Update(context.Background(), p); err != nil {
				f.t.Fatal(err)
			}
		},
		"decision replaced": func(f *dgFixture) { f.record(f.decision(true, func(d *MaintenanceDecision) { d.Digest = "other" })) },
	} {
		t.Run(name, func(t *testing.T) {
			f := dgCase(t)
			wantDelegateApproved(t, f.pendingThenDecide(func() MaintenanceDecision { return f.decision(true) }), time.Hour)
			if res := f.mustStep(1); res.RequeueAfter <= 0 || res.RequeueAfter > delegateRecheck {
				t.Fatalf("an active delegate approval requeues after %s, want at most %s", res.RequeueAfter, delegateRecheck)
			}
			change(f)
			f.mustStep(1)
			if g := f.grant(); g.Status.Phase != v1alpha1.GrantReleased || !strings.Contains(g.Status.Message, "the delegate approval no longer holds") {
				t.Fatalf("not ended: %+v", g.Status)
			}
		})
	}
}

// Maintenance through a delegated policy: #781's exact envelope, the human
// route's identity checks and one lease per target. The delegate is never a
// coordinator or worker the envelope names; an extension needs the human
// route; and with delegates configured the human route still decides.
func TestDelegateDecisionMaintenance(t *testing.T) {
	delegated := func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
		p.Spec.Attested, p.Spec.Delegated = nil, &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}
	}
	decide := func(f *amFixture, edit ...func(*MaintenanceDecision)) {
		f.t.Helper()
		g := f.grant()
		d := MaintenanceDecision{GrantUID: string(g.UID), Digest: grantattest.ScopeDigest(g), By: dgBy, Approve: true, TTL: g.Spec.TTL.Duration,
			At: f.clk.Now(), Ref: dgRef, Delegate: &DelegateIdentity{PodUID: "host-pod"}}
		for _, e := range edit {
			e(&d)
		}
		raw, _ := json.Marshal(d)
		f.annotate(MaintenanceDecisionAnnotation, string(raw))
	}
	setup := func(t *testing.T, edit ...func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)) *amFixture {
		f := amCase(t, append([]func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy){delegated}, edit...)...)
		f.b.GrantDelegates = []string{dgDelegate}
		f.b.EnableAttestedGrants, f.b.EnableAttestedMaintenance = false, false
		f.step(1)
		return f
	}

	t.Run("approves", func(t *testing.T) {
		f := setup(t)
		decide(f)
		f.step(1)
		wantDelegateApproved(t, f.grant(), 10*time.Minute)
		f.step(1)
		if !f.executorExists() {
			t.Fatal("no executor after a delegate approval")
		}
	})
	t.Run("rechecked before execution", func(t *testing.T) {
		f := setup(t)
		decide(f)
		f.step(1)
		wantDelegateApproved(t, f.grant(), 10*time.Minute)
		if err := f.c.Delete(context.Background(), f.policy()); err != nil {
			t.Fatal(err)
		}
		f.step(1)
		if g := f.grant(); g.Status.Phase != v1alpha1.GrantReleased || !strings.Contains(g.Status.Message, "maintenance approval no longer holds") || f.executorExists() {
			t.Fatalf("lease not ended before execution: %+v executor %v", g.Status, f.executorExists())
		}
	})
	t.Run("rechecked while it runs", func(t *testing.T) {
		f := setup(t)
		decide(f)
		f.step(2)
		if !f.executorExists() {
			t.Fatal("no executor")
		}
		f.b.GrantDelegates = nil
		f.step(1)
		if g := f.grant(); g.Status.Maintenance == nil || g.Status.Maintenance.Executor != "StopPending" {
			t.Fatalf("running executor not asked to stop: %+v", g.Status)
		}
	})
	for name, tc := range map[string]struct {
		edit   func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)
		decide func(*MaintenanceDecision, *v1alpha1.AccessGrant)
	}{
		"another GPU": {edit: func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) { p.Spec.Maintenance.Resource = amOtherGP }},
		"the envelope's coordinator": {decide: func(d *MaintenanceDecision, _ *v1alpha1.AccessGrant) {
			d.Delegate = &DelegateIdentity{PodUID: "host-pod", Session: "coord", SessionUID: "coord-uid"}
		}},
		"the envelope's worker pod": {
			edit: func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
				w := v1alpha1.MaintenanceIdentity{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}
				g.Spec.Maintenance.Workers, p.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{w}, []v1alpha1.MaintenanceIdentity{w}
			},
			decide: func(d *MaintenanceDecision, _ *v1alpha1.AccessGrant) {
				d.Delegate = &DelegateIdentity{PodUID: "worker-pod"}
			},
		},
		"the envelope's worker session": {
			edit: func(g *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
				w := v1alpha1.MaintenanceIdentity{Name: "worker", UID: "worker-uid", PodUID: "worker-pod"}
				g.Spec.Maintenance.Workers, p.Spec.Maintenance.Workers = []v1alpha1.MaintenanceIdentity{w}, []v1alpha1.MaintenanceIdentity{w}
			},
			decide: func(d *MaintenanceDecision, _ *v1alpha1.AccessGrant) {
				d.Delegate = &DelegateIdentity{PodUID: "host-pod", Session: "worker", SessionUID: "worker-uid"}
			},
		},
		"an extension": {edit: func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.ExtensionOf = "grant-earlier"
		}},
		// The delegate route binds ScopeDigest, never the human route's digest.
		"the human route's digest": {decide: func(d *MaintenanceDecision, g *v1alpha1.AccessGrant) { d.Digest = MaintenanceDigest(g) }},
	} {
		t.Run(name, func(t *testing.T) {
			var edits []func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)
			if tc.edit != nil {
				edits = append(edits, tc.edit)
			}
			f := setup(t, edits...)
			g := f.grant()
			decide(f, func(d *MaintenanceDecision) {
				if tc.decide != nil {
					tc.decide(d, g)
				}
			})
			wantWaiting(t, f)
		})
	}
	t.Run("human route unchanged", func(t *testing.T) {
		f := setup(t)
		f.human(true)
		f.step(1)
		if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != amHuman {
			t.Fatalf("the human route did not decide: %+v", g.Status)
		}
	})
}

// MatchesDelegated covers only kube, egress and maintenance, never a refused
// role or a mixed policy, and only with the requesters under spec.delegated.
func TestMatchesDelegated(t *testing.T) {
	f := dgCase(t)
	g, p := f.grant(), f.policy()
	if !MatchesDelegated(p, g) || MatchDelegated(g, []v1alpha1.GrantPolicy{*p}) == nil {
		t.Fatal("the delegated policy does not cover its grant")
	}
	for name, edit := range map[string]func(*v1alpha1.GrantPolicy, *v1alpha1.AccessGrant){
		"no delegated block": func(p *v1alpha1.GrantPolicy, _ *v1alpha1.AccessGrant) { p.Spec.Delegated = nil },
		"mixed":              func(p *v1alpha1.GrantPolicy, _ *v1alpha1.AccessGrant) { p.Spec.Repos = []string{"haynes-ops"} },
		"storage listed": func(p *v1alpha1.GrantPolicy, _ *v1alpha1.AccessGrant) {
			p.Spec.Kube.Roles = []string{v1alpha1.RoleWorkloads, v1alpha1.RoleStorage}
		},
		"credential": func(p *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) {
			p.Spec.Type, p.Spec.Kube = v1alpha1.GrantCredential, nil
			p.Spec.Credential = &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialHWSSH}}
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantCredential, nil
			g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialHWSSH}
		},
		"ops": func(_ *v1alpha1.GrantPolicy, g *v1alpha1.AccessGrant) { g.Spec.Requester.Profile = ProfileOps },
		"repo": func(p *v1alpha1.GrantPolicy, _ *v1alpha1.AccessGrant) {
			p.Spec.Delegated.Repos = []string{"haynesnetwork"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, g := p.DeepCopy(), g.DeepCopy()
			edit(p, g)
			if MatchesDelegated(p, g) {
				t.Fatal("matched")
			}
		})
	}
	deleting := p.DeepCopy()
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	if MatchDelegated(g, []v1alpha1.GrantPolicy{*deleting}) != nil {
		t.Fatal("a policy being deleted matched")
	}
}

// ParseGrantDelegates is shared by the operator and the broker.
func TestParseGrantDelegates(t *testing.T) {
	got, err := ParseGrantDelegates(" b/sa-2 ,a/sa-1", "x/human")
	if err != nil || !reflect.DeepEqual(got, []string{"a/sa-1", "b/sa-2"}) {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := ParseGrantDelegates(""); got != nil || err != nil {
		t.Fatalf("empty: %q %v", got, err)
	}
	many := make([]string, MaxGrantDelegates+1)
	for i := range many {
		many[i] = "ns/sa-" + string(rune('a'+i))
	}
	for _, bad := range []string{"x/human", "nobody", "a/b,a/b", "NS/x", "a/", "/b", strings.Join(many, ","), "a/b,,c/d"} {
		if _, err := ParseGrantDelegates(bad, "x/human"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
