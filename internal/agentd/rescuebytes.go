package agentd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// RescueMaterialize is a byte read of one rescue commit. Clone, Bundle,
// Commit, Base and Ref come from the operator's rescue status, not from the
// bundle file. Bundle is relative to the shared volume.
type RescueMaterialize struct {
	ID, Repo, Clone, Bundle, Commit, Base, Ref string
}

// MaterializeRescueTree verifies the existing thin bundle in the recipient
// clone and returns the delta from the bundle's single prerequisite. File
// bytes are only the blobs the bundle itself carries. The recipient's existing
// base objects are not copied out. It does not check out, run hooks, or
// contact a remote. The thin bundle on the shared volume is not rewritten.
func MaterializeRescueTree(ctx context.Context, r Runner, s Settings, req RescueMaterialize) (protocol.RescueTree, error) {
	refuse := func(code string) (protocol.RescueTree, error) {
		return protocol.RescueTree{Code: code, ID: req.ID, Repo: req.Repo, Commit: req.Commit}, nil
	}
	if !protocol.ValidGitObject(req.Commit) || !protocol.ValidGitObject(req.Base) || req.Commit == req.Base || !protocol.RescueWorkRef(req.Ref) || !acceptableGitPath(req.Clone) || !acceptableBundle(req.Bundle) {
		return refuse(protocol.RescueTreeMismatch)
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return protocol.RescueTree{}, err
	}
	raw, err := readBundleOnce(s.SharedDir, req.Bundle)
	if err != nil {
		if errors.Is(err, errBundleLarge) {
			return refuse(protocol.TreeTooLarge)
		}
		if errors.Is(err, os.ErrNotExist) {
			return refuse(protocol.RescueTreeNotFound)
		}
		return refuse(protocol.RescueTreeRefused)
	}
	if err := recipientStoreClear(req.Clone); err != nil {
		return refuse(protocol.RescueTreeRefused)
	}
	scratch, err := os.CreateTemp("", "rescue-bundle-")
	if err != nil {
		return protocol.RescueTree{}, err
	}
	scratchName := scratch.Name()
	defer func() { _ = os.Remove(scratchName) }()
	if _, err := scratch.Write(raw); err != nil {
		_ = scratch.Close()
		return protocol.RescueTree{}, err
	}
	if err := scratch.Close(); err != nil {
		return protocol.RescueTree{}, err
	}

	if _, err := isolatedGit(ctx, r, req.Clone, "cat-file", "-e", req.Base+"^{commit}"); err != nil {
		return refuse(protocol.RescueTreeMissingBase)
	}
	verified, err := isolatedGit(ctx, r, req.Clone, "bundle", "verify", scratchName)
	prereqs := bundlePrerequisites(string(verified.Stdout) + string(verified.Stderr))
	if err != nil || len(prereqs) != 1 || !protocol.ValidGitObject(prereqs[0]) {
		return refuse(protocol.RescueTreeMismatch)
	}
	prereq := prereqs[0]
	switch ancestor, missing := commitAncestor(ctx, r, req.Clone, prereq, req.Base); {
	case missing:
		return refuse(protocol.RescueTreeIncomplete)
	case !ancestor:
		return refuse(protocol.RescueTreeMismatch)
	}
	heads, err := isolatedGit(ctx, r, req.Clone, "bundle", "list-heads", scratchName)
	if err != nil || !exactRescueHead(string(heads.Stdout), req.Commit, req.Ref) {
		return refuse(protocol.RescueTreeMismatch)
	}
	quarantine := "refs/rescue-read/" + randomRef()
	fetched := false
	defer func() {
		if fetched {
			_, _ = isolatedGit(context.WithoutCancel(ctx), r, req.Clone, "update-ref", "-d", quarantine)
		}
	}()
	if _, err := isolatedGit(ctx, r, req.Clone, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-gc", scratchName, req.Ref+":"+quarantine); err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	fetched = true
	got, err := isolatedGit(ctx, r, req.Clone, "rev-parse", "--verify", quarantine+"^{commit}")
	if err != nil || strings.TrimSpace(string(got.Stdout)) != req.Commit {
		return refuse(protocol.RescueTreeMismatch)
	}
	commitBody, err := isolatedGit(ctx, r, req.Clone, "cat-file", "commit", req.Commit)
	if err != nil || objectHash("commit", commitBody.Stdout, len(req.Commit)) != req.Commit {
		return refuse(protocol.RescueTreeMismatch)
	}
	treeOID, ok := commitTree(commitBody.Stdout)
	if !ok || !protocol.ValidGitObject(treeOID) {
		return refuse(protocol.RescueTreeMismatch)
	}
	treeCheck, err := isolatedGit(ctx, r, req.Clone, "rev-parse", "--verify", req.Commit+"^{tree}")
	if err != nil || strings.TrimSpace(string(treeCheck.Stdout)) != treeOID {
		return refuse(protocol.RescueTreeMismatch)
	}
	if code := verifyTreeClosure(ctx, r, req.Clone, treeOID); code != "" {
		return refuse(code)
	}
	diff, err := isolatedGitMax(ctx, r, req.Clone, rescueTreeResponseCap+1, "diff-tree", "-r", "-z", "--no-renames", "--no-commit-id", prereq, req.Commit)
	if diff.StdoutOver || len(diff.Stdout) > rescueTreeResponseCap {
		return refuse(protocol.TreeTooLarge)
	}
	if err != nil {
		return refuse(protocol.TreeCaptureFailed)
	}
	delta, regular := parseDelta(diff.Stdout)
	if !regular {
		return refuse(protocol.TreeNotRegular)
	}
	carried, err := carriedObjects(ctx, r, req.Clone, req.Commit, req.Base)
	if err != nil {
		if errors.Is(err, errOutputCapped) {
			return refuse(protocol.TreeTooLarge)
		}
		return refuse(protocol.RescueTreeIncomplete)
	}
	var entries []treeEntry
	var blobBytes int64
	paths := make([]string, 0, len(delta))
	for i := range delta {
		paths = append(paths, delta[i].Path)
		if delta[i].Change == "deleted" {
			continue
		}
		if _, ok := carried[delta[i].OID]; ok {
			size, err := blobSize(ctx, r, req.Clone, delta[i].OID)
			if err != nil || size < 0 {
				return refuse(protocol.RescueTreeIncomplete)
			}
			delta[i].Carried = true
			blobBytes += size
			entries = append(entries, treeEntry{mode: delta[i].Mode, oid: delta[i].OID, name: delta[i].Path, size: size})
			continue
		}
		if _, err := isolatedGit(ctx, r, req.Clone, "cat-file", "-e", delta[i].OID); err != nil {
			return refuse(protocol.RescueTreeIncomplete)
		}
	}
	if carriedExceedsCap(paths, blobBytes) {
		return refuse(protocol.TreeTooLarge)
	}
	files, err := readBlobsWithStdin(ctx, r, req.Clone, entries, blobBytes)
	if err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	return protocol.RescueTree{
		OK: true, ID: req.ID, Repo: req.Repo, Commit: req.Commit, Ref: req.Ref,
		Prerequisite: prereq, Delta: delta, Files: files,
	}, nil
}

func isolatedGit(ctx context.Context, r Runner, repo string, args ...string) (Result, error) {
	return isolatedGitMax(ctx, r, repo, 0, args...)
}

func isolatedGitMax(ctx context.Context, r Runner, repo string, stdoutMax int, args ...string) (Result, error) {
	full := append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=", "-c", "core.alternateRefsCommand=", "-c", "diff.renames=false"}, args...)
	return r.Run(ctx, Cmd{Name: "git", Args: full, Env: isolatedGitEnv(), UnsetEnv: isolatedGitUnset, StdoutMax: stdoutMax})
}

var (
	errBundleLarge  = errors.New("bundle exceeds the response cap")
	errOutputCapped = errors.New("git output exceeded the response cap")
)

func readBundleOnce(shared, rel string) ([]byte, error) {
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "..") || path.IsAbs(rel) {
		return nil, errors.New("path")
	}
	parts := strings.Split(rel, "/")
	fd, err := syscall.Open(shared, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			_ = syscall.Close(fd)
			return nil, errors.New("path")
		}
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		_ = syscall.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Size < 0 || st.Size > rescueTreeResponseCap {
		_ = syscall.Close(fd)
		return nil, errBundleLarge
	}
	f := os.NewFile(uintptr(fd), "bundle")
	defer func() { _ = f.Close() }()
	buf := make([]byte, st.Size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func acceptableGitPath(p string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.Contains(p, "..") && !strings.ContainsAny(p, "\n\r")
}

func acceptableBundle(p string) bool {
	return p != "" && path.Clean(p) == p && !path.IsAbs(p) && !strings.HasPrefix(p, "..") && !strings.ContainsAny(p, "\n\r\\")
}

// recipientStoreClear refuses a clone whose object store is not its own
// before any Git command runs. An on-disk alternate, a redirected objects
// directory, or a repo-config command would otherwise supply substitute
// history. A partial clone's promisor remote stays; GIT_NO_LAZY_FETCH keeps
// it from fetching.
func recipientStoreClear(clone string) error {
	gitDir, err := resolveGitDir(clone)
	if err != nil {
		return err
	}
	if err := objectStoreClear(gitDir); err != nil {
		return err
	}
	common := filepath.Join(gitDir, "commondir")
	fi, err := os.Lstat(common)
	if errors.Is(err, os.ErrNotExist) {
		return configClear(gitDir)
	}
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return errors.New("common git dir")
	}
	raw, err := os.ReadFile(common)
	if err != nil {
		return err
	}
	other := strings.TrimSpace(string(raw))
	if other == "" {
		return errors.New("common git dir")
	}
	if !filepath.IsAbs(other) {
		other = filepath.Join(gitDir, other)
	}
	other = filepath.Clean(other)
	if err := objectStoreClear(other); err != nil {
		return err
	}
	return configClear(gitDir)
}

func resolveGitDir(clone string) (string, error) {
	gitPath := filepath.Join(clone, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("git dir symlink")
	}
	if fi.IsDir() {
		return gitPath, nil
	}
	if !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.Size() > 4096 {
		return "", errors.New("git dir")
	}
	raw, err := os.ReadFile(gitPath)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(raw))
	rest, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok {
		return "", errors.New("git dir")
	}
	rest = strings.TrimSpace(rest)
	if strings.ContainsAny(rest, "\n\r") {
		return "", errors.New("git dir")
	}
	if !filepath.IsAbs(rest) {
		rest = filepath.Join(clone, rest)
	}
	rest = filepath.Clean(rest)
	target, err := os.Lstat(rest)
	if err != nil || target.Mode()&os.ModeSymlink != 0 || !target.IsDir() {
		return "", errors.New("git dir")
	}
	return rest, nil
}

