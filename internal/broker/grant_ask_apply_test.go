package broker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestGrantAskAnswerGrantsOnceAndRepeatIsNotPending(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	created := f.grant().CreationTimestamp.Time
	if !created.Equal(time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("creation timestamp %s", created)
	}
	f.clk.Step(PendingTimeout)
	spy := f.watchDecide(true)
	f.answer(boolPtr(true), "tom", "")
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantActive })
	g := f.grant()
	if g.Status.ApprovedBy != "authentik/tom" || g.Status.DeniedBy != "" {
		t.Fatalf("approval %+v", g.Status)
	}
	if g.Status.ApprovedTTL == nil || g.Status.ApprovedTTL.Duration != g.Spec.TTL.Duration {
		t.Fatalf("approved ttl %+v", g.Status.ApprovedTTL)
	}
	if spy.calls != 1 || !spy.last.Approve || spy.last.By != "authentik/tom" || spy.last.TTL != 0 {
		t.Fatalf("decision calls=%d approve=%v by=%s ttl=%s", spy.calls, spy.last.Approve, spy.last.By, spy.last.TTL)
	}
	if f.askCount() != 1 || f.grantCount() != 1 {
		t.Fatalf("asks=%d grants=%d", f.askCount(), f.grantCount())
	}
	err := f.b.Decide(f.ctx, f.g.Name, Decision{Approve: true, By: "authentik/tom"})
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("repeat: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("repeat called Decide again: %d", spy.calls)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := f.grant(); got.Status.Phase != v1alpha1.GrantActive || got.Status.ApprovedBy != "authentik/tom" {
		t.Fatalf("after the repeat reconcile: %+v", got.Status)
	}
	if f.askCount() != 1 || f.grantCount() != 1 {
		t.Fatalf("repeat opened asks=%d grants=%d", f.askCount(), f.grantCount())
	}
}

func TestGrantAskScopeMismatchDeniesWithoutDecide(t *testing.T) {
	for _, tc := range []string{"digest", "type", "target", "ttl"} {
		t.Run(tc, func(t *testing.T) {
			f := newGrantAskFixture(t, true)
			f.openAsk()
			f.answer(boolPtr(true), "tom", "")
			ask := f.onlyAsk()
			switch tc {
			case "digest":
				ask.Spec.Grant.ScopeDigest = strings.Repeat("a", 43)
			case "type":
				ask.Spec.Grant.Type = string(v1alpha1.GrantEgress)
			case "target":
				ask.Spec.Grant.Target = "other-target"
			case "ttl":
				ask.Spec.Grant.TTL = "2h0m0s"
			}
			if err := f.base.Update(f.ctx, ask); err != nil {
				t.Fatal(err)
			}
			spy := f.watchDecide(false)
			if _, err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != "the scope no longer matches" || g.Status.ApprovedBy != "" {
				t.Fatalf("pre-check: phase %s deniedBy %s message %q approvedBy %s", g.Status.Phase, g.Status.DeniedBy, g.Status.Message, g.Status.ApprovedBy)
			}
			if spy.calls != 0 {
				t.Fatalf("Decide calls = %d, want 0", spy.calls)
			}
			if f.askCount() != 1 || f.grantCount() != 1 {
				t.Fatalf("asks=%d grants=%d", f.askCount(), f.grantCount())
			}
		})
	}
}

func TestGrantAskDetailMismatchStillGrants(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(true), "tom", "")
	ask := f.onlyAsk()
	ask.Spec.Grant.Detail = "other-namespace"
	if err := f.base.Update(f.ctx, ask); err != nil {
		t.Fatal(err)
	}
	spy := f.watchDecide(true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantActive })
	if spy.calls != 1 || f.grant().Status.ApprovedBy != "authentik/tom" {
		t.Fatalf("detail mismatch calls=%d approvedBy=%s", spy.calls, f.grant().Status.ApprovedBy)
	}
}

