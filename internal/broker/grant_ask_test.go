package broker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/asks"
	"github.com/thaynes43/dev-env/internal/grantattest"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

const (
	grantAskSessionUID = "11111111-1111-4111-8111-111111111111"
	grantAskGrantUID   = "22222222-2222-4222-8222-222222222222"
)

func TestGrantAskFlagOffDeniesAtThirtyMinutesWithoutAnAsk(t *testing.T) {
	f := newGrantAskFixture(t, false)
	f.until(func(g *v1alpha1.AccessGrant) bool {
		return g.Status.Phase == v1alpha1.GrantPending && g.Status.NotifiedAt != nil
	})
	if n := f.askCount(); n != 0 {
		t.Fatalf("flag off opened %d asks", n)
	}
	if f.note.n != 1 {
		t.Fatalf("notifier calls = %d, want 1", f.note.n)
	}
	f.clk.Step(PendingTimeout)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantDenied || g.Status.DeniedBy != DeniedByTimeout || g.Status.Message != "no answer in 30 minutes" {
		t.Fatalf("after 30 minutes: phase %s deniedBy %s message %q", g.Status.Phase, g.Status.DeniedBy, g.Status.Message)
	}
	if n := f.askCount(); n != 0 {
		t.Fatalf("flag off opened %d asks at the deny", n)
	}
	if f.note.n != 1 {
		t.Fatalf("notifier calls after the deny = %d, want 1", f.note.n)
	}
}

func TestGrantAskFlagOnHoldsPastThirtyMinutesAndOpensOneAsk(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.until(func(g *v1alpha1.AccessGrant) bool {
		return g.Status.Phase == v1alpha1.GrantPending && f.askCount() == 1
	})
	f.assertOneGrantAsk()
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if n := f.askCount(); n != 1 {
		t.Fatalf("second reconcile opened %d asks", n)
	}
	f.clk.Step(PendingTimeout)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	g := f.grant()
	if g.Status.Phase != v1alpha1.GrantPending || g.Status.DeniedBy != "" || g.Status.ApprovedBy != "" {
		t.Fatalf("flag on after 30 minutes: %+v", g.Status)
	}
	if n := f.askCount(); n != 1 {
		t.Fatalf("after 30 minutes: %d asks", n)
	}
}

func TestGrantAskMatchingPolicyApprovesWithoutAnAsk(t *testing.T) {
	f := newGrantAskFixture(t, true)
	p := kubePolicy("frontend-workloads", []string{v1alpha1.RoleWorkloads}, "frontend")
	if err := f.base.Create(f.ctx, &p); err != nil {
		t.Fatal(err)
	}
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantActive })
	g := f.grant()
	if g.Status.ApprovedBy != "policy/frontend-workloads" {
		t.Fatalf("approvedBy %s", g.Status.ApprovedBy)
	}
	if n := f.askCount(); n != 0 {
		t.Fatalf("a matching policy opened %d asks", n)
	}
}

func TestGrantAskReleaseReleases(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.until(func(g *v1alpha1.AccessGrant) bool {
		return g.Status.Phase == v1alpha1.GrantPending && f.askCount() == 1
	})
	g := f.grant()
	g.Spec.Release = true
	if err := f.base.Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantReleased })
	ask := f.onlyAsk()
	if ask.Status.State != string(apiv1.AskStateWithdrawn) {
		t.Fatalf("released grant left the ask %s", ask.Status.State)
	}
}

func TestGrantAskMissingSessionDenies(t *testing.T) {
	f := newGrantAskFixture(t, true)
	if err := f.base.Delete(f.ctx, f.session); err != nil {
		t.Fatal(err)
	}
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	g := f.grant()
	if g.Status.DeniedBy != DeniedByBroker || g.Status.Message != "the session ended" {
		t.Fatalf("missing session: deniedBy %s message %q", g.Status.DeniedBy, g.Status.Message)
	}
	if n := f.askCount(); n != 0 {
		t.Fatalf("a missing session opened %d asks", n)
	}
}