func objectStoreClear(gitDir string) error {
	objects := filepath.Join(gitDir, "objects")
	fi, err := os.Lstat(objects)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return errors.New("objects")
	}
	dir, err := os.Open(objects)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(260)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 258 {
		return errors.New("objects")
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return errors.New("objects redirected")
		}
	}
	for _, name := range []string{"info/alternates", "info/http-alternates"} {
		if _, err := os.Lstat(filepath.Join(objects, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("alternates")
		}
	}
	return nil
}

func configClear(gitDir string) error {
	cfg := filepath.Join(gitDir, "config")
	fi, err := os.Lstat(cfg)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return errors.New("config")
	}
	raw, err := os.ReadFile(cfg)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trim := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(trim, "alternaterefscommand") || strings.HasPrefix(trim, "[include") {
			return errors.New("config command")
		}
	}
	return nil
}

func bundlePrerequisites(text string) []string {
	in := false
	var oids []string
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(trim), "require") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(trim, "The bundle") {
			break
		}
		fields := strings.Fields(trim)
		if len(fields) > 0 && protocol.ValidGitObject(fields[0]) {
			oids = append(oids, fields[0])
		}
	}
	return oids
}

func commitAncestor(ctx context.Context, r Runner, clone, prereq, base string) (ancestor, missing bool) {
	_, err := isolatedGit(ctx, r, clone, "merge-base", "--is-ancestor", prereq, base)
	if err == nil {
		return true, false
	}
	var ce *CmdError
	if errors.As(err, &ce) && ce.ExitCode == 1 {
		return false, false
	}
	return false, true
}

