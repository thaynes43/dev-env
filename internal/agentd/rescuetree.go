package agentd

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// writeRescueTreeArchives records a tar of each rescue-branch tree beside the
// thin bundle. The thin bundle omits objects reachable from origin, so a later
// read on the shelf cannot rebuild those trees from the bundle alone. The tar
// is the one tree `git add -A` committed, including unchanged tracked files
// and excluding ignored files. A symlink or gitlink omits the tar and sets
// TreeRefused; it does not fail the thin bundle.
func writeRescueTreeArchives(ctx context.Context, r Runner, repo, absDir, relDir string, refs []protocol.BundleRef) ([]protocol.TreeArchive, string, error) {
	var tips []protocol.BundleRef
	for _, ref := range refs {
		if protocol.RescueWorkRef(ref.Name) && protocol.ValidGitObject(ref.Commit) {
			tips = append(tips, ref)
		}
	}
	if len(tips) == 0 {
		return nil, "", nil
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i].Commit < tips[j].Commit })
	type built struct {
		name string
		body []byte
		ref  protocol.BundleRef
	}
	var builtTrees []built
	for _, ref := range tips {
		files, regular, err := rescueTreeFiles(ctx, r, repo, ref.Commit)
		if err != nil {
			return nil, "", err
		}
		if !regular {
			return nil, protocol.TreeNotRegular, nil
		}
		body, err := tarTree(files)
		if err != nil {
			return nil, "", err
		}
		builtTrees = append(builtTrees, built{
			name: filepath.Base(repo) + "-" + ref.Commit + ".tree.tar",
			body: body,
			ref:  ref,
		})
	}
	var archives []protocol.TreeArchive
	for _, tree := range builtTrees {
		if strings.Contains(tree.name, "/") || !protocol.AcceptableRescueTreePath(tree.name) {
			return nil, "", errors.New("rescue tree name is not a single file")
		}
		dst := filepath.Join(absDir, tree.name)
		sum, size, err := writeDurable(dst, bytes.NewReader(tree.body))
		if err != nil {
			return nil, "", err
		}
		back, backSize, err := hashFile(dst)
		if err != nil || back != sum || backSize != size {
			return nil, "", errors.New("rescue tree read-back did not match")
		}
		archives = append(archives, protocol.TreeArchive{
			Ref: tree.ref.Name, Commit: tree.ref.Commit,
			File: path.Join(relDir, tree.name), SHA256: sum, Size: size,
		})
	}
	return archives, "", nil
}

type treeFile struct {
	path string
	mode string
	body []byte
}

func rescueTreeFiles(ctx context.Context, r Runner, repo, commit string) ([]treeFile, bool, error) {
	res, err := r.Run(ctx, Cmd{
		Name: "git",
		Args: []string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "ls-tree", "-r", "-z", commit},
		Env:  gitEnv,
	})
	if err != nil {
		return nil, false, err
	}
	entries, regular, err := parseTreeListing(res.Stdout)
	if err != nil || !regular {
		return nil, regular, err
	}
	if len(entries) == 0 {
		return nil, true, nil
	}
	var stdin bytes.Buffer
	for _, e := range entries {
		stdin.WriteString(e.oid)
		stdin.WriteByte('\n')
	}
	got, err := r.Run(ctx, Cmd{
		Name: "git",
		Args: []string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "cat-file", "--batch"},
		Env:  gitEnv, Stdin: &stdin,
	})
	if err != nil {
		return nil, false, err
	}
	bodies, err := parseCatFileBatch(got.Stdout, entries)
	if err != nil {
		return nil, false, err
	}
	files := make([]treeFile, len(entries))
	for i, e := range entries {
		files[i] = treeFile{path: e.path, mode: e.mode, body: bodies[i]}
	}
	return files, true, nil
}

type treeEntry struct {
	mode, oid, path string
}

