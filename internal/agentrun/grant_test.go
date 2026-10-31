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
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
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
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
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
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
			return http.StatusCreated, `{"name":"grant-abc","phase":"Pending","type":"kube","session":"task-1","ttl":"30m","reason":"read pods","approvalURL":"https://broker.example/g"}`
		},
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
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

func TestGrantRequestReportsARefusal(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
			return http.StatusCreated, `{"name":"grant-abc","phase":"Pending","type":"kube","ttl":"30m","reason":"read pods"}`
		},
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
			return http.StatusOK, `{"name":"grant-abc","phase":"Denied","type":"kube","ttl":"30m","reason":"read pods"}`
		},
	})
	h.mustRun(ExitFailed, "grant", "request", "--type", "kube", "--role", "dev-env-grant-workloads", "--reason", "read pods")
	contains(t, "stdout", h.stdout.String(), "answered", "phase: Denied")
	contains(t, "stderr", h.stderr.String(), "grant grant-abc was Denied")
}

func TestGrantRequestStopsAtThirtyMinutes(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
			return http.StatusCreated, `{"name":"grant-abc","phase":"Pending","type":"kube","ttl":"30m","reason":"read pods"}`
		},
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
			return http.StatusOK, `{"name":"grant-abc","phase":"Pending","type":"kube","ttl":"30m","reason":"read pods"}`
		},
	})
	h.mustRun(ExitFailed, "grant", "request", "--type", "kube", "--role", "dev-env-grant-workloads", "--reason", "read pods")
	contains(t, "stderr", h.stderr.String(), "still pending after 30m0s")
	if len(h.sleeps) != int(grantAnswerWait/grantAnswerPoll) {
		t.Fatalf("sleeps %d", len(h.sleeps))
	}
}

func TestGrantReleaseAsksTheBroker(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
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
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
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
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
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

// grant attest (D-88): the claim is the session's own; the CLI shows what it
// attests before it posts and sends only the claim and the digest it showed.
func attestRoutes(t *testing.T, h *harness, postStatus int, postBody string, seen *string) {
	t.Helper()
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
			if r.Method != http.MethodGet {
				t.Errorf("method %s on the grant", r.Method)
			}
			return http.StatusOK, `{"name":"grant-abc","phase":"Pending","type":"kube","role":"dev-env-grant-workloads","namespaces":["frontend","database"],"ttl":"1h0m0s","reason":"patch","scopeDigest":"DIGEST0123456789012345678901234567890123456"}`
		},
		apiv1.GrantAttestationPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Header.Get("X-Dev-Env-Grant-Task-Claims") != "" {
				t.Error("non-show route negotiated claims")
			}
			if r.Method != http.MethodPost {
				t.Errorf("method %s on the attestation", r.Method)
			}
			if seen != nil {
				*seen = h.stdout.String() // what was already printed when the POST arrived
			}
			return postStatus, postBody
		},
	})
}

func TestGrantAttestUsageErrorsSendNothing(t *testing.T) {
	h := newHarness(t)
	attestRoutes(t, h, http.StatusCreated, `{}`, nil)
	for _, args := range [][]string{
		{"grant", "attest"},
		{"grant", "attest", "grant-abc", "--ref", "m1"},                                         // no --approved-by
		{"grant", "attest", "grant-abc", "--approved-by", "user"},                               // no --ref
		{"grant", "attest", "grant-abc", "--approved-by", "tom", "--ref", "m1"},                 // not user or coordinator:
		{"grant", "attest", "grant-abc", "--approved-by", "user", "--ref", "m1", "extra"},       // extra argument
		{"grant", "attest", "not-a-grant", "--approved-by", "user", "--ref", "m1"},              // bad name
		{"grant", "attest", "grant-abc", "--approved-by", "coordinator", "--ref", "m1"},         // coordinator needs a reference
		{"grant", "attest", "grant-abc", "--approved-by", "user", "--ref", "m1", "--nope", "x"}, // unknown flag
	} {
		h.mustRun(ExitUsage, args...)
	}
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("a refused command reached the API %d times", n)
	}
}

func TestGrantAttestShowsTheScopeThenPostsTheClaim(t *testing.T) {
	h := newHarness(t)
	var shown string
	attestRoutes(t, h, http.StatusCreated, `{"name":"grant-abc","phase":"Pending","type":"kube","session":"task-1","ttl":"1h0m0s","reason":"patch","attestation":{"session":"task-1","approvedBy":"coordinator:meatball","ref":"msg-7","attestedAt":"2026-10-28T01:00:00Z"}}`, &shown)
	h.mustRun(ExitOK, "grant", "attest", "grant-abc", "--approved-by", "coordinator:meatball", "--ref", "msg-7")
	// Before the POST the command printed what it attests, with the digest and the wording.
	contains(t, "printed before posting", shown, "attesting grant-abc", "type kube", "role dev-env-grant-workloads", "namespaces frontend,database", "ttl 1h0m0s",
		"scope digest DIGEST0123456789012345678901234567890123456", "coordinator:meatball approved this exact grant", "not verified human identity")
	contains(t, "stdout", h.stdout.String(), "attested", "name: grant-abc", "attestation: session/task-1 claims coordinator:meatball approved (ref msg-7)", "not verified human identity")
	reqs := h.api.requests()
	if len(reqs) != 2 || reqs[0].method != http.MethodGet || reqs[1].method != http.MethodPost || reqs[1].path != apiv1.GrantAttestationPath("grant-abc") {
		t.Fatalf("requests %+v", reqs)
	}
	var body map[string]string
	if err := json.Unmarshal(reqs[1].body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"approvedBy": "coordinator:meatball", "ref": "msg-7", "scopeDigest": "DIGEST0123456789012345678901234567890123456"}
	if len(body) != len(want) || body["approvedBy"] != want["approvedBy"] || body["ref"] != want["ref"] || body["scopeDigest"] != want["scopeDigest"] {
		t.Fatalf("body %v, want exactly %v (no attestor or actor fields)", body, want)
	}
}

