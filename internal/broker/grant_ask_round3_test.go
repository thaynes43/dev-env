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
	"k8s.io/apimachinery/pkg/types"
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
	t.Run("pruned route", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		rec := &events.FakeRecorder{Events: make(chan string, 8)}
		f.b.Recorder = rec
		f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
		f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if g, ok := obj.(*v1alpha1.AccessGrant); ok && g.Status.Ask != "" && g.Status.Phase != v1alpha1.GrantDenied {
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
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgRouteNotStored || g.Status.ApprovedBy != "" || g.Status.Ask != "" || f.askCount() != 0 {
			t.Fatalf("pruned route: %+v asks=%d", g.Status, f.askCount())
		}
		if msgs := strings.Join(eventTexts(rec), "\n"); !strings.Contains(msgs, "the grant's CRD does not store the route") {
			t.Fatalf("events %q", msgs)
		}
		p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
		if err := f.base.Create(f.ctx, &p); err != nil {
			t.Fatal(err)
		}
		f.armDelegate(f.b.now().Time)
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		g = f.grant()
		err := f.b.Decide(f.ctx, g.Name, Decision{
			Approve: true, By: dgBy, TTL: g.Spec.TTL.Duration,
			GrantUID: string(g.UID), ScopeDigest: grantattest.ScopeDigest(g), Ref: dgRef, Policy: dgPolicy,
		})
		if !errors.Is(err, ErrNotPending) {
			t.Fatalf("Decide after the deny: %v", err)
		}
		g = f.grant()
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.ApprovedBy != "" || g.Status.Message != grantAskMsgRouteNotStored {
			t.Fatalf("after policy and delegate: %+v", g.Status)
		}
	})
	t.Run("pruned unread time", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		route := f.grant().Status.Ask
		f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if g, ok := obj.(*v1alpha1.AccessGrant); ok && g.Status.AskUnreadAt != nil && g.Status.Phase != v1alpha1.GrantDenied {
					clone := g.DeepCopy()
					clone.Status.AskUnreadAt = nil
					return c.SubResource(sub).Update(ctx, clone, opts...)
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		})
		f.b.APIReader = f.meta
		f.meta.failAskGet = apierrors.NewServiceUnavailable("ask was not read")
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		g := f.grant()
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgUnreadNotStored || g.Status.ApprovedBy != "" || g.Status.Ask != route || g.Status.AskUnreadAt != nil {
			t.Fatalf("pruned unread time: %+v", g.Status)
		}
	})
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
		// Same requester and an otherwise valid grant Ask, without the
		// broker origin. The store's requester check would allow a withdraw.
		f := newGrantAskFixture(t, true)
		f.openAsk()
		ask := f.onlyAsk()
		delete(ask.Labels, asks.OriginLabel)
		if err := f.base.Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		before := f.onlyAsk()
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		after := f.onlyAsk()
		g := f.grant()
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgForeign || g.Status.ApprovedBy != "" {
			t.Fatalf("grant %+v", g.Status)
		}
		if after.UID != before.UID || after.Status.State != string(apiv1.AskStateOpen) || after.Labels[asks.OriginLabel] != "" || after.Labels[asks.RequesterLabel] == "" {
			t.Fatalf("ask uid %s state %s labels %v", after.UID, after.Status.State, after.Labels)
		}
	})
	t.Run("replacement between the checks", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		ask := f.onlyAsk()
		before := ask.UID
		ask.Spec.Grant.TTL = "2h0m0s"
		if err := f.base.Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		swap := &swapAskAfterDeny{Client: f.base, grant: client.ObjectKey{Namespace: sessionNS, Name: f.g.Name}}
		f.b.APIReader = swap
		f.b.Client = f.meta
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		after := f.onlyAsk()
		g := f.grant()
		if !swap.swapped || after.UID == before || after.UID != swap.newUID {
			t.Fatalf("swap swapped=%v before %s after %s planted %s", swap.swapped, before, after.UID, swap.newUID)
		}
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.Message != "the scope no longer matches" || g.Status.ApprovedBy != "" || after.Status.State != string(apiv1.AskStateOpen) {
			t.Fatalf("grant %+v ask %s", g.Status, after.Status.State)
		}
	})
	t.Run("origin change on a withdraw retry", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		ask := f.onlyAsk()
		ask.Spec.Grant.TTL = "2h0m0s"
		if err := f.base.Update(f.ctx, ask); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				ask, ok := obj.(*coord.Ask)
				if !ok || ask.Status.State != string(apiv1.AskStateWithdrawn) {
					return c.SubResource(sub).Update(ctx, obj, opts...)
				}
				attempts++
				if attempts == 1 {
					var stored coord.Ask
					if err := f.base.Get(ctx, client.ObjectKeyFromObject(ask), &stored); err != nil {
						return err
					}
					if stored.Labels == nil {
						stored.Labels = map[string]string{}
					}
					stored.Labels[asks.OriginLabel] = "replaced"
					if err := f.base.Update(ctx, &stored); err != nil {
						return err
					}
					return apierrors.NewConflict(schema.GroupResource{Group: coord.GroupVersion.Group, Resource: "asks"}, ask.Name, errors.New("origin changed"))
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		})
		f.b.APIReader = f.meta
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		after := f.onlyAsk()
		g := f.grant()
		if attempts != 1 || g.Status.Phase != v1alpha1.GrantDenied || g.Status.Message != "the scope no longer matches" || g.Status.ApprovedBy != "" || after.Status.State != string(apiv1.AskStateOpen) || after.Labels[asks.OriginLabel] != "replaced" {
			t.Fatalf("attempts %d grant %+v ask %s origin %q", attempts, g.Status, after.Status.State, after.Labels[asks.OriginLabel])
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

func TestGrantAskValidationReadBackIsNotRetried(t *testing.T) {
	f := newGrantAskFixture(t, true)
	live := &invalidAskReadBack{Client: f.meta}
	f.b.Client = live
	f.b.APIReader = live
	f.until(func(g *v1alpha1.AccessGrant) bool {
		return g.Status.Phase == v1alpha1.GrantPending && live.creates == 1 && f.askCount() == 1
	})
	if err := f.base.Delete(f.ctx, f.onlyAsk()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if live.creates != 1 || f.askCount() != 0 || g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgGone || g.Status.ApprovedBy != "" {
		t.Fatalf("after the invalid read-back and deletion: creates=%d asks=%d phase=%s deniedBy=%s message=%q", live.creates, f.askCount(), g.Status.Phase, g.Status.DeniedBy, g.Status.Message)
	}
}

// invalidAskReadBack returns one stored Ask with an empty question, so the
// store's read-back fails validation after the object has been created.
type invalidAskReadBack struct {
	client.Client
	creates   int
	corrupted bool
}

func (c *invalidAskReadBack) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*coord.Ask); ok {
		c.creates++
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *invalidAskReadBack) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if ask, ok := obj.(*coord.Ask); ok && !c.corrupted {
		c.corrupted = true
		ask.Spec.Question = ""
	}
	return nil
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
	res, err := f.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != time.Second || f.grant().Status.Phase != v1alpha1.GrantDenied || f.grant().Status.Message != "the scope no longer matches" || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) || failed != 1 {
		t.Fatalf("first pass: requeue %s %+v ask %s failed %d", res.RequeueAfter, f.grant().Status, f.onlyAsk().Status.State, failed)
	}
	f.meta.failAskGet = apierrors.NewServiceUnavailable("ask was not read")
	res, err = f.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != time.Second || failed != 1 || f.grant().Status.Phase != v1alpha1.GrantDenied || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
		t.Fatalf("read failure on the retry: requeue %s failed %d %+v ask %s", res.RequeueAfter, failed, f.grant().Status, f.onlyAsk().Status.State)
	}
	f.meta.failAskGet = nil
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if f.grant().Status.Phase != v1alpha1.GrantDenied || f.onlyAsk().Status.State != string(apiv1.AskStateWithdrawn) {
		t.Fatalf("recovered: %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
	}
}

