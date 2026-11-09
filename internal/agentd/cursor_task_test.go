package agentd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// syntheticCursorKey stands in for the Cursor API key. It is not a key.
const syntheticCursorKey = "synthetic-test-value-not-a-key"

// cursorFixture is a cursor project task with the pod's Cursor key, the
// platform's common rules on disk and the identities usage binding needs.
func cursorFixture(t *testing.T) (Settings, protocol.Session, protocol.Workspace) {
	t.Helper()
	s, sess, ws := launchFixture(t)
	s.Getenv = envOf(map[string]string{cursorAPIKeyEnv: syntheticCursorKey})
	s.PodUID = "original-pod"
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"), "common rules\n")
	catalog, err := projectcatalog.Parse([]byte(projectTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ProjectSnapshot, err = json.Marshal(snapshot); err != nil {
		t.Fatal(err)
	}
	sess.SessionUID, sess.Repo, sess.Base = "existing-session-uid", "demo", "main"
	sess.Agent, sess.Model, sess.Effort = protocol.AgentCursor, "grok-code-fast-1", ""
	sess.Limits = &protocol.Limits{Timeout: "40m0s"}
	return s, sess, ws
}

func TestCursorLaunchContract(t *testing.T) {
	s, sess, ws := cursorFixture(t)
	l, err := BuildLaunch(s, sess, ws, "boot-1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Argv) != 11 {
		t.Fatalf("argv %q", l.Argv)
	}
	want := []string{cursorCLIPath(s), "-p", "--force", "--trust", "--output-format", "stream-json", "--model", "grok-code-fast-1", "--workspace", ws.Worktree}
	if !slices.Equal(l.Argv[:10], want) {
		t.Fatalf("argv %q, want %q and the prompt", l.Argv, want)
	}
	prompt := l.Argv[10]
	first, _, _ := strings.Cut(prompt, "\n")
	if !strings.HasPrefix(prompt, "Before you start, read and follow the platform rules in "+filepath.Join(s.ClaudeConfigDir, "CLAUDE.md")+" and the project rules in ") ||
		!strings.Contains(first, s.StateDir) || !strings.Contains(prompt, "NEVER push to main") || !strings.HasSuffix(prompt, "\n\n"+sess.Prompt) {
		t.Fatalf("prompt does not name the rules, carry the guard and end with the task:\n%s", prompt)
	}
	if l.Prompt != "" || l.Provider != protocol.AgentCursor || l.Dir != ws.Worktree || l.Timeout != 40*time.Minute || l.ConversationID != "" {
		t.Fatalf("launch %+v", l)
	}
	if l.UsageSource == nil || !l.UsageSource.Valid() || l.UsageSource.Provider != protocol.AgentCursor || l.UsageSource.RequestedModel != "grok-code-fast-1" {
		t.Fatalf("usage source %+v", l.UsageSource)
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY", "GH_TOKEN", protocol.SessionEnv} {
		if !slices.Contains(l.Unset, k) {
			t.Errorf("a cursor agent keeps %s", k)
		}
	}
	if slices.Contains(l.Unset, cursorAPIKeyEnv) {
		t.Error("the cursor agent loses its own key")
	}
	if launchBillingBasis(l) != protocol.UsageUnknown {
		t.Error("a cursor launch claims a billing basis")
	}

	// A Claude or Codex agent never keeps the Cursor key.
	cs, csess, cws := launchFixture(t)
	cl, err := BuildLaunch(cs, csess, cws, "boot-1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cl.Unset, cursorAPIKeyEnv) || !slices.Contains(unsetForRemote, cursorAPIKeyEnv) || !slices.Contains(append(append([]string{}, unsetForAgent...), codexUnset...), cursorAPIKeyEnv) {
		t.Error("a Claude or Codex agent can inherit the Cursor key")
	}

	// A cursor task never resumes, whatever its first launch recorded.
	if _, err := BuildResume(s, sess, ws, l, "boot-2", rescueNow); err == nil || !strings.Contains(err.Error(), "does not resume") {
		t.Fatal("a cursor task resumed", err)
	}
	if rep := launchRestart(s, sess.Name); rep.Resumable {
		t.Fatal("a cursor launch reported resumable")
	}
}

func TestCursorLaunchRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*Settings, *protocol.Session)
		want   string
	}{
		"no key":          {func(s *Settings, _ *protocol.Session) { s.Getenv = envOf(nil) }, "CURSOR_API_KEY is not set"},
		"effort":          {func(_ *Settings, sess *protocol.Session) { sess.Effort = "high" }, "Cursor encodes effort in the model id"},
		"local":           {func(_ *Settings, sess *protocol.Session) { sess.Mode, sess.Prompt = protocol.ModeLocal, "" }, "task mode only"},
		"no project":      {func(_ *Settings, sess *protocol.Session) { sess.ProjectSnapshot = nil }, "accepted project snapshot"},
		"max turns":       {func(_ *Settings, sess *protocol.Session) { sess.Limits.MaxTurns = 3 }, "no max-turns flag"},
		"model like flag": {func(_ *Settings, sess *protocol.Session) { sess.Model = "-v" }, "not a Cursor model id"},
	} {
		t.Run(name, func(t *testing.T) {
			s, sess, ws := cursorFixture(t)
			tc.mutate(&s, &sess)
			if _, err := BuildLaunch(s, sess, ws, "boot-1", rescueNow); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildLaunch = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// An agent this agentd does not know is refused, never launched as Claude.
func TestUnknownAgentIsNotLaunchedAsClaude(t *testing.T) {
	s, sess, ws := launchFixture(t)
	sess.Agent = "gemini"
	if _, err := BuildLaunch(s, sess, ws, "boot-1", rescueNow); !errors.Is(err, ErrNotInPlan01) {
		t.Fatal("an unknown agent was not refused", err)
	}
	first := Launch{Session: sess.Name, ConversationID: "c", Dir: ws.Worktree}
	if _, err := BuildResume(s, sess, ws, first, "boot-2", rescueNow); !errors.Is(err, ErrNotInPlan01) {
		t.Fatal("an unknown agent was resumed", err)
	}
}

// fakeCursorCLI writes a stand-in for the Cursor CLI: it prints version on
// --version, and otherwise checks its arguments and environment, prints the
// stream file and exits with code.
func fakeCursorCLI(t *testing.T, dir, version, stream string, code int, marker string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, "stream.jsonl"), stream)
	p := filepath.Join(dir, "fake-cursor-agent")
	body := `#!/bin/sh
if [ "$1" = "--version" ]; then [ -z "$CURSOR_API_KEY$ANTHROPIC_API_KEY$CLAUDE_CODE_OAUTH_TOKEN" ] || exit 80; echo "` + version + `"; exit 0; fi
: > "` + marker + `"
[ "$CURSOR_API_KEY" = "` + syntheticCursorKey + `" ] || { echo "no cursor key" >&2; exit 90; }
[ -z "$ANTHROPIC_API_KEY" ] || { echo "metered key leaked" >&2; exit 91; }
[ -z "$CLAUDE_CODE_OAUTH_TOKEN" ] || { echo "claude token leaked" >&2; exit 92; }
[ "$1 $2 $3" = "-p --force --trust" ] || { echo "bad flags" >&2; exit 93; }
for last; do :; done
[ "$last" = "the task" ] || { echo "bad prompt" >&2; exit 94; }
if read -r extra; then echo "stdin was not empty" >&2; exit 95; fi
cat "` + filepath.Join(dir, "stream.jsonl") + `"
exit ` + itoaInt(code) + `
`
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func itoaInt(n int) string { b, _ := json.Marshal(n); return string(b) }

// cursorStreamLines is a short Cursor stream-json task, shaped as
// https://cursor.com/docs/cli/reference/output-format shows it.
const cursorStreamLines = `{"type":"system","subtype":"init","apiKeySource":"env","cwd":"/w","session_id":"c-1","model":"Grok Code Fast","permissionMode":"default"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"the task"}]},"session_id":"c-1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Reading the README."}]},"session_id":"c-1"}
{"type":"tool_call","subtype":"started","call_id":"t1","tool_call":{"readToolCall":{"args":{"path":"README.md"}}},"session_id":"c-1"}
{"type":"tool_call","subtype":"completed","call_id":"t1","tool_call":{"readToolCall":{"args":{"path":"README.md"},"result":{"success":{"content":"x"}}}},"session_id":"c-1"}
not json at all
`

func runCursorTask(t *testing.T, version, stream string, code int) (Settings, Launch, taskResult, string, string, int) {
	t.Helper()
	s, sess, ws := cursorFixture(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	cli := fakeCursorCLI(t, dir, version, stream, code, marker)
	if err := os.MkdirAll(s.WorkDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	ws.Worktree = dir
	l, err := BuildLaunch(s, sess, ws, "boot-1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	l.Argv[0], l.Argv[len(l.Argv)-1] = cli, "the task"
	t.Setenv(cursorAPIKeyEnv, syntheticCursorKey)
	t.Setenv("ANTHROPIC_API_KEY", "metered")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "static")
	pane := &syncBuffer{}
	exit := RunAgent(writeLaunch(t, s, l), pane, make(chan os.Signal), time.Second)
	var res taskResult
	if err := readJSONFile(s.statePath(resultFile), &res); err != nil {
		t.Fatalf("no task result: %v\n%s", err, pane.String())
	}
	return s, l, res, pane.String(), marker, exit
}

func TestRunAgentCursorSuccess(t *testing.T) {
	stream := cursorStreamLines + `{"type":"result","subtype":"success","duration_ms":10,"duration_api_ms":9,"is_error":false,"result":"Done.","session_id":"c-1","request_id":"r-1"}` + "\n"
	s, l, res, out, _, exit := runCursorTask(t, cursorCLIPin.Version, stream, 0)
	if exit != 0 || res.ExitCode != 0 || res.Subtype != "success" || res.IsError || res.NumTurns != 0 {
		t.Fatalf("exit %d result %+v\n%s", exit, res.TaskResult, out)
	}
	for _, want := range []string{
		"[agentd] cursor session c-1, CLI-reported model Grok Code Fast; actual model Unknown",
		"Reading the README.",
		"-> read: README.md",
		"not json at all",
		"[agentd] result success, is_error false; usage, cost and served model Unknown (Cursor reports none)",
		"TASK-EXIT:0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pane lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "provider reported") || strings.Contains(out, "USD") {
		t.Errorf("the log labels Cursor's model or a cost as reported:\n%s", out)
	}
	if res.Usage != nil {
		t.Fatalf("a cursor result carries usage %+v", res.Usage)
	}
	u := res.UsageObservation
	if u == nil || u.Validate() != nil || u.Source != *l.UsageSource || u.ResultState != "Final" || u.Cost.State != protocol.UsageUnknown ||
		u.InputTokens.State != protocol.UsageUnknown || u.ActualModelState != protocol.UsageUnknown || len(u.ServedModels) != 0 || u.BillingBasis != protocol.UsageUnknown {
		t.Fatalf("observation %+v", u)
	}
	if res.ExitSource == nil || *res.ExitSource != *l.UsageSource {
		t.Fatal("exit source lost")
	}
	if ev, _ := os.ReadFile(l.EventsPath); !strings.Contains(string(ev), `"subtype":"success"`) {
		t.Errorf("events file:\n%s", ev)
	}
	if code := RunAgent(s.statePath(launchFile), &syncBuffer{}, make(chan os.Signal), time.Second); code != 1 {
		t.Fatal("a cursor task ran twice", code)
	}
}

func TestRunAgentCursorOutcomeIsRetained(t *testing.T) {
	s, sess, ws := cursorFixture(t)
	dir := t.TempDir()
	outcome := filepath.Join(filepath.Dir(s.StateDir), "work", sess.Name+".outcome.json")
	stream := `{"type":"result","subtype":"success","is_error":false,"result":"Done."}` + "\n"
	cli := fakeCursorCLI(t, dir, cursorCLIPin.Version, stream, 0, filepath.Join(dir, "ran"))
	writeFile(t, outcome, `{"summary":"opened the docs PR"}`)
	ws.Worktree = dir
	l, err := BuildLaunch(s, sess, ws, "boot-1", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	l.Argv[0], l.Argv[len(l.Argv)-1] = cli, "the task"
	t.Setenv(cursorAPIKeyEnv, syntheticCursorKey)
	if code := RunAgent(writeLaunch(t, s, l), &syncBuffer{}, make(chan os.Signal), time.Second); code != 0 {
		t.Fatal("exit", code)
	}
	var res taskResult
	if err := readJSONFile(s.statePath(resultFile), &res); err != nil || res.TaskOutcome == nil || res.TaskOutcome.Record.Summary != "opened the docs PR" || res.TaskOutcome.SessionUID != sess.SessionUID {
		t.Fatalf("outcome %+v err %v", res.TaskOutcome, err)
	}
}

func TestRunAgentCursorErrorFlag(t *testing.T) {
	stream := cursorStreamLines + `{"type":"result","subtype":"success","is_error":true,"result":"the model refused"}` + "\n"
	_, _, res, out, _, _ := runCursorTask(t, cursorCLIPin.Version, stream, 0)
	if !res.IsError || res.ExitCode != 0 || res.UsageObservation == nil || res.UsageObservation.ResultState != "Partial" {
		t.Fatalf("result %+v observation %+v", res.TaskResult, res.UsageObservation)
	}
	if !strings.Contains(out, "[agentd] Cursor's result: the model refused") {
		t.Errorf("the error's text is not in the log:\n%s", out)
	}
}

func TestRunAgentCursorMissingErrorFlagIsAnError(t *testing.T) {
	stream := `{"type":"result","subtype":"success","result":"Done."}` + "\n"
	_, _, res, _, _, _ := runCursorTask(t, cursorCLIPin.Version, stream, 0)
	if !res.IsError {
		t.Fatal("a result without is_error read as success")
	}
}

func TestRunAgentCursorNonZeroExit(t *testing.T) {
	stream := cursorStreamLines + `{"type":"result","subtype":"success","is_error":false,"result":"Done."}` + "\n"
	_, _, res, out, _, exit := runCursorTask(t, cursorCLIPin.Version, stream, 3)
	if exit != 3 || res.ExitCode != 3 || res.UsageObservation == nil || res.UsageObservation.ResultState != "Partial" {
		t.Fatalf("exit %d result %+v\n%s", exit, res.TaskResult, out)
	}
}

func TestRunAgentCursorStreamWithoutResult(t *testing.T) {
	_, _, res, out, _, exit := runCursorTask(t, cursorCLIPin.Version, cursorStreamLines, 0)
	if exit != 1 || res.ExitCode != 1 || !res.IsError || res.Subtype != "missing_result" {
		t.Fatalf("exit %d result %+v\n%s", exit, res.TaskResult, out)
	}
	if !strings.Contains(out, "the Cursor stream ended without a result event, so the task failed") {
		t.Errorf("no truthful reason:\n%s", out)
	}
}

func TestRunAgentCursorSecondResultIsAConflict(t *testing.T) {
	stream := `{"type":"result","subtype":"success","is_error":false,"result":"Done."}
{"type":"result","subtype":"success","is_error":true,"result":"again"}
`
	_, _, res, _, _, _ := runCursorTask(t, cursorCLIPin.Version, stream, 0)
	if res.IsError || res.UsageObservation == nil || res.UsageObservation.ResultState != "Partial" {
		t.Fatalf("a second result replaced the first or went unmarked: %+v %+v", res.TaskResult, res.UsageObservation)
	}
}

func TestRunAgentCursorVersionDrift(t *testing.T) {
	stream := `{"type":"result","subtype":"success","is_error":false,"result":"Done."}` + "\n"
	_, _, res, out, marker, exit := runCursorTask(t, "2026.11.01-updated", stream, 0)
	if exit != 1 || res.ExitCode != 1 || !res.IsError || res.Subtype != "cli_version_mismatch" {
		t.Fatalf("exit %d result %+v\n%s", exit, res.TaskResult, out)
	}
	if fileExists(marker) {
		t.Fatal("a drifted Cursor CLI ran the task")
	}
	if !strings.Contains(out, `the Cursor CLI no longer matches the pin, so the task is not started: it reports "2026.11.01-updated", not the pinned `+cursorCLIPin.Version) {
		t.Errorf("no truthful reason:\n%s", out)
	}
}

// cursorArchive is a gzip tar shaped like Cursor's package: one top
// directory with the launcher, a library file and a relative symlink.
func cursorArchive(t *testing.T, version string, extra ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// The launcher prints its version only when no key or other provider's
	// credential reached it.
	launcher := "#!/bin/sh\n[ -z \"$CURSOR_API_KEY$ANTHROPIC_API_KEY$OPENAI_API_KEY\" ] || exit 80\necho " + version + "\n"
	for _, h := range append([]tar.Header{
		{Name: "dist-package/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "dist-package/cursor-agent", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(launcher))},
		{Name: "dist-package/lib/index.js", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2},
		{Name: "dist-package/agent", Typeflag: tar.TypeSymlink, Linkname: "cursor-agent", Mode: 0o777},
	}, extra...) {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		switch h.Name {
		case "dist-package/cursor-agent":
			_, _ = tw.Write([]byte(launcher))
		case "dist-package/lib/index.js":
			_, _ = tw.Write([]byte("//"))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveCursorArchive serves body over TLS and points the pin at it.
func serveCursorArchive(t *testing.T, body []byte, sum string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	pin, client := cursorCLIPin, cursorHTTPClient
	t.Cleanup(func() { cursorCLIPin, cursorHTTPClient = pin, client })
	cursorCLIPin = cursorPin{Version: "2026.10.14-fixture", SHA256: sum, URL: srv.URL + "/agent-cli-package.tar.gz"}
	cursorHTTPClient = srv.Client
	return &hits
}

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestInstallCursorCLI(t *testing.T) {
	archive := cursorArchive(t, "2026.10.14-fixture")
	hits := serveCursorArchive(t, archive, sha256Hex(archive))
	s := testSettings(t, t.TempDir())
	t.Setenv(cursorAPIKeyEnv, syntheticCursorKey)
	t.Setenv("ANTHROPIC_API_KEY", "metered")
	t.Setenv("OPENAI_API_KEY", "metered")
	// An install agentd did not live to finish left its staging behind.
	writeFile(t, filepath.Join(cursorVersionsDir(s), ".install-left", "package.tgz"), "partial")
	if err := InstallCursorCLI(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	bin := cursorCLIPath(s)
	if err := checkCursorVersion(context.Background(), bin, cursorVersionEnv(os.Environ(), nil)); err != nil {
		t.Fatal("installed launcher", err)
	}
	if target, err := os.Readlink(filepath.Join(filepath.Dir(bin), "agent")); err != nil || target != "cursor-agent" {
		t.Fatal("relative symlink", target, err)
	}
	if err := InstallCursorCLI(context.Background(), s); err != nil || hits.Load() != 1 {
		t.Fatal("an intact pinned install was downloaded again", err, hits.Load())
	}
	if left, _ := filepath.Glob(filepath.Join(cursorVersionsDir(s), ".install-*")); len(left) != 0 {
		t.Fatal("staging left behind", left)
	}
}

func TestInstallCursorCLIChecksumMismatch(t *testing.T) {
	archive := cursorArchive(t, "2026.10.14-fixture")
	serveCursorArchive(t, archive, sha256Hex([]byte("another archive")))
	s := testSettings(t, t.TempDir())
	err := InstallCursorCLI(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 is "+sha256Hex(archive)+", not the pinned "+sha256Hex([]byte("another archive"))+"; nothing was installed") {
		t.Fatal("a mismatched archive was not refused truthfully", err)
	}
	if _, err := os.Lstat(filepath.Dir(cursorCLIPath(s))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a mismatched archive left an install", err)
	}
	if left, _ := filepath.Glob(filepath.Join(cursorVersionsDir(s), ".install-*")); len(left) != 0 {
		t.Fatal("staging left behind", left)
	}
}

func TestInstallCursorCLIRefusesADriftedInstall(t *testing.T) {
	archive := cursorArchive(t, "2026.10.14-fixture")
	hits := serveCursorArchive(t, archive, sha256Hex(archive))
	s := testSettings(t, t.TempDir())
	writeFile(t, cursorCLIPath(s), "#!/bin/sh\necho 2026.11.01-updated\n")
	if err := os.Chmod(cursorCLIPath(s), 0o755); err != nil {
		t.Fatal(err)
	}
	err := InstallCursorCLI(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "preserved for inspection") || hits.Load() != 0 {
		t.Fatal("a drifted install was not refused and preserved", err, hits.Load())
	}
}

func TestInstallCursorCLIRefusesUnsafeArchives(t *testing.T) {
	for name, h := range map[string]tar.Header{
		"climbs out":       {Name: "dist-package/../../evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"absolute":         {Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"symlink out":      {Name: "dist-package/out", Typeflag: tar.TypeSymlink, Linkname: "../../.."},
		"absolute symlink": {Name: "dist-package/abs", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		"through symlink":  {Name: "dist-package/agent/x", Typeflag: tar.TypeReg, Mode: 0o644},
		"device":           {Name: "dist-package/dev", Typeflag: tar.TypeChar},
	} {
		t.Run(name, func(t *testing.T) {
			archive := cursorArchive(t, "2026.10.14-fixture", h)
			serveCursorArchive(t, archive, sha256Hex(archive))
			s := testSettings(t, t.TempDir())
			if err := InstallCursorCLI(context.Background(), s); err == nil || !strings.Contains(err.Error(), "did not unpack") {
				t.Fatal("an unsafe archive was installed", err)
			}
			if _, err := os.Lstat(filepath.Dir(cursorCLIPath(s))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("an unsafe archive left an install", err)
			}
		})
	}
}

// Each link of a chain stays inside the package on its own, but together they
// climb out. Only the resolved path shows it, so the install is refused.
func TestInstallCursorCLIRefusesASymlinkChainOut(t *testing.T) {
	const depth = 11
	dirs := make([]string, depth)
	for i := range dirs {
		dirs[i] = "a" + itoaInt(i)
	}
	nest := strings.Join(dirs, "/")
	up := tar.Header{Name: "dist-package/" + nest + "/up", Typeflag: tar.TypeSymlink, Linkname: strings.TrimSuffix(strings.Repeat("../", depth), "/")}
	esc := tar.Header{Name: "dist-package/esc", Typeflag: tar.TypeSymlink, Linkname: nest + "/up/" + strings.Repeat("../", depth+1) + "etc/passwd"}
	archive := cursorArchive(t, "2026.10.14-fixture", up, esc)
	serveCursorArchive(t, archive, sha256Hex(archive))
	s := testSettings(t, t.TempDir())
	err := InstallCursorCLI(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), `symlink "esc"`) {
		t.Fatal("a symlink chain out of the package was installed", err)
	}
	if _, err := os.Lstat(filepath.Dir(cursorCLIPath(s))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the chain left an install", err)
	}
}

// A launcher that prints without end is cut off at the bound, not buffered.
func TestCursorVersionOutputIsBounded(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chatty")
	writeFile(t, bin, "#!/bin/sh\nhead -c 1048576 /dev/zero | tr '\\0' x\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	err := checkCursorVersion(context.Background(), bin, os.Environ())
	if err == nil || !strings.Contains(err.Error(), "an unreadable version") {
		t.Fatal("unbounded version output", err)
	}
	b := &cappedBuffer{limit: 3}
	_, _ = b.Write([]byte("abcdef"))
	if b.String() != "abc" {
		t.Fatal("cap", b.String())
	}
}

func TestInstallCursorCLIRefusesAPlainHTTPPin(t *testing.T) {
	pin := cursorCLIPin
	t.Cleanup(func() { cursorCLIPin = pin })
	cursorCLIPin.URL = "http://downloads.example/agent-cli-package.tar.gz"
	if err := InstallCursorCLI(context.Background(), testSettings(t, t.TempDir())); err == nil || !strings.Contains(err.Error(), "not an https URL") {
		t.Fatal("a plain http pin was fetched", err)
	}
}

func TestCursorPinMatchesV1(t *testing.T) {
	// The pin is v1's cursor-worker.sh (DESIGN-058); a change is deliberate.
	if cursorCLIVersion != "2026.10.14-0f9edde" || cursorCLISHA256 != "74fac931fe3578980d070701872b7dab54eb87a9abf9b6e2e6be19beeb9167a7" ||
		cursorCLIURL != "https://downloads.cursor.com/lab/2026.10.14-0f9edde/linux/x64/agent-cli-package.tar.gz" {
		t.Fatal("the Cursor CLI pin changed")
	}
}
