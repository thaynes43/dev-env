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

func restoreFixture(from, into string, man manifest, head string) error {
	if !validHead(head) {
		return errors.New("record head is not 40 lowercase hex characters")
	}
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
	modes, err := fixtureModes(src, man)
	if err != nil {
		return fmt.Errorf("predecessor fixture: %w", err)
	}
	if err := requireHEAD(src, head); err != nil {
		return err
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
	branch, err := gitCmd(src, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\n") {
		return fmt.Errorf("refusing ref %q before any restore write", branch)
	}
	if _, err := gitCmd(dst, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return err
	}
	if err := requireHEAD(dst, head); err != nil {
		return err
	}
	if err := overlay(src, dst, man, modes); err != nil {
		return finishSource(src, man, before, err)
	}
	if err := applyCommittedModes(src, dst, man); err != nil {
		return finishSource(src, man, before, err)
	}
	if err := verifyFixture(dst, man); err != nil {
		return finishSource(src, man, before, err)
	}
	if err := samePreservation(src, dst, man); err != nil {
		return finishSource(src, man, before, err)
	}
	return finishSource(src, man, before, nil)
}

func finishSource(src string, man manifest, before string, opErr error) error {
	after, err := snapshot(src, man)
	if err != nil {
		if opErr != nil {
			return opErr
		}
		return err
	}
	if before != after {
		return errOriginalAltered
	}
	if opErr != nil {
		return preservedSourceError{err: opErr}
	}
	return nil
}

type preservedSourceError struct {
	err error
}

func (e preservedSourceError) Error() string { return e.err.Error() }

func (e preservedSourceError) Unwrap() error { return e.err }

func overlay(src, dst string, man manifest, modes map[string]os.FileMode) error {
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = dstRoot.Close() }()
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcRoot.Close() }()
	for _, entry := range man.Entries {
		if entry.GitState == "committed" {
			continue
		}
		body, err := srcRoot.ReadFile(entry.Path)
		if err != nil {
			return err
		}
		if err := ensureRecipientParent(dstRoot, entry.Path); err != nil {
			return err
		}
		if err := dstRoot.WriteFile(entry.Path, body, modes[entry.Path]); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("recipient path %s was not written: %w", entry.Path, err)
			}
			return fmt.Errorf("refusing to write %s outside the recipient fixture: %w", entry.Path, err)
		}
		if err := dstRoot.Chmod(entry.Path, modes[entry.Path]); err != nil {
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

func ensureRecipientParent(root *os.Root, name string) error {
	dir := filepath.Dir(name)
	if dir == "." || dir == "" {
		return nil
	}
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create recipient parent for %s: %w", name, err)
	}
	return nil
}

func fixtureModes(root string, man manifest) (map[string]os.FileMode, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	modes := map[string]os.FileMode{}
	for _, entry := range man.Entries {
		if entry.GitState == "committed" {
			continue
		}
		info, err := r.Lstat(entry.Path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", entry.Path)
		}
		perm := info.Mode().Perm()
		if perm != 0o644 && perm != 0o755 {
			return nil, fmt.Errorf("refusing unsupported mode %o for %s before any restore write", perm, entry.Path)
		}
		modes[entry.Path] = perm
	}
	return modes, nil
}

