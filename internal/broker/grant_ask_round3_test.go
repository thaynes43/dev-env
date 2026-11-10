package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// committingAsk stores the Ask, then reports that the create was not confirmed.
// A later reconcile must not call Create again.
type committingAsk struct {
	base  client.Client
	calls int
}

func (s *committingAsk) Create(ctx context.Context, req asks.Requester, in apiv1.AskCreateRequest, _ time.Time) (apiv1.Ask, bool, error) {
	s.calls++
	if in.Grant == nil {
		return apiv1.Ask{}, false, errors.New("no grant")
	}
	name := asks.Name(req, in.RequestID)
	ask := &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: asks.Namespace,
			Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
		},
		Spec: coord.AskSpec{
			ID: name, Type: string(in.Type), Question: in.Question, RequestID: in.RequestID,
			Context: coord.AskContext{SessionName: req.Name, SessionUID: req.UID},
			Grant: &coord.AskGrant{
				UID: in.Grant.UID, ScopeDigest: in.Grant.ScopeDigest, Type: in.Grant.Type,
				Target: in.Grant.Target, TTL: in.Grant.TTL, Detail: in.Grant.Detail,
			},
		},
	}
	if err := s.base.Create(ctx, ask); err != nil {
		return apiv1.Ask{}, false, err
	}
	ask.Status.State = string(apiv1.AskStateOpen)
	if err := s.base.Status().Update(ctx, ask); err != nil {
		return apiv1.Ask{}, false, err
	}
	return apiv1.Ask{ID: name}, true, fmt.Errorf("%w: the stored ask was not read back", asks.ErrCreateUnconfirmed)
}

func TestGrantAskUnconfirmedCreateIsNotRetried(t *testing.T) {
	f := newGrantAskFixture(t, true)
	stub := &committingAsk{base: f.base}
	f.b.grantAsks = stub
	f.until(func(g *v1alpha1.AccessGrant) bool {
		return g.Status.Phase == v1alpha1.GrantPending && stub.calls == 1 && f.askCount() == 1 && g.Status.Ask != ""
	})
	f.plantAnswer(boolPtr(false), "tom", "no")
	if ans := f.onlyAsk().Status.Answer; ans == nil || ans.Approved == nil || *ans.Approved {
		t.Fatal("the committed ask has no stored no")
	}
	if err := f.base.Delete(f.ctx, f.onlyAsk()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if stub.calls != 1 || f.askCount() != 0 || g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgGone || g.Status.ApprovedBy != "" {
		t.Fatalf("after the stored no was deleted: calls=%d asks=%d phase=%s deniedBy=%s message=%q", stub.calls, f.askCount(), g.Status.Phase, g.Status.DeniedBy, g.Status.Message)
	}
}

func TestGrantAskUnreadTimeSurvivesAFreshBroker(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	started := metav1.NewTime(f.b.now().Add(-time.Minute))
	g := f.grant()
	g.Status.AskUnreadAt = &started
	if err := f.base.Status().Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.b = &Broker{
		Client: f.meta, APIReader: f.meta, SessionNamespace: sessionNS, PolicyNamespace: systemNS,
		Clock: f.clk, EnableGrantAsks: true, Notifier: f.note,
	}
	f.meta.failAskGet = apierrors.NewServiceUnavailable("ask was not read")
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if got.Status.Phase != v1alpha1.GrantPending || got.Status.AskUnreadAt == nil || !got.Status.AskUnreadAt.Equal(&started) {
		t.Fatalf("fresh broker moved the unread time: phase %s at %v", got.Status.Phase, got.Status.AskUnreadAt)
	}
	f.clk.Step(time.Minute)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got = f.grant()
	if got.Status.Phase != v1alpha1.GrantDenied || got.Status.Message != grantAskMsgUnread || got.Status.AskUnreadAt == nil || !got.Status.AskUnreadAt.Equal(&started) {
		t.Fatalf("after the original wait: phase %s message %q at %v", got.Status.Phase, got.Status.Message, got.Status.AskUnreadAt)
	}
	if f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
		t.Fatalf("unreadable ask state %s", f.onlyAsk().Status.State)
	}
}

