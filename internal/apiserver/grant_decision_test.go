package apiserver

// Delegate decision route tests (DESIGN-042 6.2): POST /v1/grants/{name}/decision.
// A delegate decision is not a human decision. Handler fixtures isolate reach
// and refusal cases; grant_decision_integration_test.go exercises the combined
// HTTP route, and grant_decision_envtest_test.go uses real TokenRequest/TokenReview.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	delegateSessionSA = sessionNS + "/dev-env-coordinator-lead"
	decisionRef       = "msg-1031-req-7"
)

// delegateFixture is a fixture whose coordinator host and coordinator-lead
// ServiceAccount are delegates, with a full session the host launched (its
// child, inside its reach) that requested a kube grant. Delegate sessions are
// launch-eligible under S4's real exported rule. It returns the fixture, the
// requester's token and the grant.
func delegateFixture(t *testing.T) (*fixture, string, apiv1.Grant) {
	t.Helper()
	f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if obj.GetUID() == "" {
			obj.SetUID(uuid.NewUUID())
		}
		return c.Create(ctx, obj, opts...)
	}})
	host := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "codex-host-a-0", Namespace: "dev-env-system", UID: "host-pod-uid",
		Annotations: map[string]string{CoordinatorHostAnnotation: "host-a"}},
		Spec:   corev1.PodSpec{ServiceAccountName: "codex-host-a", Containers: []corev1.Container{{Name: "host", Image: "synthetic"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := f.c.Create(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(coordinatorSA)
	id.PodName, id.PodUID = host.Name, string(host.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.CoordinatorEnabled = true
	f.srv.Policy.Coordinators = []CoordinatorHost{{ServiceAccount: coordinatorSA, PodName: host.Name, HostID: "host-a"}}
	f.srv.GrantDelegates = []string{coordinatorSA, delegateSessionSA}
	f.srv.Policy.NamedCoordinatorIdentities = true
	f.h = f.srv.Handler()
	tok := f.sessionPod("requester", "full", 0)
	f.reparent("requester", coordinatorSA)
	g := f.request(tok, apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}, TTL: "1h", Reason: "restart frontend"}, http.StatusCreated)
	if g.UID == "" || g.ScopeDigest == "" {
		t.Fatalf("the grant view lacks what a delegate echoes: %+v", g)
	}
	return f, tok, g
}

// reparent sets a session's parent, as its creator would have recorded it.
func (f *fixture) reparent(name, parent string) {
	f.t.Helper()
	s := f.session(name)
	s.Spec.Parent = parent
	if err := f.c.Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
}

// childGrant adds a full session whose parent is parent, at depth, and a kube
// grant it requested.
func (f *fixture) childGrant(name, parent string, depth int) apiv1.Grant {
	f.t.Helper()
	f.sessionPod(name, "full", depth)
	f.reparent(name, parent)
	return f.putGrant(name, nil)
}

func decisionFor(g apiv1.Grant, approve bool) apiv1.GrantDecisionRequest {
	return apiv1.GrantDecisionRequest{GrantUID: g.UID, Digest: g.ScopeDigest, Approve: approve, Ref: decisionRef, Reason: "fits the frontend runbook"}
}

// delegateSession adds a Running session whose pod runs as the coordinator-lead
// ServiceAccount, and returns the caller resolveSession makes of its token: what
// S4's classification will hand the route.
func (f *fixture) delegateSession(name, parent string, depth int) *caller {
	f.t.Helper()
	f.srv.Policy.NamedCoordinatorIdentities = true
	ctx := context.Background()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(), Labels: map[string]string{v1alpha1.LabelDepth: itoa(depth), templates.CoordinatorIdentityLabel: leadSA},
			Annotations: map[string]string{NamedCoordinatorLauncherAnnotation: string(LauncherHuman)},
			Finalizers:  []string{controller.Finalizer}},
		Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", Prompt: "p", Parent: parent},
	}
	if err := f.c.Create(ctx, s); err != nil {
		f.t.Fatal(err)
	}
	s.Status.Phase = v1alpha1.PhaseRunning
	if err := f.c.Status().Update(ctx, s); err != nil {
		f.t.Fatal(err)
	}
	_, account, _ := strings.Cut(delegateSessionSA, "/")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(),
			Labels:          map[string]string{v1alpha1.LabelProfile: "coordinator"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec:   corev1.PodSpec{ServiceAccountName: account, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if err := f.c.Create(ctx, pod); err != nil {
		f.t.Fatal(err)
	}
	id := saIdentity(delegateSessionSA)
	id.PodName, id.PodUID = name, string(pod.UID)
	c, err := f.srv.resolveSession(ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// decideAs calls the route's handler as c and returns the status and the
// error code, empty on success.
func (f *fixture) decideAs(c *caller, name string, req any) (int, string) {
	f.t.Helper()
	status, ae := f.decideErr(c, name, req)
	if ae == nil {
		return status, ""
	}
	return ae.status, ae.body.Code
}

// decideErr is decideAs with the refusal itself.
func (f *fixture) decideErr(c *caller, name string, req any) (int, *apiError) {
	f.t.Helper()
	raw, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, apiv1.GrantDecisionPath(name), bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", name)
	status, _, err := f.srv.decideGrant(context.Background(), httptest.NewRecorder(), r, c)
	var ae *apiError
	switch {
	case err == nil:
		return status, nil
	case errors.As(err, &ae):
		return ae.status, ae
	}
	f.t.Fatalf("decide: %v", err)
	return 0, nil
}

// putGrant stores a grant directly, as the API would have made it for the
// session named requester, edited first: for types and roles the request
// route would not build.
func (f *fixture) putGrant(requester string, edit func(*v1alpha1.AccessGrant)) apiv1.Grant {
	f.t.Helper()
	s := f.session(requester)
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "grant-1006-172226-" + string(uuid.NewUUID())[:4], Namespace: sessionNS,
			Labels: map[string]string{v1alpha1.LabelSession: requester}},
		Spec: v1alpha1.AccessGrantSpec{
			Requester: v1alpha1.GrantRequester{Session: requester, SessionUID: s.UID, Repo: s.Spec.Repo, Profile: "full", Agent: v1alpha1.AgentClaude, Parent: s.Spec.Parent},
			Type:      v1alpha1.GrantKube, Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}},
			TTL: metav1.Duration{Duration: time.Hour}, Reason: "synthetic",
		},
	}
	if edit != nil {
		edit(g)
	}
	if err := f.c.Create(context.Background(), g); err != nil {
		f.t.Fatal(err)
	}
	return f.srv.grantView(f.grant(g.Name))
}

