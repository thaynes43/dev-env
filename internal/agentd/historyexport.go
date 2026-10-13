package agentd

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The history export (DESIGN-025 6.3): `agentd ctl history-export plan|stream`,
// run by exec in a private Session's hold pod, or in its session pod, over the
// private HOME. It reads HOME only. Every file and directory is opened with
// O_RDONLY|O_NOFOLLOW (TestHistoryExportReadsOnly), and nothing under HOME is
// created, changed or removed.

// HistoryExportOptions are what the operator knows about the executor: its
// provider directories relative to HOME, from the Session's terminal record,
// and the Session's name. Rescue optionally names the Git rescue on the shared
// volume, <session>/<stamp>, to list beside the history.
type HistoryExportOptions struct {
	ClaudeConfigDir string
	CodexHome       string
	Session         string
	Rescue          string
}

// historyBounds are DESIGN-025 6.3's bounds, the protocol's constants; tests
// lower them rather than write gigabytes.
var historyBounds = struct {
	fileBytes, totalBytes int64
	files, excluded       int
}{protocol.MaxHistoryFileBytes, protocol.MaxHistoryExportBytes, protocol.MaxHistoryExportFiles, protocol.MaxHistoryExcluded}

// historyOpenFlags opens a file of HOME: read-only, never through a link, and
// never blocking on a FIFO that raced into its place.
const historyOpenFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC

// historyDirFlags opens a directory of HOME the same way.
const historyDirFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_DIRECTORY | syscall.O_CLOEXEC

// historyRootDef is one root of an export, resolved.
type historyRootDef struct {
	name  string
	rel   string // relative to HOME, slash-separated
	abs   string
	rules []historyRule // nil for the log root
	// file is set for the log root: the one file it exports.
	file string
}

// PlanHistoryExport walks the roots and returns the plan: every file it would
// export, hashed, and every name it excluded, with the reason. A refusal is a
// *protocol.HistoryError.
func PlanHistoryExport(s Settings, o HistoryExportOptions) (protocol.HistoryExportPlan, error) {
	roots, err := historyRoots(s, o)
	if err != nil {
		return protocol.HistoryExportPlan{}, err
	}
	creds, err := credentialDigests(s, roots)
	if err != nil {
		return protocol.HistoryExportPlan{}, err
	}
	w := &historyWalk{creds: creds}
	plan := protocol.HistoryExportPlan{
		Format:   protocol.HistoryExportPlanFormat,
		Session:  o.Session,
		Files:    []protocol.HistoryFile{},
		Excluded: []protocol.HistoryExcluded{},
	}
	for _, r := range roots {
		root := protocol.HistoryRoot{Name: r.name, Dir: r.rel}
		var absent bool
		if r.file != "" {
			absent, err = w.logRoot(r)
		} else {
			absent, err = w.dirRoot(r)
		}
		if err != nil {
			return protocol.HistoryExportPlan{}, err
		}
		root.Absent = absent
		plan.Roots = append(plan.Roots, root)
	}
	plan.Files = w.files
	plan.Excluded = w.excluded
	plan.Bytes = w.bytes
	protocol.SortHistoryFiles(plan.Files)
	sort.Slice(plan.Excluded, func(i, j int) bool {
		a, b := plan.Excluded[i], plan.Excluded[j]
		if a.Root != b.Root {
			return a.Root < b.Root
		}
		return a.Path < b.Path
	})
	if o.Rescue != "" {
		g, err := planRescue(s, o)
		if err != nil {
			return protocol.HistoryExportPlan{}, err
		}
		plan.Git = g
	}
	return plan, nil
}

// HistoryPlanSHA256 is the digest `stream --plan-sha256` compares: the
// SHA-256 of the plan's canonical JSON, the bytes `plan` prints.
func HistoryPlanSHA256(plan protocol.HistoryExportPlan) (string, []byte, error) {
	return protocol.CanonicalSHA256(plan)
}

