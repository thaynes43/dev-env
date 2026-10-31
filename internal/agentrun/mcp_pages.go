package agentrun

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/internal/version"
)

type pagesMCPConfig struct {
	Alertmanager bool
	Gatus        bool
}
type pagesBackend interface {
	Fetch(context.Context, string) ([]byte, error)
}
type pagesMCPSession struct {
	ctx       context.Context
	config    pagesMCPConfig
	backend   pagesBackend
	responses pages.ResponseSources
	now       func() time.Time
}

type pagesMCPItem struct {
	Response      *pages.ResponseProjection `json:"response,omitempty"`
	Kind          string                    `json:"kind"`
	Key           string                    `json:"key"`
	Name          string                    `json:"name"`
	Group         string                    `json:"group,omitempty"`
	Status        string                    `json:"status,omitempty"`
	StartsAt      string                    `json:"startsAt,omitempty"`
	EndsAt        string                    `json:"endsAt,omitempty"`
	UpdatedAt     string                    `json:"updatedAt,omitempty"`
	Summary       string                    `json:"summary,omitempty"`
	Description   string                    `json:"description,omitempty"`
	Labels        map[string]string         `json:"labels,omitempty"`
	Timestamp     string                    `json:"timestamp,omitempty"`
	Success       *bool                     `json:"success,omitempty"`
	HTTPStatus    *int64                    `json:"httpStatus,omitempty"`
	DurationNanos *int64                    `json:"durationNanos,omitempty"`
}
type pagesMCPResult struct {
	Availability     string         `json:"availability"`
	Source           string         `json:"source"`
	CapturedAt       string         `json:"capturedAt"`
	Revision         string         `json:"revision"`
	Coverage         string         `json:"coverage"`
	State            string         `json:"state"`
	Items            []pagesMCPItem `json:"items"`
	Truncated        bool           `json:"truncated"`
	ChangedSinceList bool           `json:"changedSinceList"`
}

