// Package frontdoor is Meatball's MCP server (DESIGN-060 section 6).
// It serves streamable HTTP at /mcp, accepts one bearer token, and starts
// coordinator sessions through the same operator calls as agent-run.
package frontdoor

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/tokenfile"
	"github.com/thaynes43/dev-env/internal/version"
)

const (
	mcpPath        = "/mcp"
	mcpProtocol    = "2025-03-26"
	maxBody        = 1 << 20
	defaultListen  = ":8080"
	defaultProject = "haynes-engineering"
	defaultOpwork  = "opwork"
	bearerHeader   = "Authorization"
	sessionHeader  = "Mcp-Session-Id"
)

// serverInstructions is InitializeResult.instructions in the MCP schema for
// protocol 2025-03-26 and 2025-11-25, beside protocolVersion, capabilities
// and serverInfo. It is 58 words, under the 120 word limit.
const serverInstructions = "This server is Meatball's front door to the haynes-engineering tracker and to coordinator sessions. The server and the operator enforce the caller's limits, so call a tool and report the result rather than asking the owner first. A refusal is final. Record it and do not work around it. standing_authority returns the owner's standing instruction for this caller."

// toolDeadline is one deadline for a tool call, shared by the operator client
// and the opwork process. It starts when the call is admitted and ends early
// when the client disconnects. toolSlots is how many tool calls may run at once.
var (
	toolDeadline = 90 * time.Second
	toolSlots    = 4
)

// Config is the process configuration. BearerFile is the one MCP bearer token.
// TrackerFile is the OpenProject credential. OperatorTokenFile is this
// ServiceAccount's projected token for the operator. None of the three is logged.
// AuthorityFile is the optional standing-authority text. It is not a credential.
type Config struct {
	Listen            string
	BearerFile        string
	TrackerFile       string
	OperatorTokenFile string
	APIURL            string
	CAFile            string
	Opwork            string
	Project           string
	AuthorityFile     string
}

