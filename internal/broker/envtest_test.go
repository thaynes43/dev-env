package broker

// The envtest cases (suite_test.go has the harness). Each starts its own broker,
// as its ServiceAccount, under the RBAC and admission guard of haynes-ops.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// checkMade asserts a kube grant's ServiceAccount and its RoleBindings, as the
// broker makes them.
func checkMade(t *testing.T, g *v1alpha1.AccessGrant, role string, namespaces ...string) {
	t.Helper()
	labels := map[string]string{
		v1alpha1.LabelGrant: g.Name, v1alpha1.LabelSession: g.Spec.Requester.Session, v1alpha1.LabelManagedBy: ManagedBy,
	}
	acct := sa(g.Name)
	if !exists(t, acct) {
		t.Fatalf("no ServiceAccount %s", g.Name)
	}
	if !mapsEqual(acct.Labels, labels) || acct.AutomountServiceAccountToken == nil || *acct.AutomountServiceAccountToken {
		t.Errorf("ServiceAccount labels %v, automount %v", acct.Labels, acct.AutomountServiceAccountToken)
	}
	subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: sessionNS, Name: g.Name}
	ref := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
	if len(namespaces) == 0 {
		b := crb(g.Name)
		if !exists(t, b) {
			t.Fatalf("no ClusterRoleBinding %s", g.Name)
		}
		if b.RoleRef != ref || !slices.Equal(b.Subjects, []rbacv1.Subject{subject}) || !mapsEqual(b.Labels, labels) {
			t.Errorf("ClusterRoleBinding %+v %+v %v", b.RoleRef, b.Subjects, b.Labels)
		}
		return
	}
	for _, ns := range namespaces {
		b := rb(ns, g.Name)
		if !exists(t, b) {
			t.Fatalf("no RoleBinding %s/%s", ns, g.Name)
		}
		if b.RoleRef != ref || !slices.Equal(b.Subjects, []rbacv1.Subject{subject}) || !mapsEqual(b.Labels, labels) {
			t.Errorf("RoleBinding %s: %+v %+v %v", ns, b.RoleRef, b.Subjects, b.Labels)
		}
	}
	if g.Status.ServiceAccount != g.Name || !controllerutil.ContainsFinalizer(g, Finalizer) {
		t.Errorf("status.serviceAccount %q, finalizers %v", g.Status.ServiceAccount, g.Finalizers)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// waitMade waits until the grant's ServiceAccount exists: an approval is
// written before what it grants is made.
func waitMade(t *testing.T, n string) *v1alpha1.AccessGrant {
	t.Helper()
	var g *v1alpha1.AccessGrant
	eventually(t, n+" made", func() (bool, string) {
		g = grant(t, n)
		return g.Status.ServiceAccount == n, g.Status.ServiceAccount
	})
	return g
}

func TestBrokerApprovesByPolicy(t *testing.T) {
	suite(t)
	r := startBroker(t, nil, nil)
	s, _ := newSession(t)
	ops, _ := newSession(t, profile(ProfileOps), repo("haynes-ops"))
	newPolicy(t, "a-frontend", nil, "frontend", "home-automation")
	newPolicy(t, "b-haynes-ops", func(p *v1alpha1.GrantPolicy) {
		p.Spec.Profiles, p.Spec.Repos = nil, []string{"haynes-ops"}
	}, "home-automation")

	inside := newGrant(t, s, role(v1alpha1.RoleWorkloads, "home-automation", "frontend"))
	outside := newGrant(t, s, role(v1alpha1.RoleWorkloads, "media"))
	bg := newGrant(t, s, breakglass)
	opsGrant := newGrant(t, ops)

	g := waitPhase(t, inside, v1alpha1.GrantActive)
	if g.Status.ApprovedBy != "policy/a-frontend" || g.Status.ApprovedTTL.Duration != time.Hour ||
		!g.Status.ExpiresAt.Equal(ptr.To(metav1.NewTime(g.Status.ApprovedAt.Add(time.Hour)))) {
		t.Errorf("status %+v", g.Status)
	}
	g = waitMade(t, inside)
	checkMade(t, g, v1alpha1.RoleWorkloads, "home-automation", "frontend")
	if r.notes.count(inside) != 0 {
		t.Error("Tom was told of a grant a policy approved")
	}
	// The decision is an Event on the grant, written under the broker's RBAC.
	eventually(t, "the decision's Event", func() (bool, string) {
		var evs eventsv1.EventList
		if err := admin.List(context.Background(), &evs, client.InNamespace(sessionNS)); err != nil {
			return false, err.Error()
		}
		for _, e := range evs.Items {
			if e.Regarding.Name == inside && e.Reason == string(v1alpha1.GrantActive) && strings.Contains(e.Note, "policy/a-frontend") {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d events", len(evs.Items))
	})

	// Outside the policy's namespaces, break-glass, and an ops session: Tom
	// decides, and is told once.
	for _, n := range []string{outside, bg, opsGrant} {
		g := waitPhase(t, n, v1alpha1.GrantPending)
		eventually(t, n+" notified", func() (bool, string) {
			g = grant(t, n)
			return g.Status.NotifiedAt != nil, ""
		})
		r.settle(t, n)
		if c := r.notes.count(n); c != 1 {
			t.Errorf("%s: told Tom %d times", n, c)
		}
		if g := grant(t, n); g.Status.Phase != v1alpha1.GrantPending || g.Status.ApprovedBy != "" || exists(t, sa(n)) {
			t.Errorf("%s: %+v", n, g.Status)
		}
	}

	// A new policy reaches the grants that wait.
	newPolicy(t, "c-media", nil, "media")
	g = waitPhase(t, outside, v1alpha1.GrantActive)
	if g.Status.ApprovedBy != "policy/c-media" {
		t.Errorf("approvedBy %q", g.Status.ApprovedBy)
	}
	waitMade(t, outside)
	r.settle(t, opsGrant)
	if g := grant(t, opsGrant); g.Status.Phase != v1alpha1.GrantPending {
		t.Errorf("the ops grant is %s", g.Status.Phase)
	}
}

func TestBrokerDecide(t *testing.T) {
	suite(t)
	r := startBroker(t, nil, nil)
	ctx := context.Background()
	s, _ := newSession(t)
	n := newGrant(t, s, role(v1alpha1.RoleWorkloads, "media"))
	waitPhase(t, n, v1alpha1.GrantPending)

	for _, d := range []Decision{
		{Approve: true, By: "authentik/tom", TTL: 2 * time.Hour}, // longer than asked
		{Approve: true, By: "authentik/tom", TTL: 5 * time.Minute},
		{Approve: true},
		{Approve: true, By: "policy/a-frontend"},
	} {
		if err := r.b.Decide(ctx, n, d); !errors.Is(err, ErrRefused) {
			t.Errorf("%+v: %v, want ErrRefused", d, err)
		}
	}
	if err := r.b.Decide(ctx, "grant-none", Decision{Approve: true, By: "authentik/tom"}); !errors.Is(err, ErrNotPending) {
		t.Errorf("a missing grant: %v", err)
	}

	if err := r.b.Decide(ctx, n, Decision{Approve: true, By: "authentik/tom", TTL: 20 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	g := waitPhase(t, n, v1alpha1.GrantActive)
	if g.Status.ApprovedBy != "authentik/tom" || g.Status.ApprovedTTL.Duration != 20*time.Minute ||
		g.Status.ExpiresAt.Sub(g.Status.ApprovedAt.Time) != 20*time.Minute {
		t.Errorf("status %+v", g.Status)
	}
	checkMade(t, waitMade(t, n), v1alpha1.RoleWorkloads, "media")
	if err := r.b.Decide(ctx, n, Decision{Approve: true, By: "authentik/tom"}); !errors.Is(err, ErrNotPending) {
		t.Errorf("a second decision: %v", err)
	}

	denied := newGrant(t, s, role(v1alpha1.RoleSecretsRead, "media"))
	waitPhase(t, denied, v1alpha1.GrantPending)
	if err := r.b.Decide(ctx, denied, Decision{By: "authentik/tom", Reason: "not today"}); err != nil {
		t.Fatal(err)
	}
	g = waitPhase(t, denied, v1alpha1.GrantDenied)
	if g.Status.DeniedBy != "authentik/tom" || g.Status.Message != "not today" || g.Status.EndedAt == nil || exists(t, sa(denied)) {
		t.Errorf("status %+v", g.Status)
	}

	// Break-glass: Tom only, bound cluster-wide.
	bg := newGrant(t, s, breakglass)
	waitPhase(t, bg, v1alpha1.GrantPending)
	if err := r.b.Decide(ctx, bg, Decision{Approve: true, By: "policy/anything"}); !errors.Is(err, ErrRefused) {
		t.Errorf("a policy approving break-glass: %v", err)
	}
	if err := r.b.Decide(ctx, bg, Decision{Approve: true, By: "authentik/tom"}); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, bg, v1alpha1.GrantActive)
	checkMade(t, waitMade(t, bg), v1alpha1.RoleBreakglass)
	if exists(t, rb("media", bg)) {
		t.Error("break-glass got a RoleBinding")
	}
	release(t, bg)
	waitPhase(t, bg, v1alpha1.GrantReleased)
	waitGone(t, crb(bg), sa(bg))
}

func TestBrokerExpiryTimeoutAndRetention(t *testing.T) {
	suite(t)
	r := startBroker(t, nil, nil)
	s, _ := newSession(t)
	newPolicy(t, "expiry-ha", nil, "home-automation")
	active := newGrant(t, s, ttl(time.Hour))
	waiting := newGrant(t, s, role(v1alpha1.RoleWorkloads, "media"))
	waitMade(t, active)
	waitPhase(t, waiting, v1alpha1.GrantPending)

	// An hour and a minute later, on the broker's clock.
	later := r.clock.Now().Add(61 * time.Minute)
	r.clock.SetTime(later)
	poke(t, active)
	poke(t, waiting)
	g := waitPhase(t, active, v1alpha1.GrantExpired)
	waitGone(t, rb("home-automation", active), sa(active))
	if g.Status.EndedAt == nil || !g.Status.EndedAt.Time.Equal(later.Truncate(time.Second)) || !controllerutil.ContainsFinalizer(g, Finalizer) {
		t.Errorf("status %+v, finalizers %v", g.Status, g.Finalizers)
	}
	g = waitPhase(t, waiting, v1alpha1.GrantDenied)
	if g.Status.DeniedBy != DeniedByTimeout {
		t.Errorf("deniedBy %q", g.Status.DeniedBy)
	}

	// Kept 89 days, gone after 90.
	r.clock.SetTime(later.Add(89 * 24 * time.Hour))
	r.settle(t, active)
	if !exists(t, &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: active}}) {
		t.Fatal("deleted before 90 days")
	}
	r.clock.SetTime(later.Add(Retention + time.Minute))
	poke(t, active)
	poke(t, waiting)
	waitGone(t,
		&v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: active}},
		&v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: waiting}})
}

