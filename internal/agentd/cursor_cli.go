package agentd

// The pinned Cursor CLI for cursor tasks (DESIGN-058). The CLI is proprietary
// and never bundled in the public image: agentd downloads the pinned archive
// onto the session volume at boot, checks its SHA-256 before first use, and
// checks the installed version again before every launch.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The pin comes from v1's installer, haynes-ops
// kubernetes/main/apps/dev/dev-env/app/resources/cursor-worker.sh (haynes-ops
// commit 76a53ac9): its VERSION, its SHA256 and its download URL pattern. The
// archive unpacks to one top directory holding the cursor-agent launcher,
// which prints exactly the version.
const (
	cursorCLIVersion = "2026.10.14-0f9edde"
	cursorCLISHA256  = "74fac931fe3578980d070701872b7dab54eb87a9abf9b6e2e6be19beeb9167a7"
	cursorCLIURL     = "https://downloads.cursor.com/lab/" + cursorCLIVersion + "/linux/x64/agent-cli-package.tar.gz"
)

// cursorPin is one pinned release. Tests swap cursorCLIPin for a fixture.
type cursorPin struct{ Version, SHA256, URL string }

var cursorCLIPin = cursorPin{Version: cursorCLIVersion, SHA256: cursorCLISHA256, URL: cursorCLIURL}

// cursorHTTPClient fetches the archive. Tests point it at a local TLS server.
var cursorHTTPClient = func() *http.Client {
	return &http.Client{Timeout: cursorInstallTimeout, CheckRedirect: httpsRedirectsOnly}
}

// Bounds of an install: the archive, what it unpacks to and how long it takes.
const (
	cursorInstallTimeout    = 4 * time.Minute
	cursorVersionTimeout    = 10 * time.Second
	maxCursorArchiveBytes   = 512 << 20
	maxCursorExtractBytes   = 2 << 30
	maxCursorArchiveEntries = 100_000
)

// cursorCLIBinary is the launcher inside the unpacked package.
const cursorCLIBinary = "cursor-agent"

func httpsRedirectsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" || len(via) >= 5 {
		return errors.New("the Cursor CLI download redirected off https or too many times")
	}
	return nil
}

// cursorVersionsDir is where the pinned package lives on the session volume:
// the layout of Cursor's own installer and v1's, so the launcher runs from
// the directory it was built for.
func cursorVersionsDir(s Settings) string {
	return filepath.Join(s.Home, ".local", "share", "cursor-agent", "versions")
}

// cursorCLIPath is the pinned launcher. agentd runs it by this absolute path,
// never through ~/.local/bin/agent, which Cursor's auto-update may repoint.
func cursorCLIPath(s Settings) string {
	return filepath.Join(cursorVersionsDir(s), cursorCLIPin.Version, cursorCLIBinary)
}