func TestGrantAskEndedSessionLeavesTheAskOpen(t *testing.T) {
	f := newGrantAskFixture(t, true)
	f.until(func(g *v1alpha1.AccessGrant) bool {
		return g.Status.Phase == v1alpha1.GrantPending && f.askCount() == 1
	})
	// The fake client refuses to store a deletion timestamp. The wrapper
	// reports the session as deleting, which is what Reconcile checks.
	f.meta.sessionDeleting = true
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantDenied })
	g := f.grant()
	if g.Status.Message != "the session ended" {
		t.Fatalf("ended session message %q", g.Status.Message)
	}
	ask := f.onlyAsk()
	if ask.Status.State != string(apiv1.AskStateOpen) {
		t.Fatalf("ended session withdrew the ask: %s", ask.Status.State)
	}
}

func TestGrantAskStoreLimitOrUnavailableStaysPending(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"limit", &asks.LimitError{Scope: asks.LimitOpenGrant}},
		{"unavailable", fmt.Errorf("%w: ask count unconfirmed", asks.ErrUnavailable)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGrantAskFixture(t, true)
			stub := &grantAskStub{err: tc.err}
			f.b.grantAsks = stub
			f.until(func(g *v1alpha1.AccessGrant) bool {
				return g.Status.Phase == v1alpha1.GrantPending && stub.calls >= 1
			})
			calls := stub.calls
			f.clk.Step(PendingTimeout)
			if _, err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			g := f.grant()
			if g.Status.Phase != v1alpha1.GrantPending || g.Status.DeniedBy != "" {
				t.Fatalf("store error denied the grant: %+v", g.Status)
			}
			if stub.calls <= calls {
				t.Fatalf("store calls = %d, want a retry after %d", stub.calls, calls)
			}
			if n := f.askCount(); n != 0 {
				t.Fatalf("store error left %d asks", n)
			}
		})
	}
}

func TestGrantAskUnfitUIDStaysPending(t *testing.T) {
	f := newGrantAskFixture(t, true)
	g := f.grant()
	g.UID = "bad.uid"
	if err := f.base.Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.until(func(g *v1alpha1.AccessGrant) bool { return g.Status.Phase == v1alpha1.GrantPending })
	f.clk.Step(PendingTimeout)
	if _, err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	got := f.grant()
	if got.Status.Phase != v1alpha1.GrantPending || got.Status.DeniedBy != "" {
		t.Fatalf("unfit UID: %+v", got.Status)
	}
	if n := f.askCount(); n != 0 {
		t.Fatalf("unfit UID opened %d asks", n)
	}
}