func TestBrokerReleaseAndSessionEnd(t *testing.T) {
	suite(t)
	startBroker(t, nil, nil)
	ctx := context.Background()
	newPolicy(t, "release-ha", nil, "home-automation")

	// Released by the session.
	s1, _ := newSession(t)
	g1 := newGrant(t, s1)
	waitMade(t, g1)
	release(t, g1)
	if g := waitPhase(t, g1, v1alpha1.GrantReleased); g.Status.EndedAt == nil {
		t.Errorf("status %+v", g.Status)
	}
	waitGone(t, rb("home-automation", g1), sa(g1))

	// Released before an answer.
	g2 := newGrant(t, s1, role(v1alpha1.RoleWorkloads, "media"))
	waitPhase(t, g2, v1alpha1.GrantPending)
	release(t, g2)
	waitPhase(t, g2, v1alpha1.GrantReleased)

	// The session is deleted: its active grant is released and revoked, its
	// pending one denied.
	s2, _ := newSession(t)
	g3 := newGrant(t, s2)
	g4 := newGrant(t, s2, role(v1alpha1.RoleWorkloads, "media"))
	waitMade(t, g3)
	waitPhase(t, g4, v1alpha1.GrantPending)
	if err := admin.Delete(ctx, s2); err != nil {
		t.Fatal(err)
	}
	if g := waitPhase(t, g3, v1alpha1.GrantReleased); g.Status.Message != "the session ended" {
		t.Errorf("status %+v", g.Status)
	}
	waitGone(t, rb("home-automation", g3), sa(g3))
	if g := waitPhase(t, g4, v1alpha1.GrantDenied); g.Status.DeniedBy != DeniedByBroker || g.Status.Message != "the session ended" {
		t.Errorf("status %+v", g.Status)
	}

	// A session being reaped (it waits on its rescue finalizer) ends its grants
	// too.
	s3, _ := newSession(t, func(s *v1alpha1.AgentSession) { s.Finalizers = []string{"test/rescue"} })
	g5 := newGrant(t, s3)
	waitMade(t, g5)
	if err := admin.Delete(ctx, s3); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, g5, v1alpha1.GrantReleased)
	waitGone(t, rb("home-automation", g5), sa(g5))
	if err := admin.Get(ctx, client.ObjectKeyFromObject(s3), s3); err != nil {
		t.Fatal(err)
	}
	s3.Finalizers = nil
	if err := admin.Update(ctx, s3); err != nil {
		t.Fatal(err)
	}

	// A grant whose session never existed.
	ghost := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name("s-ghost")}, Spec: v1alpha1.AgentSessionSpec{Repo: "x", Profile: "full"}}
	g6 := newGrant(t, ghost)
	if g := waitPhase(t, g6, v1alpha1.GrantDenied); g.Status.Message != "the session ended" {
		t.Errorf("status %+v", g.Status)
	}

	// A deleted grant is revoked before it goes.
	s4, _ := newSession(t)
	g7 := newGrant(t, s4)
	waitMade(t, g7)
	if err := admin.Delete(ctx, grant(t, g7)); err != nil {
		t.Fatal(err)
	}
	waitGone(t, rb("home-automation", g7), sa(g7), &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: g7}})
}