func exactRescueHead(text, commit, ref string) bool {
	found := false
	for _, line := range strings.Split(text, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || sha == "" || name == "" || name != ref {
			continue
		}
		if sha != commit || found {
			return false
		}
		found = true
	}
	return found
}

func verifyTreeClosure(ctx context.Context, r Runner, clone, root string) string {
	seen := map[string]struct{}{}
	var total int
	var walk func(oid string, depth int) string
	walk = func(oid string, depth int) string {
		if _, ok := seen[oid]; ok {
			return ""
		}
		if len(seen) >= 4096 || depth > 32 {
			return protocol.TreeTooLarge
		}
		seen[oid] = struct{}{}
		res, err := isolatedGitMax(ctx, r, clone, rescueTreeResponseCap+1, "cat-file", "tree", oid)
		if res.StdoutOver || len(res.Stdout) > rescueTreeResponseCap || total > rescueTreeResponseCap-len(res.Stdout) {
			return protocol.TreeTooLarge
		}
		if err != nil || objectHash("tree", res.Stdout, len(oid)) != oid {
			return protocol.RescueTreeMismatch
		}
		total += len(res.Stdout)
		entries, ok := parseRawTree(res.Stdout, len(oid))
		if !ok {
			return protocol.RescueTreeMismatch
		}
		for _, e := range entries {
			switch e.mode {
			case "40000", "040000":
				if code := walk(e.oid, depth+1); code != "" {
					return code
				}
			case "100644", "100755":
			default:
				return protocol.TreeNotRegular
			}
		}
		return ""
	}
	return walk(root, 0)
}

