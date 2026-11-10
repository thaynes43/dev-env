package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

func TestGrantAskAttestationAfterFirstReconcileOpensNoAsk(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.b.EnableAttestedGrants = true
	pod := f.runningPod()
	if err := f.base.Create(f.ctx, attestedWorkloadsPolicy()); err != nil {
		t.Fatal(err)
	}
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	if f.askCount() != 0 || f.grant().Status.Ask != "" {
		t.Fatalf("before the pending pass: asks=%d route %q", f.askCount(), f.grant().Status.Ask)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := f.grant(); got.Status.Phase != v1alpha1.GrantPending || got.Status.Ask != "" || got.Status.ApprovedBy != "" || f.askCount() != 0 {
		t.Fatalf("first pending pass opened an ask: %+v asks=%d", got.Status, f.askCount())
	}
	f.attest(pod)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantActive })
	g := f.grant()
	if g.Status.ApprovedBy != "policy/frontend-attested" || g.Status.Ask != "" || f.askCount() != 0 {
		t.Fatalf("attestation after the first reconcile: %+v asks=%d", g.Status, f.askCount())
	}
}

func TestGrantAskUnroutedReadFailureStillDeniesAtThirtyMinutes(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	f.meta.failAskGet = apierrors.NewServiceUnavailable("ask")
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := f.grant(); got.Status.Phase != v1alpha1.GrantPending || got.Status.Ask != "" || f.askCount() != 0 {
		t.Fatalf("unread unrouted grant: %+v asks=%d", got.Status, f.askCount())
	}
	f.clk.Step(PendingTimeout)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByTimeout || g.Status.Message != "no answer in 30 minutes" || g.Status.Ask != "" || g.Status.ApprovedBy != "" {
		t.Fatalf("unread unrouted grant after 30 minutes: %+v", g.Status)
	}
}

