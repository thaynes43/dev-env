package frontdoor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/commonrules"
)

type resourceTestResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func resourceRequest(t *testing.T, h *handler, token, method string, params any) *httptest.ResponseRecorder {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		request["params"] = params
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(res, req)
	return res
}

func decodeResourceResponse(t *testing.T, res *httptest.ResponseRecorder) resourceTestResponse {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP %d", res.Code)
	}
	var response resourceTestResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func resourceResult[T any](t *testing.T, res *httptest.ResponseRecorder) T {
	t.Helper()
	response := decodeResourceResponse(t, res)
	if response.Error != nil {
		t.Fatalf("unexpected RPC error %+v", response.Error)
	}
	var value T
	if err := json.Unmarshal(response.Result, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func resourceContent(t *testing.T, h *handler, uri, wantMIME string) string {
	t.Helper()
	result := resourceResult[struct {
		Contents []struct{ URI, MIMEType, Text string }
	}](t, resourceRequest(t, h, testBearer, "resources/read", map[string]string{"uri": uri}))
	if len(result.Contents) != 1 || result.Contents[0].URI != uri || result.Contents[0].MIMEType != wantMIME {
		t.Fatalf("invalid resource envelope: %+v", result.Contents)
	}
	return result.Contents[0].Text
}

func newResourceTestHandler(t *testing.T, logs *bytes.Buffer) *handler {
	t.Helper()
	token := filepath.Join(t.TempDir(), "bearer")
	writeFile(t, token, testBearer+"\n")
	return &handler{bearerFile: token, log: log.New(logs, "", 0), slots: make(chan struct{}, toolSlots)}
}

func TestOperatingRulesRevisionDigest(t *testing.T) {
	// Add a new revision and digest when the packaged rules change.
	golden := map[string]string{
		"2026-11-23.1": "df1ac09ddb2ac8e4cddf5b5bc196ce081c71bc05e8d6e880e62561e737fd0d23",
		"2026-11-23.2": "d551682e56daaab3874d7136a3a124cf8a9bb8a774b49bcc2256bd649e03293e",
		"2026-11-23.3": "4a1495859a0c251c99eab16f95b59e9552b3bbfc133526c17b384719bf11f504",
	}
	got := commonrules.Digest([]byte(rulesText))
	if want := golden[rulesRevision]; got != want {
		t.Fatalf("operating rules revision %s has digest %s, want %q", rulesRevision, got, want)
	}
}

func TestCatalogMatchesMCPDiscovery(t *testing.T) {
	h := newResourceTestHandler(t, &bytes.Buffer{})
	initialized := resourceResult[struct {
		Capabilities map[string]json.RawMessage
	}](t, resourceRequest(t, h, testBearer, "initialize", nil))
	if string(initialized.Capabilities["resources"]) != "{}" {
		t.Fatalf("missing read-only resources capability: %v", initialized.Capabilities)
	}
	type schema struct {
		Properties map[string]schema
		Required   []string
	}
	tools := resourceResult[struct {
		Tools []struct {
			Name        string
			InputSchema schema
			Annotations struct{ ReadOnlyHint *bool }
		}
	}](t, resourceRequest(t, h, testBearer, "tools/list", nil))
	listed := resourceResult[struct{ Resources []resource }](t, resourceRequest(t, h, testBearer, "resources/list", nil))
	templates := resourceResult[struct {
		ResourceTemplates []struct{ URITemplate string }
	}](t, resourceRequest(t, h, testBearer, "resources/templates/list", nil))
	catalog := resourceContent(t, h, catalogURI, markdownMIME)
	rows := regexp.MustCompile("(?m)^\\| `([^`]+)` \\| ([^|\n]+) \\| ([^|\n]+) \\|").FindAllStringSubmatch(catalog, -1)
	toolRows := make(map[string][]string)
	var names, uris, wantedNames, wantedURIs []string
	for _, row := range rows {
		if strings.HasPrefix(row[1], "dev-env://") {
			uris = append(uris, row[1])
		} else {
			names = append(names, row[1])
			toolRows[row[1]] = row
		}
	}
	for _, tool := range tools.Tools {
		wantedNames = append(wantedNames, tool.Name)
	}
	for _, item := range listed.Resources {
		wantedURIs = append(wantedURIs, item.URI)
	}
	for _, item := range templates.ResourceTemplates {
		wantedURIs = append(wantedURIs, item.URITemplate)
	}
	if !slices.Equal(names, wantedNames) || !slices.Equal(uris, wantedURIs) {
		t.Fatalf("catalog drift: tools %v vs %v, URIs %v vs %v", names, wantedNames, uris, wantedURIs)
	}
	var checkArguments func(tool, args string, input schema)
	checkArguments = func(tool, args string, input schema) {
		for name, property := range input.Properties {
			if !strings.Contains(args, "`"+name+"`") {
				t.Errorf("catalog %s row omits property %q", tool, name)
			}
			checkArguments(tool, args, property)
		}
		for _, required := range input.Required {
			if !strings.Contains(args, "`"+required+"`") {
				t.Errorf("catalog %s row omits required parameter %q", tool, required)
			}
		}
	}
	for _, tool := range tools.Tools {
		row := toolRows[tool.Name]
		checkArguments(tool.Name, row[3], tool.InputSchema)
		if tool.Annotations.ReadOnlyHint == nil {
			t.Errorf("%s has no readOnlyHint", tool.Name)
			continue
		}
		want := "write"
		if *tool.Annotations.ReadOnlyHint {
			want = "read"
		}
		if !strings.HasSuffix(row[2], " "+want) {
			t.Errorf("catalog %s action %q must end in %q", tool.Name, row[2], want)
		}
	}
}

const handoffFixture = `{"schema":"dev-env.work-item-handoff","version":1,"owner":"agent | worktree worker | branch agent/worker","source":{"system":"OpenProject","url":"https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/528","lockVersion":7,"updatedAt":"2026-11-23T12:00:00Z","checkpoint":"present"},"targetScope":{"project":"haynes-engineering","item":528},"workState":{"status":"In progress","hold":false},"evidence":[{"kind":"structured-checkpoint","activityId":1,"createdAt":"2026-11-23T12:00:00Z"}],"nextAction":"A checkpoint carries the recorded owner's label. Its author is unverified. Confirm custody with the recorded owner before acting.","capturedAt":"2026-11-23T13:00:00Z"}`

func TestReadResources(t *testing.T) {
	var logs bytes.Buffer
	dir := t.TempDir()
	stub := filepath.Join(dir, "opwork")
	writeFile(t, stub, fmt.Sprintf(`#!/bin/sh
[ "$1" = "--project=haynes-engineering" ] && [ "$2" = "show" ] && [ "$4" = "--handoff-json" ] && [ "$#" -eq 4 ] || exit 77
[ -z "${KUBERNETES_SERVICE_HOST-}${KUBECONFIG-}" ] || exit 77
case "$3" in
    528) echo "private-diagnostic" >&2
         cat <<'HANDOFF'
%s
HANDOFF
         ;;
    529) echo "private-diagnostic"; exit 4 ;;
    530) echo "private-diagnostic"; exit 3 ;;
    531) echo "private-diagnostic"; exit 5 ;;
    532) echo '{}' ;;
    *) exit 77 ;;
esac
`, handoffFixture))
	if err := os.Chmod(stub, 0o700); err != nil {
		t.Fatal(err)
	}
	bearer := filepath.Join(dir, "bearer")
	tracker := filepath.Join(dir, "tracker")
	operator := filepath.Join(dir, "operator")
	writeFile(t, bearer, testBearer)
	writeFile(t, tracker, testTracker)
	writeFile(t, operator, "fixture-operator-token")
	h, op, err := newHandler(Config{
		BearerFile: bearer, TrackerFile: tracker, OperatorTokenFile: operator,
		APIURL: "https://127.0.0.1:1", Opwork: stub,
	}, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(op.Close)

	t.Setenv("KUBERNETES_SERVICE_HOST", "should-not-reach-child")
	t.Setenv("KUBECONFIG", "should-not-reach-child")
	res := resourceRequest(t, h, "", "resources/read", map[string]string{"uri": handoffPrefix + "528"})
	if res.Code != http.StatusUnauthorized || res.Body.Len() != 0 {
		t.Fatalf("no bearer: HTTP %d with %d bytes", res.Code, res.Body.Len())
	}
	data := resourceContent(t, h, rulesURI, jsonMIME)
	var rules struct {
		Schema, Revision, SHA256, Text string
		Version                        int
	}
	if err := json.Unmarshal([]byte(data), &rules); err != nil {
		t.Fatal(err)
	}
	if rules.Schema != "dev-env.operating-rules" || rules.Version != 1 || rules.Revision == "" ||
		rules.SHA256 != commonrules.Digest([]byte(rules.Text)) || !strings.Contains(rules.Text, "uncommitted edit") {
		t.Fatal("invalid operating rules")
	}
	if index := resourceContent(t, h, handoffURI, markdownMIME); !strings.Contains(index, handoffTemplate) {
		t.Fatal("handoff index does not identify the template")
	}
	t.Run("successful opwork uses stdout despite stderr", func(t *testing.T) {
		handoff := resourceContent(t, h, handoffPrefix+"528", jsonMIME)
		if handoff != handoffFixture {
			t.Fatal("handoff did not marshal the validated document from stdout")
		}
	})

	for _, test := range []struct {
		uri     string
		code    int
		message string
	}{
		{handoffPrefix + "529", -32002, "resource not found"},
		{handoffPrefix + "530", -32003, "handoff read refused"},
		{handoffPrefix + "531", -32004, "handoff tracker unavailable"},
		{handoffPrefix + "532", -32004, "handoff tracker unavailable"},
		{handoffPrefix + "0528", -32602, "invalid resource URI"},
		{handoffPrefix + "%35%32%38", -32602, "invalid resource URI"},
		{handoffPrefix + "528?private-diagnostic", -32602, "invalid resource URI"},
		{handoffPrefix + "528/private-diagnostic", -32602, "invalid resource URI"},
		{"dev-env://usage/v1", -32002, "resource not found"},
	} {
		res := resourceRequest(t, h, testBearer, "resources/read", map[string]string{"uri": test.uri})
		response := decodeResourceResponse(t, res)
		if response.Error == nil || response.Error.Code != test.code || response.Error.Message != test.message || response.Result != nil {
			t.Fatalf("URI %q: error=%+v", test.uri, response.Error)
		}
		if strings.Contains(res.Body.String(), "private-diagnostic") {
			t.Fatal("response contains an upstream diagnostic")
		}
	}
	for range cap(h.slots) {
		h.admit()
	}
	res = resourceRequest(t, h, testBearer, "resources/read", map[string]string{"uri": handoffPrefix + "528"})
	if response := decodeResourceResponse(t, res); response.Error == nil || response.Error.Code != resourceBusy {
		t.Fatalf("expected busy, got %+v", response.Error)
	}
	if strings.Contains(logs.String(), "private-diagnostic") || strings.Contains(logs.String(), "528") ||
		!strings.Contains(logs.String(), "rpc resources/read handoff refused") ||
		!strings.Contains(logs.String(), "rpc resources/read handoff unavailable") {
		t.Fatalf("unexpected audit output: %s", &logs)
	}
}

func TestHandoffProjectionValidation(t *testing.T) {
	const url = "https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/528"
	const evidence = `{"kind":"structured-checkpoint","activityId":1,"createdAt":"2026-11-23T12:00:00Z"}`
	for _, test := range []struct {
		name, from, to string
	}{
		{"owner must have the recognized format", "agent | worktree worker | branch agent/worker", "upstream prose"},
		{"owner branch must match worktree", "branch agent/worker", "branch agent/other"},
		{"owner must fit the slug bound", "worktree worker | branch agent/worker", "worktree " + strings.Repeat("w", 81) + " | branch agent/" + strings.Repeat("w", 81)},
		{"checkpoint enum", `"checkpoint":"present"`, `"checkpoint":"other"`},
		{"status enum", `"status":"In progress"`, `"status":"other"`},
		{"hold must be boolean or null", `"hold":false`, `"hold":"false"`},
		{"next action allowlist", "Confirm custody with the recorded owner before acting.", "Use upstream prose."},
		{"evidence kind enum", "structured-checkpoint", "other"},
		{"at most two evidence entries", evidence, evidence + "," + evidence + "," + evidence},
		{"duplicate top-level key", `"version":1`, `"version":2,"version":1`},
		{"duplicate nested key", `"checkpoint":"present"`, `"checkpoint":"other","checkpoint":"present"`},
		{"case-sensitive keys", `"checkpoint":`, `"Checkpoint":`},
		{"exact work item URL", url, url + "?other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := strings.Replace(handoffFixture, test.from, test.to, 1)
			if text == handoffFixture {
				t.Fatal("mutation did not apply")
			}
			if validatedHandoff(text, defaultProject, 528, url, testBearer) != nil {
				t.Fatal("invalid tracker projection accepted")
			}
		})
	}
}

func TestResourceParamsIgnoreMetaAndRejectEveryOtherExtraKey(t *testing.T) {
	h := newResourceTestHandler(t, &bytes.Buffer{})
	for _, test := range []struct {
		method string
		uri    string
	}{
		{"resources/list", ""},
		{"resources/templates/list", ""},
		{"resources/read", catalogURI},
	} {
		t.Run(test.method, func(t *testing.T) {
			params := map[string]any{"_meta": map[string]any{"progressToken": 1}}
			if test.uri != "" {
				params["uri"] = test.uri
			}
			accepted := decodeResourceResponse(t, resourceRequest(t, h, testBearer, test.method, params))
			if accepted.Error != nil || accepted.Result == nil {
				t.Fatalf("params with _meta: %+v", accepted.Error)
			}
			params["cursor"] = "unused"
			rejected := decodeResourceResponse(t, resourceRequest(t, h, testBearer, test.method, params))
			if rejected.Error == nil || rejected.Error.Code != invalidParams || rejected.Result != nil {
				t.Fatalf("params with an extra key: %+v", rejected.Error)
			}
		})
	}
}