// Serve listens until ctx is cancelled. A missing token file or credential
// file stops the process before it accepts a connection.
func Serve(ctx context.Context, cfg Config, logw io.Writer) error {
	if logw == nil {
		logw = os.Stderr
	}
	cfg.Listen = listenAddr(cfg.Listen)
	logger := log.New(logw, "dev-env-frontdoor: ", log.LstdFlags)
	h, op, err := newHandler(cfg, logger)
	if err != nil {
		return err
	}
	defer op.Close()
	srv := newHTTPServer(cfg.Listen, h, logger)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// newHTTPServer is the process's HTTP server. DisableGeneralOptionsHandler
// makes OPTIONS * go through this handler, so a missing token is 401.
func newHTTPServer(addr string, h http.Handler, logger *log.Logger) *http.Server {
	return &http.Server{
		Addr:                         addr,
		Handler:                      h,
		ReadHeaderTimeout:            10 * time.Second,
		ReadTimeout:                  30 * time.Second,
		WriteTimeout:                 2 * time.Minute,
		IdleTimeout:                  2 * time.Minute,
		MaxHeaderBytes:               1 << 16,
		ErrorLog:                     logger,
		DisableGeneralOptionsHandler: true,
	}
}

type handler struct {
	bearerFile    string
	authorityFile string
	op            *agentrun.Operator
	work          opwork
	log           *log.Logger
	slots         chan struct{}
}

func listenAddr(listen string) string {
	if listen == "" {
		return defaultListen
	}
	return listen
}

func newHandler(cfg Config, logger *log.Logger) (*handler, *agentrun.Operator, error) {
	if cfg.Opwork == "" {
		cfg.Opwork = defaultOpwork
	}
	if cfg.Project == "" {
		cfg.Project = defaultProject
	}
	if cfg.BearerFile == "" || cfg.TrackerFile == "" {
		return nil, nil, errors.New("the bearer token file and the tracker credential file are required")
	}
	if _, err := readBearer(cfg.BearerFile); err != nil {
		return nil, nil, errors.New("the bearer token file is unreadable or empty")
	}
	info, err := os.Stat(cfg.TrackerFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > tokenfile.MaxBytes {
		return nil, nil, errors.New("the tracker credential file is unreadable or empty")
	}
	op, err := agentrun.OpenOperator(cfg.APIURL, cfg.OperatorTokenFile, cfg.CAFile)
	if err != nil {
		return nil, nil, err
	}
	h := &handler{
		bearerFile:    cfg.BearerFile,
		authorityFile: cfg.AuthorityFile,
		op:            op,
		work:          opwork{path: cfg.Opwork, tokenFile: cfg.TrackerFile, project: cfg.Project},
		log:           logger,
		slots:         make(chan struct{}, toolSlots),
	}
	return h, op, nil
}

// fixedLabel is an allowlisted log token. Anything else is the word unknown,
// so a client-supplied path or method is never copied into the log.
func fixedLabel(value string, allowed ...string) string {
	for _, one := range allowed {
		if value == one {
			return one
		}
	}
	return "unknown"
}

func knownMethod(method string) string {
	return fixedLabel(method,
		http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace)
}

func knownPath(path string) string {
	return fixedLabel(path, mcpPath)
}

func knownRPC(method string) string {
	return fixedLabel(method, "initialize", "ping", "tools/list", "tools/call")
}

func knownTool(name string) string {
	for _, tool := range frontDoorTools {
		canonical, _ := tool["name"].(string)
		if name == canonical && canonical != "" {
			return canonical
		}
	}
	return "unknown"
}

// admit takes one tool-call slot. Past the limit it returns false and the
// caller answers busy without starting operator or opwork work.
func (h *handler) admit() bool {
	select {
	case h.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (h *handler) release() {
	select {
	case <-h.slots:
	default:
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bearer, ok := h.authorized(r)
	if !ok {
		h.log.Printf("http %s %s 401", knownMethod(r.Method), knownPath(r.URL.Path))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != mcpPath {
		h.log.Printf("http %s %s 404", knownMethod(r.Method), knownPath(r.URL.Path))
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.post(w, r, bearer)
	case http.MethodDelete:
		w.WriteHeader(http.StatusAccepted)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *handler) authorized(r *http.Request) (string, bool) {
	got, ok := presentedBearer(r)
	if !ok {
		return "", false
	}
	want, err := readBearer(h.bearerFile)
	if err != nil || len(want) == 0 || len(got) != len(want) {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", false
	}
	return want, true
}

func presentedBearer(r *http.Request) (string, bool) {
	scheme, rest, ok := strings.Cut(r.Header.Get(bearerHeader), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.ContainsAny(rest, " \t") {
		return "", false
	}
	return rest, true
}

func readBearer(path string) (string, error) {
	b, err := tokenfile.Read(path, tokenfile.MaxBytes)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("empty")
	}
	return tok, nil
}

func (h *handler) post(w http.ResponseWriter, r *http.Request, bearer string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		h.reply(w, r, mcpError(json.RawMessage("null"), -32700, "parse error"))
		return
	}
	var req struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Method  string           `json:"method"`
		Params  json.RawMessage  `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		h.reply(w, r, mcpError(json.RawMessage("null"), -32600, "invalid request"))
		return
	}
	if req.ID == nil || string(*req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rpcErr, session := h.dispatch(r.Context(), req.Method, req.Params, bearer)
	if rpcErr != nil {
		h.log.Printf("rpc %s %d", knownRPC(req.Method), rpcErr.Code)
		h.reply(w, r, mcpError(*req.ID, rpcErr.Code, rpcErr.Message))
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		h.reply(w, r, mcpError(*req.ID, -32603, "internal error"))
		return
	}
	if knownRPC(req.Method) == "tools/call" {
		var call struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &call)
		h.log.Printf("rpc tools/call %s", knownTool(call.Name))
	} else {
		h.log.Printf("rpc %s", knownRPC(req.Method))
	}
	if session != "" {
		w.Header().Set(sessionHeader, session)
	}
	h.reply(w, r, mcpResult(*req.ID, raw))
}

type rpcError struct {
	Code    int
	Message string
}

func (h *handler) dispatch(ctx context.Context, method string, params json.RawMessage, bearer string) (any, *rpcError, string) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": negotiate(params),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "dev-env-frontdoor", "version": version.Get().Version},
			"instructions":    serverInstructions,
		}, nil, newSession()
	case "ping":
		return map[string]any{}, nil, ""
	case "tools/list":
		return map[string]any{"tools": frontDoorTools}, nil, ""
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil || call.Name == "" {
			return nil, &rpcError{-32602, "invalid tool call"}, ""
		}
		known := false
		for _, tool := range frontDoorTools {
			if tool["name"] == call.Name {
				known = true
				break
			}
		}
		if !known {
			return nil, &rpcError{-32602, "unknown tool"}, ""
		}
		if !h.admit() {
			return toolError("busy, try again"), nil, ""
		}
		defer h.release()
		// One deadline for the operator client and opwork. The parent is the
		// request, so a disconnect cancels both. opwork does not extend it.
		ctx, cancel := context.WithTimeout(ctx, toolDeadline)
		defer cancel()
		text, err := h.callTool(ctx, call.Name, call.Arguments)
		if err != nil {
			msg := scrub(err.Error(), bearer)
			if text != "" {
				msg = scrub(text, bearer) + "\n" + msg
			}
			return toolError(msg), nil, ""
		}
		return toolOK(scrub(text, bearer)), nil, ""
	default:
		return nil, &rpcError{-32601, "method not found"}, ""
	}
}

func negotiate(params json.RawMessage) string {
	var body struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &body)
	switch body.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
		return body.ProtocolVersion
	default:
		return mcpProtocol
	}
}

func newSession() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "session"
	}
	return hex.EncodeToString(b[:])
}

func toolOK(text string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": false}
}

func toolError(text string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": true}
}

func (h *handler) reply(w http.ResponseWriter, r *http.Request, payload []byte) {
	if sseOnly(r) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func sseOnly(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/event-stream") && !strings.Contains(accept, "application/json") && !strings.Contains(accept, "*/*")
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
