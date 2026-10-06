package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// varRef matches what envsubst substitutes: ${NAME} and $NAME.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// exactVar matches a value that is exactly one ${NAME} reference.
var exactVar = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// expander substitutes ${NAME} and $NAME from the pod's environment the way
// envsubst does (an unset variable becomes empty) and remembers which names
// were unset, empty or padded with whitespace, so a warning can name them.
// It never records a value.
type expander struct {
	getenv func(string) string
	issues map[string]string // name -> "is not set" or "has surrounding whitespace"
}

func newExpander(getenv func(string) string) *expander {
	return &expander{getenv: getenv, issues: map[string]string{}}
}

func (e *expander) expand(s string) string {
	return varRef.ReplaceAllStringFunc(s, func(m string) string {
		sub := varRef.FindStringSubmatch(m)
		name := sub[1]
		if name == "" {
			name = sub[2]
		}
		v := e.getenv(name)
		e.check(name, v)
		return v
	})
}

// check records an issue with a variable that a server references by name.
func (e *expander) check(name, v string) {
	switch {
	case v == "":
		e.issues[name] = "is not set"
	case strings.TrimSpace(v) != v:
		e.issues[name] = "has surrounding whitespace"
	}
}

// warnings lists the recorded issues, sorted, and clears them.
func (e *expander) warnings() []string {
	var out []string
	for name, issue := range e.issues {
		out = append(out, fmt.Sprintf("$%s %s", name, issue))
	}
	sort.Strings(out)
	e.issues = map[string]string{}
	return out
}

// mcpConfig is config/claude/mcp.json: the one MCP list both agents share
// (D-14). Each server spec is kept raw and decoded where it is used.
type mcpConfig struct {
	Servers map[string]json.RawMessage `json:"mcpServers"`
}

func readMCPConfig(path string) (mcpConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return mcpConfig{}, err
	}
	var c mcpConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return mcpConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// names returns the server names, sorted.
func (c mcpConfig) names() []string {
	out := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// expandSpec substitutes variables in every string value of a server spec, as
// v1's `envsubst` over the compact JSON did, but inside decoded strings, so a
// value cannot break the JSON. Keys are left as they are.
func expandSpec(raw json.RawMessage, e *expander) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	v = walkStrings(v, e.expand)
	return marshalNoEscape(v)
}

func walkStrings(v any, f func(string) string) any {
	switch t := v.(type) {
	case string:
		return f(t)
	case []any:
		for i := range t {
			t[i] = walkStrings(t[i], f)
		}
		return t
	case map[string]any:
		for k, x := range t {
			t[k] = walkStrings(x, f)
		}
		return t
	default:
		return v
	}
}

// marshalNoEscape is json.Marshal without HTML escaping and the trailing
// newline an Encoder adds.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// mcpManagedFile records the servers agentd registered, so a server that
// leaves mcp.json is removed at the next boot on the same volume.
const mcpManagedFile = "mcp-managed.json"

// registerMCP runs v1's loop (D-14): `claude mcp add-json -s user` for each
// server in mcp.json, with ${VAR} expanded from the pod's environment. A server
// that is already registered is removed first, so the spec in git wins. It
// never logs a spec: specs carry tokens and secret URL paths.
func registerMCP(ctx context.Context, r Runner, s Settings, cfg mcpConfig) []string {
	var notes []string
	existing := userMCPServers(s.ClaudeState)
	managedPath := filepath.Join(s.StateDir, mcpManagedFile)
	var managed []string
	if data, err := os.ReadFile(managedPath); err == nil {
		_ = json.Unmarshal(data, &managed)
	}

	e := newExpander(s.Getenv)
	var registered []string
	for _, name := range cfg.names() {
		spec, err := expandSpec(cfg.Servers[name], e)
		if err != nil {
			notes = append(notes, fmt.Sprintf("WARN mcp %q: spec is not valid JSON: %v", name, err))
			continue
		}
		for _, w := range e.warnings() {
			notes = append(notes, fmt.Sprintf("WARN mcp %q: %s", name, w))
		}
		if existing[name] {
			_, _ = r.Run(ctx, Cmd{Name: s.ClaudeBin, Args: []string{"mcp", "remove", "-s", "user", name}})
		}
		if _, err := r.Run(ctx, Cmd{Name: s.ClaudeBin, Args: []string{"mcp", "add-json", "-s", "user", name, string(spec)}}); err != nil {
			notes = append(notes, fmt.Sprintf("WARN mcp %q registration failed (%v)", name, err))
			continue
		}
		registered = append(registered, name)
	}
	for _, name := range managed {
		if _, ok := cfg.Servers[name]; ok {
			continue
		}
		if _, err := r.Run(ctx, Cmd{Name: s.ClaudeBin, Args: []string{"mcp", "remove", "-s", "user", name}}); err == nil {
			notes = append(notes, fmt.Sprintf("mcp %q removed (no longer in mcp.json)", name))
		}
	}
	if data, err := json.Marshal(registered); err == nil {
		if err := writeFileAtomic(managedPath, data, 0o600); err != nil {
			notes = append(notes, fmt.Sprintf("WARN could not record the registered MCP servers: %v", err))
		}
	}
	notes = append(notes, fmt.Sprintf("%d of %d MCP servers registered", len(registered), len(cfg.Servers)))
	return notes
}

