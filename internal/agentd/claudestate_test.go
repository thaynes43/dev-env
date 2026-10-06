package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func TestSeedClaudeStateColdHome(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".claude.json")
	notes, err := seedClaudeState(state, "", []string{"/home/dev/work/s1", "/home/dev/repos/r"})
	if err != nil {
		t.Fatal(err)
	}
	m := readJSONMap(t, state)
	if m[keyOnboarded] != true {
		t.Errorf("%s = %v", keyOnboarded, m[keyOnboarded])
	}
	projects := m[keyProjects].(map[string]any)
	for _, d := range []string{"/home/dev/work/s1", "/home/dev/repos/r"} {
		p := projects[d].(map[string]any)
		if p[keyTrusted] != true || p[keyProjOnboard] != true {
			t.Errorf("project %s = %v", d, p)
		}
	}
	if _, ok := m[keyOAuthAccount]; ok {
		t.Error("oauthAccount seeded without a file")
	}
	fi, _ := os.Stat(state)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if len(notes) != 1 {
		t.Errorf("notes = %q", notes)
	}
}

func TestSeedClaudeStateKeepsCLIKeys(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".claude.json")
	writeFile(t, state, `{"machineID":"m-1","numStartups":7,"mcpServers":{"a":{"type":"http"}},
	  "projects":{"/home/dev/work/s1":{"lastSessionId":"x","hasTrustDialogAccepted":false}},
	  "oauthAccount":{"accountUuid":"old","emailAddress":"kept@example.com"}}`)
	acct := filepath.Join(dir, "account.json")
	writeFile(t, acct, `{"accountUuid":"acc-1","organizationUuid":"org-1"}`)

	notes, err := seedClaudeState(state, acct, []string{"/home/dev/work/s1"})
	if err != nil {
		t.Fatal(err)
	}
	m := readJSONMap(t, state)
	if m["machineID"] != "m-1" || m["numStartups"] != float64(7) || m["mcpServers"] == nil {
		t.Errorf("CLI keys not kept: %v", m)
	}
	p := m[keyProjects].(map[string]any)["/home/dev/work/s1"].(map[string]any)
	if p["lastSessionId"] != "x" || p[keyTrusted] != true {
		t.Errorf("project = %v", p)
	}
	a := m[keyOAuthAccount].(map[string]any)
	if a["accountUuid"] != "acc-1" || a["organizationUuid"] != "org-1" || a["emailAddress"] != "kept@example.com" {
		t.Errorf("oauthAccount = %v", a)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "oauthAccount seeded") {
		t.Errorf("notes = %q", notes)
	}
}

func TestSeedClaudeStateAccountFileProblems(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".claude.json")

	notes, err := seedClaudeState(state, filepath.Join(dir, "missing.json"), nil)
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), "plan 03") {
		t.Errorf("missing file: notes %q, err %v", notes, err)
	}

	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, `{"accountUuid":"only-one"}`)
	notes, err = seedClaudeState(state, bad, nil)
	if err != nil || !strings.Contains(strings.Join(notes, "\n"), "WARN oauthAccount not seeded") {
		t.Errorf("incomplete file: notes %q, err %v", notes, err)
	}
	if _, ok := readJSONMap(t, state)[keyOAuthAccount]; ok {
		t.Error("an incomplete account was seeded")
	}
}

func TestSeedClaudeStateLeavesBadFile(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".claude.json")
	writeFile(t, state, `not json`)
	if _, err := seedClaudeState(state, "", []string{"/x"}); err == nil {
		t.Fatal("no error for an unparseable state file")
	}
	data, _ := os.ReadFile(state)
	if string(data) != "not json" {
		t.Errorf("file changed to %q", data)
	}
}

func TestAssertDefaultModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	writeFile(t, path, `{"model":"claude-opus-4-8","permissions":{"defaultMode":"bypassPermissions"}}`)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	note, err := assertDefaultModel(path, "claude-opus-5-5")
	if err != nil || note != "claude default model set to claude-opus-5-5" {
		t.Fatalf("note %q, err %v", note, err)
	}
	m := readJSONMap(t, path)
	if m["model"] != "claude-opus-5-5" || m["permissions"] == nil {
		t.Errorf("settings = %v", m)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 kept", fi.Mode().Perm())
	}
	if note, _ := assertDefaultModel(path, "claude-opus-5-5"); note != "claude default model already claude-opus-5-5" {
		t.Errorf("second run: %q", note)
	}
	empty := filepath.Join(dir, "empty.json")
	writeFile(t, empty, "")
	if _, err := assertDefaultModel(empty, "claude-opus-5-5"); err != nil {
		t.Errorf("empty file: %v", err)
	}
	if _, err := assertDefaultModel(filepath.Join(dir, "new", "settings.json"), "claude-opus-5-5"); err != nil {
		t.Errorf("missing file: %v", err)
	}
}
