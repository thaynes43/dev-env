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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/grantattest"
	"github.com/thaynes43/dev-env/internal/templates"
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
	c   client.WithWatch
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
	s.Spec.Parent = dgDelegate // a child of the delegate host
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
	f.c = fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}).WithObjects(g, p, s, pod, delegateHostPod()).Build()
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
		At: f.clk.Now(), Ref: dgRef, Reason: "fits the frontend runbook", Delegate: &DelegateIdentity{Form: 1, PodName: "host", PodUID: "host-pod", HostID: "host-a"}}
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

// The broker re-checks at apply time: a delegate removed from its list
// between the operator's record and the broker's pass approves nothing, for
// kube and maintenance grants alike, and the binding itself is checked on its
// own (delegateBinding), apart from Decide's check.
func TestDelegateRemovedBeforeApplyNeverApproves(t *testing.T) {
	for name, list := range map[string][]string{"emptied": nil, "replaced": {"dev-env-system/other-host"}} {
		t.Run("kube, list "+name, func(t *testing.T) {
			f := dgCase(t)
			f.mustStep(2)
			f.record(f.decision(true)) // recorded while dgDelegate was listed
			f.b.GrantDelegates = list
			f.mustStep(2)
			wantStillPending(t, f.grant())
		})
		t.Run("maintenance, list "+name, func(t *testing.T) {
			f := amCase(t, func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
				p.Spec.Attested, p.Spec.Delegated = nil, &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}
			})
			f.b.EnableAttestedGrants, f.b.EnableAttestedMaintenance = false, false
			childOfDelegateHost(f)
			f.step(1)
			g := f.grant()
			d := MaintenanceDecision{GrantUID: string(g.UID), Digest: grantattest.ScopeDigest(g), By: dgBy, Approve: true, TTL: g.Spec.TTL.Duration,
				At: f.clk.Now(), Ref: dgRef, Delegate: &DelegateIdentity{Form: 1, PodName: "host", PodUID: "host-pod", HostID: "host-a"}}
			raw, _ := json.Marshal(d)
			f.annotate(MaintenanceDecisionAnnotation, string(raw))
			f.b.GrantDelegates = list
			_, _ = f.b.Reconcile(context.Background(), ctrl.Request{NamespacedName: f.key})
			if got := f.grant(); got.Status.Phase != v1alpha1.GrantPending || got.Status.ApprovedBy != "" || f.executorExists() {
				t.Fatalf("approved after the delegate left the list: %+v", got.Status)
			}
		})
	}
	f := dgCase(t)
	g := f.grant()
	ok := f.decision(true)
	if err := delegateBinding(g, ok, []string{dgDelegate}, f.clk.Now()); err != nil {
		t.Fatalf("a good binding: %v", err)
	}
	for name, edit := range map[string]func(*MaintenanceDecision){
		"not listed":    func(d *MaintenanceDecision) { d.By = DelegateApprover + "dev-env-system/other-host" },
		"another grant": func(d *MaintenanceDecision) { d.GrantUID = "grant-uid-other" },
		"another scope": func(d *MaintenanceDecision) { d.Digest = "other" },
	} {
		bad := f.decision(true, edit)
		if err := delegateBinding(g, bad, []string{dgDelegate}, f.clk.Now()); err == nil {
			t.Errorf("%s: bound", name)
		}
	}
}

// childOfDelegateHost makes amCase's coordinator a child session of the
// delegate host dgDelegate: inside its reach.
func childOfDelegateHost(f *amFixture) {
	f.t.Helper()
	if err := f.c.Create(context.Background(), delegateHostPod()); err != nil {
		f.t.Fatal(err)
	}
	var s v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.sess), &s); err != nil {
		f.t.Fatal(err)
	}
	s.Spec.Parent = dgDelegate
	f.update(&s)
}

