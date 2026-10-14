package agentd

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The history archive (DESIGN-025 7): `agentd ctl history-archive
// put|verify|get|list|manifest|read`, run by the operator in the shelf pod over the
// archive volume at ~/.history-archive. put writes a whole entry into
// .incoming/<random>, syncs it and renames it into place; nothing is written
// over an entry once it is there, and the only removal is debris in .incoming
// (TestHistoryArchiveNeverRemoves).

// HistoryArchive is the archive in one shelf.
type HistoryArchive struct {
	// Root is the archive's mount, ~/.history-archive.
	Root string
	// Home and SharedDir locate the shared volume the Git rescue is copied
	// from.
	Home      string
	SharedDir string
	// Now is the clock: archivedAt, and the age of debris in .incoming.
	Now func() time.Time
	// Nil uses syncArchiveDir. A per-instance hook permits finite publication
	// fault injection without changing other archives or their test clocks.
	syncDir func(string) error
}

// NewHistoryArchive is the archive of the shelf's settings.
func NewHistoryArchive(s Settings) HistoryArchive {
	return HistoryArchive{Root: filepath.Join(s.Home, protocol.HistoryArchiveDirName), Home: s.Home, SharedDir: s.SharedDir, Now: time.Now}
}

// historyArchiveIsMounted checks that the archive is a filesystem of its own,
// not a directory on HOME (D-48's check). Tests replace it.
var historyArchiveIsMounted = func(root, home string) error {
	var ar, hm syscall.Stat_t
	if err := syscall.Stat(root, &ar); err != nil {
		return fmt.Errorf("the history archive is not mounted at %s: %w", root, err)
	}
	if err := syscall.Stat(home, &hm); err != nil {
		return err
	}
	if ar.Dev == hm.Dev {
		return fmt.Errorf("%s is not a volume of its own", root)
	}
	return nil
}

// CheckMounted is `agentd shelf --history-archive`'s start check.
func (a HistoryArchive) CheckMounted() error {
	if err := historyArchiveIsMounted(a.Root, a.Home); err != nil {
		return protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "%v", err)
	}
	return nil
}

// lockHistoryArchive takes .shelf.lock, exclusive for put and shared for the
// reads.
func lockHistoryArchive(root string, exclusive bool) (func(), error) {
	f, err := os.OpenFile(filepath.Join(root, protocol.ShelfLockFile), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "the archive lock: %v", err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "the archive lock: %v", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// readContentLine reads put's first line, the content manifest.
func readContentLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > historyBounds.contentBytes+1 {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "the content manifest is larger than %d bytes", historyBounds.contentBytes)
		}
		if err == nil {
			return bytes.TrimSuffix(line, []byte("\n")), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "read the content manifest: %v", err)
		}
	}
}

// parseHistoryContent decodes a content manifest strictly and checks that its
// bytes are canonical, so its digest is the one the operator computed.
func parseHistoryContent(line []byte) (protocol.HistoryContent, string, error) {
	var c protocol.HistoryContent
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, "", protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "the content manifest: %v", err)
	}
	if dec.More() {
		return c, "", protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "the content manifest has trailing data")
	}
	sum, canon, err := protocol.CanonicalSHA256(c)
	if err != nil {
		return c, "", protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "the content manifest: %v", err)
	}
	if !bytes.Equal(canon, line) {
		return c, "", protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "the content manifest is not canonical JSON")
	}
	return c, sum, nil
}

