package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// A workbench session's pod runs as dev-env-workbench, a trusted client. Its
// pod-bound token must still speak for its session (the heartbeat needs that),
// while a token of the same account that no session pod carries stays the
// client's.
func TestWorkbenchSessionPodTokenIsTheSessions(t *testing.T) {
	f := newFixture(t)
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	const name = "haynes-ops-1019-100000"
	f.sessionPod(name, "workbench", 0)
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &pod); err != nil {
		t.Fatal(err)
	}
	wb := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)

	// Bound to the session's pod: a session.
	bound := wb
	bound.PodName, bound.PodUID = name, string(pod.UID)
	c, err := f.srv.resolveCaller(context.Background(), bound)
	if err != nil || c.kind != kindSession || c.parent != name {
		t.Fatalf("pod-bound: %+v %v", c, err)
	}

	// Bound to no pod (the human's tooling minting the account's token): a client.
	c, err = f.srv.resolveCaller(context.Background(), wb)
	if err != nil || c.kind != kindClient || c.parent != sessionNS+"/"+templates.WorkbenchServiceAccount {
		t.Fatalf("unbound: %+v %v", c, err)
	}

	// Bound to a pod no session owns: a client, not a session.
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "tool", Namespace: sessionNS, UID: uuid.NewUUID()}}
	if err := f.c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	foreign := wb
	foreign.PodName, foreign.PodUID = "tool", string(other.UID)
	if c, err = f.srv.resolveCaller(context.Background(), foreign); err != nil || c.kind != kindClient {
		t.Fatalf("foreign pod: %+v %v", c, err)
	}

	// A pod-bound token whose pod is gone is refused, never a client.
	gone := wb
	gone.PodName, gone.PodUID = "vanished", "x"
	if c, err = f.srv.resolveCaller(context.Background(), gone); err == nil {
		t.Fatalf("a vanished pod resolved to %+v", c)
	}

	// Without the policy field the account is only a client, as before.
	f.srv.Policy.WorkbenchServiceAccount = ""
	if c, err = f.srv.resolveCaller(context.Background(), bound); err != nil || c.kind != kindClient {
		t.Fatalf("policy off: %+v %v", c, err)
	}
}

// The heartbeat route accepts the workbench session's own token.
func TestWorkbenchSessionHeartbeats(t *testing.T) {
	f := newFixture(t)
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	const name = "haynes-ops-1019-100001"
	tok := f.sessionPod(name, "workbench", 0)
	id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	id.PodName, id.PodUID = f.auth[tok].PodName, f.auth[tok].PodUID
	f.auth[tok] = id
	w := f.do(http.MethodPost, protocol.HeartbeatPath(name), tok, status(name))
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
}

// Story110 T30: a session whose pod runs as the workbench ServiceAccount may
// create a Codex remote host, as the workbench itself may. A session on the
// ordinary account may not.
func TestWorkbenchSessionCreatesCodexHost(t *testing.T) {
	f := newFixture(t)
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
	f.srv.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
	req := apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "codex", Mode: "remote", Model: "gpt-6-luna"}

	wbTok := f.sessionPod("haynes-ops-1019-100002", "dev", 0)
	id := saIdentity(sessionNS + "/" + templates.WorkbenchServiceAccount)
	id.PodName, id.PodUID = f.auth[wbTok].PodName, f.auth[wbTok].PodUID
	f.auth[wbTok] = id
	w := f.do(http.MethodPost, apiv1.SessionsPath, wbTok, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("workbench session creating a codex host: %d %s", w.Code, w.Body.String())
	}
	// The host must not inherit the workbench profile (its identity has pods/exec
	// and could start more hosts): it runs on the default profile.
	var host v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: decode[apiv1.Session](t, w).Name}, &host); err != nil {
		t.Fatal(err)
	}
	if host.Spec.Profile != "" {
		t.Errorf("the host's profile is %q, want the default", host.Spec.Profile)
	}
	// Naming the parent's (or any) profile is refused rather than honoured.
	withProfile := req
	withProfile.Profile = "dev"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, wbTok, withProfile), http.StatusForbidden, apiv1.CodeForbidden)

	// An ordinary session stays refused.
	plain := f.sessionPod("haynes-ops-1019-100003", "dev", 0)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, plain, req), http.StatusForbidden, apiv1.CodeForbidden)

	// The workbench session is still its session for the heartbeat route.
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("haynes-ops-1019-100002"), wbTok, status("haynes-ops-1019-100002")); w.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
}