func TestGrantAskUnshowableReasonUsesTheFixedQuestion(t *testing.T) {
	f := newGrantAskFixture(t, true)
	g := f.grant()
	g.Spec.Reason = "needs\x00access"
	if err := f.base.Update(f.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.until(func(g *v1alpha1.AccessGrant) bool { return f.askCount() == 1 })
	ask := f.onlyAsk()
	if ask.Spec.Question != grantAskQuestionFallback {
		t.Fatalf("question %q", ask.Spec.Question)
	}
}

func TestGrantAskWorkPackage(t *testing.T) {
	t.Run("one issue", func(t *testing.T) {
		f := newGrantAskFixture(t, true, issueClaim("claim-a", "issue:525"), issueClaim("claim-b", "issue:525"))
		f.until(func(g *v1alpha1.AccessGrant) bool { return f.askCount() == 1 })
		if got := f.onlyAsk().Spec.Context.WorkPackage; got != 525 {
			t.Fatalf("workPackage %d", got)
		}
	})
	t.Run("two issues", func(t *testing.T) {
		f := newGrantAskFixture(t, true, issueClaim("claim-a", "issue:525"), issueClaim("claim-b", "issue:516"))
		f.until(func(g *v1alpha1.AccessGrant) bool { return f.askCount() == 1 })
		if got := f.onlyAsk().Spec.Context.WorkPackage; got != 0 {
			t.Fatalf("workPackage %d, want none", got)
		}
	})
	t.Run("list fails", func(t *testing.T) {
		f := newGrantAskFixture(t, true)
		f.meta.failTaskClaims = true
		f.until(func(g *v1alpha1.AccessGrant) bool { return f.askCount() == 1 })
		if got := f.onlyAsk().Spec.Context.WorkPackage; got != 0 {
			t.Fatalf("workPackage %d, want none", got)
		}
	})
}

type countingNotifier struct{ n int }

func (n *countingNotifier) NotifyPending(context.Context, *v1alpha1.AccessGrant) error {
	n.n++
	return nil
}

type grantAskStub struct {
	err   error
	calls int
}

func (s *grantAskStub) Create(context.Context, asks.Requester, apiv1.AskCreateRequest, time.Time) (apiv1.Ask, bool, error) {
	s.calls++
	return apiv1.Ask{}, false, s.err
}

// askMetadataClient turns the fake client's Ask list into the metadata list
// asks.KubeStore counts with. The fake client has no partial-metadata list.
type askMetadataClient struct {
	client.Client
	failTaskClaims  bool
	sessionDeleting bool
}

func (m askMetadataClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := m.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if m.sessionDeleting {
		if sess, ok := obj.(*v1alpha1.AgentSession); ok && sess.DeletionTimestamp.IsZero() {
			ts := metav1.NewTime(time.Date(2026, 11, 10, 8, 1, 0, 0, time.UTC))
			sess.DeletionTimestamp = &ts
		}
	}
	return nil
}

func (m askMetadataClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	page, ok := list.(*metav1.PartialObjectMetadataList)
	if ok {
		kind := page.GroupVersionKind().Kind
		if kind != "" && kind != "AskList" {
			return m.Client.List(ctx, list, opts...)
		}
		var asks coord.AskList
		if err := m.Client.List(ctx, &asks, opts...); err != nil {
			return err
		}
		page.Items = make([]metav1.PartialObjectMetadata, len(asks.Items))
		for i := range asks.Items {
			page.Items[i].Name = asks.Items[i].Name
			page.Items[i].Namespace = asks.Items[i].Namespace
			page.Items[i].Labels = asks.Items[i].Labels
		}
		return nil
	}
	if _, ok := list.(*coord.TaskClaimList); ok && m.failTaskClaims {
		return apierrors.NewServiceUnavailable("task claims unavailable")
	}
	return m.Client.List(ctx, list, opts...)
}

type grantAskFixture struct {
	t       *testing.T
	ctx     context.Context
	clk     *clocktesting.FakeClock
	base    client.Client
	meta    *askMetadataClient
	b       *Broker
	g       *v1alpha1.AccessGrant
	session *v1alpha1.AgentSession
	note    *countingNotifier
}

func newGrantAskFixture(t *testing.T, grantAsks bool, extra ...client.Object) *grantAskFixture {
	t.Helper()
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	clk := clocktesting.NewFakeClock(now)
	g := kubeGrant(v1alpha1.RoleWorkloads, "frontend")
	g.UID = grantAskGrantUID
	g.CreationTimestamp = metav1.NewTime(now)
	g.Spec.Requester.SessionUID = grantAskSessionUID
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{
		Namespace: sessionNS, Name: g.Spec.Requester.Session, UID: grantAskSessionUID,
	}}
	sc := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(sc), v1alpha1.AddToScheme(sc), coord.AddToScheme(sc)); err != nil {
		t.Fatal(err)
	}
	objs := []client.Object{g, s}
	objs = append(objs, extra...)
	base := fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}, &coord.Ask{}).WithObjects(objs...).Build()
	meta := &askMetadataClient{Client: base}
	note := &countingNotifier{}
	f := &grantAskFixture{
		t: t, ctx: context.Background(), clk: clk, base: base, meta: meta, g: g, session: s, note: note,
		b: &Broker{
			Client: meta, APIReader: meta, SessionNamespace: sessionNS, PolicyNamespace: systemNS,
			Clock: clk, EnableGrantAsks: grantAsks, Notifier: note,
		},
	}
	return f
}

func (f *grantAskFixture) reconcile() (ctrl.Result, error) {
	f.t.Helper()
	return f.b.Reconcile(f.ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: sessionNS, Name: f.g.Name}})
}

func (f *grantAskFixture) until(pred func(*v1alpha1.AccessGrant) bool) {
	f.t.Helper()
	for range 8 {
		if _, err := f.reconcile(); err != nil {
			f.t.Fatal(err)
		}
		if pred(f.grant()) {
			return
		}
	}
	f.t.Fatalf("grant did not reach the wanted state: %+v asks=%d", f.grant().Status, f.askCount())
}