func parseTreeListing(raw []byte) ([]treeEntry, bool, error) {
	if len(raw) == 0 {
		return nil, true, nil
	}
	var entries []treeEntry
	seen := map[string]bool{}
	for _, rec := range bytes.Split(raw, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, false, errors.New("rescue tree listing is not a git record")
		}
		meta := strings.Fields(string(rec[:tab]))
		name := string(rec[tab+1:])
		if len(meta) != 3 || meta[1] != "blob" || !protocol.ValidGitObject(meta[2]) {
			return nil, false, nil
		}
		if !protocol.RegularTreeMode(meta[0]) || !protocol.AcceptableRescueTreePath(name) || seen[name] {
			return nil, false, nil
		}
		seen[name] = true
		entries = append(entries, treeEntry{mode: meta[0], oid: meta[2], path: name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	return entries, true, nil
}

func parseCatFileBatch(raw []byte, entries []treeEntry) ([][]byte, error) {
	out := make([][]byte, len(entries))
	rest := raw
	for i, e := range entries {
		line, after, ok := bytes.Cut(rest, []byte{'\n'})
		if !ok {
			return nil, errors.New("rescue tree object is missing")
		}
		fields := strings.Fields(string(line))
		if len(fields) != 3 || fields[0] != e.oid || fields[1] != "blob" {
			return nil, errors.New("rescue tree object is missing")
		}
		n, err := strconv.Atoi(fields[2])
		if err != nil || n < 0 || n > len(after) {
			return nil, errors.New("rescue tree object is missing")
		}
		body := after[:n]
		rest = after[n:]
		if len(rest) < 1 || rest[0] != '\n' {
			return nil, errors.New("rescue tree object is missing")
		}
		rest = rest[1:]
		out[i] = append([]byte(nil), body...)
	}
	if len(bytes.TrimRight(rest, "\n")) != 0 {
		return nil, errors.New("rescue tree object is missing")
	}
	return out, nil
}

func tarTree(files []treeFile) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		mode := int64(0o644)
		if f.mode == "100755" {
			mode = 0o755
		}
		hdr := &tar.Header{
			Name: f.path, Mode: mode, Size: int64(len(f.body)),
			ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX, Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadRescueTree reads one rescue-branch tree from the shared volume. It does
// not create a session, fetch origin, or change the rescue. id is
// <session>/<stamp>. repo is the clone's directory name. commit must be the
// rescue-branch tip recorded in the thin bundle and in the tree archive.
func ReadRescueTree(ctx context.Context, r Runner, s Settings, id, repo, commit string) (protocol.RescueTree, error) {
	refuse := func(code string) (protocol.RescueTree, error) {
		return protocol.RescueTree{Code: code}, nil
	}
	session, name, err := protocol.ParseRescueID(id)
	if err != nil || !protocol.ValidGitObject(commit) || repo == "" || strings.Contains(repo, "/") || repo == ".." || repo == "." {
		return refuse(protocol.RescueTreeMismatch)
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return protocol.RescueTree{}, err
	}
	dir, err := protocol.RescueDirOf(id)
	if err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	abs, err := containedDir(s.SharedDir, dir)
	if errors.Is(err, os.ErrNotExist) {
		return refuse(protocol.RescueTreeNotFound)
	}
	if err != nil {
		return refuse(protocol.RescueTreeRefused)
	}
	m, err := readManifest(filepath.Join(abs, protocol.ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return refuse(protocol.RescueTreeNotFound)
	}
	if err != nil {
		return refuse(protocol.RescueTreeIncomplete)
	}
	if !m.Complete || m.Session != session || m.Stamp != name || m.Dir != dir {
		return refuse(protocol.RescueTreeIncomplete)
	}
	var found *protocol.ManifestRepo
	for i := range m.Repos {
		if path.Base(m.Repos[i].Path) == repo {
			found = &m.Repos[i]
			break
		}
	}
	if found == nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	if found.Bundle.TreeRefused != "" {
		return refuse(protocol.RescueTreeRefused)
	}
	b := found.Bundle
	bundleAbs, err := containedFile(abs, b.File, dir)
	if err != nil {
		return refuse(protocol.RescueTreeRefused)
	}
	if err := checkBundleFile(bundleAbs, b.Size, b.SHA256); err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	heads, err := bundleHeads(ctx, r, bundleAbs)
	if err != nil {
		return protocol.RescueTree{}, err
	}
	ref := ""
	for name, oid := range heads {
		if oid == commit && protocol.RescueWorkRef(name) {
			if ref != "" {
				return refuse(protocol.RescueTreeRefused)
			}
			ref = name
		}
	}
	if ref == "" {
		return refuse(protocol.RescueTreeMismatch)
	}
	var archive *protocol.TreeArchive
	for i := range b.TreeArchives {
		a := &b.TreeArchives[i]
		if a.Commit == commit && a.Ref == ref {
			if archive != nil {
				return refuse(protocol.RescueTreeRefused)
			}
			archive = a
		}
	}
	if archive == nil || archive.File == "" || !protocol.ValidSHA256(archive.SHA256) {
		return refuse(protocol.RescueTreeIncomplete)
	}
	tarAbs, err := containedFile(abs, archive.File, dir)
	if err != nil {
		return refuse(protocol.RescueTreeRefused)
	}
	if err := checkBundleFile(tarAbs, archive.Size, archive.SHA256); err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	files, err := filesFromTar(tarAbs)
	if err != nil {
		return refuse(protocol.RescueTreeRefused)
	}
	out := make([]protocol.RescueTreeFile, len(files))
	for i, f := range files {
		sum := sha256.Sum256(f.body)
		out[i] = protocol.RescueTreeFile{Path: f.path, Mode: f.mode, SHA256: hex.EncodeToString(sum[:]), Bytes: f.body}
	}
	return protocol.RescueTree{OK: true, ID: id, Repo: repo, Commit: commit, Ref: ref, Files: out}, nil
}

func bundleHeads(ctx context.Context, r Runner, file string) (map[string]string, error) {
	res, err := r.Run(ctx, Cmd{
		Name:     "git",
		Args:     []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "bundle", "list-heads", file},
		Env:      []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1"},
		UnsetEnv: []string{"GIT_DIR", "GIT_WORK_TREE"},
	})
	if err != nil {
		return nil, err
	}
	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		if line == "" {
			continue
		}
		oid, name, ok := strings.Cut(line, " ")
		if !ok || !protocol.ValidGitObject(oid) || !strings.HasPrefix(name, "refs/") || strings.ContainsAny(name, " \t") {
			return nil, errors.New("bundle heads are not a ref list")
		}
		if _, dup := heads[name]; dup {
			return nil, errors.New("bundle heads repeat a ref")
		}
		heads[name] = oid
	}
	return heads, nil
}

func containedDir(shared, rel string) (string, error) {
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "..") {
		return "", errors.New("path")
	}
	abs := filepath.Join(shared, filepath.FromSlash(rel))
	parent := filepath.Join(shared, filepath.FromSlash(path.Dir(rel)))
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path")
	}
	got, err := filepath.Rel(parent, abs)
	if err != nil || got != filepath.Base(abs) || strings.HasPrefix(got, "..") {
		return "", errors.New("path")
	}
	return abs, nil
}

func containedFile(root, rel, dir string) (string, error) {
	if rel == "" || path.Clean(rel) != rel || path.Dir(rel) != dir {
		return "", errors.New("path")
	}
	abs := filepath.Join(root, path.Base(rel))
	got, err := filepath.Rel(root, abs)
	if err != nil || got != filepath.Base(rel) || strings.Contains(got, "..") || strings.Contains(got, string(filepath.Separator)+"..") {
		return "", errors.New("path")
	}
	return abs, nil
}

func filesFromTar(p string) ([]treeFile, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(f)
	var files []treeFile
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, errors.New("not regular")
		}
		mode := "100644"
		perm := hdr.Mode & 0o777
		switch perm {
		case 0o644:
		case 0o755:
			mode = "100755"
		default:
			return nil, errors.New("mode")
		}
		if !protocol.AcceptableRescueTreePath(hdr.Name) || seen[hdr.Name] || hdr.Size < 0 {
			return nil, errors.New("path")
		}
		body, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil || int64(len(body)) != hdr.Size {
			return nil, errors.New("short")
		}
		seen[hdr.Name] = true
		files = append(files, treeFile{path: hdr.Name, mode: mode, body: body})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}
