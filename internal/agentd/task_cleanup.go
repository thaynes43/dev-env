package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The three agentd commands of a task Session's clean-up (ADR-020, DESIGN-061
// section 3): `ctl rescue --strict-home` and `ctl drop-log` in the hold pod,
// and `ctl rescue-verify` in the shelf. Each is used only when called; a
// rescue, a shelf or a session without them behaves as before.

// strictHomeShown is how many problems StrictHomeCheck.Refused names before
// it counts the rest.
const strictHomeShown = 4

// strictHome is `--strict-home`'s check (protocol.StrictHomeCheck). A rescue
// finds clones only under ~/repos and their worktrees wherever they are, so a
// clone anywhere else would be deleted unseen with the HOME. It looks one
// level deep: HOME itself, every entry directly under ~/work, hidden ones
// included, and every entry directly under ~/repos or HOME that is not hidden
// (tool state such as ~/.claude, a clone in progress in ~/repos and the shared
// volume's mount are skipped). An entry counts when it is a directory, or a
// link to one, holding .git in any form. It must then be a clone under ~/repos
// (clonesIn: its .git a directory) or a worktree one of them lists, compared
// after links are resolved. It never descends and changes nothing.
func strictHome(ctx context.Context, r Runner, s Settings, clones []string) *protocol.StrictHomeCheck {
	check := &protocol.StrictHomeCheck{}
	var problems []string
	allowed := map[string]bool{}
	for _, c := range clones {
		allowed[resolvedPath(c)] = true
		wts, err := listWorktrees(ctx, r, s, c)
		if err != nil {
			problems = append(problems, "cannot list the worktrees of "+c+": "+cmdDetail(err))
			continue
		}
		for _, w := range wts {
			allowed[resolvedPath(w)] = true
		}
	}
	var stray []string
	consider := func(p string) {
		has, err := holdsGit(p)
		switch {
		case err != nil:
			problems = append(problems, "cannot inspect "+p+": "+err.Error())
		case has && !allowed[resolvedPath(p)]:
			stray = append(stray, p)
		}
	}
	consider(s.Home)
	shared := filepath.Clean(s.SharedDir)
	for _, dir := range []struct {
		path   string
		hidden bool
	}{{s.WorkDir(), true}, {s.ReposDir(), false}, {s.Home, false}} {
		entries, err := os.ReadDir(dir.path)
		if isNotExist(err) {
			continue
		}
		if err != nil {
			problems = append(problems, "cannot inspect "+dir.path+": "+err.Error())
			continue
		}
		for _, e := range entries {
			p := filepath.Join(dir.path, e.Name())
			if (!dir.hidden && strings.HasPrefix(e.Name(), ".")) || p == shared || e.Type().IsRegular() {
				continue
			}
			fi, err := os.Stat(p)
			switch {
			case isNotExist(err):
				// A link to nothing holds nothing.
				continue
			case err != nil:
				problems = append(problems, "cannot inspect "+p+": "+err.Error())
				continue
			case !fi.IsDir():
				continue
			}
			consider(p)
		}
	}
	sort.Strings(stray)
	stray = dedupSorted(stray)
	// Causes first: a clone whose worktrees could not be listed makes its
	// worktrees look stray.
	items := problems
	for _, p := range stray {
		items = append(items, "stray repository "+p)
	}
	if len(stray) > protocol.MaxStrictHomeStray {
		stray = stray[:protocol.MaxStrictHomeStray]
	}
	check.Stray = stray
	if len(items) > strictHomeShown {
		items = append(items[:strictHomeShown], fmt.Sprintf("and %d more", len(items)-strictHomeShown))
	}
	check.Refused = strings.Join(items, "; ")
	return check
}

// holdsGit reports whether dir holds .git: a clone's directory, a worktree's
// or a separate git dir's file, or a link. A dir that is not a directory holds
// nothing.
func holdsGit(dir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	switch {
	case err == nil:
		return true, nil
	case isNotExist(err), errors.Is(err, syscall.ENOTDIR):
		return false, nil
	default:
		return false, err
	}
}