// StreamHistoryExport plans again, refuses export_changed unless the new
// plan's digest is planSHA256, then writes exactly the planned files to w as a
// tar stream, hashing each again as it reads it. A file that differs from the
// plan stops the stream with export_changed; put refuses what it received.
func StreamHistoryExport(s Settings, o HistoryExportOptions, planSHA256 string, w io.Writer) error {
	plan, err := PlanHistoryExport(s, o)
	if err != nil {
		return err
	}
	got, _, err := HistoryPlanSHA256(plan)
	if err != nil {
		return err
	}
	if got != planSHA256 {
		return protocol.HistoryErrorf(protocol.HistoryErrChanged, "", "the tree changed since the plan: its digest is now %s", got)
	}
	roots := map[string]string{}
	for _, r := range plan.Roots {
		roots[r.Name] = filepath.Join(s.Home, filepath.FromSlash(r.Dir))
	}
	tw := tar.NewWriter(w)
	for _, f := range plan.Files {
		abs := filepath.Join(roots[f.Root], filepath.FromSlash(f.Path))
		if f.Root == protocol.HistoryRootLog {
			abs = roots[f.Root]
		}
		if err := streamHistoryFile(tw, abs, f); err != nil {
			return err
		}
	}
	return tw.Close()
}

func streamHistoryFile(tw *tar.Writer, abs string, f protocol.HistoryFile) error {
	file, fi, err := openHistoryFile(abs)
	if err != nil {
		return protocol.HistoryErrorf(protocol.HistoryErrChanged, f.Name(), "%v", err)
	}
	defer func() { _ = file.Close() }()
	if fi.Size() != f.Bytes || !fi.ModTime().Equal(f.ModTime) {
		return protocol.HistoryErrorf(protocol.HistoryErrChanged, f.Name(), "the file changed since the plan")
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     f.Name(),
		Size:     f.Bytes,
		Mode:     0o600,
		ModTime:  f.ModTime,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	bh := newBlockHasher()
	n, err := io.Copy(io.MultiWriter(tw, bh), io.LimitReader(file, f.Bytes))
	if err != nil {
		return err
	}
	sum, blocks := bh.finish()
	if n != f.Bytes || sum != f.SHA256 || protocol.HistoryBlocksSHA256(blocks) != f.BlocksSHA256 {
		return protocol.HistoryErrorf(protocol.HistoryErrChanged, f.Name(), "the file's bytes changed since the plan")
	}
	return nil
}

// historyRoots resolves the four roots: the provider directories the
// operator named, agentd's state directory and the Session's log. Each must
// lie strictly inside HOME, through no link, and none inside another.
func historyRoots(s Settings, o HistoryExportOptions) ([]historyRootDef, error) {
	if !filepath.IsAbs(s.Home) || filepath.Clean(s.Home) != s.Home {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, "", "HOME %q is not a clean absolute path", s.Home)
	}
	if !protocol.ValidSessionName(o.Session) {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "session %q is not a Session's name", o.Session)
	}
	rel := func(what, dir string) (string, error) {
		if dir == "" {
			return "", protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, "", "the executor's %s is unknown", what)
		}
		r := filepath.ToSlash(dir)
		if path.IsAbs(r) || path.Clean(r) != r || r == "." || r == ".." || strings.HasPrefix(r, "../") {
			return "", protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, "", "the executor's %s %q is not a clean path inside HOME", what, dir)
		}
		return r, nil
	}
	claude, err := rel("CLAUDE_CONFIG_DIR", o.ClaudeConfigDir)
	if err != nil {
		return nil, err
	}
	codex, err := rel("CODEX_HOME", o.CodexHome)
	if err != nil {
		return nil, err
	}
	stateRel, err := filepath.Rel(s.Home, s.StateDir)
	if err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, "", "agentd's state directory is not inside HOME")
	}
	agentdRel, err := rel("agentd state directory", stateRel)
	if err != nil {
		return nil, err
	}
	workRel, err := filepath.Rel(s.Home, s.WorkDir())
	if err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, "", "the work directory is not inside HOME")
	}
	logRel, err := rel("log", path.Join(filepath.ToSlash(workRel), o.Session+".log"))
	if err != nil {
		return nil, err
	}
	roots := []historyRootDef{
		{name: protocol.HistoryRootClaude, rel: claude, rules: claudeLayout},
		{name: protocol.HistoryRootCodex, rel: codex, rules: codexLayout},
		{name: protocol.HistoryRootAgentd, rel: agentdRel, rules: agentdLayout},
		{name: protocol.HistoryRootLog, rel: logRel, file: o.Session + ".log"},
	}
	for i := range roots {
		roots[i].abs = filepath.Join(s.Home, filepath.FromSlash(roots[i].rel))
		for j := 0; j < i; j++ {
			a, b := roots[i].rel, roots[j].rel
			if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, "", "the %s root %q and the %s root %q overlap", roots[i].name, a, roots[j].name, b)
			}
		}
		// Every directory between HOME and the root must be a directory, not a
		// link: the export never leaves HOME through one.
		parts := strings.Split(roots[i].rel, "/")
		cur := s.Home
		for k, p := range parts[:len(parts)-1] {
			cur = filepath.Join(cur, p)
			fi, err := os.Lstat(cur)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, roots[i].rel, "%v", err)
			}
			if !fi.IsDir() {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, path.Join(parts[:k+1]...), "a path to the %s root is not a directory", roots[i].name)
			}
		}
		if roots[i].file == "" {
			fi, err := os.Lstat(roots[i].abs)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, roots[i].rel, "%v", err)
			}
			if err == nil && !fi.IsDir() {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, roots[i].rel, "the %s root is not a directory", roots[i].name)
			}
		}
	}
	return roots, nil
}