func TestGrantAskUIDChangeDoesNotDecideReplacement(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(true), "tom", "")
	ask := f.onlyAsk()
	ask.Spec.Grant.UID = "33333333-3333-4333-8333-333333333333"
	if err := f.base.Update(f.ctx, ask); err != nil {
		t.Fatal(err)
	}
	spy := f.watchDecide(false)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.DeniedBy != "" || g.Status.ApprovedBy != "" {
		t.Fatalf("uid change: %+v", g.Status)
	}
	if spy.calls != 0 {
		t.Fatalf("Decide calls = %d", spy.calls)
	}
	if f.askCount() != 1 || f.grantCount() != 1 {
		t.Fatalf("asks=%d grants=%d", f.askCount(), f.grantCount())
	}
}

func TestGrantAskConflictRetries(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(true), "tom", "")
	conflicts := 0
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if _, ok := obj.(*v1alpha1.AccessGrant); ok {
				conflicts++
				return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants"}, obj.GetName(), errors.New("conflict"))
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.DeniedBy != "" || g.Status.ApprovedBy != "" {
		t.Fatalf("conflict denied or approved the grant: %+v", g.Status)
	}
	if conflicts != 5 {
		t.Fatalf("status conflicts = %d, want 5", conflicts)
	}
	first := conflicts
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if conflicts <= first {
		t.Fatalf("second pass did not retry: %d then %d", first, conflicts)
	}
	if f.grant().Status.Phase != v1alpha1.GrantPending {
		t.Fatalf("retry left %+v", f.grant().Status)
	}
	f.b.Client = f.meta
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantActive })
	if f.grant().Status.ApprovedBy != "authentik/tom" {
		t.Fatalf("after the conflict cleared: %+v", f.grant().Status)
	}
}

func TestGrantAskTransportErrorRetries(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(true), "tom", "")
	f.meta.failGrantGet = apierrors.NewServiceUnavailable("transport")
	f.meta.grantGetsLeft = 1
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.DeniedBy != "" || g.Status.ApprovedBy != "" {
		t.Fatalf("transport error ended the grant: %+v", g.Status)
	}
	f.meta.failGrantGet = nil
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantActive })
	if f.grant().Status.ApprovedBy != "authentik/tom" {
		t.Fatalf("after the read recovered: %+v", f.grant().Status)
	}
}

func TestGrantAskOtherRefusalDenies(t *testing.T) {
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
	f.openAsk()
	if got := f.onlyAsk().Spec.Grant.Target; got != string(v1alpha1.CredentialProxmox) {
		t.Fatalf("target %s", got)
	}
	f.answer(boolPtr(true), "tom", "")
	spy := f.watchDecide(true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	got := f.grant()
	if got.Status.DeniedBy != DeniedByBroker || got.Status.Message != "the answer could not be applied" || got.Status.ApprovedBy != "" {
		t.Fatalf("refusal: deniedBy %s message %q approvedBy %s", got.Status.DeniedBy, got.Status.Message, got.Status.ApprovedBy)
	}
	if spy.calls != 1 || spy.last.By != "authentik/tom" || !spy.last.Approve {
		t.Fatalf("Decide calls=%d by=%s approve=%v", spy.calls, spy.last.By, spy.last.Approve)
	}
	if !errors.Is(spy.err, ErrRefused) || !strings.Contains(spy.err.Error(), "credential approvals require a matching standing policy") {
		t.Fatalf("stored refusal was not the credential rule: %v", spy.err)
	}
}

func TestGrantAskDeclineDeniesAsTheUser(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(false), "tom", "not this one")
	spy := f.watchDecide(true)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	g := f.grant()
	if g.Status.DeniedBy != "authentik/tom" || g.Status.Message != "not this one" || g.Status.ApprovedBy != "" {
		t.Fatalf("decline: deniedBy %s message %q", g.Status.DeniedBy, g.Status.Message)
	}
	if spy.calls != 1 || spy.last.Approve || spy.last.By != "authentik/tom" {
		t.Fatalf("decision calls=%d approve=%v by=%s", spy.calls, spy.last.Approve, spy.last.By)
	}
}