func (f *fixture) decisionAnnotation(name string) (broker.MaintenanceDecision, bool) {
	f.t.Helper()
	raw := f.grant(name).Annotations[broker.MaintenanceDecisionAnnotation]
	var d broker.MaintenanceDecision
	return d, raw != "" && json.Unmarshal([]byte(raw), &d) == nil
}

// A configured coordinator host on the list decides over HTTP: 202 and the
// Grant, a set-once annotation naming delegate/<ns>/<sa> with its ref and pod,
// no status write, and a replay is a conflict. The production broker, with the
// same delegates and a delegated policy, then approves it as that delegate.
func TestGrantDecisionCoordinatorHostApproves(t *testing.T) {
	f, _, g := delegateFixture(t)
	w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tokCoordinator, decisionFor(g, true))
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := decode[apiv1.Grant](t, w); got.Name != g.Name || got.Phase != apiv1.GrantPending {
		t.Fatalf("answer %+v", got)
	}
	d, ok := f.decisionAnnotation(g.Name)
	if !ok || d.By != broker.DelegateRef(coordinatorSA) || d.Ref != decisionRef || d.Digest != g.ScopeDigest || d.GrantUID != g.UID ||
		d.Delegate == nil || d.Delegate.PodUID != "host-pod-uid" || d.Delegate.Session != "" || !d.Approve || d.TTL != time.Hour {
		t.Fatalf("annotation %+v", d)
	}
	if st := f.grant(g.Name).Status; st.Phase != "" || st.ApprovedBy != "" {
		t.Fatalf("the operator wrote a status: %+v", st)
	}
	wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tokCoordinator, decisionFor(g, true)), http.StatusConflict, apiv1.CodeConflict)

	p := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "kube-workloads"},
		Spec: v1alpha1.GrantPolicySpec{Description: "test", Type: v1alpha1.GrantKube, Delegated: &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}},
			Kube: &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: []string{"frontend"}}, MaxTTL: metav1.Duration{Duration: 3 * time.Hour}}}
	if err := f.c.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "requester"}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Labels[v1alpha1.LabelSession] = "requester"
	pod.Status.Phase = corev1.PodRunning
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	b := &broker.Broker{Client: f.c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: "dev-env-system", GrantDelegates: f.srv.GrantDelegates, Clock: clocktesting.NewFakeClock(f.now)}
	for range 3 {
		if _, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: sessionNS, Name: g.Name}}); err != nil {
			t.Fatal(err)
		}
	}
	if st := f.grant(g.Name).Status; st.Phase != v1alpha1.GrantActive || st.ApprovedBy != broker.DelegateRef(coordinatorSA) ||
		!strings.Contains(st.Message, broker.DelegateWording) || strings.Contains(st.ApprovedBy+st.Message, "human/") {
		t.Fatalf("broker did not apply the delegate decision: %+v", st)
	}
}

// A form 2 delegate, a Running session whose pod runs as a listed delegate
// account, decides for its child and its grandchild, and its session and form
// are recorded. Without S4's launch-eligibility check, it decides nothing.
func TestGrantDecisionDelegateSessionDecides(t *testing.T) {
	f, _, _ := delegateFixture(t)
	lead := f.delegateSession("coordinator-lead", humanSA, 0)
	cg := f.childGrant("lead-child", "coordinator-lead", 1)
	if status, code := f.decideAs(lead, cg.Name, decisionFor(cg, false)); status != http.StatusAccepted {
		t.Fatal(status, code)
	}
	d, ok := f.decisionAnnotation(cg.Name)
	if !ok || d.Approve || d.By != broker.DelegateRef(delegateSessionSA) || d.Delegate == nil || d.Delegate.Form != 2 || d.Delegate.Session != "coordinator-lead" ||
		d.Delegate.SessionUID != string(lead.session.UID) || d.Delegate.PodUID != string(lead.pod.UID) || d.Reason != "fits the frontend runbook" {
		t.Fatalf("annotation %+v", d)
	}
	gg := f.childGrant("lead-grandchild", "lead-child", 2)
	if status, code := f.decideAs(lead, gg.Name, decisionFor(gg, true)); status != http.StatusAccepted {
		t.Fatalf("a delegate may decide for its grandchild: %d %s", status, code)
	}
	// S4's exported rule refuses a changed admission record, even when the
	// caller was already classified before the change.
	lead.session.Annotations[NamedCoordinatorLauncherAnnotation] = ""
	f.mustUpdate(lead.session)
	g3 := f.childGrant("lead-child-3", "coordinator-lead", 1)
	if status, code := f.decideAs(lead, g3.Name, decisionFor(g3, true)); status != http.StatusForbidden || code != apiv1.CodeForbidden {
		t.Fatalf("an ineligible delegate session: %d %s", status, code)
	}
	// Turning off named identities refuses the real token route as well.
	f.srv.Policy.NamedCoordinatorIdentities = false
	id := saIdentity(delegateSessionSA)
	id.PodName, id.PodUID = lead.pod.Name, string(lead.pod.UID)
	f.auth["tok-delegate-session"] = id
	wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g3.Name), "tok-delegate-session", decisionFor(g3, true)), http.StatusForbidden, apiv1.CodeForbidden)
}

