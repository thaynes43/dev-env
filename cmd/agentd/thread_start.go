package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

// threadStartCtl is `ctl thread-start` (DESIGN-057): one fresh, phone-visible
// Codex thread on this host. agent-run thread start reaches it over the exec
// route agent-run pair uses. The handoff is read from stdin only. It prints
// exactly one thread-launch.v1 line and exits with that outcome's code, which
// kubectl exec relays to the caller.
func threadStartCtl(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet(binaryName+" ctl thread-start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var req agentd.CodexThreadRequest
	fs.StringVar(&req.Model, "model", "", "")
	fs.StringVar(&req.Effort, "effort", "", "")
	fs.StringVar(&req.Repo, "repo", "", "")
	fs.StringVar(&req.Name, "name", "", "")
	fs.DurationVar(&req.Wait, "wait", 5*time.Minute, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "%s: usage: ctl thread-start --model M --effort E --name N [--repo R] [--wait D] < handoff\n", binaryName)
		return exitUsage
	}
	emit := func(r apiv1.ThreadLaunchResult) int {
		b, err := threadlaunch.Encode(r)
		if err != nil {
			return exitFailure
		}
		if _, err := stdout.Write(b); err != nil {
			return exitFailure
		}
		return apiv1.ThreadLaunchExit(r.Outcome)
	}
	refuse := func(code string) int {
		return emit(threadlaunch.NewRecorder("codex", time.Now).Finish(apiv1.ThreadFailed, code, apiv1.ThreadPhaseHostReady, req.Wait))
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 4097))
	if err != nil || len(raw) > 4096 {
		return refuse("invalid_input")
	}
	req.Handoff = string(raw)
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return refuse("observation_unavailable")
	}
	sess, err := agentd.LoadSession(getenv)
	if err != nil {
		return refuse("not_codex_host")
	}
	return emit(agentd.StartCodexThread(ctx, s, sess, req))
}

// threadStatusCtl is `ctl thread-status` (DESIGN-057 section 11): one
// read-only pass over an existing Codex thread on this host, for agent-run
// thread status. It reads no stdin, runs no wrapper and sends the thread
// nothing. It prints exactly one thread-launch.v1 line and exits with that
// outcome's code.
func threadStatusCtl(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet(binaryName+" ctl thread-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var req agentd.CodexThreadStatusRequest
	fs.StringVar(&req.ThreadID, "thread", "", "")
	fs.StringVar(&req.Repo, "repo", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "%s: usage: ctl thread-status --thread ID [--repo R]\n", binaryName)
		return exitUsage
	}
	emit := func(r apiv1.ThreadLaunchResult) int {
		b, err := threadlaunch.Encode(r)
		if err != nil {
			return exitFailure
		}
		if _, err := stdout.Write(b); err != nil {
			return exitFailure
		}
		return apiv1.ThreadLaunchExit(r.Outcome)
	}
	refuse := func(code string) int {
		return emit(threadlaunch.NewStatusRecorder("codex", time.Now).Finish(apiv1.ThreadFailed, code, apiv1.ThreadPhaseHostReady, 0))
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return refuse("observation_unavailable")
	}
	sess, err := agentd.LoadSession(getenv)
	if err != nil {
		return refuse("not_codex_host")
	}
	return emit(agentd.ObserveCodexThread(ctx, s, sess, req))
}
