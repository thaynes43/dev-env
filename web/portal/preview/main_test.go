package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingFoundationFixtureRendersOffline(t *testing.T) {
	f, err := os.Open("../../../scripts/trials/portal_attention/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	body, err := render(f)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, title := range []string{"Needs you · 1", "Needs worker · 1", "Review returned · 1", "Handoff missing · 1", "Source preview", "Stale evidence", "Freshness unknown", "2026-10-23T14:18:10Z"} {
		if !strings.Contains(s, title) {
			t.Errorf("missing fixture presentation %s", title)
		}
	}
	if strings.Contains(s, "Listing counts") {
		t.Fatal("unexpected listing warning in verified synthetic snapshot")
	}
	if strings.Count(s, "<article ") != 4 || strings.Count(s, "href=") != 4 {
		t.Fatal("fixture rows or fixed source links missing")
	}
	for _, active := range []string{"<script", "<iframe", "<form", "<img"} {
		if strings.Contains(s, active) {
			t.Fatalf("unexpected active content %s", active)
		}
	}
	// A second independent render from the same accepted fixture must match.
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	again, err := render(f)
	if err != nil || string(again) != s {
		t.Fatal("preview is not repeatable")
	}
}

func TestInvalidFixtureWritesNoPartialPage(t *testing.T) {
	for _, raw := range []string{"", "{", `{"version":1,"asOf":"2026-10-23T16:00:00Z","readState":"verified","items":[],"prompt":"SECRET"}`, `{"version":2}`, strings.Repeat("x", 65537)} {
		body, err := render(strings.NewReader(raw))
		if err == nil || len(body) != 0 {
			t.Fatal("invalid fixture returned partial HTML")
		}
	}
}

func TestOfflineScenariosKeepHonestStates(t *testing.T) {
	root, head := cleanSourceFixture(t)
	useSourceFixture(t, root)
	dir := filepath.Join(t.TempDir(), "scenarios")
	if err := writeScenarios(dir); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	empty := read("verified-empty.html")
	if !strings.Contains(empty, "Verified empty") || !strings.Contains(empty, ">Needs you · 0</h2>") || !strings.Contains(empty, "No items in this group.") {
		t.Fatal("verified empty groups")
	}
	unknown := read("unknown-listing.html")
	if !strings.Contains(unknown, "Attention unknown") || strings.Contains(unknown, "· 0") || strings.Contains(unknown, "Needs you ·") {
		t.Fatal("unknown listing invented a count")
	}
	stale := read("stale-with-fresh-row.html")
	if !strings.Contains(stale, "Stale evidence. Listing counts are not verified as current;") || !strings.Contains(stale, `<p class="freshness">Verified within snapshot window</p>`) || strings.Contains(stale, "· 0") {
		t.Fatal("fresh row inside stale listing")
	}
	missing := read("fresh-row-unknown-listing.html")
	if !strings.Contains(missing, "Freshness unknown. Listing counts are not verified as current;") || !strings.Contains(missing, `<p class="freshness">Verified within snapshot window</p>`) || strings.Contains(missing, "· 0") {
		t.Fatal("fresh row inside unknown listing")
	}
	future := read("future-listing.html")
	if !strings.Contains(future, "Unknown (untrusted future timestamp:") || strings.Contains(future, "· 0") {
		t.Fatal("future listing")
	}
	hostile := read("hostile.html")
	if strings.Contains(hostile, "<script>") || strings.Contains(hostile, "<img ") || !strings.Contains(hostile, "&lt;script&gt;") || !strings.Contains(hostile, "<dt>Owner</dt><dd>Unknown</dd>") {
		t.Fatal("hostile text")
	}
	absent := read("no-owner.html")
	if !strings.Contains(absent, "<dt>Owner</dt><dd>Unknown</dd>") || !strings.Contains(absent, `<dt>Next action</dt><dd class="next">Unknown</dd>`) || !strings.Contains(absent, "<dt>Next actor</dt><dd>Unknown</dd>") {
		t.Fatal("missing actor")
	}
	const refused = "Attention unavailable. Current task state is Unknown.\n"
	for _, name := range []string{"denied.txt", "unavailable.txt"} {
		if read(name) != refused {
			t.Fatalf("%s body", name)
		}
	}
	populated := read("populated.html")
	if strings.Count(populated, "<article ") != 4 || strings.Contains(populated, "<script") || strings.Contains(populated, "Listing counts") {
		t.Fatal("populated preview")
	}
	manifest := read("manifest.json")
	sum := sha256.Sum256([]byte(populated))
	if !strings.Contains(manifest, `"sourceHead": "`+head+`"`) || !strings.Contains(manifest, hex.EncodeToString(sum[:])) {
		t.Fatal("scenario manifest does not bind the source head and populated file")
	}
	if _, err := os.Lstat(dir + ".previous"); !os.IsNotExist(err) {
		t.Fatal("fresh scenario publish created a backup")
	}
}

