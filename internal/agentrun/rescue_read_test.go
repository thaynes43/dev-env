package agentrun

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestRescueReadWritesOnlyAVerifiedTree(t *testing.T) {
	h := newHarness(t)
	const (
		id         = "byte-1006-170000/20261006-1730"
		commit     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		sessionUID = "44444444-4444-4444-8444-444444444444"
		podUID     = "55555555-5555-4555-8555-555555555555"
		marker     = "UNIQUE-BYTE-MARKER\n"
	)
	body := []byte(marker)
	nested := []byte("echo\n")
	sum := sha256.Sum256(body)
	nestedSum := sha256.Sum256(nested)
	tree := apiv1.RescueTree{
		ID: id, SessionUID: sessionUID, PodUID: podUID, Repo: "haynes-ops", Commit: commit,
		Ref: "refs/heads/rescue/byte-1006-170000-20261006-1730",
		Files: []apiv1.RescueTreeFile{
			{Path: "README.md", Mode: "100644", SHA256: hex.EncodeToString(sum[:]), Bytes: body},
			{Path: "scratch/new.sh", Mode: "100755", SHA256: hex.EncodeToString(nestedSum[:]), Bytes: nested},
		},
	}
	tries := 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		tries++
		if r.Method != http.MethodGet {
			writeTestJSON(w, http.StatusMethodNotAllowed, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeMethodNotAllowed, Message: "method"}})
			return
		}
		if tries == 1 {
			writeTestJSON(w, http.StatusServiceUnavailable, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeUnavailable, Message: "later"}})
			return
		}
		writeTestJSON(w, http.StatusOK, tree)
	}
	parent := t.TempDir()
	out := filepath.Join(parent, "tree")
	h.mustRun(ExitOK, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", out)
	if tries != 2 {
		t.Fatalf("tries %d", tries)
	}
	for _, req := range h.api.requests() {
		if req.method != http.MethodGet || !strings.Contains(req.path, "/tree") {
			t.Fatalf("request %+v", req)
		}
	}
	got, err := os.ReadFile(filepath.Join(out, "README.md"))
	if err != nil || string(got) != marker {
		t.Fatalf("README %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(out, "scratch", "new.sh"))
	if err != nil || info.Mode().Perm() != 0o755 || string(mustRead(t, filepath.Join(out, "scratch", "new.sh"))) != "echo\n" {
		t.Fatalf("script %v %v", info, err)
	}
	if strings.Contains(h.stdout.String(), marker) || strings.Contains(h.stderr.String(), marker) {
		t.Fatalf("the command printed file bytes:\n%s", h.stdout.String())
	}
	if !strings.Contains(h.stdout.String(), hex.EncodeToString(sum[:])) || !strings.Contains(h.stdout.String(), "README.md") {
		t.Fatalf("receipt:\n%s", h.stdout.String())
	}
	busy := 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		busy++
		writeTestJSON(w, http.StatusTooManyRequests, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeLimitExceeded, Message: "this replica is already reading a rescue tree"}})
	}
	h.mustRun(ExitRetry, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", filepath.Join(parent, "busy"))
	if busy != 1 {
		t.Fatalf("a 429 was retried: %d", busy)
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusOK, tree)
	}
	h.mustRun(ExitFailed, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", out)
	if string(mustRead(t, filepath.Join(out, "README.md"))) != marker {
		t.Fatal("a collision rewrote the directory")
	}
	empty := filepath.Join(parent, "empty-exists")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	h.mustRun(ExitFailed, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", empty)
	entries, err := os.ReadDir(empty)
	if err != nil || len(entries) != 0 {
		t.Fatalf("an empty directory was replaced: %v %v", entries, err)
	}

	linkParent := t.TempDir()
	real := filepath.Join(linkParent, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(linkParent, "link")); err != nil {
		t.Fatal(err)
	}
	h.mustRun(ExitFailed, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", filepath.Join(linkParent, "link", "tree"))
	if _, err := os.Stat(filepath.Join(real, "tree")); !os.IsNotExist(err) {
		t.Fatalf("symlink out wrote %v", err)
	}

	saved := tree
	bad := tree
	bad.Files = []apiv1.RescueTreeFile{{Path: "../outside", Mode: "100644", SHA256: hex.EncodeToString(sum[:]), Bytes: body}}
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, bad) }
	escaped := filepath.Join(parent, "escaped")
	h.mustRun(ExitFailed, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", escaped)
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("traversal created --out: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(parent, ".rescue-read-*")); len(leftovers) != 0 {
		t.Fatalf("leftover %v", leftovers)
	}

	mismatch := saved
	mismatch.Files = append([]apiv1.RescueTreeFile(nil), saved.Files...)
	mismatch.Files[0].SHA256 = strings.Repeat("cd", 32)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, mismatch) }
	h.mustRun(ExitFailed, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", filepath.Join(parent, "mismatch"))
	if _, err := os.Stat(filepath.Join(parent, "mismatch")); !os.IsNotExist(err) {
		t.Fatalf("hash mismatch created --out: %v", err)
	}
	gitPath := saved
	gitPath.Files = []apiv1.RescueTreeFile{{Path: ".GIT/config", Mode: "100644", SHA256: hex.EncodeToString(sum[:]), Bytes: body}}
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, gitPath) }
	h.mustRun(ExitFailed, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", filepath.Join(parent, "gitpath"))
	if _, err := os.Stat(filepath.Join(parent, "gitpath")); !os.IsNotExist(err) {
		t.Fatalf("unsafe path created --out: %v", err)
	}
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, saved) }
	h.mustRun(ExitOK, "rescue", "read", id, "--repo", "haynes-ops", "--commit", commit, "--session-uid", sessionUID, "--pod-uid", podUID, "--out", filepath.Join(parent, "retry"), "-o", "json")
	if strings.Contains(h.stdout.String(), marker) {
		t.Fatal("json receipt contained file bytes")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
