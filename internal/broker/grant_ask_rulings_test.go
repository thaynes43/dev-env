package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

func TestGrantAskStoredNoThenMatchingPolicyStaysDenied(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(false), "tom", "no")
	p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
	if err := f.base.Create(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	spy := f.watchDecide(true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.ApprovedBy != "" || g.Status.DeniedBy != "authentik/tom" {
		t.Fatalf("stored no then policy: phase %s approvedBy %s deniedBy %s", g.Status.Phase, g.Status.ApprovedBy, g.Status.DeniedBy)
	}
	if spy.calls != 1 || spy.last.Approve || spy.last.By != "authentik/tom" {
		t.Fatalf("Decide calls=%d approve=%v by=%s", spy.calls, spy.last.Approve, spy.last.By)
	}
}

func TestGrantAskStoredNoThenMatchingAttestationStaysDenied(t *testing.T) {
	// The same attestation approves when no Ask has been opened, so the
	// denial below is the Ask's no and not a missed match.
	open := newGrantAskFixture(t, true)
	pod := open.runningPod()
	open.b.EnableAttestedGrants = true
	open.attest(pod)
	if err := open.base.Create(open.ctx, attestedWorkloadsPolicy()); err != nil {
		t.Fatal(err)
	}
	open.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantActive })
	if open.grant().Status.ApprovedBy != "policy/frontend-attested" || open.askCount() != 0 {
		t.Fatalf("attestation did not match: %+v asks=%d", open.grant().Status, open.askCount())
	}

	f := newGrantAskFixture(t, true)
	answeredPod := f.runningPod()
	f.b.EnableAttestedGrants = true
	f.openAsk()
	f.answer(boolPtr(false), "tom", "no")
	f.attest(answeredPod)
	if err := f.base.Create(f.ctx, attestedWorkloadsPolicy()); err != nil {
		t.Fatal(err)
	}
	spy := f.watchDecide(true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.ApprovedBy != "" || g.Status.DeniedBy != "authentik/tom" {
		t.Fatalf("stored no then attestation: phase %s approvedBy %s deniedBy %s", g.Status.Phase, g.Status.ApprovedBy, g.Status.DeniedBy)
	}
	if spy.calls != 1 || spy.last.Approve {
		t.Fatalf("Decide calls=%d approve=%v", spy.calls, spy.last.Approve)
	}
}

func TestGrantAskOpenUnansweredThenMatchingPolicyStaysPending(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
	if err := f.base.Create(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	spy := f.watchDecide(false)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || g.Status.DeniedBy != "" {
		t.Fatalf("open ask then policy: %+v", g.Status)
	}
	if spy.calls != 0 {
		t.Fatal("Decide was called")
	}
	if f.askCount() != 1 || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
		t.Fatalf("ask was not left open")
	}
}

func TestGrantAskScopeTextStatesPortsSelectorsAndSecrets(t *testing.T) {
	base := v1alpha1.EgressGrant{
		Endpoints: []v1alpha1.EgressEndpoint{{
			Namespace: "frontend", MatchLabels: map[string]v1alpha1.LabelValue{"app": "web"},
		}},
		Ports: tcp(443),
	}
	otherPort := base
	otherPort.Ports = tcp(22)
	otherSelector := base
	otherSelector.Endpoints = []v1alpha1.EgressEndpoint{{
		Namespace: "frontend", MatchLabels: map[string]v1alpha1.LabelValue{"app": "api"},
	}}
	a := openEgressAsk(t, base)
	b := openEgressAsk(t, otherPort)
	c := openEgressAsk(t, otherSelector)
	da, db, dc := a.onlyAsk().Spec.Grant.Detail, b.onlyAsk().Spec.Grant.Detail, c.onlyAsk().Spec.Grant.Detail
	if da == db || da == dc || db == dc {
		t.Fatalf("port or selector did not change the ask text: %q %q %q", da, db, dc)
	}
	if !strings.Contains(da, "443/TCP") || !strings.Contains(da, "app=web") || !strings.Contains(db, "22/TCP") || !strings.Contains(dc, "app=api") {
		t.Fatalf("ask text dropped a port or selector: %q %q %q", da, db, dc)
	}
	for _, f := range []*grantAskFixture{a, b, c} {
		if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" {
			t.Fatalf("egress ask: %+v", g.Status)
		}
	}

	secrets := openKubeAsk(t, &v1alpha1.KubeGrant{
		Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{"downloads"},
		Secrets: []string{"prowlarr-secret", "myanonamouse-secret"},
	})
	one := openKubeAsk(t, &v1alpha1.KubeGrant{
		Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{"downloads"},
		Secrets: []string{"prowlarr-secret"},
	})
	both := secrets.onlyAsk().Spec.Grant.Detail
	single := one.onlyAsk().Spec.Grant.Detail
	if !strings.Contains(both, "prowlarr-secret") || !strings.Contains(both, "myanonamouse-secret") || both == single {
		t.Fatalf("secret names: %q and %q", both, single)
	}
	if secrets.grant().Status.Phase != v1alpha1.GrantPending || one.grant().Status.Phase != v1alpha1.GrantPending {
		t.Fatalf("secret grants: %s %s", secrets.grant().Status.Phase, one.grant().Status.Phase)
	}
}