func TestGrantAskRoutedUnreadableAskIsDenied(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	route := f.grant().Status.Ask
	p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
	if err := f.base.Create(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	f.meta.failAskGet = apierrors.NewServiceUnavailable("ask")
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := f.grant(); got.Status.Phase != v1alpha1.GrantPending || got.Status.Ask != route || got.Status.ApprovedBy != "" {
		t.Fatalf("first unread pass: %+v", got.Status)
	}
	f.clk.Step(grantAskUnreadLimit)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgUnread || g.Status.Ask != route || g.Status.ApprovedBy != "" {
		t.Fatalf("unreadable routed grant: %+v", g.Status)
	}
}

func TestGrantAskRoutedMissingAskIsDeniedAndNotRecreated(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	route := f.grant().Status.Ask
	if err := f.base.Delete(f.ctx, f.onlyAsk()); err != nil {
		t.Fatal(err)
	}
	p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
	if err := f.base.Create(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgGone || g.Status.Ask != route || g.Status.ApprovedBy != "" {
		t.Fatalf("missing routed ask: %+v", g.Status)
	}
	if f.askCount() != 0 {
		t.Fatalf("deleted ask was recreated: %d", f.askCount())
	}
}

func TestGrantAskRouteConflictDoesNotCreate(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if g, ok := obj.(*v1alpha1.AccessGrant); ok && g.Status.Ask != "" {
				return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants"}, g.Name, errors.New("conflict"))
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := f.grant(); got.Status.Phase != v1alpha1.GrantPending || got.Status.Ask != "" || got.Status.ApprovedBy != "" || f.askCount() != 0 {
		t.Fatalf("conflict created an ask: %+v asks=%d", got.Status, f.askCount())
	}
	f.b.Client = f.meta
	f.openAsk()
	if f.grant().Status.Ask == "" || f.askCount() != 1 {
		t.Fatalf("after the conflict cleared: route %q asks=%d", f.grant().Status.Ask, f.askCount())
	}
}

func TestGrantAskOriginAloneDoesNotTakeTheRoute(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	g := f.grant()
	name, ok := grantAskObjectName(f.b.SessionNamespace, g)
	if !ok {
		t.Fatal("name")
	}
	yes := true
	ask := &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: asks.Namespace,
			Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
		},
		Spec: coord.AskSpec{
			ID: name, Type: string(apiv1.AskTypeInfo), Question: "Which option?",
			RequestID: "grant-not-this-grant",
			Context:   coord.AskContext{SessionName: "other", SessionUID: "33333333-3333-4333-8333-333333333333"},
		},
	}
	if err := f.base.Create(f.ctx, ask); err != nil {
		t.Fatal(err)
	}
	ask.Status = coord.AskStatus{
		State: string(apiv1.AskStateAnswered),
		Answer: &coord.AskAnswer{
			Approved: &yes, AnsweredAt: metav1.NewTime(f.clk.Now()),
			AnsweredBy: coord.AskActor{Issuer: "https://issuer.example/grant-ask", Subject: "subject-1", Username: "tom"},
		},
	}
	if err := f.base.Status().Update(f.ctx, ask); err != nil {
		t.Fatal(err)
	}
	p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
	if err := f.base.Create(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if got.Status.Phase != v1alpha1.GrantActive || got.Status.ApprovedBy != "policy/frontend-workloads" || got.Status.Ask != "" {
		t.Fatalf("origin alone: %+v", got.Status)
	}
	if origin := f.onlyAsk().Labels[asks.OriginLabel]; origin != grantAskOriginBroker {
		t.Fatalf("origin %q", origin)
	}
}

func TestGrantAskCredentialOwnedAskIsAppliedBeforePolicy(t *testing.T) {
	for _, approved := range []bool{false, true} {
		name := "no"
		if approved {
			name = "yes"
		}
		t.Run(name, func(t *testing.T) {
			f := newGrantAskFixture(t, true)
			f.b.EnableProxmoxGrants = true
			g := f.grant()
			g.Spec.Type = v1alpha1.GrantCredential
			g.Spec.Kube = nil
			g.Spec.Credential = &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialProxmox}
			g.Spec.Reason = "maintain a guest"
			if err := f.base.Update(f.ctx, g); err != nil {
				t.Fatal(err)
			}
			p := &v1alpha1.GrantPolicy{
				ObjectMeta: metav1.ObjectMeta{Namespace: systemNS, Name: "pve-full"},
				Spec: v1alpha1.GrantPolicySpec{
					Profiles: []string{"full"}, Type: v1alpha1.GrantCredential,
					Credential: &v1alpha1.CredentialPolicy{Names: []v1alpha1.CredentialName{v1alpha1.CredentialProxmox}},
					MaxTTL:     metav1.Duration{Duration: time.Hour},
				},
			}
			if err := f.base.Create(f.ctx, p); err != nil {
				t.Fatal(err)
			}
			f.plantOwnedAsk("", &approved, "tom")
			f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
			got := f.grant()
			if got.Status.ApprovedBy != "" || got.Status.Ask == "" || f.askCount() != 1 {
				t.Fatalf("credential ask: %+v asks=%d", got.Status, f.askCount())
			}
			if !approved && got.Status.DeniedBy != "authentik/tom" {
				t.Fatalf("stored no: %+v", got.Status)
			}
			if approved && (got.Status.DeniedBy != DeniedByBroker || got.Status.Message != "the answer could not be applied") {
				t.Fatalf("stored yes: %+v", got.Status)
			}
			if got.Status.ApprovedBy == "policy/pve-full" || got.Status.Phase == v1alpha1.GrantActive {
				t.Fatalf("policy approved a routed credential: %+v", got.Status)
			}
		})
	}
}

func (f *grantAskFixture) plantOwnedAsk(detail string, approved *bool, user string) {
	f.t.Helper()
	g := f.grant()
	name, ok := grantAskObjectName(f.b.SessionNamespace, g)
	if !ok {
		f.t.Fatal("name")
	}
	id, ok := grantAskRequestID(g.UID)
	if !ok {
		f.t.Fatal("request id")
	}
	target, ok := grantAskTarget(g)
	if !ok {
		f.t.Fatal("target")
	}
	ttl, ok := grantAskTTL(g.Spec.TTL.Duration)
	if !ok {
		f.t.Fatal("ttl")
	}
	ask := &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: asks.Namespace,
			Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
		},
		Spec: coord.AskSpec{
			ID: name, Type: string(apiv1.AskTypeGrant), Question: "Access for this session?",
			RequestID: id,
			Context:   coord.AskContext{SessionName: g.Spec.Requester.Session, SessionUID: string(g.Spec.Requester.SessionUID)},
			Grant: &coord.AskGrant{
				UID: string(g.UID), ScopeDigest: grantattest.ScopeDigest(g),
				Type: string(g.Spec.Type), Target: target, TTL: ttl, Detail: detail,
			},
		},
	}
	if err := f.base.Create(f.ctx, ask); err != nil {
		f.t.Fatal(err)
	}
	if approved == nil {
		return
	}
	ask.Status = coord.AskStatus{
		State: string(apiv1.AskStateAnswered),
		Answer: &coord.AskAnswer{
			Approved: approved, AnsweredAt: metav1.NewTime(f.clk.Now()),
			AnsweredBy: coord.AskActor{Issuer: "https://issuer.example/grant-ask", Subject: "subject-1", Username: user},
		},
	}
	if err := f.base.Status().Update(f.ctx, ask); err != nil {
		f.t.Fatal(err)
	}
}