func TestBrokerFailsWhatItCannotMake(t *testing.T) {
	suite(t)
	startBroker(t, nil, nil)
	ctx := context.Background()
	s, _ := newSession(t)

	// A namespace that does not exist.
	missing := newGrant(t, s, role(v1alpha1.RoleWorkloads, "no-such-namespace"))
	waitPhase(t, missing, v1alpha1.GrantPending)
	b := startedBroker(t)
	if err := b.Decide(ctx, missing, Decision{Approve: true, By: "authentik/tom"}); err != nil {
		t.Fatal(err)
	}
	g := waitPhase(t, missing, v1alpha1.GrantFailed)
	if !strings.Contains(g.Status.Message, "namespace does not exist") {
		t.Errorf("message %q", g.Status.Message)
	}
	waitGone(t, sa(missing))

	// A ServiceAccount of the grant's name that the broker did not make: the
	// grant fails and the ServiceAccount is left alone.
	taken := newGrant(t, s, role(v1alpha1.RoleWorkloads, "media"))
	waitPhase(t, taken, v1alpha1.GrantPending)
	foreign := sa(taken)
	if err := admin.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := b.Decide(ctx, taken, Decision{Approve: true, By: "authentik/tom"}); err != nil {
		t.Fatal(err)
	}
	g = waitPhase(t, taken, v1alpha1.GrantFailed)
	if !strings.Contains(g.Status.Message, "did not make it") || !exists(t, foreign) || exists(t, rb("media", taken)) {
		t.Errorf("message %q; the foreign ServiceAccount must stay and no binding be made", g.Status.Message)
	}

	// An egress grant: not built yet, so refused rather than approved with
	// nothing made.
	eg := newGrant(t, s, func(g *v1alpha1.AccessGrant) {
		g.Spec.Type, g.Spec.Kube = v1alpha1.GrantEgress, nil
		g.Spec.Egress = &v1alpha1.EgressGrant{CIDRs: []string{"192.168.40.21/32"}, Ports: tcp(443)}
	})
	if g := waitPhase(t, eg, v1alpha1.GrantDenied); g.Status.DeniedBy != DeniedByBroker || !strings.Contains(g.Status.Message, "not built yet") {
		t.Errorf("status %+v", g.Status)
	}
}

