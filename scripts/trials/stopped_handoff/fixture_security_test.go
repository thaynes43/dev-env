package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSymlinkCannotEscapeFixture(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(outside, "new.txt")
	if err := os.WriteFile(canary, []byte("canary-original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := hashFile(t, canary)
	src := filepath.Join(base, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(src, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "d")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "committed.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(src, "add", "--", "d", "committed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(src, "commit", "-m", "symlink"); err != nil {
		t.Fatal(err)
	}
	head, err := gitCmd(src, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), fixtureReport{Head: head, Manifest: manifest{
		Algorithm: "sha256",
		Entries: []manifestEntry{
			{Path: "committed.txt", GitState: "committed", SHA256: hashFile(t, filepath.Join(src, "committed.txt"))},
			{Path: "d/new.txt", GitState: "untracked", SHA256: before},
		},
	}})
	into := filepath.Join(base, "into")
	if err := os.Mkdir(into, 0o755); err != nil {
		t.Fatal(err)
	}
	code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", src, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if code == 0 {
		t.Fatal("symlink restore succeeded")
	}
	if hashFile(t, canary) != before || string(mustRead(t, canary)) != "canary-original\n" {
		t.Fatal("outside canary changed")
	}
	if n := dirCount(t, into); n != 0 {
		t.Fatalf("recipient was written: %d entries", n)
	}
}

func TestIntoAliasCannotEnterGitDir(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	alias := filepath.Join(t.TempDir(), "into-alias")
	if err := os.Symlink(filepath.Join(pred, ".git", "refs", "tags"), alias); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", alias}, nil, &buf, &bytes.Buffer{})
	if code == 0 {
		t.Fatalf("alias restore succeeded %s", buf.String())
	}
	entries, err := os.ReadDir(filepath.Join(pred, ".git", "refs", "tags"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("git dir was written: %d", len(entries))
	}
}

func TestFixtureHeadMustMatchBothRecordHeads(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	doc.Predecessor.Head = "abababababababababababababababababababab"
	doc.Observed.Head = doc.Predecessor.Head
	record := writeDoc(t, doc)
	var checkBuf bytes.Buffer
	if code := run([]string{"check", "--verify-root", pred, record}, nil, &checkBuf, &bytes.Buffer{}); code != 0 {
		t.Fatalf("check exit %d %s", code, checkBuf.String())
	}
	var res result
	if err := json.Unmarshal(checkBuf.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.FixtureVerified || res.RestoreAuthorized || res.SourceHead != doc.Predecessor.Head {
		t.Fatalf("check bound the wrong head %#v", res)
	}
	into := t.TempDir()
	code := run([]string{"restore", "--record", record, "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if code == 0 {
		t.Fatal("wrong-head restore succeeded")
	}
	entries, err := os.ReadDir(into)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("wrong-head restore wrote the recipient")
	}
}

func TestMixedIndexStateRefusedBeforeWrite(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(pred, "add", "--", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pred, "tracked.txt"), []byte("synthetic third line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexBefore := hashFile(t, filepath.Join(pred, ".git", "index"))
	blobBefore, err := gitCmd(pred, "rev-parse", ":tracked.txt")
	if err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	doc.Manifest.Entries[1].SHA256 = hashFile(t, filepath.Join(pred, "tracked.txt"))
	digest, err := wipDigest(doc.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	doc.Observed.WIPHash = digest
	into := t.TempDir()
	code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 2 {
		t.Fatalf("MM restore exit %d", code)
	}
	entries, err := os.ReadDir(into)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("MM restore wrote the recipient")
	}
	if hashFile(t, filepath.Join(pred, ".git", "index")) != indexBefore {
		t.Fatal("MM refusal changed the predecessor index")
	}
	blobAfter, err := gitCmd(pred, "rev-parse", ":tracked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if blobAfter != blobBefore {
		t.Fatalf("index blob changed %s %s", blobBefore, blobAfter)
	}
}

func TestUntrackedModeIsPreserved(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(pred, "untracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	into := t.TempDir()
	var buf bytes.Buffer
	code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &buf, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("mode restore %d %s", code, buf.String())
	}
	info, err := os.Lstat(filepath.Join(into, "untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("recipient mode %o", info.Mode().Perm())
	}
}

func TestFsmonitorAndIndexStayUntouched(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(t.TempDir(), "fsmonitor-canary")
	hook := filepath.Join(t.TempDir(), "hook")
	script := "#!/bin/sh\necho ran > " + canary + "\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(pred, "config", "core.fsmonitor", hook); err != nil {
		t.Fatal(err)
	}
	manPath := filepath.Join(t.TempDir(), "manifest.json")
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", "--root", pred, "--manifest", manPath}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("fsmonitor config was accepted")
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatal("fsmonitor hook ran")
	}

	clean := t.TempDir()
	cleanReport, err := materialize(clean)
	if err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), cleanReport)
	indexBefore := hashFile(t, filepath.Join(clean, ".git", "index"))
	if code := run([]string{"verify", "--root", clean, "--manifest", writeManifest(t, cleanReport)}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("clean verify failed")
	}
	if hashFile(t, filepath.Join(clean, ".git", "index")) != indexBefore {
		t.Fatal("verify rewrote the index")
	}
	into := t.TempDir()
	if code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", clean, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("clean restore failed")
	}
	if hashFile(t, filepath.Join(clean, ".git", "index")) != indexBefore {
		t.Fatal("restore rewrote the predecessor index")
	}
}

