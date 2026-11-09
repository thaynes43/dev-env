package testsock

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShortDirIsShortPrivateAndRemoved(t *testing.T) {
	var dir string
	t.Run("inner", func(t *testing.T) {
		dir = ShortDir(t, "ts")
		if !strings.HasPrefix(dir, "/tmp/ts") || len(dir) > 20 {
			t.Fatalf("directory %q is not a short /tmp path", dir)
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %q: info %v err %v", dir, info, err)
		}
		l, err := net.Listen("unix", filepath.Join(dir, "a.sock"))
		if err != nil {
			t.Fatalf("a socket in the short directory must bind: %v", err)
		}
		_ = l.Close()
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory %q must be removed at the end of the test, err %v", dir, err)
	}
}
