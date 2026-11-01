package agentrun

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const grantStatusTaskClaimsDescription = "Include requester canonical task-claim metadata for a named maintenance grant. Identity is not approval or transfer permission."

func TestMCPGrantStatusTaskClaimsSchema(t *testing.T) {
	h := newHarness(t)
	h.vars[envTokenFile] = filepath.Join(h.dir, "missing-token")
	h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	h.mustRun(ExitOK, "mcp")
	var reply struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Schema      map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Tools) != 9 || len(h.api.requests()) != 0 {
		t.Fatalf("tools/list changed the tool set or called the API: %s", h.stdout.String())
	}
	var sawGrantStatus bool
	for _, tool := range reply.Result.Tools {
		props, _ := tool.Schema["properties"].(map[string]any)
		if tool.Name != "grant_status" {
			if _, ok := props["includeTaskClaims"]; ok {
				t.Errorf("%s schema accepted includeTaskClaims", tool.Name)
			}
			continue
		}
		sawGrantStatus = true
		if tool.Description != "Read one grant by name, or list grants. Filters are session, phase and mine. This does not change a grant." {
			t.Fatalf("description %q", tool.Description)
		}
		if tool.Schema["required"] != nil {
			t.Fatalf("required %v", tool.Schema["required"])
		}
		for _, key := range []string{"name", "session", "phase"} {
			if props[key].(map[string]any)["type"] != "string" {
				t.Errorf("%s schema %v", key, props[key])
			}
		}
		if props["mine"].(map[string]any)["type"] != "boolean" {
			t.Fatalf("mine %v", props["mine"])
		}
		field, ok := props["includeTaskClaims"].(map[string]any)
		if !ok || field["type"] != "boolean" || field["default"] != false || field["description"] != grantStatusTaskClaimsDescription {
			t.Fatalf("includeTaskClaims schema %v", props["includeTaskClaims"])
		}
	}
	if !sawGrantStatus {
		t.Fatal("missing grant_status")
	}
}

func TestMCPGrantStatusTaskClaimsDefaultShape(t *testing.T) {
	h := newHarness(t)
	maint := `{"name":"grant-maint","phase":"Active","type":"maintenance","session":"requester-session","ttl":"1h","reason":"drain","approvalURL":"https://broker.example/maint","approvedBy":"tom"}`
	hw := `{"name":"grant-hw","phase":"Pending","type":"credential","credential":"hw-ssh","session":"task-1","ttl":"1h","reason":"look at a node","approvalURL":"https://broker.example/hw"}`
	list := `{"items":[{"name":"grant-maint","phase":"Active","type":"maintenance","session":"requester-session","ttl":"1h","reason":"drain"},{"name":"grant-hw","phase":"Active","type":"credential","credential":"hw-ssh","session":"task-1","ttl":"30m","reason":"look"}]}`
	var headers []string
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
			recordGrantRead(t, r, &headers)
			return http.StatusOK, maint
		},
		apiv1.GrantPath("grant-hw"): func(r *http.Request) (int, string) {
			recordGrantRead(t, r, &headers)
			return http.StatusOK, hw
		},
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Method != http.MethodGet || r.URL.Query().Get("session") != "task-1" || r.URL.Query().Get("phase") != "Active" || r.URL.Query().Get("mine") != "true" {
				t.Errorf("list %s %s", r.Method, r.URL.RawQuery)
			}
			headers = append(headers, r.Header.Get(apiv1.GrantTaskClaimsHeader))
			return http.StatusOK, list
		},
	})
	omitted, omittedErr := mcpToolText(t, h, "grant_status", `{"name":"grant-maint"}`)
	explicit, explicitErr := mcpToolText(t, h, "grant_status", `{"name":"grant-maint","includeTaskClaims":false}`)
	filtered, filteredErr := mcpToolText(t, h, "grant_status", `{"name":"grant-maint","phase":"Denied","mine":true}`)
	_, hwWire := grantWire(t, hw, false)
	hwText, hwErr := mcpToolText(t, h, "grant_status", `{"name":"grant-hw"}`)
	var listed apiv1.GrantList
	if err := json.Unmarshal([]byte(list), &listed); err != nil {
		t.Fatal(err)
	}
	listWire, err := json.MarshalIndent(listed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	listText, listErr := mcpToolText(t, h, "grant_status", `{"session":"task-1","phase":"Active","mine":true}`)
	listFalse, listFalseErr := mcpToolText(t, h, "grant_status", `{"session":"task-1","phase":"Active","mine":true,"includeTaskClaims":false}`)
	_, maintWire := grantWire(t, maint, false)
	for _, got := range []struct {
		name string
		text string
		err  bool
		want string
	}{
		{"omitted", omitted, omittedErr, maintWire},
		{"explicit false", explicit, explicitErr, maintWire},
		{"named filters stay on the grant read", filtered, filteredErr, maintWire},
		{"hw-ssh", hwText, hwErr, hwWire + "\n\n" + hwsshNotice},
		{"list", listText, listErr, string(listWire) + "\n\n" + hwsshNotice},
		{"list false", listFalse, listFalseErr, string(listWire) + "\n\n" + hwsshNotice},
	} {
		if got.err || got.text != got.want || strings.Contains(got.text, "taskClaims") || strings.Contains(got.text, "NotReported") {
			t.Fatalf("%s error %v\ngot\n%s\nwant\n%s", got.name, got.err, got.text, got.want)
		}
	}
	if len(h.api.requests()) != 6 || len(headers) != 6 {
		t.Fatalf("requests %d headers %v", len(h.api.requests()), headers)
	}
	for _, header := range headers {
		if header != "" {
			t.Fatalf("default read negotiated claims: %q", header)
		}
	}
}