type rawTreeEntry struct {
	mode, name, oid string
}

func parseRawTree(body []byte, hexLen int) ([]rawTreeEntry, bool) {
	oidLen := hexLen / 2
	if oidLen != 20 && oidLen != 32 {
		return nil, false
	}
	var out []rawTreeEntry
	for len(body) > 0 {
		sp := bytes.IndexByte(body, ' ')
		if sp <= 0 || sp > 6 {
			return nil, false
		}
		mode := string(body[:sp])
		rest := body[sp+1:]
		nul := bytes.IndexByte(rest, 0)
		if nul <= 0 || len(rest) < nul+1+oidLen {
			return nil, false
		}
		name := string(rest[:nul])
		if !protocol.AcceptableRescueTreePath(name) && name != "" {
			// A tree may contain a single path component. AcceptableRescueTreePath
			// allows a component; a name with a slash is not a raw tree name.
			return nil, false
		}
		if strings.Contains(name, "/") || unsafeTreeName(name) {
			return nil, false
		}
		oid := hex.EncodeToString(rest[nul+1 : nul+1+oidLen])
		body = rest[nul+1+oidLen:]
		out = append(out, rawTreeEntry{mode: mode, name: name, oid: oid})
	}
	return out, true
}

func unsafeTreeName(name string) bool {
	return name == "" || name == "." || name == ".." || strings.Contains(name, "/") || !protocol.AcceptableRescueTreePath(name)
}

func parseDelta(raw []byte) ([]protocol.RescueTreeDelta, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	recs := bytes.Split(raw, []byte{0})
	if len(recs) > 0 && len(recs[len(recs)-1]) == 0 {
		recs = recs[:len(recs)-1]
	}
	if len(recs)%2 != 0 {
		return nil, false
	}
	var out []protocol.RescueTreeDelta
	seen := map[string]bool{}
	for i := 0; i < len(recs); i += 2 {
		rec := recs[i]
		name := string(recs[i+1])
		if len(rec) == 0 || rec[0] != ':' || seen[name] || !protocol.AcceptableRescueTreePath(name) {
			return nil, false
		}
		meta := strings.Fields(string(rec[1:]))
		if len(meta) != 5 {
			return nil, false
		}
		oldMode, newMode, _, newOID, status := meta[0], meta[1], meta[2], meta[3], meta[4]
		seen[name] = true
		switch status {
		case "D":
			out = append(out, protocol.RescueTreeDelta{Path: name, Change: "deleted"})
		case "A", "M", "T":
			if !protocol.RegularTreeMode(newMode) || !protocol.ValidGitObject(newOID) {
				return nil, false
			}
			if oldMode == "120000" || oldMode == "160000" {
				return nil, false
			}
			change := "modified"
			if status == "A" {
				change = "added"
			}
			out = append(out, protocol.RescueTreeDelta{Path: name, Change: change, Mode: newMode, OID: newOID})
		default:
			return nil, false
		}
	}
	return out, true
}

