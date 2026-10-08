package apiserver

import (
	"context"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// kubeReq is a valid kube grant request.
func kubeReq() apiv1.CreateGrantRequest {
	return apiv1.CreateGrantRequest{
		Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"home-automation", "frontend"},
		TTL: "15m", Reason: "patch the zigbee2mqtt image to test 2.12.1",
	}
}

func egressReq() apiv1.CreateGrantRequest {
	return apiv1.CreateGrantRequest{
		Type: "egress", FQDNs: []string{"a.example.com", "b.example.com"}, CIDRs: []string{"192.168.40.21/32"},
		Endpoints: []apiv1.GrantEndpoint{{Namespace: "frontend", MatchLabels: map[string]string{"app": "web", "tier": "x"}}, {Namespace: "database"}},
		Ports:     []apiv1.GrantPort{{Port: 443}, {Port: 53, Protocol: "UDP"}},
		Reason:    "reach the printer",
	}
}

func (f *fixture) grant(name string) *v1alpha1.AccessGrant {
	f.t.Helper()
	var g v1alpha1.AccessGrant
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: name}, &g); err != nil {
		f.t.Fatal(err)
	}
	return &g
}

func (f *fixture) grantCount() int {
	f.t.Helper()
	var list v1alpha1.AccessGrantList
	if err := f.c.List(context.Background(), &list); err != nil {
		f.t.Fatal(err)
	}
	return len(list.Items)
}

// setGrantStatus plays the broker.
func (f *fixture) setGrantStatus(name string, phase v1alpha1.GrantPhase, expiresAt *time.Time) {
	f.t.Helper()
	g := f.grant(name)
	g.Status.Phase = phase
	if expiresAt != nil {
		t := metav1.NewTime(*expiresAt)
		g.Status.ExpiresAt = &t
	}
	if err := f.c.Status().Update(context.Background(), g); err != nil {
		f.t.Fatal(err)
	}
}

// request posts a grant request and wants status; it returns the grant.
func (f *fixture) request(tok string, req any, status int) apiv1.Grant {
	f.t.Helper()
	w := f.do(http.MethodPost, apiv1.GrantsPath, tok, req)
	if w.Code != status {
		f.t.Fatalf("request: %d, want %d: %s", w.Code, status, w.Body.String())
	}
	g := decode[apiv1.Grant](f.t, w)
	if loc := w.Header().Get("Location"); loc != apiv1.GrantPath(g.Name) {
		f.t.Errorf("Location %q for %s", loc, g.Name)
	}
	return g
}

var grantNameRE = regexp.MustCompile(`^grant-1006-172226-[0-9a-f]{4}$`)

func TestGrantRequest(t *testing.T) {
	f := newFixture(t)
	f.srv.GrantApprovalURL = "https://dev-env.example.com/grants"
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)

	got := f.request(tok, kubeReq(), http.StatusCreated)
	if !grantNameRE.MatchString(got.Name) {
		t.Errorf("name %q", got.Name)
	}
	if got.Phase != apiv1.GrantPending || got.ApprovalURL != "https://dev-env.example.com/grants/"+got.Name || got.TTL != "15m0s" {
		t.Errorf("view %+v", got)
	}
	g := f.grant(got.Name)
	var session v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "haynes-ops-1006-100000"}, &session); err != nil {
		t.Fatal(err)
	}
	want := v1alpha1.GrantRequester{Session: "haynes-ops-1006-100000", SessionUID: session.UID, Repo: "haynes-ops", Profile: "dev", Agent: "claude", Parent: humanSA}
	if g.Spec.Requester != want {
		t.Errorf("requester %+v, want %+v", g.Spec.Requester, want)
	}
	if g.Labels[v1alpha1.LabelSession] != "haynes-ops-1006-100000" || g.Namespace != sessionNS {
		t.Errorf("labels %v namespace %s", g.Labels, g.Namespace)
	}
	if g.Spec.Kube == nil || g.Spec.Kube.Role != v1alpha1.RoleWorkloads || g.Spec.TTL.Duration != 15*time.Minute || g.Spec.Release {
		t.Errorf("spec %+v", g.Spec)
	}

	// The body cannot name a requester: the field is unknown.
	wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok,
		`{"type":"kube","role":"dev-env-grant-workloads","namespaces":["frontend"],"reason":"r","requester":{"session":"someone-else"}}`),
		http.StatusBadRequest, apiv1.CodeBadRequest)

	// The default TTL of a kube grant is an hour.
	r := kubeReq()
	r.Namespaces, r.TTL = []string{"observability"}, ""
	if g := f.request(tok, r, http.StatusCreated); g.TTL != "1h0m0s" {
		t.Errorf("default ttl %q", g.TTL)
	}
	// Once approved, the page link goes.
	f.setGrantStatus(got.Name, v1alpha1.GrantActive, nil)
	if v := decode[apiv1.Grant](t, f.do(http.MethodGet, apiv1.GrantPath(got.Name), tok, nil)); v.ApprovalURL != "" || v.Phase != "Active" {
		t.Errorf("active view %+v", v)
	}
}

