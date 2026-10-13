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
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The history export (DESIGN-025 6.3): `agentd ctl history-export plan|stream`,
// run by exec in a private Session's hold pod, or in its session pod, over the
// private HOME. It reads HOME only. Every file and directory is opened by
// openBeneath, one path component at a time from HOME with O_NOFOLLOW, so no
// component below HOME is ever a link (TestHistoryExportReadsOnly), and
// nothing under HOME is created, changed or removed.

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
	contentBytes          int
	excludedEntries       int
}{protocol.MaxHistoryFileBytes, protocol.MaxHistoryExportBytes, protocol.MaxHistoryExportFiles, protocol.MaxHistoryExcluded, protocol.MaxHistoryContentBytes, protocol.MaxHistoryExcludedEntries}

// historyContentReserve is the room a plan leaves under the content
// manifest's bound for the operator's half of it (session, project, terminal
// record and rescue), which is under 2 KiB.
const historyContentReserve = 64 << 10

// historyOpenFlags opens the last component of a file's path: read-only, never
// through a link, and never blocking on a FIFO that raced into its place.
const historyOpenFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC

// historyDirFlags opens each directory of a path the same way.
const historyDirFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_DIRECTORY | syscall.O_CLOEXEC

// historyBaseFlags opens the directory openBeneath starts from: HOME, the
// shared volume or the archive. Its own path may pass through a link or a
// mount; nothing below it may.
const historyBaseFlags = os.O_RDONLY | syscall.O_DIRECTORY | syscall.O_CLOEXEC

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
	w := &historyWalk{home: s.Home, shared: sharedMountInHome(s), creds: creds, countLeft: historyBounds.excludedEntries}
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
	if w.files != nil {
		plan.Files = w.files
	}
	if w.excluded != nil {
		plan.Excluded = w.excluded
	}
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
	if err := checkHistoryContentSize(plan); err != nil {
		return protocol.HistoryExportPlan{}, err
	}
	return plan, nil
}

// checkHistoryContentSize refuses (export_too_large) a plan whose content
// manifest would not fit put's bound with historyContentReserve to spare, so
// the plan refuses rather than a put that can never succeed.
func checkHistoryContentSize(plan protocol.HistoryExportPlan) error {
	var c protocol.HistoryContent
	protocol.HistoryContentFromPlan(&c, plan, strings.Repeat("0", 64))
	data, err := protocol.CanonicalJSON(c)
	if err != nil {
		return err
	}
	if limit := historyBounds.contentBytes - historyContentReserve; len(data) > limit {
		return protocol.HistoryErrorf(protocol.HistoryErrTooLarge, "", "the content manifest would be %d bytes, over the %d-byte bound less %d for the operator's part", len(data), historyBounds.contentBytes, historyContentReserve)
	}
	return nil
}

// sharedMountInHome is the shared volume's directory when it lies strictly
// inside HOME and is mounted there, and "" otherwise: a link to it from a
// history subtree names data HOME's delete leaves alone.
func sharedMountInHome(s Settings) string {
	r, err := filepath.Rel(s.Home, s.SharedDir)
	if err != nil || s.SharedDir == "" || !filepath.IsAbs(s.SharedDir) || r == "." || !filepath.IsLocal(r) {
		return ""
	}
	if sharedIsMounted(s.SharedDir, s.Home) != nil {
		return ""
	}
	return filepath.Clean(s.SharedDir)
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
		if err := streamHistoryFile(tw, s.Home, abs, f); err != nil {
			return err
		}
	}
	return tw.Close()
}

