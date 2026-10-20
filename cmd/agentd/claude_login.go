package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

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
	// A hangup is the terminal going away (the operator's login route closing its
	// exec, NB-01): end the ceremony the way a cancel does, so the keeper's
	// attempt is cancelled and the staging wiped instead of waiting out its
	// fifteen minutes.
	ctx, stopHup := signal.NotifyContext(ctx, syscall.SIGHUP)
	defer stopHup()
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

// claudeLoginStatus is `agentd claude-login-status [--login-dir D]`: one fixed
// word from the keeper (Ready, PendingPublication or NeedsLogin) and the
// published generation. It needs no terminal and shows no credential, so the
// coordinator can check an adoption without reading a Secret.
func claudeLoginStatus(ctx context.Context, args []string, stdout io.Writer) int {
	result := claudelogin.Response{Code: "InvalidRequest"}
	fs := flag.NewFlagSet("claude-login-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("login-dir", claudelogin.DefaultDir, "keeper/helper private staging")
	if fs.Parse(args) == nil && fs.NArg() == 0 {
		result.Code = "Unavailable"
		if r, err := claudelogin.Control(ctx, *dir, "status", ""); err == nil {
			result = r
		}
	}
	if json.NewEncoder(stdout).Encode(result) != nil || !result.OK {
		return exitFailure
	}
	return exitOK
}

// claudeLoginAbandon is `agentd claude-login-abandon` (NB-01b): the operator's
// cancel of a Claude login ceremony. It signals the running claude-login helper
// in this container (the helper treats SIGHUP as a cancel) and prints one fixed
// word. No terminal is needed, no credential is read, and no helper running is a
// success.
func claudeLoginAbandon(ctx context.Context, args []string, stdout io.Writer) int {
	result := claudelogin.Response{Code: "InvalidRequest"}
	if len(args) == 0 {
		code := claudelogin.Abandon(ctx, claudelogin.AbandonOptions{})
		result = claudelogin.Response{OK: code != claudelogin.AbandonStuck, Code: code}
	}
	if json.NewEncoder(stdout).Encode(result) != nil || !result.OK {
		return exitFailure
	}
	return exitOK
}