// validateHistoryContent checks a content manifest's format, identities,
// names and bounds, and that it describes a whole export: the four roots, a
// rescue whose verdict lets a HOME go (DESIGN-025 6.2), and, for a Verified
// rescue, that rescue's own bundle directory. Verify compares an entry only
// with its own manifest, so put is where a manifest that proves less is
// refused.
func validateHistoryContent(c protocol.HistoryContent) error {
	bad := func(format string, args ...any) error {
		return protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", format, args...)
	}
	switch {
	case c.Format != protocol.HistoryArchiveFormat:
		return bad("format %q is not %s", c.Format, protocol.HistoryArchiveFormat)
	case !protocol.ValidSessionName(c.Session.Namespace) || !protocol.ValidSessionName(c.Session.Name) || !protocol.ValidKubeUID(c.Session.UID):
		return bad("the Session's namespace, name or UID is not valid")
	case !protocol.ValidHistoryProject(c.Project) || c.Project == protocol.HistoryNoProject:
		return bad("project %q is not valid", c.Project)
	case c.Provider != protocol.HistoryProviderClaude && c.Provider != protocol.HistoryProviderCodex:
		return bad("provider %q is not claude or codex", c.Provider)
	case !protocol.ValidKubeUID(c.HomeUID):
		return bad("homeUID is not a UID")
	case !protocol.ValidKubeUID(c.Terminal.PodUID) || (c.Terminal.Phase != "Succeeded" && c.Terminal.Phase != "Failed") || len(c.Terminal.Reason) > 128 || len(c.Terminal.NodeName) > 253 || c.Terminal.ObservedAt.IsZero():
		return bad("the terminal record is not valid")
	case !protocol.ValidRescueStamp(c.Rescue.Stamp) || (c.Rescue.PodUID != "" && !protocol.ValidKubeUID(c.Rescue.PodUID)):
		return bad("the rescue's stamp or pod UID is not valid")
	case c.Rescue.Result != protocol.HistoryRescueVerified && c.Rescue.Result != protocol.HistoryRescueCleanAndPushed:
		return bad("the rescue's result %q is not %s or %s", c.Rescue.Result, protocol.HistoryRescueVerified, protocol.HistoryRescueCleanAndPushed)
	case c.Rescue.Result == protocol.HistoryRescueVerified && c.Git == nil:
		return bad("a %s rescue's bundle is not in the manifest", protocol.HistoryRescueVerified)
	case !protocol.ValidSHA256(c.PlanSHA256):
		return bad("planSHA256 is not a SHA-256")
	case len(c.Files) > historyBounds.files || len(c.Excluded) > historyBounds.excluded:
		return bad("too many files or excluded names")
	}
	roots := map[string]bool{}
	seen := map[string]bool{}
	for _, r := range c.Roots {
		if !protocol.ValidHistoryRoot(r.Name) || seen[r.Name] {
			return bad("root %q is not valid or repeated", r.Name)
		}
		if code, why := protocol.CheckHistoryPath(r.Dir); code == protocol.HistoryErrInvalid {
			return bad("root %s's directory %s", r.Name, why)
		}
		switch {
		case r.Name == protocol.HistoryRootAgentd && r.Dir != ".agentd":
			return bad("the agentd root is %q, not .agentd", r.Dir)
		case r.Name == protocol.HistoryRootLog && r.Dir != "work/"+c.Session.Name+".log":
			return bad("the log root is %q, not work/%s.log", r.Dir, c.Session.Name)
		}
		seen[r.Name] = true
		roots[r.Name] = !r.Absent
	}
	for _, name := range []string{protocol.HistoryRootClaude, protocol.HistoryRootCodex, protocol.HistoryRootAgentd, protocol.HistoryRootLog} {
		if !seen[name] {
			return bad("the manifest lacks the %s root", name)
		}
	}
	var total int64
	prev := ""
	for i, f := range c.Files {
		name := f.Name()
		if !roots[f.Root] {
			return bad("file %d names root %q, which the manifest lacks or marks absent", i, f.Root)
		}
		if code, why := protocol.CheckHistoryPath(f.Path); code != "" {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, name, "%s", why)
		}
		if f.Root == protocol.HistoryRootLog && f.Path != c.Session.Name+".log" {
			return bad("the log root holds only the Session's own log")
		}
		if f.Bytes < 0 || f.Bytes > historyBounds.fileBytes || f.Mode > 0o7777 || !protocol.ValidSHA256(f.SHA256) || !protocol.ValidSHA256(f.BlocksSHA256) || f.Blocks != nil || f.ModTime.IsZero() {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, name, "size, mode, time or digests not valid")
		}
		if i > 0 && name <= prev {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, name, "files are not in order, or repeat")
		}
		prev = name
		total += f.Bytes
	}
	if total != c.Bytes || total > historyBounds.totalBytes {
		return bad("bytes %d is not the files' total %d, or over the bound", c.Bytes, total)
	}
	for _, e := range c.Excluded {
		if !protocol.ValidHistoryRoot(e.Root) || e.Bytes < 0 {
			return bad("an excluded name is not valid")
		}
		if code, why := protocol.CheckHistoryPath(e.Path); code == protocol.HistoryErrInvalid {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, e.Root+"/"+e.Path, "excluded: %s", why)
		}
		switch e.Class {
		case protocol.HistoryClassNotHistory, protocol.HistoryClassUserCopy, protocol.HistoryClassLink:
		case protocol.HistoryClassCredential:
			if e.Bytes != 0 || e.Partial {
				return bad("a credential is listed with a size")
			}
		default:
			return bad("excluded class %q is not known", e.Class)
		}
	}
	if c.Git != nil {
		session, name, err := protocol.ParseRescueID(c.Git.Rescue)
		if err != nil || session != c.Session.Name {
			return bad("the Git rescue %q is not this Session's", c.Git.Rescue)
		}
		// The rescue's directory is its stamp, or <stamp>-N for a later
		// rescue in the same minute (D-48): an older rescue's bundle never
		// stands for this one's.
		if name != c.Rescue.Stamp && !strings.HasPrefix(name, c.Rescue.Stamp+"-") {
			return bad("the Git rescue %q is not the rescue %s", c.Git.Rescue, c.Rescue.Stamp)
		}
		if len(c.Git.Files) > protocol.MaxHistoryGitFiles {
			return bad("too many Git files")
		}
		manifest := false
		var gitBytes int64
		for i, g := range c.Git.Files {
			if !protocol.ValidHistoryGitFileName(g.Name) || g.Bytes < 0 || !protocol.ValidSHA256(g.SHA256) || (i > 0 && g.Name <= c.Git.Files[i-1].Name) {
				return bad("Git file %q is not valid, in order and unique", g.Name)
			}
			manifest = manifest || g.Name == protocol.ManifestFile
			gitBytes += g.Bytes
		}
		if !manifest || gitBytes > historyBounds.totalBytes {
			return bad("the Git rescue lacks its manifest, or is over the bound")
		}
	}
	return nil
}

