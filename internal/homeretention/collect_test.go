package homeretention

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyGitRefusesMutations(t *testing.T) {
	for _, args := range [][]string{
		{"fetch"},
		{"fetch", "--prune", "--quiet"},
		{"checkout", "main"},
		{"reset", "--hard"},
		{"switch", "main"},
		{"restore", "."},
		{"clean", "-fd"},
		{"add", "-A"},
		{"commit", "-m", "synthetic"},
		{"update-ref", "refs/heads/main", strings.Repeat("a", 40)},
		{"push", "origin", "HEAD"},
		{"pull"},
		{"merge", "HEAD"},
		{"rebase", "HEAD"},
		{"gc"},
		{"init"},
		{"clone", "file://synthetic"},
		{"stash", "push"},
		{"stash"},
		{"rev-list", "-n", "1", "HEAD", "--not", "--remotes=origin"},
		{"worktree", "add", "elsewhere"},
		{"status"},
		{"for-each-ref", "refs/remotes"},
	} {
		if err := allowedReadOnlyGit(args); err == nil || !strings.Contains(err.Error(), "read-only git command refused") {
			t.Fatalf("allowed %q: %v", strings.Join(args, " "), err)
		}
	}
	if err := allowedReadOnlyGit([]string{"stash", "list", "--format=%H"}); err != nil {
		t.Fatal(err)
	}
	if err := allowedReadOnlyGit([]string{"worktree", "list", "--porcelain"}); err != nil {
		t.Fatal(err)
	}
	if err := allowedReadOnlyGit(localConfigArgs); err != nil || allowedReadOnlyGit(worktreeConfigArgs) != nil {
		t.Fatal(err)
	}
	if err := allowedReadOnlyGit([]string{"config", "--get", "filter.x.clean"}); err == nil {
		t.Fatal("config get was allowlisted")
	}
	if err := allowedReadOnlyGit(indexFlagArgs); err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(t.TempDir(), ".gitmodules")
	if err := allowedReadOnlyGit([]string{"config", "--file", modules, "--list", "--null", "--no-includes"}); err != nil {
		t.Fatal(err)
	}
	if err := allowedReadOnlyGit([]string{"config", "--file", "relative/.gitmodules", "--list", "--null", "--no-includes"}); err == nil {
		t.Fatal("relative gitmodules file was allowlisted")
	}
	if err := allowedReadOnlyGit([]string{"config", "--file", filepath.Join(t.TempDir(), "config"), "--list", "--null", "--no-includes"}); err == nil {
		t.Fatal("non-gitmodules file was allowlisted")
	}
	if err := allowedReadOnlyGit([]string{"submodule", "status"}); err != nil {
		t.Fatal(err)
	}
	if err := allowedReadOnlyGit([]string{"submodule", "status", "--recursive"}); err == nil {
		t.Fatal("recursive submodule status was allowlisted")
	}
	if err := allowedReadOnlyGit([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}); err == nil {
		t.Fatal("status without ignore-submodules was allowlisted")
	}
	if err := allowedReadOnlyGit(statusArgs); err != nil {
		t.Fatal(err)
	}
	if err := allowedReadOnlyGit(gitlinkIndexArgs); err != nil {
		t.Fatal(err)
	}
	if err := allowedReadOnlyGit([]string{"ls-files", "-s"}); err == nil {
		t.Fatal("unbounded ls-files stage was allowlisted")
	}
}

func TestCollectFailClosedBeforeGit(t *testing.T) {
	f := newFixture(t)
	git := &forbidGit{t: t}
	_, err := CollectPreservation(context.Background(), CollectRequest{Binding: f.record.Binding, Repositories: []RepositorySpec{}, Requirements: Requirements{}, Artifacts: []ArtifactInput{}}, git, fixedRevision(f.record.Binding.HomeRevision))
	if err == nil || !strings.Contains(err.Error(), "requirements are not explicit") {
		t.Fatal(err)
	}
	_, err = CollectPreservation(context.Background(), validCollectRequest(f, t.TempDir()), git, fixedRevision("other-revision"))
	if err == nil || !strings.Contains(err.Error(), "home revision changed") {
		t.Fatal(err)
	}
}

func TestGitCommandBoundCapsLongDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	child, stop, err := boundGitContext(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline, ok := child.Deadline()
	if !ok {
		t.Fatal("command had no deadline")
	}
	remaining := time.Until(deadline)
	if remaining > maxGitCommand || remaining < maxGitCommand-5*time.Second {
		t.Fatalf("long caller deadline was not capped to 30s: %s", remaining)
	}
	short, cancelShort := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShort()
	bounded, stopShort, err := boundGitContext(short)
	if err != nil {
		t.Fatal(err)
	}
	defer stopShort()
	shortDeadline, ok := bounded.Deadline()
	if !ok || time.Until(shortDeadline) > 5*time.Second {
		t.Fatal("shorter caller deadline was extended")
	}
	expired, cancelExpired := context.WithCancel(context.Background())
	cancelExpired()
	if _, _, err := boundGitContext(expired); err == nil {
		t.Fatal("expired context was allowed to run")
	}
	env := gitCommandEnv(t.TempDir())
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_COUNT="+strconv.Itoa(len(gitSafetyConfig()))) || !strings.Contains(joined, "GIT_CONFIG_KEY_0=protocol.allow") || !strings.Contains(joined, "GIT_CONFIG_VALUE_0=never") || !strings.Contains(joined, "GIT_CONFIG_KEY_3=core.hooksPath") {
		t.Fatalf("safety config is not inherited: %v", env)
	}
}

func TestCommandBearingConfigDoesNotRun(t *testing.T) {
	payloads := []struct{ name, body string }{
		{"clean filter", "file:.git/config\x00filter.x.clean\ntouch-attacked\x00"},
		{"process filter", "file:.git/config\x00filter.x.process\nattacked\x00"},
		{"external diff", "file:.git/config\x00diff.external\n/tmp/attacked\x00"},
		{"diff driver", "file:.git/config\x00diff.attack.command\n/tmp/attacked\x00"},
		{"hooks path", "file:.git/config\x00core.hookspath\n/tmp/hooks\x00"},
		{"fsmonitor", "file:.git/config\x00core.fsmonitor\n./attacked\x00"},
		{"alias status", "file:.git/config\x00alias.status\n!touch-attacked\x00"},
		{"malformed", "garbage"},
	}
	for _, payload := range payloads {
		t.Run(payload.name, func(t *testing.T) {
			f := newFixture(t)
			git := newScriptGit(t, scriptOptions{attack: payload.body})
			_, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
			if err == nil || git.executed {
				t.Fatalf("config was invoked: err=%v executed=%v calls=%q", err, git.executed, git.calls)
			}
			for _, call := range git.calls {
				if call[0] != "config" {
					t.Fatalf("non-config command ran: %q", call)
				}
			}
		})
	}
}