func pagesMCPConfigFile(path string) (pagesMCPConfig, error) {
	var cfg pagesMCPConfig
	if path == "" {
		return cfg, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return cfg, errors.New("source configuration unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("source configuration unavailable")
	}
	defer func() { _ = f.Close() }()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return cfg, errors.New("source configuration unavailable")
	}
	owner, ok := actual.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() {
		return cfg, errors.New("source configuration unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 {
		return cfg, errors.New("source configuration unavailable")
	}
	obj, ok := pagesMCPObject(raw, nil, []string{"alertmanager", "gatus"})
	if !ok {
		return cfg, errors.New("source configuration unavailable")
	}
	for name, v := range obj {
		var value any
		if json.Unmarshal(v, &value) != nil {
			return cfg, errors.New("source configuration unavailable")
		}
		b, ok := value.(bool)
		if !ok {
			return cfg, errors.New("source configuration unavailable")
		}
		if name == "alertmanager" {
			cfg.Alertmanager = b
		} else {
			cfg.Gatus = b
		}
	}
	return cfg, nil
}
func (a *app) pagesMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pages-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var path string
	fs.StringVar(&path, "source-config", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(a.env.Stdout, commandHelp["pages-mcp"])
			return &exitError{code: ExitOK}
		}
		return usageError("pages-mcp: invalid flags")
	}
	if len(fs.Args()) != 0 {
		return usageError("pages-mcp takes no positional arguments")
	}
	cfg, err := pagesMCPConfigFile(path)
	if err != nil {
		return usageError("pages-mcp: source configuration unavailable")
	}
	s := &pagesMCPSession{ctx: ctx, config: cfg, backend: newFixedPagesBackend(), now: a.now}
	return s.serve(a.env.Stdin, a.env.Stdout)
}
func (s *pagesMCPSession) serve(in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return fail(ExitFailed, "pages-mcp: unavailable")
	}
	scanner := newMCPScanner(in)
	w := bufio.NewWriter(out)
	for {
		raw, err := nextMCPMessage(scanner)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fail(ExitFailed, "pages-mcp: invalid input")
		}
		reply := s.handle(raw)
		if reply != nil {
			if err := writeMCPMessage(w, reply); err != nil {
				return fail(ExitFailed, "pages-mcp: output unavailable")
			}
		}
	}
}
func pagesMCPObject(raw []byte, required, optional []string) (map[string]json.RawMessage, bool) {
	if len(raw) > pages.AlertmanagerMaxBytes || !utf8.Valid(raw) || !protocol.StrictPortalJSON(raw) {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
		if _, ok := obj[k]; !ok {
			return nil, false
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range obj {
		if !allowed[k] {
			return nil, false
		}
	}
	return obj, true
}
func pagesMCPString(raw json.RawMessage) (string, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
func (s *pagesMCPSession) handle(raw []byte) []byte {
	obj, ok := pagesMCPObject(raw, []string{"jsonrpc", "method"}, []string{"id", "params"})
	if !ok {
		return mcpError(json.RawMessage("null"), -32600, "invalid request")
	}
	rpc, ok := pagesMCPString(obj["jsonrpc"])
	if !ok || rpc != "2.0" {
		return mcpError(json.RawMessage("null"), -32600, "invalid request")
	}
	method, ok := pagesMCPString(obj["method"])
	if !ok {
		return mcpError(json.RawMessage("null"), -32600, "invalid request")
	}
	id, hasID := obj["id"]
	if !hasID {
		return nil
	}
	var idValue any
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if decoder.Decode(&idValue) != nil {
		return mcpError(json.RawMessage("null"), -32600, "invalid request")
	}
	switch value := idValue.(type) {
	case string:
		if len(value) > 128 {
			return mcpError(json.RawMessage("null"), -32600, "invalid request")
		}
	case json.Number:
		if _, err := strconv.ParseInt(value.String(), 10, 64); err != nil {
			return mcpError(json.RawMessage("null"), -32600, "invalid request")
		}
	default:
		return mcpError(json.RawMessage("null"), -32600, "invalid request")
	}
	if string(id) == "null" {
		return mcpError(json.RawMessage("null"), -32600, "invalid request")
	}
	var result any
	switch method {
	case "initialize":
		result = map[string]any{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "dev-env-pages", "version": version.Get().Version}}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": pagesMCPTools()}
	case "tools/call":
		result = s.call(obj["params"])
	default:
		return mcpError(id, -32601, "method not found")
	}
	b, err := json.Marshal(result)
	if err != nil || len(b) > maxMCPMessage {
		return mcpResult(id, mustPagesJSON(mcpToolError("Unavailable: response exceeds bounds")))
	}
	return mcpResult(id, b)
}
func mustPagesJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func pagesMCPTools() []map[string]any {
	annotation := map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true}
	source := map[string]any{"type": "string", "enum": []string{"alertmanager", "gatus"}}
	return []map[string]any{
		{"name": "pages_list", "description": "Read a bounded current snapshot; not notification history.", "annotations": annotation, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"source": source, "filter": map[string]any{"type": "string", "maxLength": 256}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "required": []string{"source"}, "additionalProperties": false}},
		{"name": "pages_get", "description": "Refresh and find one current snapshot item by exact key.", "annotations": annotation, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"source": source, "key": map[string]any{"type": "string", "maxLength": 512}, "expectedRevision": map[string]any{"type": "string", "maxLength": 71}}, "required": []string{"source", "key"}, "additionalProperties": false}},
	}
}

var pagesTextURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s<>"']+`)

