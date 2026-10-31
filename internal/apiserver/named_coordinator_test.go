package apiserver

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The named coordinator identity of DESIGN-042 6.4 (S4): a session pod running as
// dev-env-coordinator-<name>. Every name below is synthetic.
const leadSA = "dev-env-coordinator-lead"

// namedCoordinatorPod adds a session and its pod, as the reconciler builds it on
// a profile that names account, and returns a token bound to that pod.
func namedCoordinatorPod(t *testing.T, f *fixture, name, account string, depth int) string {
	t.Helper()
	f.sessionPod(name, "coordinator", depth)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = account
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	tok := "tok-named-" + name
	id := saIdentity(sessionNS + "/" + account)
	id.PodName, id.PodUID = name, string(pod.UID)
	f.auth[tok] = id
	return tok
}

// A pod running as a named coordinator identity, controlled by its session, is
// that session: the same kind, parent, depth, profile, session and pod an
// ordinary session pod resolves to. It is not a coordinator, not a client and not
// the workbench. With the policy off, its token is refused.
func TestNamedCoordinatorSessionPodIsTheSession(t *testing.T) {
	f := newFixture(t)
	f.srv.Policy.NamedCoordinatorIdentities = true
	const name = "haynes-ops-1031-140000"
	tok := namedCoordinatorPod(t, f, name, leadSA, 0)
	id := f.auth[tok]

	c, err := f.srv.resolveCaller(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	sess := f.session(name)
	if c.kind != kindSession || c.parent != name || c.depth != 1 || c.profile != "coordinator" ||
		c.session == nil || c.session.UID != sess.UID || c.pod == nil || string(c.pod.UID) != id.PodUID ||
		c.identity.ServiceAccount != leadSA || c.String() != "session/"+name {
		t.Fatalf("resolved %+v", c)
	}
	if f.srv.isWorkbenchSession(c) {
		t.Fatal("a named coordinator session counted as the workbench")
	}
	if !f.srv.sessionPodAccount(leadSA, c) {
		t.Fatal("the coordination fence refuses the session's own account")
	}
	// The fence accepts only the account the token carries, and only with the
	// policy on.
	for _, other := range []string{"dev-env-coordinator-other", controller.ServiceAccountName, templates.WorkbenchServiceAccount} {
		if f.srv.sessionPodAccount(other, c) {
			t.Fatalf("the fence accepted a %s pod for a %s token", other, leadSA)
		}
	}
	f.srv.Policy.NamedCoordinatorIdentities = false
	if f.srv.sessionPodAccount(leadSA, c) {
		t.Fatal("the fence accepted the account with the policy off")
	}
	f.srv.Policy.NamedCoordinatorIdentities = true

	// The heartbeat route, the first route a session pod calls.
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, status(name)); w.Code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
	// A heartbeat for another session is still refused.
	other := "haynes-ops-1031-140001"
	f.sessionPod(other, "full", 0)
	wantError(t, f.do(http.MethodPost, protocol.HeartbeatPath(other), tok, status(other)), http.StatusForbidden, apiv1.CodeForbidden)

	// Off (the zero Policy): the account is no caller at all.
	f.srv.Policy.NamedCoordinatorIdentities = false
	if c, err := f.srv.resolveCaller(context.Background(), id); err == nil {
		t.Fatalf("policy off resolved %+v", c)
	}
	wantError(t, f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, status(name)), http.StatusForbidden, apiv1.CodeForbidden)
}

