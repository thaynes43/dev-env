package apiserver

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// namespaceOperatorReq is DESIGN-042 A2's request, its Secrets out of order.
func namespaceOperatorReq(secrets ...string) apiv1.CreateGrantRequest {
	return apiv1.CreateGrantRequest{
		Type: "kube", Role: v1alpha1.RoleNamespaceOperator, Namespaces: []string{"downloads"}, Secrets: secrets,
		TTL: "30m", Reason: "apply the saved MAM cookie to Prowlarr indexer 17",
	}
}

// The named Secrets go from the request to the spec, the scope digest and
// every view, sorted, and never dropped (DESIGN-042 5.1): the approver sees
// exactly the names the digest binds.
func TestGrantSecretsRoundTrip(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)
	want := []string{"myanonamouse-secret", "prowlarr-secret"}

	created := f.request(tok, namespaceOperatorReq("prowlarr-secret", "myanonamouse-secret"), http.StatusCreated)
	live := f.grant(created.Name)
	if !slices.Equal(live.Spec.Kube.Secrets, want) {
		t.Fatalf("spec secrets %v, want %v sorted", live.Spec.Kube.Secrets, want)
	}
	if !slices.Equal(created.Secrets, want) {
		t.Fatalf("create view secrets %v, want %v", created.Secrets, want)
	}
	if created.ScopeDigest == "" || created.ScopeDigest != grantattest.ScopeDigest(live) {
		t.Fatalf("view digest %q, stored grant's %q", created.ScopeDigest, grantattest.ScopeDigest(live))
	}
	shown := decode[apiv1.Grant](t, f.do(http.MethodGet, apiv1.GrantPath(created.Name), tok, nil))
	if !slices.Equal(shown.Secrets, want) || shown.ScopeDigest != created.ScopeDigest {
		t.Fatalf("show: secrets %v digest %q", shown.Secrets, shown.ScopeDigest)
	}
	listed := decode[apiv1.GrantList](t, f.do(http.MethodGet, apiv1.GrantsPath+"?mine=true", tok, nil))
	if len(listed.Items) != 1 || !slices.Equal(listed.Items[0].Secrets, want) || listed.Items[0].ScopeDigest != created.ScopeDigest {
		t.Fatalf("list: %+v", listed.Items)
	}

	// The same names in another order are the same request: it merges.
	f.now = f.now.Add(time.Second)
	if again := f.request(tok, namespaceOperatorReq("myanonamouse-secret", "prowlarr-secret"), http.StatusOK); again.Name != created.Name {
		t.Fatalf("same Secrets reordered made %s, want a merge into %s", again.Name, created.Name)
	}
	// A changed name is another request, with another digest; it never merges.
	f.now = f.now.Add(time.Second)
	changed := f.request(tok, namespaceOperatorReq("myanonamouse-secret", "qbittorrent-secret"), http.StatusCreated)
	if changed.Name == created.Name || changed.ScopeDigest == created.ScopeDigest {
		t.Fatalf("a changed Secret name: grant %s digest %q, the first %s %q", changed.Name, changed.ScopeDigest, created.Name, created.ScopeDigest)
	}
	if !slices.Equal(changed.Secrets, []string{"myanonamouse-secret", "qbittorrent-secret"}) {
		t.Fatalf("changed view secrets %v", changed.Secrets)
	}
}