// replaceGrantSession creates a new authenticated session and pod with the same
// names, rather than editing the old requester's identity in place.
func replaceGrantSession(f *fixture, name string) string {
	f.t.Helper()
	ctx := context.Background()
	s := f.session(name)
	s.Finalizers = nil
	if err := f.c.Update(ctx, s); err != nil {
		f.t.Fatal(err)
	}
	if err := f.c.Delete(ctx, s); err != nil {
		f.t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name}}); err != nil {
		f.t.Fatal(err)
	}
	return f.sessionPod(name, "full", 0)
}

func TestGrantRequestReplacementSessionUID(t *testing.T) {
	requests := []apiv1.CreateGrantRequest{
		{Type: "credential", Credential: "proxmox", TTL: "15m", Reason: "maintain a guest"},
		kubeReq(), egressReq(), {Type: "breakglass", Reason: "repair the cluster"},
	}
	for _, req := range requests {
		for _, phase := range []v1alpha1.GrantPhase{v1alpha1.GrantPending, v1alpha1.GrantActive} {
			t.Run(req.Type+"/"+string(phase), func(t *testing.T) {
				// Stand in for API-assigned object UIDs as well as session UIDs.
				f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if obj.GetUID() == "" {
						obj.SetUID(uuid.NewUUID())
					}
					return c.Create(ctx, obj, opts...)
				}})
				const name = "haynes-ops-1006-100000"
				tok := f.sessionPod(name, "full", 0)
				original := f.request(tok, req, http.StatusCreated)
				future := f.now.Add(time.Hour)
				f.setGrantStatus(original.Name, phase, &future)
				old := []*v1alpha1.AccessGrant{f.grant(original.Name)}
				for _, ns := range []string{"old-a", "old-b"} {
					r := kubeReq()
					r.Namespaces = []string{ns}
					old = append(old, f.grant(f.request(tok, r, http.StatusCreated).Name))
				}
				oldUID := f.session(name).UID
				tok = replaceGrantSession(f, name)
				newUID := f.session(name).UID
				if oldUID == "" || newUID == "" || oldUID == newUID {
					t.Fatal("fixture did not replace the authenticated session")
				}
				created := f.request(tok, req, http.StatusCreated)
				g := f.grant(created.Name)
				if g.Name == original.Name || g.UID == old[0].UID || g.Spec.Requester.SessionUID != newUID {
					t.Fatal("replacement inherited the predecessor's grant or UID")
				}
				// Only requests from the new UID count toward its quota.
				for _, ns := range []string{"new-a", "new-b"} {
					r := kubeReq()
					r.Namespaces = []string{ns}
					f.request(tok, r, http.StatusCreated)
				}
				fourth := kubeReq()
				fourth.Namespaces = []string{"new-c"}
				wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, fourth), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
				if again := f.request(tok, req, http.StatusOK); again.Name != created.Name {
					t.Fatal("same-UID repeat did not return its own request at the limit")
				}
				for _, snapshot := range old {
					if !reflect.DeepEqual(f.grant(snapshot.Name), snapshot) {
						t.Fatal("replacement POST changed a predecessor's grant")
					}
				}
				wantError(t, f.do(http.MethodDelete, apiv1.GrantPath(original.Name), tok, nil), http.StatusForbidden, apiv1.CodeForbidden)
				if !reflect.DeepEqual(f.grant(original.Name), old[0]) {
					t.Fatal("replacement DELETE changed the predecessor's grant")
				}
				// Audit listings continue to include both incarnations by name.
				listed := decode[apiv1.GrantList](t, f.do(http.MethodGet, apiv1.GrantsPath+"?mine=true", tok, nil))
				if len(listed.Items) != 6 {
					t.Fatalf("audit list has %d grants, want all six", len(listed.Items))
				}
			})
		}
	}
}

