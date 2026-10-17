package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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
	h.mustRun(ExitOK, "grant", "request", "--no-wait", "--type", "egress", "--reason", "look up the registry",
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

func TestGrantRequestWaitsForTheAnswer(t *testing.T) {
	h := newHarness(t)
	gets := 0
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			return http.StatusCreated, `{"name":"grant-abc","phase":"Pending","type":"kube","session":"task-1","ttl":"30m","reason":"read pods","approvalURL":"https://broker.example/g"}`
		},
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			gets++
			if gets == 1 {
				return http.StatusOK, `{"name":"grant-abc","phase":"Pending","type":"kube","session":"task-1","ttl":"30m","reason":"read pods","approvalURL":"https://broker.example/g"}`
			}
			return http.StatusOK, `{"name":"grant-abc","phase":"Active","type":"kube","session":"task-1","ttl":"30m","reason":"read pods"}`
		},
	})
	h.mustRun(ExitOK, "grant", "request", "--type", "kube", "--role", "dev-env-grant-workloads", "--reason", "read pods")
	contains(t, "stdout", h.stdout.String(), "requested", "approval: https://broker.example/g", "answered", "phase: Active")
	if gets != 2 || len(h.sleeps) != 2 || h.sleeps[0] != grantAnswerPoll {
		t.Fatalf("gets %d sleeps %v", gets, h.sleeps)
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

func TestBreakglassPostsTheAsk(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusCreated, `{"name":"grant-1017-170000-ab12","phase":"Pending","type":"breakglass","session":"task-1","ttl":"30m","reason":"the node is stuck","approvalURL":"https://broker.example/bg"}`
		},
	})
	h.mustRun(ExitOK, "breakglass", "--reason", "the node is stuck", "--ttl", "20m")
	contains(t, "stdout", h.stdout.String(), "requested", "type: breakglass", "approval: https://broker.example/bg")
	var got apiv1.CreateGrantRequest
	if err := json.Unmarshal(h.api.requests()[0].body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "breakglass" || got.Reason != "the node is stuck" || got.TTL != "20m" || got.Role != "" {
		t.Fatalf("body %#v", got)
	}
	before := len(h.api.requests())
	h.mustRun(ExitUsage, "breakglass")
	h.mustRun(ExitUsage, "breakglass", "--reason", "   ")
	if len(h.api.requests()) != before {
		t.Fatal("a refused breakglass reached the API")
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

func TestGrantUseSelectsThePodContext(t *testing.T) {
	h := newHarness(t)
	h.agentd = func(argv []string) (int, string, string) {
		return 0, "kubectl now uses " + argv[len(argv)-1] + "\n", ""
	}
	h.mustRun(ExitOK, "grant", "use", "grant-abc")
	contains(t, "stdout", h.stdout.String(), "kubectl now uses grant-abc")
	if len(h.ran) != 1 || h.ran[0] != "/usr/local/bin/agentd ctl grant-use grant-abc" {
		t.Fatalf("ran %q", h.ran)
	}
	if len(h.api.requests()) != 0 {
		t.Fatal("grant use called the API")
	}
	h.mustRun(ExitOK, "grant", "use", protocol.DefaultContext)
	h.mustRun(ExitUsage, "grant", "use", "Grant-abc")
	h.mustRun(ExitUsage, "grant", "use")
	if len(h.ran) != 2 {
		t.Fatal("a refused name ran agentd")
	}
	h.agentd = func(argv []string) (int, string, string) {
		return 1, "", "agentd: grant-use: grant grant-abc is not installed in this pod, or it has expired"
	}
	h.mustRun(ExitFailed, "grant", "use", "grant-abc")
	contains(t, "stderr", h.stderr.String(), "not installed")
}
