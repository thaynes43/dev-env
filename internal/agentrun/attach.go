package agentrun

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// attach and detach (DESIGN-001 3.5, D-58) are for Tom, from the workbench, a
// laptop or the v1 pod: `kubectl exec` into the session's pod and tmux. The API
// says which pod and whether it runs; kubectl does the exec with the caller's
// own Kubernetes rights. Agents have no exec in dev-agents (D-19), so agent-run
// refuses both inside a session pod and points at msg.

// SessionNamespace is where session pods run (D-02).
const SessionNamespace = "dev-agents"

// tmuxSession is the tmux session agentd starts the agent in (D-42).
const tmuxSession = "agent"

// agentContainer is the session pod's one container (D-44).
const agentContainer = "agent"

// runProcess runs argv with the process's own terminal, and returns its exit
// code. An error means it could not start.
func runProcess(ctx context.Context, argv []string) (int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

func (a *app) attach(ctx context.Context, args []string) error {
	return a.tmuxExec(ctx, "attach", args, true)
}

func (a *app) detach(ctx context.Context, args []string) error {
	return a.tmuxExec(ctx, "detach", args, false)
}

// tmuxExec runs tmux in the session's pod through kubectl exec: attach with a
// terminal, or detach every client of the agent's tmux session.
func (a *app) tmuxExec(ctx context.Context, verb string, args []string, interactive bool) error {
	cmd := newCommand(verb)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("%s takes one session name; agent-run list shows them", verb)
	}
	if a.inSessionPod() {
		return fail(ExitAuth, "%s is for Tom, from the workbench, a laptop or the v1 pod: an agent has no exec into another session's pod (D-19). Send it a message instead: agent-run msg %s \"<text>\"", verb, pos[0])
	}
	kubectl, err := a.env.LookPath("kubectl")
	if err != nil {
		return fail(ExitFailed, "%s runs kubectl exec, and kubectl is not on PATH", verb)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	// Outside a pod, exec must use the context captured for CA/token/forward.
	// connect rejects selectors with automatic in-pod API/identity discovery.
	if a.env.Getenv(envKubeHost) == "" || cmd.c.conn.kubeconfig != "" || cmd.c.conn.context != "" {
		if _, err := a.kubectl(ctx, cmd.c.conn); err != nil {
			return err
		}
	}
	var s apiv1.Session
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionPath(url.PathEscape(pos[0])), nil, nil, &s); err != nil {
		return err
	}
	if s.Phase != "Running" {
		return fail(ExitFailed, "%s is %s, so it has no running agent to %s; agent-run show %s says why", s.Name, firstOf(s.Phase, "not observed yet"), verb, s.Name)
	}
	argv := []string{kubectl, "exec", "-n", SessionNamespace, s.Name, "-c", agentContainer}
	if a.kube != nil {
		argv = a.kube.argv("exec", "-n", SessionNamespace, s.Name, "-c", agentContainer)
	}
	if interactive {
		// tmux needs a terminal type, and kubectl exec does not pass TERM.
		term := firstOf(a.env.Getenv("TERM"), "xterm-256color")
		argv = append(argv, "-it", "--", "env", "TERM="+term, "tmux", "attach-session", "-t", tmuxSession)
	} else {
		argv = append(argv, "--", "tmux", "detach-client", "-s", tmuxSession)
	}
	code, err := a.env.Run(ctx, argv)
	switch {
	case err != nil:
		return fail(ExitFailed, "%s: %v", verb, err)
	case code != 0:
		return fail(ExitFailed, "%s: kubectl exec exited %d", verb, code)
	}
	return nil
}

// inSessionPod reports whether agent-run runs in a session pod, by the same
// sign connect uses: the pod's projected API token. agentd removes
// AGENTD_SESSION from the agent's environment (D-42), but the token's
// variable and file reach every process in the pod.
func (a *app) inSessionPod() bool {
	return a.env.Getenv(envAgentdTokenFile) != "" || exists(a.env.SessionTokenFile)
}
