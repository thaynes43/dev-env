package apiserver

// Delegate decision route tests (DESIGN-042 6.2): POST /v1/grants/{name}/decision.
// A delegate decision is not a human decision. Form 1 (a configured coordinator
// host) runs end to end over HTTP; form 2 (a session pod running as a delegate
// ServiceAccount) is called on the handler with the caller resolveSession builds,
// because resolveCaller classifies such a pod as a session only once S4 lands.

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
)

const (
	delegateSessionSA = sessionNS + "/dev-env-coordinator-lead"
	decisionRef       = "msg-1031-req-7"
)

// delegateFixture is a fixture whose coordinator host and coordinator-lead
// ServiceAccount are delegates, with a top-level full session that requested a
// kube grant. It returns the fixture, the requester's token and the grant.
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
		Spec: corev1.PodSpec{ServiceAccountName: "codex-host-a", Containers: []corev1.Container{{Name: "host", Image: "synthetic"}}}}
	if err := f.c.Create(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(coordinatorSA)
	id.PodName, id.PodUID = host.Name, string(host.UID)
	f.auth[tokCoordinator] = id
	f.srv.Policy.CoordinatorEnabled = true
	f.srv.Policy.Coordinators = []CoordinatorHost{{ServiceAccount: coordinatorSA, PodName: host.Name, HostID: "host-a"}}
	f.srv.GrantDelegates = []string{coordinatorSA, delegateSessionSA}
	f.h = f.srv.Handler()
	tok := f.sessionPod("requester", "full", 0)
	g := f.request(tok, apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}, TTL: "1h", Reason: "restart frontend"}, http.StatusCreated)
	if g.UID == "" || g.ScopeDigest == "" {
		t.Fatalf("the grant view lacks what a delegate echoes: %+v", g)
	}
	return f, tok, g
}

func decisionFor(g apiv1.Grant, approve bool) apiv1.GrantDecisionRequest {
	return apiv1.GrantDecisionRequest{GrantUID: g.UID, Digest: g.ScopeDigest, Approve: approve, Ref: decisionRef, Reason: "fits the frontend runbook"}
}

// delegateSession adds a Running session whose pod runs as the coordinator-lead
// ServiceAccount, and returns the caller resolveSession makes of its token: what
// S4's classification will hand the route.
func (f *fixture) delegateSession(name, parent string, depth int) *caller {
	f.t.Helper()
	ctx := context.Background()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(), Labels: map[string]string{v1alpha1.LabelDepth: itoa(depth)}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: "claude", Mode: "remote", Model: "claude-opus-5-5", Prompt: "p", Parent: parent},
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
		Spec: corev1.PodSpec{ServiceAccountName: account, Containers: []corev1.Container{{Name: "agent", Image: "x"}}},
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

// A form 2 delegate, a Running top-level session Tom launched whose pod runs
// as a listed delegate account, decides for another session, its own child
// included, and its session is recorded.
func TestGrantDecisionDelegateSessionDecides(t *testing.T) {
	f, _, g := delegateFixture(t)
	lead := f.delegateSession("coordinator-lead", humanSA, 0)
	if status, code := f.decideAs(lead, g.Name, decisionFor(g, false)); status != http.StatusAccepted {
		t.Fatal(status, code)
	}
	d, ok := f.decisionAnnotation(g.Name)
	if !ok || d.Approve || d.By != broker.DelegateRef(delegateSessionSA) || d.Delegate == nil || d.Delegate.Session != "coordinator-lead" ||
		d.Delegate.SessionUID != string(lead.session.UID) || d.Delegate.PodUID != string(lead.pod.UID) || d.Reason != "fits the frontend runbook" {
		t.Fatalf("annotation %+v", d)
	}
	f.sessionPod("lead-child", "full", 1)
	child := f.session("lead-child")
	child.Spec.Parent = "coordinator-lead"
	if err := f.c.Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	cg := f.putGrant("lead-child", nil)
	if status, code := f.decideAs(lead, cg.Name, decisionFor(cg, true)); status != http.StatusAccepted {
		t.Fatalf("a delegate may decide for its own child: %d %s", status, code)
	}
	// Until S4, resolveCaller does not classify a pod running as the delegate
	// account as a session: form 2 is inert over HTTP.
	tok := "tok-delegate-session"
	id := saIdentity(delegateSessionSA)
	id.PodName, id.PodUID = lead.pod.Name, string(lead.pod.UID)
	f.auth[tok] = id
	wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true)), http.StatusForbidden, apiv1.CodeForbidden)
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
	t.Run("delegate session not Running", func(t *testing.T) {
		c := f.delegateSession("stopped-lead", humanSA, 0)
		c.session.Status.Phase = v1alpha1.PhaseSuspended
		if status, code := f.decideAs(c, g.Name, decisionFor(g, true)); status != http.StatusForbidden || code != apiv1.CodeForbidden {
			t.Fatal(status, code)
		}
	})
	if _, ok := f.decisionAnnotation(g.Name); ok {
		t.Fatal("a refused caller recorded a decision")
	}
}