func TestSubmoduleConfigIsRefusedBeforeStatus(t *testing.T) {
	f := newFixture(t)
	git := newScriptGit(t, scriptOptions{})
	child := filepath.Join(git.root, "vendor", "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(child, ".git"), "gitdir: synthetic\n")
	writeFile(t, filepath.Join(git.root, ".gitmodules"), "[submodule \"vendor\"]\n\tpath = vendor/sub\n\turl = ../sub\n")
	git.extra.modulesList = "submodule.vendor.path\nvendor/sub\x00submodule.vendor.url\n../sub\x00"
	git.options.attack = "file:.git/config\x00filter.x.clean\ntouch-attacked\x00"
	git.options.attackDir = child
	_, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
	if err == nil || !strings.Contains(err.Error(), "unsupported git config") || git.executed {
		t.Fatalf("submodule command ran: %v executed=%v", err, git.executed)
	}
	if !sawGit(git, "", func(call []string) bool { return allowedFileConfig(call) }) || !sawGit(git, child, func(call []string) bool { return slices.Equal(call, localConfigArgs) }) {
		t.Fatalf("child config was not listed from git's file parse: %v", git.calls)
	}
	for _, call := range git.calls {
		if call[0] == "status" || call[0] == "ls-files" || call[0] == "submodule" {
			t.Fatalf("status or index inspection ran before descendant config refusal: %q", call)
		}
	}
}

func TestUnmappedIndexGitlinkFailsClosedBeforeStatus(t *testing.T) {
	f := newFixture(t)
	sha := strings.Repeat("a", 40)
	link := func(path string) string {
		return "160000 " + sha + " 0\t" + path + "\x00"
	}
	assertRefused := func(t *testing.T, git *scriptGit, untouched string) {
		t.Helper()
		inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
		if err == nil || inventory.Complete || !strings.Contains(err.Error(), "submodule mapping missing") || git.executed {
			t.Fatalf("unmapped gitlink was inventoried: %+v %v executed=%v calls=%v", inventory, err, git.executed, git.calls)
		}
		if untouched != "" && sawGit(git, untouched, func([]string) bool { return true }) {
			t.Fatalf("unmapped checkout was entered: %s calls=%v", untouched, git.calls)
		}
		for _, call := range git.calls {
			if call[0] == "status" || call[0] == "submodule" {
				t.Fatalf("status entered a descendant before refusal: %q", call)
			}
		}
	}
	t.Run("absent gitmodules", func(t *testing.T) {
		git := newScriptGit(t, scriptOptions{})
		child := filepath.Join(git.root, "vendor", "orphan")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(child, ".git"), "gitdir: synthetic\n")
		git.extra.indexGitlinks = link("vendor/orphan")
		git.options.attack = "file:.git/config\x00filter.x.clean\ntouch-attacked\x00"
		git.options.attackDir = child
		assertRefused(t, git, child)
	})
	t.Run("gitmodules omits gitlink", func(t *testing.T) {
		git := newScriptGit(t, scriptOptions{})
		child := filepath.Join(git.root, "vendor", "orphan")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(child, ".git"), "gitdir: synthetic\n")
		writeFile(t, filepath.Join(git.root, ".gitmodules"), "[submodule \"listed\"]\n\tpath = vendor/listed\n\turl = ../listed\n")
		git.extra.modulesList = "submodule.listed.path\nvendor/listed\x00submodule.listed.url\n../listed\x00"
		git.extra.indexGitlinks = link("vendor/orphan")
		git.options.attack = "file:.git/config\x00filter.x.clean\ntouch-attacked\x00"
		git.options.attackDir = child
		assertRefused(t, git, child)
	})
	t.Run("grandchild", func(t *testing.T) {
		git := newScriptGit(t, scriptOptions{})
		child := filepath.Join(git.root, "vendor", "sub")
		grandchild := filepath.Join(child, "nested")
		if err := os.MkdirAll(grandchild, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(child, ".git"), "gitdir: synthetic\n")
		writeFile(t, filepath.Join(git.root, ".gitmodules"), "[submodule \"vendor\"]\n\tpath = vendor/sub\n\turl = ../sub\n")
		git.extra.modulesList = "submodule.vendor.path\nvendor/sub\x00submodule.vendor.url\n../sub\x00"
		git.extra.indexGitlinksByDir = map[string]string{
			git.root: link("vendor/sub"),
			child:    link("nested"),
		}
		git.options.attack = "file:.git/config\x00filter.x.clean\ntouch-attacked\x00"
		git.options.attackDir = grandchild
		assertRefused(t, git, grandchild)
	})
}

func TestGitmodulesFileParseKeepsOrdinaryURLAndFailsClosed(t *testing.T) {
	f := newFixture(t)
	t.Run("inline comment and decoy do not hide a child filter", func(t *testing.T) {
		git := newScriptGit(t, scriptOptions{})
		child := filepath.Join(git.root, "vendor", "sub")
		decoy := filepath.Join(git.root, "vendor", "sub # pin")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(child, ".git"), "gitdir: synthetic\n")
		if err := os.MkdirAll(decoy, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(git.root, ".gitmodules"), "[submodule \"vendor\"]\n\tpath = vendor/sub # pin\n\turl = ../sub\n")
		git.extra.modulesList = "submodule.vendor.path\nvendor/sub\x00submodule.vendor.url\n../sub\x00"
		git.options.attack = "file:.git/config\x00filter.x.clean\ntouch-attacked\x00"
		git.options.attackDir = child
		inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
		if err == nil || inventory.Complete || !strings.Contains(err.Error(), "unsupported git config") || git.executed {
			t.Fatalf("false complete or execution: %+v %v executed=%v", inventory, err, git.executed)
		}
		if !sawGit(git, child, func(call []string) bool { return slices.Equal(call, localConfigArgs) }) || sawGit(git, decoy, func([]string) bool { return true }) {
			t.Fatal("decoy path was scanned or the parsed path was not")
		}
		for _, call := range git.calls {
			if call[0] == "submodule" {
				t.Fatal("submodule status ran")
			}
		}
	})
	t.Run("quoted path and url stay ordinary", func(t *testing.T) {
		git := newScriptGit(t, scriptOptions{})
		child := filepath.Join(git.root, "vendor", "quoted sub")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(child, ".git"), "gitdir: synthetic\n")
		writeFile(t, filepath.Join(git.root, ".gitmodules"), "[submodule \"quoted\"]\n\tpath = \"vendor/quoted sub\"\n\turl = ../quoted\n")
		git.extra.modulesList = "submodule.quoted.path\nvendor/quoted sub\x00submodule.quoted.url\n../quoted\x00submodule.quoted.active\ntrue\x00"
		inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
		if err != nil || !inventory.Complete || inventory.Repositories[0].FetchSucceeded {
			t.Fatalf("ordinary submodule file was refused: %+v %v", inventory, err)
		}
		if !sawGit(git, "", func(call []string) bool { return allowedFileConfig(call) }) || !sawGit(git, child, func(call []string) bool { return slices.Equal(call, localConfigArgs) }) {
			t.Fatal("parsed quoted path was not config-checked")
		}
	})
	t.Run("command and ambiguous gitmodules values do not run", func(t *testing.T) {
		for _, listing := range []string{
			"submodule.vendor.path\nvendor/sub\x00submodule.vendor.url\n../sub\x00filter.x.clean\ntouch-attacked\x00",
			"submodule.vendor.path\nvendor/sub\x00submodule.vendor.url\n../sub\x00submodule.vendor.update\n!touch-attacked\x00",
			"submodule.vendor.path\nvendor/sub\x00submodule.vendor.url\next::cmd\x00",
			"submodule.vendor.path\nvendor/sub # pin\x00submodule.vendor.url\n../sub\x00",
			"submodule.a.path\nvendor/sub\x00submodule.a.url\n../a\x00submodule.b.path\nvendor/sub\x00submodule.b.url\n../b\x00",
			"submodule.vendor.url\n../sub\x00",
		} {
			git := newScriptGit(t, scriptOptions{})
			writeFile(t, filepath.Join(git.root, ".gitmodules"), "[submodule \"vendor\"]\n\tpath = vendor/sub\n")
			git.extra.modulesList = listing
			inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
			if err == nil || inventory.Complete || git.executed {
				t.Fatalf("listing %q completed: %+v %v", listing, inventory, err)
			}
			for _, call := range git.calls {
				if call[0] == "submodule" {
					t.Fatalf("submodule status ran for %q", listing)
				}
			}
		}
	})
}