func streamHistoryFile(tw *tar.Writer, home, abs string, f protocol.HistoryFile) error {
	lfi, err := os.Lstat(abs)
	if err != nil {
		return protocol.HistoryErrorf(protocol.HistoryErrChanged, f.Name(), "%v", err)
	}
	links, ok := historyFileLinks(home, f.Root, f.Path, abs, lfi)
	if !ok {
		return protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, f.Name(), "a file with more than one hard link, which could be a credential's")
	}
	file, fi, err := openHistoryFileLinks(home, abs, links)
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
	// The shared volume is mounted inside HOME; nothing on it is the
	// Session's private history.
	sharedRel := ""
	if r, err := filepath.Rel(s.Home, s.SharedDir); err == nil && s.SharedDir != "" && !strings.HasPrefix(filepath.ToSlash(r), "../") && r != ".." && r != "." {
		sharedRel = filepath.ToSlash(r)
	}
	for i := range roots {
		roots[i].abs = filepath.Join(s.Home, filepath.FromSlash(roots[i].rel))
		if a := roots[i].rel; sharedRel != "" && (a == sharedRel || strings.HasPrefix(a, sharedRel+"/") || strings.HasPrefix(sharedRel, a+"/")) {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrLayoutUnknown, "", "the %s root %q overlaps the shared volume", roots[i].name, a)
		}
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
		names, err := readHistoryDir(s.Home, r.abs)
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
			sum, _, _, err := hashHistoryFileLinks(s.Home, filepath.Join(r.abs, name), anyHistoryLinks)
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
	// home is HOME, where every open starts; shared is sharedMountInHome.
	home     string
	shared   string
	creds    map[string]bool
	files    []protocol.HistoryFile
	excluded []protocol.HistoryExcluded
	bytes    int64
	// countLeft is how many more names excludedBytes may stat in this plan.
	countLeft int
}

func (w *historyWalk) exclude(root, p, class string, bytes int64) error {
	return w.excludePartial(root, p, class, bytes, false)
}

// excludePartial lists an excluded name; partial says bytes is a lower bound.
func (w *historyWalk) excludePartial(root, p, class string, bytes int64, partial bool) error {
	if len(w.excluded) >= historyBounds.excluded {
		return protocol.HistoryErrorf(protocol.HistoryErrTooLarge, path.Join(root, p), "more than %d excluded names", historyBounds.excluded)
	}
	if class == protocol.HistoryClassCredential {
		bytes, partial = 0, false
	}
	w.excluded = append(w.excluded, protocol.HistoryExcluded{Root: root, Path: p, Class: class, Bytes: bytes, Partial: partial})
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
	names, err := readHistoryDir(w.home, r.abs)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		abs := filepath.Join(r.abs, name)
		if !utf8.ValidString(name) {
			// JSON cannot carry the name's bytes, so the manifest and the
			// stream would name different files.
			return false, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, r.name, "a name that is not UTF-8")
		}
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
			if rule.class == classHistory {
				// What the link names would be deleted with HOME, or not, without
				// being archived either way.
				return false, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(r.name, name), "a link where history belongs")
			}
			if err := w.exclude(r.name, name, protocol.HistoryClassLink, 0); err != nil {
				return false, err
			}
			continue
		}
		if rule.class != classHistory {
			n, partial := w.excludedBytes(abs, efi, 0)
			if err := w.excludePartial(r.name, name, rule.class, n, partial); err != nil {
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
		return false, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(r.name, r.file), "the log is a link")
	case fi.Mode().IsRegular():
		return false, w.addFile(r.name, r.abs, r.file, fi)
	default:
		return false, protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(r.name, r.file), "the log is a %s", fi.Mode().Type())
	}
}

// walkDir walks one history subtree. Links are listed, never followed, and
// checkLink refuses one whose target HOME's delete would remove; anything but
// a directory, a regular file or a link refuses.
func (w *historyWalk) walkDir(root, abs, rel string, depth int) error {
	if depth >= protocol.MaxHistoryPathDepth {
		return protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(root, rel), "deeper than %d levels", protocol.MaxHistoryPathDepth)
	}
	names, err := readHistoryDir(w.home, abs)
	if err != nil {
		return err
	}
	for _, name := range names {
		childRel := rel + "/" + name
		childAbs := filepath.Join(abs, name)
		if !utf8.ValidString(name) {
			return protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(root, rel), "a name that is not UTF-8")
		}
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
			if err := w.checkLink(root, childAbs, childRel); err != nil {
				return err
			}
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