// Put stores the entry read from r: one line of canonical content manifest,
// then the export's tar stream. An entry with the same content.json already
// there is "exists"; with another, conflict, and nothing is written.
func (a HistoryArchive) Put(r io.Reader, archivedBy string) (protocol.HistoryPutResult, error) {
	var res protocol.HistoryPutResult
	if err := a.CheckMounted(); err != nil {
		return res, err
	}
	br := bufio.NewReaderSize(r, 1<<20)
	line, err := readContentLine(br)
	if err != nil {
		return res, err
	}
	c, sum, err := parseHistoryContent(line)
	if err != nil {
		return res, err
	}
	if err := validateHistoryContent(c); err != nil {
		return res, err
	}
	if len(archivedBy) > 256 {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "archivedBy is longer than 256 bytes")
	}
	unlock, err := lockHistoryArchive(a.Root, true)
	if err != nil {
		return res, err
	}
	defer unlock()
	a.pruneIncoming()

	entryRel := protocol.HistoryEntryDir(c.Project, c.Session.UID, sum)
	final := filepath.Join(a.Root, filepath.FromSlash(entryRel))
	res = protocol.HistoryPutResult{ContentSHA256: sum, Entry: entryRel, Bytes: c.Bytes, Files: len(c.Files)}
	if c.Git != nil {
		res.GitFiles = len(c.Git.Files)
	}
	if _, err := os.Lstat(final); err == nil {
		old, err := readArchiveFile(a.Root, filepath.Join(final, protocol.HistoryContentFile), protocol.MaxHistoryContentBytes)
		if err != nil {
			return res, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, entryRel, "the entry there has no readable content.json: %v", err)
		}
		if !bytes.Equal(old, line) {
			return res, protocol.HistoryErrorf(protocol.HistoryErrConflict, entryRel, "an entry with another content manifest is already there")
		}
		e, err := readHistoryEntryFile(a.Root, final, sum)
		if err != nil {
			return res, err
		}
		if err := a.linkIndexEntry(c, sum, entryRel, e.ArchivedAt); err != nil {
			return res, err
		}
		res.Status = protocol.HistoryPutExists
		return res, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, protocol.HistoryErrorf(protocol.HistoryErrNoArchive, entryRel, "%v", err)
	}

	tmp, err := a.newIncomingDir()
	if err != nil {
		return res, err
	}
	now := a.Now().UTC()
	if err := a.incomingWhole(tmp, final, c, line, br, protocol.HistoryEntry{
		Format: protocol.HistoryArchiveEntryFormat, ContentSHA256: sum, ArchivedAt: now, ArchivedBy: archivedBy,
	}); err != nil {
		// A refused or failed put leaves nothing behind, so retries cannot
		// fill the archive; only a crash leaves debris for pruneIncoming.
		_ = a.removeIncoming(tmp)
		return res, err
	}
	if err := a.linkIndexEntry(c, sum, entryRel, now); err != nil {
		return res, err
	}
	res.Status = protocol.HistoryPutStored
	return res, nil
}

// incomingWhole writes a whole entry into tmp, entry.json last, syncs it and
// renames it into place.
func (a HistoryArchive) incomingWhole(tmp, final string, c protocol.HistoryContent, line []byte, r io.Reader, entry protocol.HistoryEntry) error {
	if err := a.checkIncoming(tmp); err != nil {
		return err
	}
	if err := a.incomingEntry(tmp, c, line, r); err != nil {
		return err
	}
	data, err := protocol.CanonicalJSON(entry)
	if err != nil {
		return err
	}
	if err := a.incomingFile(filepath.Join(tmp, protocol.HistoryEntryFile), data); err != nil {
		return err
	}
	if err := a.incomingSyncTree(tmp); err != nil {
		return err
	}
	return a.publishIncoming(tmp, final)
}

// incomingEntry writes content.json, every file of the tar stream and its
// block list, and the Git rescue into the incoming directory tmp, checking
// each against the manifest as it writes.
func (a HistoryArchive) incomingEntry(tmp string, c protocol.HistoryContent, line []byte, r io.Reader) error {
	if err := a.checkIncoming(tmp); err != nil {
		return err
	}
	if err := a.incomingFile(filepath.Join(tmp, protocol.HistoryContentFile), line); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for i, f := range c.Files {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, f.Name(), "the stream ended before this listed file")
		}
		if err != nil {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, f.Name(), "the stream: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Name != f.Name() || hdr.Size != f.Bytes {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, hdr.Name, "the stream's entry is not the next listed file %s", f.Name())
		}
		dst := filepath.Join(tmp, protocol.HistoryFilesDir, f.Root, filepath.FromSlash(f.Path))
		sum, blocks, err := a.incomingCopy(dst, tr, f.Bytes)
		if err != nil {
			return err
		}
		if sum != f.SHA256 || protocol.HistoryBlocksSHA256(blocks) != f.BlocksSHA256 {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, f.Name(), "the file's bytes differ from the manifest")
		}
		data, err := protocol.CanonicalJSON(blocks)
		if err != nil {
			return err
		}
		if err := a.incomingFile(filepath.Join(tmp, protocol.HistoryBlocksDir, fmt.Sprintf("%d.json", i)), data); err != nil {
			return err
		}
	}
	if hdr, err := tr.Next(); !errors.Is(err, io.EOF) {
		name := ""
		if hdr != nil {
			name = hdr.Name
		}
		return protocol.HistoryErrorf(protocol.HistoryErrInvalid, name, "the stream holds an entry the manifest does not list")
	}
	if c.Git == nil {
		return nil
	}
	if err := sharedIsMounted(a.SharedDir, a.Home); err != nil {
		return protocol.HistoryErrorf(protocol.HistoryErrNoArchive, "", "the Git rescue: %v", err)
	}
	session, stamp, _ := protocol.ParseRescueID(c.Git.Rescue)
	src := filepath.Join(a.SharedDir, protocol.RescueRoot, session, stamp)
	for _, g := range c.Git.Files {
		in, fi, err := openHistoryFile(a.SharedDir, filepath.Join(src, g.Name))
		if err != nil {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, path.Join(protocol.HistoryGitDir, g.Name), "the rescue file: %v", err)
		}
		if fi.Size() != g.Bytes {
			_ = in.Close()
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, path.Join(protocol.HistoryGitDir, g.Name), "the rescue file is %d bytes, the manifest says %d", fi.Size(), g.Bytes)
		}
		sum, _, err := a.incomingCopy(filepath.Join(tmp, protocol.HistoryGitDir, stamp, g.Name), in, g.Bytes)
		_ = in.Close()
		if err != nil {
			return err
		}
		if sum != g.SHA256 {
			return protocol.HistoryErrorf(protocol.HistoryErrInvalid, path.Join(protocol.HistoryGitDir, g.Name), "the rescue file differs from the manifest")
		}
	}
	return nil
}