// A named coordinator identity's token speaks only for a session that controls
// the pod it is bound to, and that pod must run as the token's own account.
// There is no client fallback.
func TestNamedCoordinatorTokenWithoutItsSessionIsRefused(t *testing.T) {
	for _, kind := range []string{
		"unbound", "no-pod-uid", "pod-gone", "earlier-pod", "no-controller", "foreign-controller",
		"session-gone", "earlier-session", "ordinary-session-pod", "other-coordinator-pod",
	} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.srv.Policy.NamedCoordinatorIdentities = true
			const name = "haynes-ops-1031-140100"
			tok := namedCoordinatorPod(t, f, name, leadSA, 0)
			id := f.auth[tok]
			ctx := context.Background()
			var pod corev1.Pod
			if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unbound":
				id.PodName, id.PodUID = "", ""
			case "no-pod-uid":
				id.PodUID = ""
			case "pod-gone":
				if err := f.c.Delete(ctx, &pod); err != nil {
					t.Fatal(err)
				}
			case "earlier-pod":
				id.PodUID = string(uuid.NewUUID())
			case "no-controller", "foreign-controller":
				// A pod in the session namespace that runs as the account but that
				// no AgentSession controls.
				stray := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "stray", Namespace: sessionNS, UID: uuid.NewUUID()},
					Spec: corev1.PodSpec{ServiceAccountName: leadSA, Containers: []corev1.Container{{Name: "x", Image: "x"}}}}
				if kind == "foreign-controller" {
					isController := true
					stray.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name, UID: f.session(name).UID, Controller: &isController}}
				}
				if err := f.c.Create(ctx, stray); err != nil {
					t.Fatal(err)
				}
				id.PodName, id.PodUID = stray.Name, string(stray.UID)
			case "session-gone":
				sess := f.session(name)
				sess.Finalizers = nil
				if err := f.c.Update(ctx, sess); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(ctx, sess); err != nil {
					t.Fatal(err)
				}
			case "earlier-session":
				pod.OwnerReferences[0].UID = uuid.NewUUID()
				if err := f.c.Update(ctx, &pod); err != nil {
					t.Fatal(err)
				}
			case "ordinary-session-pod":
				// The token claims an ordinary session's pod, which runs as dev-env-agent.
				f.sessionPod("haynes-ops-1031-140101", "full", 0)
				var plain corev1.Pod
				if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: "haynes-ops-1031-140101"}, &plain); err != nil {
					t.Fatal(err)
				}
				id.PodName, id.PodUID = plain.Name, string(plain.UID)
			case "other-coordinator-pod":
				// Another named identity's session pod is not this account's.
				namedCoordinatorPod(t, f, "haynes-ops-1031-140102", "dev-env-coordinator-other", 0)
				var o corev1.Pod
				if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: "haynes-ops-1031-140102"}, &o); err != nil {
					t.Fatal(err)
				}
				id.PodName, id.PodUID = o.Name, string(o.UID)
			}
			c, err := f.srv.resolveCaller(ctx, id)
			if err == nil {
				t.Fatalf("resolved %+v", c)
			}
			var apiErr *apiError
			if !errors.As(err, &apiErr) || apiErr.status != http.StatusForbidden {
				t.Fatalf("want a 403, got %v", err)
			}
			// These two pass every resolveSession check: only the pod-account
			// check refuses them.
			if (kind == "ordinary-session-pod" || kind == "other-coordinator-pod") && !strings.Contains(err.Error(), "does not run as") {
				t.Fatalf("refused for another reason: %v", err)
			}
		})
	}
}