// A decision bound to the old digest is refused with 409 once the names differ.
// This uses the attestation route (mode (a)), which binds ScopeDigest; S2's
// delegate decision binds the same digest the same way.
func TestGrantSecretsDigestBindsTheNames(t *testing.T) {
	f := newFixture(t)
	f.srv.GrantAttestation = true
	f.h = f.srv.Handler()
	tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)
	first := f.request(tok, namespaceOperatorReq("prowlarr-secret", "myanonamouse-secret"), http.StatusCreated)
	f.now = f.now.Add(time.Second)
	second := f.request(tok, namespaceOperatorReq("myanonamouse-secret", "qbittorrent-secret"), http.StatusCreated)
	withUID(f, first.Name)
	withUID(f, second.Name)

	stale := body(first) // the digest the approver saw for the first list
	wantError(t, f.do(http.MethodPost, apiv1.GrantAttestationPath(second.Name), tok, stale), http.StatusConflict, apiv1.CodeConflict)
	if _, ok := f.grant(second.Name).Annotations[grantattest.Annotation]; ok {
		t.Fatal("an attestation for other Secret names was written")
	}
	if w := f.do(http.MethodPost, apiv1.GrantAttestationPath(second.Name), tok, body(second)); w.Code != http.StatusCreated {
		t.Fatalf("attest with the grant's own digest: %d %s", w.Code, w.Body.String())
	}
}

func TestGrantSecretsRefusals(t *testing.T) {
	seventeen := make([]string, 17)
	for i := range seventeen {
		seventeen[i] = "app-" + string(rune('a'+i)) + "-secret"
	}
	for _, c := range []struct {
		name string
		req  apiv1.CreateGrantRequest
		want string // a part of the secrets field's message
	}{
		{"no Secret", namespaceOperatorReq(), "required for role"},
		{"a duplicate", namespaceOperatorReq("prowlarr-secret", "prowlarr-secret"), "named twice"},
		{"seventeen", namespaceOperatorReq(seventeen...), "at most 16"},
		{"a wildcard", namespaceOperatorReq("*"), "not a Secret name"},
		{"a pattern", namespaceOperatorReq("prowlarr-*"), "not a Secret name"},
		{"a namespace-qualified name", namespaceOperatorReq("downloads/prowlarr-secret"), "not a Secret name"},
		{"upper case", namespaceOperatorReq("Prowlarr"), "not a Secret name"},
		{"254 characters", namespaceOperatorReq(strings.Repeat("a", 254)), "not a Secret name"},
		{"on workloads", func() apiv1.CreateGrantRequest {
			r := kubeReq()
			r.Secrets = []string{"prowlarr-secret"}
			return r
		}(), "only for role"},
		{"on nodes", apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleNodes, Secrets: []string{"prowlarr-secret"}, Reason: "drain"}, "only for role"},
		{"on break-glass", apiv1.CreateGrantRequest{Type: "breakglass", Secrets: []string{"prowlarr-secret"}, Reason: "re-clone"}, "not for type breakglass"},
		{"on egress", apiv1.CreateGrantRequest{Type: "egress", CIDRs: []string{"192.168.40.21/32"}, Ports: []apiv1.GrantPort{{Port: 443}}, Secrets: []string{"prowlarr-secret"}, Reason: "reach"}, "not for type egress"},
		{"on a credential", apiv1.CreateGrantRequest{Type: "credential", Credential: "proxmox", Secrets: []string{"prowlarr-secret"}, Reason: "mint"}, "not for type credential"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)
			e := wantError(t, f.do(http.MethodPost, apiv1.GrantsPath, tok, c.req), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
			found := false
			for _, fe := range e.Fields {
				found = found || (fe.Field == "secrets" && strings.Contains(fe.Message, c.want))
			}
			if !found {
				t.Fatalf("no secrets field containing %q: %+v", c.want, e)
			}
			if n := f.grantCount(); n != 0 {
				t.Fatalf("%d grants made by a refused request", n)
			}
		})
	}
}

// Other grants carry no secrets field at all: their wire shape is unchanged.
func TestGrantSecretsAbsentElsewhere(t *testing.T) {
	f := newFixture(t)
	tok := f.sessionPod("haynes-ops-1006-100000", "full", 0)
	w := f.do(http.MethodPost, apiv1.GrantsPath, tok, kubeReq())
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"secrets"`) {
		t.Fatalf("a workloads grant's view: %d %s", w.Code, w.Body.String())
	}
}
