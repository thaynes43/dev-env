package apiserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"
)

func TestAuthentication(t *testing.T) {
	f := newFixture(t)
	w := f.do(http.MethodGet, apiv1.SessionsPath, "", nil)
	wantError(t, w, http.StatusUnauthorized, apiv1.CodeUnauthenticated)
	if got := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate %q", got)
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath, "not-a-token", nil), http.StatusUnauthorized, apiv1.CodeUnauthenticated)

	// A token is never echoed: not in a refusal, not anywhere.
	w = f.do(http.MethodGet, apiv1.SessionsPath, "secret-token-value", nil)
	if strings.Contains(w.Body.String(), "secret-token-value") {
		t.Errorf("the refusal echoes the token: %s", w.Body.String())
	}
}

func TestAuthorizationByCallerClass(t *testing.T) {
	f := newFixture(t)
	f.auth["tok-user"] = Identity{Username: "tom@example.com"}
	f.auth["tok-unbound"] = saIdentity(sessionNS + "/" + controller.ServiceAccountName)

	for _, tc := range []struct {
		name, tok string
		status    int
	}{
		{"human", tokHuman, http.StatusOK},
		{"client (the v1 pod)", tokClient, http.StatusOK},
		{"a summoning caller before plan 10", tokStranger, http.StatusForbidden},
		{"a user that is not a ServiceAccount", "tok-user", http.StatusForbidden},
		{"the agent ServiceAccount without a bound pod", "tok-unbound", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.do(http.MethodGet, apiv1.FleetPath, tc.tok, nil)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestSessionTokenMustMatchItsPod(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)

	stale := f.auth[tok]
	stale.PodUID = "an-earlier-pod"
	f.auth["tok-stale"] = stale
	wantError(t, f.do(http.MethodGet, apiv1.FleetPath, "tok-stale", nil), http.StatusForbidden, apiv1.CodeForbidden)

	gone := f.auth[tok]
	gone.PodName = "no-such-pod"
	f.auth["tok-gone"] = gone
	wantError(t, f.do(http.MethodGet, apiv1.FleetPath, "tok-gone", nil), http.StatusForbidden, apiv1.CodeForbidden)

	if w := f.do(http.MethodGet, apiv1.FleetPath, tok, nil); w.Code != http.StatusOK {
		t.Fatalf("the session's own token: %d %s", w.Code, w.Body.String())
	}
}

func TestCreateAsHuman(t *testing.T) {
	f := newFixture(t)
	w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := decode[apiv1.Session](t, w)
	if want := "haynes-ops-1006-172226"; got.Name != want {
		t.Errorf("name %q, want %q", got.Name, want)
	}
	if loc := w.Header().Get("Location"); loc != apiv1.SessionPath(got.Name) {
		t.Errorf("Location %q", loc)
	}
	if got.Prompt != "" {
		t.Errorf("a create's answer carries the prompt")
	}
	s := f.session(got.Name)
	if s.Spec.Parent != humanSA {
		t.Errorf("parent %q, want %q", s.Spec.Parent, humanSA)
	}
	if s.Labels[v1alpha1.LabelDepth] != "0" {
		t.Errorf("depth label %q", s.Labels[v1alpha1.LabelDepth])
	}
	if s.Spec.Prompt != "fix the docs" || s.Spec.Effort != "xhigh" || s.Spec.Model != "claude-opus-5-5" {
		t.Errorf("spec %+v", s.Spec)
	}

	// The same second again: a suffix, never an overwrite.
	w = f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task())
	if w.Code != http.StatusCreated || decode[apiv1.Session](t, w).Name != "haynes-ops-1006-172226-2" {
		t.Fatalf("second create: %d %s", w.Code, w.Body.String())
	}

	// The detail carries the prompt; a list does not.
	d := decode[apiv1.Session](t, f.do(http.MethodGet, apiv1.SessionPath(got.Name), tokClient, nil))
	if d.Prompt != "fix the docs" {
		t.Errorf("detail prompt %q", d.Prompt)
	}
	l := decode[apiv1.SessionList](t, f.do(http.MethodGet, apiv1.SessionsPath, tokClient, nil))
	if len(l.Sessions) != 2 || l.Sessions[0].Prompt != "" {
		t.Errorf("list %+v", l)
	}
}

func TestCreateRefusals(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name   string
		mutate func(*apiv1.CreateSessionRequest)
		status int
		code   string
		field  string
	}{
		{"local mode is plan 02's", func(r *apiv1.CreateSessionRequest) { r.Mode, r.Prompt = "local", "" }, 422, apiv1.CodeInvalid, "mode"},
		{"remote mode is plan 03's", func(r *apiv1.CreateSessionRequest) { r.Mode, r.Prompt = "remote", "" }, 422, apiv1.CodeInvalid, "mode"},
		{"codex is plan 04's", func(r *apiv1.CreateSessionRequest) { r.Agent, r.Model, r.Effort = "codex", "gpt-6-astra", "" }, 422, apiv1.CodeInvalid, "agent"},
		{"no agent", func(r *apiv1.CreateSessionRequest) { r.Agent = "" }, 422, apiv1.CodeInvalid, "agent"},
		{"no model", func(r *apiv1.CreateSessionRequest) { r.Model, r.Effort = "", "" }, 422, apiv1.CodeInvalid, "model"},
		{"tools are plan 08's", func(r *apiv1.CreateSessionRequest) { r.Tools = []string{"blender"} }, 422, apiv1.CodeInvalid, "tools"},
		{"haiku takes no effort", func(r *apiv1.CreateSessionRequest) { r.Model, r.Effort = "claude-haiku-4-5", "low" }, 422, apiv1.CodeInvalid, "effort"},
		{"the 4.6 tier has no xhigh", func(r *apiv1.CreateSessionRequest) { r.Model = "claude-opus-4-6" }, 422, apiv1.CodeInvalid, "effort"},
		{"an unknown level", func(r *apiv1.CreateSessionRequest) { r.Effort = "turbo" }, 422, apiv1.CodeInvalid, "effort"},
		{"a prompt over 64 KiB", func(r *apiv1.CreateSessionRequest) { r.Prompt = strings.Repeat("x", protocol.MaxPromptBytes+1) }, 422, apiv1.CodeInvalid, "prompt"},
		{"a bad timeout", func(r *apiv1.CreateSessionRequest) { r.Limits = &apiv1.Limits{Timeout: "forty minutes"} }, 422, apiv1.CodeInvalid, "limits.timeout"},
		{"an idempotency key that is not a label value", func(r *apiv1.CreateSessionRequest) { r.IdempotencyKey = "has space" }, 422, apiv1.CodeInvalid, "idempotencyKey"},
		{"an unknown profile", func(r *apiv1.CreateSessionRequest) { r.Profile = "root" }, 422, apiv1.CodeInvalid, "profile"},
		{"an alias, by agentd's own check", func(r *apiv1.CreateSessionRequest) { r.Model, r.Effort = "opus", "" }, 422, apiv1.CodeInvalid, ""},
		{"a name is a summoning caller's", func(r *apiv1.CreateSessionRequest) { r.Name = "rem-x" }, 403, apiv1.CodeForbidden, ""},
		{"a lane is a summoning caller's", func(r *apiv1.CreateSessionRequest) { r.Lane = "remediation" }, 403, apiv1.CodeForbidden, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := task()
			tc.mutate(&req)
			e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req), tc.status, tc.code)
			if tc.code == apiv1.CodeInvalid {
				found := false
				for _, fe := range e.Fields {
					found = found || fe.Field == tc.field
				}
				if !found {
					t.Errorf("fields %+v lack %q", e.Fields, tc.field)
				}
			}
		})
	}

	ok := task()
	ok.Effort = "ultracode"
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, ok); w.Code != http.StatusCreated {
		t.Errorf("ultracode on a model that takes xhigh: %d %s", w.Code, w.Body.String())
	}
	ok = task()
	ok.Model, ok.Effort = "claude-haiku-4-5", ""
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, ok); w.Code != http.StatusCreated {
		t.Errorf("haiku with no effort: %d %s", w.Code, w.Body.String())
	}

	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list); err != nil || len(list.Items) != 2 {
		t.Errorf("a refused create left an object behind: %d, %v", len(list.Items), err)
	}
}