// InstallCursorCLI makes sure the pinned Cursor CLI is on the session volume.
// An install that reports the pinned version is kept. Anything else at the
// pinned path is preserved for inspection and refused: agentd never replaces
// an unexpected install in place (v1 did the same). A fresh install downloads
// the archive, checks its SHA-256 before anything is unpacked, unpacks it in a
// staging directory, checks the version the launcher reports, and only then
// moves it into place.
func InstallCursorCLI(ctx context.Context, s Settings) error {
	pin := cursorCLIPin
	dir := filepath.Join(cursorVersionsDir(s), pin.Version)
	bin := filepath.Join(dir, cursorCLIBinary)
	if _, err := os.Lstat(dir); err == nil {
		if err := checkCursorVersion(ctx, bin, cursorVersionEnv(os.Environ())); err != nil {
			return fmt.Errorf("the pinned Cursor CLI at %s is not intact (%w); it is preserved for inspection and the task is not started", dir, err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the pinned Cursor CLI path is unreadable: %w", err)
	}
	root := cursorVersionsDir(s)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("cursor CLI install directory: %w", err)
	}
	stage, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return fmt.Errorf("cursor CLI staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	ctx, cancel := context.WithTimeout(ctx, cursorInstallTimeout)
	defer cancel()
	archive := filepath.Join(stage, "package.tgz")
	if err := downloadCursorArchive(ctx, pin, archive); err != nil {
		return err
	}
	pkg := filepath.Join(stage, "package")
	if err := unpackCursorArchive(archive, pkg); err != nil {
		return fmt.Errorf("the pinned Cursor CLI archive did not unpack: %w", err)
	}
	if err := checkCursorVersion(ctx, filepath.Join(pkg, cursorCLIBinary), cursorVersionEnv(os.Environ())); err != nil {
		return fmt.Errorf("the unpacked Cursor CLI is not the pin: %w", err)
	}
	if err := os.Rename(pkg, dir); err != nil {
		return fmt.Errorf("the pinned Cursor CLI could not be moved into place: %w", err)
	}
	return nil
}

// downloadCursorArchive writes the archive to dst and checks its SHA-256. A
// mismatch leaves nothing installed and says so.
func downloadCursorArchive(ctx context.Context, pin cursorPin, dst string) error {
	if !strings.HasPrefix(pin.URL, "https://") {
		return errors.New("the Cursor CLI pin is not an https URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return fmt.Errorf("cursor CLI download: %w", err)
	}
	resp, err := cursorHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("the pinned Cursor CLI could not be downloaded: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the pinned Cursor CLI download answered HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cursor CLI archive file: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxCursorArchiveBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("the pinned Cursor CLI download did not complete: %w", err)
	}
	if n > maxCursorArchiveBytes {
		return fmt.Errorf("the Cursor CLI archive is over %d bytes; nothing was installed", maxCursorArchiveBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != pin.SHA256 {
		return fmt.Errorf("the Cursor CLI archive's SHA-256 is %s, not the pinned %s; nothing was installed", got, pin.SHA256)
	}
	return nil
}

// unpackCursorArchive unpacks a gzip tar into dst, dropping the top directory
// as v1's tar --strip-components=1 did. It takes regular files, directories
// and relative symlinks that stay inside the package, writes nothing through
// a symlink, and refuses any other entry.
func unpackCursorArchive(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	if err := os.Mkdir(dst, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var links []string
	var total int64
	for entries := 0; ; entries++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entries >= maxCursorArchiveEntries {
			return fmt.Errorf("more than %d entries", maxCursorArchiveEntries)
		}
		rel, ok := strippedArchivePath(hdr.Name)
		if !ok {
			return fmt.Errorf("entry %q leaves the package", hdr.Name)
		}
		if rel == "" {
			continue
		}
		if slices.ContainsFunc(links, func(l string) bool { return rel == l || strings.HasPrefix(rel, l+"/") }) {
			return fmt.Errorf("entry %q is written through a symlink", hdr.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if hdr.Size < 0 || total > maxCursorExtractBytes {
				return fmt.Errorf("the package unpacks to more than %d bytes", maxCursorExtractBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.CopyN(out, tr, hdr.Size)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if path.IsAbs(hdr.Linkname) {
				return fmt.Errorf("symlink %q is absolute", hdr.Name)
			}
			if resolved := path.Clean(path.Join(path.Dir(rel), hdr.Linkname)); resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("symlink %q leaves the package", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
			links = append(links, rel)
		default:
			return fmt.Errorf("entry %q has unsupported type %q", hdr.Name, hdr.Typeflag)
		}
	}
}

// strippedArchivePath drops an entry's first path element. It reports false
// for an absolute name or one that climbs out of the package.
func strippedArchivePath(name string) (string, bool) {
	if strings.HasPrefix(name, "/") {
		return "", false
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	_, rest, _ := strings.Cut(clean, "/")
	return rest, true
}

// checkCursorVersion runs `<bin> --version` and compares it with the pin. It
// reads at most a small bounded output and never authenticates.
func checkCursorVersion(ctx context.Context, bin string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, cursorVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = env
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("it did not report its version: %w", err)
	}
	got := strings.TrimSpace(string(out))
	if got != cursorCLIPin.Version {
		if len(got) > 64 || strings.ContainsFunc(got, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			got = "an unreadable version"
		}
		return fmt.Errorf("it reports %q, not the pinned %s", got, cursorCLIPin.Version)
	}
	return nil
}

// cursorVersionEnv is the environment of a version check at install: the
// pod's, without the API key, which a version check does not need.
func cursorVersionEnv(base []string) []string {
	return slices.DeleteFunc(slices.Clone(base), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == cursorAPIKeyEnv
	})
}

// verifyCursorCLI is run-agent's check before it starts a cursor launch: the
// launch's own binary must still report the pin. Cursor's CLI updates itself
// by default and documents no switch to stop it, so a launcher that changed
// since boot fails the task with that reason instead of running.
func verifyCursorCLI(l Launch) error {
	if len(l.Argv) == 0 {
		return errors.New("the cursor launch has no command")
	}
	if err := checkCursorVersion(context.Background(), l.Argv[0], agentEnv(os.Environ(), l)); err != nil {
		return fmt.Errorf("the Cursor CLI no longer matches the pin, so the task is not started: %w", err)
	}
	return nil
}

// cursorAPIKeyEnv is the variable the Cursor CLI reads its key from. The
// operator sets it on cursor pods only (templates.CursorAPIKeyEnv).
const cursorAPIKeyEnv = "CURSOR_API_KEY"
