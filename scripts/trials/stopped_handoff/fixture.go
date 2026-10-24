package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const fixtureSchema = "dev-env.stopped-handoff-fixture/v1"

var errOriginalAltered = errors.New("predecessor fixture changed during restore")

type fixtureReport struct {
	Schema             string   `json:"schema"`
	Head               string   `json:"head,omitempty"`
	Manifest           manifest `json:"manifest,omitempty"`
	Verified           bool     `json:"verified"`
	OriginalsPreserved string   `json:"originalsPreserved"`
	Error              string   `json:"error,omitempty"`
}

func materialize(dir string) (fixtureReport, error) {
	root, err := absOutside(dir)
	if err != nil {
		return fixtureReport{}, err
	}
	if err := requireEmpty(root); err != nil {
		return fixtureReport{}, err
	}
	if _, err := gitCmd(root, "init", "-b", "main"); err != nil {
		return fixtureReport{}, err
	}
	if err := writeRel(root, "committed.txt", "synthetic committed line\n"); err != nil {
		return fixtureReport{}, err
	}
	if err := writeRel(root, "tracked.txt", "synthetic base line\n"); err != nil {
		return fixtureReport{}, err
	}
	if _, err := gitCmd(root, "add", "--", "committed.txt", "tracked.txt"); err != nil {
		return fixtureReport{}, err
	}
	if _, err := gitCmd(root, "commit", "-m", "synthetic public fixture commit"); err != nil {
		return fixtureReport{}, err
	}
	if err := writeRel(root, "staged.txt", "synthetic staged line\n"); err != nil {
		return fixtureReport{}, err
	}
	if _, err := gitCmd(root, "add", "--", "staged.txt"); err != nil {
		return fixtureReport{}, err
	}
	if err := writeRel(root, "tracked.txt", "synthetic unstaged line\n"); err != nil {
		return fixtureReport{}, err
	}
	if err := writeRel(root, "untracked.txt", "synthetic untracked line\n"); err != nil {
		return fixtureReport{}, err
	}
	man, err := manifestFrom(root)
	if err != nil {
		return fixtureReport{}, err
	}
	if err := verifyFixture(root, man); err != nil {
		return fixtureReport{}, err
	}
	head, err := gitCmd(root, "rev-parse", "HEAD")
	if err != nil {
		return fixtureReport{}, err
	}
	return fixtureReport{Schema: fixtureSchema, Head: head, Manifest: man, Verified: true, OriginalsPreserved: "not-checked"}, nil
}

func manifestFrom(root string) (manifest, error) {
	specs := []struct {
		path  string
		state string
	}{
		{"committed.txt", "committed"},
		{"tracked.txt", "unstaged"},
		{"staged.txt", "staged"},
		{"untracked.txt", "untracked"},
	}
	man := manifest{Algorithm: "sha256"}
	for _, spec := range specs {
		sum, err := fileSHA(root, spec.path)
		if err != nil {
			return manifest{}, err
		}
		man.Entries = append(man.Entries, manifestEntry{Path: spec.path, GitState: spec.state, SHA256: sum})
	}
	return man, nil
}

func restoreFixture(from, into string, man manifest) error {
	src, err := absOutside(from)
	if err != nil {
		return err
	}
	dst, err := absOutside(into)
	if err != nil {
		return err
	}
	if err := separate(src, dst); err != nil {
		return err
	}
	if err := verifyFixture(src, man); err != nil {
		return fmt.Errorf("predecessor fixture: %w", err)
	}
	before, err := snapshot(src, man)
	if err != nil {
		return err
	}
	if err := requireEmpty(dst); err != nil {
		return err
	}
	if _, err := gitCmd(dst, "init", "-b", "main"); err != nil {
		return err
	}
	if _, err := gitCmd(dst, "fetch", "--no-tags", src, "HEAD"); err != nil {
		return err
	}
	if _, err := gitCmd(dst, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}
	if err := overlay(src, dst, man); err != nil {
		return err
	}
	if err := verifyFixture(dst, man); err != nil {
		return err
	}
	after, err := snapshot(src, man)
	if err != nil {
		return err
	}
	if before != after {
		return errOriginalAltered
	}
	return nil
}

