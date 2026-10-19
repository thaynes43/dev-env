package agentrun

import (
	"context"
)

// login drives a remote session's pod-owned Claude login (Story110 T03,
// backlog 03 "interim slice"). It is a thin kubectl exec of `agentd ctl login`
// in the session's pod, like attach: for Tom's workbench, a laptop or the v1
// pod, never an agent inside a session pod. The login URL and the code that
// comes back are secrets: url prints the URL to stdout only, and code reads
// the code from stdin, so it is never an argument, a file or a log line.
func (a *app) login(ctx context.Context, args []string) error {
	cmd := newCommand("login")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return usageError("login takes a session name and url, code or status; agent-run help login says how")
	}
	verb := "status"
	if len(pos) == 2 {
		verb = pos[1]
	}
	switch verb {
	case "url", "code", "status":
	default:
		return usageError("login takes url, code or status, not %q", verb)
	}
	argv, err := a.sessionExecBase(ctx, cmd, "login", pos[0])
	if err != nil {
		return err
	}
	if verb == "code" {
		// -i forwards stdin to agentd, which sends it to the login prompt.
		argv = append(argv, "-i")
	}
	argv = append(argv, "--", "agentd", "ctl", "login", verb)
	code, err := a.env.Run(ctx, argv, a.env.Stdin)
	switch {
	case err != nil:
		return fail(ExitFailed, "login %s: %v", verb, err)
	case code != 0:
		return fail(ExitFailed, "login %s: kubectl exec exited %d", verb, code)
	}
	return nil
}