// credentialDigests are the SHA-256 of every credential file the export can
// find: the provider directories' credential entries and the GitHub token
// (DESIGN-025 6.3). A file in an exported subtree with one of these digests
// is a renamed whole copy. Empty files are left out, or every empty file
// would match.
func credentialDigests(s Settings, roots []historyRootDef) (map[string]bool, error) {
	out := map[string]bool{}
	for _, r := range roots {
		if r.rules == nil {
			continue
		}
		names, err := readHistoryDir(r.abs)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			rule, ok := classifyHistoryName(r.rules, name)
			if !ok || rule.class != protocol.HistoryClassCredential {
				continue
			}
			fi, err := os.Lstat(filepath.Join(r.abs, name))
			if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > historyBounds.fileBytes {
				continue
			}
			sum, _, _, err := hashHistoryFileLinks(filepath.Join(r.abs, name), true)
			if err != nil {
				return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, path.Join(r.name, name), "read a credential to compare: %v", err)
			}
			out[sum] = true
		}
	}
	if s.GHTokenFile != "" {
		sum, err := readCredentialReference(s.Home, s.GHTokenFile)
		if err != nil {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, "", "read %s to compare: %v", s.GHTokenFile, err)
		}
		if sum != "" {
			out[sum] = true
		}
	}
	return out, nil
}

// readCredentialReference hashes a credential file outside HOME, such as
// /creds/gh_token. A Secret volume's files are links into ..data/, so this
// one open follows links; it refuses a path inside HOME, which only the
// O_NOFOLLOW helpers read. A missing file has no digest.
func readCredentialReference(home, p string) (string, error) {
	if !filepath.IsAbs(p) || p == home || strings.HasPrefix(filepath.Clean(p), home+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not a credential file outside HOME", p)
	}
	f, err := os.OpenFile(p, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return "", nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, historyBounds.fileBytes)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// historyWalk collects a plan's files and exclusions.
type historyWalk struct {
	creds    map[string]bool
	files    []protocol.HistoryFile
	excluded []protocol.HistoryExcluded
	bytes    int64
}

func (w *historyWalk) exclude(root, p, class string, bytes int64) error {
	if len(w.excluded) >= historyBounds.excluded {
		return protocol.HistoryErrorf(protocol.HistoryErrTooLarge, path.Join(root, p), "more than %d excluded names", historyBounds.excluded)
	}
	if class == protocol.HistoryClassCredential {
		bytes = 0
	}
	w.excluded = append(w.excluded, protocol.HistoryExcluded{Root: root, Path: p, Class: class, Bytes: bytes})
	return nil
}

// dirRoot classifies a provider or agentd directory's top-level entries and
// walks the history ones.
func (w *historyWalk) dirRoot(r historyRootDef) (absent bool, err error) {
	fi, err := os.Lstat(r.abs)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, protocol.HistoryErrorf(protocol.HistoryErrIO, r.name, "%v", err)
	}
	if !fi.IsDir() {
		return false, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, r.name, "the %s root %q is not a directory", r.name, r.rel)
	}
	names, err := readHistoryDir(r.abs)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		abs := filepath.Join(r.abs, name)
		rule, ok := classifyHistoryName(r.rules, name)
		switch {
		case ok && rule.class == protocol.HistoryClassCredential:
			if err := w.exclude(r.name, name, rule.class, 0); err != nil {
				return false, err
			}
			continue
		case (!ok || rule.class == classHistory) && protocol.IsCredentialName(name):
			// A credential's name where history or nothing is expected, such
			// as an auth.json in agentd's state directory.
			return false, protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, path.Join(r.name, name), "a credential's name")
		case !ok:
			return false, protocol.HistoryErrorf(protocol.HistoryErrUnknownEntry, path.Join(r.name, name), "no class for this name; a source change must classify it")
		}
		efi, err := os.Lstat(abs)
		if err != nil {
			return false, protocol.HistoryErrorf(protocol.HistoryErrIO, path.Join(r.name, name), "%v", err)
		}
		if efi.Mode()&fs.ModeSymlink != 0 {
			if err := w.exclude(r.name, name, protocol.HistoryClassLink, 0); err != nil {
				return false, err
			}
			continue
		}
		if rule.class != classHistory {
			n, err := treeBytesNoFollow(abs, efi)
			if err != nil {
				return false, err
			}
			if err := w.exclude(r.name, name, rule.class, n); err != nil {
				return false, err
			}
			continue
		}
		switch {
		case rule.kind == kindDir && efi.IsDir():
			if err := w.walkDir(r.name, abs, name, 1); err != nil {
				return false, err
			}
		case rule.kind == kindFile && efi.Mode().IsRegular():
			if err := w.addFile(r.name, abs, name, efi); err != nil {
				return false, err
			}
		default:
			return false, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(r.name, name), "a %s where history of another kind belongs", efi.Mode().Type())
		}
	}
	return false, nil
}