// The broker checks the delegate's reach again (lead ruling on #786): a host
// decides only for its own child sessions, a delegate session only for its
// strict descendants; an unknown form decides nothing.
func TestDelegateReachIsTheDelegatesTree(t *testing.T) {
	const lead = sessionNS + "/dev-env-coordinator-lead"
	addSession := func(f *dgFixture, name, parent string) *v1alpha1.AgentSession {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name, UID: types.UID(name + "-uid")}}
		s.Spec.Parent = parent
		if err := f.c.Create(context.Background(), s); err != nil {
			f.t.Fatal(err)
		}
		if name == "lead-s" {
			s.Labels = map[string]string{templates.CoordinatorIdentityLabel: "dev-env-coordinator-lead", v1alpha1.LabelDepth: "0"}
			s.Status.Phase = v1alpha1.PhaseRunning
			if err := f.c.Update(context.Background(), s); err != nil {
				f.t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID(name + "-pod"), OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-coordinator-lead"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			if err := f.c.Create(context.Background(), pod); err != nil {
				f.t.Fatal(err)
			}
		}
		return s
	}
	reparent := func(f *dgFixture, parent string) {
		var s v1alpha1.AgentSession
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "req"}, &s); err != nil {
			f.t.Fatal(err)
		}
		s.Spec.Parent = parent
		if err := f.c.Update(context.Background(), &s); err != nil {
			f.t.Fatal(err)
		}
	}
	session := func(name, uid string) func(*MaintenanceDecision) {
		return func(d *MaintenanceDecision) {
			d.By = DelegateApprover + lead
			d.Delegate = &DelegateIdentity{Form: 2, PodName: name, PodUID: name + "-pod", Session: name, SessionUID: uid}
		}
	}
	for name, tc := range map[string]struct {
		setup   func(*dgFixture)
		decide  func(*MaintenanceDecision)
		approve bool
	}{
		"a host's own child":                {approve: true},
		"another host's child":              {setup: func(f *dgFixture) { reparent(f, "dev-env-system/codex-host-b") }},
		"a host for a session Tom launched": {setup: func(f *dgFixture) { reparent(f, "dev-env-system/dev-env-human") }},
		"a lead's child": {setup: func(f *dgFixture) {
			addSession(f, "lead-s", "dev-env-system/dev-env-human")
			reparent(f, "lead-s")
		}, decide: session("lead-s", "lead-s-uid"), approve: true},
		"a lead's grandchild": {setup: func(f *dgFixture) {
			addSession(f, "lead-s", "dev-env-system/dev-env-human")
			addSession(f, "worker-s", "lead-s")
			reparent(f, "worker-s")
		}, decide: session("lead-s", "lead-s-uid"), approve: true},
		"a lead's sibling": {setup: func(f *dgFixture) {
			addSession(f, "root-s", "dev-env-system/dev-env-human")
			addSession(f, "lead-s", "root-s")
			reparent(f, "root-s")
		}, decide: session("lead-s", "lead-s-uid")},
		"a lead's parent": {setup: func(f *dgFixture) {
			addSession(f, "lead-s", "req")
			reparent(f, "dev-env-system/dev-env-human")
		}, decide: session("lead-s", "lead-s-uid")},
		"an unrelated session": {setup: func(f *dgFixture) {
			addSession(f, "lead-s", "dev-env-system/dev-env-human")
			reparent(f, "dev-env-system/dev-env-human")
		}, decide: session("lead-s", "lead-s-uid")},
		"a replaced lead of the same name": {setup: func(f *dgFixture) {
			addSession(f, "lead-s", "dev-env-system/dev-env-human")
			reparent(f, "lead-s")
		}, decide: session("lead-s", "earlier-uid")},
		"an unknown form": {decide: func(d *MaintenanceDecision) { d.Delegate.Form = 0 }},
	} {
		t.Run(name, func(t *testing.T) {
			f := dgCase(t)
			f.b.GrantDelegates = []string{dgDelegate, lead}
			if tc.setup != nil {
				tc.setup(f)
			}
			var edits []func(*MaintenanceDecision)
			if tc.decide != nil {
				edits = append(edits, tc.decide)
			}
			g := f.pendingThenDecide(func() MaintenanceDecision { return f.decision(true, edits...) })
			if tc.approve {
				if g.Status.Phase != v1alpha1.GrantActive {
					t.Fatalf("not approved inside the delegate's tree: %+v", g.Status)
				}
				return
			}
			wantStillPending(t, g)
		})
	}
}

