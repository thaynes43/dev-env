package agentd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// bundleTimeout bounds one clone's `git bundle create`. In a partial clone,
// git fetches any blob the bundle needs that the clone never downloaded.
var bundleTimeout = 5 * time.Minute

// sharedIsMounted checks that the shared directory is a filesystem of its own,
// not a plain directory on the session volume: a bundle there would go with
// the volume at archive. Tests, whose directories share one filesystem,
// replace it.
var sharedIsMounted = func(shared, home string) error {
	var sh, hm syscall.Stat_t
	if err := syscall.Stat(shared, &sh); err != nil {
		return fmt.Errorf("the shared volume is not mounted at %s: %w", shared, err)
	}
	if err := syscall.Stat(home, &hm); err != nil {
		return err
	}
	if sh.Dev == hm.Dev {
		return fmt.Errorf("%s is on the session volume, not the shared one; a bundle there would be deleted with the volume", shared)
	}
	return nil
}

// writeBundles is D-10 step 2 (D-48). For each clone with unpushed refs it
// writes one git bundle of those refs, thin against origin's default branch,
// into rescue/<session>/<name>/<clone>.bundle on the shared volume, checks it
// with `git bundle verify` and its list of heads, copies it there and reads it
// back, then writes manifest.json beside it last. Any failure makes the rescue
// not OK, which blocks archive (D-10). A rescue with nothing to bundle writes
// nothing to the shared volume.
func writeBundles(ctx context.Context, r Runner, s Settings, rep *protocol.RescueReport, now time.Time) {
	var need []int
	for i, rr := range rep.Repos {
		if len(rr.UnpushedRefs) > 0 {
			need = append(need, i)
		}
	}
	if len(need) == 0 {
		return
	}
	b := &protocol.BundleReport{}
	rep.Bundle = b
	fail := func(msg string) {
		b.Error = msg
		rep.OK = false
	}
	if rep.Session == "" {
		fail("no session name (AGENTD_SESSION), so no rescue directory to write to")
		return
	}
	if err := sharedIsMounted(s.SharedDir, s.Home); err != nil {
		fail(err.Error())
		return
	}
	name, abs, err := makeRescueDir(s.SharedDir, rep.Session, rep.Stamp)
	if err != nil {
		fail("rescue directory: " + err.Error())
		return
	}
	b.Dir = protocol.RescueDir(rep.Session, name)

	m := protocol.RescueManifest{
		Version: protocol.ManifestVersion, Session: rep.Session, Stamp: rep.Stamp, Dir: b.Dir,
		CreatedAt: now.UTC(), Complete: true, Repos: []protocol.ManifestRepo{},
	}
	for _, i := range need {
		rr := &rep.Repos[i]
		rb := bundleRepo(ctx, r, s, *rr, abs, b.Dir)
		rr.Bundle = &rb
		if rb.Error != "" {
			m.Complete = false
		}
		m.Repos = append(m.Repos, protocol.ManifestRepo{Path: rr.Path, Remote: originURL(ctx, r, s, rr.Path), Bundle: rb})
	}
	if !m.Complete {
		rep.OK = false
		b.Error = "a bundle could not be written; see the repos"
	}
	// The manifest goes last, so a directory without one is a rescue that
	// did not finish. It is written even when a bundle failed, to say so.
	if err := writeManifest(filepath.Join(abs, protocol.ManifestFile), m); err != nil {
		fail("manifest: " + err.Error())
		return
	}
	b.Manifest = b.Dir + "/" + protocol.ManifestFile
}