func TestGrantAskUnreadDenyConflictKeepsTheTime(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	started := metav1.NewTime(f.b.now().Add(-grantAskUnreadLimit))
	g := f.grant()
	g.Status.AskUnreadAt = &started
	if err := f.base.Status().Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.meta.failAskGet = apierrors.NewServiceUnavailable("ask was not read")
	f.b.APIReader = f.meta
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if g, ok := obj.(*v1alpha1.AccessGrant); ok && g.Status.Phase == v1alpha1.GrantDenied {
				return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants"}, g.Name, errors.New("conflict"))
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if got.Status.Phase != v1alpha1.GrantPending || got.Status.AskUnreadAt == nil || !got.Status.AskUnreadAt.Equal(&started) {
		t.Fatalf("failed deny: phase %s at %v", got.Status.Phase, got.Status.AskUnreadAt)
	}
	f.b.Client = f.meta
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got = f.grant()
	if got.Status.Phase != v1alpha1.GrantDenied || got.Status.Message != grantAskMsgUnread || got.Status.AskUnreadAt == nil || !got.Status.AskUnreadAt.Equal(&started) {
		t.Fatalf("retry: phase %s message %q at %v", got.Status.Phase, got.Status.Message, got.Status.AskUnreadAt)
	}
}

type emptyPolicyCache struct{ client.Client }

func (c emptyPolicyCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.GrantPolicyList); ok {
		return nil
	}
	return c.Client.List(ctx, list, opts...)
}

type countingPolicies struct {
	client.Client
	n int
}

func (c *countingPolicies) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.GrantPolicyList); ok {
		c.n++
	}
	return c.Client.List(ctx, list, opts...)
}

func TestGrantAskCachedPolicyDoesNotOpenAnAsk(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.b.EnableAttestedGrants = true
	if err := f.base.Create(f.ctx, attestedWorkloadsPolicy()); err != nil {
		t.Fatal(err)
	}
	live := &countingPolicies{Client: f.meta}
	f.b.APIReader = live
	f.b.Client = emptyPolicyCache{Client: f.meta}
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	live.n = 0
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if live.n != 1 || f.askCount() != 0 || g.Status.Ask != "" || g.Status.ApprovedBy != "" || g.Status.Phase != v1alpha1.GrantPending {
		t.Fatalf("cache lag: lists=%d asks=%d ask=%q approvedBy=%s phase=%s", live.n, f.askCount(), g.Status.Ask, g.Status.ApprovedBy, g.Status.Phase)
	}
}

func TestGrantAskPrunedRouteCreatesNoAsk(t *testing.T) {
	f := newGrantAskFixture(t, true)
	rec := &events.FakeRecorder{Events: make(chan string, 8)}
	f.b.Recorder = rec
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if g, ok := obj.(*v1alpha1.AccessGrant); ok && g.Status.Ask != "" {
				clone := g.DeepCopy()
				clone.Status.Ask = ""
				return c.SubResource(sub).Update(ctx, clone, opts...)
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	f.b.APIReader = f.meta
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Ask != "" || f.askCount() != 0 || g.Status.Phase != v1alpha1.GrantPending || g.Status.DeniedBy != "" {
		t.Fatalf("pruned route: %+v asks=%d", g.Status, f.askCount())
	}
	if msgs := strings.Join(eventTexts(rec), "\n"); !strings.Contains(msgs, "the grant's CRD does not store the route") {
		t.Fatalf("events %q", msgs)
	}
	f.clk.Step(PendingTimeout)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g = f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByTimeout || g.Status.Message != "no answer in 30 minutes" || g.Status.Ask != "" || f.askCount() != 0 {
		t.Fatalf("after 30 minutes: %+v asks=%d", g.Status, f.askCount())
	}
}

// The route is absent from the grant the writer was given and present only on
// the object its final read returns. Removing the check on that object would
// write Active.
type routeOnGrantRead struct {
	client.Client
	name string
	gets int
}

func (r *routeOnGrantRead) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if g, ok := obj.(*v1alpha1.AccessGrant); ok {
		r.gets++
		g.Status.Ask = r.name
	}
	return nil
}