// checkIncoming refuses a path that is not strictly inside .incoming. Every
// incoming* writer calls it first.
func (a HistoryArchive) checkIncoming(p string) error {
	base := filepath.Join(a.Root, protocol.HistoryArchiveIncoming) + string(filepath.Separator)
	if !strings.HasPrefix(filepath.Clean(p), base) {
		return fmt.Errorf("%s is not inside the archive's .incoming", p)
	}
	return nil
}

// newIncomingDir makes a new .incoming/<random> directory.
func (a HistoryArchive) newIncomingDir() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tmp := filepath.Join(a.Root, protocol.HistoryArchiveIncoming, hex.EncodeToString(b[:]))
	return tmp, a.incomingMkdir(tmp)
}

// incomingMkdir makes one new directory under .incoming.
func (a HistoryArchive) incomingMkdir(p string) error {
	if err := a.checkIncoming(p); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.Mkdir(p, 0o700)
}

// incomingFile writes a new file under .incoming and syncs it.
func (a HistoryArchive) incomingFile(p string, data []byte) error {
	if err := a.checkIncoming(p); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// incomingCopy copies exactly n bytes of r into a new file under .incoming,
// hashing as it writes, and syncs it.
func (a HistoryArchive) incomingCopy(p string, r io.Reader, n int64) (string, []string, error) {
	if err := a.checkIncoming(p); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return "", nil, err
	}
	bh := newBlockHasher()
	got, err := io.Copy(io.MultiWriter(f, bh), io.LimitReader(r, n))
	if err == nil && got != n {
		err = protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "the stream ended %d bytes into a %d-byte file", got, n)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var he *protocol.HistoryError
		if errors.As(err, &he) {
			return "", nil, err
		}
		return "", nil, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "write: %v", err)
	}
	sum, blocks := bh.finish()
	return sum, blocks, nil
}