// Every caller that is not a listed delegate is refused.
func TestGrantDecisionRefusesCallersOffTheList(t *testing.T) {
	f, requester, g := delegateFixture(t)
	other := f.sessionPod("other-session", "full", 0)
	for name, tok := range map[string]string{"human": tokHuman, "client": tokClient, "stranger": tokStranger, "requester": requester, "a plain session": other} {
		t.Run(name, func(t *testing.T) {
			wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true)), http.StatusForbidden, apiv1.CodeForbidden)
		})
	}
	t.Run("coordinator host off the list", func(t *testing.T) {
		f.srv.GrantDelegates = []string{delegateSessionSA}
		f.h = f.srv.Handler()
		defer func() { f.srv.GrantDelegates = []string{coordinatorSA, delegateSessionSA}; f.h = f.srv.Handler() }()
		wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tokCoordinator, decisionFor(g, true)), http.StatusForbidden, apiv1.CodeForbidden)
	})
	if _, ok := f.decisionAnnotation(g.Name); ok {
		t.Fatal("a refused caller recorded a decision")
	}
}

// A delegate decides only inside its own tree (lead ruling on #786): a host
// for its own child sessions, a delegate session for its strict descendants.
// Its own grants, an ancestor's, a sibling's, a cousin's and an unrelated
// session's are refused, as are a requester on its account and its own pod.
func TestGrantDecisionReachIsTheDelegatesTree(t *testing.T) {
	f, _, g := delegateFixture(t)
	const self, outside, unverified = "its own session's grant", "only for its own descendants", "cannot be verified"
	forbiddenFor := func(t *testing.T, c *caller, g apiv1.Grant, why string) {
		t.Helper()
		status, ae := f.decideErr(c, g.Name, decisionFor(g, true))
		if status != http.StatusForbidden || ae == nil || ae.body.Code != apiv1.CodeForbidden || !strings.Contains(ae.body.Message, why) {
			t.Fatalf("got %d %v, want 403 for %q", status, ae, why)
		}
		if _, ok := f.decisionAnnotation(g.Name); ok {
			t.Fatal("recorded")
		}
	}
	allowedFor := func(t *testing.T, c *caller, g apiv1.Grant) {
		t.Helper()
		if status, ae := f.decideErr(c, g.Name, decisionFor(g, true)); status != http.StatusAccepted {
			t.Fatalf("inside the delegate's tree: %d %v", status, ae)
		}
	}
	// A delegate session, a lead that Tom launched, with a child and a
	// grandchild; Tom's other top-level session; and a launching session with
	// two children, one of them a lead.
	lead := f.delegateSession("tree-lead", humanSA, 0)
	child := f.childGrant("tree-child", "tree-lead", 1)
	grandchild := f.childGrant("tree-grandchild", "tree-child", 2)

	t.Run("the lead's child", func(t *testing.T) { allowedFor(t, lead, child) })
	t.Run("the lead's grandchild", func(t *testing.T) { allowedFor(t, lead, grandchild) })
	t.Run("the lead's own grant", func(t *testing.T) {
		forbiddenFor(t, lead, f.putGrant("tree-lead", nil), self)
	})
	t.Run("the lead's parent", func(t *testing.T) {
		f.sessionPod("tree-parent", "full", 0)
		under := f.delegateSession("lead-under-parent", "tree-parent", 1)
		forbiddenFor(t, under, f.putGrant("tree-parent", nil), outside)
	})
	t.Run("the lead's grandparent", func(t *testing.T) {
		f.sessionPod("tree-grandparent", "full", 0)
		f.childGrant("tree-mid", "tree-grandparent", 1)
		deep := f.delegateSession("lead-under-mid", "tree-mid", 2)
		forbiddenFor(t, deep, f.putGrant("tree-grandparent", nil), outside)
	})
	t.Run("the lead's sibling", func(t *testing.T) {
		f.sessionPod("tree-root", "full", 0)
		sibling := f.childGrant("tree-sibling", "tree-root", 1)
		siblingLead := f.delegateSession("tree-sibling-lead", "tree-root", 1)
		forbiddenFor(t, siblingLead, sibling, outside)
	})
	t.Run("the lead's cousin", func(t *testing.T) {
		f.sessionPod("cousin-root", "full", 0)
		f.childGrant("cousin-uncle", "cousin-root", 1)
		cousin := f.childGrant("cousin-a", "cousin-uncle", 2)
		f.childGrant("cousin-parent", "cousin-root", 1)
		cousinLead := f.delegateSession("cousin-lead", "cousin-parent", 2)
		forbiddenFor(t, cousinLead, cousin, outside)
	})
	t.Run("an unrelated session's grant", func(t *testing.T) {
		f.sessionPod("unrelated-root", "full", 0)
		forbiddenFor(t, lead, f.putGrant("unrelated-root", nil), outside)
	})
	t.Run("an unverifiable chain", func(t *testing.T) {
		orphan := f.childGrant("orphan", "gone-parent", 1)
		forbiddenFor(t, lead, orphan, unverified)
	})
	t.Run("a requester on the delegate's own account", func(t *testing.T) {
		f.delegateSession("same-account-a", humanSA, 0)
		other := f.delegateSession("same-account-b", humanSA, 0)
		forbiddenFor(t, other, f.putGrant("same-account-a", nil), "runs as its own delegate account")
	})
	// Form 1: a host decides for its own child sessions only.
	host, err := f.srv.resolveCaller(context.Background(), f.auth[tokCoordinator])
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a host's own child", func(t *testing.T) { allowedFor(t, host, g) })
	t.Run("a host for Tom's session", func(t *testing.T) {
		f.sessionPod("toms-session", "full", 0)
		forbiddenFor(t, host, f.putGrant("toms-session", nil), outside)
	})
	t.Run("a host for another host's child", func(t *testing.T) {
		forbiddenFor(t, host, f.childGrant("other-hosts-child", "dev-env-system/codex-host-b", 0), outside)
	})
	t.Run("a host for its child's child", func(t *testing.T) {
		f.sessionPod("host-child", "full", 0)
		f.reparent("host-child", coordinatorSA)
		forbiddenFor(t, host, f.childGrant("host-grandchild", "host-child", 1), outside)
	})
	t.Run("the requester's current pod", func(t *testing.T) {
		// A host pod that is the pod named after the requesting session.
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "host-x", Namespace: sessionNS, UID: uuid.NewUUID()},
			Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA}}
		if err := f.c.Create(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		forbiddenFor(t, f.hostPod("host-x", sessionNS, nil), f.putGrant("host-x", nil), self)
	})
	t.Run("a coordinator host pod the requester controls", func(t *testing.T) {
		f.sessionPod("parent-f", "full", 0)
		owner := f.session("parent-f")
		owner.Status.Phase = v1alpha1.PhaseRunning
		if err := f.c.Status().Update(context.Background(), owner); err != nil {
			t.Fatal(err)
		}
		forbiddenFor(t, f.hostPod("host-y", sessionNS, owner), f.putGrant("parent-f", nil), self)
	})
	t.Run("a coordinator host pod whose controller cannot be read", func(t *testing.T) {
		f.sessionPod("parent-h", "full", 0)
		// An owner in another namespace is never this pod's controller.
		forbiddenFor(t, f.hostPod("host-z", "dev-env-system", f.session("parent-h")), f.putGrant("parent-h", nil), unverified)
	})
}

