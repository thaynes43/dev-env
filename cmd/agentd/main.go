// Command agentd is the supervisor inside each session pod, a child of tini
// (DESIGN-001 3.6). It renders config at boot, clones the repo, starts or
// resumes the agent in tmux, sends heartbeats, and answers
// `agentd ctl status|rescue|prepare-restart|deliver` for the operator.
//
// Built so far: `render`, boot step 1 (the port of v1's dev-init.sh). The
// daemon (clone, tmux start, heartbeat) and `ctl status|rescue` follow in plan
// 01 step 4's next PRs.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/version"
)

const binaryName = "agentd"

// Exit codes: 1 is a failure, 2 a usage error.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const usage = `Usage: agentd <command>

Commands:
  render    Render the GitOps config into $HOME (boot step 1; idempotent).
  version   Print the version, commit, Go version and platform.
  help      Print this help.

agentd reads the session from AGENTD_SESSION (JSON) or the file named by
AGENTD_SESSION_FILE. The daemon and ctl status|rescue arrive with the next
plan 01 step 4 PRs.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv, agentd.ExecRunner{})
	stop()
	os.Exit(code)
}

// run is main without the process, so tests can call it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string, r agentd.Runner) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	log := slog.New(slog.NewTextHandler(stderr, nil)).With("component", binaryName)
	switch args[0] {
	case "version", "--version":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "%s: version takes no arguments, got %q\n", binaryName, args[1:])
			return exitUsage
		}
		_, _ = fmt.Fprintln(stdout, version.Get().String(binaryName))
		return exitOK
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	case "render":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "%s: render takes no arguments, got %q\n", binaryName, args[1:])
			return exitUsage
		}
		return render(ctx, log, getenv, r)
	default:
		_, _ = fmt.Fprintf(stderr, "%s: unknown command %q\n\n%s", binaryName, args[0], usage)
		return exitUsage
	}
}

func render(ctx context.Context, log *slog.Logger, getenv func(string) string, r agentd.Runner) int {
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		log.Error("settings", "err", err)
		return exitFailure
	}
	sess, err := agentd.LoadSession(getenv)
	if err != nil {
		log.Error("session", "err", err)
		return exitFailure
	}
	steps := agentd.Render(ctx, r, s, sess)
	agentd.LogSteps(log, steps)
	for _, st := range steps {
		if st.State == agentd.StepFail {
			return exitFailure
		}
	}
	return exitOK
}