func TestCoreWorktreeAndIndexFlagsFailClosed(t *testing.T) {
	f := newFixture(t)
	git := newScriptGit(t, scriptOptions{})
	git.extra.localConfig = "file:.git/config\x00core.worktree\n/clean\x00"
	inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
	if err == nil || inventory.Complete || !strings.Contains(err.Error(), "unsupported git config") {
		t.Fatalf("core.worktree was inventoried: %+v %v", inventory, err)
	}
	for _, call := range git.calls {
		if call[0] == "status" || call[0] == "ls-files" || call[0] == "worktree" {
			t.Fatalf("tree command ran: %q", call)
		}
	}
	ordinary := newScriptGit(t, scriptOptions{})
	ordinary.extra.localConfig = "file:.git/config\x00core.bare\nfalse\x00file:.git/config\x00user.email\nsynthetic@example.invalid\x00"
	inventory, err = CollectPreservation(context.Background(), validCollectRequest(f, ordinary.root), ordinary, fixedRevision(f.record.Binding.HomeRevision))
	if err != nil || !inventory.Complete {
		t.Fatalf("ordinary core config was refused: %+v %v", inventory, err)
	}
	for _, flags := range []string{"S hidden.txt\x00", "s hidden.txt\x00", "h hidden.txt\x00", "H README\x00S hidden.txt\x00"} {
		flagged := newScriptGit(t, scriptOptions{})
		flagged.extra.indexFlags = flags
		inventory, err = CollectPreservation(context.Background(), validCollectRequest(f, flagged.root), flagged, fixedRevision(f.record.Binding.HomeRevision))
		if err == nil || inventory.Complete || !strings.Contains(err.Error(), "index flags hide edits") {
			t.Fatalf("flags %q were preserved: %+v %v", flags, inventory, err)
		}
		for _, call := range flagged.calls {
			if call[0] == "status" {
				t.Fatalf("status ran with flags %q", flags)
			}
		}
	}
	clean := newScriptGit(t, scriptOptions{})
	clean.extra.indexFlags = "H README\x00"
	inventory, err = CollectPreservation(context.Background(), validCollectRequest(f, clean.root), clean, fixedRevision(f.record.Binding.HomeRevision))
	if err != nil || !inventory.Complete || !inventory.Repositories[0].Worktrees[0].SubmodulesComplete {
		t.Fatalf("ordinary index was refused: %+v %v", inventory, err)
	}
	childGit := newScriptGit(t, scriptOptions{})
	child := filepath.Join(childGit.root, "vendor", "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(child, ".git"), "gitdir: synthetic\n")
	writeFile(t, filepath.Join(childGit.root, ".gitmodules"), "[submodule \"vendor\"]\n\tpath = vendor/sub\n\turl = ../sub\n")
	childGit.extra.modulesList = "submodule.vendor.path\nvendor/sub\x00submodule.vendor.url\n../sub\x00"
	childGit.extra.submoduleStatus = " " + strings.Repeat("a", 40) + " vendor/sub\n"
	childGit.extra.indexFlags = "S hidden.txt\x00"
	childGit.extra.indexFlagDir = child
	inventory, err = CollectPreservation(context.Background(), validCollectRequest(f, childGit.root), childGit, fixedRevision(f.record.Binding.HomeRevision))
	if err == nil || inventory.Complete || !strings.Contains(err.Error(), "index flags hide edits") {
		t.Fatalf("child index flags were preserved: %+v %v", inventory, err)
	}
	if sawGit(childGit, child, func(call []string) bool { return call[0] == "status" }) {
		t.Fatal("child status ran")
	}
}

func TestGitmodulesAndIndexAllowlist(t *testing.T) {
	if supportedRepositoryKey("core.worktree", "/clean") || !supportedRepositoryKey("core.bare", "false") {
		t.Fatal("core allowlist mismatch")
	}
	if !supportedGitmodulesKey("submodule.vendor.path", "vendor/sub") || !supportedGitmodulesKey("submodule.vendor.url", "../sub") || !supportedGitmodulesKey("submodule.vendor.url", "https://example.invalid/sub.git") || !supportedGitmodulesKey("submodule.quoted.path", "vendor/quoted sub") || !supportedGitmodulesKey("submodule.vendor.active", "true") || !supportedGitmodulesKey("submodule.foo.bar.path", "lib/foo") {
		t.Fatal("ordinary gitmodules key was refused")
	}
	for _, tc := range []struct{ key, value string }{
		{"filter.x.clean", "touch-attacked"},
		{"include.path", "../other"},
		{"submodule.vendor.update", "!touch-attacked"},
		{"submodule.vendor.update", "checkout"},
		{"submodule.vendor.ignore", "all"},
		{"submodule.vendor.branch", "main"},
		{"submodule.vendor.url", "ext::cmd"},
		{"submodule.vendor.url", "!touch"},
		{"submodule.vendor.active", "yes"},
		{"submodule.vendor.path", "vendor/sub # pin"},
		{"submodule.vendor.path", "\"vendor/sub\""},
	} {
		if supportedGitmodulesKey(tc.key, tc.value) {
			t.Fatalf("allowed %s=%s", tc.key, tc.value)
		}
	}
	if err := rejectHiddenIndexFlags(""); err != nil {
		t.Fatal(err)
	}
	if err := rejectHiddenIndexFlags("H README\x00H vendor/sub\x00"); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{"S hidden.txt\x00", "s hidden.txt\x00", "h README\x00", "M README\x00"} {
		if err := rejectHiddenIndexFlags(out); err == nil || !strings.Contains(err.Error(), "index flags hide edits") {
			t.Fatalf("accepted %q: %v", out, err)
		}
	}
	sha := strings.Repeat("c", 40)
	ordinary := "100644 " + sha + " 0\tREADME\x00"
	if err := rejectUnmappedGitlinks("", nil); err != nil {
		t.Fatal(err)
	}
	if err := rejectUnmappedGitlinks(ordinary, nil); err != nil {
		t.Fatal(err)
	}
	mapped := "160000 " + sha + " 0\tvendor/sub\x00"
	if err := rejectUnmappedGitlinks(mapped, []string{"vendor/sub"}); err != nil {
		t.Fatal(err)
	}
	if err := rejectUnmappedGitlinks(mapped+"160000 "+sha+" 1\tvendor/sub\x00", []string{"vendor/sub"}); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{mapped, ordinary + "160000 " + sha + " 0\tvendor/orphan\x00"} {
		if err := rejectUnmappedGitlinks(out, []string{"vendor/listed"}); err == nil || !strings.Contains(err.Error(), "submodule mapping missing") {
			t.Fatalf("accepted unmapped gitlink %q: %v", out, err)
		}
	}
	if err := rejectUnmappedGitlinks("160000 "+sha+" 0", nil); err == nil || !strings.Contains(err.Error(), "index metadata unreadable") {
		t.Fatalf("malformed gitlink was accepted: %v", err)
	}
}

func TestPrunableBareAndBisectAreNotOmitted(t *testing.T) {
	f := newFixture(t)
	for _, options := range []scriptOptions{{prunable: true}, {bare: true}} {
		git := newScriptGit(t, options)
		inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
		if err == nil || !strings.Contains(err.Error(), "prunable or bare worktree") || inventory.Complete {
			t.Fatalf("omitted worktree: %+v %v", inventory, err)
		}
	}
	git := newScriptGit(t, scriptOptions{bisect: true})
	inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Repositories[0].Worktrees[0].OperationInProgress {
		t.Fatal("BISECT_START looked idle")
	}
}

func TestIgnoredNestedRepositoryIsDiscoveredWithoutFollowingSymlinks(t *testing.T) {
	f := newFixture(t)
	git := newScriptGit(t, scriptOptions{})
	hidden := filepath.Join(git.root, "skip", "hidden", ".git")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.Repositories[0].Worktrees[0].NestedRepository {
		t.Fatal("ignored nested repository was omitted")
	}
	linked := newScriptGit(t, scriptOptions{})
	if err := os.Symlink(t.TempDir(), filepath.Join(linked.root, "escaped")); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectPreservation(context.Background(), validCollectRequest(f, linked.root), linked, fixedRevision(f.record.Binding.HomeRevision)); err == nil || !strings.Contains(err.Error(), "symlink refused") {
		t.Fatal(err)
	}
}

func TestNestedWalkBoundFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxCollectStatus; i++ {
		if err := os.WriteFile(filepath.Join(root, "f"+strconv.Itoa(i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found, err := discoverNestedRepositories(root, filepath.Join(root, ".git"), map[string]bool{})
	if err == nil || found {
		t.Fatalf("bound walk returned found=%v err=%v", found, err)
	}
}

func TestCollectedInventoryEvaluatesFromReceipts(t *testing.T) {
	f := newFixture(t)
	git := newScriptGit(t, scriptOptions{})
	when := formatTime(f.now.Add(-3 * time.Minute))
	head := "HEAD:" + git.root
	allow := map[string]bool{
		"github:example/project\nrefs/heads/main": true,
		"github:example/project\n" + head:         true,
	}
	collect := func(pubs PublicationSource) Inventory {
		t.Helper()
		req := validCollectRequest(f, git.root)
		req.Verifier = f.trust
		req.Publications = pubs
		inventory, err := CollectPreservation(context.Background(), req, git, fixedRevision(req.Binding.HomeRevision))
		if err != nil {
			t.Fatal(err)
		}
		if len(inventory.Repositories) != 1 || inventory.Repositories[0].FetchSucceeded {
			t.Fatalf("FetchSucceeded became evidence: %+v", inventory.Repositories)
		}
		return inventory
	}
	preserved := evaluateCollected(t, f, collect(scriptPubs{key: f.keys["publication"], allow: allow, when: when}))
	if len(preserved.ImmediateCandidates) != 1 || preserved.ImmediateCandidates[0].Status != "immediate_preserved" || preserved.DeleteEnabled || len(preserved.StoppedWorkQueue) != 0 {
		t.Fatalf("stopped inventory with receipts was not preserved: %+v", preserved)
	}
	refused := evaluateCollected(t, f, collect(nil))
	if len(refused.ImmediateCandidates) != 0 || len(refused.StoppedWorkQueue) != 1 {
		t.Fatalf("missing receipts were preserved: %+v", refused)
	}
	seen := false
	for _, reason := range refused.StoppedWorkQueue[0].Reasons {
		if reason == "publication_receipt_missing_or_invalid" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("receipt refusal absent: %v", refused.StoppedWorkQueue[0].Reasons)
	}
}

func evaluateCollected(t *testing.T, f fixture, inventory Inventory) Report {
	t.Helper()
	record := f.record
	payload := record.Proof.Payload
	payload.Inventory = inventory
	record.Proof = &Signed[ProofPayload]{Payload: payload, Signature: f.sign(t, "proof", payload)}
	report, err := Evaluate(Snapshot{Version: Version, Records: []Record{record}}, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestCollectScriptedInventoryDoesNotTrustTrackingOrFetch(t *testing.T) {
	f := newFixture(t)
	git := newScriptGit(t, scriptOptions{})
	req := validCollectRequest(f, git.root)
	req.Verifier = f.trust
	req.Publications = scriptPubs{key: f.keys["publication"], allow: map[string]bool{}, when: formatTime(f.now)}
	inventory, err := CollectPreservation(context.Background(), req, git, fixedRevision(req.Binding.HomeRevision))
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repositories) != 1 || inventory.Repositories[0].FetchSucceeded {
		t.Fatalf("fetch boolean became proof: %+v", inventory.Repositories)
	}
	repo := inventory.Repositories[0]
	if !repo.WorktreesComplete || !repo.RefsComplete || !repo.StashesComplete || len(repo.UnpushedRefs) == 0 {
		t.Fatalf("inventory was not explicit: %+v", repo)
	}
	for _, ref := range repo.Refs {
		if len(ref.Receipts) != 0 {
			t.Fatal("nil publication boundary attached a receipt")
		}
	}
	for _, call := range git.calls {
		if strings.Contains(strings.Join(call, " "), "remotes") || call[0] == "fetch" || call[0] == "rev-list" {
			t.Fatalf("collector consulted remote state: %q", call)
		}
	}
	req.Publications = scriptPubs{key: f.keys["publication"], allow: map[string]bool{"github:example/project\nrefs/heads/main": true, "github:example/project\nHEAD:" + git.root: true}, when: formatTime(f.now)}
	inventory, err = CollectPreservation(context.Background(), req, git, fixedRevision(req.Binding.HomeRevision))
	if err != nil {
		t.Fatal(err)
	}
	repo = inventory.Repositories[0]
	if repo.FetchSucceeded || len(repo.UnpushedRefs) != 0 {
		t.Fatalf("verified receipts still depended on fetch state: %+v", repo)
	}
	req.Publications = scriptPubs{key: f.keys["publication"], allow: map[string]bool{"github:example/project\nrefs/heads/main": true}, mismatch: true, when: formatTime(f.now)}
	if _, err := CollectPreservation(context.Background(), req, git, fixedRevision(req.Binding.HomeRevision)); err == nil || !strings.Contains(err.Error(), "publication proof rejected") {
		t.Fatal(err)
	}
	req.Publications = scriptPubs{key: f.keys["publication"], allow: map[string]bool{"github:example/project\nrefs/heads/main": true}, when: formatTime(f.now)}
	req.Verifier = nil
	if _, err := CollectPreservation(context.Background(), req, git, fixedRevision(req.Binding.HomeRevision)); err == nil || !strings.Contains(err.Error(), "verifier absent") {
		t.Fatal(err)
	}
}

func TestCollectScriptedRaceAndReadErrors(t *testing.T) {
	f := newFixture(t)
	git := newScriptGit(t, scriptOptions{race: true})
	_, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
	if err == nil || !strings.Contains(err.Error(), "preservation race") {
		t.Fatal(err)
	}
	git = newScriptGit(t, scriptOptions{fail: true})
	_, err = CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
	if err == nil || !strings.Contains(err.Error(), "preservation incomplete") {
		t.Fatal(err)
	}
	git = newScriptGit(t, scriptOptions{})
	late := &lateRevision{value: f.record.Binding.HomeRevision}
	_, err = CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, late)
	if err == nil || !strings.Contains(err.Error(), "home revision changed") {
		t.Fatal(err)
	}
	git = newScriptGit(t, scriptOptions{dirty: true, nested: true, operation: true, detached: true})
	inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, git.root), git, fixedRevision(f.record.Binding.HomeRevision))
	if err != nil {
		t.Fatal(err)
	}
	worktree := inventory.Repositories[0].Worktrees[0]
	if !worktree.Staged || !worktree.Unstaged || !worktree.Untracked || !worktree.NestedRepository || !worktree.OperationInProgress {
		t.Fatalf("local state was dropped: %+v", worktree)
	}
	if inventory.Repositories[0].Worktrees[0].HeadRef != "HEAD:"+git.root {
		t.Fatalf("detached HEAD was not bound: %+v", worktree)
	}
}

func TestCollectSyntheticWorktreeDoesNotMutate(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	home := filepath.Join(root, "git-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	detached := filepath.Join(root, "detached")
	sub := filepath.Join(root, "sub")
	run := func(args ...string) { runGit(t, home, args...) }
	run("-c", "init.defaultBranch=main", "init", work)
	run("-C", work, "config", "user.email", "synthetic@example.invalid")
	run("-C", work, "config", "user.name", "Synthetic")
	writeFile(t, filepath.Join(work, "README"), "hello\n")
	run("-C", work, "add", "README")
	run("-C", work, "commit", "-m", "synthetic initial")
	if branch := strings.TrimSpace(runGitOutput(t, home, "-C", work, "symbolic-ref", "HEAD")); branch != "refs/heads/main" {
		t.Fatalf("setup branch %s", branch)
	}
	run("-c", "init.defaultBranch=main", "init", "--bare", bare)
	run("-C", work, "remote", "add", "origin", bare)
	run("-C", work, "-c", "protocol.file.allow=always", "push", "origin", "HEAD:main")
	run("-c", "init.defaultBranch=main", "init", sub)
	run("-C", sub, "config", "user.email", "synthetic@example.invalid")
	run("-C", sub, "config", "user.name", "Synthetic")
	writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
	run("-C", sub, "add", "lib.txt")
	run("-C", sub, "commit", "-m", "synthetic sub")
	run("-C", work, "-c", "protocol.file.allow=always", "submodule", "add", "--name", "vendor", "../sub", "vendor/sub")
	run("-C", work, "commit", "-m", "synthetic submodule")
	writeFile(t, filepath.Join(work, "README"), "hello\nmore\n")
	run("-C", work, "commit", "-am", "synthetic unpublished")
	run("-C", work, "tag", "-a", "v1", "-m", "synthetic tag")
	run("-C", work, "branch", "extra")
	run("-C", work, "update-ref", "refs/remotes/origin/main", "HEAD")
	writeFile(t, filepath.Join(work, "README"), "hello\nmore\nstash\n")
	run("-C", work, "stash", "push", "-m", "synthetic", "--", "README")
	writeFile(t, filepath.Join(work, "README"), "hello\nmore\ndirty\n")
	writeFile(t, filepath.Join(work, "staged.txt"), "staged\n")
	run("-C", work, "add", "staged.txt")
	writeFile(t, filepath.Join(work, "extra.txt"), "extra\n")
	nested := filepath.Join(work, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	run("-c", "init.defaultBranch=main", "init", nested)
	run("-C", nested, "config", "user.email", "synthetic@example.invalid")
	run("-C", nested, "config", "user.name", "Synthetic")
	writeFile(t, filepath.Join(nested, "inside.txt"), "inside\n")
	run("-C", nested, "add", "inside.txt")
	run("-C", nested, "commit", "-m", "synthetic nested")
	run("-C", work, "worktree", "add", "--detach", detached, "HEAD")

	paths := []string{work, detached, bare, sub, filepath.Join(work, "vendor", "sub")}
	before := map[string]string{}
	runner, err := NewExecGit()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	})
	recorded := &recordingGit{next: runner}
	req := validCollectRequest(f, work)
	req.Verifier = f.trust
	req.Publications = scriptPubs{key: f.keys["publication"], allow: map[string]bool{"github:example/project\nrefs/tags/v1": true}, when: formatTime(f.now)}
	req.SubmoduleIdentities = map[string]string{SubmoduleIdentityKey("github:example/project", "vendor/sub"): "synthetic:vendor"}
	// Git creates core.worktree for this submodule. The collector deliberately
	// refuses that override; verify the refusal before preparing the supported
	// fixture shape. Only test setup changes the synthetic repository config.
	if _, err := CollectPreservation(context.Background(), req, recorded, fixedRevision(req.Binding.HomeRevision)); err == nil || !strings.Contains(err.Error(), "unsupported git config") {
		t.Fatalf("Git-created core.worktree was not refused: %v", err)
	}
	run("-C", filepath.Join(work, "vendor", "sub"), "config", "--unset", "core.worktree")
	// An absorbed submodule needs that override when Git enters its separate
	// git directory. Use the supported embedded .git directory layout instead.
	modulePath := filepath.Join(work, "vendor", "sub")
	moduleGitDir := strings.TrimSpace(runGitOutput(t, home, "-C", modulePath, "rev-parse", "--absolute-git-dir"))
	if err := os.Rename(filepath.Join(modulePath, ".git"), filepath.Join(root, "saved-submodule-gitfile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moduleGitDir, filepath.Join(modulePath, ".git")); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		before[path] = passiveFingerprint(t, home, path)
	}
	inventory, err := CollectPreservation(context.Background(), req, recorded, fixedRevision(req.Binding.HomeRevision))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if after := passiveFingerprint(t, home, path); after != before[path] {
			t.Fatalf("repository changed %s", path)
		}
	}
	for _, call := range recorded.calls {
		joined := strings.Join(call, " ")
		if call[0] == "fetch" || call[0] == "checkout" || call[0] == "reset" || call[0] == "update-ref" || strings.Contains(joined, "--remotes") {
			t.Fatalf("mutating or remote-tracking command: %q", call)
		}
	}
	var parent, vendor Repository
	for _, repo := range inventory.Repositories {
		if repo.FetchSucceeded {
			t.Fatal("collector synthesized FetchSucceeded")
		}
		switch repo.Identity {
		case "github:example/project":
			parent = repo
		case "synthetic:vendor":
			vendor = repo
		}
	}
	if parent.Identity == "" || vendor.Identity == "" {
		t.Fatalf("missing repositories: %+v", inventory.Repositories)
	}
	if !containsString(parent.UnpushedRefs, "refs/heads/main") || containsString(parent.UnpushedRefs, "refs/tags/v1") || len(parent.Stashes) != 1 {
		t.Fatalf("publication or stash accounting: %+v", parent)
	}
	var tagged Ref
	main, mainOK := findWorktree(parent, work)
	detachedTree, detachedOK := findWorktree(parent, detached)
	for _, ref := range parent.Refs {
		if ref.Name == "refs/tags/v1" {
			tagged = ref
		}
	}
	if !detachedOK || detachedTree.HeadRef != "HEAD:"+detachedTree.Path || detachedTree.Staged || detachedTree.Unstaged {
		t.Fatalf("detached worktree: %+v paths=%v", detachedTree, worktreePaths(parent))
	}
	if tagged.Kind != "tag" || tagged.Object == tagged.Commit || len(tagged.Receipts) != 1 || tagged.Receipts[0].Payload.Method != "authenticated_remote_query" {
		t.Fatalf("annotated tag receipt: %+v", tagged)
	}
	if !mainOK || !main.Staged || !main.Unstaged || !main.Untracked || !main.NestedRepository || !main.SubmodulesComplete || !main.SubmodulesClean {
		t.Fatalf("main worktree: %+v paths=%v", main, worktreePaths(parent))
	}
	if len(vendor.UnpushedRefs) == 0 || vendor.FetchSucceeded {
		t.Fatalf("submodule publication was inferred: %+v", vendor)
	}
}

func TestSubmoduleCleanFilterDoesNotRun(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	home := filepath.Join(root, "git-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	sub := filepath.Join(root, "sub")
	sentinel := filepath.Join(root, "clean-filter-sentinel")
	scriptPath := filepath.Join(root, "clean-filter.sh")
	if strings.ContainsAny(sentinel, "'\\\n") {
		t.Fatal("sentinel path is not a safe filter argument")
	}
	writeFile(t, scriptPath, "#!/bin/sh\ntouch '"+sentinel+"'\ncat\n")
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatal(err)
	}
	initSyntheticRepo(t, home, work)
	writeFile(t, filepath.Join(work, "README"), "hello\n")
	runGit(t, home, "-C", work, "add", "README")
	runGit(t, home, "-C", work, "commit", "-m", "synthetic initial")
	initSyntheticRepo(t, home, sub)
	writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
	runGit(t, home, "-C", sub, "add", "lib.txt")
	runGit(t, home, "-C", sub, "commit", "-m", "synthetic sub")
	runGit(t, home, "-C", work, "-c", "protocol.file.allow=always", "submodule", "add", "--name", "vendor", "../sub", "vendor/sub")
	runGit(t, home, "-C", work, "commit", "-m", "synthetic submodule")
	checkout := filepath.Join(work, "vendor", "sub")
	writeFile(t, filepath.Join(checkout, ".gitattributes"), "* filter=attack\n")
	runGit(t, home, "-C", checkout, "config", "filter.attack.clean", scriptPath)
	writeFile(t, filepath.Join(checkout, "lib.txt"), "LIB\n")

	runner, err := NewExecGit()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	})
	req := validCollectRequest(f, work)
	req.SubmoduleIdentities = map[string]string{SubmoduleIdentityKey("github:example/project", "vendor/sub"): "synthetic:vendor"}
	inventory, err := CollectPreservation(context.Background(), req, runner, fixedRevision(req.Binding.HomeRevision))
	if err == nil || inventory.Complete || !strings.Contains(err.Error(), "unsupported git config") {
		t.Fatalf("submodule filter was not refused: %+v %v", inventory, err)
	}
	assertFilterDidNotRun(t, sentinel)
	// Production status is only allowlisted with --ignore-submodules=all, so it
	// cannot prove this premise. The historical argv runs as direct git.
	assertDefaultStatusRunsFilter(t, home, work, sentinel)
}