// A delegate must be live and eligible when it decides, read from the API
// server after it was classified: a stale, replaced, terminating, suspended,
// draining, archived or reaped delegate is refused. Classification itself is
// unchanged: such a session still reads its grants on the ordinary routes.
func TestGrantDecisionRefusesIneligibleDelegates(t *testing.T) {
	ctx := context.Background()
	const ineligible = "not Running and eligible"
	const stale = "never a replaced or terminating one"
	sessionCases := map[string]struct {
		change func(*fixture, *caller)
		why    string
	}{
		"eligible": {func(*fixture, *caller) {}, ""},
		"Suspended": {func(f *fixture, c *caller) {
			c.session.Status.Phase = v1alpha1.PhaseSuspended
			f.mustStatus(c.session)
		}, ineligible},
		"Draining": {func(f *fixture, c *caller) {
			c.session.Status.Phase = v1alpha1.PhaseDraining
			f.mustStatus(c.session)
		}, ineligible},
		"asked to suspend": {func(f *fixture, c *caller) {
			c.session.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			f.mustUpdate(c.session)
		}, ineligible},
		"archived": {func(f *fixture, c *caller) {
			c.session.Status.ArchivedAt = &metav1.Time{Time: f.now}
			f.mustStatus(c.session)
		}, ineligible},
		"being reaped": {func(f *fixture, c *caller) {
			if err := f.c.Delete(ctx, c.session); err != nil { // the session carries the controller's finalizer
				f.t.Fatal(err)
			}
		}, ineligible},
		"pod replaced": {func(f *fixture, c *caller) {
			if err := f.c.Delete(ctx, c.pod); err != nil {
				f.t.Fatal(err)
			}
			rolled := c.pod.DeepCopy()
			rolled.ResourceVersion, rolled.UID = "", uuid.NewUUID()
			if err := f.c.Create(ctx, rolled); err != nil {
				f.t.Fatal(err)
			}
		}, stale},
		"pod terminating": {func(f *fixture, c *caller) {
			c.pod.Finalizers = []string{"test/hold"}
			f.mustUpdate(c.pod)
			if err := f.c.Delete(ctx, c.pod); err != nil {
				f.t.Fatal(err)
			}
		}, stale},
		"pod not Running": {func(f *fixture, c *caller) {
			c.pod.Status.Phase = corev1.PodPending
			f.mustStatus(c.pod)
		}, stale},
		"pod re-owned": {func(f *fixture, c *caller) {
			other := f.delegateSession("other-owner", humanSA, 0)
			c.pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(other.session, v1alpha1.GroupVersion.WithKind("AgentSession"))}
			f.mustUpdate(c.pod)
		}, "controlled by its own session"},
	}
	for name, tc := range sessionCases {
		t.Run("session "+name, func(t *testing.T) {
			f, _, _ := delegateFixture(t)
			c := f.delegateSession("eligible-lead", humanSA, 0)
			g := f.childGrant("eligible-child", c.session.Name, 1)
			tc.change(f, c)
			status, ae := f.decideErr(c, g.Name, decisionFor(g, true))
			if tc.why == "" {
				if status != http.StatusAccepted {
					t.Fatalf("an eligible delegate: %d %v", status, ae)
				}
				return
			}
			if status != http.StatusForbidden || ae == nil || !strings.Contains(ae.body.Message, tc.why) {
				t.Fatalf("got %d %v, want 403 for %q", status, ae, tc.why)
			}
			if _, ok := f.decisionAnnotation(g.Name); ok {
				t.Fatal("recorded")
			}
		})
	}
	hostCases := map[string]struct {
		change func(*fixture, *corev1.Pod)
		why    string
	}{
		"pod replaced": {func(f *fixture, p *corev1.Pod) {
			if err := f.c.Delete(ctx, p); err != nil {
				f.t.Fatal(err)
			}
			rolled := p.DeepCopy()
			rolled.ResourceVersion, rolled.UID = "", "host-pod-uid-2"
			if err := f.c.Create(ctx, rolled); err != nil {
				f.t.Fatal(err)
			}
		}, stale},
		"host annotation changed": {func(f *fixture, p *corev1.Pod) {
			p.Annotations[CoordinatorHostAnnotation] = "host-b"
			f.mustUpdate(p)
		}, "host binding changed"},
		"not Running": {func(f *fixture, p *corev1.Pod) {
			p.Status.Phase = corev1.PodFailed
			f.mustStatus(p)
		}, stale},
	}
	for name, tc := range hostCases {
		t.Run("host "+name, func(t *testing.T) {
			f, _, g := delegateFixture(t)
			c, err := f.srv.resolveCaller(ctx, f.auth[tokCoordinator])
			if err != nil {
				t.Fatal(err)
			}
			var p corev1.Pod
			if err := f.c.Get(ctx, types.NamespacedName{Namespace: "dev-env-system", Name: "codex-host-a-0"}, &p); err != nil {
				t.Fatal(err)
			}
			tc.change(f, &p)
			status, ae := f.decideErr(c, g.Name, decisionFor(g, true))
			if status != http.StatusForbidden || ae == nil || !strings.Contains(ae.body.Message, tc.why) {
				t.Fatalf("got %d %v, want 403 for %q", status, ae, tc.why)
			}
		})
	}
	t.Run("classification unchanged on ordinary routes", func(t *testing.T) {
		f, _, g := delegateFixture(t)
		c := f.delegateSession("suspended-lead", humanSA, 0)
		c.session.Status.Phase = v1alpha1.PhaseSuspended
		f.mustStatus(c.session)
		r := httptest.NewRequest(http.MethodGet, apiv1.GrantPath(g.Name), nil)
		r.SetPathValue("name", g.Name)
		if status, _, err := f.srv.getGrant(ctx, httptest.NewRecorder(), r, c); err != nil || status != http.StatusOK {
			t.Fatalf("a suspended session lost its ordinary read: %d %v", status, err)
		}
	})
}