func TestMCPGrantStatusIncludeTaskClaimsRequiresNamedStatus(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			t.Errorf("API %s %s", r.Method, r.URL.RequestURI())
			return http.StatusCreated, `{"name":"grant-abc","type":"kube"}`
		},
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			t.Errorf("API %s %s", r.Method, r.URL.RequestURI())
			return http.StatusOK, `{"name":"grant-abc","type":"kube"}`
		},
		apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
			t.Errorf("API %s %s", r.Method, r.URL.RequestURI())
			return http.StatusOK, `{"name":"grant-maint","type":"maintenance"}`
		},
	})
	for _, tc := range []struct {
		tool, args, want string
		partial          bool
	}{
		{tool: "grant_status", args: `{"includeTaskClaims":true}`, want: "includeTaskClaims requires a named grant_status"},
		{tool: "grant_status", args: `{"includeTaskClaims":true,"session":"task-1","phase":"Active","mine":true}`, want: "includeTaskClaims requires a named grant_status"},
		{tool: "grant_status", args: `{"name":"not-a-grant","includeTaskClaims":true}`, want: "is not a grant name", partial: true},
		{tool: "grant_status", args: `{"name":"grant-maint","includeTaskClaims":"yes"}`, want: "includeTaskClaims must be a boolean"},
		{tool: "grant_status", args: `{"name":"grant-maint","includeTaskClaims":null}`, want: "includeTaskClaims must be a boolean"},
		{tool: "request_access", args: `{"type":"kube","reason":"read pods","includeTaskClaims":true}`, want: "includeTaskClaims requires a named grant_status"},
		{tool: "release_access", args: `{"name":"grant-abc","includeTaskClaims":true}`, want: "includeTaskClaims requires a named grant_status"},
		{tool: "decide_grant", args: `{"name":"grant-abc","grantUID":"uid","digest":"digest","ref":"msg-1","approve":false,"includeTaskClaims":true}`, want: "includeTaskClaims requires a named grant_status"},
		{tool: "hardware_intent", args: `{"name":"grant-abc","grantUID":"uid","digest":"digest","operation":"read","host":"pve","device":"gpu","firmware":"fw","tomRef":"msg","includeTaskClaims":true}`, want: "includeTaskClaims requires a named grant_status"},
		{tool: "ask_status", args: `{"id":"ask-0123456789abcdef","includeTaskClaims":true}`, want: "invalid ask tool arguments", partial: true},
	} {
		before := len(h.api.requests())
		text, isErr := mcpToolText(t, h, tc.tool, tc.args)
		match := text == tc.want
		if tc.partial {
			match = strings.Contains(text, tc.want)
		}
		if !isErr || !match || len(h.api.requests()) != before {
			t.Fatalf("%s %s: error %v requests %d text %q", tc.tool, tc.args, isErr, len(h.api.requests())-before, text)
		}
	}
}