func TestGrantRequestLegacyUIDCompatibility(t *testing.T) {
	for _, req := range []apiv1.CreateGrantRequest{kubeReq(), egressReq(), {Type: "breakglass", Reason: "repair the cluster"}, {Type: "credential", Credential: "proxmox", Reason: "maintain a guest"}} {
		t.Run(req.Type, func(t *testing.T) {
			f := newFixture(t)
			const name = "haynes-ops-1006-100000"
			tok := f.sessionPod(name, "full", 0)
			old := f.request(tok, req, http.StatusCreated)
			legacy := f.grant(old.Name)
			// Represent a grant written before requester.sessionUID existed.
			legacy.Spec.Requester.SessionUID = ""
			if err := f.c.Update(context.Background(), legacy); err != nil {
				t.Fatal(err)
			}
			if req.Type == "credential" {
				// Three unbound credential requests cannot consume a new UID's quota.
				for _, suffix := range []string{"-a", "-b"} {
					other := legacy.DeepCopy()
					other.Name += suffix
					other.ResourceVersion = ""
					if err := f.c.Create(context.Background(), other); err != nil {
						t.Fatal(err)
					}
				}
			}
			tok = replaceGrantSession(f, name)
			status := http.StatusOK
			if req.Type == "credential" {
				status = http.StatusCreated
			}
			got := f.request(tok, req, status)
			if req.Type == "credential" {
				if got.Name == old.Name || f.grant(got.Name).Spec.Requester.SessionUID != f.session(name).UID {
					t.Fatal("replacement inherited a legacy credential request")
				}
			} else if got.Name != old.Name {
				t.Fatal("historical unbound non-credential grant lost name compatibility")
			}
			// A compatible legacy non-credential request still counts; excluded
			// legacy credentials do not. Both leave room for exactly two more.
			for _, ns := range []string{"fresh-a", "fresh-b"} {
				r := kubeReq()
				r.Namespaces = []string{ns}
				f.request(tok, r, http.StatusCreated)
			}
			fourth := kubeReq()
			fourth.Namespaces = []string{"fresh-c"}
			wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, fourth), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
			w := f.do(http.MethodDelete, apiv1.GrantPath(old.Name), tok, nil)
			if req.Type == "credential" {
				wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
				if f.grant(old.Name).Spec.Release {
					t.Fatal("replacement released an unbound legacy credential grant")
				}
			} else if w.Code != http.StatusAccepted || !f.grant(old.Name).Spec.Release {
				t.Fatal("legacy non-credential requester lost release compatibility")
			}
		})
	}
}