func (f *grantAskFixture) grant() *v1alpha1.AccessGrant {
	f.t.Helper()
	var g v1alpha1.AccessGrant
	if err := f.base.Get(f.ctx, types.NamespacedName{Namespace: sessionNS, Name: f.g.Name}, &g); err != nil {
		f.t.Fatal(err)
	}
	return &g
}

func (f *grantAskFixture) sessionObject() *v1alpha1.AgentSession {
	f.t.Helper()
	var s v1alpha1.AgentSession
	if err := f.base.Get(f.ctx, types.NamespacedName{Namespace: sessionNS, Name: f.session.Name}, &s); err != nil {
		f.t.Fatal(err)
	}
	return &s
}

func (f *grantAskFixture) asks() []coord.Ask {
	f.t.Helper()
	var list coord.AskList
	if err := f.base.List(f.ctx, &list, client.InNamespace(asks.Namespace)); err != nil {
		f.t.Fatal(err)
	}
	return list.Items
}

func (f *grantAskFixture) askCount() int { return len(f.asks()) }

func (f *grantAskFixture) onlyAsk() *coord.Ask {
	f.t.Helper()
	items := f.asks()
	if len(items) != 1 {
		f.t.Fatalf("asks = %d", len(items))
	}
	return &items[0]
}

func (f *grantAskFixture) assertOneGrantAsk() {
	f.t.Helper()
	ask := f.onlyAsk()
	g := f.grant()
	if ask.Labels[asks.OriginLabel] != grantAskOriginBroker {
		f.t.Fatalf("origin %q", ask.Labels[asks.OriginLabel])
	}
	if ask.Spec.Type != string(apiv1.AskTypeGrant) || ask.Spec.Scope != nil || !ask.Spec.ExpiresAt.IsZero() {
		f.t.Fatalf("ask shape type %s scope %v expires %v", ask.Spec.Type, ask.Spec.Scope, ask.Spec.ExpiresAt)
	}
	id, ok := grantAskRequestID(g.UID)
	if !ok || ask.Spec.RequestID != id {
		f.t.Fatalf("requestId %s", ask.Spec.RequestID)
	}
	if ask.Spec.Context.SessionUID != string(g.Spec.Requester.SessionUID) || ask.Spec.Context.SessionName != g.Spec.Requester.Session {
		f.t.Fatalf("context %+v", ask.Spec.Context)
	}
	if ask.Spec.Grant == nil || ask.Spec.Grant.UID != string(g.UID) || ask.Spec.Grant.ScopeDigest != grantattest.ScopeDigest(g) {
		f.t.Fatalf("grant binding %+v", ask.Spec.Grant)
	}
	if ask.Spec.Grant.Type != string(g.Spec.Type) || ask.Spec.Grant.Target != g.Spec.Kube.Role {
		f.t.Fatalf("type %s target %s", ask.Spec.Grant.Type, ask.Spec.Grant.Target)
	}
	if got, err := time.ParseDuration(ask.Spec.Grant.TTL); err != nil || got != g.Spec.TTL.Duration {
		f.t.Fatalf("ttl %s: %v", ask.Spec.Grant.TTL, err)
	}
	if ask.Spec.Grant.Detail != "frontend" || ask.Spec.Question != g.Spec.Reason {
		f.t.Fatalf("detail %q question %q", ask.Spec.Grant.Detail, ask.Spec.Question)
	}
	if ask.Spec.Context.WorkPackage != 0 {
		f.t.Fatalf("workPackage %d", ask.Spec.Context.WorkPackage)
	}
}

func issueClaim(name, taskKey string) *coord.TaskClaim {
	return &coord.TaskClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sessionNS, Name: name,
			Labels: map[string]string{taskclaim.SourceLabel: taskclaim.SessionLabel(coord.SessionIdentity{
				Namespace: sessionNS, Name: "s1", UID: grantAskSessionUID,
			})},
		},
		Spec: coord.TaskClaimSpec{Identity: coord.TaskIdentity{TaskKey: taskKey}},
	}
}