func TestMCPGrantStatusIncludeTaskClaimsMaintenance(t *testing.T) {
	h := newHarness(t)
	grant := apiv1.Grant{
		Name: "grant-maint", Phase: "Active", Type: "maintenance", Session: "requester-session", TTL: "1h", Reason: "drain",
		ApprovalURL: "https://broker.example/maint", ApprovedBy: "tom", UID: "grant-uid",
		Maintenance: &apiv1.MaintenanceTask{Node: "node-a", NodeUID: "node-uid", Resource: "not-the-claim", Operation: "issue:999"},
		TaskClaims: &apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{
			claimObservation("claim-one", "canonical-uid", "issue:322"),
			claimObservation("claim-two", "second-uid", "issue:400"),
		}},
	}
	body, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	var headers []string
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
			recordGrantRead(t, r, &headers)
			return http.StatusOK, string(body)
		},
	})
	text, isErr := mcpToolText(t, h, "grant_status", `{"name":"grant-maint","includeTaskClaims":true,"phase":"Denied","mine":true}`)
	_, want := grantWire(t, string(body), true)
	if isErr || text != want || len(h.api.requests()) != 1 || headers[0] != apiv1.GrantTaskClaimsV1 {
		t.Fatalf("mcp error %v requests %d header %v\n%s", isErr, len(h.api.requests()), headers, text)
	}
	var got apiv1.Grant
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.ApprovalURL != grant.ApprovalURL || got.ApprovedBy != "tom" || got.Phase != "Active" || got.Maintenance == nil || got.Maintenance.Operation != "issue:999" || got.Maintenance.Resource != "not-the-claim" {
		t.Fatalf("grant fields changed: %+v", got)
	}
	if got.TaskClaims == nil || got.TaskClaims.State != "Available" || len(got.TaskClaims.Items) != 2 {
		t.Fatalf("claims %#v", got.TaskClaims)
	}
	first, second := got.TaskClaims.Items[0], got.TaskClaims.Items[1]
	if first.Owner.Namespace != "dev-agents" || first.Owner.Name != "requester-session" || first.Owner.UID != "session-uid" || first.Name != "claim-one" || first.UID != "canonical-uid" {
		t.Fatalf("canonical claim %+v", first)
	}
	if first.Identity.Project != "sample" || first.Identity.Repository != "dev-env" || first.Identity.TaskKey != "issue:322" || second.Name != "claim-two" || second.UID != "second-uid" || second.Identity.TaskKey != "issue:400" {
		t.Fatalf("identity drift %+v %+v", first.Identity, second.Identity)
	}
	if first.Provenance.ReportedHead != strings.Repeat("a", 40) || strings.Contains(text, "envelopeClaim") || strings.Contains(text, `"taskRef"`) {
		t.Fatalf("claim metadata was rewritten: %s", text)
	}
	shown := grantShowClaims(t, h, "grant-maint")
	if !reflect.DeepEqual(got.TaskClaims, shown.TaskClaims) || shown.TaskClaims.Items[0].Owner != first.Owner || shown.TaskClaims.Items[0].UID != "canonical-uid" {
		t.Fatalf("grant show %#v mcp %#v", shown.TaskClaims, got.TaskClaims)
	}
	if len(h.api.requests()) != 2 || headers[1] != apiv1.GrantTaskClaimsV1 {
		t.Fatalf("grant show read %+v %v", h.api.requests(), headers)
	}
}

