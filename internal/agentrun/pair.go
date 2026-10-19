package agentrun

import (
	"context"
)

// pair prints a phone pairing code for a Codex --interactive session
// (Story110 T17). It is login's twin: a thin kubectl exec of `agentd ctl
// codex-pair` in the session's pod, over the POST (SPDY) transport sessionExecBase
// forces. The code is a short-lived secret: it comes back on stdout only, and
// this side puts nothing about it in an argument, a file or a log line.
func (a *app) pair(ctx context.Context, args []string) error {
	cmd := newCommand("pair")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("pair takes one session name; agent-run help pair says how")
	}
	argv, err := a.sessionExecBase(ctx, cmd, "pair", pos[0])
	if err != nil {
		return err
	}
	argv = append(argv, "--", "agentd", "ctl", "codex-pair")
	code, err := a.env.Run(ctx, argv, a.env.Stdin)
	switch {
	case err != nil:
		return fail(ExitFailed, "pair: %v", err)
	case code != 0:
		return fail(ExitFailed, "pair: kubectl exec exited %d", code)
	}
	return nil
}
