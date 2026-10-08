package agentd

import (
	"context"
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
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The shelf (D-67): the rescues and logs on the shared volume, listed and
// pruned for the operator by `agentd ctl rescues` and `agentd ctl prune` in the
// shelf pod, and a rescue restored into a new session's clone on its first boot.

// maxManifestBytes caps a manifest read. A manifest lists refs, not files, so
// even a large one is a few kilobytes.
const maxManifestBytes = 4 << 20

// ListRescues lists the rescue directories on the shared volume, newest
// first; with session set, only that session's.
func ListRescues(s Settings, session string) (protocol.RescueList, error) {
	list := protocol.RescueList{Rescues: []protocol.RescueEntry{}}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return list, err
	}
	root := filepath.Join(s.SharedDir, protocol.RescueRoot)
	sessions, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return list, nil
	}
	if err != nil {
		return list, err
	}
	for _, sd := range sessions {
		rel := path.Join(protocol.RescueRoot, sd.Name())
		if !sd.IsDir() || !protocol.ValidSessionName(sd.Name()) {
			list.Unrecognized = append(list.Unrecognized, rel)
			continue
		}
		if session != "" && sd.Name() != session {
			continue
		}
		rescues, err := os.ReadDir(filepath.Join(root, sd.Name()))
		if err != nil {
			return list, err
		}
		for _, rd := range rescues {
			if !rd.IsDir() || !protocol.ValidRescueName(rd.Name()) {
				list.Unrecognized = append(list.Unrecognized, path.Join(rel, rd.Name()))
				continue
			}
			list.Rescues = append(list.Rescues, readRescue(s, sd.Name(), rd.Name()))
		}
	}
	sort.SliceStable(list.Rescues, func(i, j int) bool {
		a, b := list.Rescues[i], list.Rescues[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID > b.ID
	})
	return list, nil
}

// readRescue describes one rescue directory. Its problems go in the entry's
// Error, so one bad directory never hides the others.
func readRescue(s Settings, session, name string) protocol.RescueEntry {
	dir := protocol.RescueDir(session, name)
	e := protocol.RescueEntry{ID: protocol.RescueID(session, name), Session: session, Name: name, Dir: dir}
	abs := filepath.Join(s.SharedDir, filepath.FromSlash(dir))
	if fi, err := os.Lstat(abs); err == nil {
		e.ModifiedAt = fi.ModTime().UTC()
		e.CreatedAt = e.ModifiedAt
	}
	e.Bytes = treeBytes(abs)
	m, err := readManifest(filepath.Join(abs, protocol.ManifestFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A rescue that did not finish (D-48).
	case err != nil:
		e.Finished = true
		e.Error = err.Error()
	default:
		e.Finished = true
		e.Manifest = &m
		if !m.CreatedAt.IsZero() {
			e.CreatedAt = m.CreatedAt.UTC()
		}
		if m.Session != session || m.Dir != dir {
			e.Error = fmt.Sprintf("the manifest names session %q and directory %q, not this one", m.Session, m.Dir)
		}
	}
	return e
}

// readManifest reads a manifest that is a regular file, never through a link.
func readManifest(p string) (protocol.RescueManifest, error) {
	var m protocol.RescueManifest
	fi, err := os.Lstat(p)
	if err != nil {
		return m, err
	}
	if !fi.Mode().IsRegular() {
		return m, fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	f, err := os.Open(p)
	if err != nil {
		return m, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return m, err
	}
	if len(data) > maxManifestBytes {
		return m, fmt.Errorf("the manifest is larger than %d bytes", maxManifestBytes)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("the manifest is not JSON: %w", err)
	}
	return m, nil
}

// treeBytes is the size of the regular files under dir. WalkDir never follows
// a link.
func treeBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// Prune removes the rescue directories and session logs on the shared volume
// that are older than the request's retention and belong to no session in its
// Keep list (D-67). A session's empty rescue directory goes with its last
// rescue. Paths that do not fit the layout are never touched.
func Prune(s Settings, req protocol.PruneRequest, now time.Time) (protocol.PruneReport, error) {
	rep := protocol.PruneReport{OlderThan: req.OlderThan, DryRun: req.DryRun, Removed: []protocol.PrunedPath{}}
	olderThan, err := time.ParseDuration(req.OlderThan)
	if err != nil {
		return rep, fmt.Errorf("olderThan %q is not a Go duration", req.OlderThan)
	}
	if olderThan < protocol.MinPruneAge {
		return rep, fmt.Errorf("olderThan %s is below the floor of %s; a prune never removes anything that young", olderThan, protocol.MinPruneAge)
	}
	keep := make(map[string]bool, len(req.Keep))
	for _, k := range req.Keep {
		keep[k] = true
	}
	list, err := ListRescues(s, "")
	if err != nil {
		return rep, err
	}

	emptied := map[string]bool{}
	for _, e := range list.Rescues {
		age := e.Age(now)
		if keep[e.Session] || age < olderThan {
			rep.Kept++
			continue
		}
		p := protocol.PrunedPath{Path: e.Dir, Session: e.Session, Age: age.Round(time.Minute).String(), Bytes: e.Bytes}
		if !req.DryRun {
			if err := removeDir(filepath.Join(s.SharedDir, filepath.FromSlash(e.Dir))); err != nil {
				rep.Errors = append(rep.Errors, e.Dir+": "+err.Error())
				continue
			}
			emptied[e.Session] = true
		}
		rep.Removed = append(rep.Removed, p)
	}
	for session := range emptied {
		// Fails, and is meant to, while the directory still holds anything.
		_ = os.Remove(filepath.Join(s.SharedDir, protocol.RescueRoot, session))
	}

	logs, err := os.ReadDir(filepath.Join(s.SharedDir, protocol.LogsRoot))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rep, err
	}
	for _, l := range logs {
		session, ok := strings.CutSuffix(l.Name(), ".log")
		if !ok || !l.Type().IsRegular() || !protocol.ValidSessionName(session) {
			continue
		}
		fi, err := l.Info()
		if err != nil {
			continue
		}
		age := now.Sub(fi.ModTime())
		if keep[session] || age < olderThan {
			rep.Kept++
			continue
		}
		rel := path.Join(protocol.LogsRoot, l.Name())
		if !req.DryRun {
			if err := os.Remove(filepath.Join(s.SharedDir, filepath.FromSlash(rel))); err != nil {
				rep.Errors = append(rep.Errors, rel+": "+err.Error())
				continue
			}
		}
		rep.Removed = append(rep.Removed, protocol.PrunedPath{Path: rel, Session: session, Age: age.Round(time.Minute).String(), Bytes: fi.Size()})
	}
	return rep, nil
}

// removeDir removes a rescue directory, after checking it is still a
// directory and not a link that now points somewhere else.
func removeDir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("no longer a directory")
	}
	return os.RemoveAll(p)
}

