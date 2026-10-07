package agentrun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Waiting for a new session's pod (D-50): agent-run polls the session until its
// pod is Ready, the session fails, or the scheduler has said twice in a row that
// it cannot place the pod, and then prints that reason at once (plan 01's
// acceptance). A brief Unschedulable, such as a volume still binding, does not
// last two polls.
const (
	defaultWait  = 30 * time.Second
	pollInterval = 2 * time.Second
)

// conditionPodReady is the reconciler's condition for the session's pod, and
// unschedulable the scheduler's reason when it cannot place one.
const (
	conditionPodReady = "PodReady"
	unschedulable     = "Unschedulable"
)

func (a *app) create(ctx context.Context, args []string) error {
	cmd := newCommand("run", outputName, outputJSON)
	fs := cmd.fs
	var (
		prompt, promptFile, repo, agent, model, effort, base, size, profile, timeout, key string
		maxTurns                                                                          int
		wait                                                                              time.Duration
		interactive, local, safe                                                          bool
	)
	fs.StringVar(&prompt, "p", "", "")
	fs.StringVar(&prompt, "prompt", "", "")
	fs.StringVar(&promptFile, "prompt-file", "", "")
	fs.StringVar(&repo, "repo", "", "")
	fs.StringVar(&agent, "agent", protocol.AgentClaude, "")
	fs.StringVar(&model, "model", "", "")
	fs.StringVar(&effort, "effort", "", "")
	fs.StringVar(&base, "base", "", "")
	fs.StringVar(&size, "size", "", "")
	fs.StringVar(&profile, "profile", "", "")
	fs.StringVar(&timeout, "timeout", "", "")
	fs.IntVar(&maxTurns, "max-turns", 0, "")
	fs.StringVar(&key, "idempotency-key", "", "")
	fs.DurationVar(&wait, "wait", defaultWait, "")
	fs.BoolVar(&interactive, "interactive", false, "")
	fs.BoolVar(&local, "local", false, "")
	fs.BoolVar(&safe, "safe", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}

	switch {
	case len(pos) > 1:
		return usageError("one repository, got %q; quote the task: -p \"<task>\"", pos)
	case len(pos) == 1 && isCommand(pos[0]):
		return usageError("%q is a command: put it first, as in agent-run %s", pos[0], pos[0])
	case len(pos) == 1 && repo != "" && repo != pos[0]:
		return usageError("two repositories: --repo %s and %s", repo, pos[0])
	case len(pos) == 1:
		repo = pos[0]
	}
	if safe {
		return usageError("--safe is gone in v2: a session pod runs its agent without approval prompts, and the platform is the boundary (D-23)")
	}
	if (interactive || local) && (prompt != "" || promptFile != "") {
		return usageError("-p runs a task headless, so it cannot combine with --interactive or --local; pick one")
	}
	if interactive || local {
		return usageError("--interactive and --local sessions arrive in plans 02 and 03; until then v1's agent-run in the dev-env pod starts them")
	}
	if promptFile != "" {
		if prompt != "" {
			return usageError("give the task with -p or --prompt-file, not both")
		}
		if prompt, err = a.readPrompt(promptFile); err != nil {
			return err
		}
	}
	if strings.TrimSpace(prompt) == "" {
		return usageError("say what to run: -p \"<task>\" or --prompt-file <path>; agent-run help run lists the flags")
	}
	if len(prompt) > protocol.MaxPromptBytes {
		return usageError("the task is %d bytes, more than %d: a session's environment carries it (D-40)", len(prompt), protocol.MaxPromptBytes)
	}
	if repo == "" {
		return usageError("say which repository: --repo <name>, for example --repo haynes-ops")
	}
	if strings.Contains(repo, "/") {
		return usageError("--repo is a repository name under the GitHub owner, such as haynes-ops, not %q", repo)
	}

	switch agent {
	case protocol.AgentClaude:
		from := "--model"
		if model == "" {
			model, from = a.env.Getenv(envDefaultModel), envDefaultModel
			if model == "" {
				model, from = DefaultClaudeModel, ""
			}
		}
		if from != "" {
			if err := checkClaudeModel(model, from); err != nil {
				return err
			}
		}
		if effort, err = claudeEffort(model, effort); err != nil {
			return err
		}
	case protocol.AgentCodex, protocol.AgentOpencode:
		// The API says which plan serves these; agent-run does not restate it.
		if model == "" {
			return usageError("--agent %s needs --model: its model id or LLM pool model", agent)
		}
	default:
		return usageError("--agent is claude, codex or opencode, not %q", agent)
	}
	if size != "" {
		size = strings.ToUpper(size)
		if !slices.Contains([]string{"S", "M", "L"}, size) {
			return usageError("--size is S, M or L, not %q", size)
		}
	}
	var limits *apiv1.Limits
	if timeout != "" || maxTurns != 0 {
		limits = &apiv1.Limits{Timeout: timeout, MaxTurns: int32(maxTurns)}
		if timeout != "" {
			if d, err := time.ParseDuration(timeout); err != nil || d <= 0 {
				return usageError("--timeout %q is not a positive duration such as 40m or 2h", timeout)
			}
		}
		if maxTurns < 0 || maxTurns > 1<<30 {
			return usageError("--max-turns is a positive number of turns, not %d", maxTurns)
		}
	}
	if wait < 0 {
		return usageError("--wait is a duration of 0 or more, not %s", wait)
	}
	// givenKey is a key the caller chose. A 200 for agent-run's own key is
	// its own retry finding the session its first attempt created: that is
	// still a create, and says so.
	givenKey := key != ""
	if !givenKey {
		key = a.env.NewKey()
	}

	c, err := a.connect(cmd.c.conn)
	if err != nil {
		return err
	}
	req := apiv1.CreateSessionRequest{
		Repo:           repo,
		Base:           base,
		Agent:          agent,
		Mode:           protocol.ModeTask,
		Model:          model,
		Effort:         effort,
		Prompt:         prompt,
		Size:           size,
		Profile:        profile,
		Limits:         limits,
		IdempotencyKey: key,
	}
	var sess apiv1.Session
	status, err := a.call(ctx, c, http.MethodPost, apiv1.SessionsPath, nil, req, &sess)
	if err != nil {
		return err
	}
	if cmd.c.output == outputText {
		if status == http.StatusOK && givenKey {
			a.outf("%s already exists: idempotency key %s created it, so agent-run did not start another\n", sess.Name, key)
		} else {
			a.outf("created %s\n", sess.Name)
		}
		a.outf("  %s %s, %s, size %s, repo %s\n", sess.Agent, sess.Model, effortText(sess.Effort), firstOf(sess.Size, "M"), sess.Repo)
	}

	final, waitErr := a.waitForPod(ctx, c, sess, wait)
	switch cmd.c.output {
	case outputName:
		a.outf("%s\n", sess.Name)
	case outputJSON:
		if err := a.printJSON(final); err != nil {
			return err
		}
	default:
		if waitErr == nil {
			a.outf("  %s\n", startLine(final, wait))
		}
		a.outf("Follow it with: agent-run show %s\n", sess.Name)
	}
	if waitErr != nil {
		a.errf("could not follow %s after it was created: %v", sess.Name, waitErr)
		return nil
	}
	if final.Phase == "Failed" {
		return fail(ExitFailed, "%s failed: %s", final.Name, firstOf(podReadyMessage(final), "see agent-run show "+final.Name))
	}
	return nil
}