func TestGrantAskNoUsernameDeniesWithoutDecide(t *testing.T) {
	for _, user := range []string{"", "bad/name"} {
		t.Run(user, func(t *testing.T) {
			f := newGrantAskFixture(t, true)
			f.openAsk()
			f.answer(boolPtr(true), user, "")
			spy := f.watchDecide(false)
			if _, err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != "the answer has no usable username" {
				t.Fatalf("username %q: phase %s deniedBy %s message %q", user, g.Status.Phase, g.Status.DeniedBy, g.Status.Message)
			}
			if spy.calls != 0 {
				t.Fatal("Decide was called")
			}
		})
	}
}

func TestGrantAskByOnlyDoesNotPassTheRecheck(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	err := f.b.Decide(f.ctx, f.g.Name, Decision{Approve: true, By: "authentik/tom"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("by only: %v", err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || g.Status.DeniedBy != "" {
		t.Fatalf("by only changed the grant: %+v", g.Status)
	}
}

func TestGrantAskFlagOffDecideKeepsTheAgeCheck(t *testing.T) {
	f := newGrantAskFixture(t, false)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	if err := f.b.Decide(f.ctx, f.g.Name, Decision{Approve: true, By: "authentik/tom"}); err != nil {
		t.Fatal(err)
	}
	if g := f.grant(); g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "authentik/tom" {
		t.Fatalf("flag off approval: %+v", g.Status)
	}
	if f.askCount() != 0 {
		t.Fatalf("flag off opened %d asks", f.askCount())
	}

	late := newGrantAskFixture(t, false)
	late.until(func(g *v1alpha1.AccessGrant) bool {
		return g.Status.Phase == v1alpha1.GrantPending && g.Status.NotifiedAt != nil
	})
	late.clk.Step(PendingTimeout)
	err := late.b.Decide(late.ctx, late.g.Name, Decision{Approve: true, By: "authentik/tom"})
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("flag off after 30 minutes: %v", err)
	}
	if late.grant().Status.Phase != v1alpha1.GrantPending {
		t.Fatalf("Decide wrote %+v", late.grant().Status)
	}
	if _, err := late.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := late.grant()
	if got.Status.Phase != v1alpha1.GrantDenied || got.Status.DeniedBy != DeniedByTimeout || got.Status.Message != "no answer in 30 minutes" {
		t.Fatalf("flag off deny: %+v", got.Status)
	}
	if late.askCount() != 0 {
		t.Fatalf("flag off opened %d asks", late.askCount())
	}
}

func TestGrantAskAgeSkipDoesNotCoverDelegateOrMaintenance(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.clk.Step(PendingTimeout)
	now := f.b.now().Time
	g := f.grant()
	for _, by := range []string{f.b.maintenanceHumanApprover(), DelegateApprover + "dev-env-system/delegate", PolicyApprover + "frontend-workloads"} {
		_, err := f.b.decisionAllowed(f.ctx, g, Decision{Approve: true, By: by}, now)
		if !errors.Is(err, ErrNotPending) {
			t.Fatalf("%s after 30 minutes: %v", by, err)
		}
	}
	if f.grant().Status.Phase != v1alpha1.GrantPending {
		t.Fatalf("age check wrote %+v", f.grant().Status)
	}
}

func TestGrantAskEndedSessionDoesNotDecide(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(true), "tom", "")
	f.meta.sessionDeleting = true
	spy := f.watchDecide(false)
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	g := f.grant()
	if g.Status.Message != "the session ended" || g.Status.ApprovedBy != "" {
		t.Fatalf("ended session: %+v", g.Status)
	}
	if spy.calls != 0 {
		t.Fatal("Decide ran for an ended session")
	}
	if st := f.onlyAsk().Status.State; st != "answered" {
		t.Fatalf("ask state %s", st)
	}
}

func boolPtr(v bool) *bool { return &v }
