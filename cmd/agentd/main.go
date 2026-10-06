// Command agentd is the supervisor inside each session pod, a child of tini
// (DESIGN-001 3.6). It renders config at boot, clones the repo, starts or
// resumes the agent in tmux, sends heartbeats, and answers
// `agentd ctl status|rescue|prepare-restart|deliver` for the operator.
//
// Built so far: the daemon (`run`), the task runner in the tmux pane
// (`run-agent`), `render` and `ctl status`. `ctl rescue` follows in plan 01
// step 4's last PR; `ctl prepare-restart` and `ctl deliver` in plan 02.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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
  run                      The supervisor tini starts: render the config, clone
                           the repo, start the agent in tmux session "agent",
                           then heartbeat until SIGTERM.
  render                   Render the GitOps config into $HOME (boot step 1).
  ctl status               Print the session's status as JSON.
  run-agent --launch FILE  Run the agent CLI; agentd starts this in tmux.
  version                  Print the version, commit, Go version and platform.
  help                     Print this help.

agentd reads the session from AGENTD_SESSION (JSON) or the file named by
AGENTD_SESSION_FILE (D-40). ctl rescue arrives with plan 01 step 4's last PR;
ctl prepare-restart and ctl deliver with plan 02.
`

// Daemon timings (DESIGN-001 3.6). stopGrace must fit inside the pod's
// termination grace period, which the operator sets longer.
const (
	heartbeatInterval = 60 * time.Second
	resultPoll        = 5 * time.Second
	stopGrace         = 30 * time.Second
)

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
	case "run":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "%s: run takes no arguments, got %q\n", binaryName, args[1:])
			return exitUsage
		}
		return daemon(ctx, log, getenv, r)
	case "run-agent":
		if len(args) != 3 || args[1] != "--launch" {
			_, _ = fmt.Fprintf(stderr, "%s: usage: run-agent --launch FILE\n", binaryName)
			return exitUsage
		}
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer signal.Stop(sigs)
		return agentd.RunAgent(args[2], stdout, sigs, stopGrace)
	case "ctl":
		return ctl(ctx, args[1:], stdout, stderr, getenv, r)
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

func daemon(ctx context.Context, log *slog.Logger, getenv func(string) string, r agentd.Runner) int {
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		log.Error("settings", "err", err)
		return exitFailure
	}
	sess, err := agentd.LoadSession(getenv)
	if err != nil {
		// Nothing useful can run without the session; the pod fails and the
		// operator reports it.
		log.Error("session", "err", err)
		return exitFailure
	}
	self, err := os.Executable()
	if err != nil {
		log.Error("own path", "err", err)
		return exitFailure
	}
	d := &agentd.Daemon{S: s, R: r, Log: log, Session: sess, Self: self, Interval: heartbeatInterval, Poll: resultPoll, StopGrace: stopGrace}
	if hb, err := agentd.NewHeartbeatClient(s, sess.Name); err != nil {
		log.Warn("heartbeats are off", "why", err)
	} else {
		d.Send = hb.Send
	}
	if err := d.Run(ctx); err != nil {
		log.Error("agentd", "err", err)
		return exitFailure
	}
	return exitOK
}

func ctl(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string, r agentd.Runner) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintf(stderr, "%s: usage: ctl status\n", binaryName)
		return exitUsage
	}
	switch args[0] {
	case "status":
		s, err := agentd.LoadSettings(getenv)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
			return exitFailure
		}
		sess, err := agentd.LoadSession(getenv)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
			return exitFailure
		}
		st := agentd.CollectStatus(ctx, r, s, sess.Name, time.Now())
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(st); err != nil {
			return exitFailure
		}
		return exitOK
	case "rescue":
		_, _ = fmt.Fprintf(stderr, "%s: ctl rescue is not built yet; it arrives with plan 01 step 4's last PR\n", binaryName)
		return exitFailure
	case "prepare-restart", "deliver":
		_, _ = fmt.Fprintf(stderr, "%s: ctl %s is not built yet; it arrives with plan 02\n", binaryName, args[0])
		return exitFailure
	default:
		_, _ = fmt.Fprintf(stderr, "%s: unknown ctl command %q\n", binaryName, args[0])
		return exitUsage
	}
}