func TestUnmappedPopulatedGitlinkDoesNotRunCleanFilter(t *testing.T) {
	f := newFixture(t)
	collectChild := func(t *testing.T, modules bool) {
		t.Helper()
		root := t.TempDir()
		home, script, sentinel := prepareCleanFilter(t, root)
		work := filepath.Join(root, "work")
		initSyntheticRepo(t, home, work)
		writeFile(t, filepath.Join(work, "README"), "hello\n")
		runGit(t, home, "-C", work, "add", "README")
		runGit(t, home, "-C", work, "commit", "-m", "synthetic initial")
		orphan := filepath.Join(work, "vendor", "orphan")
		initSyntheticRepo(t, home, orphan)
		writeFile(t, filepath.Join(orphan, "lib.txt"), "lib\n")
		runGit(t, home, "-C", orphan, "add", "lib.txt")
		runGit(t, home, "-C", orphan, "commit", "-m", "synthetic orphan")
		sha := strings.TrimSpace(runGitOutput(t, home, "-C", orphan, "rev-parse", "HEAD"))
		if modules {
			writeFile(t, filepath.Join(work, ".gitmodules"), "[submodule \"listed\"]\n\tpath = vendor/listed\n\turl = ../listed\n")
			runGit(t, home, "-C", work, "add", ".gitmodules")
		}
		addSyntheticGitlink(t, home, work, "vendor/orphan", sha)
		runGit(t, home, "-C", work, "commit", "-m", "synthetic gitlink")
		armCleanFilter(t, home, orphan, script, "lib.txt")
		runner := newCollectorGit(t)
		inventory, err := CollectPreservation(context.Background(), validCollectRequest(f, work), runner, fixedRevision(f.record.Binding.HomeRevision))
		if err == nil || inventory.Complete || !strings.Contains(err.Error(), "submodule mapping missing") {
			t.Fatalf("unmapped gitlink was inventoried: %+v %v", inventory, err)
		}
		assertFilterDidNotRun(t, sentinel)
		assertDefaultStatusRunsFilter(t, home, work, sentinel)
	}
	t.Run("absent gitmodules", func(t *testing.T) { collectChild(t, false) })
	t.Run("gitmodules omits gitlink", func(t *testing.T) { collectChild(t, true) })
	t.Run("grandchild", func(t *testing.T) {
		root := t.TempDir()
		home, script, sentinel := prepareCleanFilter(t, root)
		work := filepath.Join(root, "work")
		sub := filepath.Join(root, "sub")
		initSyntheticRepo(t, home, work)
		writeFile(t, filepath.Join(work, "README"), "hello\n")
		runGit(t, home, "-C", work, "add", "README")
		runGit(t, home, "-C", work, "commit", "-m", "synthetic initial")
		initSyntheticRepo(t, home, sub)
		writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
		runGit(t, home, "-C", sub, "add", "lib.txt")
		runGit(t, home, "-C", sub, "commit", "-m", "synthetic sub")
		runGit(t, home, "-C", work, "-c", "protocol.file.allow=always", "submodule", "add", "--name", "vendor", "../sub", "vendor/sub")
		runGit(t, home, "-C", work, "commit", "-m", "synthetic submodule")
		checkout := filepath.Join(work, "vendor", "sub")
		nested := filepath.Join(checkout, "nested")
		initSyntheticRepo(t, home, nested)
		writeFile(t, filepath.Join(nested, "inner.txt"), "inner\n")
		runGit(t, home, "-C", nested, "add", "inner.txt")
		runGit(t, home, "-C", nested, "commit", "-m", "synthetic nested")
		nestedSHA := strings.TrimSpace(runGitOutput(t, home, "-C", nested, "rev-parse", "HEAD"))
		addSyntheticGitlink(t, home, checkout, "nested", nestedSHA)
		runGit(t, home, "-C", checkout, "commit", "-m", "synthetic grandchild gitlink")
		embedSyntheticGitDir(t, home, checkout, filepath.Join(root, "saved-vendor-gitfile"))
		armCleanFilter(t, home, nested, script, "inner.txt")
		runner := newCollectorGit(t)
		req := validCollectRequest(f, work)
		req.SubmoduleIdentities = map[string]string{SubmoduleIdentityKey("github:example/project", "vendor/sub"): "synthetic:vendor"}
		inventory, err := CollectPreservation(context.Background(), req, runner, fixedRevision(req.Binding.HomeRevision))
		if err == nil || inventory.Complete || !strings.Contains(err.Error(), "submodule mapping missing") {
			t.Fatalf("grandchild gitlink was inventoried: %+v %v", inventory, err)
		}
		assertFilterDidNotRun(t, sentinel)
		assertDefaultStatusRunsFilter(t, home, work, sentinel)
	})
}

