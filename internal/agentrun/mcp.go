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

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/version"
)

// mcp is the in-pod dev-env access server (DESIGN-001 6.12, plan 07 step 7).
// It speaks MCP on stdin and stdout. The three tools call the existing
// /v1/grants routes. They do not approve a grant or install one.

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
		result = map[string]any{"tools": accessTools()}
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
		return mcpJSON(out)
	case "grant_status":
		if args.Name != "" {
			if err := protocol.ValidGrantName(args.Name); err != nil {
				return "", err
			}
			var out apiv1.Grant
			if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantPath(args.Name), nil, nil, &out); err != nil {
				return "", err
			}
			return mcpJSON(out)
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
		return mcpJSON(out)
	case "release_access":
		if err := protocol.ValidGrantName(args.Name); err != nil {
			return "", err
		}
		var out apiv1.Grant
		if _, err := a.call(ctx, c, http.MethodDelete, apiv1.GrantPath(args.Name), nil, nil, &out); err != nil {
			return "", err
		}
		return mcpJSON(out)
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
	for _, p := range args.Ports {
		if p.Port < 1 || p.Port > 65535 {
			return apiv1.CreateGrantRequest{}, usageError("port %d is outside 1-65535", p.Port)
		}
		switch strings.ToUpper(p.Protocol) {
		case "", "TCP", "UDP":
		default:
			return apiv1.CreateGrantRequest{}, usageError("port protocol is tcp or udp, not %q", p.Protocol)
		}
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
			"description": "Ask for an access grant. Tom still approves it. The reply includes the approval page. This does not install a grant.",
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