// Every replica serves the route and the slot is set once: of two concurrent
// decisions through two Server instances on one API server, exactly one is
// recorded and the other is a conflict. A replay is a conflict too.
func TestGrantDecisionConcurrentDecisionsRecordOne(t *testing.T) {
	f, _, g := delegateFixture(t)
	host, err := f.srv.resolveCaller(context.Background(), f.auth[tokCoordinator])
	if err != nil {
		t.Fatal(err)
	}
	other := host // The same delegate races through two operator replicas.
	replica := func(c client.Client) *Server {
		return &Server{Client: c, Live: f.c, Auth: f.auth, Policy: f.srv.Policy, GrantDelegates: f.srv.GrantDelegates,
			Log: f.srv.Log, Now: f.srv.Now}
	}
	b := replica(f.c)
	var second struct {
		status int
		err    error
	}
	raced := false
	// Replica A has read and checked the grant; replica B decides first.
	a := replica(interceptor.NewClient(f.c, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
		if !raced {
			raced = true
			raw, _ := json.Marshal(decisionFor(g, false))
			r := httptest.NewRequest(http.MethodPost, apiv1.GrantDecisionPath(g.Name), bytes.NewReader(raw))
			r.Header.Set("Content-Type", "application/json")
			r.SetPathValue("name", g.Name)
			second.status, _, second.err = b.decideGrant(ctx, httptest.NewRecorder(), r, other)
		}
		return c.Patch(ctx, obj, patch, opts...)
	}}))
	raw, _ := json.Marshal(decisionFor(g, true))
	r := httptest.NewRequest(http.MethodPost, apiv1.GrantDecisionPath(g.Name), bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("name", g.Name)
	_, _, firstErr := a.decideGrant(context.Background(), httptest.NewRecorder(), r, host)
	if second.err != nil || second.status != http.StatusAccepted {
		t.Fatalf("the first replica to write: %d %v", second.status, second.err)
	}
	var ae *apiError
	if !errors.As(firstErr, &ae) || ae.status != http.StatusConflict || !strings.Contains(ae.body.Message, "decided by another request") {
		t.Fatalf("the losing replica: %v", firstErr)
	}
	d, ok := f.decisionAnnotation(g.Name)
	if !ok || d.Approve || d.By != broker.DelegateRef(coordinatorSA) {
		t.Fatalf("not exactly the winner's decision: %+v", d)
	}
	if status, code := f.decideAs(other, g.Name, decisionFor(g, false)); status != http.StatusConflict || code != apiv1.CodeConflict {
		t.Fatalf("a replay: %d %s", status, code)
	}
}