func TestCreateBodyRefusals(t *testing.T) {
	f := newFixture(t)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, `{"repo":"haynes-ops","parent":"someone-else"}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, `{"repo":`), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, `{} {}`), http.StatusBadRequest, apiv1.CodeBadRequest)
	big := `{"repo":"haynes-ops","prompt":"` + strings.Repeat("x", maxCreateBody) + `"}`
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, big), http.StatusRequestEntityTooLarge, apiv1.CodeTooLarge)

	req := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, nil)
	wantError(t, req, http.StatusUnsupportedMediaType, apiv1.CodeUnsupportedMediaType)
}

func TestIdempotencyKey(t *testing.T) {
	f := newFixture(t)
	req := task()
	req.IdempotencyKey = "agent-run-7f3a9c"
	first := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	name := decode[apiv1.Session](t, first).Name

	f.now = f.now.Add(5 * time.Second)
	again := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req)
	if again.Code != http.StatusOK || decode[apiv1.Session](t, again).Name != name {
		t.Fatalf("a repeat returns the first session: %d %s", again.Code, again.Body.String())
	}

	other := req
	other.Prompt = "something else"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, other), http.StatusConflict, apiv1.CodeConflict)

	// A key is scoped to its caller.
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokClient, req); w.Code != http.StatusCreated {
		t.Fatalf("another caller's key: %d %s", w.Code, w.Body.String())
	}

	// A finished session frees its key (V-03).
	s := f.session(name)
	s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited}
	if err := f.c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req); w.Code != http.StatusCreated || decode[apiv1.Session](t, w).Name == name {
		t.Fatalf("after the first finished: %d %s", w.Code, w.Body.String())
	}
}

func TestSessionChildren(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)

	w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("child: %d %s", w.Code, w.Body.String())
	}
	child := f.session(decode[apiv1.Session](t, w).Name)
	if child.Spec.Parent != "haynes-ops-1006-100000" || child.Labels[v1alpha1.LabelDepth] != "1" || child.Spec.Profile != "dev" {
		t.Errorf("child parent %q depth %q profile %q", child.Spec.Parent, child.Labels[v1alpha1.LabelDepth], child.Spec.Profile)
	}

	wider := task()
	wider.Profile = "full"
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, wider), http.StatusForbidden, apiv1.CodeForbidden)

	same := task()
	same.Profile = "dev"
	for i := 0; i < 2; i++ {
		f.now = f.now.Add(time.Second)
		if w := f.do(http.MethodPost, apiv1.SessionsPath, tok, same); w.Code != http.StatusCreated {
			t.Fatalf("child %d: %d %s", i+2, w.Code, w.Body.String())
		}
	}
	keyed := task()
	keyed.IdempotencyKey = "fourth-child"
	f.now = f.now.Add(time.Second)
	w = f.do(http.MethodPost, apiv1.SessionsPath, tok, keyed)
	if w.Code != http.StatusCreated {
		t.Fatalf("child 4: %d %s", w.Code, w.Body.String())
	}
	fourth := decode[apiv1.Session](t, w).Name
	f.now = f.now.Add(time.Second)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tok, task()), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	// A retry of the fourth, whose answer was lost, gets it back, not a 429.
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tok, keyed); w.Code != http.StatusOK || decode[apiv1.Session](t, w).Name != fourth {
		t.Fatalf("retry of the fourth child at the limit: %d %s", w.Code, w.Body.String())
	}

	// A child whose task ended no longer counts.
	child = f.session(child.Name)
	child.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited}
	if err := f.c.Status().Update(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	if w := f.do(http.MethodPost, apiv1.SessionsPath, tok, task()); w.Code != http.StatusCreated {
		t.Fatalf("after a child ended: %d %s", w.Code, w.Body.String())
	}

	// Two levels deep, then no more.
	grand := f.sessionPod("haynes-ops-1006-100001", "dev", 2)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, grand, task()), http.StatusForbidden, apiv1.CodeForbidden)
	level1 := f.sessionPod("haynes-ops-1006-100002", "dev", 1)
	f.now = f.now.Add(time.Second)
	w = f.do(http.MethodPost, apiv1.SessionsPath, level1, task())
	if w.Code != http.StatusCreated || f.session(decode[apiv1.Session](t, w).Name).Labels[v1alpha1.LabelDepth] != "2" {
		t.Fatalf("a level-1 session's child: %d %s", w.Code, w.Body.String())
	}
}

func TestSchemaRefusalIsMapped(t *testing.T) {
	f := newFixtureWith(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok {
				return apierrors.NewInvalid(schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "AgentSession"}, obj.GetName(), field.ErrorList{
					field.NotSupported(field.NewPath("spec", "size"), "XL", []string{"S", "M", "L"}),
				})
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	req := task()
	req.Size = "XL"
	e := wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, tokHuman, req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if len(e.Fields) != 1 || e.Fields[0].Field != "size" || !strings.Contains(e.Fields[0].Message, `"S"`) {
		t.Errorf("fields %+v", e.Fields)
	}
}

func TestList(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)
	mk := func(token, repo string) string {
		r := task()
		r.Repo = repo
		f.now = f.now.Add(time.Second)
		w := f.do(http.MethodPost, apiv1.SessionsPath, token, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		return decode[apiv1.Session](t, w).Name
	}
	a := mk(tokHuman, "haynes-ops")
	b := mk(tok, "dev-env")
	names := func(q, token string) []string {
		w := f.do(http.MethodGet, apiv1.SessionsPath+q, token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", q, w.Code, w.Body.String())
		}
		var out []string
		for _, s := range decode[apiv1.SessionList](t, w).Sessions {
			out = append(out, s.Name)
		}
		return out
	}
	if got := names("", tokHuman); len(got) != 3 || got[0] != b {
		t.Errorf("all, newest first: %v", got)
	}
	if got := names("?repo=dev-env", tokHuman); len(got) != 1 || got[0] != b {
		t.Errorf("repo: %v", got)
	}
	if got := names("?mine=true", tokHuman); len(got) != 2 || got[0] != a {
		t.Errorf("mine for Tom (his session and the fixture's): %v", got)
	}
	if got := names("?mine=true", tok); len(got) != 1 || got[0] != b {
		t.Errorf("mine for the session: %v", got)
	}
	if got := names("?state=pending", tokHuman); len(got) != 3 {
		t.Errorf("no phase yet counts as Pending: %v", got)
	}
	if got := names("?state=Running", tokHuman); len(got) != 0 {
		t.Errorf("Running: %v", got)
	}
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath+"?owner=me", tokHuman, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath+"?mine=yes", tokHuman, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	wantError(t, f.do(http.MethodGet, apiv1.SessionsPath+"?repo=a&repo=b", tokHuman, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
}

func TestGetAndRoutes(t *testing.T) {
	f := newFixture(t)
	wantError(t, f.do(http.MethodGet, apiv1.SessionPath("nope"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.SessionPath("Not_A_Label"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, "/v1/nothing", tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	w := f.do(http.MethodPut, apiv1.SessionsPath, tokHuman, nil)
	wantError(t, w, http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
	if w.Header().Get("Allow") != "GET, POST" {
		t.Errorf("Allow %q", w.Header().Get("Allow"))
	}
	// Method refusals come before authentication.
	wantError(t, f.do(http.MethodGet, apiv1.SessionPath("x")+"/heartbeat", "", nil), http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
}

func TestReap(t *testing.T) {
	f := newFixture(t)
	f.sessionPod("haynes-ops-1006-100000", "full", 0)

	w := f.do(http.MethodDelete, apiv1.SessionPath("haynes-ops-1006-100000"), tokHuman, nil)
	if w.Code != http.StatusAccepted || !decode[apiv1.Session](t, w).Reaping {
		t.Fatalf("reap: %d %s", w.Code, w.Body.String())
	}
	s := f.session("haynes-ops-1006-100000")
	if s.DeletionTimestamp.IsZero() {
		t.Fatal("the session is not being deleted")
	}
	// The finalizer holds it: the reap is the operator's to finish (D-45).
	if w := f.do(http.MethodDelete, apiv1.SessionPath("haynes-ops-1006-100000"), tokClient, nil); w.Code != http.StatusAccepted {
		t.Fatalf("a second reap: %d %s", w.Code, w.Body.String())
	}
	wantError(t, f.do(http.MethodDelete, apiv1.SessionPath("nope"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)

	// A session the operator has seen, without the finalizer, is refused:
	// deleting it could lose its pod and volume unrescued.
	bare := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Name: "bare", Namespace: sessionNS},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"},
	}
	if err := f.c.Create(context.Background(), bare); err != nil {
		t.Fatal(err)
	}
	bare.Status.Phase, bare.Status.PodName = v1alpha1.PhaseRunning, "bare"
	if err := f.c.Status().Update(context.Background(), bare); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodDelete, apiv1.SessionPath("bare"), tokHuman, nil), http.StatusConflict, apiv1.CodeConflict)
}

func TestFleet(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	add := func(name string, phase v1alpha1.SessionPhase, node string, outdated bool) {
		s := &v1alpha1.AgentSession{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS},
			Spec:       v1alpha1.AgentSessionSpec{Repo: "r", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p"},
		}
		if err := f.c.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		s.Status.Phase, s.Status.NodeName = phase, node
		if phase == v1alpha1.PhasePending {
			s.Status.PendingReason = "0/9 nodes are available: 3 Insufficient cpu."
		}
		st := metav1.ConditionFalse
		if outdated {
			st = metav1.ConditionTrue
		}
		s.Status.Conditions = []metav1.Condition{{Type: controller.ConditionOutdated, Status: st, Reason: "R", LastTransitionTime: metav1.Now()}}
		if err := f.c.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	add("a", v1alpha1.PhaseRunning, "talosw02", false)
	add("b", v1alpha1.PhaseRunning, "talosw02", true)
	add("c", v1alpha1.PhasePending, "", false)
	add("d", v1alpha1.PhaseSuspended, "", false)

	w := f.do(http.MethodGet, apiv1.FleetPath, tokHuman, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	fl := decode[apiv1.Fleet](t, w)
	if fl.Revision != f.tmpl.Revision() || fl.RevisionError != "" {
		t.Errorf("revision %q %q", fl.Revision, fl.RevisionError)
	}
	if fl.Phases["Running"] != 2 || fl.Phases["Pending"] != 1 || fl.Phases["Suspended"] != 1 {
		t.Errorf("phases %v", fl.Phases)
	}
	if len(fl.Sessions) != 3 || fl.Sessions[0].Name != "c" || fl.Sessions[0].Pending == "" {
		t.Errorf("sessions %+v", fl.Sessions)
	}
	if len(fl.Nodes) != 1 || fl.Nodes[0] != (apiv1.FleetNode{Name: "talosw02", Sessions: 2}) {
		t.Errorf("nodes %+v", fl.Nodes)
	}
	if len(fl.Outdated) != 1 || fl.Outdated[0] != "b" {
		t.Errorf("outdated %v", fl.Outdated)
	}
	wantError(t, f.do(http.MethodGet, apiv1.FleetPath+"?x=1", tokHuman, nil), http.StatusBadRequest, apiv1.CodeBadRequest)

	f.tmplOK = false
	if fl := decode[apiv1.Fleet](t, f.do(http.MethodGet, apiv1.FleetPath, tokHuman, nil)); fl.Revision != "" || fl.RevisionError == "" {
		t.Errorf("unreadable templates: %+v", fl)
	}
}

func TestGeneratedName(t *testing.T) {
	at := time.Date(2026, 10, 5, 20, 25, 4, 0, time.FixedZone("EDT", -4*3600))
	for repo, want := range map[string]string{
		"haynes-ops":             "haynes-ops-1006-002504",
		"My_Repo.git":            "my-repo-git-1006-002504",
		"__":                     "session-1006-002504",
		strings.Repeat("a", 100): strings.Repeat("a", maxRepoInName) + "-1006-002504",
	} {
		got := generatedName(repo, at)
		if got != want {
			t.Errorf("%q: %q, want %q", repo, got, want)
		}
		if len(got)+2 > 63 {
			t.Errorf("%q: %d characters leave no room for a suffix", repo, len(got))
		}
	}
}