func applyCommittedModes(src, dst string, man manifest) error {
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcRoot.Close() }()
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = dstRoot.Close() }()
	for _, entry := range man.Entries {
		if entry.GitState != "committed" {
			continue
		}
		info, err := srcRoot.Lstat(entry.Path)
		if err != nil {
			return err
		}
		perm := info.Mode().Perm()
		if perm != 0o644 && perm != 0o755 {
			return fmt.Errorf("refusing unsupported mode %o for %s", perm, entry.Path)
		}
		if err := dstRoot.Chmod(entry.Path, perm); err != nil {
			return err
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
	if err := guardRepo(abs); err != nil {
		return err
	}
	if err := refuseReplaceRefs(abs); err != nil {
		return err
	}
	if err := refuseUnsupportedRefs(abs); err != nil {
		return err
	}
	if err := refuseLocalAttributes(abs); err != nil {
		return err
	}
	if err := refuseWholeIndexFlags(abs); err != nil {
		return err
	}
	if err := refuseGitlinks(abs); err != nil {
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
				return fmt.Errorf("refusing unsupported git state %q for %s before any restore write", code, entry.Path)
			}
			want[entry.Path] = code
		case "unstaged":
			if code != " M" {
				return fmt.Errorf("refusing unsupported git state %q for %s before any restore write", code, entry.Path)
			}
			want[entry.Path] = code
		case "untracked":
			if code != "??" {
				return fmt.Errorf("refusing unsupported git state %q for %s before any restore write", code, entry.Path)
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
	return bindTreeAndModes(abs, man)
}

func bindTreeAndModes(root string, man manifest) error {
	if _, err := fixtureModes(root, man); err != nil {
		return err
	}
	for _, entry := range man.Entries {
		switch entry.GitState {
		case "committed":
			if err := refuseIndexFlags(root, entry.Path); err != nil {
				return err
			}
			if err := requireWorktreeBlob(root, entry.Path, "HEAD:"+entry.Path); err != nil {
				return err
			}
			mode, err := headBlobMode(root, entry.Path)
			if err != nil {
				return err
			}
			if err := worktreeModeMatches(root, entry.Path, mode); err != nil {
				return err
			}
			if err := requireCheckoutBytes(root, entry.Path, entry.SHA256); err != nil {
				return err
			}
		case "staged":
			if err := refuseIndexFlags(root, entry.Path); err != nil {
				return err
			}
			if err := requireWorktreeBlob(root, entry.Path, ":"+entry.Path); err != nil {
				return err
			}
			mode, err := indexGitMode(root, entry.Path)
			if err != nil {
				return err
			}
			if err := worktreeModeMatches(root, entry.Path, mode); err != nil {
				return err
			}
		case "unstaged":
			if err := refuseIndexFlags(root, entry.Path); err != nil {
				return err
			}
			if err := requireSameGitObject(root, ":"+entry.Path, "HEAD:"+entry.Path); err != nil {
				return err
			}
			mode, err := indexGitMode(root, entry.Path)
			if err != nil {
				return err
			}
			if err := worktreeModeMatches(root, entry.Path, mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func refuseIndexFlags(dir, path string) error {
	out, err := gitCmd(dir, "ls-files", "-v", "--", path)
	if err != nil {
		return err
	}
	if out == "" || strings.Contains(out, "\n") {
		return fmt.Errorf("%s is not a single index record", path)
	}
	tag, name, ok := strings.Cut(out, " ")
	if !ok || name != path || len(tag) != 1 {
		return fmt.Errorf("%s index record %q is not a single tag", path, out)
	}
	if indexFlagUnsupported(tag[0]) {
		return fmt.Errorf("refusing index flag %q for %s before any restore write", tag, path)
	}
	return nil
}

func refuseWholeIndexFlags(dir string) error {
	out, err := gitCmd(dir, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		if len(rec) < 3 || rec[1] != ' ' {
			return fmt.Errorf("index record %q is not a single tag", rec)
		}
		path := rec[2:]
		if indexFlagUnsupported(rec[0]) {
			return fmt.Errorf("refusing index flag %q for %s before any restore write", rec[:1], path)
		}
	}
	return nil
}

func indexFlagUnsupported(flag byte) bool {
	return flag == 'S' || (flag >= 'a' && flag <= 'z')
}

func refuseReplaceRefs(dir string) error {
	out, err := gitCmd(dir, "for-each-ref", "--format=%(refname)", "refs/replace")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("refusing replace refs before any restore write")
	}
	return nil
}

func refuseUnsupportedRefs(dir string) error {
	head, err := gitCmd(dir, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || !strings.HasPrefix(head, "refs/heads/") {
		return fmt.Errorf("refusing detached HEAD before any restore write")
	}
	out, err := gitCmd(dir, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return err
	}
	found := false
	for _, ref := range strings.Split(out, "\n") {
		if ref == "" {
			continue
		}
		if ref == head {
			found = true
			continue
		}
		return fmt.Errorf("refusing ref %s before any restore write", ref)
	}
	if !found {
		return fmt.Errorf("refusing missing HEAD ref %s before any restore write", head)
	}
	return nil
}

func refuseGitlinks(dir string) error {
	out, err := gitStdout(dir, "ls-files", "-s", "-z")
	if err != nil {
		return err
	}
	for rec := range strings.SplitSeq(string(out), "\x00") {
		if rec == "" {
			continue
		}
		mode, _, ok := strings.Cut(rec, " ")
		if !ok {
			return fmt.Errorf("index record %q has no mode", rec)
		}
		if mode == "160000" {
			path := rec
			if i := strings.IndexByte(rec, '\t'); i >= 0 {
				path = rec[i+1:]
			}
			return fmt.Errorf("refusing gitlink %s before any restore write", path)
		}
	}
	return nil
}

func requireCheckoutBytes(dir, path, want string) error {
	out, err := gitStdout(dir, "cat-file", "--filters", "HEAD:"+path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(out)
	if hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("refusing checkout bytes for %s before any restore write", path)
	}
	return nil
}

func refuseLocalAttributes(dir string) error {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	info, err := r.Lstat(".git/info/attributes")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 0 {
		return fmt.Errorf("refusing local git attributes before any restore write")
	}
	return nil
}

func requireWorktreeBlob(dir, path, object string) error {
	work, err := gitCmd(dir, "hash-object", "--", path)
	if err != nil {
		return err
	}
	return sameGitID(dir, path, work, object)
}

func requireSameGitObject(dir, left, right string) error {
	got, err := gitCmd(dir, "rev-parse", "--verify", left)
	if err != nil {
		return err
	}
	return sameGitID(dir, left, got, right)
}

func sameGitID(dir, path, got, object string) error {
	want, err := gitCmd(dir, "rev-parse", "--verify", object)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("refusing %s before any restore write: blob %s is not %s (%s)", path, got, object, want)
	}
	return nil
}

func worktreeModeMatches(root, path, gitMode string) error {
	want, err := permFromGitMode(gitMode)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	info, err := r.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm() != want {
		return fmt.Errorf("refusing worktree mode %o for %s; git mode is %s; refusing before any restore write", info.Mode().Perm(), path, gitMode)
	}
	return nil
}

func permFromGitMode(mode string) (os.FileMode, error) {
	switch mode {
	case "100644":
		return 0o644, nil
	case "100755":
		return 0o755, nil
	default:
		return 0, fmt.Errorf("refusing unsupported git mode %s before any restore write", mode)
	}
}

func headBlobMode(dir, path string) (string, error) {
	out, err := gitCmd(dir, "ls-tree", "HEAD", "--", path)
	if err != nil {
		return "", err
	}
	if out == "" || strings.Contains(out, "\n") {
		return "", fmt.Errorf("%s is not a single HEAD blob", path)
	}
	meta, name, ok := strings.Cut(out, "\t")
	if !ok || name != path {
		return "", fmt.Errorf("%s is not in the HEAD tree", path)
	}
	parts := strings.Fields(meta)
	if len(parts) != 3 || parts[1] != "blob" {
		return "", fmt.Errorf("%s is not a HEAD blob", path)
	}
	if _, err := permFromGitMode(parts[0]); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return parts[0], nil
}

func indexGitMode(dir, path string) (string, error) {
	out, err := gitCmd(dir, "ls-files", "-s", "--", path)
	if err != nil {
		return "", err
	}
	if out == "" || strings.Contains(out, "\n") {
		return "", fmt.Errorf("%s is not a single index blob", path)
	}
	meta, name, ok := strings.Cut(out, "\t")
	if !ok || name != path {
		return "", fmt.Errorf("%s is not in the index", path)
	}
	parts := strings.Fields(meta)
	if len(parts) != 3 || parts[2] != "0" {
		return "", fmt.Errorf("%s is not a stage-zero index blob", path)
	}
	if _, err := permFromGitMode(parts[0]); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return parts[0], nil
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
	indexHash, err := hashGitIndex(root)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "index %s\n", indexHash)
	for _, entry := range man.Entries {
		sum, err := fileSHA(root, entry.Path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "file %s %s\n", entry.Path, sum)
	}
	return b.String(), nil
}

func readManifest(path string) (manifest, string, error) {
	data, err := readInput(path, nil)
	if err != nil {
		return manifest{}, "", err
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return manifest{}, "", err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var report fixtureReport
	if err := dec.Decode(&report); err != nil {
		dec = json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var man manifest
		if err2 := dec.Decode(&man); err2 != nil {
			return manifest{}, "", err
		}
		if err := requireEOF(dec); err != nil {
			return manifest{}, "", err
		}
		if len(man.Entries) == 0 {
			return manifest{}, "", errors.New("manifest is missing")
		}
		return man, "", nil
	}
	if err := requireEOF(dec); err != nil {
		return manifest{}, "", err
	}
	if len(report.Manifest.Entries) == 0 {
		return manifest{}, "", errors.New("manifest is missing")
	}
	return report.Manifest, report.Head, nil
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
	rel, err := cleanRel(name)
	if err != nil {
		return "", err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	info, err := r.Lstat(rel)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", name)
	}
	body, err := r.ReadFile(rel)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func writeRel(root, name, body string) error {
	rel, err := cleanRel(name)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if err := r.WriteFile(rel, []byte(body), 0o644); err != nil {
		return err
	}
	return r.Chmod(rel, 0o644)
}

func cleanRel(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("path %q is not a relative fixture path", name)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the fixture", name)
	}
	for _, part := range strings.Split(clean, "/") {
		if strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("path %q is git metadata", name)
		}
	}
	return clean, nil
}

func gitCmd(dir string, args ...string) (string, error) {
	stdout, err := gitStdout(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(stdout), "\r\n"), nil
}

func gitStdout(dir string, args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+8)
	full = append(full,
		"--no-optional-locks",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=",
		"-c", "core.excludesFile=/dev/null",
		"-c", "core.attributesFile=/dev/null",
		"-c", "diff.autoRefreshIndex=false",
		"-c", "protocol.file.allow=always",
	)
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
		return nil, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

func fixtureEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
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
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if err := ensureOutsideWorktree(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func ensureOutsideWorktree(abs string) error {
	parent := filepath.Dir(abs)
	cmd := exec.Command("git", "-C", parent, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=", "rev-parse", "--show-toplevel")
	cmd.Env = fixtureEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "not a git repository") {
			return nil
		}
		return fmt.Errorf("worktree guard failed closed for %s: %w (%s)", abs, err, strings.TrimSpace(string(out)))
	}
	top := filepath.Clean(strings.TrimSpace(string(out)))
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = filepath.Clean(resolved)
	}
	if abs == top || strings.HasPrefix(abs, top+string(filepath.Separator)) {
		return fmt.Errorf("%s is inside a git worktree", abs)
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

func separate(src, dst string) error {
	if src == dst {
		return errors.New("fixture directories must be separate")
	}
	if inside(src, dst) {
		return errors.New("recipient fixture is inside the predecessor")
	}
	if inside(dst, src) {
		return errors.New("predecessor fixture is inside the recipient")
	}
	gitDir := filepath.Join(src, ".git")
	if dst == gitDir || inside(gitDir, dst) || inside(dst, gitDir) {
		return errors.New("recipient fixture is inside the predecessor git directory")
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

func requireHEAD(dir, head string) error {
	got, err := gitCmd(dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if got != head {
		return fmt.Errorf("fixture HEAD %s does not match record head %s", got, head)
	}
	return nil
}

func guardRepo(dir string) error {
	if err := plainGitDir(dir); err != nil {
		return err
	}
	return localConfigAllowed(dir)
}

func plainGitDir(dir string) error {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New(".git is a symlink")
	}
	if !info.IsDir() {
		return errors.New(".git is not a plain directory")
	}
	return nil
}

func localConfigAllowed(dir string) error {
	out, err := gitCmd(dir, "config", "--local", "--list")
	if err != nil {
		return err
	}
	allowed := map[string]string{
		"core.repositoryformatversion": "0",
		"core.filemode":                "true",
		"core.bare":                    "false",
		"core.logallrefupdates":        "true",
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		want, known := allowed[strings.ToLower(key)]
		if !ok || !known || value != want {
			return fmt.Errorf("local git config %q is not allowed", line)
		}
	}
	return nil
}

func hashGitIndex(dir string) (string, error) {
	if err := plainGitDir(dir); err != nil {
		return "", err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	body, err := r.ReadFile(".git/index")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func samePreservation(src, dst string, man manifest) error {
	srcStatus, err := gitCmd(src, "status", "--porcelain=v1")
	if err != nil {
		return err
	}
	dstStatus, err := gitCmd(dst, "status", "--porcelain=v1")
	if err != nil {
		return err
	}
	srcCodes := parsePorcelain(srcStatus)
	dstCodes := parsePorcelain(dstStatus)
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcRoot.Close() }()
	dstRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = dstRoot.Close() }()
	for _, entry := range man.Entries {
		if srcCodes[entry.Path] != dstCodes[entry.Path] {
			return fmt.Errorf("%s status %q does not match recipient %q", entry.Path, srcCodes[entry.Path], dstCodes[entry.Path])
		}
		srcInfo, err := srcRoot.Lstat(entry.Path)
		if err != nil {
			return err
		}
		dstInfo, err := dstRoot.Lstat(entry.Path)
		if err != nil {
			return err
		}
		if srcInfo.Mode().Perm() != dstInfo.Mode().Perm() {
			return fmt.Errorf("%s mode %o does not match recipient %o", entry.Path, srcInfo.Mode().Perm(), dstInfo.Mode().Perm())
		}
		if entry.GitState == "untracked" {
			continue
		}
		srcLine, err := gitCmd(src, "ls-files", "-s", "--", entry.Path)
		if err != nil {
			return err
		}
		dstLine, err := gitCmd(dst, "ls-files", "-s", "--", entry.Path)
		if err != nil {
			return err
		}
		if srcLine != dstLine {
			return fmt.Errorf("%s index %q does not match recipient %q", entry.Path, srcLine, dstLine)
		}
	}
	return nil
}