func TestDirtyMappedSubmoduleStaysComplete(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	home := filepath.Join(root, "git-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	sub := filepath.Join(root, "sub")
	initSyntheticRepo(t, home, work)
	writeFile(t, filepath.Join(work, "README"), "hello\n")
	runGit(t, home, "-C", work, "add", "README")
	runGit(t, home, "-C", work, "commit", "-m", "synthetic initial")
	initSyntheticRepo(t, home, sub)
	writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
	runGit(t, home, "-C", sub, "add", "lib.txt")
	runGit(t, home, "-C", sub, "commit", "-m", "synthetic sub")
	runGit(t, home, "-C", work, "-c", "protocol.file.allow=always", "submodule", "add", "--name", "vendor", "../sub", "vendor/sub")
	runGit(t, home, "-C", work, "commit", "-m", "synthetic submodule")
	checkout := filepath.Join(work, "vendor", "sub")
	embedSyntheticGitDir(t, home, checkout, filepath.Join(root, "saved-vendor-gitfile"))
	writeFile(t, filepath.Join(checkout, "lib.txt"), "LIB\n")

	runner := newCollectorGit(t)
	recorded := &recordingGit{next: runner}
	req := validCollectRequest(f, work)
	req.SubmoduleIdentities = map[string]string{SubmoduleIdentityKey("github:example/project", "vendor/sub"): "synthetic:vendor"}
	inventory, err := CollectPreservation(context.Background(), req, recorded, fixedRevision(req.Binding.HomeRevision))
	if err != nil || !inventory.Complete {
		t.Fatalf("dirty child was refused: %+v %v", inventory, err)
	}
	var parent, vendor Repository
	for _, repo := range inventory.Repositories {
		switch repo.Identity {
		case "github:example/project":
			parent = repo
		case "synthetic:vendor":
			vendor = repo
		}
	}
	main, mainOK := findWorktree(parent, work)
	child, childOK := findWorktree(vendor, checkout)
	if parent.Identity == "" || vendor.Identity == "" || !mainOK || !main.SubmodulesComplete || main.SubmodulesClean || !childOK || !child.Unstaged {
		t.Fatalf("dirty child was dropped: parent=%+v child=%+v", main, child)
	}
	sawStatus := false
	for _, call := range recorded.calls {
		if call[0] != "status" {
			continue
		}
		sawStatus = true
		if !slices.Equal(call, statusArgs) {
			t.Fatalf("collector status can enter a descendant: %q", call)
		}
	}
	if !sawStatus {
		t.Fatal("collector did not read status")
	}
}

func TestNestedEmbeddedSubmoduleStaysWithItsParent(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	home := filepath.Join(root, "git-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	sub := filepath.Join(root, "sub")
	nestedSrc := filepath.Join(root, "nested-src")
	initSyntheticRepo(t, home, work)
	writeFile(t, filepath.Join(work, "README"), "hello\n")
	runGit(t, home, "-C", work, "add", "README")
	runGit(t, home, "-C", work, "commit", "-m", "synthetic initial")
	initSyntheticRepo(t, home, sub)
	writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
	runGit(t, home, "-C", sub, "add", "lib.txt")
	runGit(t, home, "-C", sub, "commit", "-m", "synthetic sub")
	initSyntheticRepo(t, home, nestedSrc)
	writeFile(t, filepath.Join(nestedSrc, "inner.txt"), "inner\n")
	runGit(t, home, "-C", nestedSrc, "add", "inner.txt")
	runGit(t, home, "-C", nestedSrc, "commit", "-m", "synthetic nested")
	runGit(t, home, "-C", work, "-c", "protocol.file.allow=always", "submodule", "add", "--name", "vendor", "../sub", "vendor/sub")
	checkout := filepath.Join(work, "vendor", "sub")
	runGit(t, home, "-C", checkout, "-c", "protocol.file.allow=always", "submodule", "add", "--name", "nested", nestedSrc, "nested")
	runGit(t, home, "-C", checkout, "commit", "-m", "synthetic nested submodule")
	runGit(t, home, "-C", work, "add", "vendor/sub")
	runGit(t, home, "-C", work, "commit", "-m", "synthetic submodule")

	runner, err := NewExecGit()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	})
	req := validCollectRequest(f, work)
	req.SubmoduleIdentities = map[string]string{
		SubmoduleIdentityKey("github:example/project", "vendor/sub"): "synthetic:vendor",
		SubmoduleIdentityKey("synthetic:vendor", "nested"):           "synthetic:nested",
	}
	if inventory, err := CollectPreservation(context.Background(), req, runner, fixedRevision(req.Binding.HomeRevision)); err == nil || inventory.Complete || !strings.Contains(err.Error(), "unsupported git config") {
		t.Fatalf("Git-created core.worktree was accepted: %+v %v", inventory, err)
	}
	embedSyntheticGitDir(t, home, filepath.Join(checkout, "nested"), filepath.Join(root, "saved-nested-gitfile"))
	embedSyntheticGitDir(t, home, checkout, filepath.Join(root, "saved-vendor-gitfile"))

	paths := []string{work, checkout, filepath.Join(checkout, "nested")}
	before := map[string]string{}
	for _, path := range paths {
		before[path] = passiveFingerprint(t, home, path)
	}
	recorded := &recordingGit{next: runner}
	inventory, err := CollectPreservation(context.Background(), req, recorded, fixedRevision(req.Binding.HomeRevision))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if after := passiveFingerprint(t, home, path); after != before[path] {
			t.Fatalf("repository changed %s", path)
		}
	}
	for _, call := range recorded.calls {
		if call[0] == "submodule" && slices.Contains(call, "--recursive") {
			t.Fatalf("recursive submodule status: %q", call)
		}
	}
	seen := map[string]bool{}
	for _, repo := range inventory.Repositories {
		seen[repo.Identity] = true
	}
	if !seen["github:example/project"] || !seen["synthetic:vendor"] || !seen["synthetic:nested"] || len(inventory.Repositories) != 3 {
		t.Fatalf("nested identity was not kept with its parent: %+v", inventory.Repositories)
	}
}

