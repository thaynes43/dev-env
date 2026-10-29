package agentrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/version"
)

// mcp is the in-pod dev-env server (DESIGN-001 6.12, plan 07 step 7).
// It speaks MCP on stdin and stdout. Access tools call /v1/grants; asks tools
// call /v1/asks only when invoked. Neither waits for a human response.

const mcpProtocolVersion = "2024-11-05"

func (a *app) mcp(ctx context.Context, args []string) error {
	cmd := newCommand("mcp")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("mcp takes no arguments; it speaks MCP on stdin and stdout")
	}
	// The API connection waits for the first tools/call. initialize can be
	// answered while the operator or the token is briefly unavailable.
	return a.serveMCP(ctx, cmd.c.conn, a.env.Stdin, a.env.Stdout)
}

func (a *app) serveMCP(ctx context.Context, opts connOpts, in io.Reader, out io.Writer) error {
	s := &mcpSession{app: a, ctx: ctx, opts: opts}
	sc := newMCPScanner(in)
	w := bufio.NewWriter(out)
	for {
		body, err := nextMCPMessage(sc)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fail(ExitFailed, "mcp: %v", err)
		}
		msg, err := s.handle(body)
		if err != nil {
			return err
		}
		if msg == nil {
			continue
		}
		if err := writeMCPMessage(w, msg); err != nil {
			return fail(ExitFailed, "mcp: %v", err)
		}
	}
}

// mcpSession is one stdio server. conn stays nil until a tool needs the API.
type mcpSession struct {
	app  *app
	ctx  context.Context
	opts connOpts
	conn *conn
}

func (s *mcpSession) client() (*conn, error) {
	if s.conn != nil {
		return s.conn, nil
	}
	c, err := s.app.connect(s.ctx, s.opts)
	if err != nil {
		return nil, err
	}
	s.conn = c
	return c, nil
}

type mcpRequest struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

func (s *mcpSession) handle(body []byte) ([]byte, error) {
	var req mcpRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return mcpError(json.RawMessage("null"), -32700, "parse error"), nil
	}
	if req.ID == nil {
		return nil, nil
	}
	var result any
	var rpcErr *mcpRPCError
	switch req.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "dev-env", "version": version.Get().Version},
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": append(accessTools(), askTools()...)}
	case "tools/call":
		c, err := s.client()
		if err != nil {
			result = mcpToolError(err.Error())
			break
		}
		result = s.app.callAccessTool(s.ctx, c, req.Params)
	default:
		rpcErr = &mcpRPCError{Code: -32601, Message: "method not found"}
	}
	if rpcErr != nil {
		return mcpError(*req.ID, rpcErr.Code, rpcErr.Message), nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fail(ExitFailed, "mcp: %v", err)
	}
	return mcpResult(*req.ID, raw), nil
}

type mcpRPCError struct {
	Code    int
	Message string
}

func (a *app) callAccessTool(ctx context.Context, c *conn, params json.RawMessage) map[string]any {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return mcpToolError("the tool call is not JSON")
	}
	if isAskTool(call.Name) {
		text, err := a.dispatchAskTool(ctx, c, call.Name, call.Arguments)
		if err != nil {
			return mcpToolError(err.Error())
		}
		return map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
			"isError": false,
		}
	}
	var args accessToolArgs
	if len(call.Arguments) > 0 && string(call.Arguments) != "null" {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return mcpToolError("the tool arguments are not the access fields")
		}
	}
	text, err := a.dispatchAccessTool(ctx, c, call.Name, args)
	if err != nil {
		return mcpToolError(err.Error())
	}
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": false,
	}
}

type accessToolArgs struct {
	Name       string                `json:"name"`
	Type       string                `json:"type"`
	Reason     string                `json:"reason"`
	Role       string                `json:"role"`
	Namespaces []string              `json:"namespaces"`
	FQDNs      []string              `json:"fqdns"`
	CIDRs      []string              `json:"cidrs"`
	Endpoints  []apiv1.GrantEndpoint `json:"endpoints"`
	Ports      []apiv1.GrantPort     `json:"ports"`
	Credential string                `json:"credential"`
	TTL        string                `json:"ttl"`
	Session    string                `json:"session"`
	Phase      string                `json:"phase"`
	Mine       bool                  `json:"mine"`
}