// A delegate denies only inside a delegated scope too, and never for a
// requester running as its own delegate account.
func TestDelegateDenialAndSameAccountWait(t *testing.T) {
	t.Run("a denial outside every delegated policy", func(t *testing.T) {
		f := dgCase(t, func(_ *v1alpha1.AccessGrant, p *v1alpha1.GrantPolicy) {
			p.Spec.Delegated.Profiles = []string{"workbench"}
		})
		wantStillPending(t, f.pendingThenDecide(func() MaintenanceDecision { return f.decision(false) }))
	})
	t.Run("a requester on the delegate's own account", func(t *testing.T) {
		f := dgCase(t)
		const lead = sessionNS + "/dev-env-coordinator-lead"
		f.b.GrantDelegates = []string{lead}
		var pod corev1.Pod
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "req"}, &pod); err != nil {
			t.Fatal(err)
		}
		pod.Spec.ServiceAccountName = "dev-env-coordinator-lead"
		if err := f.c.Update(context.Background(), &pod); err != nil {
			t.Fatal(err)
		}
		wantStillPending(t, f.pendingThenDecide(func() MaintenanceDecision {
			return f.decision(true, func(d *MaintenanceDecision) {
				d.By = DelegateApprover + lead
				d.Delegate = &DelegateIdentity{PodUID: "other-lead-pod", Session: "other-lead", SessionUID: "22222222-2222-2222-2222-222222222222"}
			})
		}))
	})
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
		"storage":                   {edit: func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleStorage }},
		"secrets-read":              {edit: func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleSecretsRead }},
		"not listed":                {dec: func(d *Decision) { d.By = DelegateApprover + "dev-env-system/nobody" }},
		"unbound":                   {dec: func(d *Decision) { d.ScopeDigest = "other" }},
		"no ref":                    {dec: func(d *Decision) { d.Ref = "" }},
		"no policy":                 {dec: func(d *Decision) { d.Policy = "" }},
		"a denial naming no policy": {dec: func(d *Decision) { d.Approve, d.Policy = false, "" }},
		"an absent policy":          {dec: func(d *Decision) { d.Policy = "deleted-policy" }},
		"a policy that does not cover it": {edit: func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube.Namespaces = []string{"frontend", "media"}
		}},
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
	d := MaintenanceDecision{GrantUID: string(gm.UID), Digest: grantattest.ScopeDigest(gm), By: dgBy, Approve: true, TTL: gm.Spec.TTL.Duration, At: m.clk.Now(), Ref: dgRef, Delegate: &DelegateIdentity{Form: 1, PodName: "host", PodUID: "host-pod", HostID: "host-a"}}
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
			At: f.clk.Now(), Ref: dgRef, Delegate: &DelegateIdentity{Form: 1, PodName: "host", PodUID: "host-pod", HostID: "host-a"}}
		for _, e := range edit {
			e(&d)
		}
		raw, _ := json.Marshal(d)
		f.annotate(MaintenanceDecisionAnnotation, string(raw))
	}
	setup := func(t *testing.T, edit ...func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy)) *amFixture {
		f := amCase(t, append([]func(*v1alpha1.AccessGrant, *v1alpha1.GrantPolicy){delegated}, edit...)...)
		childOfDelegateHost(f)
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
	t.Run("extension is a fresh delegated lease", func(t *testing.T) {
		f := setup(t, func(g *v1alpha1.AccessGrant, _ *v1alpha1.GrantPolicy) {
			g.Spec.Maintenance.ExtensionOf = "grant-earlier"
		})
		decide(f)
		f.step(1)
		wantDelegateApproved(t, f.grant(), 10*time.Minute)
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

// A configured host is a real live pod when its recorded decision is applied.
func delegateHostPod() *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "host", Namespace: systemNS, UID: "host-pod", Annotations: map[string]string{v1alpha1.LabelPrefix + "coordinator-host": "host-a"}}, Spec: corev1.PodSpec{ServiceAccountName: "codex-host-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

// The grant cannot be issued or retained using a stale configured host, even
// when its API decision was valid before the host changed.
func TestDelegateIdentityRechecksHost(t *testing.T) {
	for name, edit := range map[string]func(*corev1.Pod){
		"replaced":             func(p *corev1.Pod) { p.UID = "replacement" },
		"not Running":          func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending },
		"account changed":      func(p *corev1.Pod) { p.Spec.ServiceAccountName = "other" },
		"host binding changed": func(p *corev1.Pod) { p.Annotations[v1alpha1.LabelPrefix+"coordinator-host"] = "host-b" },
		"session controller acquired": func(p *corev1.Pod) {
			p.OwnerReferences = []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: "unexpected", UID: "unexpected", Controller: ptr.To(true)}}
		},
	} {
		for _, active := range []bool{false, true} {
			suffix := " before apply"
			if active {
				suffix = " while active"
			}
			t.Run(name+suffix, func(t *testing.T) {
				f := dgCase(t)
				if active {
					wantDelegateApproved(t, f.pendingThenDecide(func() MaintenanceDecision { return f.decision(true) }), time.Hour)
					f.mustStep(1)
				} else {
					f.mustStep(2)
					f.record(f.decision(true))
				}
				var host corev1.Pod
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: "host"}, &host); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(context.Background(), &host); err != nil {
					t.Fatal(err)
				}
				host.ResourceVersion = ""
				edit(&host)
				if err := f.c.Create(context.Background(), &host); err != nil {
					t.Fatal(err)
				}
				f.mustStep(2)
				g := f.grant()
				if active {
					if g.Status.Phase != v1alpha1.GrantReleased {
						t.Fatalf("retained stale actor lease: %+v", g.Status)
					}
				} else {
					wantStillPending(t, g)
				}
			})
		}
	}
	t.Run("a non-session controller is allowed", func(t *testing.T) {
		f := dgCase(t)
		var host corev1.Pod
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: systemNS, Name: "host"}, &host); err != nil {
			t.Fatal(err)
		}
		host.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "host", UID: "host-set", Controller: ptr.To(true)}}
		if err := f.c.Update(context.Background(), &host); err != nil {
			t.Fatal(err)
		}
		wantDelegateApproved(t, f.pendingThenDecide(func() MaintenanceDecision { return f.decision(true) }), time.Hour)
	})
}