func TestGrantAskOverlongScopeOpensNoAskAndDeniesAtThirtyMinutes(t *testing.T) {
	f := newGrantAskFixture(t, true)
	rec := &events.FakeRecorder{Events: make(chan string, 8)}
	f.b.Recorder = rec
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + ".example.com"
	g := f.grant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Kube = nil
	g.Spec.Egress = &v1alpha1.EgressGrant{FQDNs: []string{long}, Ports: tcp(443)}
	g.Spec.Reason = "reach a host"
	if err := f.base.Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, fits := grantAskDetail(g); fits {
		t.Fatal("the fixture scope fit the ask")
	}
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := f.grant(); got.Status.Phase != v1alpha1.GrantPending || got.Status.DeniedBy != "" || got.Status.ApprovedBy != "" {
		t.Fatalf("over-long scope inside the window: %+v", got.Status)
	}
	if n := f.askCount(); n != 0 {
		t.Fatalf("over-long scope opened %d asks", n)
	}
	msgs := eventTexts(rec)
	if len(msgs) == 0 || !strings.Contains(msgs[0], "AskTooLarge") || !strings.Contains(msgs[0], "the request is too large to show and should be split") {
		t.Fatalf("events %q", msgs)
	}
	f.clk.Step(PendingTimeout)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if got.Status.Phase != v1alpha1.GrantDenied || got.Status.DeniedBy != DeniedByTimeout || got.Status.Message != "no answer in 30 minutes" || got.Status.ApprovedBy != "" {
		t.Fatalf("over-long scope after 30 minutes: phase %s deniedBy %s message %q", got.Status.Phase, got.Status.DeniedBy, got.Status.Message)
	}
	if n := f.askCount(); n != 0 {
		t.Fatalf("over-long scope opened %d asks at the deny", n)
	}
}

func TestGrantAskWithdrawnWhilePendingDeniesWithoutDecide(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	ask := f.onlyAsk()
	ask.Status.State = string(apiv1.AskStateWithdrawn)
	if err := f.base.Status().Update(f.ctx, ask); err != nil {
		t.Fatal(err)
	}
	spy := f.watchDecide(false)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Spec.Release || g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != "the grant ask was withdrawn" || g.Status.ApprovedBy != "" {
		t.Fatalf("withdrawn ask: release %v phase %s deniedBy %s message %q", g.Spec.Release, g.Status.Phase, g.Status.DeniedBy, g.Status.Message)
	}
	if spy.calls != 0 {
		t.Fatal("Decide was called")
	}
}