// isCommand reports whether word is one of agent-run's commands.
func isCommand(word string) bool {
	return slices.Contains(commands, word)
}

func (a *app) readPrompt(path string) (string, error) {
	var (
		b   []byte
		err error
	)
	if path == "-" {
		b, err = io.ReadAll(io.LimitReader(a.env.Stdin, protocol.MaxPromptBytes+1))
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return "", usageError("--prompt-file: %v", err)
	}
	return string(b), nil
}

// waitForPod polls the session until its pod is Ready, it fails, the scheduler
// cannot place it, or wait is over. It returns the last view it read; an error
// means it could not read one after the create.
func (a *app) waitForPod(ctx context.Context, c *conn, sess apiv1.Session, wait time.Duration) (apiv1.Session, error) {
	if wait <= 0 || settled(sess) {
		return sess, nil
	}
	// The deadline is on env's clock, so tests can run it on a fake one.
	deadline := a.env.Now().Add(wait)
	seenUnschedulable := false
	for {
		if err := a.env.Sleep(ctx, pollInterval); err != nil {
			return sess, nil
		}
		var cur apiv1.Session
		if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionPath(sess.Name), nil, nil, &cur); err != nil {
			if ctx.Err() != nil {
				return sess, nil
			}
			return sess, err
		}
		sess = cur
		if settled(sess) {
			return sess, nil
		}
		if podReadyReason(sess) == unschedulable {
			if seenUnschedulable {
				return sess, nil
			}
			seenUnschedulable = true
		} else {
			seenUnschedulable = false
		}
		if !a.env.Now().Before(deadline) {
			return sess, nil
		}
	}
}

// settled is a session whose start is over, one way or the other.
func settled(s apiv1.Session) bool {
	switch s.Phase {
	case "Running", "Idle", "Failed", "Suspended", "Archived":
		return true
	}
	return s.Reaping
}

func podReady(s apiv1.Session) *apiv1.Condition {
	for i := range s.Conditions {
		if s.Conditions[i].Type == conditionPodReady {
			return &s.Conditions[i]
		}
	}
	return nil
}

func podReadyReason(s apiv1.Session) string {
	if c := podReady(s); c != nil {
		return c.Reason
	}
	return ""
}

func podReadyMessage(s apiv1.Session) string {
	if c := podReady(s); c != nil {
		return c.Message
	}
	return ""
}

// startLine says how the start went, in one line.
func startLine(s apiv1.Session, wait time.Duration) string {
	switch {
	case s.Reaping:
		return "It is being reaped."
	case s.Phase == "Running" || s.Phase == "Idle":
		return fmt.Sprintf("%s on %s.", s.Phase, firstOf(s.Node, "a node"))
	case s.Phase == "Failed":
		return "Failed: " + firstOf(podReadyMessage(s), "see agent-run show "+s.Name) + "."
	case s.Phase == "Suspended" || s.Phase == "Archived":
		return s.Phase + "."
	case podReadyReason(s) == unschedulable:
		return "Pending: the scheduler cannot place it yet: " + firstOf(s.Pending, "no reason given") + ". It starts when room frees up."
	case wait <= 0:
		return "Pending: not waiting for it to start (--wait 0)."
	}
	return "Still starting: " + firstOf(s.Pending, "no pod yet") + "."
}

func (a *app) outf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.env.Stdout, format, args...)
}