func TestGrantRequestCallers(t *testing.T) {
	f := newFixture(t)
	for _, tok := range []string{tokHuman, tokClient} {
		e := wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, kubeReq()), http.StatusForbidden, apiv1.CodeForbidden)
		if !strings.Contains(e.Message, "requested by sessions") || !strings.Contains(e.Message, "broker's page") {
			t.Errorf("message %q", e.Message)
		}
	}
	wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tokStranger, kubeReq()), http.StatusForbidden, apiv1.CodeForbidden)

	// A session being reaped asks for nothing more.
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)
	if err := f.c.Delete(context.Background(), f.session("haynes-ops-1006-100000")); err != nil {
		t.Fatal(err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, kubeReq()), http.StatusConflict, apiv1.CodeConflict)
	if n := f.grantCount(); n != 0 {
		t.Errorf("%d grants", n)
	}
}

func TestGrantRequestRefusals(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)
	for _, tc := range []struct {
		name  string
		req   apiv1.CreateGrantRequest
		field string
	}{
		{"no type", apiv1.CreateGrantRequest{Reason: "r"}, "type"},
		{"an unknown type", apiv1.CreateGrantRequest{Type: "lease", Reason: "r"}, "type"},
		{"kube without a role", apiv1.CreateGrantRequest{Type: "kube", Namespaces: []string{"x"}, Reason: "r"}, "role"},
		{"kube with ports", apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleNodes, Ports: []apiv1.GrantPort{{Port: 1}}, Reason: "r"}, "ports"},
		{"kube with a credential", apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleNodes, Credential: "proxmox", Reason: "r"}, "credential"},
		{"break-glass with another role", apiv1.CreateGrantRequest{Type: "breakglass", Role: v1alpha1.RoleWorkloads, Reason: "r"}, "role"},
		{"break-glass with fqdns", apiv1.CreateGrantRequest{Type: "breakglass", FQDNs: []string{"a.example.com"}, Reason: "r"}, "fqdns"},
		{"egress without ports", apiv1.CreateGrantRequest{Type: "egress", FQDNs: []string{"a.example.com"}, Reason: "r"}, "ports"},
		{"egress without a destination", apiv1.CreateGrantRequest{Type: "egress", Ports: []apiv1.GrantPort{{Port: 443}}, Reason: "r"}, ""},
		{"egress with a role", apiv1.CreateGrantRequest{Type: "egress", Role: v1alpha1.RoleWorkloads, CIDRs: []string{"10.0.0.1/32"}, Ports: []apiv1.GrantPort{{Port: 443}}, Reason: "r"}, "role"},
		{"credential without a name", apiv1.CreateGrantRequest{Type: "credential", Reason: "r"}, "credential"},
		{"credential with namespaces", apiv1.CreateGrantRequest{Type: "credential", Credential: "hw-ssh", Namespaces: []string{"x"}, Reason: "r"}, "namespaces"},
		{"a TTL that is not a duration", apiv1.CreateGrantRequest{Type: "credential", Credential: "hw-ssh", TTL: "an hour", Reason: "r"}, "ttl"},
		{"no reason", apiv1.CreateGrantRequest{Type: "credential", Credential: "hw-ssh", Reason: "  "}, "reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, tc.req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			found := false
			for _, fe := range e.Fields {
				found = found || fe.Field == tc.field
			}
			if !found {
				t.Errorf("fields %+v lack %q", e.Fields, tc.field)
			}
		})
	}
	if n := f.grantCount(); n != 0 {
		t.Errorf("a refused request left %d grants", n)
	}
	wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, `{"type":`), http.StatusBadRequest, apiv1.CodeBadRequest)
}

func TestGrantBreakglass(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "ops", 0)
	got := f.request(tok, apiv1.CreateGrantRequest{Type: "breakglass", Reason: "re-clone the CNPG cluster"}, http.StatusCreated)
	g := f.grant(got.Name)
	if g.Spec.Kube == nil || g.Spec.Kube.Role != v1alpha1.RoleBreakglass || g.Spec.TTL.Duration != 30*time.Minute || got.Role != apiv1.BreakglassRole {
		t.Errorf("spec %+v view %+v", g.Spec, got)
	}
	// Naming the role is allowed, and it is the same request.
	again := f.request(tok, apiv1.CreateGrantRequest{Type: "breakglass", Role: apiv1.BreakglassRole, TTL: "45m", Reason: "again"}, http.StatusOK)
	if again.Name != got.Name {
		t.Errorf("merge %s, want %s", again.Name, got.Name)
	}
}

