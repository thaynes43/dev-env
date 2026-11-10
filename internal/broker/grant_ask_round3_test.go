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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
	t.Run("commit after the denial is withdrawn", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		stub := &grantAskStub{err: &asks.LimitError{Scope: asks.LimitOpenGrant}}
		f.b.grantAsks = stub
		f.until(func(g *v1alpha1.AccessGrant) bool {
			return g.Status.Phase == v1alpha1.GrantPending && g.Status.Ask != "" && stub.calls >= 1
		})
		// The metadata client gives the committed Ask a UID. Withdraw refuses an
		// object with an empty UID, and the fake client does not assign one.
		late := &lateCommitAsk{base: f.meta}
		f.b.grantAsks = late
		res, err := f.reconcile()
		if err != nil {
			t.Fatal(err)
		}
		g := f.grant()
		if late.calls != 1 || f.askCount() != 0 || res.RequeueAfter != Retention || g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgGone || g.Status.ApprovedBy != "" {
			t.Fatalf("before the commit: calls=%d asks=%d requeue=%s %+v", late.calls, f.askCount(), res.RequeueAfter, g.Status)
		}
		if err := late.commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		ask := f.onlyAsk()
		if ask.Name != g.Status.Ask || ask.UID == "" || ask.Status.State != string(apiv1.AskStateOpen) || ask.Labels[asks.StateLabel] != string(apiv1.AskStateOpen) {
			t.Fatalf("committed ask %s uid %q state %s label %s route %s", ask.Name, ask.UID, ask.Status.State, ask.Labels[asks.StateLabel], g.Status.Ask)
		}
		reqs := f.b.enqueueGrantAsk(f.ctx, ask)
		if len(reqs) != 1 || reqs[0].Name != g.Name || reqs[0].Namespace != sessionNS {
			t.Fatalf("enqueue %+v", reqs)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		got := f.onlyAsk()
		g = f.grant()
		if got.Status.State != string(apiv1.AskStateWithdrawn) || got.Labels[asks.StateLabel] != string(apiv1.AskStateWithdrawn) || g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.ApprovedBy != "" || g.Status.Message != grantAskMsgGone {
			t.Fatalf("after the event: grant %+v ask %s label %s", g.Status, got.Status.State, got.Labels[asks.StateLabel])
		}
	})
	t.Run("owner lookup failure is retried", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		pending := f.b.enqueueGrantAsk(f.ctx, f.onlyAsk())
		if len(pending) != 1 || pending[0].Name != f.g.Name {
			t.Fatalf("pending enqueue %+v", pending)
		}
		g := f.grant()
		ended := f.b.now()
		g.Status.Phase = v1alpha1.GrantDenied
		g.Status.DeniedBy = DeniedByBroker
		g.Status.Message = "the scope no longer matches"
		g.Status.EndedAt = &ended
		if err := f.base.Status().Update(f.ctx, g); err != nil {
			t.Fatal(err)
		}
		ask := f.onlyAsk()
		f.b.APIReader = &failGrantList{Client: f.meta, fail: true}
		if reqs := f.b.enqueueGrantAsk(f.ctx, ask); len(reqs) != 0 {
			t.Fatalf("failed lookup enqueued %+v", reqs)
		}
		f.b.APIReader = f.meta
		reqs, err := f.b.retryGrantAskOwners(f.ctx)
		if err != nil || len(reqs) != 1 || reqs[0].Name != f.g.Name {
			t.Fatalf("retry %+v %v", reqs, err)
		}
		q := &captureQueue{}
		f.b.APIReader = &failGrantList{Client: f.meta, fail: true}
		f.b.deliverGrantAsk(f.ctx, ask, q)
		if len(q.after) != 1 || q.after[0].d != time.Second || q.after[0].req.Name != grantAskOwnerRetryName || len(q.added) != 0 {
			t.Fatalf("schedule added %+v after %+v", q.added, q.after)
		}
		f.b.APIReader = f.meta
		res, err := f.b.Reconcile(f.ctx, q.after[0].req)
		if err != nil {
			t.Fatal(err)
		}
		if res.RequeueAfter != 0 || len(q.added) != 1 || q.added[0].Name != f.g.Name {
			t.Fatalf("sentinel added %+v requeue %s", q.added, res.RequeueAfter)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Phase != v1alpha1.GrantDenied || f.grant().Status.ApprovedBy != "" || f.onlyAsk().Status.State != string(apiv1.AskStateWithdrawn) {
			t.Fatalf("cleanup %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
	t.Run("session ended is not enqueued", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		f.meta.sessionDeleting = true
		f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
		if f.grant().Status.Message != "the session ended" || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
			t.Fatalf("setup %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
		if reqs := f.b.enqueueGrantAsk(f.ctx, f.onlyAsk()); len(reqs) != 0 {
			t.Fatalf("session ended enqueued %+v", reqs)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if f.grant().Status.Message != "the session ended" || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) || f.grant().Status.ApprovedBy != "" {
			t.Fatalf("after the event %+v ask %s", f.grant().Status, f.onlyAsk().Status.State)
		}
	})
}

// lateCommitAsk reports that the create was not confirmed and stores nothing
// until commit. The broker must not create again; the later object is what
// an Ask event has to withdraw.
type lateCommitAsk struct {
	base  client.Client
	calls int
	req   asks.Requester
	body  apiv1.AskCreateRequest
	now   time.Time
	saved bool
}

func (s *lateCommitAsk) Create(_ context.Context, req asks.Requester, in apiv1.AskCreateRequest, now time.Time) (apiv1.Ask, bool, error) {
	s.calls++
	s.req, s.body, s.now, s.saved = req, in, now, true
	return apiv1.Ask{}, false, fmt.Errorf("%w: the create left the client", asks.ErrCreateUnconfirmed)
}

func (s *lateCommitAsk) commit(ctx context.Context) error {
	if !s.saved {
		return errors.New("nothing to commit")
	}
	store := asks.KubeStore{Client: s.base, Live: s.base, Namespace: asks.Namespace}
	_, _, err := store.Create(ctx, s.req, s.body, s.now)
	return err
}

type failGrantList struct {
	client.Client
	fail bool
}

func (f *failGrantList) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.AccessGrantList); ok && f.fail {
		return apierrors.NewServiceUnavailable("grants were not listed")
	}
	return f.Client.List(ctx, list, opts...)
}

type queuedAfter struct {
	req reconcile.Request
	d   time.Duration
}

type captureQueue struct {
	added []reconcile.Request
	after []queuedAfter
}

func (q *captureQueue) Add(req reconcile.Request) { q.added = append(q.added, req) }

func (q *captureQueue) AddAfter(req reconcile.Request, d time.Duration) {
	q.after = append(q.after, queuedAfter{req: req, d: d})
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
	t.Run("pruned route withdraws the owned ask", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.openAsk()
		before := f.onlyAsk().UID
		g := f.grant()
		g.Status.Ask = ""
		if err := f.base.Status().Update(f.ctx, g); err != nil {
			t.Fatal(err)
		}
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
		reader := &failAskGetOnceDenied{Client: f.meta, grant: client.ObjectKey{Namespace: sessionNS, Name: f.g.Name}}
		f.b.APIReader = reader
		res, err := f.reconcile()
		if err != nil {
			t.Fatal(err)
		}
		g = f.grant()
		ask := f.onlyAsk()
		if !reader.failed || res.RequeueAfter != time.Second || g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgRouteNotStored || g.Status.Ask != "" || g.Status.ApprovedBy != "" || ask.UID != before || ask.Status.State != string(apiv1.AskStateOpen) {
			t.Fatalf("read failure: requeue %s failed %v grant %+v ask %s", res.RequeueAfter, reader.failed, g.Status, ask.Status.State)
		}
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		g = f.grant()
		ask = f.onlyAsk()
		if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByBroker || g.Status.Message != grantAskMsgRouteNotStored || g.Status.ApprovedBy != "" || g.Status.Ask != "" || ask.UID != before || ask.Status.State != string(apiv1.AskStateWithdrawn) || ask.Labels[asks.StateLabel] != string(apiv1.AskStateWithdrawn) {
			t.Fatalf("recovered: %+v ask %s label %s uid %s", g.Status, ask.Status.State, ask.Labels[asks.StateLabel], ask.UID)
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
	t.Run("replacement after the status write", func(t *testing.T) {
		assertLabelFence(t, "replace-after")
	})
	t.Run("replacement between the label read and the patch", func(t *testing.T) {
		assertLabelFence(t, "replace-between")
	})
	t.Run("origin change after the status write", func(t *testing.T) {
		assertLabelFence(t, "origin-after")
	})
	t.Run("origin change between the label read and the patch", func(t *testing.T) {
		assertLabelFence(t, "origin-between")
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
	if res.RequeueAfter != 2*time.Second || failed != 1 || f.grant().Status.Phase != v1alpha1.GrantDenied || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
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

func TestGrantAskInfoEventDoesNotDrainHeldOwners(t *testing.T) {
	f := newGrantAskFixture(t, true)
	reader := &countingGrantList{Client: f.meta, fail: true}
	f.b.APIReader = reader
	q := &captureQueue{}
	const extra = 3
	n := grantAskOwnerHeldMax + extra
	for i := range n {
		ask := &coord.Ask{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("ask-held-%02d", i),
				Namespace: asks.Namespace,
				Labels:    map[string]string{asks.OriginLabel: grantAskOriginBroker},
			},
			Spec: coord.AskSpec{Grant: &coord.AskGrant{UID: fmt.Sprintf("uid-%02d", i)}},
		}
		f.b.deliverGrantAsk(f.ctx, ask, q)
	}
	if got := len(f.b.grantAskOwnerHeld); got != grantAskOwnerHeldMax {
		t.Fatalf("held %d, want %d", got, grantAskOwnerHeldMax)
	}
	oldest := asks.Namespace + "/ask-held-00"
	if f.b.grantAskOwnerHeld[0].key != oldest {
		t.Fatalf("oldest %s", f.b.grantAskOwnerHeld[0].key)
	}
	dropped := asks.Namespace + "/" + fmt.Sprintf("ask-held-%02d", n-1)
	for _, h := range f.b.grantAskOwnerHeld {
		if h.key == dropped {
			t.Fatal("newest was held")
		}
	}
	if reader.lists != n {
		t.Fatalf("setup lists %d, want %d", reader.lists, n)
	}
	listed := reader.lists
	added := len(q.added)
	scheduled := len(q.after)
	info := &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{Name: "ask-info", Namespace: asks.Namespace},
		Spec:       coord.AskSpec{Type: string(apiv1.AskTypeInfo), Question: "status"},
	}
	f.b.deliverGrantAsk(f.ctx, info, q)
	if reader.lists != listed || len(q.added) != added || len(q.after) != scheduled || len(f.b.grantAskOwnerHeld) != grantAskOwnerHeldMax {
		t.Fatalf("info event before the retry: lists %d added %d scheduled %d held %d", reader.lists, len(q.added), len(q.after), len(f.b.grantAskOwnerHeld))
	}
	res, err := f.b.Reconcile(f.ctx, reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: f.b.SessionNamespace, Name: grantAskOwnerRetryName,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reader.lists != listed+1 {
		t.Fatalf("due retry listed %d times, want 1", reader.lists-listed)
	}
	if len(f.b.grantAskOwnerHeld) != grantAskOwnerHeldMax || f.b.grantAskOwnerHeld[0].key != oldest {
		t.Fatalf("after the due retry held %d oldest %s", len(f.b.grantAskOwnerHeld), f.b.grantAskOwnerHeld[0].key)
	}
	for _, h := range f.b.grantAskOwnerHeld {
		if h.key == dropped {
			t.Fatal("newest was held after the due retry")
		}
	}
	if res.RequeueAfter == 0 || len(q.added) != added {
		t.Fatalf("due retry requeue %s added %d", res.RequeueAfter, len(q.added))
	}

	// An arrival during a failing retry stays behind the older keys when the
	// snapshot has room. The retry lists once; the arrival lists for itself.
	f.b.grantAskOwnerHeld = f.b.grantAskOwnerHeld[:grantAskOwnerHeldMax-1]
	arrivedKey := asks.Namespace + "/ask-during-retry"
	listed = reader.lists
	reader.onList = func() {
		reader.onList = nil
		f.b.deliverGrantAsk(f.ctx, &coord.Ask{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ask-during-retry", Namespace: asks.Namespace,
				Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
			},
			Spec: coord.AskSpec{Grant: &coord.AskGrant{UID: "uid-during"}, Question: "arrived"},
		}, q)
	}
	res, err = f.b.Reconcile(f.ctx, reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: f.b.SessionNamespace, Name: grantAskOwnerRetryName,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reader.lists != listed+2 {
		t.Fatalf("arrival during the failing retry listed %d, want 2", reader.lists-listed)
	}
	if len(f.b.grantAskOwnerHeld) != grantAskOwnerHeldMax || f.b.grantAskOwnerHeld[0].key != oldest || f.b.grantAskOwnerHeld[len(f.b.grantAskOwnerHeld)-1].key != arrivedKey {
		t.Fatalf("after the arrival held %d first %s last %s", len(f.b.grantAskOwnerHeld), f.b.grantAskOwnerHeld[0].key, f.b.grantAskOwnerHeld[len(f.b.grantAskOwnerHeld)-1].key)
	}

	// A duplicate update keeps its place and takes the newer object. A new
	// key past the merge bound of 32 is dropped.
	listed = reader.lists
	overflowKey := asks.Namespace + "/ask-overflow"
	reader.onList = func() {
		reader.onList = nil
		f.b.deliverGrantAsk(f.ctx, &coord.Ask{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ask-held-00", Namespace: asks.Namespace,
				Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
			},
			Spec: coord.AskSpec{Grant: &coord.AskGrant{UID: "uid-00"}, Question: "replaced"},
		}, q)
		f.b.deliverGrantAsk(f.ctx, &coord.Ask{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ask-overflow", Namespace: asks.Namespace,
				Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
			},
			Spec: coord.AskSpec{Grant: &coord.AskGrant{UID: "uid-overflow"}},
		}, q)
		if len(f.b.grantAskOwnerHeld) != 2 {
			t.Fatalf("arrivals during the failing list: %d", len(f.b.grantAskOwnerHeld))
		}
	}
	res, err = f.b.Reconcile(f.ctx, reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: f.b.SessionNamespace, Name: grantAskOwnerRetryName,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reader.lists != listed+3 {
		t.Fatalf("duplicate and overflow listed %d, want 3", reader.lists-listed)
	}
	if len(f.b.grantAskOwnerHeld) != grantAskOwnerHeldMax || f.b.grantAskOwnerHeld[0].key != oldest {
		t.Fatalf("merge bound held %d first %s", len(f.b.grantAskOwnerHeld), f.b.grantAskOwnerHeld[0].key)
	}
	replaced, ok := f.b.grantAskOwnerHeld[0].obj.(*coord.Ask)
	if !ok || replaced.Spec.Question != "replaced" {
		t.Fatalf("duplicate update was not kept in place: %#v", f.b.grantAskOwnerHeld[0].obj)
	}
	sawArrived := false
	for _, h := range f.b.grantAskOwnerHeld {
		if h.key == overflowKey {
			t.Fatal("overflow past 32 was held")
		}
		if h.key == arrivedKey {
			sawArrived = true
		}
	}
	if !sawArrived {
		t.Fatal("the arrival was dropped from the older snapshot")
	}
	if res.RequeueAfter == 0 {
		t.Fatal("the failing retry did not requeue")
	}

	// A successful retry matches several owners with one list.
	f.b.grantAskOwnerHeld = nil
	const owners = 3
	want := map[string]bool{}
	for i := range owners {
		name := fmt.Sprintf("grant-owner-%d", i)
		uid := fmt.Sprintf("33333333-3333-4333-8333-%012d", i)
		if err := f.base.Create(f.ctx, &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{
			Namespace: sessionNS, Name: name, UID: types.UID(uid),
		}}); err != nil {
			t.Fatal(err)
		}
		want[name] = true
		f.b.deliverGrantAsk(f.ctx, &coord.Ask{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("ask-owner-%d", i), Namespace: asks.Namespace,
				Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
			},
			Spec: coord.AskSpec{Grant: &coord.AskGrant{UID: uid}},
		}, q)
	}
	f.b.deliverGrantAsk(f.ctx, &coord.Ask{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ask-owner-none", Namespace: asks.Namespace,
			Labels: map[string]string{asks.OriginLabel: grantAskOriginBroker},
		},
		Spec: coord.AskSpec{Grant: &coord.AskGrant{UID: "uid-none"}},
	}, q)
	if len(f.b.grantAskOwnerHeld) != owners+1 {
		t.Fatalf("held owners before the successful retry: %d", len(f.b.grantAskOwnerHeld))
	}
	reader.fail = false
	reader.onList = nil
	listed = reader.lists
	added = len(q.added)
	res, err = f.b.Reconcile(f.ctx, reconcile.Request{NamespacedName: types.NamespacedName{
		Namespace: f.b.SessionNamespace, Name: grantAskOwnerRetryName,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reader.lists != listed+1 {
		t.Fatalf("successful retry listed %d, want 1", reader.lists-listed)
	}
	if len(f.b.grantAskOwnerHeld) != 0 || res.RequeueAfter != 0 {
		t.Fatalf("after the successful retry held %d requeue %s", len(f.b.grantAskOwnerHeld), res.RequeueAfter)
	}
	if len(q.added) != added+owners {
		t.Fatalf("enqueued %d, want %d", len(q.added)-added, owners)
	}
	for _, req := range q.added[added:] {
		if req.Namespace != sessionNS || !want[req.Name] {
			t.Fatalf("enqueued %s/%s", req.Namespace, req.Name)
		}
		delete(want, req.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing owners %v", want)
	}
}

type countingGrantList struct {
	client.Client
	fail   bool
	lists  int
	onList func()
}

func (c *countingGrantList) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*v1alpha1.AccessGrantList); ok {
		c.lists++
		if c.onList != nil {
			c.onList()
		}
		if c.fail {
			return apierrors.NewServiceUnavailable("grants were not listed")
		}
	}
	return c.Client.List(ctx, list, opts...)
}

func TestGrantAskDeleteConflictKeepsCleanupBackoff(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	g := f.grant()
	ended := f.b.now()
	g.Status.Phase = v1alpha1.GrantDenied
	g.Status.DeniedBy = DeniedByBroker
	g.Status.Message = "the scope no longer matches"
	g.Status.EndedAt = &ended
	if err := f.base.Status().Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.clk.Step(Retention)
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*v1alpha1.AccessGrant); ok {
				return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants"}, obj.GetName(), errors.New("uid precondition"))
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	f.b.APIReader = f.meta
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for i, wait := range want {
		res, err := f.reconcile()
		if err != nil {
			t.Fatal(err)
		}
		got := f.grant()
		if res.RequeueAfter != wait || !got.DeletionTimestamp.IsZero() || got.Status.Phase != v1alpha1.GrantDenied || got.Status.ApprovedBy != "" {
			t.Fatalf("conflict %d requeue %s want %s deleted %v phase %s approvedBy %q", i, res.RequeueAfter, wait, !got.DeletionTimestamp.IsZero(), got.Status.Phase, got.Status.ApprovedBy)
		}
	}
	kept := f.grant()
	if stored, ok := f.b.grantAskCleanupWait[kept.UID]; !ok || stored != 8*time.Second {
		t.Fatalf("cleanup wait after conflicts: %v", f.b.grantAskCleanupWait)
	}
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*v1alpha1.AccessGrant); ok {
				return apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants"}, obj.GetName())
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	res, err := f.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if len(f.b.grantAskCleanupWait) != 0 || !got.DeletionTimestamp.IsZero() || got.Status.Phase != v1alpha1.GrantDenied || got.Status.ApprovedBy != "" {
		t.Fatalf("NotFound left wait %v deleted %v phase %s approvedBy %q", f.b.grantAskCleanupWait, !got.DeletionTimestamp.IsZero(), got.Status.Phase, got.Status.ApprovedBy)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("NotFound requeue %s", res.RequeueAfter)
	}
}

func TestGrantAskCleanupBackoffDoesNotBlockRetention(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	g := f.grant()
	ended := f.b.now()
	g.Status.Phase = v1alpha1.GrantDenied
	g.Status.DeniedBy = DeniedByBroker
	g.Status.Message = "the scope no longer matches"
	g.Status.EndedAt = &ended
	if err := f.base.Status().Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.meta.failAskGet = apierrors.NewServiceUnavailable("ask was not read")
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 64 * time.Second, 128 * time.Second,
		256 * time.Second, 5 * time.Minute, 5 * time.Minute,
	}
	for i, wait := range want {
		res, err := f.reconcile()
		if err != nil {
			t.Fatal(err)
		}
		got := f.grant()
		if res.RequeueAfter != wait || got.Status.Phase != v1alpha1.GrantDenied || !got.DeletionTimestamp.IsZero() || f.onlyAsk().Status.State != string(apiv1.AskStateOpen) {
			t.Fatalf("step %d requeue %s want %s phase %s deleted %v ask %s", i, res.RequeueAfter, wait, got.Status.Phase, got.DeletionTimestamp, f.onlyAsk().Status.State)
		}
	}
	if len(f.b.grantAskCleanupWait) == 0 {
		t.Fatal("cleanup wait was empty before retention")
	}
	f.clk.Step(Retention)
	res, err := f.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	// The fake client keeps an object that still has its revoke finalizer and
	// records the delete as a deletion timestamp. That timestamp is the
	// retention delete; the unreadable card is left open.
	got := f.grant()
	ask := f.onlyAsk()
	if res.RequeueAfter != 0 || got.DeletionTimestamp.IsZero() || got.Status.Phase != v1alpha1.GrantDenied || got.Status.DeniedBy != DeniedByBroker || got.Status.ApprovedBy != "" || ask.Status.State != string(apiv1.AskStateOpen) || ask.Labels[asks.StateLabel] == string(apiv1.AskStateWithdrawn) {
		t.Fatalf("retention requeue %s deleted %v grant %+v ask %s label %s", res.RequeueAfter, got.DeletionTimestamp, got.Status, ask.Status.State, ask.Labels[asks.StateLabel])
	}
	if len(f.b.grantAskCleanupWait) != 0 {
		t.Fatalf("successful delete left %v", f.b.grantAskCleanupWait)
	}
}

func TestGrantAskCommittedDeleteWithLostResponseClearsCleanupBackoff(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.openAsk()
	g := f.grant()
	ended := f.b.now()
	g.Status.Phase = v1alpha1.GrantDenied
	g.Status.DeniedBy = DeniedByBroker
	g.Status.Message = "the scope no longer matches"
	g.Status.EndedAt = &ended
	if err := f.base.Status().Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.meta.failAskGet = apierrors.NewServiceUnavailable("ask was not read")
	res, err := f.reconcile()
	if err != nil {
		t.Fatal(err)
	}
	uid := f.grant().UID
	if res.RequeueAfter != time.Second || f.b.grantAskCleanupWait[uid] == 0 {
		t.Fatalf("before retention requeue %s wait %v", res.RequeueAfter, f.b.grantAskCleanupWait)
	}
	f.clk.Step(Retention)
	f.b.Client = interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*v1alpha1.AccessGrant); ok {
				if err := c.Delete(ctx, obj, opts...); err != nil {
					return err
				}
				return errors.New("read the delete response: EOF")
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	if _, err := f.reconcile(); err == nil || !strings.Contains(err.Error(), "read the delete response") {
		t.Fatalf("lost response: %v", err)
	}
	lost := f.grant()
	if lost.DeletionTimestamp.IsZero() || lost.Status.Phase != v1alpha1.GrantDenied || lost.Status.ApprovedBy != "" || f.b.grantAskCleanupWait[uid] == 0 {
		t.Fatalf("committed delete deleted %v phase %s approvedBy %q wait %v", lost.DeletionTimestamp, lost.Status.Phase, lost.Status.ApprovedBy, f.b.grantAskCleanupWait)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.b.grantAskCleanupWait[uid]; ok || len(f.b.grantAskCleanupWait) != 0 {
		t.Fatalf("finalized grant left cleanup wait %v", f.b.grantAskCleanupWait)
	}
}

type failAskGetOnceDenied struct {
	client.Client
	grant  client.ObjectKey
	failed bool
}

func (f *failAskGetOnceDenied) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*coord.Ask); ok && !f.failed {
		var g v1alpha1.AccessGrant
		if err := f.Client.Get(ctx, f.grant, &g); err == nil && g.Status.Phase == v1alpha1.GrantDenied {
			f.failed = true
			return apierrors.NewServiceUnavailable("ask was not read")
		}
	}
	return f.Client.Get(ctx, key, obj, opts...)
}

func assertLabelFence(t *testing.T, mode string) {
	t.Helper()
	f := newGrantAskFixture(t, true)
	f.openAsk()
	ask := f.onlyAsk()
	before := ask.UID
	ask.Spec.Grant.TTL = "2h0m0s"
	if err := f.base.Update(f.ctx, ask); err != nil {
		t.Fatal(err)
	}
	gate := &labelFence{base: f.base, mode: mode}
	wrapped := interceptor.NewClient(f.base.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			return gate.afterGet(ctx, obj)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if err := c.SubResource(sub).Update(ctx, obj, opts...); err != nil {
				return err
			}
			return gate.afterStatus(ctx, obj)
		},
	})
	f.b.Client = wrapped
	f.b.APIReader = wrapped
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if !gate.moved {
		t.Fatal("the label window was not reached")
	}
	got := f.onlyAsk()
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.Message != "the scope no longer matches" || g.Status.ApprovedBy != "" {
		t.Fatalf("grant %+v", g.Status)
	}
	if got.Labels[asks.StateLabel] == string(apiv1.AskStateWithdrawn) {
		t.Fatalf("withdrew the label of %s: %v", mode, got.Labels)
	}
	switch mode {
	case "replace-after", "replace-between":
		if got.UID == before || got.UID != gate.newUID || got.Status.State != string(apiv1.AskStateOpen) {
			t.Fatalf("%s uid %s -> %s planted %s state %s", mode, before, got.UID, gate.newUID, got.Status.State)
		}
	case "origin-after", "origin-between":
		if got.UID != before || got.Labels[asks.OriginLabel] != "replaced" || got.Status.State != string(apiv1.AskStateWithdrawn) {
			t.Fatalf("%s uid %s origin %q state %s", mode, got.UID, got.Labels[asks.OriginLabel], got.Status.State)
		}
	default:
		t.Fatalf("unknown mode %s", mode)
	}
}