// Only dev-env-coordinator-<name> in the session namespace, <name> a DNS-1123
// label of at most templates.MaxCoordinatorNameLength characters, is the
// identity. Each look-alike below is bound to a real session pod that runs as
// that same look-alike account, so only the name decides, and every one is
// refused.
func TestNamedCoordinatorNamesAreExact(t *testing.T) {
	long := templates.CoordinatorServiceAccountPrefix + strings.Repeat("a", templates.MaxCoordinatorNameLength+1)
	for _, account := range []string{
		"dev-env-coordinator-",
		"dev-env-coordinator",
		"dev-env-coordinatorlead",
		"dev-env-coordinator-Lead",
		"dev-env-coordinator--lead",
		"dev-env-coordinator-lead-",
		"dev-env-coordinator-lead.x",
		"x-dev-env-coordinator-lead",
		"dev-env-workbench-coordinator-lead",
		long,
	} {
		t.Run(account, func(t *testing.T) {
			f := newFixture(t)
			f.srv.Policy.NamedCoordinatorIdentities = true
			tok := namedCoordinatorPod(t, f, "haynes-ops-1031-140200", account, 0)
			if c, err := f.srv.resolveCaller(context.Background(), f.auth[tok]); err == nil {
				t.Fatalf("%q resolved to %+v", account, c)
			}
		})
	}

	// The right name in another namespace is not the identity, even when its token
	// names a real named coordinator session's pod.
	f := newFixture(t)
	f.srv.Policy.NamedCoordinatorIdentities = true
	tok := namedCoordinatorPod(t, f, "haynes-ops-1031-140201", leadSA, 0)
	for _, ns := range []string{"default", "dev-env-system", "dev-tools"} {
		id := f.auth[tok]
		id.Username, id.Namespace = serviceAccountPrefix+ns+":"+leadSA, ns
		if c, err := f.srv.resolveCaller(context.Background(), id); err == nil {
			t.Fatalf("%s/%s resolved to %+v", ns, leadSA, c)
		}
	}
	// The longest name is accepted.
	max := templates.CoordinatorServiceAccountPrefix + strings.Repeat("a", templates.MaxCoordinatorNameLength)
	tok = namedCoordinatorPod(t, f, "haynes-ops-1031-140202", max, 0)
	if c, err := f.srv.resolveCaller(context.Background(), f.auth[tok]); err != nil || c.kind != kindSession {
		t.Fatalf("the longest name: %+v %v", c, err)
	}
}

// Turning the policy on changes no other caller's classification: Tom, a
// client, the workbench (bound and unbound), an ordinary session, a coordinator
// host, the portal reader, and a stranger resolve exactly as they do with it
// off, and a coordinator host stays a coordinator. (A human or client entry with
// a coordinator-shaped name is a refused configuration:
// TestNamedCoordinatorPolicyRefusesHumanAndClientOverlap.)
func TestNamedCoordinatorLeavesOtherClassesUnchanged(t *testing.T) {
	f := coordinatorFixture(t)
	ctx := context.Background()
	wbRef := sessionNS + "/" + templates.WorkbenchServiceAccount
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, wbRef)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	f.srv.Policy.PortalReaderEnabled = true

	// An ordinary session pod.
	plainTok := f.sessionPod("haynes-ops-1031-140300", "full", 1)
	// A workbench session pod.
	wbTok := f.sessionPod("haynes-ops-1031-140301", "workbench", 0)
	var wbPod corev1.Pod
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: "haynes-ops-1031-140301"}, &wbPod); err != nil {
		t.Fatal(err)
	}
	wbPod.Spec.ServiceAccountName = templates.WorkbenchServiceAccount
	if err := f.c.Update(ctx, &wbPod); err != nil {
		t.Fatal(err)
	}
	wbBound := saIdentity(wbRef)
	wbBound.PodName, wbBound.PodUID = wbPod.Name, string(wbPod.UID)
	f.auth[wbTok] = wbBound
	// The portal reader's pod.
	portal := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-0", Namespace: portalReaderNamespace, UID: uuid.NewUUID()},
		Spec: corev1.PodSpec{ServiceAccountName: portalReaderAccount, Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	if err := f.c.Create(ctx, portal); err != nil {
		t.Fatal(err)
	}
	portalID := saIdentity(portalReaderRef)
	portalID.PodName, portalID.PodUID = portal.Name, string(portal.UID)
	// An ordinary session account token bound to no pod.
	agentUnbound := saIdentity(sessionNS + "/" + controller.ServiceAccountName)

	ids := map[string]Identity{
		"human":             f.auth[tokHuman],
		"client":            f.auth[tokClient],
		"stranger":          f.auth[tokStranger],
		"workbench-unbound": saIdentity(wbRef),
		"workbench-session": f.auth[wbTok],
		"session":           f.auth[plainTok],
		"session-unbound":   agentUnbound,
		"coordinator-host":  f.auth[tokCoordinator],
		"portal-reader":     portalID,
	}
	wantKind := map[string]callerKind{
		"human": kindHuman, "client": kindClient, "workbench-unbound": kindClient,
		"workbench-session": kindSession, "session": kindSession, "coordinator-host": kindCoordinator,
		"portal-reader": kindPortalReader,
	}
	type result struct {
		c   *caller
		err string
	}
	resolve := func() map[string]result {
		out := map[string]result{}
		for k, id := range ids {
			c, err := f.srv.resolveCaller(ctx, id)
			r := result{c: c}
			if err != nil {
				r.err = err.Error()
			}
			out[k] = r
		}
		return out
	}
	f.srv.Policy.NamedCoordinatorIdentities = false
	before := resolve()
	f.srv.Policy.NamedCoordinatorIdentities = true
	after := resolve()
	for k := range ids {
		b, a := before[k], after[k]
		if !reflect.DeepEqual(b, a) {
			t.Errorf("%s changed: before %+v %q, after %+v %q", k, b.c, b.err, a.c, a.err)
		}
		if want, ok := wantKind[k]; ok {
			if a.c == nil || a.c.kind != want {
				t.Errorf("%s: %+v %q, want %s", k, a.c, a.err, want)
			}
		} else if a.err == "" {
			t.Errorf("%s resolved to %+v, want a refusal", k, a.c)
		}
	}
}