// swapAskAfterDeny returns the Ask the broker already accepted, then replaces
// the stored object with an otherwise identical Ask of a new UID. The store's
// next read has to refuse that replacement.
type swapAskAfterDeny struct {
	client.Client
	grant   client.ObjectKey
	swapped bool
	newUID  types.UID
}

func (s *swapAskAfterDeny) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := s.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	ask, ok := obj.(*coord.Ask)
	if !ok || s.swapped {
		return nil
	}
	var g v1alpha1.AccessGrant
	if err := s.Client.Get(ctx, s.grant, &g); err != nil || g.Status.Phase != v1alpha1.GrantDenied {
		return nil
	}
	snapshot := ask.DeepCopy()
	s.swapped = true
	if err := s.Delete(ctx, snapshot); err != nil {
		return err
	}
	replacement := snapshot.DeepCopy()
	replacement.ResourceVersion = ""
	replacement.UID = snapshot.UID + "-replaced"
	replacement.Status = coord.AskStatus{}
	if err := s.Create(ctx, replacement); err != nil {
		return err
	}
	replacement.Status = snapshot.Status
	if err := s.Client.Status().Update(ctx, replacement); err != nil {
		return err
	}
	s.newUID = replacement.UID
	snapshot.DeepCopyInto(ask)
	return nil
}