func (a *app) dispatchAccessTool(ctx context.Context, c *conn, name string, args accessToolArgs) (string, error) {
	switch name {
	case "request_access":
		req, err := accessRequest(args)
		if err != nil {
			return "", err
		}
		var out apiv1.Grant
		if _, err := a.call(ctx, c, http.MethodPost, apiv1.GrantsPath, nil, req, &out); err != nil {
			return "", err
		}
		text, err := mcpJSON(out)
		return noticeFor(text, out), err
	case "grant_status":
		if args.Name != "" {
			if err := protocol.ValidGrantName(args.Name); err != nil {
				return "", err
			}
			var out apiv1.Grant
			if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantPath(args.Name), nil, nil, &out); err != nil {
				return "", err
			}
			text, err := mcpJSON(out)
			return noticeFor(text, out), err
		}
		if args.Phase != "" && !grantPhaseKnown(args.Phase) {
			return "", usageError("phase is one of %s, not %q", strings.Join(grantPhases, ", "), args.Phase)
		}
		q := url.Values{}
		if args.Session != "" {
			q.Set(apiv1.FilterSession, args.Session)
		}
		if args.Phase != "" {
			q.Set(apiv1.FilterPhase, args.Phase)
		}
		if args.Mine {
			q.Set(apiv1.FilterMine, "true")
		}
		var out apiv1.GrantList
		if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantsPath, q, nil, &out); err != nil {
			return "", err
		}
		text, err := mcpJSON(out)
		return noticeFor(text, out.Items...), err
	case "release_access":
		if err := protocol.ValidGrantName(args.Name); err != nil {
			return "", err
		}
		var out apiv1.Grant
		if _, err := a.call(ctx, c, http.MethodDelete, apiv1.GrantPath(args.Name), nil, nil, &out); err != nil {
			return "", err
		}
		text, err := mcpJSON(out)
		return noticeFor(text, out), err
	default:
		return "", usageError("unknown tool %q", name)
	}
}

func accessRequest(args accessToolArgs) (apiv1.CreateGrantRequest, error) {
	switch args.Type {
	case apiv1.GrantTypeKube, apiv1.GrantTypeEgress, apiv1.GrantTypeCredential, apiv1.GrantTypeBreakglass:
	default:
		return apiv1.CreateGrantRequest{}, usageError("type is kube, egress, credential or breakglass, not %q", args.Type)
	}
	if strings.TrimSpace(args.Reason) == "" {
		return apiv1.CreateGrantRequest{}, usageError("reason is required; Tom reads it on the approval page")
	}
	for i, p := range args.Ports {
		if p.Port < 1 || p.Port > 65535 {
			return apiv1.CreateGrantRequest{}, usageError("port %d is outside 1-65535", p.Port)
		}
		switch strings.ToUpper(p.Protocol) {
		case "", "TCP", "UDP":
		default:
			return apiv1.CreateGrantRequest{}, usageError("port protocol is tcp or udp, not %q", p.Protocol)
		}
		args.Ports[i].Protocol = strings.ToUpper(p.Protocol)
	}
	return apiv1.CreateGrantRequest{
		Type: args.Type, Role: args.Role, Namespaces: args.Namespaces, FQDNs: args.FQDNs,
		CIDRs: args.CIDRs, Endpoints: args.Endpoints, Ports: args.Ports,
		Credential: args.Credential, TTL: args.TTL, Reason: args.Reason,
	}, nil
}

func accessTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "request_access",
			"description": "Ask for an access grant. Tom still approves it unless a deployed policy accepts an agent attestation: then, after you obtain your coordinator's approval (the user's, where the coordinator cannot approve), run `agent-run grant attest` (this tool returns at once; the CLI `grant request` blocks up to 30 minutes unless given --no-wait), then poll grant_status. That is your claim, not verified human identity. credential hw-ssh leases the one shared SSH key: anything in the pod can copy it while installed, and removing it later is neither containment nor revocation (the reply says so). The reply includes the approval page. This does not install a grant.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type":       map[string]any{"type": "string", "enum": []string{"kube", "egress", "credential", "breakglass"}},
					"reason":     map[string]any{"type": "string"},
					"role":       map[string]any{"type": "string"},
					"namespaces": map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
					"fqdns":      map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
					"cidrs":      map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
					"endpoints":  map[string]any{"type": "array"},
					"ports":      map[string]any{"type": "array"},
					"credential": map[string]any{"type": "string"},
					"ttl":        map[string]any{"type": "string"},
				},
				"required": []string{"type", "reason"},
			},
		},
		{
			"name":        "grant_status",
			"description": "Read one grant by name, or list grants. Filters are session, phase and mine. This does not change a grant.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    map[string]any{"type": "string"},
					"session": map[string]any{"type": "string"},
					"phase":   map[string]any{"type": "string"},
					"mine":    map[string]any{"type": "boolean"},
				},
			},
		},
		{
			"name":        "release_access",
			"description": "Ask the broker to end one grant. The grant object stays as the audit record. This does not approve or install a grant.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
				"required":   []string{"name"},
			},
		},
	}
}