func TestGrantAskMaintenanceRouteOnFinalReadDoesNotApprove(t *testing.T) {
	f := amCase(t)
	f.attest(f.statement())
	g := f.grant()
	g.Status.Phase = v1alpha1.GrantPending
	if err := f.c.Status().Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	reader := &routeOnGrantRead{Client: f.c, name: "ask-0123456789abcdef"}
	f.b.APIReader = reader
	approved, err := f.b.attestedMaintenanceApproval(context.Background(), f.grant(), f.sess, f.b.now())
	if approved || err != nil {
		t.Fatalf("approved %v err %v", approved, err)
	}
	got := f.grant()
	if got.Status.Phase == v1alpha1.GrantActive || got.Status.ApprovedBy != "" || reader.gets != 1 {
		t.Fatalf("stored phase %s approvedBy %q grant reads %d", got.Status.Phase, got.Status.ApprovedBy, reader.gets)
	}
}

func TestGrantAskDelegateConflictStoresRouteAndDoesNotApprove(t *testing.T) {
	// A reserved decision uses this same Decide loop. The first status write
	// that would approve conflicts and leaves a real route in the store. The
	// next attempt is allowed to write, and must not.
	f := newGrantAskFixture(t, true)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	f.armDelegate(f.b.now().Time)
	name, ok := grantAskObjectName(f.b.SessionNamespace, f.grant())
	if !ok {
		t.Fatal("name")
	}
	activeWrites := 0
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			g, ok := obj.(*v1alpha1.AccessGrant)
			if !ok || g.Status.Phase != v1alpha1.GrantActive {
				return c.SubResource(sub).Update(ctx, obj, opts...)
			}
			activeWrites++
			if activeWrites == 1 {
				stored := f.grant()
				stored.Status.Ask = name
				if err := f.base.Status().Update(ctx, stored); err != nil {
					return err
				}
				return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants"}, g.Name, errors.New("route stored"))
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	f.b.APIReader = f.meta
	g := f.grant()
	err := f.b.Decide(f.ctx, g.Name, Decision{
		Approve: true, By: dgBy, TTL: g.Spec.TTL.Duration,
		GrantUID: string(g.UID), ScopeDigest: grantattest.ScopeDigest(g), Ref: dgRef, Policy: dgPolicy,
	})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Decide: %v", err)
	}
	got := f.grant()
	if got.Status.Phase == v1alpha1.GrantActive || got.Status.ApprovedBy != "" || got.Status.Ask != name || activeWrites != 1 {
		t.Fatalf("phase %s approvedBy %q ask %q active writes %d", got.Status.Phase, got.Status.ApprovedBy, got.Status.Ask, activeWrites)
	}
}

