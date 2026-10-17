package agentrun

import (
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
	h.mustRun(ExitUsage, "grant", "request", "kube")
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
