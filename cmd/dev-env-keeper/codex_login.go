package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/keeper"
)

// All helper subcommands dispatch before Kubernetes/App/manager setup. The idle
// entrypoint does nothing until shutdown. Login refuses pod-log/pipe output: it
// must be an authenticated interactive control/exec with a terminal.
func codexLoginCommand(args []string, in, out *os.File) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if len(args) == 1 && args[0] == "codex-login-idle" {
		<-ctx.Done()
		return 0
	}
	if len(args) == 0 {
		return 1
	}
	result := keeper.CodexControlResponse{Code: "InvalidRequest"}
	fs := flag.NewFlagSet("codex-login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("codex-login-dir", keeper.DefaultCodexLoginDir, "keeper/helper private staging")
	method := fs.String("method", "device", "pinned native ceremony: device or browser")
	expected := fs.Uint64("expected-generation", 0, "one confirmed keeper generation")
	if fs.Parse(args[1:]) == nil && fs.NArg() == 0 && len(args) > 0 {
		switch args[0] {
		case "codex-auth-status":
			if *expected == 0 {
				if r, err := keeper.CodexControl(ctx, *dir, "status", ""); err == nil {
					result = r
				}
			}
		case "codex-auth-refresh-once":
			if r, err := keeper.CodexRefreshOnce(ctx, *dir, *expected); err == nil {
				result = r
			}
		case "codex-login":
			if *expected != 0 {
				break
			}
			_, ierr := unix.IoctlGetTermios(int(in.Fd()), unix.TCGETS)
			_, oerr := unix.IoctlGetTermios(int(out.Fd()), unix.TCGETS)
			if ierr == nil && oerr == nil {
				result.Code = "NeedsLogin"
				result, _ = keeper.CodexLoginHelper(ctx, *dir, *method, in, out)
			} else {
				result.Code = "AuthenticatedTerminalRequired"
			}
		}
	}
	if json.NewEncoder(out).Encode(result) != nil || !result.OK {
		return 1
	}
	return 0
}
