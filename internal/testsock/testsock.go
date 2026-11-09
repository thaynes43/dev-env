// Package testsock gives tests a short directory for Unix sockets.
//
// A Unix socket path is limited to 108 bytes (sun_path). t.TempDir() is not
// short enough to rely on: Go 1.27 puts it under GOTMPDIR whatever TMPDIR says,
// and the agent image sets GOTMPDIR to /home/dev/.cache/go-tmp, so a socket
// path built from it passes the limit and bind fails with "invalid argument".
// Even under /tmp, a long test name and a nested fixture can reach about 101
// bytes. A test that binds, listens on or renames a socket makes its directory
// here and keeps the rest of its fixture under t.TempDir().
//
// Test support only: import it from _test.go files, never from production code.
package testsock

import (
	"os"
	"testing"
)

// base is a literal short directory. It is not os.TempDir or t.TempDir, which
// follow TMPDIR and GOTMPDIR.
const base = "/tmp"

// ShortDir returns a new empty directory (mode 0700) named /tmp/<prefix><random>
// for a Unix socket, removed when the test ends. Keep prefix to two or three
// letters so the whole path stays well under 108 bytes.
func ShortDir(tb testing.TB, prefix string) string {
	tb.Helper()
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		tb.Fatalf("testsock: make short socket directory: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		tb.Fatalf("testsock: chmod short socket directory: %v", err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
