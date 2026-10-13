package agentd

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The fixture trees below hold the names a HOME written by the agent image's
// pinned versions (Claude Code 2.1.296, Codex 0.160.1) and by agentd has at
// the top of each provider directory, one of each class, plus nested history.

const (
	testHistorySession = "dev-env-1012-105043"
	testClaudeLogin    = `{"claudeAiOauth":{"accessToken":"sk-ant-oat-TEST-ONLY"}}`
	testCodexLogin     = `{"tokens":{"access_token":"codex-TEST-ONLY"}}`
	testGHToken        = "ghs_TEST_ONLY_TOKEN"
)

type historyRig struct {
	s    Settings
	home string
	o    HistoryExportOptions
}

// newHistoryRig builds a HOME with all three fixture trees and the session
// log, and a GitHub token outside HOME.
func newHistoryRig(t *testing.T) *historyRig {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	creds := filepath.Join(base, "creds")
	for _, d := range []string{home, creds} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(creds, "gh_token"), testGHToken)
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), SharedDir: filepath.Join(home, ".shared"), GHTokenFile: filepath.Join(creds, "gh_token")}
	old := sharedIsMounted
	sharedIsMounted = func(shared, _ string) error {
		if _, err := os.Stat(shared); err != nil {
			return err
		}
		return nil
	}
	t.Cleanup(func() { sharedIsMounted = old })
	r := &historyRig{s: s, home: home, o: HistoryExportOptions{ClaudeConfigDir: ".claude", CodexHome: ".codex", Session: testHistorySession}}
	r.claudeFixture(t)
	r.codexFixture(t)
	r.agentdFixture(t)
	writeTestFile(t, filepath.Join(home, "work", testHistorySession+".log"), "the session's log\n")
	return r
}

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *historyRig) path(rel string) string { return filepath.Join(r.home, filepath.FromSlash(rel)) }

func (r *historyRig) write(t *testing.T, rel, content string) { writeTestFile(t, r.path(rel), content) }