// incomingSyncTree syncs every directory of an incoming entry, deepest first,
// so the rename publishes a tree whose names are all on disk.
func (a HistoryArchive) incomingSyncTree(tmp string) error {
	if err := a.checkIncoming(tmp); err != nil {
		return err
	}
	var dirs []string
	var walk func(string) error
	walk = func(d string) error {
		dirs = append(dirs, d)
		names, err := readHistoryDir(a.Root, d)
		if err != nil {
			return err
		}
		for _, n := range names {
			fi, err := os.Lstat(filepath.Join(d, n))
			if err != nil {
				return err
			}
			if fi.IsDir() {
				if err := walk(filepath.Join(d, n)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(tmp); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncArchiveDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

// publishIncoming renames a complete incoming entry into place and syncs the
// parents. The exclusive lock is held, so nothing appears at final between
// the check and the rename.
func (a HistoryArchive) publishIncoming(tmp, final string) error {
	if err := a.checkIncoming(tmp); err != nil {
		return err
	}
	syncDir := a.syncDir
	if syncDir == nil {
		syncDir = syncArchiveDir
	}
	if _, err := os.Lstat(final); !errors.Is(err, fs.ErrNotExist) {
		return protocol.HistoryErrorf(protocol.HistoryErrConflict, "", "the entry appeared during the put")
	}
	parent := filepath.Dir(final)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	for d := parent; ; d = filepath.Dir(d) {
		if err := syncDir(d); err != nil {
			return err
		}
		if d == a.Root || len(d) <= len(a.Root) {
			break
		}
	}
	return syncDir(filepath.Dir(tmp))
}

// historyIndexName is an index file's path relative to the archive's root:
// index/<project>/<YYYY-MM>/<archivedAt>-<session UID>-<16 hex>.json, the
// time fixed-width so names sort by it.
func historyIndexName(project, sessionUID, sum string, at time.Time) string {
	at = at.UTC()
	return path.Join(protocol.HistoryArchiveIndex, protocol.HistoryProjectDir(project), at.Format("2006-01"),
		at.Format("20060102T150405.000000000Z")+"-"+sessionUID+"-"+sum[:16]+".json")
}

// linkIndexEntry writes the entry's index file unless it is there: a synced
// file under .incoming, hard-linked into place, which never replaces a file.
// It repairs a put that stopped between the rename and the index. Its
// .incoming directory goes on every return, a failed one included; only a
// crash leaves it for pruneIncoming.
func (a HistoryArchive) linkIndexEntry(c protocol.HistoryContent, sum, entryRel string, at time.Time) error {
	rel := historyIndexName(c.Project, c.Session.UID, sum, at)
	final := filepath.Join(a.Root, filepath.FromSlash(rel))
	if _, err := os.Lstat(final); err == nil {
		return nil
	}
	idx := protocol.HistoryIndexEntry{
		Format: protocol.HistoryArchiveIndexFormat, Project: c.Project, Session: c.Session.Name,
		SessionUID: c.Session.UID, ContentSHA256: sum, ArchivedAt: at.UTC(), Entry: entryRel,
	}
	data, err := protocol.CanonicalJSON(idx)
	if err != nil {
		return err
	}
	tmp, err := a.newIncomingDir()
	if err != nil {
		return err
	}
	removed := false
	defer func() {
		if !removed {
			_ = a.removeIncoming(tmp)
		}
	}()
	tmpFile := filepath.Join(tmp, "index.json")
	if err := a.incomingFile(tmpFile, data); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	if err := os.Link(tmpFile, final); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := syncArchiveDir(filepath.Dir(final)); err != nil {
		return err
	}
	removed = true
	return a.removeIncoming(tmp)
}

// pruneIncoming removes debris in .incoming older than an hour: the leftovers
// of a put that crashed. Errors leave the debris for the next put.
func (a HistoryArchive) pruneIncoming() {
	dir := filepath.Join(a.Root, protocol.HistoryArchiveIncoming)
	names, err := readHistoryDir(a.Root, dir)
	if err != nil {
		return
	}
	now := a.Now()
	for _, n := range names {
		fi, err := os.Lstat(filepath.Join(dir, n))
		if err != nil || now.Sub(fi.ModTime()) < protocol.HistoryIncomingMaxAge {
			continue
		}
		_ = a.removeIncoming(filepath.Join(dir, n))
	}
}

// removeIncoming is the archive's one removal: a path strictly inside
// .incoming.
func (a HistoryArchive) removeIncoming(p string) error {
	if err := a.checkIncoming(p); err != nil {
		return err
	}
	return os.RemoveAll(p)
}

// syncArchiveDir fsyncs a directory of the archive, never through a link.
func syncArchiveDir(d string) error {
	f, err := os.OpenFile(d, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// readArchiveFile reads a whole regular file of the archive at root, never
// through a link, up to bound bytes.
func readArchiveFile(root, p string, bound int64) ([]byte, error) {
	return readArchiveFileLinks(root, p, bound, 1)
}

// readArchiveFileLinks is readArchiveFile for a file that may have up to
// maxLinks hard links: an index file has two while its .incoming copy waits
// for removal after a put that stopped between the link and the removal.
func readArchiveFileLinks(root, p string, bound int64, maxLinks int64) ([]byte, error) {
	f, fi, err := openHistoryFileLinks(root, p, maxLinks)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if fi.Size() > bound {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(p), bound)
	}
	return io.ReadAll(io.LimitReader(f, bound))
}

// readHistoryEntryFile reads entry.json of the entry dir under the archive at
// root and checks it names sum.
func readHistoryEntryFile(root, dir, sum string) (protocol.HistoryEntry, error) {
	var e protocol.HistoryEntry
	data, err := readArchiveFile(root, filepath.Join(dir, protocol.HistoryEntryFile), 64<<10)
	if err != nil {
		return e, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "entry.json: %v", err)
	}
	if err := json.Unmarshal(data, &e); err != nil || e.Format != protocol.HistoryArchiveEntryFormat || e.ContentSHA256 != sum || e.ArchivedAt.IsZero() {
		return e, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "entry.json does not describe this entry")
	}
	return e, nil
}

// openEntry finds an entry by Session UID and content digest, reads its
// content.json and checks the digest. Without sum it takes the Session's
// newest entry.
func (a HistoryArchive) openEntry(sessionUID, sum string) (string, protocol.HistoryContent, protocol.HistoryEntry, error) {
	var c protocol.HistoryContent
	var e protocol.HistoryEntry
	if !protocol.ValidKubeUID(sessionUID) || (sum != "" && !protocol.ValidSHA256(sum)) {
		return "", c, e, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "the Session UID or content digest is not valid")
	}
	projects, err := readHistoryDir(a.Root, filepath.Join(a.Root, protocol.HistoryArchiveEntries))
	if errors.Is(err, fs.ErrNotExist) {
		return "", c, e, protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "no entry for Session %s", sessionUID)
	}
	if err != nil {
		return "", c, e, err
	}
	type cand struct {
		dir string
		e   protocol.HistoryEntry
		sum string
	}
	// named gives an entry's refusal the entry's directory (DESIGN-025 8.1):
	// a reader then tells whose entry failed without any of its bytes, and
	// hides one of a project outside its caller's scope.
	named := func(dir string, err error) error {
		var he *protocol.HistoryError
		if rel, rerr := filepath.Rel(a.Root, dir); rerr == nil && errors.As(err, &he) && he.Path == "" {
			return protocol.HistoryErrorf(he.Code, filepath.ToSlash(rel), "%s", he.Message)
		}
		return err
	}
	var found []cand
	for _, p := range projects {
		sdir := filepath.Join(a.Root, protocol.HistoryArchiveEntries, p, sessionUID)
		names, err := readHistoryDir(a.Root, sdir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", c, e, err
		}
		for _, n := range names {
			if sum != "" && n != sum[:16] {
				continue
			}
			dir := filepath.Join(sdir, n)
			data, err := readArchiveFile(a.Root, filepath.Join(dir, protocol.HistoryContentFile), protocol.MaxHistoryContentBytes)
			if err != nil {
				return "", c, e, named(dir, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "content.json: %v", err))
			}
			h := sha256.Sum256(data)
			got := hex.EncodeToString(h[:])
			if got[:16] != n || (sum != "" && got != sum) {
				return "", c, e, named(dir, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "content.json does not match its entry"))
			}
			ent, err := readHistoryEntryFile(a.Root, dir, got)
			if err != nil {
				return "", c, e, named(dir, err)
			}
			found = append(found, cand{dir, ent, got})
		}
	}
	if len(found) == 0 {
		return "", c, e, protocol.HistoryErrorf(protocol.HistoryErrNotFound, "", "no such entry for Session %s", sessionUID)
	}
	if sum != "" && len(found) > 1 {
		return "", c, e, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "the entry is under more than one project")
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].e.ArchivedAt.Equal(found[j].e.ArchivedAt) {
			return found[i].e.ArchivedAt.After(found[j].e.ArchivedAt)
		}
		return found[i].sum > found[j].sum
	})
	best := found[0]
	data, err := readArchiveFile(a.Root, filepath.Join(best.dir, protocol.HistoryContentFile), protocol.MaxHistoryContentBytes)
	if err != nil {
		return "", c, e, named(best.dir, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "content.json: %v", err))
	}
	c, got, err := parseHistoryContent(data)
	if err != nil || got != best.sum || validateHistoryContent(c) != nil || c.Session.UID != sessionUID {
		return "", protocol.HistoryContent{}, e, named(best.dir, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "content.json is not a valid manifest of this entry"))
	}
	if filepath.Join(a.Root, filepath.FromSlash(protocol.HistoryEntryDir(c.Project, c.Session.UID, got))) != best.dir {
		return "", protocol.HistoryContent{}, e, named(best.dir, protocol.HistoryErrorf(protocol.HistoryErrCorrupt, "", "the entry is not under its own project"))
	}
	return best.dir, c, best.e, nil
}