func overlay(src, dst string, man manifest) error {
	for _, entry := range man.Entries {
		if entry.GitState == "committed" {
			continue
		}
		from, err := joinWithin(src, entry.Path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		if err := writeRel(dst, entry.Path, string(body)); err != nil {
			return err
		}
		if entry.GitState == "staged" {
			if _, err := gitCmd(dst, "add", "--", entry.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyFixture(root string, man manifest) error {
	abs, err := absOutside(root)
	if err != nil {
		return err
	}
	if err := validateManifest(man); err != nil {
		return err
	}
	status, err := gitCmd(abs, "status", "--porcelain=v1")
	if err != nil {
		return err
	}
	got := parsePorcelain(status)
	want := map[string]string{}
	for _, entry := range man.Entries {
		sum, err := fileSHA(abs, entry.Path)
		if err != nil {
			return err
		}
		if sum != entry.SHA256 {
			return fmt.Errorf("%s hash mismatch", entry.Path)
		}
		code, present := got[entry.Path]
		switch entry.GitState {
		case "committed":
			if present {
				return fmt.Errorf("%s is not a clean commit", entry.Path)
			}
		case "staged":
			if code != "A " && code != "M " {
				return fmt.Errorf("%s is not staged (%q)", entry.Path, code)
			}
			want[entry.Path] = code
		case "unstaged":
			if code != " M" && code != "MM" {
				return fmt.Errorf("%s is not unstaged (%q)", entry.Path, code)
			}
			want[entry.Path] = code
		case "untracked":
			if code != "??" {
				return fmt.Errorf("%s is not untracked (%q)", entry.Path, code)
			}
			want[entry.Path] = code
		default:
			return fmt.Errorf("gitState %q is unknown", entry.GitState)
		}
	}
	for path := range got {
		if _, ok := want[path]; !ok {
			return fmt.Errorf("unexpected git status for %s", path)
		}
	}
	return nil
}

func snapshot(root string, man manifest) (string, error) {
	var b strings.Builder
	head, err := gitCmd(root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "head %s\n", head)
	status, err := gitCmd(root, "status", "--porcelain=v1")
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "status\n%s\n", status)
	diff, err := gitCmd(root, "diff")
	if err != nil {
		return "", err
	}
	cached, err := gitCmd(root, "diff", "--cached")
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "diff\n%s\ncached\n%s\n", diff, cached)
	for _, entry := range man.Entries {
		sum, err := fileSHA(root, entry.Path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "file %s %s\n", entry.Path, sum)
	}
	return b.String(), nil
}

func readManifest(path string) (manifest, error) {
	data, err := readInput(path, nil)
	if err != nil {
		return manifest{}, err
	}
	var report fixtureReport
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		var man manifest
		dec = json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err2 := dec.Decode(&man); err2 != nil {
			return manifest{}, err
		}
		return man, nil
	}
	if len(report.Manifest.Entries) > 0 {
		return report.Manifest, nil
	}
	return manifest{}, errors.New("manifest is missing")
}

func parsePorcelain(status string) map[string]string {
	got := map[string]string{}
	if status == "" {
		return got
	}
	for _, line := range strings.Split(status, "\n") {
		if line == "" {
			continue
		}
		if len(line) < 4 {
			got[line] = line
			continue
		}
		got[line[3:]] = line[:2]
	}
	return got
}

func fileSHA(root, name string) (string, error) {
	path, err := joinWithin(root, name)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", name)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func writeRel(root, name, body string) error {
	path, err := joinWithin(root, name)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func cleanRel(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("path %q is not a relative fixture path", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the fixture", name)
	}
	return clean, nil
}

func joinWithin(root, name string) (string, error) {
	rel, err := cleanRel(name)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, rel)
	root = filepath.Clean(root)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the fixture", name)
	}
	return full, nil
}

func gitCmd(dir string, args ...string) (string, error) {
	full := make([]string, 0, len(args)+4)
	full = append(full, "-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always")
	full = append(full, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, msg)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func fixtureEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=synthetic",
		"GIT_AUTHOR_EMAIL=synthetic@example.invalid",
		"GIT_COMMITTER_NAME=synthetic",
		"GIT_COMMITTER_EMAIL=synthetic@example.invalid",
	}
}

func absOutside(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if err := ensureOutsideWorktree(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func ensureOutsideWorktree(abs string) error {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Env = fixtureEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	top := filepath.Clean(strings.TrimRight(string(out), "\r\n"))
	if abs == top || strings.HasPrefix(abs, top+string(filepath.Separator)) {
		return fmt.Errorf("%s is inside the source worktree", abs)
	}
	return nil
}

func requireEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("%s is not an empty directory", dir)
	}
	return nil
}

func separate(a, b string) error {
	if a == b {
		return errors.New("fixture directories must be separate")
	}
	if inside(a, b) {
		return errors.New("recipient fixture is inside the predecessor")
	}
	if inside(b, a) {
		return errors.New("predecessor fixture is inside the recipient")
	}
	return nil
}

func inside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