// resolvedPath is p with its links resolved, else p cleaned.
func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func dedupSorted(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// VerifyRescue is `agentd ctl rescue-verify [--id <session>/<stamp>]` in the
// shelf (DESIGN-061 section 3). It reports the shared volume's free and total
// bytes. With an id, under the shelf lock shared with other verifies (a prune
// or a restore's hold waits), it reads the rescue's manifest once, hashes
// those bytes, and re-hashes every bundle the manifest names against the size
// and SHA-256 written there. A refusal (protocol.RescueVerifyBundleMissing or
// RescueVerifyBundleMismatch) is a result with OK false and a nil error; an
// error is a failure that proves nothing either way: a bad id, the shared
// volume not mounted, or a read that failed. It writes nothing and needs no
// git.
func VerifyRescue(s Settings, id string) (protocol.RescueVerifyResult, error) {
	res := protocol.RescueVerifyResult{Version: protocol.RescueVerifyVersion, ID: id}
	var session, name string
	if id != "" {
		var err error
		if session, name, err = protocol.ParseRescueID(id); err != nil {
			return res, err
		}
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return res, err
	}
	total, free, err := sharedSpace(s.SharedDir)
	if err != nil {
		return res, err
	}
	res.SharedTotalBytes, res.SharedFreeBytes = total, free
	if id == "" {
		res.OK = true
		return res, nil
	}
	unlock, err := lockShelfHow(s, syscall.LOCK_SH)
	if err != nil {
		return res, err
	}
	defer unlock()

	dir := protocol.RescueDir(session, name)
	res.Manifest = path.Join(dir, protocol.ManifestFile)
	refuse := func(code, format string, a ...any) (protocol.RescueVerifyResult, error) {
		res.OK, res.Refusal, res.Detail = false, code, fmt.Sprintf(format, a...)
		return res, nil
	}
	abs := filepath.Join(s.SharedDir, filepath.FromSlash(dir))
	for _, d := range []string{filepath.Dir(abs), abs} {
		fi, err := os.Lstat(d)
		switch {
		case isNotExist(err):
			return refuse(protocol.RescueVerifyBundleMissing, "rescue %s is not on the shared volume", id)
		case err != nil:
			return res, err
		case !fi.IsDir():
			return refuse(protocol.RescueVerifyBundleMissing, "%s is not a directory", relShared(s, d))
		}
	}
	data, err := readRegular(filepath.Join(abs, protocol.ManifestFile), maxManifestBytes)
	switch {
	case isNotExist(err):
		return refuse(protocol.RescueVerifyBundleMissing, "no manifest at %s", res.Manifest)
	case errors.Is(err, errNotRegular):
		return refuse(protocol.RescueVerifyBundleMissing, "%s is not a regular file", res.Manifest)
	case errors.Is(err, errTooLarge):
		return refuse(protocol.RescueVerifyBundleMismatch, "%s is larger than %d bytes", res.Manifest, maxManifestBytes)
	case err != nil:
		return res, err
	}
	sum := sha256.Sum256(data)
	res.ManifestSHA256 = hex.EncodeToString(sum[:])
	var m protocol.RescueManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return refuse(protocol.RescueVerifyBundleMismatch, "%s is not JSON: %v", res.Manifest, err)
	}
	switch {
	case m.Version != protocol.ManifestVersion:
		return refuse(protocol.RescueVerifyBundleMismatch, "%s is version %d; this agentd reads %d", res.Manifest, m.Version, protocol.ManifestVersion)
	case m.Session != session || m.Dir != dir:
		return refuse(protocol.RescueVerifyBundleMismatch, "%s names session %q and directory %q, not this rescue", res.Manifest, m.Session, m.Dir)
	case !m.Complete:
		return refuse(protocol.RescueVerifyBundleMismatch, "%s says the rescue did not complete", res.Manifest)
	}
	for _, repo := range m.Repos {
		res.Bundles++
		b := repo.Bundle
		res.Bytes += b.Size
		if b.File == "" || !b.Verified || b.SHA256 == "" || b.Size < 0 {
			return refuse(protocol.RescueVerifyBundleMismatch, "%s lists %s without a verified bundle", res.Manifest, repo.Path)
		}
		file := filepath.Join(s.SharedDir, filepath.FromSlash(b.File))
		if filepath.Dir(file) != abs {
			return refuse(protocol.RescueVerifyBundleMismatch, "the bundle %q is not in %s", b.File, dir)
		}
		size, got, err := hashRegular(file)
		switch {
		case isNotExist(err):
			return refuse(protocol.RescueVerifyBundleMissing, "%s is gone", b.File)
		case errors.Is(err, errNotRegular):
			return refuse(protocol.RescueVerifyBundleMissing, "%s is not a regular file", b.File)
		case err != nil:
			return res, err
		case size != b.Size:
			return refuse(protocol.RescueVerifyBundleMismatch, "%s is %d bytes; the manifest says %d", b.File, size, b.Size)
		case got != b.SHA256:
			return refuse(protocol.RescueVerifyBundleMismatch, "%s has SHA-256 %s; the manifest says %s", b.File, got, b.SHA256)
		}
	}
	res.OK = true
	return res, nil
}

