//go:build linux

package usagecollector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestCursorOwnPTYBuiltinAndBoundedCancellation(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if os.Mkdir(workspace, 0700) != nil {
		t.Fatal("private workspace")
	}
	fixture := filepath.Join(dir, "cursor-fixture")
	// No provider, login, model or user terminal. The fixture validates every
	// input and emits only the same separately observed local UI state order.
	script := "#!/bin/sh\nstty -echo -icanon\nprintf 'Cursor Agent v" + providerallowance.CursorCLIQualifiedVersion + "\\nPlan, search, build anything\\n%s\\n' \"$2\"\nfirst=$(dd bs=1 count=6 2>/dev/null)\n[ \"$first\" = /usage ] || exit 21\nprintf '→ /usage\\nShow plan and on-demand usage\\n'\nIFS= read -r second\n[ -z \"$second\" ] || exit 22\nprintf '%s' '" + strings.ReplaceAll(cursorScreen, "'", "'\\''") + "'\n/bin/sleep 60\n"
	if os.WriteFile(fixture, []byte(script), 0700) != nil {
		t.Fatal("fixture write")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	report, err := CollectCursor(ctx, CursorOptions{Executable: fixture, Workspace: workspace}, time.Now)
	if err != nil || report.AccountRef != nil || report.IncludedPercentDisplay == nil || *report.IncludedPercentDisplay != 61 || report.CollectionStartedAt == "" || report.ReceivedAt == "" || time.Since(start) > time.Second {
		t.Fatal("own normal builtin fixture failed or leaked", err)
	}
	if os.WriteFile(fixture, []byte("#!/bin/sh\n/bin/sleep 60\n"), 0700) != nil {
		t.Fatal("fixture write")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	start = time.Now()
	if _, err = CollectCursor(ctx2, CursorOptions{Executable: fixture, Workspace: workspace}, time.Now); err == nil || time.Since(start) > time.Second {
		t.Fatal("terminal inherited output defeated cancellation", err)
	}
}

func TestCursorRefusesNonemptyOrUnsafeWorkspaceBeforeProcess(t *testing.T) {
	workspace := t.TempDir()
	if os.Chmod(workspace, 0755) != nil {
		t.Fatal("chmod")
	}
	if _, err := CollectCursor(context.Background(), CursorOptions{Executable: "/never-start", Workspace: workspace}, time.Now); err == nil {
		t.Fatal("unsafe workspace accepted")
	}
	if os.Chmod(workspace, 0700) != nil || os.WriteFile(filepath.Join(workspace, "user-work"), nil, 0600) != nil {
		t.Fatal("setup")
	}
	if _, err := CollectCursor(context.Background(), CursorOptions{Executable: "/never-start", Workspace: workspace}, time.Now); err == nil {
		t.Fatal("existing work accepted")
	}
}