// labelFence moves the Ask after its withdrawal status is stored, or between
// the label read and the label patch. The patch must not mark the result withdrawn.
type labelFence struct {
	base   client.Client
	mode   string
	moved  bool
	newUID types.UID
}

func (g *labelFence) afterStatus(ctx context.Context, obj client.Object) error {
	ask, ok := obj.(*coord.Ask)
	if !ok || ask.Status.State != string(apiv1.AskStateWithdrawn) || g.moved {
		return nil
	}
	if g.mode != "replace-after" && g.mode != "origin-after" {
		return nil
	}
	return g.move(ctx, ask)
}

func (g *labelFence) afterGet(ctx context.Context, obj client.Object) error {
	ask, ok := obj.(*coord.Ask)
	if !ok || ask.Status.State != string(apiv1.AskStateWithdrawn) || g.moved {
		return nil
	}
	if g.mode != "replace-between" && g.mode != "origin-between" {
		return nil
	}
	snapshot := ask.DeepCopy()
	if err := g.move(ctx, snapshot); err != nil {
		return err
	}
	snapshot.DeepCopyInto(ask)
	return nil
}

func (g *labelFence) move(ctx context.Context, old *coord.Ask) error {
	g.moved = true
	if g.mode == "origin-after" || g.mode == "origin-between" {
		var stored coord.Ask
		if err := g.base.Get(ctx, client.ObjectKeyFromObject(old), &stored); err != nil {
			return err
		}
		if stored.Labels == nil {
			stored.Labels = map[string]string{}
		}
		stored.Labels[asks.OriginLabel] = "replaced"
		return g.base.Update(ctx, &stored)
	}
	if err := g.base.Delete(ctx, old); err != nil {
		return err
	}
	next := old.DeepCopy()
	next.ResourceVersion = ""
	next.UID = old.UID + "-replaced"
	next.Status = coord.AskStatus{}
	if next.Labels == nil {
		next.Labels = map[string]string{}
	}
	next.Labels[asks.StateLabel] = string(apiv1.AskStateOpen)
	next.Labels[asks.OriginLabel] = grantAskOriginBroker
	if err := g.base.Create(ctx, next); err != nil {
		return err
	}
	next.Status.State = string(apiv1.AskStateOpen)
	if err := g.base.Status().Update(ctx, next); err != nil {
		return err
	}
	g.newUID = next.UID
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