func TestGrantMerge(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)
	first := f.request(tok, kubeReq(), http.StatusCreated)

	// The same scope with the namespaces in another order, another TTL and
	// another reason is the same request.
	same := kubeReq()
	same.Namespaces = []string{"frontend", "home-automation"}
	same.TTL, same.Reason = "2h", "something else"
	f.now = f.now.Add(time.Second)
	if g := f.request(tok, same, http.StatusOK); g.Name != first.Name {
		t.Errorf("merge %s, want %s", g.Name, first.Name)
	}
	if n := f.grantCount(); n != 1 {
		t.Fatalf("%d grants after a merge", n)
	}

	// Another scope is another grant.
	other := kubeReq()
	other.Namespaces = []string{"frontend"}
	second := f.request(tok, other, http.StatusCreated)

	// Another session's identical request is its own.
	tok2 := f.sessionPod("haynes-ops-1006-100001", "dev", 0)
	if g := f.request(tok2, kubeReq(), http.StatusCreated); g.Name == first.Name {
		t.Error("another session's request merged into this session's grant")
	}

	// An active grant still merges; past its expiry, or ended, or released, it
	// does not.
	future, past := f.now.Add(time.Hour), f.now.Add(-time.Minute)
	f.setGrantStatus(first.Name, v1alpha1.GrantActive, &future)
	if g := f.request(tok, same, http.StatusOK); g.Name != first.Name {
		t.Errorf("active merge %s", g.Name)
	}
	f.setGrantStatus(first.Name, v1alpha1.GrantActive, &past)
	expiredButActive := f.request(tok, same, http.StatusCreated)
	f.setGrantStatus(second.Name, v1alpha1.GrantDenied, nil)
	if g := f.request(tok, other, http.StatusCreated); g.Name == second.Name {
		t.Error("a denied grant merged")
	}
	if w := f.do(http.MethodDelete, apiv1.GrantPath(expiredButActive.Name), tok, nil); w.Code != http.StatusAccepted {
		t.Fatalf("release: %d %s", w.Code, w.Body.String())
	}
	if g := f.request(tok, same, http.StatusCreated); g.Name == expiredButActive.Name {
		t.Error("a released grant merged")
	}

	// Egress: sets and order-insensitive lists; an empty protocol is TCP.
	tok3 := f.sessionPod("haynes-ops-1006-100002", "dev", 0)
	eg := f.request(tok3, egressReq(), http.StatusCreated)
	if eg.Ports[0].Protocol != "TCP" {
		t.Errorf("protocol %+v", eg.Ports)
	}
	shuffled := egressReq()
	shuffled.FQDNs = []string{"b.example.com", "a.example.com"}
	shuffled.Endpoints = []apiv1.GrantEndpoint{{Namespace: "database"}, {Namespace: "frontend", MatchLabels: map[string]string{"tier": "x", "app": "web"}}}
	shuffled.Ports = []apiv1.GrantPort{{Port: 53, Protocol: "UDP"}, {Port: 443, Protocol: "TCP"}}
	if g := f.request(tok3, shuffled, http.StatusOK); g.Name != eg.Name {
		t.Errorf("egress merge %s, want %s", g.Name, eg.Name)
	}
	narrower := egressReq()
	narrower.Endpoints[0].MatchLabels = map[string]string{"app": "web"}
	if g := f.request(tok3, narrower, http.StatusCreated); g.Name == eg.Name {
		t.Error("other endpoint labels merged")
	}
	cred := apiv1.CreateGrantRequest{Type: "credential", Credential: "hw-ssh", Reason: "read the array log"}
	c1 := f.request(tok3, cred, http.StatusCreated)
	if g := f.request(tok3, cred, http.StatusOK); g.Name != c1.Name {
		t.Errorf("credential merge %s", g.Name)
	}
}