// userMCPServers lists the user-scope servers in the CLI's state file. Any
// error means "none known", which only costs a skipped remove.
func userMCPServers(statePath string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return out
	}
	var st struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(data, &st) != nil {
		return out
	}
	for n := range st.MCPServers {
		out[n] = true
	}
	return out
}

// forwardVars are passed to every Codex stdio server, which otherwise gets only
// a core environment (v1 mcp-json-to-codex-toml.sh FORWARD_VARS).
// PLAYWRIGHT_BROWSERS_PATH is the one that matters: it points at the volume's
// browser cache.
var forwardVars = []string{"HOME", "PATH", "LANG", "TERM", "TMPDIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "PLAYWRIGHT_BROWSERS_PATH", "UV_CACHE_DIR"}

// mcpServerSpec is the part of a Claude MCP spec the Codex rendering reads.
type mcpServerSpec struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// renderCodexMCP is a port of v1's mcp-json-to-codex-toml.sh: claude's mcp.json
// as Codex `[mcp_servers.*]` TOML. A value that is exactly ${VAR} becomes a
// reference by name (env_vars, env_http_headers, bearer_token_env_var), so
// Codex reads the variable when it connects and no secret is written to the
// file. ${VAR} inside a longer string (a URL path, a non-Bearer header) is
// expanded, as Claude's registration does. SSE servers are skipped: Codex
// speaks streamable HTTP only.
func renderCodexMCP(cfg mcpConfig, e *expander) (string, error) {
	var b strings.Builder
	for _, name := range cfg.names() {
		var s mcpServerSpec
		if err := json.Unmarshal(cfg.Servers[name], &s); err != nil {
			return "", fmt.Errorf("mcp server %q: %w", name, err)
		}
		typ := s.Type
		if typ == "" {
			if s.Command != "" {
				typ = "stdio"
			} else {
				typ = "http"
			}
		}
		var lines []string
		switch typ {
		case "sse":
			lines = []string{fmt.Sprintf("# %s: type=sse in mcp.json — codex has no SSE client (streamable HTTP only), skipped", name)}
		case "stdio":
			lines = append(lines, "[mcp_servers."+tomlKey(name)+"]", "command = "+tomlString(e.expand(s.Command)))
			if len(s.Args) > 0 {
				args := make([]string, len(s.Args))
				for i, a := range s.Args {
					args[i] = tomlString(e.expand(a))
				}
				lines = append(lines, "args = ["+strings.Join(args, ", ")+"]")
			}
			byName := append([]string(nil), forwardVars...)
			literal := map[string]string{}
			for k, v := range s.Env {
				if m := exactVar.FindStringSubmatch(v); m != nil {
					byName = append(byName, m[1])
					e.check(m[1], e.getenv(m[1]))
				} else {
					literal[k] = e.expand(v)
				}
			}
			if len(literal) > 0 {
				lines = append(lines, "env = "+tomlInline(literal))
			}
			sort.Strings(byName)
			byName = slices.Compact(byName)
			quoted := make([]string, len(byName))
			for i, n := range byName {
				quoted[i] = tomlString(n)
			}
			lines = append(lines, "env_vars = ["+strings.Join(quoted, ", ")+"]", "startup_timeout_sec = 60")
		default:
			lines = append(lines, "[mcp_servers."+tomlKey(name)+"]", "url = "+tomlString(e.expand(s.URL)))
			bearer := ""
			for k, v := range s.Headers {
				if strings.EqualFold(k, "authorization") && strings.HasPrefix(v, "Bearer ") {
					if m := exactVar.FindStringSubmatch(strings.TrimPrefix(v, "Bearer ")); m != nil {
						bearer = m[1]
					}
				}
			}
			byName := map[string]string{}
			literal := map[string]string{}
			for k, v := range s.Headers {
				if bearer != "" && strings.EqualFold(k, "authorization") {
					continue
				}
				if m := exactVar.FindStringSubmatch(v); m != nil {
					byName[k] = m[1]
					e.check(m[1], e.getenv(m[1]))
				} else {
					literal[k] = e.expand(v)
				}
			}
			if bearer != "" {
				lines = append(lines, "bearer_token_env_var = "+tomlString(bearer))
				e.check(bearer, e.getenv(bearer))
			}
			if len(byName) > 0 {
				lines = append(lines, "env_http_headers = "+tomlInline(byName))
			}
			if len(literal) > 0 {
				lines = append(lines, "http_headers = "+tomlInline(literal))
			}
		}
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n\n")
	}
	return b.String(), nil
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey is a bare key when TOML allows one, else a quoted key.
func tomlKey(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return tomlString(k)
}

// tomlString quotes s as a TOML basic string. JSON string escapes are valid
// TOML basic-string escapes, which is what v1's jq `@json` relied on.
func tomlString(s string) string {
	b, err := marshalNoEscape(s)
	if err != nil { // a string always marshals
		return `""`
	}
	return string(b)
}

// tomlInline renders a map as an inline table with sorted keys.
func tomlInline(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = tomlKey(k) + " = " + tomlString(m[k])
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
