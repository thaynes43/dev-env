package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestGrantListFiltersAndTable(t *testing.T) {
	h := newHarness(t)
	body := `{"items":[{"name":"grant-abc","phase":"Active","type":"kube","session":"task-1","ttl":"30m","expiresAt":"2026-10-17T18:00:00Z","reason":"read pods"}]}`
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Method != http.MethodGet {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusOK, body
		},
	})
	h.mustRun(ExitOK, "grant", "list", "--session", "task-1", "--phase", "Active", "--mine")
	contains(t, "stdout", h.stdout.String(), "NAME", "PHASE", "grant-abc", "Active", "kube", "task-1", "30m", "2026-10-17 18:00")
	q := h.api.requests()[0].query
	if q["session"][0] != "task-1" || q["phase"][0] != "Active" || q["mine"][0] != "true" {
		t.Fatalf("query %v", q)
	}
	h.stdout.Reset()
	h.mustRun(ExitOK, "grant", "list", "-o", "json")
	if !strings.Contains(h.stdout.String(), `"name": "grant-abc"`) {
		t.Fatalf("json %s", h.stdout.String())
	}
	h.mustRun(ExitUsage, "grant", "list", "--phase", "Later")
	h.mustRun(ExitUsage, "grant", "list", "extra")
	h.mustRun(ExitUsage, "grant", "install")
}

func TestGrantRequestPostsTheAsk(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusCreated, `{"name":"grant-1017-160000-ab12","phase":"Pending","type":"egress","session":"task-1","ttl":"30m","reason":"look up the registry","message":"waiting","approvalURL":"https://broker.example/grants/grant-1017-160000-ab12"}`
		},
	})
	h.mustRun(ExitOK, "grant", "request", "--type", "egress", "--reason", "look up the registry",
		"--fqdn", "ghcr.io", "--endpoint", "database:app=registry", "--port", "443", "--port", "53/udp", "--ttl", "30m")
	contains(t, "stdout", h.stdout.String(), "requested", "name: grant-1017-160000-ab12", "phase: Pending", "message: waiting", "approval: https://broker.example/grants/grant-1017-160000-ab12")
	var got apiv1.CreateGrantRequest
	if err := json.Unmarshal(h.api.requests()[0].body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "egress" || got.Reason != "look up the registry" || got.TTL != "30m" || len(got.FQDNs) != 1 || got.FQDNs[0] != "ghcr.io" {
		t.Fatalf("body %#v", got)
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0].Namespace != "database" || got.Endpoints[0].MatchLabels["app"] != "registry" {
		t.Fatalf("endpoint %#v", got.Endpoints)
	}
	if len(got.Ports) != 2 || got.Ports[0].Port != 443 || got.Ports[0].Protocol != "" || got.Ports[1].Port != 53 || got.Ports[1].Protocol != "UDP" {
		t.Fatalf("ports %#v", got.Ports)
	}
	before := len(h.api.requests())
	h.mustRun(ExitUsage, "grant", "request", "--type", "kube")
	h.mustRun(ExitUsage, "grant", "request", "--type", "egress", "--reason", "x", "--port", "nope")
	h.mustRun(ExitUsage, "grant", "request", "--type", "egress", "--reason", "x", "--endpoint", ":app=a")
	if len(h.api.requests()) != before {
		t.Fatal("a refused request reached the API")
	}
}

func TestGrantReleaseAsksTheBroker(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Method != http.MethodDelete {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusAccepted, `{"name":"grant-abc","phase":"Active","type":"kube","session":"task-1","ttl":"30m","reason":"read pods","release":true}`
		},
	})
	h.mustRun(ExitOK, "grant", "release", "grant-abc")
	contains(t, "stdout", h.stdout.String(), "release requested", "name: grant-abc", "phase: Active")
	if len(h.api.requests()) != 1 || h.api.requests()[0].method != http.MethodDelete || len(h.api.requests()[0].body) != 0 {
		t.Fatalf("requests %+v", h.api.requests())
	}
	h.mustRun(ExitUsage, "grant", "release", "not-a-grant")
	if len(h.api.requests()) != 1 {
		t.Fatal("a refused release reached the API")
	}
}

func TestGrantReleaseTextFollowsTheRecord(t *testing.T) {
	h := newHarness(t)
	call := 0
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			call++
			if call == 1 {
				return http.StatusOK, `{"name":"grant-abc","phase":"Denied","type":"kube","session":"task-1","ttl":"30m","reason":"read pods"}`
			}
			return http.StatusOK, `{"name":"grant-abc","phase":"Released","type":"kube","session":"task-1","ttl":"30m","reason":"read pods","release":true}`
		},
	})
	h.mustRun(ExitOK, "grant", "release", "grant-abc")
	if strings.Contains(h.stdout.String(), "already released") || !strings.Contains(h.stdout.String(), "already ended") {
		t.Fatalf("denied grant: %s", h.stdout.String())
	}
	h.stdout.Reset()
	h.mustRun(ExitOK, "grant", "release", "grant-abc")
	contains(t, "stdout", h.stdout.String(), "already released", "phase: Released")
}

func TestGrantShowOneName(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			return http.StatusOK, `{"name":"grant-abc","phase":"Pending","type":"egress","session":"task-1","ttl":"15m","reason":"lookup"}`
		},
	})
	h.mustRun(ExitOK, "grant", "show", "grant-abc")
	contains(t, "stdout", h.stdout.String(), "name: grant-abc", "phase: Pending", "type: egress", "reason: lookup")
	if len(h.api.requests()) != 1 || h.api.requests()[0].path != "/v1/grants/grant-abc" {
		t.Fatalf("requests %+v", h.api.requests())
	}
	h.mustRun(ExitUsage, "grant", "show", "../secret")
	h.mustRun(ExitUsage, "grant", "show")
	if len(h.api.requests()) != 1 {
		t.Fatal("a refused name reached the API")
	}
}

func TestGrantNameAndPhase(t *testing.T) {
	if !validGrantCLIName("grant-abc") || validGrantCLIName("grant-") || validGrantCLIName("Grant-abc") || validGrantCLIName("grant-abc/extra") {
		t.Fatal("grant name check")
	}
	if !grantPhaseKnown("Active") || grantPhaseKnown("Later") {
		t.Fatal("phase check")
	}
}