// Verify re-hashes an entry completely: content.json, every file and block
// list, and every Git file. Anything missing, extra or different is corrupt.
func (a HistoryArchive) Verify(sessionUID, sum string) (protocol.HistoryVerifyResult, error) {
	var res protocol.HistoryVerifyResult
	if err := a.CheckMounted(); err != nil {
		return res, err
	}
	if !protocol.ValidSHA256(sum) {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--content-sha256 is not a SHA-256")
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return res, err
	}
	defer unlock()
	dir, c, _, err := a.openEntry(sessionUID, sum)
	if err != nil {
		return res, err
	}
	corrupt := func(p, format string, args ...any) error {
		return protocol.HistoryErrorf(protocol.HistoryErrCorrupt, p, format, args...)
	}
	want := map[string]bool{protocol.HistoryContentFile: true, protocol.HistoryEntryFile: true}
	for i, f := range c.Files {
		rel := path.Join(protocol.HistoryFilesDir, f.Root, f.Path)
		brel := path.Join(protocol.HistoryBlocksDir, fmt.Sprintf("%d.json", i))
		want[rel], want[brel] = true, true
		got, blocks, fi, err := hashHistoryFile(a.Root, filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return res, corrupt(f.Name(), "%v", err)
		}
		if fi.Size() != f.Bytes || got != f.SHA256 || protocol.HistoryBlocksSHA256(blocks) != f.BlocksSHA256 {
			return res, corrupt(f.Name(), "the file differs from the manifest")
		}
		stored, err := readArchiveFile(a.Root, filepath.Join(dir, filepath.FromSlash(brel)), 1<<20)
		canon, _ := protocol.CanonicalJSON(blocks)
		if err != nil || !bytes.Equal(stored, canon) {
			return res, corrupt(f.Name(), "its block list differs from the file")
		}
	}
	if c.Git != nil {
		_, stamp, _ := protocol.ParseRescueID(c.Git.Rescue)
		for _, g := range c.Git.Files {
			rel := path.Join(protocol.HistoryGitDir, stamp, g.Name)
			want[rel] = true
			got, _, fi, err := hashHistoryFile(a.Root, filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil || fi.Size() != g.Bytes || got != g.SHA256 {
				return res, corrupt(rel, "the Git file differs from the manifest")
			}
		}
	}
	// Nothing else may be in the entry.
	var extra string
	var walk func(abs, rel string) error
	walk = func(abs, rel string) error {
		names, err := readHistoryDir(a.Root, abs)
		if err != nil {
			return err
		}
		for _, n := range names {
			r := path.Join(rel, n)
			fi, err := os.Lstat(filepath.Join(abs, n))
			if err != nil {
				return err
			}
			switch {
			case fi.IsDir():
				if err := walk(filepath.Join(abs, n), r); err != nil {
					return err
				}
			case !fi.Mode().IsRegular() || !want[r]:
				if extra == "" {
					extra = r
				}
			}
		}
		return nil
	}
	if err := walk(dir, ""); err != nil {
		return res, corrupt("", "%v", err)
	}
	if extra != "" {
		return res, corrupt(extra, "the entry holds a file the manifest does not list")
	}
	res = protocol.HistoryVerifyResult{ContentSHA256: sum, Entry: protocol.HistoryEntryDir(c.Project, c.Session.UID, sum), Files: len(c.Files), Bytes: c.Bytes}
	if c.Git != nil {
		res.GitFiles = len(c.Git.Files)
	}
	return res, nil
}

// Get prints an entry's content manifest after checking its digest; without
// sum, the Session's newest entry.
func (a HistoryArchive) Get(sessionUID, sum string) (protocol.HistoryGetResult, error) {
	var res protocol.HistoryGetResult
	if err := a.CheckMounted(); err != nil {
		return res, err
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return res, err
	}
	defer unlock()
	_, c, e, err := a.openEntry(sessionUID, sum)
	if err != nil {
		return res, err
	}
	return protocol.HistoryGetResult{ContentSHA256: e.ContentSHA256, Entry: e, Content: c}, nil
}

// List walks a project's index newest first and prints each entry's verified
// summary: at most limit items, after the cursor.
func (a HistoryArchive) List(project string, limit int, after string) (protocol.HistoryList, error) {
	res := protocol.HistoryList{Project: project, Items: []protocol.HistorySummary{}}
	if !protocol.ValidHistoryProject(project) || project == protocol.HistoryNoProject {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "project %q is not valid", project)
	}
	if limit == 0 {
		limit = protocol.MaxHistoryListLimit
	}
	if limit < 1 || limit > protocol.MaxHistoryListLimit {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--limit must be 1 to %d", protocol.MaxHistoryListLimit)
	}
	if after != "" && !protocol.ValidHistoryCursor(after) {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--after is not a cursor of this list")
	}
	if err := a.CheckMounted(); err != nil {
		return res, err
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return res, err
	}
	defer unlock()
	base := filepath.Join(a.Root, protocol.HistoryArchiveIndex, protocol.HistoryProjectDir(project))
	months, err := readHistoryDir(a.Root, base)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	var cursors []string
	for i := len(months) - 1; i >= 0 && len(cursors) <= limit; i-- {
		names, err := readHistoryDir(a.Root, filepath.Join(base, months[i]))
		if err != nil {
			return res, err
		}
		for j := len(names) - 1; j >= 0 && len(cursors) <= limit; j-- {
			cur := months[i] + "/" + names[j]
			if !protocol.ValidHistoryCursor(cur) || (after != "" && cur >= after) {
				continue
			}
			cursors = append(cursors, cur)
		}
	}
	if len(cursors) > limit {
		cursors = cursors[:limit]
		res.Next = cursors[limit-1]
	}
	for _, cur := range cursors {
		res.Items = append(res.Items, a.summary(project, filepath.Join(base, filepath.FromSlash(cur))))
	}
	return res, nil
}