func safePagesText(s string) string {
	s = pages.RedactText(pagesTextURL.ReplaceAllString(s, pages.Redacted))
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

const pagesMCPKeyLimit = 512

func validPagesArg(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && safePagesText(s) == s
}
func (s *pagesMCPSession) call(raw json.RawMessage) map[string]any {
	call, ok := pagesMCPObject(raw, []string{"name", "arguments"}, nil)
	if !ok {
		return mcpToolError("Invalid request")
	}
	name, ok := pagesMCPString(call["name"])
	if !ok || (name != "pages_list" && name != "pages_get") {
		return mcpToolError("Invalid request")
	}
	required := []string{"source"}
	optional := []string{"filter", "limit"}
	if name == "pages_get" {
		required = append(required, "key")
		optional = []string{"expectedRevision"}
	}
	args, ok := pagesMCPObject(call["arguments"], required, optional)
	if !ok {
		return mcpToolError("Invalid request")
	}
	source, ok := pagesMCPString(args["source"])
	if !ok || (source != "alertmanager" && source != "gatus") {
		return mcpToolError("Invalid request")
	}
	filter, key, revision := "", "", ""
	limit := 20
	if raw, exists := args["filter"]; exists {
		filter, ok = pagesMCPString(raw)
		if !ok || (filter != "" && !validPagesArg(filter, 256)) {
			return mcpToolError("Invalid request")
		}
	}
	if raw, exists := args["limit"]; exists {
		var value any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if dec.Decode(&value) != nil {
			return mcpToolError("Invalid request")
		}
		n, valid := value.(json.Number)
		if !valid {
			return mcpToolError("Invalid request")
		}
		i, e := strconv.Atoi(n.String())
		if e != nil || i < 1 || i > 100 {
			return mcpToolError("Invalid request")
		}
		limit = i
	}
	if name == "pages_get" {
		key, ok = pagesMCPString(args["key"])
		if !ok || !validPagesArg(key, pagesMCPKeyLimit) {
			return mcpToolError("Invalid request")
		}
		if raw, exists := args["expectedRevision"]; exists {
			revision, ok = pagesMCPString(raw)
			if !ok || len(revision) != 71 || !strings.HasPrefix(revision, "sha256:") {
				return mcpToolError("Invalid request")
			}
			if _, e := hex.DecodeString(revision[7:]); e != nil || strings.ToLower(revision) != revision {
				return mcpToolError("Invalid request")
			}
		}
	}
	if (source == "alertmanager" && !s.config.Alertmanager) || (source == "gatus" && !s.config.Gatus) || s.backend == nil || s.now == nil {
		return mcpToolError("Unavailable: source disabled")
	}
	body, err := s.backend.Fetch(s.ctx, source)
	if err != nil {
		return mcpToolError("Unavailable: current snapshot")
	}
	at := s.now().UTC()
	if at.IsZero() {
		return mcpToolError("Unavailable: current snapshot")
	}
	items, coverage, err := normalizePagesMCP(source, body, at)
	if err != nil {
		return mcpToolError("Unavailable: current snapshot")
	}
	if source == "alertmanager" {
		identities := make([]pages.AlertIdentity, len(items))
		for i := range items {
			identities[i] = pages.AlertIdentity{Fingerprint: items[i].Key, StartsAt: items[i].StartsAt}
		}
		responses := pages.ProjectResponses(s.ctx, identities, s.responses, at)
		for i := range items {
			response := responses[identities[i]]
			items[i].Response = &response
		}
	}
	all, err := json.Marshal(items)
	if err != nil || len(all) > pages.AlertmanagerMaxBytes {
		return mcpToolError("Unavailable: current snapshot")
	}
	current, err := pagesRevision(items)
	if err != nil {
		return mcpToolError("Unavailable: current snapshot")
	}
	out := pagesMCPResult{Availability: "Available", Source: source, CapturedAt: at.Format(time.RFC3339Nano), Revision: current, Coverage: coverage, State: "CurrentSnapshot", Items: []pagesMCPItem{}}
	for _, item := range items {
		if name == "pages_get" && item.Key != key {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(item.Key+" "+item.Name), strings.ToLower(filter)) {
			continue
		}
		out.Items = append(out.Items, item)
	}
	if name == "pages_get" {
		out.ChangedSinceList = revision != "" && revision != current
		if len(out.Items) == 0 {
			out.State = "NotPresentInCurrentSnapshot"
		} else if out.ChangedSinceList {
			out.State = "ChangedSinceList"
		}
	} else if len(out.Items) > limit {
		out.Truncated = true
		out.Items = out.Items[:limit]
	}
	text, err := json.Marshal(out)
	if err != nil || len(text) > maxMCPMessage/2 {
		return mcpToolError("Unavailable: response exceeds bounds")
	}
	return map[string]any{"content": []map[string]string{{"type": "text", "text": string(text)}}, "isError": false}
}

// pagesRevision hashes the normalized items without capture or response
// observation times, so an adapter re-observing unchanged evidence keeps the
// revision. Conditions, sources and event times still count as content.
func pagesRevision(items []pagesMCPItem) (string, error) {
	stable := make([]pagesMCPItem, len(items))
	copy(stable, items)
	for i := range stable {
		if stable[i].Response != nil {
			response := *stable[i].Response
			response.OrderObservedAt, response.ClaimObservedAt = "", ""
			stable[i].Response = &response
		}
	}
	raw, err := json.Marshal(stable)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func normalizePagesMCP(source string, raw []byte, at time.Time) ([]pagesMCPItem, string, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, "", errors.New("unsupported snapshot envelope")
	}
	var out []pagesMCPItem
	var coverage string
	if source == "alertmanager" {
		snap, err := pages.ParseAlertmanager(raw)
		if err != nil {
			return nil, "", err
		}
		coverage = snap.Coverage
		for _, a := range snap.Alerts {
			labels := map[string]string{}
			for k, v := range a.Labels {
				if safePagesText(k) != k {
					return nil, "", errors.New("unsafe label key")
				}
				labels[k] = safePagesText(v)
			}
			if !validPagesArg(a.Fingerprint, pagesMCPKeyLimit) {
				return nil, "", errors.New("unsafe alert key")
			}
			out = append(out, pagesMCPItem{Kind: "current_alert", Key: a.Fingerprint, Name: safePagesText(a.Name), Status: a.Status, StartsAt: a.StartsAt, EndsAt: a.EndsAt, UpdatedAt: a.UpdatedAt, Summary: safePagesText(a.Summary), Description: safePagesText(a.Description), Labels: labels})
		}
	} else {
		snap, err := pages.ParseGatus(raw, at, "Gatus current checks")
		if err != nil {
			return nil, "", err
		}
		coverage = snap.Coverage
		for _, g := range snap.Endpoints {
			if !validPagesArg(g.Key, pagesMCPKeyLimit) {
				return nil, "", errors.New("unsafe endpoint key")
			}
			out = append(out, pagesMCPItem{Kind: "current_endpoint_check", Key: g.Key, Name: safePagesText(g.Name), Group: safePagesText(g.Group), Timestamp: g.Latest.Timestamp, Success: g.Latest.Success, HTTPStatus: g.Latest.Status, DurationNanos: g.Latest.DurationNanos})
		}
	}
	if out == nil {
		out = []pagesMCPItem{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, coverage, nil
}

const pagesGrafanaURL = "https://mcp-grafana.haynesops.com/mcp"
const pagesGrafanaPath = "/api/datasources/proxy/uid/alertmanager/api/v2/alerts"
const pagesGatusURL = "https://status.haynesops.com/api/v1/endpoints/statuses"

type fixedPagesBackend struct{ client *http.Client }

func newFixedPagesBackend() *fixedPagesBackend {
	return &fixedPagesBackend{client: &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (b *fixedPagesBackend) request(ctx context.Context, method, url string, body []byte, session string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, "", errors.New("unavailable")
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, "", errors.New("unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", errors.New("unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, pages.AlertmanagerMaxBytes+1))
	if err != nil || len(raw) > pages.AlertmanagerMaxBytes {
		return nil, "", errors.New("unavailable")
	}
	token := resp.Header.Get("Mcp-Session-Id")
	if len(token) > 128 || strings.ContainsAny(token, "\r\n \t") {
		return nil, "", errors.New("unavailable")
	}
	return raw, token, nil
}
func pagesRPCResult(raw []byte, id string) (json.RawMessage, bool) {
	obj, ok := pagesMCPObject(raw, []string{"jsonrpc", "id", "result"}, nil)
	if !ok || string(obj["id"]) != id {
		return nil, false
	}
	rpc, ok := pagesMCPString(obj["jsonrpc"])
	return obj["result"], ok && rpc == "2.0"
}
func validPagesMCPInitialize(obj map[string]json.RawMessage) bool {
	version, ok := pagesMCPString(obj["protocolVersion"])
	if !ok || version != "2024-11-05" || len(obj["capabilities"]) > 16384 {
		return false
	}
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(obj["capabilities"], &capabilities) != nil || capabilities == nil {
		return false
	}
	for _, raw := range capabilities {
		var capability map[string]json.RawMessage
		if json.Unmarshal(raw, &capability) != nil || capability == nil {
			return false
		}
	}
	info, ok := pagesMCPObject(obj["serverInfo"], []string{"name", "version"}, []string{"title"})
	if !ok || len(obj["serverInfo"]) > 4096 {
		return false
	}
	for _, key := range []string{"name", "version", "title"} {
		if raw, exists := info[key]; exists {
			value, ok := pagesMCPString(raw)
			if !ok || len(value) == 0 || len(value) > 256 {
				return false
			}
		}
	}
	if raw, exists := obj["instructions"]; exists {
		value, ok := pagesMCPString(raw)
		if !ok || len(value) > 16384 {
			return false
		}
	}
	return true
}
func (b *fixedPagesBackend) Fetch(ctx context.Context, source string) ([]byte, error) {
	if source == "gatus" {
		raw, _, err := b.request(ctx, http.MethodGet, pagesGatusURL, nil, "")
		return raw, err
	}
	if source != "alertmanager" {
		return nil, errors.New("unavailable")
	}
	init := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"dev-env-pages","version":"1"}}}`)
	raw, session, err := b.request(ctx, http.MethodPost, pagesGrafanaURL, init, "")
	if err != nil {
		return nil, err
	}
	result, ok := pagesRPCResult(raw, "1")
	if !ok {
		return nil, errors.New("unavailable")
	}
	initialized, ok := pagesMCPObject(result, []string{"protocolVersion", "capabilities", "serverInfo"}, []string{"instructions"})
	if !ok || !validPagesMCPInitialize(initialized) {
		return nil, errors.New("unavailable")
	}
	notification := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if _, _, err = b.request(ctx, http.MethodPost, pagesGrafanaURL, notification, session); err != nil {
		return nil, err
	}
	call := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grafana_api_request","arguments":{"method":"GET","endpoint":"/api/datasources/proxy/uid/alertmanager/api/v2/alerts"}}}`)
	raw, _, err = b.request(ctx, http.MethodPost, pagesGrafanaURL, call, session)
	if err != nil {
		return nil, err
	}
	result, ok = pagesRPCResult(raw, "2")
	if !ok {
		return nil, errors.New("unavailable")
	}
	obj, ok := pagesMCPObject(result, []string{"content"}, []string{"isError"})
	if !ok {
		return nil, errors.New("unavailable")
	}
	if raw, exists := obj["isError"]; exists && string(raw) != "false" {
		return nil, errors.New("unavailable")
	}
	var content []json.RawMessage
	if json.Unmarshal(obj["content"], &content) != nil || len(content) != 1 {
		return nil, errors.New("unavailable")
	}
	item, ok := pagesMCPObject(content[0], []string{"type", "text"}, nil)
	if !ok {
		return nil, errors.New("unavailable")
	}
	typ, ok := pagesMCPString(item["type"])
	if !ok || typ != "text" {
		return nil, errors.New("unavailable")
	}
	text, ok := pagesMCPString(item["text"])
	if !ok || len(text) > pages.AlertmanagerMaxBytes || !strings.HasPrefix(strings.TrimSpace(text), "[") {
		return nil, errors.New("unavailable")
	}
	return []byte(text), nil
}
