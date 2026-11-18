package agentd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// prepareCodexHostPackage permits one pinned predecessor, never arbitrary older
// packages. Native start installs a missing package but preserves an existing
// selection (prepare_install.rs, InstallMode::Missing). Only native --from-cli
// replaces it from the invoking image's complete package. No lifecycle receipt,
// login or history is read or removed here. The caller owns its lifecycle lock.
func prepareCodexHostPackage(ctx context.Context, s Settings, native func(context.Context, Settings, string, []string, io.Writer) ([]byte, error), absent func() error) error {
	managed, err := codexHostManagedBin(s)
	if errors.Is(err, os.ErrNotExist) {
		// A broken current link or incomplete package is not a fresh install.
		for _, root := range []string{"app-server-daemon", "standalone"} {
			if _, err := os.Lstat(filepath.Join(s.CodexHome, "packages", root, "current")); !errors.Is(err, os.ErrNotExist) {
				return errCodexHostUnknown
			}
		}
		return nil
	}
	if err != nil {
		return errCodexHostUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	version := func(bin string) string {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := native(probeCtx, s, bin, []string{"--version"}, nil)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	switch version(managed) {
	case "codex-cli " + managedCodexVersion:
		return nil
	case "codex-cli " + previousPinnedCodexVersion:
		if version(s.CodexBin) != "codex-cli "+managedCodexVersion || absent() != nil {
			return errCodexHostUnknown
		}
	default:
		return errCodexHostUnknown
	}
	if _, err := native(ctx, s, s.CodexBin, []string{"app-server", "daemon", "update", "--from-cli", "--yes"}, nil); err != nil {
		return errCodexHostUnknown
	}
	if absent() != nil {
		return errCodexHostUnknown
	}
	managed, err = codexHostManagedBin(s)
	if err != nil || version(managed) != "codex-cli "+managedCodexVersion {
		return errCodexHostUnknown
	}
	return nil
}