func TestStdinDashIsRefused(t *testing.T) {
	dir := t.TempDir()
	var errBuf bytes.Buffer
	code := run([]string{"restore", "--record", "-", "--from", dir, "--into", dir}, nil, &bytes.Buffer{}, &errBuf)
	if code != 2 || bytes.Contains(errBuf.Bytes(), []byte("panic")) {
		t.Fatalf("restore dash %d %s", code, errBuf.String())
	}
	errBuf.Reset()
	code = run([]string{"verify", "--root", dir, "--manifest", "-"}, nil, &bytes.Buffer{}, &errBuf)
	if code != 2 || bytes.Contains(errBuf.Bytes(), []byte("panic")) {
		t.Fatalf("verify dash %d %s", code, errBuf.String())
	}
}

func writeManifest(t *testing.T, report fixtureReport) string {
	t.Helper()
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hexEncode(sum[:])
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0x0f]
	}
	return string(out)
}

func TestInheritedGitEnvIsNotUsed(t *testing.T) {
	canary := t.TempDir()
	if _, err := gitCmd(canary, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canary, "c"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(canary, "add", "--", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(canary, "commit", "-m", "c"); err != nil {
		t.Fatal(err)
	}
	before := hashFile(t, filepath.Join(canary, ".git", "index"))
	t.Setenv("GIT_DIR", filepath.Join(canary, ".git"))
	t.Setenv("GIT_WORK_TREE", canary)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(canary, ".git", "index"))
	pred := t.TempDir()
	if _, err := materialize(pred); err != nil {
		t.Fatal(err)
	}
	if hashFile(t, filepath.Join(canary, ".git", "index")) != before {
		t.Fatal("inherited Git environment changed the canary")
	}
}

func TestUnsupportedModesLeaveRecipientEmpty(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(pred, "untracked.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	index := hashFile(t, filepath.Join(pred, ".git", "index"))
	into := t.TempDir()
	code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if code == 0 || dirCount(t, into) != 0 {
		t.Fatalf("0600 restore code %d recipient %d", code, dirCount(t, into))
	}
	if hashFile(t, filepath.Join(pred, ".git", "index")) != index {
		t.Fatal("0600 refusal changed the predecessor index")
	}

	pred = t.TempDir()
	report, err = materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(pred, "tracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc = withFixture(t, loadDoc(t), report)
	index = hashFile(t, filepath.Join(pred, ".git", "index"))
	tracked := hashFile(t, filepath.Join(pred, "tracked.txt"))
	into = t.TempDir()
	code = run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if code == 0 || dirCount(t, into) != 0 {
		t.Fatalf("exec-bit restore code %d recipient %d", code, dirCount(t, into))
	}
	if hashFile(t, filepath.Join(pred, ".git", "index")) != index || hashFile(t, filepath.Join(pred, "tracked.txt")) != tracked {
		t.Fatal("exec-bit refusal changed predecessor bytes or index")
	}
}

func TestCommittedEntriesMustBeHeadBlobs(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pred, ".git", "info", "exclude"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pred, "ignored.txt"), []byte("never committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	doc.Manifest.Entries = append(doc.Manifest.Entries, manifestEntry{
		Path: "ignored.txt", GitState: "committed", SHA256: hashFile(t, filepath.Join(pred, "ignored.txt")),
	})
	var checkBuf bytes.Buffer
	if code := run([]string{"check", "--verify-root", pred, writeDoc(t, doc)}, nil, &checkBuf, &bytes.Buffer{}); code != 0 {
		t.Fatalf("ignored check exit %d %s", code, checkBuf.String())
	}
	var res result
	if err := json.Unmarshal(checkBuf.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.FixtureVerified || res.RestoreAuthorized {
		t.Fatalf("ignored file verified %#v", res)
	}
	index := hashFile(t, filepath.Join(pred, ".git", "index"))
	into := t.TempDir()
	if code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 || dirCount(t, into) != 0 {
		t.Fatal("ignored-as-committed wrote the recipient")
	}
	if hashFile(t, filepath.Join(pred, ".git", "index")) != index {
		t.Fatal("ignored refusal changed the index")
	}

	alias := withFixture(t, loadDoc(t), report)
	alias.Manifest.Entries = append(alias.Manifest.Entries, manifestEntry{
		Path: "./committed.txt", GitState: "committed", SHA256: alias.Manifest.Entries[0].SHA256,
	})
	body, err := json.Marshal(alias)
	if err != nil {
		t.Fatal(err)
	}
	aliasRes, code := check(body)
	if code != 2 || aliasRes.RestoreAuthorized || !strings.Contains(aliasRes.Refusals[0], "not canonical") {
		t.Fatalf("alias code %d %#v", code, aliasRes.Refusals)
	}
	meta := loadDoc(t)
	meta.Manifest.Entries = append(meta.Manifest.Entries, manifestEntry{
		Path: ".git/description", GitState: "committed", SHA256: strings.Repeat("ab", 32),
	})
	body, err = json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	metaRes, code := check(body)
	if code != 2 || metaRes.RestoreAuthorized || !strings.Contains(metaRes.Refusals[0], "git metadata") {
		t.Fatalf("metadata code %d %#v", code, metaRes.Refusals)
	}
}

func TestManifestParserRejectsFoldedKeys(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	good := report.Manifest.Entries[0].SHA256
	bad := strings.Repeat("0", 64)
	body := `{"algorithm":"sha256","entries":[{"path":"committed.txt","gitState":"committed","sha256":"` + bad + `","SHA256":"` + good + `"}`
	for _, entry := range report.Manifest.Entries[1:] {
		body += `,{"path":"` + entry.Path + `","gitState":"` + entry.GitState + `","sha256":"` + entry.SHA256 + `"}`
	}
	body += `]}`
	path := filepath.Join(t.TempDir(), "folded.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", "--root", pred, "--manifest", path}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("case-variant manifest key was accepted")
	}
	folded := strings.Replace(body, `"sha256":"`+bad+`","SHA256":"`+good+`"`, `"\u017fha256":"`+good+`"`, 1)
	foldedPath := filepath.Join(t.TempDir(), "long-s.json")
	if err := os.WriteFile(foldedPath, []byte(folded), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", "--root", pred, "--manifest", foldedPath}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("unicode manifest key was accepted")
	}
	trail := body + `{"x":1}`
	trailPath := filepath.Join(t.TempDir(), "trail.json")
	if err := os.WriteFile(trailPath, []byte(trail), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", "--root", pred, "--manifest", trailPath}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("trailing manifest JSON was accepted")
	}
}

func TestVerifyBindsReportHead(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	next, err := gitCmd(pred, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "advance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(pred, "update-ref", "HEAD", next); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", "--root", pred, "--manifest", writeManifest(t, report)}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("verify accepted a moved HEAD")
	}
}

func TestUmaskDoesNotChangeRestoredModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	into := t.TempDir()
	if code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("umask 077 restore failed")
	}
	for _, name := range []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt"} {
		src, err := os.Lstat(filepath.Join(pred, name))
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.Lstat(filepath.Join(into, name))
		if err != nil {
			t.Fatal(err)
		}
		if src.Mode().Perm() != 0o644 || dst.Mode().Perm() != src.Mode().Perm() {
			t.Fatalf("%s mode src %o dst %o", name, src.Mode().Perm(), dst.Mode().Perm())
		}
	}
}

func TestFsmonitorOverrideDoesNotRunHook(t *testing.T) {
	dir := t.TempDir()
	canary := filepath.Join(t.TempDir(), "fsmonitor-canary")
	hook := filepath.Join(t.TempDir(), "hook")
	script := "#!/bin/sh\necho ran > " + canary + "\nexit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "config", "core.fsmonitor", hook); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "add", "--", "f"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "commit", "-m", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "status", "--porcelain=v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatal("fsmonitor hook ran under the command override")
	}
}

func TestRecipientSymlinkCannotReceiveBytes(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(outside, []byte("canary-original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := hashFile(t, outside)
	dst := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dst, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	modes, err := fixtureModes(pred, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := overlay(pred, dst, report.Manifest, modes); err == nil {
		t.Fatal("overlay followed a recipient symlink")
	}
	if hashFile(t, outside) != before {
		t.Fatal("recipient symlink changed the outside canary")
	}
}

func TestPreservedSourceErrorKeepsTheSnapshot(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	before, err := snapshot(pred, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	err = finishSource(pred, report.Manifest, before, errors.New("post-write failure"))
	var preserved preservedSourceError
	if !errors.As(err, &preserved) || preserved.Error() != "post-write failure" {
		t.Fatalf("preserved error %#v", err)
	}
	if snapshotAfter, err := snapshot(pred, report.Manifest); err != nil || snapshotAfter != before {
		t.Fatal("snapshot changed")
	}
}

func TestIndexFlagsRefuseBeforeWrite(t *testing.T) {
	cases := []struct {
		name string
		path string
		flag string
	}{
		{"committed-skip-worktree", "committed.txt", "--skip-worktree"},
		{"committed-assume-unchanged", "committed.txt", "--assume-unchanged"},
		{"staged-skip-worktree", "staged.txt", "--skip-worktree"},
		{"staged-assume-unchanged", "staged.txt", "--assume-unchanged"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pred := t.TempDir()
			report, err := materialize(pred)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gitCmd(pred, "update-index", tc.flag, "--", tc.path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pred, tc.path), []byte("hidden divergence\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sum := hashFile(t, filepath.Join(pred, tc.path))
			for i := range report.Manifest.Entries {
				if report.Manifest.Entries[i].Path == tc.path {
					report.Manifest.Entries[i].SHA256 = sum
				}
			}
			doc := withFixture(t, loadDoc(t), report)
			index := hashFile(t, filepath.Join(pred, ".git", "index"))
			flags, err := gitCmd(pred, "ls-files", "-v", "--", tc.path)
			if err != nil {
				t.Fatal(err)
			}
			var checkBuf, errBuf bytes.Buffer
			if code := run([]string{"check", "--verify-root", pred, writeDoc(t, doc)}, nil, &checkBuf, &bytes.Buffer{}); code != 0 {
				t.Fatalf("verify-root exit %d %s", code, checkBuf.String())
			}
			var res result
			if err := json.Unmarshal(checkBuf.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if res.FixtureVerified || res.RestoreAuthorized {
				t.Fatalf("flagged divergence verified %#v", res.FixtureVerified)
			}
			into := t.TempDir()
			code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &errBuf)
			if code == 0 || dirCount(t, into) != 0 || !strings.Contains(errBuf.String(), "before any restore write") {
				t.Fatalf("restore code %d recipient %d err %s", code, dirCount(t, into), errBuf.String())
			}
			if hashFile(t, filepath.Join(pred, ".git", "index")) != index || hashFile(t, filepath.Join(pred, tc.path)) != sum {
				t.Fatal("predecessor bytes or index changed")
			}
			flagsAfter, err := gitCmd(pred, "ls-files", "-v", "--", tc.path)
			if err != nil || flagsAfter != flags {
				t.Fatalf("index flags changed %q -> %q (%v)", flags, flagsAfter, err)
			}
		})
	}
}

func TestStagedNewDirectoryRestores(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(pred, "newdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic staged nested\n")
	if err := os.WriteFile(filepath.Join(pred, "newdir", "added.txt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(pred, "add", "--", "newdir/added.txt"); err != nil {
		t.Fatal(err)
	}
	report.Manifest.Entries = append(report.Manifest.Entries, manifestEntry{
		Path: "newdir/added.txt", GitState: "staged", SHA256: hashFile(t, filepath.Join(pred, "newdir", "added.txt")),
	})
	doc := withFixture(t, loadDoc(t), report)
	index := hashFile(t, filepath.Join(pred, ".git", "index"))
	into := t.TempDir()
	var errBuf bytes.Buffer
	if code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &errBuf); code != 0 {
		t.Fatalf("nested staged restore %d %s", code, errBuf.String())
	}
	if string(mustRead(t, filepath.Join(into, "newdir", "added.txt"))) != string(body) {
		t.Fatal("nested staged bytes were not restored")
	}
	if hashFile(t, filepath.Join(pred, ".git", "index")) != index {
		t.Fatal("nested staged restore changed the predecessor index")
	}
}

func TestUnlistedIndexFlagsRefuseBeforeWrite(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		tracked bool
		delete  bool
	}{
		{"committed-skip-edit", "--skip-worktree", true, false},
		{"committed-assume-edit", "--assume-unchanged", true, false},
		{"committed-skip-delete", "--skip-worktree", true, true},
		{"committed-assume-delete", "--assume-unchanged", true, true},
		{"staged-skip-edit", "--skip-worktree", false, false},
		{"staged-assume-edit", "--assume-unchanged", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pred := t.TempDir()
			report, err := materialize(pred)
			if err != nil {
				t.Fatal(err)
			}
			if tc.tracked {
				report.Head = trackExtra(t, pred, "extra committed\n")
			} else if err := os.WriteFile(filepath.Join(pred, "extra.txt"), []byte("staged extra\n"), 0o644); err != nil {
				t.Fatal(err)
			} else if _, err := gitCmd(pred, "add", "--", "extra.txt"); err != nil {
				t.Fatal(err)
			}
			if _, err := gitCmd(pred, "update-index", tc.flag, "--", "extra.txt"); err != nil {
				t.Fatal(err)
			}
			if tc.delete {
				if err := os.Remove(filepath.Join(pred, "extra.txt")); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(pred, "extra.txt"), []byte("HIDDEN predecessor WIP\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			doc := withFixture(t, loadDoc(t), report)
			before := snapFixture(t, pred, []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt", "extra.txt"})
			assertFixtureRefused(t, pred, doc, before, "index flag")
		})
	}
}

func TestReplaceRefsRefuseBeforeWrite(t *testing.T) {
	t.Run("listed", func(t *testing.T) {
		pred := t.TempDir()
		report, err := materialize(pred)
		if err != nil {
			t.Fatal(err)
		}
		replaceBlob(t, pred, "committed.txt", "replaced committed\n")
		if err := os.WriteFile(filepath.Join(pred, "committed.txt"), []byte("replaced committed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for i := range report.Manifest.Entries {
			if report.Manifest.Entries[i].Path == "committed.txt" {
				report.Manifest.Entries[i].SHA256 = hashFile(t, filepath.Join(pred, "committed.txt"))
			}
		}
		doc := withFixture(t, loadDoc(t), report)
		before := snapFixture(t, pred, []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt"})
		assertFixtureRefused(t, pred, doc, before, "replace refs")
	})
	t.Run("unlisted", func(t *testing.T) {
		pred := t.TempDir()
		report, err := materialize(pred)
		if err != nil {
			t.Fatal(err)
		}
		report.Head = trackExtra(t, pred, "extra committed\n")
		replaceBlob(t, pred, "extra.txt", "HIDDEN predecessor WIP\n")
		if err := os.WriteFile(filepath.Join(pred, "extra.txt"), []byte("HIDDEN predecessor WIP\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		doc := withFixture(t, loadDoc(t), report)
		before := snapFixture(t, pred, []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt", "extra.txt"})
		assertFixtureRefused(t, pred, doc, before, "replace refs")
	})
}

func TestFilemodeFalseRefusesUnlistedExecBit(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	report.Head = trackExtra(t, pred, "extra committed\n")
	if _, err := gitCmd(pred, "config", "core.filemode", "false"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(pred, "extra.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	before := snapFixture(t, pred, []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt", "extra.txt"})
	assertFixtureRefused(t, pred, doc, before, "core.filemode=false")
	info, err := os.Lstat(filepath.Join(pred, "extra.txt"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("predecessor mode changed: %v", err)
	}
}

func TestLocalAttributesRefuseBeforeWrite(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pred, ".git", "info", "attributes"), []byte("* text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pred, "committed.txt"), []byte("synthetic committed line\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blob, err := gitCmd(pred, "rev-parse", "HEAD:committed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(pred, "update-index", "--cacheinfo", "100644,"+blob+",committed.txt"); err != nil {
		t.Fatal(err)
	}
	for i := range report.Manifest.Entries {
		if report.Manifest.Entries[i].Path == "committed.txt" {
			report.Manifest.Entries[i].SHA256 = hashFile(t, filepath.Join(pred, "committed.txt"))
		}
	}
	doc := withFixture(t, loadDoc(t), report)
	before := snapFixture(t, pred, []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt"})
	assertFixtureRefused(t, pred, doc, before, "local git attributes")
}

func TestCommittedAttributesStillRestore(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pred, ".gitattributes"), []byte("committed.txt text eol=lf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report.Head = trackPath(t, pred, ".gitattributes")
	doc := withFixture(t, loadDoc(t), report)
	index := hashFile(t, filepath.Join(pred, ".git", "index"))
	into := t.TempDir()
	var errBuf bytes.Buffer
	if code := run([]string{"restore", "--record", writeDoc(t, doc), "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &errBuf); code != 0 {
		t.Fatalf("committed attributes restore %d %s", code, errBuf.String())
	}
	if string(mustRead(t, filepath.Join(into, "committed.txt"))) != string(mustRead(t, filepath.Join(pred, "committed.txt"))) {
		t.Fatal("committed bytes changed")
	}
	if string(mustRead(t, filepath.Join(into, ".gitattributes"))) != "committed.txt text eol=lf\n" {
		t.Fatal("committed attributes were not restored")
	}
	if hashFile(t, filepath.Join(pred, ".git", "index")) != index {
		t.Fatal("predecessor index changed")
	}
}

func TestUntrackedNewDirectoryRefusedBeforeWrite(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(pred, "newdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pred, "newdir", "notes.txt"), []byte("note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report.Manifest.Entries = append(report.Manifest.Entries, manifestEntry{
		Path: "newdir/notes.txt", GitState: "untracked", SHA256: hashFile(t, filepath.Join(pred, "newdir", "notes.txt")),
	})
	doc := withFixture(t, loadDoc(t), report)
	before := snapFixture(t, pred, []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt", "newdir/notes.txt"})
	assertFixtureRefused(t, pred, doc, before, "before any restore write")
}

func assertFixtureRefused(t *testing.T, pred string, doc document, before map[string]string, want string) {
	t.Helper()
	var checkBuf bytes.Buffer
	record := writeDoc(t, doc)
	if code := run([]string{"check", "--verify-root", pred, record}, nil, &checkBuf, &bytes.Buffer{}); code != 0 {
		t.Fatalf("verify-root exit %d %s", code, checkBuf.String())
	}
	var res result
	if err := json.Unmarshal(checkBuf.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.FixtureVerified || res.RestoreAuthorized {
		t.Fatalf("fixture verified: %#v", res)
	}
	report := fixtureReport{Schema: fixtureSchema, Head: doc.Predecessor.Head, Manifest: doc.Manifest, Verified: true, OriginalsPreserved: "not-checked"}
	if code := run([]string{"verify", "--root", pred, "--manifest", writeManifest(t, report)}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("verify accepted the fixture")
	}
	into := t.TempDir()
	var errBuf bytes.Buffer
	code := run([]string{"restore", "--record", record, "--from", pred, "--into", into}, nil, &bytes.Buffer{}, &errBuf)
	if code != 2 || dirCount(t, into) != 0 || !strings.Contains(errBuf.String(), want) {
		t.Fatalf("restore code %d entries %d err %s", code, dirCount(t, into), errBuf.String())
	}
	missing := filepath.Join(t.TempDir(), "absent")
	var missingErr bytes.Buffer
	if code := run([]string{"restore", "--record", record, "--from", pred, "--into", missing}, nil, &bytes.Buffer{}, &missingErr); code == 0 {
		t.Fatal("restore created a missing recipient")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing recipient exists: %v", err)
	}
	sameSnap(t, before, snapFixture(t, pred, keysOf(before)))
}

func keysOf(snap map[string]string) []string {
	var paths []string
	for key := range snap {
		if strings.HasPrefix(key, "f:") {
			paths = append(paths, strings.TrimPrefix(key, "f:"))
		}
	}
	return paths
}

func sameSnap(t *testing.T, before, after map[string]string) {
	t.Helper()
	for key, value := range before {
		if after[key] != value {
			t.Fatalf("%s changed %q -> %q", key, value, after[key])
		}
	}
}

func snapFixture(t *testing.T, dir string, paths []string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	head, err := gitCmd(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	snap["head"] = head
	snap["index"] = hashFile(t, filepath.Join(dir, ".git", "index"))
	flags, err := gitCmd(dir, "ls-files", "-v")
	if err != nil {
		t.Fatal(err)
	}
	snap["flags"] = flags
	status, err := gitCmd(dir, "status", "--porcelain=v1")
	if err != nil {
		t.Fatal(err)
	}
	snap["status"] = status
	for _, path := range paths {
		full := filepath.Join(dir, path)
		info, err := os.Lstat(full)
		if err != nil {
			if os.IsNotExist(err) {
				snap["f:"+path] = "absent"
				continue
			}
			t.Fatal(err)
		}
		snap["f:"+path] = fmt.Sprintf("%o %s", info.Mode().Perm(), hashFile(t, full))
	}
	return snap
}

func trackExtra(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return trackPath(t, dir, "extra.txt")
}

func trackPath(t *testing.T, dir, name string) string {
	t.Helper()
	blob, err := gitCmd(dir, "hash-object", "-w", "--", name)
	if err != nil {
		t.Fatal(err)
	}
	list, err := gitCmd(dir, "ls-tree", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree := gitInput(t, dir, list+"\n100644 blob "+blob+"\t"+name+"\n", "mktree")
	parent, err := gitCmd(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := gitCmd(dir, "commit-tree", tree, "-p", parent, "-m", "track "+name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "update-ref", "HEAD", commit); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+name); err != nil {
		t.Fatal(err)
	}
	return commit
}

func replaceBlob(t *testing.T, dir, name, body string) {
	t.Helper()
	blob := gitInput(t, dir, body, "hash-object", "-w", "--stdin")
	list, err := gitCmd(dir, "ls-tree", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var treeIn strings.Builder
	for _, line := range strings.Split(list, "\n") {
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "\t"+name) {
			treeIn.WriteString("100644 blob " + blob + "\t" + name + "\n")
			continue
		}
		treeIn.WriteString(line + "\n")
	}
	tree := gitInput(t, dir, treeIn.String(), "mktree")
	parent, err := gitCmd(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit, err := gitCmd(dir, "commit-tree", tree, "-p", parent, "-m", "replace "+name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitCmd(dir, "replace", parent, commit); err != nil {
		t.Fatal(err)
	}
}

func gitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	full := []string{
		"--no-optional-locks",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=",
		"-c", "core.excludesFile=/dev/null",
		"-c", "core.attributesFile=/dev/null",
		"-c", "diff.autoRefreshIndex=false",
		"-c", "protocol.file.allow=always",
	}
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv()
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimRight(stdout.String(), "\r\n")
}

func dirCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