// makeRescueDir creates rescue/<session>/<stamp> on the shared volume, or
// <stamp>-2, -3 and so on when an earlier rescue in the same minute has it.
// os.Mkdir fails on an existing directory, so two rescues never share one.
func makeRescueDir(shared, session, stamp string) (string, string, error) {
	parent := filepath.Join(shared, protocol.RescueRoot, session)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", "", err
	}
	for i := 1; i <= 50; i++ {
		name := stamp
		if i > 1 {
			name = stamp + "-" + strconv.Itoa(i)
		}
		p := filepath.Join(parent, name)
		err := os.Mkdir(p, 0o700)
		if err == nil {
			return name, p, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("every name for %s is taken", stamp)
}

// bundleRepo writes and checks one clone's bundle.
func bundleRepo(ctx context.Context, r Runner, s Settings, rr protocol.RepoRescue, absDir, relDir string) protocol.RepoBundle {
	rb := protocol.RepoBundle{}
	fail := func(format string, a ...any) protocol.RepoBundle {
		rb.Error = fmt.Sprintf(format, a...)
		rb.Verified = false
		return rb
	}

	// A stash entry has no ref of its own but the newest; give each one a
	// ref for the bundle, and take them away again after.
	var temp []string
	defer func() {
		for _, ref := range temp {
			_, _ = s.git(context.WithoutCancel(ctx), r, rr.Path, "update-ref", "-d", ref)
		}
	}()
	for _, ref := range rr.UnpushedRefs {
		name := ref.Name
		if n, ok := stashIndex(ref.Name); ok {
			name = protocol.StashRefPrefix + strconv.Itoa(n)
			if _, err := s.git(ctx, r, rr.Path, "update-ref", "-m", "agentd rescue bundle", name, ref.Commit); err != nil {
				return fail("anchor %s: %s", ref.Name, cmdDetail(err))
			}
			temp = append(temp, name)
		}
		rb.Refs = append(rb.Refs, protocol.BundleRef{Name: name, Source: ref.Name, Commit: ref.Commit})
	}

	// Thin against origin's default branch, which is never rewritten; a
	// feature branch on origin may be deleted after its merge, and a
	// bundle that needed its commits could no longer be restored.
	rb.Base = "refs/remotes/origin/HEAD"
	if _, err := s.git(ctx, r, rr.Path, "rev-parse", "--verify", "--quiet", rb.Base); err != nil {
		rb.Base = "--remotes=origin"
	}

	tmp, err := os.CreateTemp(s.StateDir, "rescue-*.bundle")
	if err != nil {
		return fail("temporary file: %v", err)
	}
	local := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(local) }()
	args := []string{"-C", rr.Path, "bundle", "create", "--quiet", local}
	for _, br := range rb.Refs {
		args = append(args, br.Name)
	}
	args = append(args, "--not", rb.Base)
	bctx, cancel := context.WithTimeout(ctx, bundleTimeout)
	_, err = r.Run(bctx, Cmd{Name: "git", Args: args, Env: gitEnv})
	cancel()
	if err != nil {
		if bctx.Err() == context.DeadlineExceeded {
			return fail("git bundle create: no answer within %s", bundleTimeout)
		}
		return fail("git bundle create: %s", cmdDetail(err))
	}
	if err := checkBundle(ctx, r, s, rr.Path, local, rb.Refs); err != nil {
		return fail("%v", err)
	}

	rb.File = relDir + "/" + filepath.Base(rr.Path) + ".bundle"
	dst := filepath.Join(absDir, filepath.Base(rr.Path)+".bundle")
	sum, size, err := copyDurable(local, dst)
	if err != nil {
		return fail("copy to the shared volume: %v", err)
	}
	// Read it back from the shared volume: the bytes there are git's.
	back, backSize, err := hashFile(dst)
	switch {
	case err != nil:
		return fail("read back %s: %v", rb.File, err)
	case back != sum || backSize != size:
		return fail("read back %s: %d bytes with sha256 %s, wrote %d bytes with %s", rb.File, backSize, back, size, sum)
	}
	if err := checkBundle(ctx, r, s, rr.Path, dst, rb.Refs); err != nil {
		return fail("after the copy: %v", err)
	}
	rb.SHA256, rb.Size, rb.Verified = sum, size, true
	return rb
}

// checkBundle runs `git bundle verify` in the clone, which checks the bundle's
// format and that the clone has its prerequisites, and checks that its heads
// are exactly the refs it should hold.
func checkBundle(ctx context.Context, r Runner, s Settings, repo, file string, want []protocol.BundleRef) error {
	if _, err := s.git(ctx, r, repo, "bundle", "verify", "--quiet", file); err != nil {
		return fmt.Errorf("git bundle verify: %s", cmdDetail(err))
	}
	out, err := s.git(ctx, r, repo, "bundle", "list-heads", file)
	if err != nil {
		return fmt.Errorf("git bundle list-heads: %s", cmdDetail(err))
	}
	heads := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if sha, name, ok := strings.Cut(sc.Text(), " "); ok {
			heads[name] = sha
		}
	}
	for _, ref := range want {
		got, ok := heads[ref.Name]
		if !ok {
			return fmt.Errorf("the bundle lacks %s", ref.Name)
		}
		if got != ref.Commit {
			return fmt.Errorf("the bundle has %s at %s, want %s", ref.Name, got, ref.Commit)
		}
		delete(heads, ref.Name)
	}
	if len(heads) > 0 {
		return fmt.Errorf("the bundle holds refs it should not: %s", strings.Join(slices.Sorted(maps.Keys(heads)), ", "))
	}
	return nil
}

// stashIndex parses stash@{n}.
func stashIndex(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "stash@{")
	if !ok {
		return 0, false
	}
	num, ok := strings.CutSuffix(rest, "}")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil && n >= 0
}

// originURL is origin's URL with any credentials taken out, or "".
func originURL(ctx context.Context, r Runner, s Settings, repo string) string {
	raw, err := s.git(ctx, r, repo, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	if strings.Contains(raw, "@") && strings.Contains(raw, "://") {
		// A URL with credentials that does not parse: leave it out.
		return ""
	}
	return raw
}

// copyDurable copies src to dst durably; see writeDurable.
func copyDurable(src, dst string) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = in.Close() }()
	return writeDurable(dst, in)
}

// writeDurable writes what src holds to dst through a temporary file beside
// dst, syncs it and its directory, and returns the sha256 and size of what it
// wrote. A reader sees no file at dst or the whole one.
func writeDurable(dst string, src io.Reader) (string, int64, error) {
	dir := filepath.Dir(dst)
	out, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".part-*")
	if err != nil {
		return "", 0, err
	}
	tmp := out.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), src)
	if err == nil {
		err = out.Chmod(0o600)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", 0, err
	}
	ok = true
	if err := syncDir(dir); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// writeManifest writes the manifest durably and reads it back.
func writeManifest(path string, m protocol.RescueManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, _, err := writeDurable(path, bytes.NewReader(data)); err != nil {
		return err
	}
	back, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read back: %w", err)
	}
	if !bytes.Equal(back, data) {
		return errors.New("read back: the manifest differs from what was written")
	}
	return nil
}