func TestDelegateRefusedPendingRecordReleasesSlot(t *testing.T) {
	for name, change := range map[string]func(*dgFixture){
		"policy removed": func(f *dgFixture) {
			if err := f.c.Delete(context.Background(), f.policy()); err != nil {
				t.Fatal(err)
			}
		},
		"delegate removed": func(f *dgFixture) { f.b.GrantDelegates = []string{"dev-env-system/other"} },
		"pod removed": func(f *dgFixture) {
			if err := f.c.Delete(context.Background(), delegateHostPod()); err != nil {
				t.Fatal(err)
			}
		},
		"scope changed": func(f *dgFixture) {
			g := f.grant()
			g.Spec.TTL.Duration = 30 * time.Minute
			if err := f.c.Update(context.Background(), g); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := dgCase(t)
			f.mustStep(2)
			f.record(f.decision(true))
			change(f)
			f.mustStep(1)
			g := f.grant()
			wantStillPending(t, g)
			if g.Annotations[MaintenanceDecisionAnnotation] != "" {
				t.Fatal("permanently invalid decision still monopolizes the slot")
			}
		})
	}
}

func TestDelegateDecisionCleanupPreservesHumanAndConcurrentChanges(t *testing.T) {
	for _, when := range []string{"before live read", "after live read"} {
		for _, replacement := range []string{"human", "delegate", "active", "replaced grant"} {
			t.Run(when+"/"+replacement, func(t *testing.T) {
				f := dgCase(t)
				f.mustStep(2)
				f.record(f.decision(true))
				observed := f.grant()
				change := func() {
					switch replacement {
					case "human", "delegate":
						d := f.decision(false)
						if replacement == "human" {
							d.By = "human/dev-env-system/dev-env-human"
						}
						f.record(d)
					case "active":
						g := f.grant()
						g.Status.Phase = v1alpha1.GrantActive
						if err := f.c.Status().Update(context.Background(), g); err != nil {
							t.Fatal(err)
						}
					case "replaced grant":
						g := f.grant()
						g.Finalizers = nil
						if err := f.c.Update(context.Background(), g); err != nil {
							t.Fatal(err)
						}
						if err := f.c.Delete(context.Background(), g); err != nil {
							t.Fatal(err)
						}
						g.UID, g.ResourceVersion = "replacement-uid", ""
						if err := f.c.Create(context.Background(), g); err != nil {
							t.Fatal(err)
						}
					}
				}
				if when == "before live read" {
					change()
				} else {
					f.b.Client = interceptor.NewClient(f.c, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						change()
						return c.Patch(ctx, obj, patch, opts...)
					}})
				}
				cleared, err := f.b.clearRefusedDelegateDecision(context.Background(), observed)
				if err != nil || cleared || f.grant().Annotations[MaintenanceDecisionAnnotation] == "" {
					t.Fatalf("replacement erased: cleared=%t error=%v", cleared, err)
				}
			})
		}
	}
	t.Run("human record is never eligible for cleanup", func(t *testing.T) {
		f := dgCase(t)
		f.mustStep(2)
		f.record(f.decision(true, func(d *MaintenanceDecision) { d.By = "human/dev-env-system/dev-env-human" }))
		if cleared, err := f.b.clearRefusedDelegateDecision(context.Background(), f.grant()); cleared || err != nil {
			t.Fatalf("human cleanup: %t %v", cleared, err)
		}
		if f.grant().Annotations[MaintenanceDecisionAnnotation] == "" {
			t.Fatal("human record erased")
		}
	})
}