func isAskTool(name string) bool {
	switch name {
	case "ask", "ask_status", "list_my_asks", "withdraw":
		return true
	default:
		return false
	}
}

// Ask arguments describe intent only. Server-owned identity, answers and
// delivery fields are deliberately absent and unknown fields are refused.
func decodeAskArguments(raw json.RawMessage, out any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = json.RawMessage(`{}`)
	}
	if raw[0] != '{' {
		return usageError("ask tool arguments must be a JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return usageError("invalid ask tool arguments: %v", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return usageError("ask tool arguments must contain one JSON object")
	}
	return nil
}

func (a *app) dispatchAskTool(ctx context.Context, c *conn, name string, raw json.RawMessage) (string, error) {
	var out any
	var err error
	switch name {
	case "ask":
		var req apiv1.AskCreateRequest
		if err := decodeAskArguments(raw, &req); err != nil {
			return "", err
		}
		out, err = a.CreateAsk(ctx, c, req)
	case "ask_status", "withdraw":
		var args struct {
			ID string `json:"id"`
		}
		if err := decodeAskArguments(raw, &args); err != nil {
			return "", err
		}
		if name == "ask_status" {
			var ask apiv1.Ask
			ask, err = a.GetAsk(ctx, c, args.ID)
			out = computedAskStatus(ask, a.env.Now())
		} else {
			out, err = a.WithdrawAsk(ctx, c, args.ID)
		}
	case "list_my_asks":
		var args struct {
			Limit  *int   `json:"limit"`
			Cursor string `json:"cursor"`
		}
		if err := decodeAskArguments(raw, &args); err != nil {
			return "", err
		}
		limit := apiv1.AskListDefaultLimit
		if args.Limit != nil {
			if *args.Limit < 1 || *args.Limit > apiv1.AskListMaxLimit {
				return "", usageError("asks limit must be between 1 and %d", apiv1.AskListMaxLimit)
			}
			limit = *args.Limit
		}
		var page apiv1.AskList
		page, err = a.ListMyAsks(ctx, c, limit, args.Cursor)
		now := a.env.Now()
		asks := make([]askStatusView, 0, len(page.Asks))
		for _, ask := range page.Asks {
			asks = append(asks, computedAskStatus(ask, now))
		}
		out = struct {
			Asks       []askStatusView `json:"asks"`
			NextCursor string          `json:"nextCursor,omitempty"`
			Complete   bool            `json:"complete"`
		}{asks, page.NextCursor, page.Complete}
	default:
		return "", usageError("unknown tool %q", name)
	}
	if err != nil {
		return "", err
	}
	return mcpJSON(out)
}

// State reflects expiry at read time. Approval validity is separate computed
// metadata, never presented as an operator-stored field or new authority.
type askStatusView struct {
	apiv1.Ask
	Computed *askComputedStatus `json:"computed,omitempty"`
}

type askComputedStatus struct {
	ApprovalValid bool `json:"approvalValid"`
}

func computedAskStatus(ask apiv1.Ask, now time.Time) askStatusView {
	out := askStatusView{Ask: sessionAskView(ask)}
	out.State = ask.EffectiveState(now)
	if ask.Type == apiv1.AskTypeApproval {
		out.Computed = &askComputedStatus{ApprovalValid: ask.ApprovalValid(now)}
	}
	return out
}

func askStringSchema(min, max int) map[string]any {
	return map[string]any{"type": "string", "minLength": min, "maxLength": max}
}