func TestGrantAskInnerReadExpiresAcrossRestartAndConflict(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	f.answer(boolPtr(true), "tom", "")
	reader := &innerAskRead{Client: f.meta}
	f.b.APIReader = reader
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if reader.askGets < 2 || got.Status.Phase != v1alpha1.GrantPending || got.Status.AskUnreadAt == nil || got.Status.ApprovedBy != "" {
		t.Fatalf("inner read: gets %d %+v", reader.askGets, got.Status)
	}
	started := *got.Status.AskUnreadAt
	f.b = &Broker{
		Client: f.meta, APIReader: reader, SessionNamespace: sessionNS, PolicyNamespace: systemNS,
		Clock: f.clk, EnableGrantAsks: true, Notifier: f.note,
	}
	reader.askGets = 0
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got = f.grant()
	if got.Status.Phase != v1alpha1.GrantPending || got.Status.AskUnreadAt == nil || !got.Status.AskUnreadAt.Equal(&started) || got.Status.ApprovedBy != "" {
		t.Fatalf("restart: %+v", got.Status)
	}
	f.clk.Step(grantAskUnreadLimit)
	gate := &denyWriteGate{}
	f.b = &Broker{
		Client: interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if g, ok := obj.(*v1alpha1.AccessGrant); ok && gate.block && g.Status.Phase == v1alpha1.GrantDenied {
					return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants"}, g.Name, errors.New("conflict"))
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}),
		APIReader: reader, SessionNamespace: sessionNS, PolicyNamespace: systemNS,
		Clock: f.clk, EnableGrantAsks: true, Notifier: f.note,
	}
	gate.block = true
	reader.askGets = 0
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got = f.grant()
	if got.Status.Phase != v1alpha1.GrantPending || got.Status.AskUnreadAt == nil || !got.Status.AskUnreadAt.Equal(&started) || got.Status.ApprovedBy != "" {
		t.Fatalf("conflict: %+v", got.Status)
	}
	gate.block = false
	reader.askGets = 0
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got = f.grant()
	if got.Status.Phase != v1alpha1.GrantDenied || got.Status.DeniedBy != DeniedByBroker || got.Status.Message != grantAskMsgUnread || got.Status.ApprovedBy != "" || got.Status.AskUnreadAt == nil || !got.Status.AskUnreadAt.Equal(&started) {
		t.Fatalf("after the bound: %+v", got.Status)
	}
	if f.onlyAsk().Status.State != string(apiv1.AskStateAnswered) {
		t.Fatalf("ask state %s", f.onlyAsk().Status.State)
	}
}

type innerAskRead struct {
	client.Client
	askGets int
}

func (r *innerAskRead) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*coord.Ask); ok {
		r.askGets++
		if r.askGets > 1 {
			return apierrors.NewServiceUnavailable("inner ask was not read")
		}
	}
	return r.Client.Get(ctx, key, obj, opts...)
}

type denyWriteGate struct{ block bool }

func TestGrantAskStoreStatusAndFinalReadAreNotRetried(t *testing.T) {
	for _, mode := range []string{"status", "read"} {
		t.Run(mode, func(t *testing.T) {
			f := newGrantAskFixture(t, true)
			fault := &askCreateFault{mode: mode}
			f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*coord.Ask); ok {
						fault.creates++
						fault.created = true
					}
					return c.Create(ctx, obj, opts...)
				},
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*coord.Ask); ok && fault.mode == "read" && fault.created && !fault.failedRead {
						fault.failedRead = true
						return apierrors.NewServiceUnavailable("ask was not read back")
					}
					return c.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return f.meta.List(ctx, list, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if _, ok := obj.(*coord.Ask); ok && fault.mode == "status" {
						return apierrors.NewServiceUnavailable("ask status was not stored")
					}
					return c.SubResource(sub).Update(ctx, obj, opts...)
				},
			})
			f.b.APIReader = f.b.Client
			f.until(func(g *v1alpha1.AccessGrant) bool {
				return g.Status.Phase == v1alpha1.GrantPending && fault.creates == 1 && f.askCount() == 1
			})
			if err := f.base.Delete(f.ctx, f.onlyAsk()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			g := f.grant()
			if fault.creates != 1 || f.askCount() != 0 || g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgGone || g.Status.ApprovedBy != "" {
				t.Fatalf("%s: creates=%d asks=%d phase=%s deniedBy=%s message=%q", mode, fault.creates, f.askCount(), g.Status.Phase, g.Status.DeniedBy, g.Status.Message)
			}
		})
	}
}

type askCreateFault struct {
	mode       string
	creates    int
	created    bool
	failedRead bool
}