func TestGrantPendingLimit(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)
	var names []string
	for _, ns := range []string{"a", "b", "c"} {
		r := kubeReq()
		r.Namespaces = []string{ns}
		names = append(names, f.request(tok, r, http.StatusCreated).Name)
	}
	fourth := kubeReq()
	fourth.Namespaces = []string{"d"}
	wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, fourth), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)

	// A repeat of a pending one at the limit gets it back, not a 429.
	r := kubeReq()
	r.Namespaces = []string{"b"}
	if g := f.request(tok, r, http.StatusOK); g.Name != names[1] {
		t.Errorf("merge at the limit %s", g.Name)
	}

	// An active grant does not count; nor does a released one.
	f.setGrantStatus(names[0], v1alpha1.GrantActive, nil)
	f.request(tok, fourth, http.StatusCreated)
	fifth := kubeReq()
	fifth.Namespaces = []string{"e"}
	wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, fifth), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if w := f.do(http.MethodDelete, apiv1.GrantPath(names[1]), tok, nil); w.Code != http.StatusAccepted {
		t.Fatalf("release: %d %s", w.Code, w.Body.String())
	}
	f.request(tok, fifth, http.StatusCreated)

	// The limit is per session.
	other := f.sessionPod("haynes-ops-1006-100001", "dev", 0)
	f.request(other, fifth, http.StatusCreated)
}

func TestGrantReleaseAuthenticatedUID(t *testing.T) {
	for _, privileged := range []string{tokHuman, tokClient} {
		t.Run(privileged, func(t *testing.T) {
			f := newFixture(t)
			const name = "haynes-ops-1006-100000"
			tok := f.sessionPod(name, "full", 0)
			req := apiv1.CreateGrantRequest{Type: "credential", Credential: "proxmox", Reason: "maintain a guest"}
			first := f.request(tok, req, http.StatusCreated)
			if w := f.do(http.MethodDelete, apiv1.GrantPath(first.Name), tok, nil); w.Code != http.StatusAccepted {
				t.Fatal("authenticated original owner could not release its credential grant")
			}
			old := f.request(tok, req, http.StatusCreated)
			requesterUID := f.grant(old.Name).Spec.Requester.SessionUID
			tok = replaceGrantSession(f, name)
			wantError(t, f.do(http.MethodDelete, apiv1.GrantPath(old.Name), tok, nil), http.StatusForbidden, apiv1.CodeForbidden)
			if f.grant(old.Name).Spec.Release {
				t.Fatal("replacement changed the predecessor's release state")
			}
			if w := f.do(http.MethodDelete, apiv1.GrantPath(old.Name), privileged, nil); w.Code != http.StatusAccepted {
				t.Fatal("authorized human or client could not release the predecessor's grant")
			}
			if g := f.grant(old.Name); !g.Spec.Release || g.Spec.Requester.SessionUID != requesterUID {
				t.Fatal("privileged release lost the recorded original requester UID")
			}
		})
	}
}