// logRoot exports the Session's log, ~/work/<session>.log, when it is there.
func (w *historyWalk) logRoot(r historyRootDef) (absent bool, err error) {
	fi, err := os.Lstat(r.abs)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, protocol.HistoryErrorf(protocol.HistoryErrIO, r.name, "%v", err)
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return false, w.exclude(r.name, r.file, protocol.HistoryClassLink, 0)
	case fi.Mode().IsRegular():
		return false, w.addFile(r.name, r.abs, r.file, fi)
	default:
		return false, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(r.name, r.file), "the log is a %s", fi.Mode().Type())
	}
}

// walkDir walks one history subtree. Links are listed, never followed;
// anything but a directory, a regular file or a link refuses.
func (w *historyWalk) walkDir(root, abs, rel string, depth int) error {
	if depth >= protocol.MaxHistoryPathDepth {
		return protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(root, rel), "deeper than %d levels", protocol.MaxHistoryPathDepth)
	}
	names, err := readHistoryDir(abs)
	if err != nil {
		return err
	}
	for _, name := range names {
		childRel := rel + "/" + name
		childAbs := filepath.Join(abs, name)
		if len(childRel) > protocol.MaxHistoryPathBytes {
			return protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(root, rel), "a path longer than %d bytes", protocol.MaxHistoryPathBytes)
		}
		if protocol.IsCredentialName(name) {
			return protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, path.Join(root, childRel), "a credential's name")
		}
		fi, err := os.Lstat(childAbs)
		if err != nil {
			return protocol.HistoryErrorf(protocol.HistoryErrIO, path.Join(root, childRel), "%v", err)
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			if err := w.exclude(root, childRel, protocol.HistoryClassLink, 0); err != nil {
				return err
			}
		case fi.IsDir():
			if err := w.walkDir(root, childAbs, childRel, depth+1); err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			if err := w.addFile(root, childAbs, childRel, fi); err != nil {
				return err
			}
		default:
			return protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(root, childRel), "a %s", fi.Mode().Type())
		}
	}
	return nil
}

// addFile hashes one regular file into the plan.
func (w *historyWalk) addFile(root, abs, rel string, lfi fs.FileInfo) error {
	name := path.Join(root, rel)
	if st, ok := lfi.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, name, "a file with %d hard links, which could be a credential's", st.Nlink)
	}
	if lfi.Size() > historyBounds.fileBytes {
		return protocol.HistoryErrorf(protocol.HistoryErrTooLarge, name, "%d bytes, over the %d-byte bound for one file", lfi.Size(), historyBounds.fileBytes)
	}
	if len(w.files) >= historyBounds.files {
		return protocol.HistoryErrorf(protocol.HistoryErrTooLarge, name, "more than %d files", historyBounds.files)
	}
	if w.bytes+lfi.Size() > historyBounds.totalBytes {
		return protocol.HistoryErrorf(protocol.HistoryErrTooLarge, name, "more than %d bytes in total", historyBounds.totalBytes)
	}
	sum, blocks, fi, err := hashHistoryFile(abs)
	if err != nil {
		var he *protocol.HistoryError
		if errors.As(err, &he) {
			he.Path = name
			return he
		}
		return protocol.HistoryErrorf(protocol.HistoryErrIO, name, "%v", err)
	}
	if !os.SameFile(lfi, fi) || fi.Size() != lfi.Size() || !fi.ModTime().Equal(lfi.ModTime()) {
		return protocol.HistoryErrorf(protocol.HistoryErrChanged, name, "the file changed while it was read")
	}
	if w.creds[sum] {
		return protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, name, "its bytes are a credential file's")
	}
	w.bytes += fi.Size()
	w.files = append(w.files, protocol.HistoryFile{
		Root:         root,
		Path:         rel,
		Bytes:        fi.Size(),
		Mode:         uint32(fi.Mode().Perm()),
		ModTime:      fi.ModTime().UTC(),
		SHA256:       sum,
		Blocks:       blocks,
		BlocksSHA256: protocol.HistoryBlocksSHA256(blocks),
	})
	return nil
}