// A coordinator host may not be configured with a named coordinator identity:
// that account is a session's, and resolveCaller would never reach the host. The
// one host configured live, and any other host account, parse as before.
func TestCoordinatorHostsMayNotNameACoordinatorIdentity(t *testing.T) {
	policy := Policy{Human: humanSA, Clients: []string{clientSA}, SessionNamespace: sessionNS,
		SessionServiceAccount: controller.ServiceAccountName, NamedCoordinatorIdentities: true}
	named := `[{"serviceAccount":"dev-agents/dev-env-coordinator-lead","podName":"host-0","hostID":"host"}]`
	if _, err := ParseCoordinatorHosts(named, policy); err == nil {
		t.Fatal("accepted a named coordinator identity as a coordinator host")
	}
	for _, raw := range []string{
		`[{"serviceAccount":"dev-agents/dev-env-owned-native-fixture","podName":"dev-env-owned-native-fixture","hostID":"owned-native-fixture"}]`,
		`[{"serviceAccount":"dev-env-system/codex-host-a","podName":"codex-host-a-0","hostID":"host-a"}]`,
		// The shape outside the session namespace is not the identity.
		`[{"serviceAccount":"dev-env-system/dev-env-coordinator-lead","podName":"host-0","hostID":"host"}]`,
	} {
		if hosts, err := ParseCoordinatorHosts(raw, policy); err != nil || len(hosts) != 1 {
			t.Fatalf("refused %s: %v", raw, err)
		}
	}
	// With the policy off the parse is what it was.
	policy.NamedCoordinatorIdentities = false
	if hosts, err := ParseCoordinatorHosts(named, policy); err != nil || len(hosts) != 1 {
		t.Fatalf("policy off: %v", err)
	}
}

// The coordination routes recheck the live Pod's account (sessionPodAccount). A
// named coordinator session passes that fence for its own pod, as the workbench
// session does, and fails it with the policy off.
func TestNamedCoordinatorSessionUsesCoordinationRoutes(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableClaims(f)
	f.srv.Policy.NamedCoordinatorIdentities = true
	const name = "leadsender"
	claimSession(t, f, name, humanSA)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ServiceAccountName = leadSA
	if err := f.c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	id := saIdentity(sessionNS + "/" + leadSA)
	id.PodName, id.PodUID = name, string(pod.UID)
	f.auth["tok-lead"] = id

	if w := f.do(http.MethodPost, apiv1.TaskClaimsPath, "tok-lead", claimRequest("issue:42")); w.Code != http.StatusCreated {
		t.Fatal("named coordinator session could not claim", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", "tok-lead", nil); w.Code != http.StatusOK {
		t.Fatal("named coordinator session could not list claims", w.Code, w.Body.String())
	}
	f.srv.Policy.NamedCoordinatorIdentities = false
	if w := f.do(http.MethodGet, apiv1.TaskClaimsPath+"?project=sample", "tok-lead", nil); w.Code != http.StatusForbidden {
		t.Fatal("named coordinator pod accepted with the policy off", w.Code)
	}
}