func TestPublishFailureLeavesUnrelatedTreeByteIdentical(t *testing.T) {
	parent := t.TempDir()
	final := filepath.Join(parent, "scenarios")
	mustWrite(t, filepath.Join(final, "notes.md"), []byte("keep-notes"))
	mustWrite(t, filepath.Join(final, "unrelated-user-dir", "important.txt"), []byte("keep-important"))
	before := treeSnapshot(t, final)
	staging := stagingDir(t, parent)
	err := publishScenarioDir(staging, final)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace existing evidence") {
		t.Fatalf("publish err %v", err)
	}
	sameTree(t, before, treeSnapshot(t, final))
	if _, statErr := os.Lstat(final + ".previous"); !os.IsNotExist(statErr) {
		t.Fatal("refusal created a previous backup")
	}
	assertStagingRemains(t, staging)
}

func TestPublishFailureLeavesPreviousBackupByteIdentical(t *testing.T) {
	parent := t.TempDir()
	final := filepath.Join(parent, "scenarios")
	backup := final + ".previous"
	mustWrite(t, filepath.Join(backup, "old.txt"), []byte("keep-backup"))
	before := treeSnapshot(t, backup)
	staging := stagingDir(t, parent)
	err := publishScenarioDir(staging, final)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace existing evidence") {
		t.Fatalf("publish err %v", err)
	}
	if _, statErr := os.Lstat(final); !os.IsNotExist(statErr) {
		t.Fatal("refusal created a destination and lost the backup path")
	}
	sameTree(t, before, treeSnapshot(t, backup))
	assertStagingRemains(t, staging)
}

func TestPublishFailureLeavesExistingScenarioArtifacts(t *testing.T) {
	parent := t.TempDir()
	final := filepath.Join(parent, "scenarios")
	mustWrite(t, filepath.Join(final, "manifest.json"), []byte("{\"sourceHead\":\"0123456789abcdef0123456789abcdef01234567\"}\n"))
	mustWrite(t, filepath.Join(final, "populated.html"), []byte("previous page"))
	mustWrite(t, filepath.Join(final+".previous", "populated.html"), []byte("older page"))
	beforeFinal := treeSnapshot(t, final)
	beforeBackup := treeSnapshot(t, final+".previous")
	err := publishScenarioDir(stagingDir(t, parent), final)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace existing evidence") {
		t.Fatalf("publish err %v", err)
	}
	sameTree(t, beforeFinal, treeSnapshot(t, final))
	sameTree(t, beforeBackup, treeSnapshot(t, final+".previous"))
}

func TestPublishFailureLeavesEmptyDirectoryAndFileAndSymlink(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		parent := t.TempDir()
		final := filepath.Join(parent, "scenarios")
		if err := os.Mkdir(final, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := publishScenarioDir(stagingDir(t, parent), final); err == nil {
			t.Fatal("empty destination was replaced")
		}
		entries, err := os.ReadDir(final)
		if err != nil || len(entries) != 0 {
			t.Fatalf("empty destination changed: %v %v", entries, err)
		}
	})
	t.Run("regular file", func(t *testing.T) {
		parent := t.TempDir()
		final := filepath.Join(parent, "scenarios")
		body := []byte("not-a-directory")
		mustWrite(t, final, body)
		if err := publishScenarioDir(stagingDir(t, parent), final); err == nil {
			t.Fatal("file destination was replaced")
		}
		got, err := os.ReadFile(final)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal("file destination bytes changed")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		parent := t.TempDir()
		real := filepath.Join(parent, "real")
		mustWrite(t, filepath.Join(real, "important.txt"), []byte("keep-through-symlink"))
		link := filepath.Join(parent, "scenarios")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if err := publishScenarioDir(stagingDir(t, parent), link); err == nil {
			t.Fatal("symlink destination was replaced")
		}
		gotLink, err := os.Readlink(link)
		if err != nil || gotLink != real {
			t.Fatalf("symlink changed to %q (%v)", gotLink, err)
		}
		got, err := os.ReadFile(filepath.Join(real, "important.txt"))
		if err != nil || string(got) != "keep-through-symlink" {
			t.Fatal("symlink target was deleted or changed")
		}
	})
}

func TestPublishFreshDestinationCreatesNoBackup(t *testing.T) {
	parent := t.TempDir()
	final := filepath.Join(parent, "scenarios")
	staging := stagingDir(t, parent)
	if err := publishScenarioDir(staging, final); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(final, "populated.html"))
	if err != nil || string(got) != "replacement" {
		t.Fatal("fresh destination missing the staged page")
	}
	if _, err = os.Lstat(staging); !os.IsNotExist(err) {
		t.Fatal("staging remained after a successful rename")
	}
	if _, err = os.Lstat(final + ".previous"); !os.IsNotExist(err) {
		t.Fatal("fresh publish created a backup")
	}
}