func TestImmediateSubmoduleDepthStaysBounded(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	req := CollectRequest{SubmoduleIdentities: map[string]string{SubmoduleIdentityKey("parent", "child"): "child-id"}}
	snap := repoSnapshot{Worktrees: []worktreeSnapshot{{
		Path:       root,
		Submodules: []submoduleSnapshot{{Path: "child", State: " "}},
	}}}
	if got, err := submoduleChildren(req, queuedRepo{Identity: "parent", Path: root, Depth: maxCollectDepth - 1}, snap); err == nil || !strings.Contains(err.Error(), "submodule depth exceeded") {
		t.Fatalf("depth cap: %+v %v", got, err)
	}
	got, err := submoduleChildren(req, queuedRepo{Identity: "parent", Path: root, Depth: maxCollectDepth - 2}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Identity != "child-id" || got[0].Depth != maxCollectDepth-1 || got[0].Path != child {
		t.Fatalf("immediate child: %+v", got)
	}
}

type fixedRevision string

func (r fixedRevision) Current(context.Context) (string, error) { return string(r), nil }

type lateRevision struct {
	value string
	calls int
}

func (r *lateRevision) Current(context.Context) (string, error) {
	r.calls++
	if r.calls >= 3 {
		return "revision-changed", nil
	}
	return r.value, nil
}

type forbidGit struct{ t *testing.T }

func (g forbidGit) Run(context.Context, string, []string) (string, int, error) {
	g.t.Fatal("git was called")
	return "", 0, errors.New("git was called")
}

type recordingGit struct {
	next  GitRunner
	calls [][]string
}

func (g *recordingGit) Run(ctx context.Context, dir string, args []string) (string, int, error) {
	g.calls = append(g.calls, append([]string(nil), args...))
	return g.next.Run(ctx, dir, args)
}

type scriptOptions struct {
	race, fail, dirty, nested, operation, detached, prunable, bare, bisect bool
	attack                                                                 string
	attackDir                                                              string
}

type scriptExtra struct {
	localConfig        string
	modulesList        string
	submoduleStatus    string
	indexFlags         string
	indexFlagDir       string
	indexGitlinks      string
	indexGitlinksByDir map[string]string
}

type scriptGit struct {
	t        *testing.T
	root     string
	options  scriptOptions
	calls    [][]string
	dirs     []string
	refCalls int
	executed bool
	extra    scriptExtra
}

func newScriptGit(t *testing.T, options scriptOptions) *scriptGit {
	t.Helper()
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if options.operation {
		writeFile(t, filepath.Join(gitDir, "MERGE_HEAD"), strings.Repeat("a", 40)+"\n")
	}
	if options.bisect {
		writeFile(t, filepath.Join(gitDir, "BISECT_START"), "refs/heads/main\n")
	}
	return &scriptGit{t: t, root: root, options: options}
}

func (g *scriptGit) Run(_ context.Context, dir string, args []string) (string, int, error) {
	g.calls = append(g.calls, append([]string(nil), args...))
	g.dirs = append(g.dirs, dir)
	attacked := g.options.attack != "" && (g.options.attackDir == "" || dir == g.options.attackDir)
	if (attacked && args[0] != "config") || (args[0] == "submodule" && g.options.attack != "") {
		g.executed = true
	}
	if g.options.fail && args[0] == "worktree" {
		return "", -1, errors.New("synthetic read error")
	}
	if allowedFileConfig(args) {
		return g.extra.modulesList, 0, nil
	}
	switch strings.Join(args, "\x00") {
	case strings.Join(localConfigArgs, "\x00"):
		if attacked {
			return g.options.attack, 0, nil
		}
		if g.extra.localConfig != "" {
			return g.extra.localConfig, 0, nil
		}
		return "", 0, nil
	case strings.Join([]string{"rev-parse", "--git-common-dir"}, "\x00"):
		return ".git\n", 0, nil
	case strings.Join([]string{"rev-parse", "--absolute-git-dir"}, "\x00"):
		return filepath.Join(g.root, ".git") + "\n", 0, nil
	case strings.Join([]string{"rev-parse", "--verify", "--quiet", "HEAD"}, "\x00"):
		return strings.Repeat("a", 40) + "\n", 0, nil
	case strings.Join([]string{"worktree", "list", "--porcelain"}, "\x00"):
		mode := "branch refs/heads/main"
		if g.options.detached {
			mode = "detached"
		}
		out := "worktree " + g.root + "\nHEAD " + strings.Repeat("a", 40) + "\n" + mode + "\n"
		sha := strings.Repeat("a", 40)
		if g.options.prunable {
			out += "\nworktree " + g.root + "-prunable\nHEAD " + sha + "\nbranch refs/heads/main\nprunable missing\n"
		}
		if g.options.bare {
			out += "\nworktree " + g.root + "-bare\nHEAD " + sha + "\nbare\n"
		}
		return out, 0, nil
	case strings.Join([]string{"for-each-ref", "--format=" + forEachRefFormat, "refs/heads", "refs/tags"}, "\x00"):
		g.refCalls++
		sha := strings.Repeat("a", 40)
		if g.options.race && g.refCalls == 2 {
			sha = strings.Repeat("b", 40)
		}
		return sha + "\x00commit\x00\x00refs/heads/main\n", 0, nil
	case strings.Join([]string{"stash", "list", "--format=%H"}, "\x00"):
		return "", 0, nil
	case strings.Join(statusArgs, "\x00"):
		if g.options.dirty {
			return " M README\x00A  staged.txt\x00?? extra.txt\x00", 0, nil
		}
		return "", 0, nil
	case strings.Join([]string{"ls-files", "-o", "--exclude-standard", "-z"}, "\x00"):
		if g.options.nested {
			return "nested/\x00", 0, nil
		}
		return "", 0, nil
	case strings.Join(indexFlagArgs, "\x00"):
		if g.extra.indexFlagDir == "" || dir == g.extra.indexFlagDir {
			return g.extra.indexFlags, 0, nil
		}
		return "", 0, nil
	case strings.Join(headTreeArgs, "\x00"):
		return "", 0, nil
	case strings.Join(gitlinkIndexArgs, "\x00"):
		if g.extra.indexGitlinksByDir != nil {
			return g.extra.indexGitlinksByDir[dir], 0, nil
		}
		return g.extra.indexGitlinks, 0, nil
	case strings.Join([]string{"submodule", "status"}, "\x00"):
		return g.extra.submoduleStatus, 0, nil
	default:
		g.t.Fatalf("unexpected git in %s: %q", dir, args)
		return "", -1, errors.New("unexpected git")
	}
}

type scriptPubs struct {
	key      ed25519.PrivateKey
	allow    map[string]bool
	mismatch bool
	when     string
}

func (s scriptPubs) Query(_ context.Context, query PublicationQuery) (*Signed[PublicationPayload], error) {
	if !s.allow[query.Repository+"\n"+query.Ref.Name] {
		return nil, nil
	}
	commit, object := query.Ref.Commit, query.Ref.Object
	if s.mismatch {
		commit, object = strings.Repeat("b", 40), strings.Repeat("b", 40)
	}
	remoteRef := query.Ref.Name
	if !strings.HasPrefix(remoteRef, "refs/") {
		remoteRef = "refs/heads/preserved"
	}
	payload := PublicationPayload{Version: Version, Binding: query.Binding, Repository: query.Repository, LocalRef: query.Ref.Name, Commit: commit, Object: object, Remote: query.Repository, RemoteRef: remoteRef, RemoteCommit: commit, RemoteObject: object, Method: "authenticated_remote_query", VerifiedAt: s.when}
	message, err := SigningBytes("publication", payload)
	if err != nil {
		return nil, err
	}
	return &Signed[PublicationPayload]{Payload: payload, Signature: Signature{KeyID: "publication", Value: base64.StdEncoding.EncodeToString(ed25519.Sign(s.key, message))}}, nil
}

func validCollectRequest(f fixture, path string) CollectRequest {
	return CollectRequest{
		Binding:      f.record.Binding,
		Repositories: []RepositorySpec{{Identity: "github:example/project", Path: path}},
		Requirements: Requirements{Source: "owner-decision:synthetic", Revision: "fixture-v1", Handoff: "none", Result: "none", History: "none", Recovery: "none"},
		Artifacts:    []ArtifactInput{},
	}
}

func syntheticGitEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Synthetic",
		"GIT_AUTHOR_EMAIL=synthetic@example.invalid",
		"GIT_COMMITTER_NAME=Synthetic",
		"GIT_COMMITTER_EMAIL=synthetic@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
}

