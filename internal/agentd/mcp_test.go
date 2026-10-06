package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExpanderMatchesEnvsubst(t *testing.T) {
	e := newExpander(envOf(map[string]string{"A": "1", "B_2": "two", "PAD": " x "}))
	cases := map[string]string{
		"${A}":           "1",
		"$A-$B_2":        "1-two",
		"x${B_2}y":       "xtwoy",
		"${UNSET}z":      "z",
		"$1 and $$ stay": "$1 and $$ stay",
		"${not valid}":   "${not valid}",
		"no refs":        "no refs",
		"${PAD}":         " x ",
	}
	for in, want := range cases {
		if got := e.expand(in); got != want {
			t.Errorf("expand(%q) = %q, want %q", in, got, want)
		}
	}
	want := []string{"$PAD has surrounding whitespace", "$UNSET is not set"}
	if got := e.warnings(); !reflect.DeepEqual(got, want) {
		t.Errorf("warnings = %q, want %q", got, want)
	}
	if got := e.warnings(); len(got) != 0 {
		t.Errorf("warnings not cleared: %q", got)
	}
}

func TestExpandSpecKeepsJSONValid(t *testing.T) {
	e := newExpander(envOf(map[string]string{"TOK": `a"b\c`, "P": "/p"}))
	raw := json.RawMessage(`{"type":"http","url":"http://h${P}","headers":{"Authorization":"Bearer ${TOK}"},"n":1.50}`)
	out, err := expandSpec(raw, e)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("expanded spec is not JSON: %v: %s", err, out)
	}
	if got["url"] != "http://h/p" {
		t.Errorf("url = %v", got["url"])
	}
	if h := got["headers"].(map[string]any); h["Authorization"] != `Bearer a"b\c` {
		t.Errorf("Authorization = %v", h["Authorization"])
	}
	if !strings.Contains(string(out), `"n":1.50`) {
		t.Errorf("number not kept verbatim: %s", out)
	}
}

func TestRenderCodexMCPMatchesV1(t *testing.T) {
	// testdata/codex-mcp.golden.toml is the output of v1's
	// mcp-json-to-codex-toml.sh on testdata/mcp.json, run with
	// HA_MCP_SECRET_PATH=/private/abc123 WEIRD_TOKEN=w-tok
	// CIGAR_JOURNAL_TOKEN=should-not-appear.
	mc, err := readMCPConfig(filepath.Join("testdata", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HA_MCP_SECRET_PATH": "/private/abc123", "WEIRD_TOKEN": "w-tok", "CIGAR_JOURNAL_TOKEN": "should-not-appear"}
	e := newExpander(envOf(env))
	got, err := renderCodexMCP(mc, e)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "codex-mcp.golden.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("rendered TOML differs from v1's:\n--- got\n%s\n--- want\n%s", got, want)
	}
	if strings.Contains(got, "should-not-appear") {
		t.Error("a bearer token referenced by name was written into the file")
	}
	// Referenced but unset: named in the warnings, never valued.
	w := strings.Join(e.warnings(), "\n")
	for _, name := range []string{"$OUTLINE_API_KEY is not set", "$VEXA_API_KEY is not set"} {
		if !strings.Contains(w, name) {
			t.Errorf("warnings %q lack %q", w, name)
		}
	}
}

func TestRegisterMCP(t *testing.T) {
	home := t.TempDir()
	s := testSettings(t, home)
	s.Getenv = envOf(map[string]string{"TOK": "secret-token-value"})
	// One server is already registered (a resume on the same volume), and one
	// that agentd registered at an earlier boot has left mcp.json.
	writeJSON(t, s.ClaudeState, map[string]any{"mcpServers": map[string]any{"alpha": map[string]any{}, "gone": map[string]any{}}})
	writeJSON(t, filepath.Join(s.StateDir, mcpManagedFile), []string{"alpha", "gone"})
	mc := mcpConfig{Servers: map[string]json.RawMessage{
		"alpha": json.RawMessage(`{"type":"http","url":"http://a/mcp","headers":{"Authorization":"Bearer ${TOK}"}}`),
		"beta":  json.RawMessage(`{"command":"beta-mcp"}`),
		"broke": json.RawMessage(`{"command":"x"}`),
	}}
	f := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if len(c.Args) > 4 && c.Args[1] == "add-json" && c.Args[4] == "broke" {
			return Result{ExitCode: 1}, &CmdError{Name: c.Name, Sub: c.Args[0], ExitCode: 1, Stderr: "spec " + strings.Join(c.Args, " ")}
		}
		return Result{}, nil
	}}

	notes := registerMCP(context.Background(), f, s, mc)

	want := []string{
		"claude mcp remove -s user alpha",
		`claude mcp add-json -s user alpha {"headers":{"Authorization":"Bearer secret-token-value"},"type":"http","url":"http://a/mcp"}`,
		`claude mcp add-json -s user beta {"command":"beta-mcp"}`,
		`claude mcp add-json -s user broke {"command":"x"}`,
		"claude mcp remove -s user gone",
	}
	if got := f.lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	joined := strings.Join(notes, "\n")
	if strings.Contains(joined, "secret-token-value") {
		t.Errorf("a secret reached the notes: %s", joined)
	}
	for _, want := range []string{`WARN mcp "broke" registration failed (claude mcp: exit status 1)`, `mcp "gone" removed`, "2 of 3 MCP servers registered"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes lack %q:\n%s", want, joined)
		}
	}
	var managed []string
	data, _ := os.ReadFile(filepath.Join(s.StateDir, mcpManagedFile))
	_ = json.Unmarshal(data, &managed)
	if !reflect.DeepEqual(managed, []string{"alpha", "beta"}) {
		t.Errorf("managed = %q", managed)
	}
}

func TestCmdErrorHidesArguments(t *testing.T) {
	err := error(&CmdError{Name: "claude", Sub: "mcp", ExitCode: 3, Stderr: "boom"})
	if err.Error() != "claude mcp: exit status 3" {
		t.Errorf("Error() = %q", err.Error())
	}
	var ce *CmdError
	if !errors.As(err, &ce) || ce.Detail() != "claude mcp: exit status 3: boom" {
		t.Errorf("Detail() = %q", ce.Detail())
	}
	if ExitCodeOf(err) != 3 || ExitCodeOf(errors.New("x")) != -1 {
		t.Error("ExitCodeOf")
	}
}

// testSettings points every path at a temporary home.
func testSettings(t *testing.T, home string) Settings {
	t.Helper()
	env := map[string]string{
		"HOME":                      home,
		"CLAUDE_CONFIG_DIR":         filepath.Join(home, ".claude"),
		"XDG_RUNTIME_DIR":           filepath.Join(home, "run"),
		"AGENTD_CONFIG_DIR":         filepath.Join(home, "cfg"),
		"AGENTD_PLAYWRIGHT_STAGING": filepath.Join(home, "image", "ms-playwright"),
		"AGENTD_BASHRC":             filepath.Join(home, "image", "bashrc.sh"),
		"AGENTD_GH_TOKEN_FILE":      filepath.Join(home, "creds", "gh_token"),
	}
	s, err := LoadSettings(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	s.SystemBin = filepath.Join(home, "image", "bin")
	return s
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