// openHistoryFile opens a regular file of HOME with historyOpenFlags and
// checks, on the open file, that it is regular with one link.
func openHistoryFile(abs string) (*os.File, fs.FileInfo, error) {
	return openHistoryFileLinks(abs, false)
}

// openHistoryFileLinks is openHistoryFile; with anyLinks, a file with more
// than one hard link opens too (a credential file read only to compare).
func openHistoryFileLinks(abs string, anyLinks bool) (*os.File, fs.FileInfo, error) {
	f, err := os.OpenFile(abs, historyOpenFlags, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, "", "not a regular file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 && !anyLinks {
		_ = f.Close()
		return nil, nil, protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, "", "a file with %d hard links", st.Nlink)
	}
	return f, fi, nil
}

// hashHistoryFile reads a whole file once: its SHA-256 and the SHA-256 of
// each block. The size must not change while it is read.
func hashHistoryFile(abs string) (string, []string, fs.FileInfo, error) {
	return hashHistoryFileLinks(abs, false)
}

// hashHistoryFileLinks is hashHistoryFile, with openHistoryFileLinks' anyLinks.
func hashHistoryFileLinks(abs string, anyLinks bool) (string, []string, fs.FileInfo, error) {
	f, fi, err := openHistoryFileLinks(abs, anyLinks)
	if err != nil {
		return "", nil, nil, err
	}
	defer func() { _ = f.Close() }()
	bh := newBlockHasher()
	n, err := io.Copy(bh, io.LimitReader(f, fi.Size()+1))
	if err != nil {
		return "", nil, nil, err
	}
	if n != fi.Size() {
		return "", nil, nil, protocol.HistoryErrorf(protocol.HistoryErrChanged, "", "the file's size changed while it was read")
	}
	sum, blocks := bh.finish()
	return sum, blocks, fi, nil
}

// readHistoryDir lists a directory of HOME, opened with historyDirFlags,
// sorted by name.
func readHistoryDir(abs string) ([]string, error) {
	d, err := os.OpenFile(abs, historyDirFlags, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, "", "open %s: %v", filepath.Base(abs), err)
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, "", "list %s: %v", filepath.Base(abs), err)
	}
	sort.Strings(names)
	return names, nil
}

// treeBytesNoFollow is the size of the regular files under an excluded entry,
// from directory listings and Lstat only: no file is opened.
func treeBytesNoFollow(abs string, fi fs.FileInfo) (int64, error) {
	if fi.Mode().IsRegular() {
		return fi.Size(), nil
	}
	if !fi.IsDir() {
		return 0, nil
	}
	names, err := readHistoryDir(abs)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, name := range names {
		cfi, err := os.Lstat(filepath.Join(abs, name))
		if err != nil {
			return 0, protocol.HistoryErrorf(protocol.HistoryErrIO, "", "%v", err)
		}
		c, err := treeBytesNoFollow(filepath.Join(abs, name), cfi)
		if err != nil {
			return 0, err
		}
		n += c
	}
	return n, nil
}