func TestGrantAskDelegatePastDeadlineDeniesOnReconcile(t *testing.T) {
	// Inside the window the same decision approves, so the denial is the
	// deadline and not a refused delegate record.
	early := newGrantAskFixture(t, true)
	// One reconcile adds the finalizer and sets Pending. It returns before
	// pending, so no Ask is opened and the delegate decision is the first
	// thing that pass sees.
	if _, err := early.reconcile(); err != nil {
		t.Fatal(err)
	}
	if early.grant().Status.Phase != v1alpha1.GrantPending {
		t.Fatalf("not pending: %+v", early.grant().Status)
	}
	early.clk.Step(29 * time.Minute)
	early.armDelegate(early.b.now().Time)
	if _, err := early.reconcile(); err != nil {
		t.Fatal(err)
	}
	if g := early.grant(); g.Status.Phase != v1alpha1.GrantActive || !strings.HasPrefix(g.Status.ApprovedBy, DelegateApprover) {
		t.Fatalf("delegate inside the window: %+v", g.Status)
	}
	if early.askCount() != 0 {
		t.Fatal("delegate approval opened an ask")
	}

	f := newGrantAskFixture(t, true)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	f.clk.Step(29 * time.Minute)
	f.armDelegate(f.b.now().Time)
	f.clk.Step(2 * time.Minute)
	spy := f.watchDecide(false)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByTimeout || g.Status.Message != "no answer in 30 minutes" || g.Status.ApprovedBy != "" {
		t.Fatalf("delegate across the deadline: phase %s deniedBy %s message %q approvedBy %s", g.Status.Phase, g.Status.DeniedBy, g.Status.Message, g.Status.ApprovedBy)
	}
	if spy.calls != 0 {
		t.Fatal("Decide was called")
	}
	if f.askCount() != 0 {
		t.Fatal("delegate past the deadline opened an ask")
	}
}

func TestGrantAskOriginIsSetOnCreateWithoutAPatch(t *testing.T) {
	f := newGrantAskFixture(t, true)
	patches := 0
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*coord.Ask); ok {
				patches++
				return fmt.Errorf("ask metadata patch")
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	f.openAsk()
	ask := f.onlyAsk()
	if patches != 0 || ask.Labels[asks.OriginLabel] != grantAskOriginBroker {
		t.Fatalf("patches=%d origin=%q", patches, ask.Labels[asks.OriginLabel])
	}
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" {
		t.Fatalf("origin create: %+v", g.Status)
	}
}