// OP502 (Tom, 2026-11-09): a workbench session is the superuser class, so its
// workers run guarded unless it says otherwise. Its child runs on the default
// profile when the request names none, as a trusted client's does, and on a
// named profile when that profile is valid, workbench included. The Codex host
// rule and an ordinary session's parent-profile rule are unchanged.
func TestWorkbenchSessionChildProfile(t *testing.T) {
	f := newFixture(t)
	wb := workbenchSessionToken(t, f, "haynes-ops-1109-100000")
	create := func(tok string, req apiv1.CreateSessionRequest) *v1alpha1.AgentSession {
		t.Helper()
		f.now = f.now.Add(time.Second)
		w := f.do(http.MethodPost, apiv1.SessionsPath, tok, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("profile %q: %d %s", req.Profile, w.Code, w.Body.String())
		}
		v := decode[apiv1.Session](t, w)
		s := f.session(v.Name)
		if v.Profile != s.Spec.Profile {
			t.Fatalf("the response says profile %q, the session %q", v.Profile, s.Spec.Profile)
		}
		return s
	}

	// No profile: the templates' default, not the workbench's.
	s := create(wb, task())
	if s.Spec.Profile != "" || s.Spec.Parent != "haynes-ops-1109-100000" || s.Labels[v1alpha1.LabelDepth] != "1" {
		t.Fatalf("no profile: profile %q, parent %q, depth %q", s.Spec.Profile, s.Spec.Parent, s.Labels[v1alpha1.LabelDepth])
	}
	if name, _, err := f.tmpl.Profile(s.Spec.Profile); err != nil || name != "full" {
		t.Fatalf("the default resolves to %q (%v), want the templates' default full", name, err)
	}
	// An explicit workbench: honoured.
	if s := create(wb, withProfile(task(), "workbench")); s.Spec.Profile != "workbench" || s.Labels[templates.CoordinatorIdentityLabel] != "" {
		t.Fatalf("workbench: profile %q, admission %q", s.Spec.Profile, s.Labels[templates.CoordinatorIdentityLabel])
	}
	// Another valid profile: honoured.
	if s := create(wb, withProfile(task(), "dev")); s.Spec.Profile != "dev" {
		t.Fatalf("dev: profile %q", s.Spec.Profile)
	}
	// ops, the summoned route's profile: honoured too, as for a trusted
	// client's create. It holds less than workbench, so it is no escalation.
	if _, _, err := f.tmpl.Profile("ops"); err != nil {
		t.Fatalf("the fixture templates have no ops profile: %v", err)
	}
	if s := create(wb, withProfile(task(), "ops")); s.Spec.Profile != "ops" {
		t.Fatalf("ops: profile %q", s.Spec.Profile)
	}
	// A profile the templates lack: refused, and nothing is created.
	before := f.sessionCount()
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, wb, withProfile(task(), "root")), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if len(e.Fields) != 1 || e.Fields[0].Field != "profile" || f.sessionCount() != before {
		t.Fatalf("unknown profile: %+v, %d sessions created", e, f.sessionCount()-before)
	}

	// The Codex host a workbench session starts still runs on the default
	// profile, and naming any profile, workbench included, is still refused.
	host := apiv1.CreateSessionRequest{Repo: "haynes-ops", Agent: "codex", Mode: "remote", Model: "gpt-6-luna"}
	if s := create(wb, host); s.Spec.Profile != "" {
		t.Fatalf("codex host: profile %q, want the default", s.Spec.Profile)
	}
	for _, profile := range []string{"workbench", "full", "dev"} {
		e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, wb, withProfile(host, profile)), http.StatusForbidden, apiv1.CodeForbidden)
		if !strings.Contains(e.Message, "omit the profile") {
			t.Fatalf("codex host on %s: %q", profile, e.Message)
		}
	}

	// A workbench session that is itself a child follows the same rule.
	deep := workbenchSessionToken(t, f, "haynes-ops-1109-100001")
	deepSess := f.session("haynes-ops-1109-100001")
	deepSess.Labels[v1alpha1.LabelDepth] = "1"
	if err := f.c.Update(context.Background(), deepSess); err != nil {
		t.Fatal(err)
	}
	if s := create(deep, task()); s.Spec.Profile != "" || s.Labels[v1alpha1.LabelDepth] != "2" {
		t.Fatalf("deep workbench child: profile %q, depth %q", s.Spec.Profile, s.Labels[v1alpha1.LabelDepth])
	}

	// An ordinary session's child still inherits its parent's profile, and
	// another profile, workbench included, is still refused.
	plain := f.sessionPod("haynes-ops-1109-100002", "dev", 0)
	if s := create(plain, task()); s.Spec.Profile != "dev" {
		t.Fatalf("ordinary child: profile %q, want its parent's dev", s.Spec.Profile)
	}
	for _, profile := range []string{"workbench", "full"} {
		e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, plain, withProfile(task(), profile)), http.StatusForbidden, apiv1.CodeForbidden)
		if !strings.Contains(e.Message, "parent's profile dev") {
			t.Fatalf("ordinary child on %s: %q", profile, e.Message)
		}
	}
}

func withProfile(r apiv1.CreateSessionRequest, profile string) apiv1.CreateSessionRequest {
	r.Profile = profile
	return r
}