func initSyntheticRepo(t *testing.T, home, path string) {
	t.Helper()
	runGit(t, home, "-c", "init.defaultBranch=main", "init", path)
	runGit(t, home, "-C", path, "config", "user.email", "synthetic@example.invalid")
	runGit(t, home, "-C", path, "config", "user.name", "Synthetic")
}

func embedSyntheticGitDir(t *testing.T, home, worktree, savedGitFile string) {
	t.Helper()
	runGit(t, home, "-C", worktree, "config", "--unset", "core.worktree")
	gitFile := filepath.Join(worktree, ".git")
	gitDir := strings.TrimSpace(runGitOutput(t, home, "-C", worktree, "rev-parse", "--absolute-git-dir"))
	if err := os.Rename(gitFile, savedGitFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(gitDir, gitFile); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, home string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, home, args...)
}

func newCollectorGit(t *testing.T) *ExecGit {
	t.Helper()
	runner, err := NewExecGit()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	})
	return runner
}

func prepareCleanFilter(t *testing.T, root string) (home, script, sentinel string) {
	t.Helper()
	home = filepath.Join(root, "git-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel = filepath.Join(root, "clean-filter-sentinel")
	script = filepath.Join(root, "clean-filter.sh")
	if strings.ContainsAny(sentinel, "'\\\n") || strings.ContainsAny(script, "'\\\n") {
		t.Fatal("filter path is not a safe shell argument")
	}
	writeFile(t, script, "#!/bin/sh\ntouch '"+sentinel+"'\ncat\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, script, sentinel
}

func addSyntheticGitlink(t *testing.T, home, repo, path, sha string) {
	t.Helper()
	runGit(t, home, "-C", repo, "update-index", "--add", "--cacheinfo", "160000,"+sha+","+path)
}

func armCleanFilter(t *testing.T, home, repo, script, tracked string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, ".gitattributes"), "* filter=attack\n")
	runGit(t, home, "-C", repo, "config", "filter.attack.clean", script)
	original, err := os.ReadFile(filepath.Join(repo, tracked))
	if err != nil {
		t.Fatal(err)
	}
	// Equal size forces Git to compare content and exercise the clean filter.
	changed := strings.ToUpper(string(original))
	if changed == string(original) {
		t.Fatal("fixture needs lowercase tracked content")
	}
	writeFile(t, filepath.Join(repo, tracked), changed)
}

