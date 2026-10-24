package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
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