// checkLink lets a link inside a history subtree be listed only when HOME's
// delete leaves what it names: a target outside HOME, or on the shared volume
// mounted inside it, such as Claude's projects/<key>/memory (DESIGN-001 6.6).
// A link to anything else in HOME refuses (unexpected_entry), because its
// target would go with HOME without being archived. The target is read with
// Readlink and resolved by its text; it is never followed.
func (w *historyWalk) checkLink(root, abs, rel string) error {
	target, err := os.Readlink(abs)
	if err != nil {
		return protocol.HistoryErrorf(protocol.HistoryErrIO, path.Join(root, rel), "%v", err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(abs), target)
	}
	target = filepath.Clean(target)
	within := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) }
	if w.shared != "" && within(target, w.shared) {
		return nil
	}
	if within(target, w.home) {
		return protocol.HistoryErrorf(protocol.HistoryErrUnexpectedEntry, path.Join(root, rel), "a link to something in HOME, which would be deleted without being archived")
	}
	return nil
}

// historyFileLinks is how many hard links an exported file may have: one, or
// two when it is a file at the top of agentd's state directory whose other
// name is the temporary copy writeFileOnce (files.go) left beside it when
// agentd stopped between linking the copy into place and removing it,
// ".<name>.agentd-*" in the same directory. Then the file has no name outside
// that directory, so it cannot be a credential's. ok is false for any other
// file with more than one link. The copy's name is built from rel, which
// holds a slash below the top, so a file deeper down never finds one; there
// the copy would be a history file with two links itself.
func historyFileLinks(home, root, rel, abs string, fi fs.FileInfo) (links int64, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat || st.Nlink <= 1 {
		return 1, true
	}
	if st.Nlink != 2 || root != protocol.HistoryRootAgentd {
		return 0, false
	}
	dir := filepath.Dir(abs)
	names, err := readHistoryDir(home, dir)
	if err != nil {
		return 0, false
	}
	prefix := "." + rel + ".agentd-"
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) || len(n) == len(prefix) {
			continue
		}
		sfi, err := os.Lstat(filepath.Join(dir, n))
		if err == nil && sfi.Mode().IsRegular() && os.SameFile(fi, sfi) {
			return 2, true
		}
	}
	return 0, false
}

// addFile hashes one regular file into the plan.
func (w *historyWalk) addFile(root, abs, rel string, lfi fs.FileInfo) error {
	name := path.Join(root, rel)
	links, ok := historyFileLinks(w.home, root, rel, abs, lfi)
	if !ok {
		return protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, name, "a file with more than one hard link, which could be a credential's")
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
	sum, blocks, fi, err := hashHistoryFileLinks(w.home, abs, links)
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

// anyHistoryLinks lets an open accept a file with any number of hard links:
// a credential file read only to compare.
const anyHistoryLinks = int64(^uint64(0) >> 1)

// openBeneath opens abs, which must lie strictly inside base, one path
// component at a time: base with historyBaseFlags, then each directory below
// it with historyDirFlags and the last component with flags, each relative to
// the directory before it. O_NOFOLLOW on every step means no component below
// base is ever a link, even one swapped in while the walk runs. flags is
// historyOpenFlags or historyDirFlags (TestHistoryExportReadsOnly).
func openBeneath(base, abs string, flags int) (*os.File, error) {
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return nil, &os.PathError{Op: "open", Path: abs, Err: fmt.Errorf("not inside %s", base)}
	}
	dir, err := os.OpenFile(base, historyBaseFlags, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts[:len(parts)-1] {
		fd, err := syscall.Openat(int(dir.Fd()), p, historyDirFlags, 0)
		_ = dir.Close()
		if err != nil {
			return nil, &os.PathError{Op: "openat", Path: abs, Err: err}
		}
		dir = os.NewFile(uintptr(fd), abs)
	}
	fd, err := syscall.Openat(int(dir.Fd()), parts[len(parts)-1], flags, 0)
	_ = dir.Close()
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: abs, Err: err}
	}
	return os.NewFile(uintptr(fd), abs), nil
}

