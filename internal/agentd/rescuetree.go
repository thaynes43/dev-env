package agentd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// rescueTreeResponseCap is the existing shelf exec stdout cap (maxOutput) and
// the existing agent-run response cap (maxResponse). It is not a new limit.
const rescueTreeResponseCap = 8 << 20

// isolatedGitUnset drops object-lookup settings that could point Git at
// another object store or turn replace refs and lazy fetch on.
var isolatedGitUnset = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE", "GIT_NO_REPLACE_OBJECTS", "GIT_NO_LAZY_FETCH",
	"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
}

func isolatedGitEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
	}
}

// classifyRescueTree records why a rescue-branch tree was not stored.
// It reads listing metadata only. It does not write a tar, does not read
// blob bodies, and does not fail the thin bundle: every outcome is a code.
func classifyRescueTree(ctx context.Context, r Runner, repo string, refs []protocol.BundleRef) string {
	var tips []protocol.BundleRef
	for _, ref := range refs {
		if protocol.RescueWorkRef(ref.Name) && protocol.ValidGitObject(ref.Commit) {
			tips = append(tips, ref)
		}
	}
	if len(tips) == 0 {
		return ""
	}
	code := protocol.TreeWithheld
	for _, ref := range tips {
		next, err := classifyOneTree(ctx, r, repo, ref.Commit)
		if err != nil {
			return protocol.TreeCaptureFailed
		}
		if next == protocol.TreeNotRegular || next == protocol.TreeTooLarge {
			return next
		}
		if next != "" {
			code = next
		}
	}
	return code
}

func classifyOneTree(ctx context.Context, r Runner, repo, commit string) (string, error) {
	bctx, cancel := context.WithTimeout(ctx, bundleTimeout)
	defer cancel()
	res, err := r.Run(bctx, Cmd{
		Name: "git",
		Args: []string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=", "-c", "core.alternateRefsCommand=", "ls-tree", "-r", "-l", "-z", commit},
		Env:  isolatedGitEnv(), UnsetEnv: isolatedGitUnset,
		StdoutMax: rescueTreeResponseCap + 1,
	})
	if res.StdoutOver || len(res.Stdout) > rescueTreeResponseCap {
		return protocol.TreeTooLarge, nil
	}
	if err != nil {
		return "", err
	}
	entries, regular := parseTreeSizes(res.Stdout)
	if !regular {
		return protocol.TreeNotRegular, nil
	}
	var blobs int64
	for _, e := range entries {
		blobs += e.size
	}
	if encodedTreeExceedsCap(blobs, len(entries)) {
		return protocol.TreeTooLarge, nil
	}
	return protocol.TreeWithheld, nil
}

type treeSize struct {
	size int64
}

func parseTreeSizes(raw []byte) ([]treeSize, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var entries []treeSize
	seen := map[string]bool{}
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
		n, err := strconv.ParseInt(meta[3], 10, 64)
		if err != nil || n < 0 {
			return nil, false
		}
		seen[name] = true
		entries = append(entries, treeSize{size: n})
	}
	return entries, true
}

// encodedTreeExceedsCap reports whether blob bytes plus the existing base64
// expansion would exceed rescueTreeResponseCap. It does not allocate them.
func encodedTreeExceedsCap(blobBytes int64, files int) bool {
	if blobBytes < 0 || files < 0 {
		return true
	}
	overhead := int64(files)*512 + 1024
	encoded := (blobBytes*4 + 2) / 3
	if blobBytes > 0 && encoded < blobBytes {
		return true
	}
	return encoded+overhead > rescueTreeResponseCap
}

// ReadRescueTree checks that a rescue directory stays inside the shared
// volume. It does not return file bytes. A tar next to the bundle is not
// proof of the commit, and this path does not serve one.
func ReadRescueTree(_ context.Context, _ Runner, s Settings, id, repo, commit string) (protocol.RescueTree, error) {
	refuse := func(code string) (protocol.RescueTree, error) {
		return protocol.RescueTree{Code: code}, nil
	}
	if _, _, err := protocol.ParseRescueID(id); err != nil || !protocol.ValidGitObject(commit) || repo == "" || strings.Contains(repo, "/") || repo == ".." || repo == "." {
		return refuse(protocol.RescueTreeMismatch)
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return protocol.RescueTree{}, err
	}
	dir, err := protocol.RescueDirOf(id)
	if err != nil {
		return refuse(protocol.RescueTreeMismatch)
	}
	if err := rescueDirInside(s.SharedDir, dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return refuse(protocol.RescueTreeNotFound)
		}
		return refuse(protocol.RescueTreeRefused)
	}
	return refuse(protocol.TreeWithheld)
}

func rescueDirInside(shared, rel string) error {
	if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "..") || path.IsAbs(rel) {
		return errors.New("path")
	}
	root, err := os.OpenRoot(shared)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	cur := "."
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("path")
		}
		cur = path.Join(cur, part)
		fi, err := root.Lstat(cur)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return errors.New("path")
		}
	}
	return nil
}