// summary reads one index file and the entry it names, verifying the entry's
// content.json; a failed check is {"error":"corrupt"}.
func (a HistoryArchive) summary(project, indexFile string) protocol.HistorySummary {
	bad := protocol.HistorySummary{Error: protocol.HistoryErrCorrupt}
	data, err := readArchiveFileLinks(a.Root, indexFile, 64<<10, 2)
	if err != nil {
		return bad
	}
	var idx protocol.HistoryIndexEntry
	if json.Unmarshal(data, &idx) != nil || idx.Format != protocol.HistoryArchiveIndexFormat || !protocol.ValidKubeUID(idx.SessionUID) || !protocol.ValidSHA256(idx.ContentSHA256) {
		return bad
	}
	bad.SessionUID, bad.ArchivedAt, bad.ContentSHA256 = idx.SessionUID, idx.ArchivedAt, idx.ContentSHA256
	if idx.Project != project || idx.Entry != protocol.HistoryEntryDir(project, idx.SessionUID, idx.ContentSHA256) {
		return bad
	}
	dir := filepath.Join(a.Root, filepath.FromSlash(idx.Entry))
	content, err := readArchiveFile(a.Root, filepath.Join(dir, protocol.HistoryContentFile), protocol.MaxHistoryContentBytes)
	if err != nil {
		return bad
	}
	c, sum, err := parseHistoryContent(content)
	if err != nil || sum != idx.ContentSHA256 || c.Session.UID != idx.SessionUID || c.Project != project {
		return bad
	}
	e, err := readHistoryEntryFile(a.Root, dir, sum)
	if err != nil {
		return bad
	}
	t := c.Terminal
	return protocol.HistorySummary{
		SessionUID: c.Session.UID, Session: c.Session.Name, Provider: c.Provider, ArchivedAt: e.ArchivedAt,
		ContentSHA256: sum, Bytes: c.Bytes, Files: len(c.Files), Terminal: &t,
	}
}