func TestMCPGrantStatusIncludeTaskClaimsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, claims, reason string
		leak                 []string
	}{
		{name: "missing", claims: "null", reason: "NotReported"},
		{name: "old server", reason: "NotReported"},
		{name: "empty", claims: `{"state":"Available","reason":"Complete","items":[]}`, reason: "NoJoinedClaims", leak: []string{"Complete"}},
		{name: "unreadable", claims: `{"state":"Unknown","reason":"Unreadable","items":[{"name":"leaked-claim","uid":"leak-uid","identity":{"project":"sample","repository":"dev-env","taskKey":"issue:secret"}}]}`, reason: "Unreadable", leak: []string{"leaked-claim", "leak-uid", "issue:secret"}},
		{name: "unreadable without reason", claims: `{"state":"","reason":"","items":[{"name":"leaked-claim","uid":"leak-uid"}]}`, reason: "Unavailable", leak: []string{"leaked-claim", "leak-uid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			body := `{"name":"grant-maint","phase":"Active","type":"maintenance","session":"requester-session","ttl":"1h","reason":"drain","approvalURL":"https://broker.example/maint","approvedBy":"tom"}`
			if tc.claims != "" {
				body = body[:len(body)-1] + `,"taskClaims":` + tc.claims + `}`
			}
			var headers []string
			serveRaw(h, map[string]func(*http.Request) (int, string){
				apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
					recordGrantRead(t, r, &headers)
					return http.StatusOK, body
				},
			})
			text, isErr := mcpToolText(t, h, "grant_status", `{"name":"grant-maint","includeTaskClaims":true}`)
			_, want := grantWire(t, body, true)
			if isErr || text != want || len(h.api.requests()) != 1 || headers[0] != apiv1.GrantTaskClaimsV1 {
				t.Fatalf("error %v requests %d header %v\n%s", isErr, len(h.api.requests()), headers, text)
			}
			var got apiv1.Grant
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatal(err)
			}
			if got.ApprovedBy != "tom" || got.ApprovalURL != "https://broker.example/maint" || got.TaskClaims == nil || got.TaskClaims.State != "Unknown" || got.TaskClaims.Reason != tc.reason || len(got.TaskClaims.Items) != 0 || !strings.Contains(text, `"items": []`) {
				t.Fatalf("claims %#v", got.TaskClaims)
			}
			shown := grantShowClaims(t, h, "grant-maint")
			if !reflect.DeepEqual(got.TaskClaims, shown.TaskClaims) || len(h.api.requests()) != 2 || headers[1] != apiv1.GrantTaskClaimsV1 {
				t.Fatalf("grant show %#v requests %d", shown.TaskClaims, len(h.api.requests()))
			}
			for _, leaked := range tc.leak {
				if strings.Contains(text, leaked) || strings.Contains(h.stdout.String(), leaked) {
					t.Fatalf("leaked %s\n%s\n%s", leaked, text, h.stdout.String())
				}
			}
		})
	}
}

func TestMCPGrantStatusIncludeTaskClaimsNonMaintenance(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		body := `{"name":"grant-hw","phase":"Pending","type":"credential","credential":"hw-ssh","session":"task-1","ttl":"1h","reason":"look at a node","approvalURL":"https://broker.example/hw","approvedBy":"tom"}`
		text, got, headers := namedGrantStatus(t, "grant-hw", body, true)
		_, wire := grantWire(t, body, false)
		if text != wire+"\n\n"+hwsshNotice || got.TaskClaims != nil || strings.Contains(text, "taskClaims") || strings.Contains(text, "NotReported") || got.ApprovalURL != "https://broker.example/hw" || headers[0] != apiv1.GrantTaskClaimsV1 {
			t.Fatalf("header %v\n%s", headers, text)
		}
	})
	t.Run("empty observation stays", func(t *testing.T) {
		body := `{"name":"grant-kube","phase":"Active","type":"kube","session":"task-1","ttl":"30m","reason":"read pods","approvalURL":"https://broker.example/kube","taskClaims":{"state":"Available","reason":"Complete","items":[]}}`
		text, got, headers := namedGrantStatus(t, "grant-kube", body, true)
		_, wire := grantWire(t, body, false)
		if text != wire || headers[0] != apiv1.GrantTaskClaimsV1 || got.TaskClaims == nil || got.TaskClaims.State != "Available" || got.TaskClaims.Reason != "Complete" || len(got.TaskClaims.Items) != 0 {
			t.Fatalf("normalized a non-maintenance grant: header %v %#v\n%s", headers, got.TaskClaims, text)
		}
	})
}

func TestMCPGrantStatusIncludeTaskClaimsAPIError(t *testing.T) {
	h := newHarness(t)
	var headers []string
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath("grant-maint"): func(r *http.Request) (int, string) {
			recordGrantRead(t, r, &headers)
			return http.StatusNotFound, `{"error":{"code":"not_found","message":"no such grant"}}`
		},
	})
	text, isErr := mcpToolText(t, h, "grant_status", `{"name":"grant-maint","includeTaskClaims":true}`)
	if !isErr || text != "no such grant (404 not_found)" || len(h.api.requests()) != 1 || h.api.requests()[0].method != http.MethodGet || headers[0] != apiv1.GrantTaskClaimsV1 {
		t.Fatalf("error %v requests %+v header %v text %q", isErr, h.api.requests(), headers, text)
	}
}

