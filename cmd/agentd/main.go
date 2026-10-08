// Command agentd is the supervisor inside each session pod, a child of tini
// (DESIGN-001 3.6). It renders config at boot, clones the repo, starts or
// resumes the agent in tmux, sends heartbeats, and answers
// `agentd ctl status|rescue|prepare-restart|deliver` for the operator.
//
// Built so far: the daemon (`run`), the rescue pod's `hold`, the agent runner
// in the tmux pane (`run-agent`), `render`, `ctl status`, `ctl rescue` and
// `ctl prepare-restart`, `ctl deliver` and `ctl log`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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
  hold                     The rescue pod's command (D-55): hold the session
                           volume for the operator's rescue and start nothing
                           (no config, clone, agent or heartbeat), until SIGTERM.
  shelf                    The shelf pod's command (D-67): check that the shared
                           volume is mounted, then wait until SIGTERM. The
                           operator lists and prunes rescues here by exec.
  render                   Render the GitOps config into $HOME (boot step 1).
  ctl status               Print the session's status as JSON.
  ctl rescue [--stop-agent]
                           Commit every worktree's uncommitted work to a local
                           rescue/ branch, write a bundle of every ref origin
                           lacks to the shared volume (rescue/<session>/<stamp>/,
                           D-48), and print the report as JSON; exit 1 when a
                           worktree could not be rescued or a bundle could not
                           be written (D-43). --stop-agent stops the agent CLI
                           first, as the operator does before a suspend.
  ctl deliver --from S     Deliver the message on stdin to the agent (D-65): a
                           bracketed paste into the Claude TUI, or codex queue.
                           Exit 3 when the agent takes no message (a headless
                           task, or no agent running).
  ctl log [--tail N]       Print the last N lines (default 200) of the session's
                           log, or of its copy on the shared volume.
  ctl rescues [--session S]
                           List the rescues on the shared volume as JSON,
                           newest first (D-67).
  ctl hold-rescue ID       A restore's check (D-67): find the rescue <session>/
                           <stamp> under the shelf lock and set its directory's
                           time to now, so no prune takes it before the new
                           session keeps it; print {"found":...} as JSON.
  ctl prune                Remove rescues and session logs older than the
                           request's retention whose session is not in its keep
                           list; the request is JSON on stdin, the report JSON
                           on stdout (D-67).
  ctl prepare-restart      Print what the next boot resumes (the conversation in
                           ~/.agentd/launch.json) as JSON, then stop the agent
                           CLI as the pod's SIGTERM would (D-58).
  run-agent --launch FILE  Run the agent CLI; agentd starts this in tmux.
  ctl grant-install        Install a kube grant; token on stdin, pod UID required.
  ctl grant-remove         Remove a kube grant; pod UID required.
  ctl grant-list           List this pod's grants and their expiry.
  ctl grant-use            Select a grant context or default.
  ctl credential-install  Install a private typed credential from JSON stdin.
  ctl credential-remove   Remove a credential fenced by grant and pod UIDs.
  ctl credential-list     List public credential metadata, with -o json.
  ctl credential-available Check for a live credential without printing material.
  ctl credential-use      Run a command with a live credential in its environment.
  ctl credential-expire   Remove expired credential files from the grants volume.
  version                  Print the version, commit, Go version and platform.
  help                     Print this help.

agentd reads the session from AGENTD_SESSION (JSON) or the file named by
AGENTD_SESSION_FILE (D-40).
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
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv, agentd.ExecRunner{})
	stop()
	os.Exit(code)
}

// run is main without the process, so tests can call it.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, r agentd.Runner) int {
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
	case "shelf":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "%s: shelf takes no arguments, got %q\n", binaryName, args[1:])
			return exitUsage
		}
		return shelf(ctx, log, getenv)
	case "hold":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "%s: hold takes no arguments, got %q\n", binaryName, args[1:])
			return exitUsage
		}
		return hold(ctx, log, getenv)
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
		return ctl(ctx, args[1:], stdin, stdout, stderr, getenv, r)
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

// hold is the rescue pod's command (D-55). The operator gives a session whose
// volume needs a rescue but has no pod a pod that runs this, then runs `agentd
// ctl rescue` in it by exec, as in any session pod. hold starts nothing and
// writes nothing: no config, no clone, no agent and no heartbeat. A task never
// runs twice (D-42), and an empty volume stays empty, which is how the rescue
// proves it holds no work.
func hold(ctx context.Context, log *slog.Logger, getenv func(string) string) int {
	name := ""
	if sess, err := agentd.LoadSession(getenv); err == nil {
		name = sess.Name
	} else {
		log.Warn("hold without a session; the rescue's report will name none", "err", err)
	}
	log.Info("holding the session volume for the operator's rescue; no agent starts in this pod", "session", name)
	<-ctx.Done()
	log.Info("hold ends", "session", name)
	return exitOK
}

// shelf is the shelf pod's command (D-67). It refuses to run without the
// shared volume, so a shelf that cannot see the rescues never looks healthy.
func shelf(ctx context.Context, log *slog.Logger, getenv func(string) string) int {
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		log.Error("settings", "err", err)
		return exitFailure
	}
	list, err := agentd.ListRescues(s, "")
	if err != nil {
		log.Error("the shelf needs the shared volume", "err", err)
		return exitFailure
	}
	log.Info("shelf ready; the operator lists and prunes rescues here by exec", "shared", s.SharedDir, "rescues", len(list.Rescues))
	<-ctx.Done()
	log.Info("shelf ends")
	return exitOK
}