func (r *historyRig) mkdir(t *testing.T, rel string) {
	t.Helper()
	if err := os.MkdirAll(r.path(rel), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (r *historyRig) claudeFixture(t *testing.T) {
	const tr = ".claude/projects/-home-dev-work-x/0b6f6c1e-1111-4222-8333-444455556666"
	r.write(t, tr+".jsonl", `{"type":"user","message":"hello"}`+"\n")
	r.write(t, tr+"/subagents/agent-a1.jsonl", `{"type":"assistant"}`+"\n")
	r.write(t, tr+"/subagents/agent-a1.meta.json", `{}`)
	r.write(t, tr+"/tool-results/b5bufiqfg.txt", "a large tool output\n")
	r.write(t, tr+"/workflows/wf_f832e295-d7a.json", `{}`)
	r.write(t, ".claude/projects/-home-dev-work-x/bridge-pointer.json", `{}`)
	// A 2.5 MiB transcript: three blocks.
	r.write(t, ".claude/projects/-home-dev/big.jsonl", strings.Repeat("0123456789abcdef", 5<<16)+"tail")
	r.write(t, ".claude/history.jsonl", `{"display":"a prompt"}`+"\n")
	r.write(t, ".claude/jobs/job-1.json", `{}`)
	r.write(t, ".claude/feedback/f-1.json", `{}`)
	// Copies of user files: a snapshot of an edited .env under a hashed name.
	r.write(t, ".claude/file-history/0b6f6c1e/3f9a1c2e@v1", "SECRET_FROM_DOTENV=1\n")
	r.write(t, ".claude/paste-cache/9a1b.txt", "pasted\n")
	r.write(t, ".claude/uploads/u1.png", "png")
	for _, f := range []string{"settings.json", "settings.json.bak-0829", "daemon.lock", "daemon.status.json",
		"policy-limits.json", "policy-limits.json.stamp.json.tmp.a60e464e", "remote-settings.json",
		"statusline-command.sh", ".last-cleanup", ".claude.json"} {
		r.write(t, ".claude/"+f, "{}")
	}
	for _, d := range []string{"agents", "plugins", "skills", "cache", "state", "daemon", "telemetry"} {
		r.mkdir(t, ".claude/"+d)
	}
	r.write(t, ".claude/sessions/1234.json", `{"pid":1234}`)
	r.write(t, ".claude/backups/.claude.json.backup", "{}")
	r.write(t, ".claude/shell-snapshots/snapshot-bash.sh", "export TOKEN=x\n")
	r.write(t, ".claude/session-env/0b6f/env", "X=1\n")
	r.write(t, ".claude/.credentials.json", testClaudeLogin)
	r.write(t, ".claude/.credentials.json.tmp.e5023036", testClaudeLogin)
	// What writeFileAtomic leaves when agentd stops mid-write.
	r.write(t, ".claude/..claude.json.agentd-77", "{}")
	r.write(t, ".claude/.settings.json.agentd-78", "{}")
	// Links: render's CLAUDE.md and the shared memory link (DESIGN-001 6.6).
	r.mkdir(t, ".shared/memory/x")
	r.write(t, "config/CLAUDE.md", "rules\n")
	if err := os.Symlink(r.path("config/CLAUDE.md"), r.path(".claude/CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.path(".shared/memory/x"), r.path(".claude/projects/-home-dev-work-x/memory")); err != nil {
		t.Fatal(err)
	}
}

func (r *historyRig) codexFixture(t *testing.T) {
	r.write(t, ".codex/sessions/2026/10/12/rollout-2026-10-12T01-00-00-abc.jsonl", `{"type":"session_meta"}`+"\n")
	r.write(t, ".codex/archived_sessions/rollout-2026-09-01T01-00-00-def.jsonl", `{}`+"\n")
	r.write(t, ".codex/session_index.jsonl", `{}`+"\n")
	r.write(t, ".codex/history.jsonl", `{}`+"\n")
	for _, db := range []string{"state_5", "goals_1", "queue_1", "thread_history_1"} {
		for _, sfx := range []string{".sqlite", ".sqlite-wal", ".sqlite-shm"} {
			r.write(t, ".codex/"+db+sfx, db+sfx)
		}
	}
	r.write(t, ".codex/memories_1.sqlite", "memories")
	r.write(t, ".codex/generated_images/img-1.png", "png")
	r.write(t, ".codex/visualizations/2026/09/17/0f1e/index.html", "<html></html>")
	r.write(t, ".codex/attachments/a1.txt", "attached\n")
	for _, f := range []string{"config.toml", "AGENTS.md", "models_cache.json", "logs_2.sqlite", "logs_2.sqlite-wal",
		"installation_id", "version.json", ".sqlite-maintenance.lock", ".personality_migration", ".sandbox_migration"} {
		r.write(t, ".codex/"+f, "x")
	}
	for _, d := range []string{"rules", "skills", "plugins", "packages", "cache", "app-server-control", "app-server-daemon",
		".tmp", "rollout-migrations", "thread-writer-locks", "mcp-oauth-locks", "shell_snapshots"} {
		r.mkdir(t, ".codex/"+d)
	}
	r.write(t, ".codex/log/codex-tui.log", "log\n")
	r.write(t, ".codex/tmp/arg0", "x")
	r.write(t, ".codex/auth.json", testCodexLogin)
	r.write(t, ".codex/.credentials.json", testCodexLogin+" ")
	// What writeCodexPrivateAtomic and writeFileAtomic leave when agentd
	// stops mid-write: a copy of the login, and one of config.toml.
	r.write(t, ".codex/.codex-access-1234", testCodexLogin+"\n")
	r.write(t, ".codex/.config.toml.agentd-55", "x")
}

func (r *historyRig) agentdFixture(t *testing.T) {
	for _, f := range []string{"boot.json", "launch.json", "resume.json", "task-result.json", "tui-exit.json",
		"task-events.jsonl", "task-source.json", "child-decision.json"} {
		r.write(t, ".agentd/"+f, `{"record":"`+f+`"}`)
	}
	r.write(t, ".agentd/decisions/019a-thread.json", `{"state":"Delivered"}`)
	r.write(t, ".agentd/managed-output/inv-1/0.txt", "assistant text\n")
	r.write(t, ".agentd/initial-usage/observation.json", `{}`)
	for _, f := range []string{"agent.pid", "deliver.lock", "rescue.lock", "project-snapshot.json", "mcp-managed.json",
		"workspace-supervisor.lock", "native-invocation.json", "rescue-123.bundle", "rescue-index-123",
		".launch.json.agentd-4711", "published-context.0123.4567.json", ".receipt-2718", ".codex-access-3141"} {
		r.write(t, ".agentd/"+f, "x")
	}
}

func (r *historyRig) plan(t *testing.T) protocol.HistoryExportPlan {
	t.Helper()
	plan, err := PlanHistoryExport(r.s, r.o)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

func historyCode(err error) string {
	var he *protocol.HistoryError
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func TestHistoryExportPlanFollowsTheAllowlists(t *testing.T) {
	r := newHistoryRig(t)
	plan := r.plan(t)
	var exported []string
	for _, f := range plan.Files {
		exported = append(exported, f.Name())
	}
	want := []string{
		"agentd/boot.json", "agentd/child-decision.json", "agentd/decisions/019a-thread.json",
		"agentd/initial-usage/observation.json", "agentd/launch.json", "agentd/managed-output/inv-1/0.txt",
		"agentd/resume.json", "agentd/task-events.jsonl", "agentd/task-result.json", "agentd/task-source.json",
		"agentd/tui-exit.json",
		"claude/feedback/f-1.json", "claude/history.jsonl", "claude/jobs/job-1.json",
		"claude/projects/-home-dev-work-x/0b6f6c1e-1111-4222-8333-444455556666.jsonl",
		"claude/projects/-home-dev-work-x/0b6f6c1e-1111-4222-8333-444455556666/subagents/agent-a1.jsonl",
		"claude/projects/-home-dev-work-x/0b6f6c1e-1111-4222-8333-444455556666/subagents/agent-a1.meta.json",
		"claude/projects/-home-dev-work-x/0b6f6c1e-1111-4222-8333-444455556666/tool-results/b5bufiqfg.txt",
		"claude/projects/-home-dev-work-x/0b6f6c1e-1111-4222-8333-444455556666/workflows/wf_f832e295-d7a.json",
		"claude/projects/-home-dev-work-x/bridge-pointer.json",
		"claude/projects/-home-dev/big.jsonl",
		"codex/archived_sessions/rollout-2026-09-01T01-00-00-def.jsonl",
		"codex/generated_images/img-1.png",
		"codex/goals_1.sqlite", "codex/goals_1.sqlite-shm", "codex/goals_1.sqlite-wal",
		"codex/history.jsonl", "codex/memories_1.sqlite",
		"codex/queue_1.sqlite", "codex/queue_1.sqlite-shm", "codex/queue_1.sqlite-wal",
		"codex/session_index.jsonl",
		"codex/sessions/2026/10/12/rollout-2026-10-12T01-00-00-abc.jsonl",
		"codex/state_5.sqlite", "codex/state_5.sqlite-shm", "codex/state_5.sqlite-wal",
		"codex/thread_history_1.sqlite", "codex/thread_history_1.sqlite-shm", "codex/thread_history_1.sqlite-wal",
		"codex/visualizations/2026/09/17/0f1e/index.html",
		"log/" + testHistorySession + ".log",
	}
	if !slices.Equal(exported, want) {
		t.Errorf("exported:\n%s\nwant:\n%s", strings.Join(exported, "\n"), strings.Join(want, "\n"))
	}
	excluded := map[string]protocol.HistoryExcluded{}
	for _, e := range plan.Excluded {
		excluded[e.Root+"/"+e.Path] = e
	}
	for name, class := range map[string]string{
		"claude/file-history":                               protocol.HistoryClassUserCopy,
		"claude/paste-cache":                                protocol.HistoryClassUserCopy,
		"claude/uploads":                                    protocol.HistoryClassUserCopy,
		"codex/attachments":                                 protocol.HistoryClassUserCopy,
		"claude/.credentials.json":                          protocol.HistoryClassCredential,
		"claude/.credentials.json.tmp.e5023036":             protocol.HistoryClassCredential,
		"codex/auth.json":                                   protocol.HistoryClassCredential,
		"codex/.credentials.json":                           protocol.HistoryClassCredential,
		"claude/CLAUDE.md":                                  protocol.HistoryClassLink,
		"claude/projects/-home-dev-work-x/memory":           protocol.HistoryClassLink,
		"claude/shell-snapshots":                            protocol.HistoryClassNotHistory,
		"claude/telemetry":                                  protocol.HistoryClassNotHistory,
		"claude/sessions":                                   protocol.HistoryClassNotHistory,
		"codex/tmp":                                         protocol.HistoryClassNotHistory,
		"codex/version.json":                                protocol.HistoryClassNotHistory,
		"codex/logs_2.sqlite":                               protocol.HistoryClassNotHistory,
		"agentd/rescue-123.bundle":                          protocol.HistoryClassNotHistory,
		"agentd/.launch.json.agentd-4711":                   protocol.HistoryClassNotHistory,
		"agentd/published-context.0123.4567.json":           protocol.HistoryClassNotHistory,
		"agentd/workspace-supervisor.lock":                  protocol.HistoryClassNotHistory,
		"claude/policy-limits.json.stamp.json.tmp.a60e464e": protocol.HistoryClassNotHistory,
		"claude/..claude.json.agentd-77":                    protocol.HistoryClassNotHistory,
		"claude/.settings.json.agentd-78":                   protocol.HistoryClassNotHistory,
		"codex/.codex-access-1234":                          protocol.HistoryClassCredential,
		"codex/.config.toml.agentd-55":                      protocol.HistoryClassNotHistory,
		"agentd/.receipt-2718":                              protocol.HistoryClassNotHistory,
		"agentd/.codex-access-3141":                         protocol.HistoryClassNotHistory,
	} {
		e, ok := excluded[name]
		if !ok || e.Class != class {
			t.Errorf("%s: excluded as %+v (listed %v), want class %s", name, e, ok, class)
		}
	}
	if e := excluded["claude/file-history"]; e.Bytes != int64(len("SECRET_FROM_DOTENV=1\n")) {
		t.Errorf("file-history is listed with %d bytes", e.Bytes)
	}
	for _, e := range plan.Excluded {
		if e.Class == protocol.HistoryClassCredential && e.Bytes != 0 {
			t.Errorf("credential %s is listed with a size", e.Path)
		}
	}
	// A 2.5 MiB file has three 1 MiB blocks, and the digests agree.
	for _, f := range plan.Files {
		data, err := os.ReadFile(filepath.Join(r.home, map[string]string{"claude": ".claude", "codex": ".codex", "agentd": ".agentd", "log": "work"}[f.Root], filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != f.SHA256 || f.Bytes != int64(len(data)) || f.BlocksSHA256 != protocol.HistoryBlocksSHA256(f.Blocks) {
			t.Errorf("%s: digests do not match its bytes", f.Name())
		}
		if f.Path == "big.jsonl" && len(f.Blocks) != 3 {
			t.Errorf("big.jsonl has %d blocks, want 3", len(f.Blocks))
		}
	}
	// The plan is deterministic, and the stream writes exactly it.
	sum1, data1, err := HistoryPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum2, _, _ := HistoryPlanSHA256(r.plan(t))
	if sum1 != sum2 {
		t.Errorf("two plans of one tree differ: %s %s", sum1, sum2)
	}
	if bytes.Contains(data1, []byte("TEST-ONLY")) || bytes.Contains(data1, []byte("SECRET_FROM_DOTENV")) {
		t.Errorf("the plan holds credential or excluded bytes")
	}
	if plan.Roots[0].Name != "claude" || plan.Roots[3].Dir != "work/"+testHistorySession+".log" {
		t.Errorf("roots: %+v", plan.Roots)
	}
}

func TestHistoryExportStreamWritesThePlan(t *testing.T) {
	r := newHistoryRig(t)
	plan := r.plan(t)
	sum, _, err := HistoryPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := StreamHistoryExport(r.s, r.o, sum, &buf); err != nil {
		t.Fatalf("stream: %v", err)
	}
	tr := tar.NewReader(&buf)
	for i := 0; ; i++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			if i != len(plan.Files) {
				t.Errorf("the stream holds %d files, the plan %d", i, len(plan.Files))
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		f := plan.Files[i]
		data, _ := io.ReadAll(tr)
		h := sha256.Sum256(data)
		if hdr.Name != f.Name() || hdr.Typeflag != tar.TypeReg || hex.EncodeToString(h[:]) != f.SHA256 {
			t.Errorf("entry %d is %s, want %s with its bytes", i, hdr.Name, f.Name())
		}
	}

	// A wrong digest, or a tree that changed since the plan, is export_changed.
	if err := StreamHistoryExport(r.s, r.o, strings.Repeat("0", 64), io.Discard); historyCode(err) != protocol.HistoryErrChanged {
		t.Errorf("wrong digest: %v", err)
	}
	r.write(t, ".codex/history.jsonl", "changed\n")
	if err := StreamHistoryExport(r.s, r.o, sum, io.Discard); historyCode(err) != protocol.HistoryErrChanged {
		t.Errorf("changed tree: %v", err)
	}
}

func TestHistoryExportRefusesCredentialsAnywhere(t *testing.T) {
	cases := map[string]func(t *testing.T, r *historyRig){
		"auth.json copied into codex sessions": func(t *testing.T, r *historyRig) {
			r.write(t, ".codex/sessions/2026/10/12/auth.json", testCodexLogin)
		},
		"a temporary credentials copy inside projects": func(t *testing.T, r *historyRig) {
			r.write(t, ".claude/projects/-home-dev/.credentials.json.tmp.x", testClaudeLogin)
		},
		"a temporary auth.json copy, nested deep": func(t *testing.T, r *historyRig) {
			r.write(t, ".codex/archived_sessions/a/b/auth.json.bak", "x")
		},
		"a hard link to auth.json named x.jsonl": func(t *testing.T, r *historyRig) {
			r.mkdir(t, ".codex/sessions/2026/10/12")
			if err := os.Link(r.path(".codex/auth.json"), r.path(".codex/sessions/2026/10/12/x.jsonl")); err != nil {
				t.Fatal(err)
			}
		},
		"any hard-linked file, whatever its bytes": func(t *testing.T, r *historyRig) {
			if err := os.Link(r.path(".codex/history.jsonl"), r.path(".agentd/managed-output/inv-1/linked.txt")); err != nil {
				t.Fatal(err)
			}
		},
		"a renamed whole copy of the Codex login": func(t *testing.T, r *historyRig) {
			r.write(t, ".codex/sessions/2026/10/12/rollout-z.jsonl", testCodexLogin)
		},
		"a renamed whole copy of the Claude login": func(t *testing.T, r *historyRig) {
			r.write(t, ".agentd/managed-output/inv-1/1.txt", testClaudeLogin)
		},
		"a renamed copy of the GitHub token": func(t *testing.T, r *historyRig) {
			r.write(t, ".agentd/decisions/token.json", testGHToken)
		},
		"a renamed copy of an interrupted Codex login write": func(t *testing.T, r *historyRig) {
			r.write(t, ".codex/sessions/2026/10/12/rollout-y.jsonl", testCodexLogin+"\n")
		},
		"a third link to agentd's own write": func(t *testing.T, r *historyRig) {
			if err := os.Link(r.path(".agentd/task-source.json"), r.path(".agentd/.task-source.json.agentd-1")); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(r.path(".agentd/task-source.json"), r.path(".agentd/managed-output/inv-1/copy.txt")); err != nil {
				t.Fatal(err)
			}
		},
		"a second link that only looks like agentd's own write": func(t *testing.T, r *historyRig) {
			if err := os.Link(r.path(".agentd/task-source.json"), r.path(".agentd/.task-source.json.agentd-")); err != nil {
				t.Fatal(err)
			}
		},
		"a second link named like agentd's copy, in a provider's directory": func(t *testing.T, r *historyRig) {
			if err := os.Link(r.path(".claude/history.jsonl"), r.path(".claude/.history.jsonl.agentd-1")); err != nil {
				t.Fatal(err)
			}
		},
		"a second link beside a nested history file": func(t *testing.T, r *historyRig) {
			if err := os.Link(r.path(".agentd/decisions/019a-thread.json"), r.path(".agentd/decisions/.019a-thread.json.agentd-1")); err != nil {
				t.Fatal(err)
			}
		},
		"auth.json at the top of agentd's state": func(t *testing.T, r *historyRig) {
			r.write(t, ".agentd/auth.json", "x")
		},
		"a credential name in upper case": func(t *testing.T, r *historyRig) {
			r.write(t, ".claude/projects/-home-dev/AUTH.JSON", "x")
		},
		"a private key":      func(t *testing.T, r *historyRig) { r.write(t, ".codex/generated_images/id_ed25519", "x") },
		"a .pem file":        func(t *testing.T, r *historyRig) { r.write(t, ".claude/jobs/server.pem", "x") },
		"an .env.local":      func(t *testing.T, r *historyRig) { r.write(t, ".claude/feedback/.env.local", "x") },
		"a kubeconfig":       func(t *testing.T, r *historyRig) { r.write(t, ".agentd/managed-output/kubeconfig", "x") },
		"a .git-credentials": func(t *testing.T, r *historyRig) { r.write(t, ".claude/projects/x/.git-credentials", "x") },
		"a gh_token":         func(t *testing.T, r *historyRig) { r.write(t, ".agentd/initial-usage/gh_token", "x") },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newHistoryRig(t)
			setup(t, r)
			_, err := PlanHistoryExport(r.s, r.o)
			if historyCode(err) != protocol.HistoryErrCredentialInTree {
				t.Fatalf("plan: %v, want credential_in_tree", err)
			}
			for _, secret := range []string{"TEST-ONLY", "TEST_ONLY"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the refusal shows credential bytes: %v", err)
				}
			}
			// The stream refuses the same way, before writing anything.
			var buf bytes.Buffer
			if err := StreamHistoryExport(r.s, r.o, strings.Repeat("0", 64), &buf); historyCode(err) != protocol.HistoryErrCredentialInTree || buf.Len() != 0 {
				t.Errorf("stream: %v, %d bytes written", err, buf.Len())
			}
		})
	}
	// An empty file is never a renamed credential, even beside an empty one.
	r := newHistoryRig(t)
	r.write(t, ".claude/.credentials.json.tmp.empty", "")
	r.write(t, ".claude/projects/-home-dev/empty.jsonl", "")
	if _, err := PlanHistoryExport(r.s, r.o); err != nil {
		t.Errorf("an empty file beside an empty credential: %v", err)
	}
}

func TestHistoryExportRefusesUnclassifiedEntries(t *testing.T) {
	for _, rel := range []string{".claude/plans", ".claude/brand-new.json", ".codex/visualizer_2.sqlite", ".agentd/new-state.json", ".agentd/new-dir"} {
		t.Run(rel, func(t *testing.T) {
			r := newHistoryRig(t)
			if strings.HasSuffix(rel, "dir") || strings.HasSuffix(rel, "plans") {
				r.mkdir(t, rel)
			} else {
				r.write(t, rel, "x")
			}
			_, err := PlanHistoryExport(r.s, r.o)
			if historyCode(err) != protocol.HistoryErrUnknownEntry || !strings.Contains(err.Error(), filepath.Base(rel)) {
				t.Errorf("plan: %v, want unknown_entry naming %s", err, filepath.Base(rel))
			}
		})
	}
}

func TestHistoryExportRefusesUnknownLayouts(t *testing.T) {
	cases := map[string]func(t *testing.T, r *historyRig){
		"no CLAUDE_CONFIG_DIR": func(t *testing.T, r *historyRig) { r.o.ClaudeConfigDir = "" },
		"no CODEX_HOME":        func(t *testing.T, r *historyRig) { r.o.CodexHome = "" },
		"an absolute path":     func(t *testing.T, r *historyRig) { r.o.CodexHome = "/home/dev/.codex" },
		"a path out of HOME":   func(t *testing.T, r *historyRig) { r.o.ClaudeConfigDir = "../.claude" },
		"an unclean path":      func(t *testing.T, r *historyRig) { r.o.ClaudeConfigDir = "./.claude" },
		"HOME itself":          func(t *testing.T, r *historyRig) { r.o.CodexHome = "." },
		"overlapping roots":    func(t *testing.T, r *historyRig) { r.o.CodexHome = ".claude/codex" },
		"the same root twice":  func(t *testing.T, r *historyRig) { r.o.CodexHome = ".claude" },
		"a root on the shared volume": func(t *testing.T, r *historyRig) {
			r.mkdir(t, ".shared/codex")
			r.o.CodexHome = ".shared/codex"
		},
		"a root that is a link": func(t *testing.T, r *historyRig) {
			if err := os.Symlink(r.path(".codex"), r.path("codex-link")); err != nil {
				t.Fatal(err)
			}
			r.o.CodexHome = "codex-link"
		},
		"a link on the way to a root": func(t *testing.T, r *historyRig) {
			r.mkdir(t, "elsewhere/codex")
			if err := os.Symlink(r.path("elsewhere"), r.path(".config")); err != nil {
				t.Fatal(err)
			}
			r.o.CodexHome = ".config/codex"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newHistoryRig(t)
			setup(t, r)
			if _, err := PlanHistoryExport(r.s, r.o); historyCode(err) != protocol.HistoryErrLayoutUnknown {
				t.Errorf("plan: %v, want layout_unknown", err)
			}
		})
	}
	// A provider directory that was never made has nothing to export.
	r := newHistoryRig(t)
	r.o.CodexHome = ".codex-never-made"
	plan := r.plan(t)
	if !plan.Roots[1].Absent {
		t.Errorf("an absent CODEX_HOME is not marked absent: %+v", plan.Roots[1])
	}
}

func TestHistoryExportRefusesUnexpectedEntries(t *testing.T) {
	cases := map[string]func(t *testing.T, r *historyRig){
		"a FIFO in codex sessions": func(t *testing.T, r *historyRig) {
			r.mkdir(t, ".codex/sessions/2026")
			if err := syscall.Mkfifo(r.path(".codex/sessions/2026/fifo"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"a socket in projects": func(t *testing.T, r *historyRig) {
			// A socket path must be short, so bind it in a short directory
			// and move it into the tree.
			r.mkdir(t, ".claude/projects/s")
			short, err := os.MkdirTemp("", "h4")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(short) })
			l, err := net.Listen("unix", filepath.Join(short, "s"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = l.Close() })
			if err := os.Rename(filepath.Join(short, "s"), r.path(".claude/projects/s/sock")); err != nil {
				t.Fatal(err)
			}
		},
		"history of the wrong kind": func(t *testing.T, r *historyRig) {
			if err := os.RemoveAll(r.path(".claude/jobs")); err != nil {
				t.Fatal(err)
			}
			r.write(t, ".claude/jobs", "not a directory")
		},
		"a tree deeper than the bound": func(t *testing.T, r *historyRig) {
			r.write(t, ".claude/projects/"+strings.Repeat("d/", protocol.MaxHistoryPathDepth)+"f.jsonl", "x")
		},
		"a path longer than the bound": func(t *testing.T, r *historyRig) {
			long := strings.Repeat("p", 200)
			r.write(t, ".codex/sessions/"+strings.Join([]string{long, long, long, long, long, long}, "/")+".jsonl", "x")
		},
		// JSON would carry U+FFFD for the name, and put would refuse the
		// stream's own name on every retry.
		"a name that is not UTF-8": func(t *testing.T, r *historyRig) {
			r.write(t, ".codex/sessions/2026/10/12/rollout-\xff.jsonl", "x")
		},
		"a top-level name that is not UTF-8": func(t *testing.T, r *historyRig) {
			r.write(t, ".agentd/state_\xfe.json", "x")
		},
		// History behind a link would be deleted with HOME, or kept, without
		// being archived either way.
		"history that is a link": func(t *testing.T, r *historyRig) {
			if err := os.Rename(r.path(".codex/sessions"), r.path("elsewhere-sessions")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(r.path("elsewhere-sessions"), r.path(".codex/sessions")); err != nil {
				t.Fatal(err)
			}
		},
		"history that links to the shared volume": func(t *testing.T, r *historyRig) {
			r.mkdir(t, ".shared/jobs")
			if err := os.RemoveAll(r.path(".claude/jobs")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(r.path(".shared/jobs"), r.path(".claude/jobs")); err != nil {
				t.Fatal(err)
			}
		},
		"a log that is a link": func(t *testing.T, r *historyRig) {
			if err := os.Rename(r.path("work/"+testHistorySession+".log"), r.path("work/real.log")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("real.log", r.path("work/"+testHistorySession+".log")); err != nil {
				t.Fatal(err)
			}
		},
		"a nested link into HOME": func(t *testing.T, r *historyRig) {
			r.write(t, "elsewhere/rollout.jsonl", "x")
			if err := os.Symlink(r.path("elsewhere"), r.path(".codex/sessions/2026/10/13")); err != nil {
				t.Fatal(err)
			}
		},
		"a relative nested link into HOME": func(t *testing.T, r *historyRig) {
			r.write(t, "elsewhere/a.jsonl", "x")
			if err := os.Symlink("../../../elsewhere/a.jsonl", r.path(".claude/projects/-home-dev/a.jsonl")); err != nil {
				t.Fatal(err)
			}
		},
		"a nested link into the same tree": func(t *testing.T, r *historyRig) {
			if err := os.Symlink("big.jsonl", r.path(".claude/projects/-home-dev/latest.jsonl")); err != nil {
				t.Fatal(err)
			}
		},
		"the memory link when the shared volume is not mounted": func(t *testing.T, r *historyRig) {
			sharedIsMounted = func(string, string) error { return errors.New("not mounted") }
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			r := newHistoryRig(t)
			setup(t, r)
			if _, err := PlanHistoryExport(r.s, r.o); historyCode(err) != protocol.HistoryErrUnexpectedEntry {
				t.Errorf("plan: %v, want unexpected_entry", err)
			}
		})
	}
}

func TestHistoryExportListsLinksHOMEKeeps(t *testing.T) {
	r := newHistoryRig(t)
	// A link out of HOME, and links whose targets are on the shared volume,
	// absolute or relative: listed, never followed, never refused.
	if err := os.Symlink("/etc/hostname", r.path(".codex/sessions/2026/10/12/outside")); err != nil {
		t.Fatal(err)
	}
	r.mkdir(t, ".shared/memory/y")
	if err := os.Symlink("../../../.shared/memory/y", r.path(".claude/projects/-home-dev/memory")); err != nil {
		t.Fatal(err)
	}
	plan := r.plan(t)
	links := map[string]bool{}
	for _, e := range plan.Excluded {
		if e.Class == protocol.HistoryClassLink {
			links[e.Root+"/"+e.Path] = true
		}
	}
	for _, want := range []string{"claude/CLAUDE.md", "claude/projects/-home-dev-work-x/memory", "claude/projects/-home-dev/memory", "codex/sessions/2026/10/12/outside"} {
		if !links[want] {
			t.Errorf("%s is not listed as a link: %v", want, links)
		}
	}
}

// TestHistoryExportKeepsAgentdsInterruptedWrite: writeFileOnce links its
// synced copy into place and then removes it, so agentd stopping between the
// two leaves task-source.json with two links, the other its own copy. That
// file has no name outside agentd's state directory, so it is exported.
func TestHistoryExportKeepsAgentdsInterruptedWrite(t *testing.T) {
	r := newHistoryRig(t)
	if err := os.Link(r.path(".agentd/task-source.json"), r.path(".agentd/.task-source.json.agentd-1234")); err != nil {
		t.Fatal(err)
	}
	plan := r.plan(t)
	found := false
	for _, f := range plan.Files {
		found = found || f.Name() == "agentd/task-source.json"
	}
	listed := false
	for _, e := range plan.Excluded {
		listed = listed || (e.Root == "agentd" && e.Path == ".task-source.json.agentd-1234" && e.Class == protocol.HistoryClassNotHistory)
	}
	if !found || !listed {
		t.Fatalf("task-source.json exported %v, its copy listed %v", found, listed)
	}
	sum, _, err := HistoryPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := StreamHistoryExport(r.s, r.o, sum, &buf); err != nil {
		t.Fatalf("stream: %v", err)
	}
}

// TestHistoryExportNeverOpensThroughALink: every open walks from its base one
// component at a time, so a directory swapped for a link between a check and
// an open is never followed, even to identical bytes.
func TestHistoryExportNeverOpensThroughALink(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, filepath.Join(base, "a", "b", "f"), "x")
	if err := os.Symlink(filepath.Join(base, "a"), filepath.Join(base, "l")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f", filepath.Join(base, "a", "b", "g")); err != nil {
		t.Fatal(err)
	}
	if f, _, err := openHistoryFile(base, filepath.Join(base, "a", "b", "f")); err != nil {
		t.Fatalf("a plain path: %v", err)
	} else {
		_ = f.Close()
	}
	for _, p := range []string{"l/b/f", "a/b/g"} {
		if f, _, err := openHistoryFile(base, filepath.Join(base, filepath.FromSlash(p))); err == nil {
			_ = f.Close()
			t.Errorf("%s opened through a link", p)
		}
	}
	if _, err := readHistoryDir(base, filepath.Join(base, "l", "b")); err == nil {
		t.Errorf("a directory listed through a link")
	}
	for _, p := range []string{base, filepath.Dir(base), filepath.Join(base, "..", "x")} {
		if f, err := openBeneath(base, p, historyOpenFlags); err == nil {
			_ = f.Close()
			t.Errorf("%s opened, which is not below the base", p)
		}
	}

	// In the export: a nested directory of history swapped for a link to a
	// copy with the same bytes and times, after the plan's walk.
	r := newHistoryRig(t)
	plan := r.plan(t)
	sum, _, err := HistoryPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	dir := r.path(".codex/sessions/2026/10/12")
	copyDir := r.path("elsewhere/12")
	r.mkdir(t, "elsewhere/12")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		fi, _ := os.Stat(filepath.Join(dir, e.Name()))
		writeTestFile(t, filepath.Join(copyDir, e.Name()), string(data))
		if err := os.Chtimes(filepath.Join(copyDir, e.Name()), fi.ModTime(), fi.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(dir, r.path("elsewhere/old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(copyDir, dir); err != nil {
		t.Fatal(err)
	}
	// streamHistoryFile is what runs after stream's own plan; call it on the
	// first plan's file directly, as a swap after that plan would.
	var f protocol.HistoryFile
	for _, pf := range plan.Files {
		if strings.HasPrefix(pf.Name(), "codex/sessions/2026/10/12/") {
			f = pf
		}
	}
	tw := tar.NewWriter(io.Discard)
	if err := streamHistoryFile(tw, r.home, filepath.Join(r.home, ".codex", filepath.FromSlash(f.Path)), f); historyCode(err) != protocol.HistoryErrChanged {
		t.Errorf("stream through a swapped directory: %v, want export_changed", err)
	}
	if err := StreamHistoryExport(r.s, r.o, sum, io.Discard); historyCode(err) == "" {
		t.Errorf("stream after the swap succeeded")
	}
}

func TestHistoryExportSizeCaps(t *testing.T) {
	old := historyBounds
	t.Cleanup(func() { historyBounds = old })
	cases := map[string]func(){
		"one file over its bound": func() { historyBounds.fileBytes = 1 << 20 },
		"over the total":          func() { historyBounds.totalBytes = 2 << 20 },
		"too many files":          func() { historyBounds.files = 10 },
		"too many excluded names": func() { historyBounds.excluded = 5 },
		// The fixture's manifest is some kilobytes: over a bound with 1 KiB
		// to spare for it, put could never take it.
		"a content manifest over put's bound": func() { historyBounds.contentBytes = historyContentReserve + 1<<10 },
	}
	for name, lower := range cases {
		t.Run(name, func(t *testing.T) {
			historyBounds = old
			r := newHistoryRig(t)
			lower()
			if _, err := PlanHistoryExport(r.s, r.o); historyCode(err) != protocol.HistoryErrTooLarge {
				t.Errorf("plan: %v, want export_too_large", err)
			}
		})
	}
}

// writeTestRescue writes a finished rescue directory on the shared volume:
// one bundle and its manifest.
func writeTestRescue(t *testing.T, r *historyRig, stamp string, mutate func(*protocol.RescueManifest)) string {
	t.Helper()
	dir := protocol.RescueDir(testHistorySession, stamp)
	abs := filepath.Join(r.s.SharedDir, filepath.FromSlash(dir))
	bundle := "# v2 git bundle\nfake bundle bytes\n"
	writeTestFile(t, filepath.Join(abs, "dev-env.bundle"), bundle)
	sum := sha256.Sum256([]byte(bundle))
	m := protocol.RescueManifest{Version: 1, Session: testHistorySession, Stamp: stamp, Dir: dir, CreatedAt: time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC), Complete: true,
		Repos: []protocol.ManifestRepo{{Path: "/home/dev/repos/dev-env", Bundle: protocol.RepoBundle{File: dir + "/dev-env.bundle", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bundle)), Verified: true}}}}
	if mutate != nil {
		mutate(&m)
	}
	data, _ := json.Marshal(m)
	writeTestFile(t, filepath.Join(abs, protocol.ManifestFile), string(data))
	return testHistorySession + "/" + stamp
}

func TestHistoryExportListsTheGitRescue(t *testing.T) {
	r := newHistoryRig(t)
	r.o.Rescue = writeTestRescue(t, r, "20261012-0102", nil)
	plan := r.plan(t)
	if plan.Git == nil || plan.Git.Rescue != r.o.Rescue || len(plan.Git.Files) != 2 || plan.Git.Files[0].Name != "dev-env.bundle" || plan.Git.Files[1].Name != protocol.ManifestFile {
		t.Fatalf("git: %+v", plan.Git)
	}

	cases := map[string]struct {
		setup func(t *testing.T, r *historyRig)
		code  string
	}{
		"a bundle that differs from the manifest": {func(t *testing.T, r *historyRig) {
			r.o.Rescue = writeTestRescue(t, r, "20261012-0103", nil)
			writeTestFile(t, filepath.Join(r.s.SharedDir, "rescue", testHistorySession, "20261012-0103", "dev-env.bundle"), "other bytes")
		}, protocol.HistoryErrInvalid},
		"a file the manifest does not name": {func(t *testing.T, r *historyRig) {
			r.o.Rescue = writeTestRescue(t, r, "20261012-0104", nil)
			writeTestFile(t, filepath.Join(r.s.SharedDir, "rescue", testHistorySession, "20261012-0104", "extra"), "x")
		}, protocol.HistoryErrUnexpectedEntry},
		"an incomplete rescue": {func(t *testing.T, r *historyRig) {
			r.o.Rescue = writeTestRescue(t, r, "20261012-0105", func(m *protocol.RescueManifest) { m.Complete = false })
		}, protocol.HistoryErrInvalid},
		"another Session's rescue": {func(t *testing.T, r *historyRig) {
			r.o.Rescue = "other-session/20261012-0106"
		}, protocol.HistoryErrInvalid},
		"a rescue that is not there": {func(t *testing.T, r *historyRig) {
			r.o.Rescue = testHistorySession + "/20261012-0107"
		}, protocol.HistoryErrIO},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newHistoryRig(t)
			c.setup(t, r)
			if _, err := PlanHistoryExport(r.s, r.o); historyCode(err) != c.code {
				t.Errorf("plan: %v, want %s", err, c.code)
			}
		})
	}
}