// Manifest prints one page of an entry's verified manifest (DESIGN-025 8):
// the Session's newest entry, or the one sum names. offset and the page's
// items count the files, then the excluded names; limit 0 prints the header
// and counts only. An offset past the end is an empty page, never an error,
// so a reader learns nothing from an offset that it would not from the
// header.
func (a HistoryArchive) Manifest(sessionUID, sum string, offset, limit int) (protocol.HistoryManifestPage, error) {
	var res protocol.HistoryManifestPage
	if offset < 0 || limit < 0 || limit > protocol.MaxHistoryManifestPage {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--offset must be 0 or more and --limit 0 to %d", protocol.MaxHistoryManifestPage)
	}
	if err := a.CheckMounted(); err != nil {
		return res, err
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return res, err
	}
	defer unlock()
	_, c, e, err := a.openEntry(sessionUID, sum)
	if err != nil {
		return res, err
	}
	res = protocol.HistoryManifestPage{
		ContentSHA256: e.ContentSHA256, Entry: e, Session: c.Session, Project: c.Project, Provider: c.Provider, HomeUID: c.HomeUID,
		Terminal: c.Terminal, Rescue: c.Rescue, Roots: c.Roots, Git: c.Git, Bytes: c.Bytes, PlanSHA256: c.PlanSHA256,
		FileCount: len(c.Files), ExcludedCount: len(c.Excluded), Offset: offset,
		Files: []protocol.HistoryFile{}, Excluded: []protocol.HistoryExcluded{},
	}
	total := len(c.Files) + len(c.Excluded)
	if offset >= total {
		return res, nil
	}
	end := offset + min(limit, total-offset)
	for i := offset; i < end; i++ {
		if i < len(c.Files) {
			f := c.Files[i]
			f.Blocks = nil
			res.Files = append(res.Files, f)
		} else {
			res.Excluded = append(res.Excluded, c.Excluded[i-len(c.Files)])
		}
	}
	if limit > 0 && end < total {
		res.Next = end
	}
	return res, nil
}

// Read returns at most MaxHistoryReadBytes of one archived file from offset.
// It checks content.json, the file's block list against content.json, and
// each block it reads against the list, and never returns a byte it could not
// verify.
func (a HistoryArchive) Read(sessionUID, sum, file string, offset int64) (protocol.HistoryReadResult, error) {
	var res protocol.HistoryReadResult
	if err := a.CheckMounted(); err != nil {
		return res, err
	}
	if !protocol.ValidSHA256(sum) {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--content-sha256 is not a SHA-256")
	}
	unlock, err := lockHistoryArchive(a.Root, false)
	if err != nil {
		return res, err
	}
	defer unlock()
	dir, c, _, err := a.openEntry(sessionUID, sum)
	if err != nil {
		return res, err
	}
	idx := -1
	for i, f := range c.Files {
		if f.Name() == file {
			idx = i
			break
		}
	}
	if idx < 0 {
		return res, protocol.HistoryErrorf(protocol.HistoryErrNotFound, file, "the entry has no such file")
	}
	f := c.Files[idx]
	if offset < 0 || offset > f.Bytes {
		return res, protocol.HistoryErrorf(protocol.HistoryErrInvalid, file, "offset %d is outside the file's %d bytes", offset, f.Bytes)
	}
	corrupt := func(format string, args ...any) error {
		return protocol.HistoryErrorf(protocol.HistoryErrCorrupt, file, format, args...)
	}
	data, err := readArchiveFile(a.Root, filepath.Join(dir, protocol.HistoryBlocksDir, fmt.Sprintf("%d.json", idx)), 1<<20)
	if err != nil {
		return res, corrupt("its block list: %v", err)
	}
	var blocks []string
	if json.Unmarshal(data, &blocks) != nil || protocol.HistoryBlocksSHA256(blocks) != f.BlocksSHA256 {
		return res, corrupt("its block list differs from the manifest")
	}
	if int64(len(blocks)) != (f.Bytes+protocol.HistoryBlockSize-1)/protocol.HistoryBlockSize {
		return res, corrupt("its block list does not fit its size")
	}
	in, fi, err := openHistoryFile(a.Root, filepath.Join(dir, protocol.HistoryFilesDir, f.Root, filepath.FromSlash(f.Path)))
	if err != nil {
		return res, corrupt("%v", err)
	}
	defer func() { _ = in.Close() }()
	if fi.Size() != f.Bytes {
		return res, corrupt("the file is %d bytes, the manifest says %d", fi.Size(), f.Bytes)
	}
	end := offset + protocol.MaxHistoryReadBytes
	if end > f.Bytes {
		end = f.Bytes
	}
	out := make([]byte, 0, end-offset)
	if end > offset {
		buf := make([]byte, protocol.HistoryBlockSize)
		for b := offset / protocol.HistoryBlockSize; b <= (end-1)/protocol.HistoryBlockSize; b++ {
			start := b * protocol.HistoryBlockSize
			size := f.Bytes - start
			if size > protocol.HistoryBlockSize {
				size = protocol.HistoryBlockSize
			}
			if _, err := io.ReadFull(io.NewSectionReader(in, start, size), buf[:size]); err != nil {
				return res, corrupt("read block %d: %v", b, err)
			}
			h := sha256.Sum256(buf[:size])
			if hex.EncodeToString(h[:]) != blocks[b] {
				return res, corrupt("block %d differs from its hash", b)
			}
			lo, hi := int64(0), size
			if offset > start {
				lo = offset - start
			}
			if end < start+size {
				hi = end - start
			}
			out = append(out, buf[lo:hi]...)
		}
	}
	return protocol.HistoryReadResult{
		ContentSHA256: sum, File: file, FileSHA256: f.SHA256, Bytes: f.Bytes,
		Offset: offset, Next: end, EOF: end == f.Bytes, Data: out,
	}, nil
}
