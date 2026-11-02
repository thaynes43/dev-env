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
		if call[0] == "submodule" {
			t.Fatal("submodule status ran")
		}
	}
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
	localConfig     string
	modulesList     string
	submoduleStatus string
	indexFlags      string
	indexFlagDir    string
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
	case strings.Join([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}, "\x00"):
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
	case strings.Join([]string{"submodule", "status", "--recursive"}, "\x00"):
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

func runGit(t *testing.T, home string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, home, args...)
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
