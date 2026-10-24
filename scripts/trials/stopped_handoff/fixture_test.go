package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureRestorePreservesOriginal(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verified || !validHead(report.Head) {
		t.Fatalf("materialize %#v", report)
	}
	doc := withFixture(t, loadDoc(t), report)
	record := writeDoc(t, doc)
	before, err := snapshot(pred, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	recip := t.TempDir()
	var buf bytes.Buffer
	var errBuf bytes.Buffer
	code := run([]string{"restore", "--record", record, "--from", pred, "--into", recip}, nil, &buf, &errBuf)
	if code != 0 {
		t.Fatalf("restore %d %s %s", code, errBuf.String(), buf.String())
	}
	var res result
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.FixtureVerified || res.OriginalsPreserved != "preserved" || !res.RestoreAuthorized || res.InstalledJourney || res.TaskClaim != "unchanged" {
		t.Fatalf("restore result %#v", res)
	}
	after, err := snapshot(pred, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("predecessor fixture changed")
	}
	if err := verifyFixture(pred, report.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := verifyFixture(recip, report.Manifest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"committed.txt", "tracked.txt", "staged.txt", "untracked.txt"} {
		left, err := os.ReadFile(filepath.Join(pred, name))
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(filepath.Join(recip, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(right) {
			t.Fatalf("%s bytes differ", name)
		}
	}

	tracked := filepath.Join(recip, "untracked.txt")
	if err := os.WriteFile(tracked, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyFixture(recip, report.Manifest); err == nil {
		t.Fatal("changed WIP verified")
	}
	again, err := snapshot(pred, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if again != before {
		t.Fatal("tampering the recipient changed the predecessor")
	}
	if err := os.Remove(filepath.Join(recip, "staged.txt")); err != nil {
		t.Fatal(err)
	}
	if err := verifyFixture(recip, report.Manifest); err == nil {
		t.Fatal("missing WIP verified")
	}
}

func TestRestoreRefusesWithoutWriting(t *testing.T) {
	pred := t.TempDir()
	report, err := materialize(pred)
	if err != nil {
		t.Fatal(err)
	}
	doc := withFixture(t, loadDoc(t), report)
	doc.Predecessor.ExecutorState = "running"
	record := writeDoc(t, doc)
	before, err := snapshot(pred, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	into := t.TempDir()
	var buf bytes.Buffer
	code := run([]string{"restore", "--record", record, "--from", pred, "--into", into}, nil, &buf, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("running restore exit %d %s", code, buf.String())
	}
	var res result
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.RestoreAuthorized || res.FixtureVerified || res.OriginalsPreserved != "not-checked" {
		t.Fatalf("running result %#v", res)
	}
	entries, err := os.ReadDir(into)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("recipient was written: %d entries", len(entries))
	}
	after, err := snapshot(pred, report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("refused restore changed the predecessor")
	}

	changed := withFixture(t, loadDoc(t), report)
	changed.Observed.WIPChanged = true
	changed.Observed.WIPHash = stringsRepeatA()
	into2 := t.TempDir()
	buf.Reset()
	code = run([]string{"restore", "--record", writeDoc(t, changed), "--from", pred, "--into", into2}, nil, &buf, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("changed restore %d", code)
	}
	entries, err = os.ReadDir(into2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("changed WIP restore wrote the recipient")
	}
}

func TestMaterializeRefusesSourceWorktree(t *testing.T) {
	dir := filepath.Join(".", "testdata", "guard-empty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if _, err := materialize(dir); err == nil {
		t.Fatal("materialize wrote inside the source worktree")
	}
}

func withFixture(t *testing.T, doc document, report fixtureReport) document {
	t.Helper()
	doc.Predecessor.Head = report.Head
	doc.Observed.Head = report.Head
	doc.Manifest = report.Manifest
	digest, err := wipDigest(report.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	doc.Observed.WIPPresent = true
	doc.Observed.WIPChanged = false
	doc.Observed.WIPHash = digest
	return doc
}

func writeDoc(t *testing.T, doc document) string {
	t.Helper()
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func stringsRepeatA() string {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}
