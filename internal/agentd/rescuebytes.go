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
// Commit and Base come from the operator's rescue status, not from the
// bundle file. Bundle is relative to the shared volume.
type RescueMaterialize struct {
	ID, Repo, Clone, Bundle, Commit, Base string
}

// MaterializeRescueTree verifies the existing thin bundle against the
// recipient clone and returns that commit's regular files. It does not
// check out, run hooks, or contact a remote. A missing base or a tree that
// would exceed the existing 8 MiB response cap refuses before file bodies
// are allocated. The thin bundle on the shared volume is not rewritten.
func MaterializeRescueTree(ctx context.Context, r Runner, s Settings, req RescueMaterialize) (protocol.RescueTree, error) {
	refuse := func(code string) (protocol.RescueTree, error) {
		return protocol.RescueTree{Code: code, ID: req.ID, Repo: req.Repo, Commit: req.Commit}, nil
	}
	if !protocol.ValidGitObject(req.Commit) || !protocol.ValidGitObject(req.Base) || req.Commit == req.Base || !acceptableGitPath(req.Clone) || !acceptableBundle(req.Bundle) {
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
	if err != nil || !bundleRequiresCommit(string(verified.Stdout)+string(verified.Stderr), req.Base) {
		return refuse(protocol.RescueTreeMismatch)
	}
	heads, err := isolatedGit(ctx, r, req.Clone, "bundle", "list-heads", scratchName)
	if err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	refName, ok := rescueHead(string(heads.Stdout), req.Commit)
	if !ok {
		return refuse(protocol.RescueTreeMismatch)
	}
	quarantine := "refs/rescue-read/" + randomRef()
	fetched := false
	defer func() {
		if fetched {
			_, _ = isolatedGit(context.WithoutCancel(ctx), r, req.Clone, "update-ref", "-d", quarantine)
		}
	}()
	if _, err := isolatedGit(ctx, r, req.Clone, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-gc", scratchName, refName+":"+quarantine); err != nil {
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
	listed, err := isolatedGit(ctx, r, req.Clone, "ls-tree", "-r", "-l", "-z", req.Commit)
	if err != nil {
		return refuse(protocol.TreeCaptureFailed)
	}
	if len(listed.Stdout) > rescueTreeResponseCap {
		return refuse(protocol.TreeTooLarge)
	}
	entries, regular := parseTreeEntries(listed.Stdout)
	if !regular {
		return refuse(protocol.TreeNotRegular)
	}
	var blobs int64
	for _, e := range entries {
		blobs += e.size
	}
	if encodedTreeExceedsCap(blobs, len(entries)) {
		return refuse(protocol.TreeTooLarge)
	}
	files, err := readBlobsWithStdin(ctx, r, req.Clone, entries, blobs)
	if err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	return protocol.RescueTree{OK: true, ID: req.ID, Repo: req.Repo, Commit: req.Commit, Ref: refName, Files: files}, nil
}

func isolatedGit(ctx context.Context, r Runner, repo string, args ...string) (Result, error) {
	full := append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor="}, args...)
	return r.Run(ctx, Cmd{Name: "git", Args: full, Env: isolatedGitEnv(), UnsetEnv: isolatedGitUnset})
}

var errBundleLarge = errors.New("bundle exceeds the response cap")

func readBundleOnce(shared, rel string) ([]byte, error) {
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "..") || path.IsAbs(rel) {
		return nil, errors.New("path")
	}
	root, err := os.OpenRoot(shared)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	cur := "."
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return nil, errors.New("path")
		}
		cur = path.Join(cur, part)
		fi, err := root.Lstat(cur)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return nil, errors.New("path")
		}
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() < 0 || st.Size() > rescueTreeResponseCap || !st.Mode().IsRegular() {
		return nil, errBundleLarge
	}
	buf := make([]byte, st.Size())
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

func bundleRequiresCommit(text, base string) bool {
	in := false
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
		if len(fields) > 0 && fields[0] == base {
			return true
		}
	}
	return false
}

func rescueHead(text, commit string) (string, bool) {
	var found string
	for _, line := range strings.Split(text, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || sha != commit || !protocol.RescueWorkRef(name) {
			continue
		}
		if found != "" {
			return "", false
		}
		found = name
	}
	return found, found != ""
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

func parseTreeEntries(raw []byte) ([]treeEntry, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var entries []treeEntry
	seen := map[string]bool{}
	var total int64
	for _, rec := range bytes.Split(raw, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, false
		}
		meta := strings.Fields(string(rec[:tab]))
		name := string(rec[tab+1:])
		if len(meta) != 4 || meta[1] != "blob" || !protocol.ValidGitObject(meta[2]) || !protocol.RegularTreeMode(meta[0]) || !protocol.AcceptableRescueTreePath(name) || seen[name] {
			return nil, false
		}
		n, err := strconvParse(meta[3])
		if err != nil || n < 0 || total > (1<<62)-n {
			return nil, false
		}
		total += n
		seen[name] = true
		entries = append(entries, treeEntry{mode: meta[0], oid: meta[2], name: name, size: n})
	}
	return entries, true
}

func strconvParse(s string) (int64, error) {
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
	full := append([]string{"-C", clone, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor="}, "cat-file", "--batch")
	res, err := r.Run(ctx, Cmd{Name: "git", Args: full, Env: isolatedGitEnv(), UnsetEnv: isolatedGitUnset, Stdin: &stdin})
	if err != nil {
		return nil, err
	}
	if int64(len(res.Stdout)) > expect+int64(len(entries))*128 {
		return nil, errors.New("large")
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
