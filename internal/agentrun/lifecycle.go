package agentrun

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// suspend and resume (DESIGN-001 3.5, D-60) map one to one onto the API's
// routes. A suspend rescues the session's work and stops its pod, keeping its
// volume; a resume starts a new pod on the volume, and the agent resumes its
// conversation (D-58).

func (a *app) suspend(ctx context.Context, args []string) error {
	cmd := newCommand("suspend", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError("suspend takes the names of the sessions to suspend; agent-run list shows them")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var (
		done  []apiv1.Session
		first error
	)
	for _, name := range pos {
		var s apiv1.Session
		status, err := a.call(ctx, c, http.MethodPost, apiv1.SessionSuspendPath(url.PathEscape(name)), nil, nil, &s)
		if err != nil {
			if first == nil {
				first = err
			}
			if len(pos) > 1 {
				a.errf("%s: %v", name, err)
			}
			continue
		}
		done = append(done, s)
		if cmd.c.output == outputText {
			if status == http.StatusOK {
				a.outf("%s is already suspended\n", s.Name)
			} else {
				a.outf("suspending %s: the operator rescues its work, then stops its pod; its volume and conversation stay for agent-run resume %s\n", s.Name, s.Name)
			}
		}
	}
	if cmd.c.output == outputJSON {
		if err := a.printJSON(apiv1.SessionList{Sessions: append([]apiv1.Session{}, done...)}); err != nil {
			return err
		}
	}
	if first != nil && len(pos) > 1 {
		var ee *exitError
		if errors.As(first, &ee) {
			return &exitError{code: ee.code}
		}
		return &exitError{code: ExitFailed}
	}
	return first
}

func (a *app) resume(ctx context.Context, args []string) error {
	cmd := newCommand("resume", outputJSON)
	var wait time.Duration
	cmd.fs.DurationVar(&wait, "wait", defaultWait, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("resume takes one session name; agent-run list shows them")
	}
	if wait < 0 {
		return usageError("--wait is a duration of 0 or more, not %s", wait)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var s apiv1.Session
	status, err := a.call(ctx, c, http.MethodPost, apiv1.SessionResumePath(url.PathEscape(pos[0])), nil, nil, &s)
	if err != nil {
		return err
	}
	if cmd.c.output == outputText {
		if status == http.StatusOK {
			a.outf("%s is not suspended\n", s.Name)
		} else {
			a.outf("resuming %s: a new pod starts on its volume, and the agent resumes its conversation\n", s.Name)
		}
	}
	final, waitErr := a.waitForPod(ctx, c, s, wait, status != http.StatusOK)
	switch cmd.c.output {
	case outputJSON:
		return a.printJSON(final)
	default:
		if waitErr != nil {
			a.errf("could not follow %s after the resume: %v", s.Name, waitErr)
			return nil
		}
		a.outf("  %s\n", startLine(final, wait))
		if final.Phase == "Running" || final.Phase == "Idle" {
			a.outf("Attach to it with: agent-run attach %s\n", s.Name)
		}
	}
	if final.Phase == "Failed" {
		return fail(ExitFailed, "%s failed: %s", final.Name, firstOf(podReadyMessage(final), "see agent-run show "+final.Name))
	}
	return nil
}