// openHistoryFile opens a regular file with one link below base: HOME, the
// shared volume or the archive.
func openHistoryFile(base, abs string) (*os.File, fs.FileInfo, error) {
	return openHistoryFileLinks(base, abs, 1)
}

// openHistoryFileLinks opens a regular file below base through openBeneath
// and checks, on the open file, that it is regular with at most maxLinks hard
// links.
func openHistoryFileLinks(base, abs string, maxLinks int64) (*os.File, fs.FileInfo, error) {
	f, err := openBeneath(base, abs, historyOpenFlags)
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
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int64(st.Nlink) > maxLinks {
		_ = f.Close()
		return nil, nil, protocol.HistoryErrorf(protocol.HistoryErrCredentialInTree, "", "a file with %d hard links", st.Nlink)
	}
	return f, fi, nil
}

// hashHistoryFile reads a whole file below base once: its SHA-256 and the
// SHA-256 of each block. The size must not change while it is read.
func hashHistoryFile(base, abs string) (string, []string, fs.FileInfo, error) {
	return hashHistoryFileLinks(base, abs, 1)
}

// hashHistoryFileLinks is hashHistoryFile, with openHistoryFileLinks' maxLinks.
func hashHistoryFileLinks(base, abs string, maxLinks int64) (string, []string, fs.FileInfo, error) {
	f, fi, err := openHistoryFileLinks(base, abs, maxLinks)
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

// readHistoryDir lists a directory below base, opened through openBeneath,
// sorted by name.
func readHistoryDir(base, abs string) ([]string, error) {
	d, err := openBeneath(base, abs, historyDirFlags)
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

// excludedBytes is the size of the regular files under an excluded entry,
// from directory listings and Lstat only: no file is opened. Nothing under an
// excluded entry is exported, so what it cannot count never refuses the
// export: it stops below MaxHistoryPathDepth levels, after the plan's
// countLeft names, and at a name it cannot list or stat (a path past
// PATH_MAX, say), and then partial says bytes is a lower bound.
func (w *historyWalk) excludedBytes(abs string, fi fs.FileInfo, depth int) (bytes int64, partial bool) {
	if fi.Mode().IsRegular() {
		return fi.Size(), false
	}
	if !fi.IsDir() {
		return 0, false
	}
	if depth >= protocol.MaxHistoryPathDepth {
		return 0, true
	}
	names, err := readHistoryDir(w.home, abs)
	if err != nil {
		return 0, true
	}
	for _, name := range names {
		if w.countLeft <= 0 {
			return bytes, true
		}
		w.countLeft--
		child := filepath.Join(abs, name)
		cfi, err := os.Lstat(child)
		if err != nil {
			partial = true
			continue
		}
		n, p := w.excludedBytes(child, cfi, depth+1)
		bytes += n
		partial = partial || p
	}
	return bytes, partial
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
	m, err := readRescueManifestNoFollow(s.SharedDir, filepath.Join(abs, protocol.ManifestFile))
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
	names, err := readHistoryDir(s.SharedDir, abs)
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
		sum, _, fi, err := hashHistoryFile(s.SharedDir, filepath.Join(abs, name))
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

// readRescueManifestNoFollow reads a rescue's manifest.json below the shared
// volume's directory through openHistoryFile.
func readRescueManifestNoFollow(shared, abs string) (protocol.RescueManifest, error) {
	var m protocol.RescueManifest
	f, fi, err := openHistoryFile(shared, abs)
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