func TestWriteScenariosPreservesUnrelatedTree(t *testing.T) {
	root, _ := cleanSourceFixture(t)
	useSourceFixture(t, root)
	parent := t.TempDir()
	final := filepath.Join(parent, "scenarios")
	mustWrite(t, filepath.Join(final, "notes.md"), []byte("keep-notes"))
	mustWrite(t, filepath.Join(final, "unrelated-user-dir", "important.txt"), []byte("keep-important"))
	mustWrite(t, filepath.Join(final+".previous", "old.txt"), []byte("keep-backup"))
	before := treeSnapshot(t, parent)
	err := writeScenarios(final)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace existing evidence") {
		t.Fatalf("writeScenarios err %v", err)
	}
	sameTree(t, before, treeSnapshot(t, parent))
}

func TestSourceHeadRefusesDirtyRelevantTree(t *testing.T) {
	t.Run("modified", func(t *testing.T) {
		root, head := cleanSourceFixture(t)
		mustWrite(t, filepath.Join(root, "web/portal/attention.html"), []byte("dirty"))
		assertDirtyHead(t, root, head)
	})
	t.Run("untracked", func(t *testing.T) {
		root, head := cleanSourceFixture(t)
		mustWrite(t, filepath.Join(root, "scripts/trials/portal_attention/untracked.cjs"), []byte("dirty"))
		assertDirtyHead(t, root, head)
	})
	t.Run("staged", func(t *testing.T) {
		root, head := cleanSourceFixture(t)
		mustWrite(t, filepath.Join(root, "web/portal/attention.html"), []byte("staged"))
		runGit(t, root, "add", "web/portal/attention.html")
		assertDirtyHead(t, root, head)
	})
}

func TestSourceHeadIgnoresUnrelatedDirtyFile(t *testing.T) {
	root, head := cleanSourceFixture(t)
	mustWrite(t, filepath.Join(root, "notes.md"), []byte("other work"))
	got, err := inspectSourceHead(root)
	if err != nil || got != head {
		t.Fatalf("unrelated dirt returned %q err %v", got, err)
	}
}

func TestWriteManifestOmitsCleanHeadWhenDirty(t *testing.T) {
	root, head := cleanSourceFixture(t)
	mustWrite(t, filepath.Join(root, "web/portal/attention.html"), []byte("dirty"))
	useSourceFixture(t, root)
	dir := t.TempDir()
	page := []byte("page")
	mustWrite(t, filepath.Join(dir, "populated.html"), page)
	err := writeManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty manifest err %v", err)
	}
	if strings.Contains(err.Error(), head) {
		t.Fatal("dirty refusal presented the clean head")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(statErr) {
		t.Fatal("dirty source wrote manifest.json")
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "populated.html"))
	if readErr != nil || !bytes.Equal(got, page) {
		t.Fatal("dirty refusal changed the scenario file")
	}
}

func TestWriteManifestRecordsExactCleanHead(t *testing.T) {
	root, head := cleanSourceFixture(t)
	useSourceFixture(t, root)
	dir := t.TempDir()
	page := []byte("page")
	mustWrite(t, filepath.Join(dir, "populated.html"), page)
	if err := writeManifest(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(page)
	if !strings.Contains(string(raw), `"sourceHead": "`+head+`"`) || !strings.Contains(string(raw), hex.EncodeToString(sum[:])) {
		t.Fatalf("manifest %s", raw)
	}
}

func assertDirtyHead(t *testing.T, root, head string) {
	t.Helper()
	got, err := inspectSourceHead(root)
	if err == nil || got != "" || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("dirty tree returned head %q err %v", got, err)
	}
	if strings.Contains(err.Error(), head) {
		t.Fatal("dirty error presented the clean head")
	}
}

func useSourceFixture(t *testing.T, root string) {
	t.Helper()
	previous := resolveSourceHead
	resolveSourceHead = func() (string, error) { return inspectSourceHead(root) }
	t.Cleanup(func() { resolveSourceHead = previous })
}

func cleanSourceFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	mustWrite(t, filepath.Join(root, "web/portal/attention.html"), []byte("clean"))
	mustWrite(t, filepath.Join(root, "scripts/trials/portal_attention/browser_check.cjs"), []byte("clean"))
	runGit(t, root, "add", "web/portal", "scripts/trials/portal_attention")
	runGit(t, root, "commit", "-m", "fixture")
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	got, err := inspectSourceHead(root)
	if err != nil || got != head {
		t.Fatalf("clean fixture head %q err %v", got, err)
	}
	return root, head
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := gitCommand(append([]string{"-C", root, "-c", "user.email=portal-test@example.com", "-c", "user.name=portal-test", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func stagingDir(t *testing.T, parent string) string {
	t.Helper()
	staging := filepath.Join(parent, "staging")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(staging, "populated.html"), []byte("replacement"))
	return staging
}

func mustWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertStagingRemains(t *testing.T, staging string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(staging, "populated.html"))
	if err != nil || string(got) != "replacement" {
		t.Fatal("failed publish consumed staging or lost the existing target")
	}
}

func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = "symlink:" + target
			return nil
		}
		if info.IsDir() {
			out[rel] = "dir"
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = "file:" + string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("tree entries %d to %d", len(before), len(after))
	}
	for name, body := range before {
		if after[name] != body {
			t.Fatalf("path %s changed", name)
		}
	}
}