func TestGrantAttestMapsTheRoutesAnswers(t *testing.T) {
	errBody := func(code, msg string) string {
		b, _ := json.Marshal(apiv1.ErrorResponse{Error: apiv1.Error{Code: code, Message: msg}})
		return string(b)
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		exit   int
		out    string
	}{
		{"200 identical retry", http.StatusOK, `{"name":"grant-abc","phase":"Pending","type":"kube","ttl":"1h0m0s","reason":"patch","attestation":{"session":"task-1","approvedBy":"user","ref":"m1"}}`, ExitOK, "attested"},
		{"400 bad request", http.StatusBadRequest, errBody(apiv1.CodeBadRequest, "unknown field"), ExitFailed, "unknown field"},
		{"403 not your grant", http.StatusForbidden, errBody(apiv1.CodeForbidden, "grant grant-abc belongs to another requesting session"), ExitAuth, "another requesting session"},
		{"409 one per grant", http.StatusConflict, errBody(apiv1.CodeConflict, "grant grant-abc already carries an attestation; there is one per grant"), ExitFailed, "already carries an attestation"},
		{"404 route off", http.StatusNotFound, errBody(apiv1.CodeNotFound, "no such route"), ExitNotFound, "no such route"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			attestRoutes(t, h, tc.status, tc.body, nil)
			h.mustRun(tc.exit, "grant", "attest", "grant-abc", "--approved-by", "user", "--ref", "m1")
			if tc.exit == ExitOK {
				contains(t, "stdout", h.stdout.String(), tc.out)
			} else {
				contains(t, "stderr", h.stderr.String(), tc.out)
			}
		})
	}
	// An API that returns no digest cannot be attested, and nothing is posted.
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-abc"): func(*http.Request) (int, string) {
			return http.StatusOK, `{"name":"grant-abc","phase":"Pending","type":"kube","ttl":"1h0m0s","reason":"patch"}`
		},
	})
	h.mustRun(ExitFailed, "grant", "attest", "grant-abc", "--approved-by", "user", "--ref", "m1")
	contains(t, "stderr", h.stderr.String(), "scope digest")
	if len(h.api.requests()) != 1 {
		t.Fatalf("requests %+v", h.api.requests())
	}
}

// hw-ssh (D-89): the notice is keyed on the grant's credential, in the printout
// and in the MCP text; other grants carry none; the wire view is unchanged.
func TestHWSSHNoticeIsPrintedOnlyForTheHWSSHCredential(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-ssh"): func(*http.Request) (int, string) {
			return http.StatusOK, `{"name":"grant-ssh","phase":"Active","type":"credential","credential":"hw-ssh","session":"task-1","ttl":"1h0m0s","reason":"reboot a node"}`
		},
		apiv1.GrantPath("grant-pve"): func(*http.Request) (int, string) {
			return http.StatusOK, `{"name":"grant-pve","phase":"Active","type":"credential","credential":"proxmox","session":"task-1","ttl":"1h0m0s","reason":"api"}`
		},
	})
	h.mustRun(ExitOK, "grant", "show", "grant-ssh")
	contains(t, "stdout", h.stdout.String(), "hw-ssh notice", "ONE shared ed25519 key", "COPY the key", "not containment and not revocation", "no cryptographic expiry", "rotating the key on every host", "qm is root-equivalent")
	h.mustRun(ExitOK, "grant", "show", "grant-pve")
	if strings.Contains(h.stdout.String(), "hw-ssh notice") {
		t.Fatal("a proxmox grant carries the hw-ssh notice")
	}
	if got := noticeFor("[]"); got != "[]" {
		t.Fatalf("empty list: %q", got)
	}
}

// An empty coordinator reference passes the CLI's light check and the server
// refuses it with 422; the CLI reports that plainly and exits 1.
func TestGrantAttestEmptyCoordinatorRefIsRefusedByTheServer(t *testing.T) {
	h := newHarness(t)
	b, _ := json.Marshal(apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeInvalid, Message: "invalid request", Fields: []apiv1.FieldError{{Field: "approvedBy", Message: "approvedBy is user, or coordinator:<ref> with a reference of letters, digits, '.', '_' or '-'"}}}})
	attestRoutes(t, h, http.StatusUnprocessableEntity, string(b), nil)
	h.mustRun(ExitFailed, "grant", "attest", "grant-abc", "--approved-by", "coordinator:", "--ref", "m1")
	contains(t, "stderr", h.stderr.String(), "approvedBy")
	reqs := h.api.requests()
	var body map[string]string
	if len(reqs) != 2 || json.Unmarshal(reqs[1].body, &body) != nil || body["approvedBy"] != "coordinator:" {
		t.Fatalf("requests %+v", reqs)
	}
}
