package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFileAndMintSourcesRefuseUnboundedTokens(t *testing.T) {
	for _, kind := range []string{"fifo", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "token")
			var err error
			if kind == "fifo" {
				err = syscall.Mkfifo(path, 0o600)
			} else {
				err = os.WriteFile(path, []byte(strings.Repeat("x", 64<<10+1)), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (&fileToken{path: path, from: "synthetic"}).Token(context.Background()); err == nil {
				t.Fatal("file source accepted unsafe token file")
			}
			env := Env{ServiceAccountDir: dir}
			if _, err := newMinter(env); err == nil {
				t.Fatal("mint discovery accepted unsafe token file")
			}
			// An uncached request must fail before any mint HTTP client is needed.
			if _, err := (&minter{env: env}).Token(context.Background()); err == nil {
				t.Fatal("mint refresh accepted unsafe token file")
			}
		})
	}
}