func runDefaultStatus(t *testing.T, home, dir string) int {
	t.Helper()
	// Historical collector status, outside ExecGit, with no --ignore-submodules.
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	cmd.Env = syntheticGitEnv(home)
	_, err := cmd.CombinedOutput()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatal(err)
	return -1
}

func assertFilterDidNotRun(t *testing.T, sentinel string) {
	t.Helper()
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("collector executed clean filter: %v", err)
	}
}

func assertDefaultStatusRunsFilter(t *testing.T, home, dir, sentinel string) {
	t.Helper()
	code := runDefaultStatus(t, home, dir)
	if _, err := os.Lstat(sentinel); err != nil {
		t.Fatalf("premise failed: default git status did not run the clean filter (status exit %d): %v", code, err)
	}
}

func runGitOutput(t *testing.T, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = syntheticGitEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s\n%s\n%v", strings.Join(args, " "), out, err)
	}
	return string(out)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func passiveFingerprint(t *testing.T, home, dir string) string {
	t.Helper()
	hash := sha256.New()
	output := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "--no-optional-locks"}, args...)...)
		cmd.Env = syntheticGitEnv(home)
		out, _ := cmd.CombinedOutput()
		_, _ = hash.Write(out)
		_, _ = hash.Write([]byte{0})
		return string(out)
	}
	output("for-each-ref", "--format=%(objectname) %(objecttype) %(*objectname) %(refname)")
	output("stash", "list")
	gitDir := strings.TrimSpace(output("rev-parse", "--absolute-git-dir"))
	common := strings.TrimSpace(output("rev-parse", "--git-common-dir"))
	if common != "" && !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	for _, path := range []string{
		filepath.Join(gitDir, "HEAD"), filepath.Join(gitDir, "index"), filepath.Join(gitDir, "config"),
		filepath.Join(common, "config"), filepath.Join(common, "packed-refs"), filepath.Join(common, "HEAD"),
		filepath.Join(common, "logs", "HEAD"), filepath.Join(common, "logs", "refs", "stash"),
	} {
		file, err := os.Open(path)
		if err != nil {
			_, _ = io.WriteString(hash, "missing "+path+" "+err.Error())
			continue
		}
		_, _ = io.WriteString(hash, "file "+path+"\n")
		_, _ = io.Copy(hash, file)
		_ = file.Close()
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func findWorktree(repo Repository, path string) (Worktree, bool) {
	base := filepath.Base(path)
	for _, worktree := range repo.Worktrees {
		if worktree.Path == path || filepath.Base(worktree.Path) == base {
			return worktree, true
		}
	}
	return Worktree{}, false
}

func worktreePaths(repo Repository) []string {
	paths := make([]string, 0, len(repo.Worktrees))
	for _, worktree := range repo.Worktrees {
		paths = append(paths, worktree.Path)
	}
	return paths
}

func sawGit(g *scriptGit, dir string, match func([]string) bool) bool {
	for i, call := range g.calls {
		if (dir == "" || g.dirs[i] == dir) && match(call) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestStagedGitlinkComparisonFailsClosed(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	index := func(mode, oid, stage string) string { return mode + " " + oid + " " + stage + "\tsub\x00" }
	tree := func(mode, kind, oid string) string { return mode + " " + kind + " " + oid + "\tsub\x00" }
	for _, tc := range []struct{ name, index, head string }{
		{"modified", index("160000", b, "0"), tree("160000", "commit", a)},
		{"added", index("160000", a, "0"), ""},
		{"deleted", "", tree("160000", "commit", a)},
		{"gitlink to file", index("100644", a, "0"), tree("160000", "commit", a)},
		{"file to gitlink", index("160000", a, "0"), tree("100644", "blob", a)},
		{"conflict", index("160000", a, "1"), tree("160000", "commit", a)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := rejectStagedGitlinks(tc.index, tc.head); err == nil {
				t.Fatal("staged gitlink was clean")
			}
		})
	}
	if err := rejectStagedGitlinks(index("160000", a, "0"), tree("160000", "commit", a)); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticStagedGitlinksAreNotClean(t *testing.T) {
	for _, nested := range []bool{false, true} {
		level := "parent"
		if nested {
			level = "nested child"
		}
		for _, change := range []string{"modified", "added", "deleted", "mode", "conflict"} {
			t.Run(level+"/"+change, func(t *testing.T) {
				f := newFixture(t)
				root := t.TempDir()
				home := filepath.Join(root, "home")
				if err := os.Mkdir(home, 0o700); err != nil {
					t.Fatal(err)
				}
				work := filepath.Join(root, "work")
				initSyntheticRepo(t, home, work)
				writeFile(t, filepath.Join(work, "README"), "root\n")
				runGit(t, home, "-C", work, "add", "README")
				runGit(t, home, "-C", work, "commit", "-m", "root")
				repo := work
				req := validCollectRequest(f, work)
				req.SubmoduleIdentities = map[string]string{}
				if nested {
					repo = filepath.Join(work, "outer")
					initSyntheticRepo(t, home, repo)
					writeFile(t, filepath.Join(repo, "README"), "outer\n")
					runGit(t, home, "-C", repo, "add", "README")
					runGit(t, home, "-C", repo, "commit", "-m", "outer")
					writeFile(t, filepath.Join(work, ".gitmodules"), "[submodule \"outer\"]\n path = outer\n url = ../outer\n")
					req.SubmoduleIdentities[SubmoduleIdentityKey("github:example/project", "outer")] = "synthetic:outer"
				}
				child := filepath.Join(repo, "sub")
				initSyntheticRepo(t, home, child)
				writeFile(t, filepath.Join(child, "lib"), "first\n")
				runGit(t, home, "-C", child, "add", "lib")
				runGit(t, home, "-C", child, "commit", "-m", "child")
				sha := strings.TrimSpace(runGitOutput(t, home, "-C", child, "rev-parse", "HEAD"))
				writeFile(t, filepath.Join(repo, ".gitmodules"), "[submodule \"sub\"]\n path = sub\n url = ../sub\n[submodule \"added\"]\n path = added\n url = ../added\n")
				runGit(t, home, "-C", repo, "add", ".gitmodules")
				addSyntheticGitlink(t, home, repo, "sub", sha)
				runGit(t, home, "-C", repo, "commit", "-m", "mapped child")
				identity := "github:example/project"
				if nested {
					identity = "synthetic:outer"
					outerSHA := strings.TrimSpace(runGitOutput(t, home, "-C", repo, "rev-parse", "HEAD"))
					runGit(t, home, "-C", work, "add", ".gitmodules")
					addSyntheticGitlink(t, home, work, "outer", outerSHA)
					runGit(t, home, "-C", work, "commit", "-m", "outer child")
				}
				req.SubmoduleIdentities[SubmoduleIdentityKey(identity, "sub")] = "synthetic:sub"
				runner := newCollectorGit(t)
				if inventory, err := CollectPreservation(context.Background(), req, runner, fixedRevision(req.Binding.HomeRevision)); err != nil || !inventory.Complete {
					t.Fatalf("clean baseline: %+v %v", inventory, err)
				}
				switch change {
				case "modified":
					writeFile(t, filepath.Join(child, "lib"), "second\n")
					runGit(t, home, "-C", child, "add", "lib")
					runGit(t, home, "-C", child, "commit", "-m", "next child")
					runGit(t, home, "-C", repo, "add", "sub")
				case "added":
					addSyntheticGitlink(t, home, repo, "added", sha)
				case "deleted":
					runGit(t, home, "-C", repo, "update-index", "--force-remove", "sub")
				case "mode":
					blob := strings.TrimSpace(runGitOutput(t, home, "-C", repo, "rev-parse", "HEAD:README"))
					runGit(t, home, "-C", repo, "update-index", "--cacheinfo", "100644,"+blob+",sub")
				case "conflict":
					cmd := exec.Command("git", "-C", repo, "update-index", "--index-info")
					cmd.Env = syntheticGitEnv(home)
					cmd.Stdin = strings.NewReader("0 " + strings.Repeat("0", 40) + "\tsub\n160000 " + sha + " 1\tsub\n160000 " + sha + " 2\tsub\n160000 " + sha + " 3\tsub\n")
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("fixture conflict: %s %v", out, err)
					}
				}
				inventory, err := CollectPreservation(context.Background(), req, runner, fixedRevision(req.Binding.HomeRevision))
				if err == nil || inventory.Complete || !(strings.Contains(err.Error(), "staged submodule changes") || strings.Contains(err.Error(), "unmerged index")) {
					t.Fatalf("staged gitlink was clean: %+v %v", inventory, err)
				}
			})
		}
	}
}