func TestGrantAskForeignAskIsNotAdopted(t *testing.T) {
	f := newGrantAskFixture(t, true)
	rec := &events.FakeRecorder{Events: make(chan string, 8)}
	f.b.Recorder = rec
	f.openAsk()
	src := f.onlyAsk()
	foreign := src.DeepCopy()
	if err := f.base.Delete(f.ctx, src); err != nil {
		t.Fatal(err)
	}
	foreign.ResourceVersion = ""
	foreign.UID = ""
	delete(foreign.Labels, asks.OriginLabel)
	yes := true
	foreign.Status = coord.AskStatus{
		State: string(apiv1.AskStateAnswered),
		Answer: &coord.AskAnswer{
			Approved: &yes, AnsweredAt: metav1.NewTime(f.clk.Now()),
			AnsweredBy: coord.AskActor{Issuer: "https://issuer.example/grant-ask", Subject: "subject-1", Username: "tom"},
		},
	}
	if err := f.base.Create(f.ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := f.base.Status().Update(f.ctx, foreign); err != nil {
		t.Fatal(err)
	}
	p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
	if err := f.base.Create(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	spy := f.watchDecide(false)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || g.Status.DeniedBy != "" {
		t.Fatalf("foreign ask: %+v", g.Status)
	}
	if got := f.onlyAsk().Labels[asks.OriginLabel]; got != "" {
		t.Fatalf("foreign ask was labelled %q", got)
	}
	if spy.calls != 0 {
		t.Fatal("Decide was called")
	}
	msgs := eventTexts(rec)
	if len(msgs) == 0 || !strings.Contains(strings.Join(msgs, "\n"), "AskNotBroker") {
		t.Fatalf("events %q", msgs)
	}
	f.clk.Step(PendingTimeout)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if got.Status.Phase != v1alpha1.GrantDenied || got.Status.DeniedBy != DeniedByTimeout || got.Status.ApprovedBy != "" {
		t.Fatalf("foreign ask after 30 minutes: %+v", got.Status)
	}
	if origin := f.onlyAsk().Labels[asks.OriginLabel]; origin != "" {
		t.Fatalf("foreign ask was labelled %q", origin)
	}
}

func (f *grantAskFixture) runningPod() *corev1.Pod {
	f.t.Helper()
	var sess v1alpha1.AgentSession
	if err := f.base.Get(f.ctx, client.ObjectKey{Namespace: sessionNS, Name: f.g.Spec.Requester.Session}, &sess); err != nil {
		f.t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sessionNS, Name: sess.Name, UID: "pod-uid-grant-ask",
			Labels:          map[string]string{v1alpha1.LabelSession: sess.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&sess, v1alpha1.GroupVersion.WithKind("AgentSession"))},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if err := f.base.Create(f.ctx, pod); err != nil {
		f.t.Fatal(err)
	}
	return pod
}

func (f *grantAskFixture) attest(pod *corev1.Pod) {
	f.t.Helper()
	g := f.grant()
	st := grantattest.Statement{
		V: grantattest.Version,
		Attestor: grantattest.Attestor{
			Session: g.Spec.Requester.Session, SessionUID: string(g.Spec.Requester.SessionUID), PodUID: string(pod.UID),
		},
		Approver:    grantattest.Approver{Kind: grantattest.ApproverCoordinator, Ref: "meatball"},
		Ref:         "msg-1",
		GrantUID:    string(g.UID),
		ScopeDigest: grantattest.ScopeDigest(g),
		TTL:         g.Spec.TTL.Duration.String(),
		AttestedAt:  f.b.now().Time,
	}
	raw, err := st.Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[grantattest.Annotation] = raw
	if err := f.base.Update(f.ctx, g); err != nil {
		f.t.Fatal(err)
	}
}

func (f *grantAskFixture) armDelegate(at time.Time) {
	f.t.Helper()
	f.b.GrantDelegates = []string{dgDelegate}
	var sess v1alpha1.AgentSession
	if err := f.base.Get(f.ctx, client.ObjectKey{Namespace: sessionNS, Name: f.g.Spec.Requester.Session}, &sess); err != nil {
		f.t.Fatal(err)
	}
	sess.Spec.Parent = dgDelegate
	if err := f.base.Update(f.ctx, &sess); err != nil {
		f.t.Fatal(err)
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
	if err := f.base.Create(f.ctx, p); err != nil {
		f.t.Fatal(err)
	}
	if err := f.base.Create(f.ctx, delegateHostPod()); err != nil {
		f.t.Fatal(err)
	}
	g := f.grant()
	d := MaintenanceDecision{
		GrantUID: string(g.UID), Digest: grantattest.ScopeDigest(g), By: dgBy, Approve: true,
		TTL: g.Spec.TTL.Duration, At: at, Ref: dgRef, Reason: "fits the frontend runbook",
		Delegate: &DelegateIdentity{Form: 1, PodName: "host", PodUID: "host-pod", HostID: "host-a"},
	}
	raw, err := json.Marshal(d)
	if err != nil {
		f.t.Fatal(err)
	}
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[MaintenanceDecisionAnnotation] = string(raw)
	if err := f.base.Update(f.ctx, g); err != nil {
		f.t.Fatal(err)
	}
}

func attestedWorkloadsPolicy() *v1alpha1.GrantPolicy {
	return &v1alpha1.GrantPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: "frontend-attested"},
		Spec: v1alpha1.GrantPolicySpec{
			Description: "test", Type: v1alpha1.GrantKube,
			Attested: &v1alpha1.AttestedRequesters{Profiles: []string{"full"}},
			Kube:     &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: []string{"frontend"}},
			MaxTTL:   metav1.Duration{Duration: 2 * time.Hour},
		},
	}
}

func openEgressAsk(t *testing.T, e v1alpha1.EgressGrant) *grantAskFixture {
	t.Helper()
	f := newGrantAskFixture(t, true)
	g := f.grant()
	g.Spec.Type = v1alpha1.GrantEgress
	g.Spec.Kube = nil
	g.Spec.Egress = &e
	g.Spec.Reason = "reach a service"
	if err := f.base.Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.openAsk()
	return f
}

func openKubeAsk(t *testing.T, k *v1alpha1.KubeGrant) *grantAskFixture {
	t.Helper()
	f := newGrantAskFixture(t, true)
	g := f.grant()
	g.Spec.Kube = k
	g.Spec.Reason = "use the named secrets"
	if err := f.base.Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.openAsk()
	return f
}