// shelfCtl is `ctl rescues [--session S]`, `ctl hold-rescue <id>` and `ctl
// prune` (D-67).
func shelfCtl(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("ctl "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	session := fs.String("session", "", "")
	wantArgs := 0
	if args[0] == "hold-rescue" {
		wantArgs = 1
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != wantArgs || (args[0] != "rescues" && *session != "") {
		_, _ = fmt.Fprintf(stderr, "%s: usage: ctl rescues [--session S] | ctl hold-rescue <session>/<stamp> | ctl prune (the request on stdin)\n", binaryName)
		return exitUsage
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
		return exitFailure
	}
	var out any
	switch args[0] {
	case "hold-rescue":
		res, err := agentd.HoldRescue(s, fs.Arg(0), time.Now())
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: hold-rescue: %v\n", binaryName, err)
			return exitFailure
		}
		out = res
	case "rescues":
		list, err := agentd.ListRescues(s, *session)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: rescues: %v\n", binaryName, err)
			return exitFailure
		}
		out = list
	default:
		var req protocol.PruneRequest
		data, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err == nil {
			err = json.Unmarshal(data, &req)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: prune: read the request: %v\n", binaryName, err)
			return exitUsage
		}
		rep, err := agentd.Prune(s, req, time.Now())
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: prune: %v\n", binaryName, err)
			return exitFailure
		}
		out = rep
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return exitFailure
	}
	return exitOK
}

// exitNotAddressable is ctl deliver's exit code for a session that takes no
// message now (agentd.ErrNotAddressable); the API answers it with a 409.
const exitNotAddressable = 3

// exitNoLog is ctl log's exit code for a session with no log yet; the API
// answers it with a 404.
const exitNoLog = 4

// ctlMessageOrLog is `ctl deliver --from <caller>` (the message on stdin) and
// `ctl log [--tail N]` (D-65).
func ctlMessageOrLog(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, r agentd.Runner) int {
	fs := flag.NewFlagSet("ctl "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "")
	tail := fs.Int("tail", 200, "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || (args[0] == "log" && (*from != "" || *tail < 1 || *tail > 5000)) || (args[0] == "deliver" && *from == "") {
		_, _ = fmt.Fprintf(stderr, "%s: usage: ctl deliver --from <sender> (the message on stdin) | ctl log [--tail N, 1 to 5000]\n", binaryName)
		return exitUsage
	}
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
	if args[0] == "log" {
		if err := agentd.TailLog(s, sess.Name, *tail, stdout); err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
			if errors.Is(err, agentd.ErrNoLog) {
				return exitNoLog
			}
			return exitFailure
		}
		return exitOK
	}
	text, err := io.ReadAll(io.LimitReader(stdin, agentd.MaxMessageBytes+1))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: read the message: %v\n", binaryName, err)
		return exitFailure
	}
	if err := agentd.Deliver(ctx, r, s, sess, *from, string(text)); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: deliver: %v\n", binaryName, err)
		if errors.Is(err, agentd.ErrNotAddressable) {
			return exitNotAddressable
		}
		return exitFailure
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

func ctl(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, r agentd.Runner) int {
	if len(args) > 0 {
		switch args[0] {
		case "grant-install", "grant-remove", "grant-use", "grant-list":
			return grantCtl(args, stdin, stdout, stderr, getenv)
		case "credential-install", "credential-remove", "credential-list", "credential-available", "credential-use", "credential-expire":
			return credentialCtl(ctx, args, stdin, stdout, stderr, getenv, r)
		case "deliver", "log":
			return ctlMessageOrLog(ctx, args, stdin, stdout, stderr, getenv, r)
		case "rescues", "prune", "hold-rescue":
			return shelfCtl(args, stdin, stdout, stderr, getenv)
		}
	}
	stopAgent := false
	switch {
	case len(args) == 1:
	case len(args) == 2 && args[0] == "rescue" && args[1] == "--stop-agent":
		stopAgent = true
	default:
		_, _ = fmt.Fprintf(stderr, "%s: usage: ctl status | rescue [--stop-agent] | rescues [--session S] | prune | prepare-restart | grant-install | grant-remove | grant-list | grant-use\n", binaryName)
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
		s, err := agentd.LoadSettings(getenv)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
			return exitFailure
		}
		// The session name only labels the report; rescue works without one.
		name := ""
		if sess, err := agentd.LoadSession(getenv); err == nil {
			name = sess.Name
		}
		opt := agentd.RescueOptions{StopAgent: stopAgent, StopGrace: stopGrace}
		rep, err := agentd.Rescue(ctx, r, s, name, time.Now(), opt)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: rescue: %v\n", binaryName, err)
			return exitFailure
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return exitFailure
		}
		if !rep.OK {
			// The report says which worktree was refused or which bundle
			// failed; the operator marks the session rescueFailed (D-10).
			return exitFailure
		}
		return exitOK
	case "prepare-restart":
		s, err := agentd.LoadSettings(getenv)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
			return exitFailure
		}
		name := ""
		if sess, err := agentd.LoadSession(getenv); err == nil {
			name = sess.Name
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(agentd.PrepareRestart(s, name, stopGrace)); err != nil {
			return exitFailure
		}
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "%s: unknown ctl command %q\n", binaryName, args[0])
		return exitUsage
	}
}