// The requester, its descendants, the envelope's participants and a delegate
// session Tom's callers did not launch never decide (DESIGN-042 6.2, c4638e6).
func TestGrantDecisionRefusesSelfAndAncestry(t *testing.T) {
	f, _, _ := delegateFixture(t)
	const self, ancestor, unverified, launched = "its own session's grant", "its parent or another ancestor", "cannot be verified", "Tom's human or a client caller launched"
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
	t.Run("the requester itself", func(t *testing.T) {
		lead := f.delegateSession("self-lead", humanSA, 0)
		forbiddenFor(t, lead, f.putGrant("self-lead", nil), self)
	})
	t.Run("the requester's current pod", func(t *testing.T) {
		f.delegateSession("parent-g", humanSA, 0)
		other := f.delegateSession("pod-sharer", humanSA, 0)
		var rp corev1.Pod
		if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "parent-g"}, &rp); err != nil {
			t.Fatal(err)
		}
		other.pod = other.pod.DeepCopy()
		other.pod.UID = rp.UID
		other.identity.PodUID = string(rp.UID)
		forbiddenFor(t, other, f.putGrant("parent-g", nil), self)
	})
	t.Run("a child of the requester", func(t *testing.T) {
		f.delegateSession("parent-a", humanSA, 0)
		child := f.delegateSession("child-of-a", "parent-a", 1)
		forbiddenFor(t, child, f.putGrant("parent-a", nil), ancestor)
	})
	t.Run("a grandchild of the requester", func(t *testing.T) {
		f.delegateSession("parent-b", humanSA, 0)
		f.delegateSession("child-of-b", "parent-b", 1)
		grandchild := f.delegateSession("grandchild-of-b", "child-of-b", 2)
		forbiddenFor(t, grandchild, f.putGrant("parent-b", nil), ancestor)
	})
	t.Run("an ancestry that cannot be verified", func(t *testing.T) {
		f.delegateSession("parent-c", humanSA, 0)
		orphan := f.delegateSession("orphan-of-c", "gone-parent", 1)
		forbiddenFor(t, orphan, f.putGrant("parent-c", nil), unverified)
	})
	t.Run("a sibling launched by a session", func(t *testing.T) {
		f.delegateSession("parent-d", humanSA, 0)
		f.delegateSession("sibling-d1", "parent-d", 1)
		sibling := f.delegateSession("sibling-d2", "parent-d", 1)
		forbiddenFor(t, sibling, f.putGrant("sibling-d1", nil), launched)
	})
	t.Run("a session the portal launched", func(t *testing.T) {
		f.delegateSession("parent-e", humanSA, 0)
		portal := f.delegateSession("portal-lead", "portal/tom", 1)
		forbiddenFor(t, portal, f.putGrant("parent-e", nil), launched)
	})
	t.Run("a coordinator host pod the requester controls", func(t *testing.T) {
		f.delegateSession("parent-f", humanSA, 0)
		c, err := f.srv.resolveCaller(context.Background(), f.auth[tokCoordinator])
		if err != nil {
			t.Fatal(err)
		}
		// A host in the session namespace whose pod the requester controls.
		owner := f.session("parent-f")
		c.pod.Namespace = sessionNS
		c.pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(owner, v1alpha1.GroupVersion.WithKind("AgentSession"))}
		forbiddenFor(t, c, f.putGrant("parent-f", nil), self)
	})
	t.Run("a coordinator host pod whose controller cannot be read", func(t *testing.T) {
		f.delegateSession("parent-h", humanSA, 0)
		c, err := f.srv.resolveCaller(context.Background(), f.auth[tokCoordinator])
		if err != nil {
			t.Fatal(err)
		}
		c.pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(f.session("parent-h"), v1alpha1.GroupVersion.WithKind("AgentSession"))}
		forbiddenFor(t, c, f.putGrant("parent-h", nil), unverified)
	})
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