// startedBroker is a Broker for Decide alone, as a replica that is not the
// leader serves the approval page: it reads and writes through its own client.
func startedBroker(t *testing.T) *Broker {
	t.Helper()
	c, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return &Broker{Client: c, APIReader: c, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: realClock{}}
}

type realClock struct{}

func (realClock) Now() time.Time                  { return time.Now() }
func (realClock) Since(t time.Time) time.Duration { return time.Since(t) }

func TestBrokerTokens(t *testing.T) {
	suite(t)
	inst := &recorder{}
	startBroker(t, nil, inst)
	ctx := context.Background()
	s, pod := newSession(t)
	newPolicy(t, "tokens-ha", nil, "home-automation")
	n := newGrant(t, s)

	var first install
	eventually(t, "the grant installed", func() (bool, string) {
		ins, _ := inst.of(n)
		if len(ins) == 0 {
			return false, "no install"
		}
		first = ins[0]
		return grant(t, n).Status.InstalledPodUID == string(pod.UID), "status not written"
	})
	g := grant(t, n)
	if first.pod != pod.UID || first.grant != n || first.token == "" || g.Status.InstalledAt == nil ||
		first.expires.After(g.Status.ExpiresAt.Time) || g.Status.ExpiresAt.Sub(first.expires) > time.Minute {
		t.Errorf("install %+v, status %+v", first, g.Status)
	}

	// The token is the grant's identity, and holds the role where it was
	// granted, and nowhere else.
	who := review(t, first.token)
	if !who.Authenticated || who.User.Username != "system:serviceaccount:"+sessionNS+":"+n {
		t.Fatalf("review %+v", who)
	}
	agent := tokenClient(t, first.token)
	if err := agent.List(ctx, &corev1.ConfigMapList{}, client.InNamespace("home-automation")); err != nil {
		t.Errorf("list ConfigMaps where granted: %v", err)
	}
	if err := agent.List(ctx, &corev1.ConfigMapList{}, client.InNamespace("frontend")); !apierrors.IsForbidden(err) {
		t.Errorf("list ConfigMaps where not granted: %v", err)
	}

	// The API server will not bind a grant's token to the session's pod,
	// because the pod runs as another ServiceAccount (D-58).
	bc, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
		BoundObjectRef: &authenticationv1.BoundObjectReference{Kind: "Pod", APIVersion: "v1", Name: pod.Name, UID: pod.UID},
	}}
	if err := bc.SubResource("token").Create(ctx, sa(n), tr); !apierrors.IsBadRequest(err) || !strings.Contains(err.Error(), "different serviceaccount") {
		t.Errorf("a pod-bound token for the grant: %v", err)
	}

	// A new pod (a resume or a drain) gets the grant installed again.
	if err := admin.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	waitGone(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: pod.Name}})
	pod2 := runningPod(t, s)
	eventually(t, "the grant installed in the new pod", func() (bool, string) {
		ins, _ := inst.of(n)
		g := grant(t, n)
		return slices.ContainsFunc(ins, func(i install) bool { return i.pod == pod2.UID }) && g.Status.InstalledPodUID == string(pod2.UID),
			fmt.Sprintf("installs %+v, status %+v", ins, g.Status)
	})

	// Revoked: the ServiceAccount is gone and the token with it.
	release(t, n)
	waitPhase(t, n, v1alpha1.GrantReleased)
	waitGone(t, rb("home-automation", n), sa(n))
	if _, removes := inst.of(n); !slices.Equal(removes, []types.UID{pod2.UID}) {
		t.Errorf("removed from %v, want the new pod", removes)
	}
	eventually(t, "the API server to refuse the revoked token", func() (bool, string) {
		st := review(t, first.token)
		return !st.Authenticated, st.Error
	})
	if err := agent.List(ctx, &corev1.ConfigMapList{}, client.InNamespace("home-automation")); !apierrors.IsUnauthorized(err) {
		t.Errorf("the revoked token: %v", err)
	}
	ins, _ := inst.of(n)
	for _, i := range ins {
		if strings.Contains(logs.String(), i.token) {
			t.Error("a grant token is in the broker's log")
		}
	}
}

