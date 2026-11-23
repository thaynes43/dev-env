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

func TestCatalogMatchesMCPDiscovery(t *testing.T) {
	h := newResourceTestHandler(t, &bytes.Buffer{})
	initialized := resourceResult[struct {
		Capabilities map[string]json.RawMessage
	}](t, resourceRequest(t, h, testBearer, "initialize", nil))
	if string(initialized.Capabilities["resources"]) != "{}" {
		t.Fatalf("missing read-only resources capability: %v", initialized.Capabilities)
	}
	tools := resourceResult[struct {
		Tools []struct{ Name string }
	}](t, resourceRequest(t, h, testBearer, "tools/list", nil))
	listed := resourceResult[struct{ Resources []resource }](t, resourceRequest(t, h, testBearer, "resources/list", nil))
	templates := resourceResult[struct {
		ResourceTemplates []struct{ URITemplate string }
	}](t, resourceRequest(t, h, testBearer, "resources/templates/list", nil))
	catalog := resourceContent(t, h, catalogURI, markdownMIME)
	rows := regexp.MustCompile("(?m)^\\| `([^`]+)` \\|").FindAllStringSubmatch(catalog, -1)
	var names, uris, wantedNames, wantedURIs []string
	for _, row := range rows {
		if strings.HasPrefix(row[1], "dev-env://") {
			uris = append(uris, row[1])
		} else {
			names = append(names, row[1])
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
}

const handoffFixture = `{"schema":"dev-env.work-item-handoff","version":1,"owner":"agent | worktree worker | branch agent/worker","source":{"system":"OpenProject","url":"https://work.haynesnetwork.com/projects/haynes-engineering/work_packages/528","lockVersion":7,"updatedAt":"2026-11-23T12:00:00Z","checkpoint":"present"},"targetScope":{"project":"haynes-engineering","item":528},"workState":{"status":"In progress","hold":false},"evidence":[{"kind":"structured-checkpoint","activityId":1,"createdAt":"2026-11-23T12:00:00Z"}],"nextAction":"Confirm custody and consult the referenced checkpoint for the recorded next action.","capturedAt":"2026-11-23T13:00:00Z"}`

func TestReadResources(t *testing.T) {
	var logs bytes.Buffer
	h := newResourceTestHandler(t, &logs)
	stub := filepath.Join(t.TempDir(), "opwork")
	writeFile(t, stub, fmt.Sprintf(`#!/bin/sh
[ "$1" = "--project=haynes-engineering" ] && [ "$2" = "show" ] && [ "$4" = "--handoff-json" ] && [ "$#" -eq 4 ] || exit 77
[ -z "${KUBERNETES_SERVICE_HOST-}${KUBECONFIG-}" ] || exit 77
case "$3" in
    528) printf '%%s\n' '%s' ;;
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
	h.work = opwork{path: stub, project: defaultProject}

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
	if handoff := resourceContent(t, h, handoffPrefix+"528", jsonMIME); handoff != handoffFixture {
		t.Fatal("handoff round trip did not preserve the versioned document")
	}

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

func TestResourceReadsWithEmptyOperatorToken(t *testing.T) {
	dir := t.TempDir()
	bearer := filepath.Join(dir, "bearer")
	tracker := filepath.Join(dir, "tracker")
	operator := filepath.Join(dir, "operator")
	writeFile(t, bearer, testBearer)
	writeFile(t, tracker, testTracker)
	writeFile(t, operator, "")
	h, op, err := newHandler(Config{
		BearerFile: bearer, TrackerFile: tracker, OperatorTokenFile: operator,
		APIURL: "https://127.0.0.1:1",
	}, log.New(&bytes.Buffer{}, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(op.Close)
	if resourceContent(t, h, rulesURI, jsonMIME) == "" {
		t.Fatal("rules resource unavailable")
	}
	status := resourceResult[struct {
		IsError bool
		Content []struct{ Text string }
	}](t, resourceRequest(t, h, testBearer, "tools/call", map[string]any{
		"name": "session_status", "arguments": map[string]string{"name": "example-session"},
	}))
	if !status.IsError || len(status.Content) != 1 || !strings.Contains(status.Content[0].Text, "is empty") {
		t.Fatal("missing operator token did not fail the operator request locally")
	}
}