func (f *fixture) mustUpdate(obj client.Object) {
	f.t.Helper()
	if err := f.c.Update(context.Background(), obj); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) mustStatus(obj client.Object) {
	f.t.Helper()
	if err := f.c.Status().Update(context.Background(), obj); err != nil {
		f.t.Fatal(err)
	}
}

// hostPod adds a Running coordinator host pod, its configured binding and its
// delegate listing, controlled by owner when one is given, and returns the
// caller resolveCaller makes of its token.
func (f *fixture) hostPod(name, namespace string, owner *v1alpha1.AgentSession) *caller {
	f.t.Helper()
	sa := "codex-" + name
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: uuid.NewUUID(),
		Annotations: map[string]string{CoordinatorHostAnnotation: name}},
		Spec:   corev1.PodSpec{ServiceAccountName: sa, Containers: []corev1.Container{{Name: "host", Image: "synthetic"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(owner, v1alpha1.GroupVersion.WithKind("AgentSession"))}
	}
	if err := f.c.Create(context.Background(), pod); err != nil {
		f.t.Fatal(err)
	}
	ref := namespace + "/" + sa
	f.srv.Policy.Coordinators = append(f.srv.Policy.Coordinators, CoordinatorHost{ServiceAccount: ref, PodName: name, HostID: name})
	f.srv.GrantDelegates = append(f.srv.GrantDelegates, ref)
	id := saIdentity(ref)
	id.PodName, id.PodUID = name, string(pod.UID)
	c, err := f.srv.resolveCaller(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

// A coordinator or worker the maintenance envelope names never decides it.
func TestGrantDecisionRefusesMaintenanceParticipants(t *testing.T) {
	f, parent, _, req := maintenanceAPI(t)
	f.srv.GrantDelegates = []string{delegateSessionSA}
	f.h = f.srv.Handler()
	got := f.request(parent, req, http.StatusCreated)
	// The worker's pod moved onto the delegate account stands in for S4.
	ws := f.session("maintenance-worker")
	var wp corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(ws), &wp); err != nil {
		t.Fatal(err)
	}
	wp.Spec.ServiceAccountName = "dev-env-coordinator-lead"
	if err := f.c.Update(context.Background(), &wp); err != nil {
		t.Fatal(err)
	}
	ws.Status.Phase = v1alpha1.PhaseRunning
	if err := f.c.Status().Update(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(delegateSessionSA)
	id.PodName, id.PodUID = wp.Name, string(wp.UID)
	c, err := f.srv.resolveSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	g := f.srv.grantView(f.grant(got.Name))
	status, ae := f.decideErr(c, g.Name, decisionFor(g, true))
	if status != http.StatusForbidden || ae == nil || !strings.Contains(ae.body.Message, "named in the maintenance envelope") {
		t.Fatalf("a named worker decided: %d %v", status, ae)
	}
	// Another delegate decides it, through ScopeDigest, never the human digest.
	lead := f.delegateSession("maintenance-lead", humanSA, 0)
	f.reparent("maintenance-parent", lead.session.Name)
	human := decisionFor(g, true)
	human.Digest = g.MaintenanceDigest
	if status, _ := f.decideAs(lead, g.Name, human); status != http.StatusConflict {
		t.Fatalf("the human route's digest: %d", status)
	}
	if status, code := f.decideAs(lead, g.Name, decisionFor(g, true)); status != http.StatusAccepted {
		t.Fatalf("a delegate outside the envelope: %d %s", status, code)
	}
}

// Stale, replayed, mismatched, released, decided, ended or timed-out requests
// are conflicts; bad TTLs, refs and reasons are invalid; credential and
// break-glass grants and the refused roles are forbidden.
func TestGrantDecisionRefusesTheRequest(t *testing.T) {
	type expect struct {
		status int
		code   string
	}
	conflict := expect{http.StatusConflict, apiv1.CodeConflict}
	bad := expect{http.StatusUnprocessableEntity, apiv1.CodeInvalid}
	refused := expect{http.StatusForbidden, apiv1.CodeForbidden}
	for name, tc := range map[string]struct {
		grant func(*v1alpha1.AccessGrant)
		after func(*fixture, string)
		req   func(*apiv1.GrantDecisionRequest)
		want  expect
	}{
		"another grant UID": {req: func(r *apiv1.GrantDecisionRequest) { r.GrantUID = "earlier-uid" }, want: conflict},
		"a digest mismatch": {req: func(r *apiv1.GrantDecisionRequest) { r.Digest = "stale" }, want: conflict},
		"no digest":         {req: func(r *apiv1.GrantDecisionRequest) { r.Digest = "" }, want: conflict},
		"released":          {grant: func(g *v1alpha1.AccessGrant) { g.Spec.Release = true }, want: conflict},
		"Active":            {after: func(f *fixture, n string) { f.setGrantStatus(n, v1alpha1.GrantActive, nil) }, want: conflict},
		"Denied":            {after: func(f *fixture, n string) { f.setGrantStatus(n, v1alpha1.GrantDenied, nil) }, want: conflict},
		"past 30 minutes":   {after: func(f *fixture, _ string) { f.now = f.now.Add(31 * time.Minute) }, want: conflict},
		"already decided": {grant: func(g *v1alpha1.AccessGrant) {
			g.Annotations = map[string]string{broker.MaintenanceDecisionAnnotation: `{"by":"x"}`}
		}, want: conflict},
		"no ref":                {req: func(r *apiv1.GrantDecisionRequest) { r.Ref = "" }, want: bad},
		"a ref with a space":    {req: func(r *apiv1.GrantDecisionRequest) { r.Ref = "msg 1" }, want: bad},
		"a long reason":         {req: func(r *apiv1.GrantDecisionRequest) { r.Reason = strings.Repeat("x", maxDecisionReason+1) }, want: bad},
		"a control character":   {req: func(r *apiv1.GrantDecisionRequest) { r.Reason = "ok\nnot" }, want: bad},
		"a bidi override":       {req: func(r *apiv1.GrantDecisionRequest) { r.Reason = "ok \u202enot" }, want: bad},
		"a TTL under 10m":       {req: func(r *apiv1.GrantDecisionRequest) { r.TTL = "5m" }, want: bad},
		"a longer TTL":          {req: func(r *apiv1.GrantDecisionRequest) { r.TTL = "2h" }, want: bad},
		"a TTL that is not one": {req: func(r *apiv1.GrantDecisionRequest) { r.TTL = "soon" }, want: bad},
		"credential": {grant: func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube, g.Spec.Credential = v1alpha1.GrantCredential, nil, &v1alpha1.CredentialGrant{Name: v1alpha1.CredentialHWSSH}
		}, want: refused},
		"breakglass": {grant: func(g *v1alpha1.AccessGrant) {
			g.Spec.Type, g.Spec.Kube = v1alpha1.GrantBreakglass, &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
		}, want: refused},
		"secrets-read": {grant: func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleSecretsRead }, want: refused},
		"storage":      {grant: func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleStorage }, want: refused},
		"the breakglass role on kube": {grant: func(g *v1alpha1.AccessGrant) {
			g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass}
		}, want: refused},
		"no requester UID": {grant: func(g *v1alpha1.AccessGrant) { g.Spec.Requester.SessionUID = "" }, want: refused},
	} {
		t.Run(name, func(t *testing.T) {
			f, _, _ := delegateFixture(t)
			g := f.putGrant("requester", tc.grant)
			if tc.after != nil {
				tc.after(f, g.Name)
			}
			req := decisionFor(g, true)
			if tc.req != nil {
				tc.req(&req)
			}
			wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tokCoordinator, req), tc.want.status, tc.want.code)
			if d, ok := f.decisionAnnotation(g.Name); ok && strings.HasPrefix(d.By, broker.DelegateApprover) {
				t.Fatal("a refused request recorded a decision")
			}
		})
	}
	f, _, g := delegateFixture(t)
	wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tokCoordinator, map[string]any{"grantUID": g.UID, "digest": g.ScopeDigest, "ref": decisionRef, "approvedBy": "user"}), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath("grant-1006-172226-none"), tokCoordinator, decisionFor(g, true)), http.StatusNotFound, apiv1.CodeNotFound)
}