func TestBrokerRestartChangesNothing(t *testing.T) {
	suite(t)
	inst := &recorder{}
	r1 := startBroker(t, nil, inst)
	s, pod := newSession(t)
	newPolicy(t, "restart-ha", nil, "home-automation")
	n := newGrant(t, s)
	eventually(t, "the grant installed", func() (bool, string) {
		return grant(t, n).Status.InstalledPodUID == string(pod.UID), ""
	})
	before := grant(t, n)
	acct, binding := sa(n), rb("home-automation", n)
	if !exists(t, acct) || !exists(t, binding) {
		t.Fatal("the grant is not made")
	}
	ins, _ := inst.of(n)
	token := ins[0].token

	r1.stop(t)
	inst2 := &recorder{}
	r2 := startBroker(t, r1.clock, inst2)
	// A fresh broker reconciles every grant at start.
	eventually(t, "the new broker to reconcile the grant", func() (bool, string) {
		return r2.reconciles(n) > 0, ""
	})
	r2.settle(t, n)

	after := grant(t, n)
	acct2, binding2 := sa(n), rb("home-automation", n)
	if !exists(t, acct2) || !exists(t, binding2) {
		t.Fatal("the restart removed the grant's objects")
	}
	if acct2.UID != acct.UID || acct2.ResourceVersion != acct.ResourceVersion ||
		binding2.UID != binding.UID || binding2.ResourceVersion != binding.ResourceVersion {
		t.Errorf("the restart rewrote the grant's objects: ServiceAccount %s/%s -> %s/%s, RoleBinding %s/%s -> %s/%s",
			acct.UID, acct.ResourceVersion, acct2.UID, acct2.ResourceVersion,
			binding.UID, binding.ResourceVersion, binding2.UID, binding2.ResourceVersion)
	}
	if after.Status.Phase != v1alpha1.GrantActive || after.Status.InstalledPodUID != before.Status.InstalledPodUID ||
		!after.Status.InstalledAt.Equal(before.Status.InstalledAt) || !after.Status.ExpiresAt.Equal(before.Status.ExpiresAt) {
		t.Errorf("status %+v, was %+v", after.Status, before.Status)
	}
	if ins, _ := inst2.of(n); len(ins) != 0 {
		t.Errorf("the restart minted and installed again: %d", len(ins))
	}
	if st := review(t, token); !st.Authenticated {
		t.Errorf("the token stopped working across the restart: %s", st.Error)
	}
}