func TestGrantAskBrokerDenyAskState(t *testing.T) {
	t.Run("own no stays answered", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		f.answer(boolPtr(false), "tom", "no")
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		g := f.grant()
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != "authentik/tom" {
			t.Fatalf("%+v", g.Status)
		}
		if f.onlyAsk().Status.State != string(apiv1.AskStateAnswered) {
			t.Fatalf("state %s", f.onlyAsk().Status.State)
		}
	})
	t.Run("open scope mismatch is withdrawn", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		ask := f.onlyAsk()
		ask.Spec.Grant.TTL = "2h0m0s"
		if err := f.base.Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		g := f.grant()
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.Message != "the scope no longer matches" {
			t.Fatalf("%+v", g.Status)
		}
		if f.onlyAsk().Status.State != string(apiv1.AskStateWithdrawn) {
			t.Fatalf("state %s", f.onlyAsk().Status.State)
		}
	})
	t.Run("answered scope mismatch stays answered", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		f.answer(boolPtr(true), "tom", "")
		ask := f.onlyAsk()
		ask.Spec.Grant.TTL = "2h0m0s"
		if err := f.base.Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != "the scope no longer matches" || f.onlyAsk().Status.State != string(apiv1.AskStateAnswered) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("open detail mismatch is withdrawn", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		ask := f.onlyAsk()
		ask.Spec.Grant.Detail = "other-namespace"
		if err := f.base.Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != grantAskMsgRemake || f.onlyAsk().Status.State != string(apiv1.AskStateWithdrawn) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("answered detail mismatch stays answered", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		f.answer(boolPtr(true), "tom", "")
		ask := f.onlyAsk()
		ask.Spec.Grant.Detail = "other-namespace"
		if err := f.base.Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != grantAskMsgRemake || f.onlyAsk().Status.State != string(apiv1.AskStateAnswered) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("bad username stays answered", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		f.plantAnswer(boolPtr(true), "not a user", "")
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != "the answer has no usable username" || f.onlyAsk().Status.State != string(apiv1.AskStateAnswered) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("could not be applied stays answered", func(t *testing.T) {
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
		yes := true
		f.plantOwnedAsk("", &yes, "tom")
		f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
		if f.grant().Status.Message != "the answer could not be applied" || f.onlyAsk().Status.State != string(apiv1.AskStateAnswered) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("already withdrawn stays withdrawn", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		ask := f.onlyAsk()
		ask.Status.State = string(apiv1.AskStateWithdrawn)
		if err := f.base.Status().Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != "the grant ask was withdrawn" || f.onlyAsk().Status.State != string(apiv1.AskStateWithdrawn) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("gone has no ask", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		if err := f.base.Delete(f.ctx, f.onlyAsk()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != grantAskMsgGone || f.askCount() != 0 {
			t.Fatalf("%+v asks=%d", f.grant().Status, f.askCount())
		}
	})
	t.Run("foreign ask is untouched", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
		g := f.grant()
		name, ok := grantAskObjectName(f.b.SessionNamespace, g)
		if !ok {
			t.Fatal("name")
		}
		ask := &coord.Ask{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: asks.Namespace,
				Labels: map[string]string{"keep": "yes"},
			},
			Spec: coord.AskSpec{ID: name, Type: "info", Question: "foreign", RequestID: "foreign-1"},
		}
		if err := f.base.Create(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		ask.Status.State = string(apiv1.AskStateOpen)
		if err := f.base.Status().Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		g.Status.Ask = name
		if err := f.base.Status().Update(f.ctx, g); err != nil {
			t.Fatal(err)
		}
		before := f.onlyAsk()
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		after := f.onlyAsk()
		if f.grant().Status.Message != grantAskMsgForeign || after.Status.State != before.Status.State || after.Labels["keep"] != "yes" || after.Labels[asks.OriginLabel] != "" || after.Spec.Question != before.Spec.Question {
			t.Fatalf("grant %+v ask state %s labels %v", f.grant().Status, after.Status.State, after.Labels)
		}
	})
	t.Run("unreadable ask stays open", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		f.meta.failAskGet = apierrors.NewServiceUnavailable("ask was not read")
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Phase != v1alpha1.GrantPending || f.grant().Status.AskUnreadAt == nil {
			t.Fatalf("first failure did not store the unread time: %+v", f.grant().Status)
		}
		f.clk.Step(grantAskUnreadLimit)
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		f.meta.failAskGet = nil
		if f.grant().Status.Message != grantAskMsgUnread || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("ended session stays open", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		if err := f.base.Delete(f.ctx, f.session); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != "the session ended" || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
			t.Fatalf("grant %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
}

func TestGrantAskFailedWithdrawRetries(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	ask := f.onlyAsk()
	ask.Spec.Grant.TTL = "2h0m0s"
	if err := f.base.Update(f.ctx, ask); err != nil {
		t.Fatal(err)
	}
	failed := 0
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if _, ok := obj.(*coord.Ask); ok && failed == 0 {
				failed++
				return apierrors.NewServiceUnavailable("withdraw failed")
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	f.b.APIReader = f.meta
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if f.grant().Status.Phase != v1alpha1.GrantDenied || f.grant().Status.Message != "the scope no longer matches" || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) || failed != 1 {
		t.Fatalf("first pass: %+v ask %s failed %d", f.grant().Status, f.onlyAsk().Status.State, failed)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if f.grant().Status.Phase != v1alpha1.GrantDenied || f.onlyAsk().Status.State != string(apiv1.AskStateWithdrawn) {
		t.Fatalf("retry: %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
	}
}