var (
	errNotRegular = errors.New("not a regular file")
	errTooLarge   = errors.New("too large")
)

// openRegular opens p for reading without following a link at its last
// element, and checks the open file, not the name, is a regular file.
func openRegular(p string) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, nil, errNotRegular
	}
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
		return nil, nil, errNotRegular
	}
	return f, fi, nil
}

// readRegular reads a regular file of at most limit bytes.
func readRegular(p string, limit int64) ([]byte, error) {
	f, _, err := openRegular(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge
	}
	return data, nil
}

// hashRegular is a regular file's size, as read, and SHA-256.
func hashRegular(p string) (int64, string, error) {
	f, _, err := openRegular(p)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// sharedStatfs is the statfs sharedSpace calls; tests replace it.
var sharedStatfs = syscall.Statfs

// sharedSpace is the shared volume's total and free bytes, as df reports
// them (byteFigures).
func sharedSpace(dir string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := sharedStatfs(dir, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs the shared volume: %w", err)
	}
	t, a, ok := byteFigures(st)
	if !ok || t > math.MaxInt64 {
		return 0, 0, errors.New("statfs the shared volume: the figures contradict each other")
	}
	return int64(t), int64(a), nil
}

func relShared(s Settings, p string) string {
	if rel, err := filepath.Rel(s.SharedDir, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return p
}

// DropLog is `agentd ctl drop-log` in a hold pod (DESIGN-061 section 3): it
// removes the session's own log copy on the shared volume,
// logs/<session>.log, and nothing else. The copy must be a regular file in a
// real logs/ directory; a link or anything else is left in place and
// refused. An absent copy is not an error. It refuses while an agent this pod
// started is running, since that agent's agentd would copy the log again.
func DropLog(s Settings, session string) (protocol.DropLogResult, error) {
	res := protocol.DropLogResult{Session: session, Path: path.Join(protocol.LogsRoot, session+".log")}
	if !protocol.ValidSessionName(session) {
		return res, fmt.Errorf("session name %q is not one (AGENTD_SESSION)", session)
	}
	var p agentPid
	if readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p) {
		return res, errors.New("the agent is running in this pod; drop-log runs in a hold pod")
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return res, err
	}
	logs := filepath.Join(s.SharedDir, protocol.LogsRoot)
	fi, err := os.Lstat(logs)
	switch {
	case isNotExist(err):
		return res, nil
	case err != nil:
		return res, err
	case !fi.IsDir():
		return res, fmt.Errorf("%s on the shared volume is not a directory; nothing removed", protocol.LogsRoot)
	}
	file := s.SharedLogPath(session)
	fi, err = os.Lstat(file)
	switch {
	case isNotExist(err):
		return res, nil
	case err != nil:
		return res, err
	case !fi.Mode().IsRegular():
		return res, fmt.Errorf("%s is not a regular file; left in place", res.Path)
	}
	if err := os.Remove(file); err != nil {
		if isNotExist(err) {
			return res, nil
		}
		return res, err
	}
	res.Removed, res.Bytes = true, fi.Size()
	return res, nil
}