// A listed coordinator host reads the grant it decides; an unlisted one does
// not, none releases a grant, and without delegates coordinators read none, as
// before. A maintenance decision needs the maintenance API.
func TestGrantDecisionCoordinatorReadsAndMaintenanceGate(t *testing.T) {
	f, _, g := delegateFixture(t)
	if w := f.do(http.MethodGet, apiv1.GrantPath(g.Name), tokCoordinator, nil); w.Code != http.StatusOK || decode[apiv1.Grant](t, w).ScopeDigest != g.ScopeDigest {
		t.Fatalf("a listed delegate could not review the grant: %d %s", w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodDelete, apiv1.GrantPath(g.Name), tokCoordinator, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if f.grant(g.Name).Spec.Release {
		t.Fatal("a coordinator released a grant")
	}
	wantError(t, f.do(http.MethodGet, apiv1.GrantsPath, tokCoordinator, nil), http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.GrantDelegates = []string{delegateSessionSA}
	f.h = f.srv.Handler()
	wantError(t, f.do(http.MethodGet, apiv1.GrantPath(g.Name), tokCoordinator, nil), http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.GrantDelegates = nil
	f.h = f.srv.Handler()
	wantError(t, f.do(http.MethodGet, apiv1.GrantPath(g.Name), tokCoordinator, nil), http.StatusForbidden, apiv1.CodeForbidden)

	// Each '<' encodes as six bytes: with a long session name a record would
	// pass the broker's bound, be accepted and never apply, so it is refused.
	f.srv.GrantDelegates = []string{coordinatorSA, delegateSessionSA}
	long := f.delegateSession(strings.Repeat("l", 63), humanSA, 0)
	g = f.childGrant("long-lead-child", long.session.Name, 1)
	big := decisionFor(g, true)
	big.Ref, big.Reason = strings.Repeat("r", 128), strings.Repeat("<", maxDecisionReason)
	if status, code := f.decideAs(long, g.Name, big); status != http.StatusUnprocessableEntity || code != apiv1.CodeInvalid {
		t.Fatalf("an oversized record: %d %s", status, code)
	}
	if _, ok := f.decisionAnnotation(g.Name); ok {
		t.Fatal("an oversized record was written")
	}

	m, parent, _, req := maintenanceAPI(t)
	m.srv.GrantDelegates = []string{delegateSessionSA}
	got := m.request(parent, req, http.StatusCreated)
	m.srv.MaintenanceEnabled = false
	lead := m.delegateSession("maintenance-lead", humanSA, 0)
	view := m.srv.grantView(m.grant(got.Name))
	if status, code := m.decideAs(lead, got.Name, decisionFor(view, true)); status != http.StatusForbidden || code != apiv1.CodeForbidden {
		t.Fatalf("a maintenance decision with the maintenance API off: %d %s", status, code)
	}
}

// Default off: with no delegates the decision route does not exist (404 for
// every caller), the grant view and the human maintenance route are exactly as
// before, and turning delegates on adds only the UID to a non-maintenance view.
func TestGrantDecisionDefaultOffIsANoOp(t *testing.T) {
	f, parent, _, req := maintenanceAPI(t)
	if f.srv.GrantDelegates != nil {
		t.Fatal("delegates on by default")
	}
	got := f.request(parent, req, http.StatusCreated)
	kube := f.request(parent, apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}, Reason: "x"}, http.StatusCreated)
	for _, tok := range []string{tokHuman, tokClient, parent} {
		if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(kube.Name), tok, decisionFor(kube, true)); w.Code != http.StatusNotFound {
			t.Fatal(tok, w.Code, w.Body.String())
		}
	}
	if kube.UID != "" {
		t.Fatalf("the default kube view gained a UID: %+v", kube)
	}
	off, maintOff := f.srv.grantView(f.grant(kube.Name)), f.srv.grantView(f.grant(got.Name))
	f.srv.GrantDelegates = []string{}
	if empty := f.srv.grantView(f.grant(kube.Name)); !reflect.DeepEqual(off, empty) {
		t.Fatalf("an empty delegate list changed the view:\n%+v\n%+v", off, empty)
	}
	f.srv.GrantDelegates = []string{delegateSessionSA}
	if maintOn := f.srv.grantView(f.grant(got.Name)); !reflect.DeepEqual(maintOff, maintOn) {
		t.Fatalf("delegates changed a maintenance view:\n%+v\n%+v", maintOff, maintOn)
	}
	on := f.srv.grantView(f.grant(kube.Name))
	if on.UID == "" {
		t.Fatal("delegates on: no UID to echo")
	}
	on.UID = ""
	if !reflect.DeepEqual(off, on) {
		t.Fatalf("delegates on changed more than the UID:\n%+v\n%+v", off, on)
	}

	// The human route writes the same annotation with delegates on or off.
	human := func(delegates []string) string {
		f, parent, _, req := maintenanceAPI(t)
		f.srv.GrantDelegates = delegates
		f.h = f.srv.Handler()
		g := f.request(parent, req, http.StatusCreated)
		w := f.do(http.MethodPost, apiv1.GrantPath(g.Name)+"/maintenance-decision", tokHuman, apiv1.MaintenanceDecisionRequest{GrantUID: g.UID, Digest: g.MaintenanceDigest, Approve: true})
		if w.Code != http.StatusAccepted {
			t.Fatal(w.Code, w.Body.String())
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(f.grant(g.Name).Annotations[broker.MaintenanceDecisionAnnotation]), &d); err != nil {
			t.Fatal(err)
		}
		delete(d, "grantUID")
		delete(d, "digest")
		raw, _ := json.Marshal(d)
		return string(raw)
	}
	if a, b := human(nil), human([]string{delegateSessionSA}); a != b || strings.Contains(a, "delegate") || strings.Contains(a, "ref") {
		t.Fatalf("the human route changed: %s vs %s", a, b)
	}
}

// ParseGrantDelegates refuses every identity the API serves in another class.
func TestParseGrantDelegatesRefusesOtherClasses(t *testing.T) {
	p := Policy{Human: humanSA, Clients: []string{clientSA}, SessionNamespace: sessionNS, SessionServiceAccount: controller.ServiceAccountName, WorkbenchServiceAccount: "dev-env-workbench"}
	for _, ref := range []string{humanSA, clientSA, sessionNS + "/" + controller.ServiceAccountName, sessionNS + "/dev-env-workbench", portalReaderRef} {
		if _, err := ParseGrantDelegates(ref, p); err == nil {
			t.Errorf("%s accepted as a delegate", ref)
		}
	}
	if got, err := ParseGrantDelegates(coordinatorSA+","+delegateSessionSA, p); err != nil || len(got) != 2 {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := ParseGrantDelegates("", p); got != nil || err != nil {
		t.Fatalf("%q %v", got, err)
	}
}

func TestGrantDecisionMaintenanceExtension(t *testing.T) {
	f, parent, _, req := maintenanceAPI(t)
	lead := f.delegateSession("extension-lead", humanSA, 0)
	f.reparent("maintenance-parent", lead.session.Name)
	f.srv.GrantDelegates = []string{delegateSessionSA}
	f.h = f.srv.Handler()
	req.Maintenance.ExtensionOf = "grant-earlier"
	g := f.request(parent, req, http.StatusCreated)
	id := saIdentity(delegateSessionSA)
	id.PodName, id.PodUID = lead.pod.Name, string(lead.pod.UID)
	f.auth["tok-extension-lead"] = id
	w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), "tok-extension-lead", decisionFor(g, true))
	if w.Code != http.StatusAccepted || decode[apiv1.Grant](t, w).Phase != apiv1.GrantPending {
		t.Fatalf("fresh extension decision: %d %s", w.Code, w.Body.String())
	}
}