func TestGrantRelease(t *testing.T) {
	f := newFixture(t)
	f.srv.GrantApprovalURL = "https://dev-env.example.com/grants/"
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)
	other := f.sessionPod("haynes-ops-1006-100001", "dev", 0)
	g := f.request(tok, kubeReq(), http.StatusCreated)

	wantError(t, f.do(http.MethodDelete, apiv1.GrantPath(g.Name), other, nil), http.StatusForbidden, apiv1.CodeForbidden)
	if f.grant(g.Name).Spec.Release {
		t.Fatal("another session released it")
	}

	w := f.do(http.MethodDelete, apiv1.GrantPath(g.Name), tok, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("release: %d %s", w.Code, w.Body.String())
	}
	if v := decode[apiv1.Grant](t, w); !v.Release || v.ApprovalURL != "" {
		t.Errorf("release view %+v", v)
	}
	if !f.grant(g.Name).Spec.Release {
		t.Fatal("spec.release is not set")
	}
	// Again: already released, nothing to do.
	if w := f.do(http.MethodDelete, apiv1.GrantPath(g.Name), tok, nil); w.Code != http.StatusOK {
		t.Fatalf("second release: %d %s", w.Code, w.Body.String())
	}

	// Tom and clients may release any grant.
	r := kubeReq()
	r.Namespaces = []string{"x"}
	g2 := f.request(tok, r, http.StatusCreated)
	if w := f.do(http.MethodDelete, apiv1.GrantPath(g2.Name), tokHuman, nil); w.Code != http.StatusAccepted {
		t.Fatalf("Tom's release: %d %s", w.Code, w.Body.String())
	}
	r.Namespaces = []string{"y"}
	g3 := f.request(tok, r, http.StatusCreated)
	if w := f.do(http.MethodDelete, apiv1.GrantPath(g3.Name), tokClient, nil); w.Code != http.StatusAccepted {
		t.Fatalf("a client's release: %d %s", w.Code, w.Body.String())
	}

	// An ended grant is answered as it is.
	r.Namespaces = []string{"z"}
	g4 := f.request(tok, r, http.StatusCreated)
	f.setGrantStatus(g4.Name, v1alpha1.GrantDenied, nil)
	if w := f.do(http.MethodDelete, apiv1.GrantPath(g4.Name), tok, nil); w.Code != http.StatusOK || decode[apiv1.Grant](t, w).Release {
		t.Fatalf("an ended grant: %d %s", w.Code, w.Body.String())
	}
	if f.grant(g4.Name).Spec.Release {
		t.Error("an ended grant was patched")
	}

	wantError(t, f.do(http.MethodDelete, apiv1.GrantPath("grant-1006-000000-abcd"), tok, nil), http.StatusNotFound, apiv1.CodeNotFound)
}