// restoreRescue fetches the bundle a rescue holds for this session's repo into
// refs/rescued/* of the clone (D-48's recipe, D-67): the manifest must be
// complete and name this repo, and the bundle must match the manifest's size
// and SHA-256 and pass `git bundle verify` before anything is fetched.
func restoreRescue(ctx context.Context, r Runner, s Settings, sess protocol.Session, clone string) (string, error) {
	dir, err := protocol.RescueDirOf(sess.Restore)
	if err != nil {
		return "", err
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		return "", fmt.Errorf("restore %s: %w", sess.Restore, err)
	}
	abs := filepath.Join(s.SharedDir, filepath.FromSlash(dir))
	m, err := readManifest(filepath.Join(abs, protocol.ManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("restore %s: no manifest at %s: the rescue is gone or never finished", sess.Restore, path.Join(dir, protocol.ManifestFile))
	}
	if err != nil {
		return "", fmt.Errorf("restore %s: %w", sess.Restore, err)
	}
	if !m.Complete {
		return "", fmt.Errorf("restore %s: the rescue did not complete, so its bundles are not trusted", sess.Restore)
	}
	var repo *protocol.ManifestRepo
	var names []string
	for i := range m.Repos {
		names = append(names, filepath.Base(m.Repos[i].Path))
		if filepath.Base(m.Repos[i].Path) == sess.Repo {
			repo = &m.Repos[i]
		}
	}
	if repo == nil {
		return "", fmt.Errorf("restore %s: it holds no bundle for %s (it holds: %s)", sess.Restore, sess.Repo, strings.Join(names, ", "))
	}
	b := repo.Bundle
	file := filepath.Join(s.SharedDir, filepath.FromSlash(b.File))
	if rel, err := filepath.Rel(abs, file); err != nil || b.File == "" || strings.HasPrefix(rel, "..") || rel == "." {
		return "", fmt.Errorf("restore %s: the manifest's bundle %q is not inside %s", sess.Restore, b.File, dir)
	}
	if err := checkBundleFile(file, b.Size, b.SHA256); err != nil {
		return "", fmt.Errorf("restore %s: %w", sess.Restore, err)
	}
	if _, err := s.git(ctx, r, clone, "bundle", "verify", "--quiet", file); err != nil {
		return "", fmt.Errorf("restore %s: git bundle verify: %s", sess.Restore, cmdDetail(err))
	}
	if _, err := s.git(ctx, r, clone, "fetch", "--quiet", file, "refs/*:refs/rescued/*"); err != nil {
		return "", fmt.Errorf("restore %s: fetch the bundle: %s", sess.Restore, cmdDetail(err))
	}
	return fmt.Sprintf("restored %d refs from rescue %s into refs/rescued/ (size, SHA-256 and git bundle verify checked)", len(b.Refs), sess.Restore), nil
}

// checkBundleFile compares a bundle on the shared volume with its manifest.
func checkBundleFile(p string, size int64, sum string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filepath.Base(p))
	}
	if fi.Size() != size {
		return fmt.Errorf("%s is %d bytes, the manifest says %d", filepath.Base(p), fi.Size(), size)
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("%s has SHA-256 %s, the manifest says %s", filepath.Base(p), got, sum)
	}
	return nil
}