func carriedObjects(ctx context.Context, r Runner, clone, commit, base string) (map[string]struct{}, error) {
	res, err := isolatedGitMax(ctx, r, clone, rescueTreeResponseCap+1, "rev-list", "--objects", "--no-object-names", commit, "--not", base)
	if res.StdoutOver || len(res.Stdout) > rescueTreeResponseCap {
		return nil, errOutputCapped
	}
	if err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		oid := strings.TrimSpace(line)
		if oid == "" {
			continue
		}
		if !protocol.ValidGitObject(oid) {
			return nil, errors.New("object list")
		}
		out[oid] = struct{}{}
	}
	return out, nil
}

func blobSize(ctx context.Context, r Runner, clone, oid string) (int64, error) {
	res, err := isolatedGitMax(ctx, r, clone, 64, "cat-file", "-s", oid)
	if err != nil || res.StdoutOver {
		return 0, errors.New("size")
	}
	return strconvParse(strings.TrimSpace(string(res.Stdout)))
}

func carriedExceedsCap(paths []string, blobBytes int64) bool {
	if blobBytes < 0 {
		return true
	}
	var overhead int64 = 1024
	for _, p := range paths {
		overhead += int64(len(p)) + 256
	}
	if overhead < 0 || overhead > rescueTreeResponseCap {
		return true
	}
	encoded := (blobBytes*4 + 2) / 3
	if blobBytes > 0 && encoded < blobBytes {
		return true
	}
	return encoded+overhead > rescueTreeResponseCap
}

func commitTree(body []byte) (string, bool) {
	line, _, _ := bytes.Cut(body, []byte("\n"))
	oid, ok := bytes.CutPrefix(line, []byte("tree "))
	if !ok || !protocol.ValidGitObject(string(oid)) {
		return "", false
	}
	return string(oid), true
}

func objectHash(kind string, content []byte, hexLen int) string {
	var h hash.Hash
	if hexLen == 64 {
		h = sha256.New()
	} else {
		h = sha1.New()
	}
	_, _ = fmt.Fprintf(h, "%s %d\x00", kind, len(content))
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

type treeEntry struct {
	mode, oid, name string
	size            int64
}

func strconvParse(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("size")
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("size")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

func readBlobsWithStdin(ctx context.Context, r Runner, clone string, entries []treeEntry, expect int64) ([]protocol.RescueTreeFile, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	var stdin bytes.Buffer
	for _, e := range entries {
		stdin.WriteString(e.oid)
		stdin.WriteByte('\n')
	}
	limit := expect + int64(len(entries))*128 + 1
	if limit <= 0 || limit > rescueTreeResponseCap+int64(len(entries))*128+1 {
		return nil, errors.New("large")
	}
	full := append([]string{"-C", clone, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=", "-c", "core.alternateRefsCommand="}, "cat-file", "--batch")
	res, err := r.Run(ctx, Cmd{Name: "git", Args: full, Env: isolatedGitEnv(), UnsetEnv: isolatedGitUnset, Stdin: &stdin, StdoutMax: int(limit)})
	if res.StdoutOver || err != nil {
		return nil, errors.New("batch")
	}
	rest := res.Stdout
	files := make([]protocol.RescueTreeFile, 0, len(entries))
	var used int64
	for _, e := range entries {
		line, after, ok := bytes.Cut(rest, []byte("\n"))
		if !ok {
			return nil, errors.New("short")
		}
		fields := strings.Fields(string(line))
		if len(fields) != 3 || fields[0] != e.oid || fields[1] != "blob" || fields[2] != fmt.Sprint(e.size) {
			return nil, errors.New("header")
		}
		if int64(len(after)) < e.size+1 || used > rescueTreeResponseCap-e.size {
			return nil, errors.New("body")
		}
		body := after[:e.size]
		if after[e.size] != '\n' {
			return nil, errors.New("frame")
		}
		rest = after[e.size+1:]
		if objectHash("blob", body, len(e.oid)) != e.oid {
			return nil, errors.New("oid")
		}
		sum := sha256.Sum256(body)
		files = append(files, protocol.RescueTreeFile{
			Path: e.name, Mode: e.mode, SHA256: hex.EncodeToString(sum[:]), Bytes: append([]byte(nil), body...),
		})
		used += e.size
	}
	if len(rest) != 0 {
		return nil, errors.New("tail")
	}
	return files, nil
}

func randomRef() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "pending"
	}
	return hex.EncodeToString(b[:])
}