func TestGrantListAndGet(t *testing.T) {
	f := newFixture(t)
	a := f.sessionPod("haynes-ops-1006-100000", "dev", 0)
	b := f.sessionPod("haynes-ops-1006-100001", "dev", 0)
	g1 := f.request(a, kubeReq(), http.StatusCreated)
	f.now = f.now.Add(time.Second)
	g2 := f.request(b, kubeReq(), http.StatusCreated)
	f.now = f.now.Add(time.Second)
	cred := apiv1.CreateGrantRequest{Type: "credential", Credential: "hw-ssh", Reason: "r"}
	g3 := f.request(a, cred, http.StatusCreated)
	f.setGrantStatus(g3.Name, v1alpha1.GrantActive, nil)

	names := func(q, tok string) []string {
		t.Helper()
		w := f.do(http.MethodGet, apiv1.GrantsPath+q, tok, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", q, w.Code, w.Body.String())
		}
		out := []string{}
		for _, g := range decode[apiv1.GrantList](t, w).Items {
			out = append(out, g.Name)
		}
		return out
	}
	eq := func(got []string, want ...string) bool { return strings.Join(got, ",") == strings.Join(want, ",") }
	if got := names("", tokHuman); !eq(got, g3.Name, g2.Name, g1.Name) {
		t.Errorf("all, newest first: %v", got)
	}
	if got := names("", b); !eq(got, g3.Name, g2.Name, g1.Name) {
		t.Errorf("a session sees every grant: %v", got)
	}
	if got := names("?session=haynes-ops-1006-100000", tokHuman); !eq(got, g3.Name, g1.Name) {
		t.Errorf("session: %v", got)
	}
	if got := names("?phase=Active", tokHuman); !eq(got, g3.Name) {
		t.Errorf("active: %v", got)
	}
	if got := names("?phase=Pending", tokHuman); !eq(got, g2.Name, g1.Name) {
		t.Errorf("no phase yet is Pending: %v", got)
	}
	if got := names("?mine=true", b); !eq(got, g2.Name) {
		t.Errorf("mine: %v", got)
	}
	if got := names("?mine=true", tokHuman); len(got) != 0 {
		t.Errorf("Tom has no grants of his own: %v", got)
	}
	for _, q := range []string{"?owner=me", "?phase=pending", "?mine=yes", "?session=a&session=b"} {
		wantError(t, f.do(http.MethodGet, apiv1.GrantsPath+q, tokHuman, nil), http.StatusBadRequest, apiv1.CodeBadRequest)
	}

	d := decode[apiv1.Grant](t, f.do(http.MethodGet, apiv1.GrantPath(g1.Name), tokClient, nil))
	if d.Session != "haynes-ops-1006-100000" || d.Reason == "" || d.Role != v1alpha1.RoleWorkloads || len(d.Namespaces) != 2 {
		t.Errorf("detail %+v", d)
	}
	wantError(t, f.do(http.MethodGet, apiv1.GrantPath("grant-1006-000000-abcd"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.GrantPath("haynes-ops-1006-100000"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	wantError(t, f.do(http.MethodGet, apiv1.GrantPath("grant-Not_A_Label"), tokHuman, nil), http.StatusNotFound, apiv1.CodeNotFound)
	w := f.do(http.MethodPut, apiv1.GrantPath(g1.Name), tokHuman, nil)
	wantError(t, w, http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
	if w.Header().Get("Allow") != "DELETE, GET" {
		t.Errorf("Allow %q", w.Header().Get("Allow"))
	}
}

func TestGrantSchemaRefusalIsMapped(t *testing.T) {
	f := newFixtureWith(t, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.AccessGrant); ok {
				return apierrors.NewInvalid(schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "AccessGrant"}, obj.GetName(), field.ErrorList{
					field.Invalid(field.NewPath("spec", "kube", "namespaces").Index(0), "dev-agents", "no grant reaches the dev-env namespaces (DESIGN-001 6.12)"),
					field.Invalid(field.NewPath("spec"), "", "ttl runs from 10m to 8h"),
					field.NotSupported(field.NewPath("spec", "credential", "name"), "x", []string{"proxmox", "hw-ssh"}),
				})
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	tok := f.sessionPod("haynes-ops-1006-100000", "dev", 0)
	e := wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, kubeReq()), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	if len(e.Fields) != 3 || e.Fields[0].Field != "namespaces[0]" || e.Fields[1].Field != "" || e.Fields[2].Field != "credential" {
		t.Errorf("fields %+v", e.Fields)
	}
	if !strings.Contains(e.Message, "no grant reaches the dev-env namespaces") || strings.Contains(e.Message, "; : ") {
		t.Errorf("message %q", e.Message)
	}
}

func TestGrantRequestField(t *testing.T) {
	for in, want := range map[string]string{
		"spec.kube.namespaces[0]":            "namespaces[0]",
		"spec.kube.role":                     "role",
		"spec.kube":                          "",
		"spec.egress":                        "",
		"spec.egress.fqdns[1]":               "fqdns[1]",
		"spec.egress.endpoints[0].namespace": "endpoints[0].namespace",
		"spec.credential.name":               "credential",
		"spec.ttl":                           "ttl",
		"spec":                               "",
		"metadata.name":                      "name",
	} {
		if got := grantRequestField(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestGrantName(t *testing.T) {
	at := time.Date(2026, 10, 5, 20, 25, 4, 0, time.FixedZone("EDT", -4*3600))
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		n, err := grantName(at)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^grant-1006-002504-[0-9a-f]{4}$`).MatchString(n) {
			t.Fatalf("name %q", n)
		}
		seen[n] = true
	}
	if len(seen) < 2 {
		t.Errorf("the random part does not vary: %v", seen)
	}
}