func askTools() []map[string]any {
	id := map[string]any{"type": "string", "pattern": "^ask-[a-f0-9]{16}$"}
	expires := map[string]any{
		"type": "string", "format": "date-time",
		"description": "RFC 3339 instant from 1 minute to 24 hours after creation. The ask must expire no later than this scope.",
	}
	optionID := map[string]any{"type": "string", "pattern": "^[a-z0-9-]{1,24}$"}
	return []map[string]any{
		{
			"name":        "ask",
			"description": "Create an info or approval ask for the authenticated portal user. Returns the Ask JSON and id immediately, without waiting for an answer. Keep working, then read ask_status or list_my_asks for the answer. Reuse the same requestId only for retries of the identical intent. An approval applies only to its recorded scope and expiry. Requires --enable-asks on the operator.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"requestId", "type", "question"},
				"properties": map[string]any{
					"requestId": map[string]any{"type": "string", "minLength": 8, "maxLength": 64, "pattern": "^[A-Za-z0-9-]{8,64}$", "description": "Stable idempotency key for this caller and ask intent."},
					"type":      map[string]any{"type": "string", "enum": []string{"info", "approval"}},
					"question":  askStringSchema(1, apiv1.AskQuestionMaxRunes),
					"options": map[string]any{
						"type": "array", "maxItems": apiv1.AskMaxOptions,
						"items": map[string]any{
							"type": "object", "additionalProperties": false,
							"required": []string{"id", "label"},
							"properties": map[string]any{
								"id": optionID, "label": askStringSchema(1, apiv1.AskOptionLabelMaxRunes), "description": askStringSchema(0, apiv1.AskOptionDescriptionMaxRunes),
							},
						},
					},
					"recommended":   map[string]any{"type": "string", "pattern": "^([a-z0-9-]{1,24})?$", "description": "An id from options, or an empty string."},
					"allowFreeText": map[string]any{"type": "boolean"},
					"context": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"workPackage": map[string]any{"type": "integer", "minimum": 0},
							"links": map[string]any{
								"type": "array", "maxItems": apiv1.AskMaxLinks,
								"items": map[string]any{"type": "string", "format": "uri", "pattern": "^https://[!-~]+$", "minLength": 1, "maxLength": apiv1.AskLinkMaxLen},
							},
						},
					},
					"scope": map[string]any{
						"type": "object", "additionalProperties": false,
						"required": []string{"action", "target", "expiresAt"},
						"properties": map[string]any{
							"action": askStringSchema(1, apiv1.AskScopeFieldMaxRunes), "target": askStringSchema(1, apiv1.AskScopeFieldMaxRunes), "expiresAt": expires,
						},
					},
					"expiresIn": map[string]any{"type": "string", "description": "Go duration in whole seconds, from 1m to 168h, for example 24h or 90m; 7d is not accepted. Omitted or empty defaults to 24h, capped by scope.expiresAt for approvals. An explicit duration must not outlive the approval scope. Validated before sending."},
				},
				"allOf": []map[string]any{{
					"if":   map[string]any{"properties": map[string]any{"type": map[string]any{"const": "approval"}}},
					"then": map[string]any{"required": []string{"scope"}},
					"else": map[string]any{"not": map[string]any{"required": []string{"scope"}}},
				}},
				"anyOf": []map[string]any{
					{"required": []string{"type"}, "properties": map[string]any{"type": map[string]any{"const": "approval"}}},
					{"required": []string{"options"}, "properties": map[string]any{"options": map[string]any{"minItems": 1}}},
					{"required": []string{"allowFreeText"}, "properties": map[string]any{"allowFreeText": map[string]any{"const": true}}},
				},
			},
		},
		{
			"name":        "ask_status",
			"description": "Read your own Ask JSON by id, including effective state at read time and any answer (answeredBy contains displayName only). Approval records include computed.approvalValid, computed locally from the answer and scope expiry. Returns immediately; does not wait or answer an ask.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"id": id}, "required": []string{"id"},
			},
		},
		{
			"name":        "list_my_asks",
			"description": "Read one page of your own asks, newest first. Records show effective state at read time, answers with displayName only, and for approvals computed.approvalValid (computed locally). Includes the cursor for the next page, if any. Does not wait for answers.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": apiv1.AskListMaxLimit, "default": apiv1.AskListDefaultLimit},
					"cursor": map[string]any{"type": "string"},
				},
			},
		},
		{
			"name":        "withdraw",
			"description": "Withdraw your own open ask by id. Returns its Ask JSON immediately. This does not answer an ask or grant an approval.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"id": id}, "required": []string{"id"},
			},
		},
	}
}

func mcpToolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func mcpJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return string(b), err
}

func mcpResult(id, result json.RawMessage) []byte {
	raw, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{"2.0", id, result})
	return raw
}

func mcpError(id json.RawMessage, code int, msg string) []byte {
	raw, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   map[string]any  `json:"error"`
	}{"2.0", id, map[string]any{"code": code, "message": msg}})
	return raw
}

const maxMCPMessage = 1 << 20

// newMCPScanner reads MCP stdio: one JSON object per line. The buffer cap is
// the largest line the server will hold.
func newMCPScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxMCPMessage)
	return sc
}

func nextMCPMessage(sc *bufio.Scanner) ([]byte, error) {
	for {
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				if errors.Is(err, bufio.ErrTooLong) {
					return nil, fmt.Errorf("mcp message exceeds %d bytes", maxMCPMessage)
				}
				return nil, err
			}
			return nil, io.EOF
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		return append([]byte(nil), line...), nil
	}
}

func writeMCPMessage(w *bufio.Writer, body []byte) error {
	if bytes.Contains(body, []byte("\n")) {
		return fmt.Errorf("mcp message contains a newline")
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}