// planRescue lists the Git rescue directory on the shared volume: every file
// a regular file, the manifest complete and naming this Session and directory,
// and each bundle matching the manifest's size and SHA-256.
func planRescue(s Settings, o HistoryExportOptions) (*protocol.HistoryGit, error) {
	session, stamp, err := protocol.ParseRescueID(o.Rescue)
	if err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "%v", err)
	}
	if session != o.Session {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "rescue %s is not Session %s's", o.Rescue, o.Session)
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, "", "%v", err)
	}
	dir := protocol.RescueDir(session, stamp)
	abs := filepath.Join(s.SharedDir, filepath.FromSlash(dir))
	for _, p := range []string{protocol.RescueRoot, path.Join(protocol.RescueRoot, session), dir} {
		fi, err := os.Lstat(filepath.Join(s.SharedDir, filepath.FromSlash(p)))
		if err != nil {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, "", "rescue %s: %v", o.Rescue, err)
		}
		if !fi.IsDir() {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, p, "not a directory")
		}
	}
	m, err := readRescueManifestNoFollow(filepath.Join(abs, protocol.ManifestFile))
	if err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrIO, path.Join(dir, protocol.ManifestFile), "%v", err)
	}
	if !m.Complete || m.Session != session || m.Dir != dir {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, path.Join(dir, protocol.ManifestFile), "the manifest is not a complete rescue of this directory")
	}
	want := map[string]protocol.RepoBundle{}
	for _, r := range m.Repos {
		if r.Bundle.File == "" {
			continue
		}
		if path.Dir(r.Bundle.File) != dir || !protocol.ValidHistoryGitFileName(path.Base(r.Bundle.File)) {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, path.Join(dir, protocol.ManifestFile), "the bundle %q is not a file of this directory", r.Bundle.File)
		}
		want[path.Base(r.Bundle.File)] = r.Bundle
	}
	names, err := readHistoryDir(abs)
	if err != nil {
		return nil, err
	}
	if len(names) > protocol.MaxHistoryGitFiles {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrTooLarge, dir, "more than %d files", protocol.MaxHistoryGitFiles)
	}
	g := &protocol.HistoryGit{Rescue: o.Rescue, Files: []protocol.HistoryGitFile{}}
	var total int64
	for _, name := range names {
		b, isBundle := want[name]
		if name != protocol.ManifestFile && !isBundle {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(dir, name), "the manifest does not name it")
		}
		sum, _, fi, err := hashHistoryFile(filepath.Join(abs, name))
		if err != nil {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(dir, name), "%v", err)
		}
		if isBundle && (fi.Size() != b.Size || sum != b.SHA256) {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, path.Join(dir, name), "the bundle differs from the manifest")
		}
		total += fi.Size()
		if total > historyBounds.totalBytes {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrTooLarge, dir, "the rescue holds more than %d bytes", historyBounds.totalBytes)
		}
		delete(want, name)
		g.Files = append(g.Files, protocol.HistoryGitFile{Name: name, Bytes: fi.Size(), SHA256: sum})
	}
	if len(want) > 0 {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, dir, "a bundle the manifest names is missing")
	}
	return g, nil
}

// readRescueManifestNoFollow reads a rescue's manifest.json with
// historyOpenFlags.
func readRescueManifestNoFollow(abs string) (protocol.RescueManifest, error) {
	var m protocol.RescueManifest
	f, fi, err := openHistoryFile(abs)
	if err != nil {
		return m, err
	}
	defer func() { _ = f.Close() }()
	if fi.Size() > maxManifestBytes {
		return m, fmt.Errorf("the manifest is larger than %d bytes", maxManifestBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("the manifest is not JSON: %w", err)
	}
	return m, nil
}

// blockHasher hashes a stream whole and in HistoryBlockSize blocks.
type blockHasher struct {
	whole  hash.Hash
	block  hash.Hash
	inBlk  int
	blocks []string
}

func newBlockHasher() *blockHasher {
	return &blockHasher{whole: sha256.New(), block: sha256.New(), blocks: []string{}}
}

func (b *blockHasher) Write(p []byte) (int, error) {
	n := len(p)
	b.whole.Write(p)
	for len(p) > 0 {
		take := protocol.HistoryBlockSize - b.inBlk
		if take > len(p) {
			take = len(p)
		}
		b.block.Write(p[:take])
		b.inBlk += take
		p = p[take:]
		if b.inBlk == protocol.HistoryBlockSize {
			b.blocks = append(b.blocks, hex.EncodeToString(b.block.Sum(nil)))
			b.block.Reset()
			b.inBlk = 0
		}
	}
	return n, nil
}

// finish returns the whole digest and the block digests; an empty stream has
// no blocks.
func (b *blockHasher) finish() (string, []string) {
	if b.inBlk > 0 {
		b.blocks = append(b.blocks, hex.EncodeToString(b.block.Sum(nil)))
		b.block.Reset()
		b.inBlk = 0
	}
	return hex.EncodeToString(b.whole.Sum(nil)), b.blocks
}