func TestBrokerRBAC(t *testing.T) {
	suite(t)
	ctx := context.Background()
	bc, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	binding := func(ns, n, role, subject string) client.Object {
		subj := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Namespace: sessionNS, Name: subject}}
		ref := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}
		if ns == "" {
			return &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: n}, RoleRef: ref, Subjects: subj}
		}
		return &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: n}, RoleRef: ref, Subjects: subj}
	}
	const escalation = "attempting to grant RBAC permissions not currently held"
	const guard = "dev-env-broker-guard:"

	// The broker binds the catalog, through its own client.
	for _, b := range []client.Object{
		binding("home-automation", "grant-rbac-ok-1", v1alpha1.RoleWorkloads, "grant-rbac-ok-1"),
		binding("frontend", "grant-rbac-ok-2", v1alpha1.RoleSecretsRead, "grant-rbac-ok-2"),
		binding("", "grant-rbac-ok-3", v1alpha1.RoleNodes, "grant-rbac-ok-3"),
		binding("", "grant-rbac-ok-4", v1alpha1.RoleBreakglass, "grant-rbac-ok-4"),
	} {
		if err := bc.Create(ctx, b); err != nil {
			t.Errorf("bind %s: %v", b.GetName(), err)
			continue
		}
		if err := bc.Delete(ctx, b); err != nil {
			t.Errorf("delete %s: %v", b.GetName(), err)
		}
	}

	// RBAC refuses a role outside the catalog, whatever the name: the broker
	// has no bind on it and does not hold its rules. envtest runs no
	// clusterrole-aggregation controller, so admin, edit and view have no rules
	// there and binding them is no escalation; the guard refuses those, and the
	// bind SARs below show RBAC would.
	for _, role := range []string{"cluster-admin", outsideRole} {
		for _, b := range []client.Object{
			binding("home-automation", "grant-rbac-no", role, "grant-rbac-no"),
			binding("", "grant-rbac-no", role, "grant-rbac-no"),
		} {
			if err := bc.Create(ctx, b); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), escalation) {
				t.Errorf("bind %s (%T): %v, want RBAC's escalation refusal", role, b, err)
			}
		}
	}
	for _, role := range []string{"admin", "edit", "view"} {
		for _, b := range []client.Object{
			binding("home-automation", "grant-rbac-no", role, "grant-rbac-no"),
			binding("", "grant-rbac-no", role, "grant-rbac-no"),
		} {
			if err := bc.Create(ctx, b); !apierrors.IsForbidden(err) {
				t.Errorf("bind %s (%T): %v, want Forbidden", role, b, err)
			}
		}
	}

	// The admission guard holds the broker to its own grant objects.
	for what, b := range map[string]client.Object{
		"another subject":                    binding("home-automation", "grant-rbac-g1", v1alpha1.RoleWorkloads, "dev-env-agent"),
		"a name that is not grant-<id>":      binding("home-automation", "rbac-g2", v1alpha1.RoleWorkloads, "rbac-g2"),
		"a dev-env namespace":                binding(toolsNS, "grant-rbac-g3", v1alpha1.RoleWorkloads, "grant-rbac-g3"),
		"a namespaced role cluster-wide":     binding("", "grant-rbac-g4", v1alpha1.RoleSecretsRead, "grant-rbac-g4"),
		"a cluster-wide role in a namespace": binding("home-automation", "grant-rbac-g5", v1alpha1.RoleNodes, "grant-rbac-g5"),
	} {
		if err := bc.Create(ctx, b); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), guard) {
			t.Errorf("%s: %v, want the broker guard's refusal", what, err)
		}
	}
	if err := bc.Create(ctx, sa("not-a-grant")); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), guard) {
		t.Errorf("a ServiceAccount not named grant-<id>: %v", err)
	}
	tr := &authenticationv1.TokenRequest{}
	if err := bc.SubResource("token").Create(ctx, sa("dev-env-workbench"), tr); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), guard) {
		t.Errorf("a token for the workbench: %v (token %t)", err, tr.Status.Token != "")
	}

	// What the API server's authorizer says, from the installed RBAC.
	rbac := func(verb, resource, ns, n string) authorizationv1.ResourceAttributes {
		return authorizationv1.ResourceAttributes{Verb: verb, Group: rbacv1.GroupName, Resource: resource, Namespace: ns, Name: n}
	}
	ccnp := func(verb string) authorizationv1.ResourceAttributes {
		return authorizationv1.ResourceAttributes{Verb: verb, Group: "cilium.io", Resource: "ciliumclusterwidenetworkpolicies", Name: "grant-x"}
	}
	for what, ra := range map[string]authorizationv1.ResourceAttributes{
		"create a RoleBinding in dev-agents":      rbac("create", "rolebindings", sessionNS, ""),
		"create a RoleBinding in frontend":        rbac("create", "rolebindings", "frontend", ""),
		"create a ClusterRoleBinding":             rbac("create", "clusterrolebindings", "", ""),
		"bind a catalog role":                     rbac("bind", "clusterroles", "frontend", v1alpha1.RoleWorkloads),
		"bind a catalog role cluster-wide":        rbac("bind", "clusterroles", "", v1alpha1.RoleNodes),
		"bind cluster-admin":                      rbac("bind", "clusterroles", "", "cluster-admin"),
		"escalate a ClusterRole":                  rbac("escalate", "clusterroles", "", ""),
		"create a CiliumClusterwideNetworkPolicy": ccnp("create"),
		"delete a CiliumClusterwideNetworkPolicy": ccnp("delete"),
	} {
		if allowed(t, operatorUser, ra) {
			t.Errorf("the operator may %s", what)
		}
	}
	for what, ra := range map[string]authorizationv1.ResourceAttributes{
		"bind cluster-admin":              rbac("bind", "clusterroles", "", "cluster-admin"),
		"bind admin":                      rbac("bind", "clusterroles", "frontend", "admin"),
		"bind edit":                       rbac("bind", "clusterroles", "frontend", "edit"),
		"bind view":                       rbac("bind", "clusterroles", "", "view"),
		"bind a role outside the catalog": rbac("bind", "clusterroles", "frontend", outsideRole),
		"escalate a ClusterRole":          rbac("escalate", "clusterroles", "", ""),
		"create a CiliumClusterwideNetworkPolicy": ccnp("create"),
		"delete a CiliumClusterwideNetworkPolicy": ccnp("delete"),
		"read a Secret": {Verb: "get", Resource: "secrets", Namespace: sessionNS},
	} {
		if allowed(t, brokerUser, ra) {
			t.Errorf("the broker may %s", what)
		}
	}
	if !allowed(t, brokerUser, rbac("bind", "clusterroles", "frontend", v1alpha1.RoleWorkloads)) ||
		!allowed(t, brokerUser, authorizationv1.ResourceAttributes{Verb: "update", Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants", Subresource: "status", Namespace: sessionNS}) {
		t.Error("the broker lacks a right it needs")
	}
	if allowed(t, operatorUser, authorizationv1.ResourceAttributes{Verb: "update", Group: v1alpha1.GroupVersion.Group, Resource: "accessgrants", Subresource: "status", Namespace: sessionNS}) {
		t.Error("the operator may write a grant's status, so it could approve one")
	}
}
