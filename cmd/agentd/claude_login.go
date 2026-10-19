package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/claudelogin"
)

// claudeLogin is `agentd claude-login [--login-dir D]`: the login helper for
// the keeper-owned shared Claude login (Story110 T09 increment 5). It runs in
// the keeper pod's claude-login container (the agent image, which already holds
// the pinned claude CLI) and is reached only through authenticated pods/exec
// with a terminal: pod-log or piped output is refused before the CLI starts.
// stdout gets one JSON line with the sign-in link, then one with the final fixed
// result; the one-time code is typed on stdin.
func claudeLogin(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) int {
	result := claudelogin.Response{Code: "InvalidRequest"}
	fs := flag.NewFlagSet("claude-login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("login-dir", claudelogin.DefaultDir, "keeper/helper private staging")
	in, inOK := stdin.(*os.File)
	out, outOK := stdout.(*os.File)
	if fs.Parse(args) == nil && fs.NArg() == 0 {
		result.Code = "AuthenticatedTerminalRequired"
		if inOK && outOK {
			_, ierr := unix.IoctlGetTermios(int(in.Fd()), unix.TCGETS)
			_, oerr := unix.IoctlGetTermios(int(out.Fd()), unix.TCGETS)
			if ierr == nil && oerr == nil {
				bin, err := exec.LookPath("claude")
				if err != nil {
					bin = "claude"
				}
				result, _ = claudelogin.Helper(ctx, *dir, claudelogin.Options{Bin: bin}, in, stdout)
			}
		}
	}
	if json.NewEncoder(stdout).Encode(result) != nil || !result.OK {
		return exitFailure
	}
	return exitOK
}

// claudeLoginIdle is the helper container's entrypoint: it does nothing until
// shutdown. The CLI runs only inside an authenticated claude-login exec.
func claudeLoginIdle(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintf(stderr, "%s: claude-login-idle takes no arguments, got %q\n", binaryName, args)
		return exitUsage
	}
	<-ctx.Done()
	return exitOK
}