func namedGrantStatus(t *testing.T, name, body string, claims bool) (string, apiv1.Grant, []string) {
	t.Helper()
	h := newHarness(t)
	var headers []string
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath(name): func(r *http.Request) (int, string) {
			recordGrantRead(t, r, &headers)
			return http.StatusOK, body
		},
	})
	args := `{"name":"` + name + `"}`
	if claims {
		args = `{"name":"` + name + `","includeTaskClaims":true}`
	}
	text, isErr := mcpToolText(t, h, "grant_status", args)
	if isErr || len(h.api.requests()) != 1 || h.api.requests()[0].method != http.MethodGet || h.api.requests()[0].path != apiv1.GrantPath(name) {
		t.Fatalf("error %v requests %+v\n%s", isErr, h.api.requests(), text)
	}
	var got apiv1.Grant
	if err := json.NewDecoder(strings.NewReader(text)).Decode(&got); err != nil {
		t.Fatal(err)
	}
	shown := grantShowClaims(t, h, name)
	if !reflect.DeepEqual(got.TaskClaims, shown.TaskClaims) || len(h.api.requests()) != 2 || len(headers) != 2 || headers[1] != apiv1.GrantTaskClaimsV1 {
		t.Fatalf("grant show %#v mcp %#v headers %v", shown.TaskClaims, got.TaskClaims, headers)
	}
	return text, got, headers
}

func claimObservation(name, uid, taskKey string) apiv1.TaskClaim {
	return apiv1.TaskClaim{
		Name: name, UID: uid,
		Identity:   apiv1.TaskClaimIdentity{Project: "sample", Repository: "dev-env", TaskKey: taskKey},
		Owner:      apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "requester-session", UID: "session-uid"},
		Actor:      apiv1.TaskClaimActor{Kind: "coordinator", ServiceAccount: "dev/host", PodName: "source-pod", PodUID: "pod-uid"},
		Provenance: apiv1.TaskClaimProvenance{ReportedHead: strings.Repeat("a", 40)},
		CreatedAt:  testNow, OwnerState: "Running", StopState: "Unknown",
	}
}

func recordGrantRead(t *testing.T, r *http.Request, headers *[]string) {
	t.Helper()
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get(apiv1.SessionTaskClaimsHeader) != "" {
		t.Errorf("grant read %s %s session header %q", r.Method, r.URL.RequestURI(), r.Header.Get(apiv1.SessionTaskClaimsHeader))
	}
	*headers = append(*headers, r.Header.Get(apiv1.GrantTaskClaimsHeader))
}

func grantShowClaims(t *testing.T, h *harness, name string) apiv1.Grant {
	t.Helper()
	h.mustRun(ExitOK, "grant", "show", name, "-o", "json")
	var grant apiv1.Grant
	if err := json.Unmarshal(h.stdout.Bytes(), &grant); err != nil {
		t.Fatal(err, h.stdout.String())
	}
	return grant
}

func grantWire(t *testing.T, body string, normalize bool) (apiv1.Grant, string) {
	t.Helper()
	var grant apiv1.Grant
	if err := json.Unmarshal([]byte(body), &grant); err != nil {
		t.Fatal(err)
	}
	if normalize && grant.Type == "maintenance" {
		grant.TaskClaims = sessionTaskClaimsOrUnknown(grant.TaskClaims)
	}
	raw, err := json.MarshalIndent(grant, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return grant, string(raw)
}

func mcpToolText(t *testing.T, h *harness, tool, args string) (string, bool) {
	t.Helper()
	h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + "}}\n")
	h.mustRun(ExitOK, "mcp")
	frames := decodeMCPFrames(t, h.stdout.String())
	if len(frames) != 1 {
		t.Fatalf("frames %d: %s", len(frames), h.stdout.String())
	}
	var reply struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(frames[0]), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Content) != 1 {
		t.Fatalf("content %s", frames[0])
	}
	return reply.Result.Content[0].Text, reply.Result.IsError
}