func TestDelegateForbiddenPodReadRevokesActive(t *testing.T) {
	f := dgCase(t)
	wantDelegateApproved(t, f.pendingThenDecide(func() MaintenanceDecision { return f.decision(true) }), time.Hour)
	f.mustStep(1)
	f.b.APIReader = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Pod); ok && key.Namespace == systemNS && key.Name == "host" {
			return apierrors.NewForbidden(corev1.Resource("pods"), key.Name, errors.New("fixture"))
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	f.mustStep(2)
	if st := f.grant().Status; st.Phase != v1alpha1.GrantReleased {
		t.Fatalf("Forbidden delegate read retained lease: %+v", st)
	}
	var sa corev1.ServiceAccount
	if err := f.c.Get(context.Background(), f.key, &sa); !apierrors.IsNotFound(err) {
		t.Fatalf("revoked grant identity remains: %v", err)
	}
}

func TestDelegateTemporaryReadErrorKeepsPendingRecord(t *testing.T) {
	f := dgCase(t)
	f.mustStep(2)
	f.record(f.decision(true))
	before := f.grant().Annotations[MaintenanceDecisionAnnotation]
	f.b.APIReader = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.Pod); ok && key.Namespace == systemNS && key.Name == "host" {
			return errors.New("temporary fixture read failure")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if _, err := f.step(1); err == nil {
		t.Fatal("temporary read error was hidden")
	}
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending || g.Annotations[MaintenanceDecisionAnnotation] != before {
		t.Fatal("temporary read failure changed the decision")
	}
	f.b.APIReader = f.c
	f.mustStep(1)
	wantDelegateApproved(t, f.grant(), time.Hour)
}
