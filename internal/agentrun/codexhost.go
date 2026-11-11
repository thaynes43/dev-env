package agentrun

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	hostSelectionOff     = "Host selection is off."
	resumeNotBuilt       = "Resume is not built yet."
	commandCreatesNoHost = "This command does not create a host."
)

func (a *app) codexHost(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		return a.help([]string{"codex-host"})
	}
	if args[0] != "select" {
		return usageError("codex-host select --repo <name> [--profile <name>] [--check]")
	}
	return a.codexHostSelect(ctx, args[1:])
}

func (a *app) codexHostSelect(ctx context.Context, args []string) error {
	cmd := newCommand("codex-host")
	var repo, profile string
	var check bool
	cmd.fs.StringVar(&repo, "repo", "", "")
	cmd.fs.StringVar(&profile, "profile", "", "")
	cmd.fs.BoolVar(&check, "check", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 || !apiv1.ValidRepoName(repo) {
		return usageError("codex-host select needs --repo <name>")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	choice, off, err := a.chooseCodexHost(ctx, c, repo, profile, check)
	if err != nil {
		return err
	}
	if off {
		return fail(ExitFailed, "route_unsupported: %s", apiv1.CodexHostRouteUnsupported)
	}
	if choice.Action == "" {
		return codexHostRefused(choice)
	}
	return a.printJSON(choice)
}

// chooseCodexHost posts one select. The bool is true when the route is absent
// (any 404): the flag is off, or the operator is older. A refusal is a choice
// with an empty action, not an error.
func (a *app) chooseCodexHost(ctx context.Context, c *conn, repo, profile string, check bool) (apiv1.CodexHostChoice, bool, error) {
	payload, err := json.Marshal(apiv1.CodexHostSelectRequest{Repo: repo, Profile: profile})
	if err != nil {
		return apiv1.CodexHostChoice{}, false, fail(ExitFailed, "could not build the selection request")
	}
	target := c.url + apiv1.CodexHostsSelectPath
	if check {
		target += "?check=true"
	}
	status, raw, err := a.send(ctx, c, http.MethodPost, target, payload)
	if err != nil {
		return apiv1.CodexHostChoice{}, false, err
	}
	if status == http.StatusNotFound {
		return apiv1.CodexHostChoice{}, true, nil
	}
	if status != http.StatusOK {
		return apiv1.CodexHostChoice{}, false, refusal(c, status, raw)
	}
	var choice apiv1.CodexHostChoice
	if json.Unmarshal(raw, &choice) != nil {
		return apiv1.CodexHostChoice{}, false, fail(ExitFailed, "the selection answer is not the host choice")
	}
	return choice, false, nil
}

func (a *app) releaseCodexHost(ctx context.Context, c *conn, host string) error {
	payload, err := json.Marshal(apiv1.CodexHostReleaseRequest{Host: host})
	if err != nil {
		return fail(ExitFailed, "could not build the release request")
	}
	status, raw, err := a.send(ctx, c, http.MethodPost, c.url+apiv1.CodexHostsReleasePath, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return refusal(c, status, raw)
	}
	return nil
}

func codexHostRefused(choice apiv1.CodexHostChoice) error {
	sentence, ok := apiv1.CodexHostSentence(choice.Reason)
	if !ok {
		sentence = "The selection was refused."
	}
	code := choice.Reason
	if code == "" {
		code = "refused"
	}
	return fail(ExitFailed, "%s: %s", code, sentence)
}

// codexBeforeCreate asks select before an interactive Codex create. False
// means the create does not continue: the choice was printed, or it was refused.
func (a *app) codexBeforeCreate(ctx context.Context, c *conn, repo, profile string) (bool, error) {
	choice, off, err := a.chooseCodexHost(ctx, c, repo, profile, false)
	if err != nil {
		return false, err
	}
	if off {
		a.errf("%s", hostSelectionOff)
		return true, nil
	}
	if choice.Action == "create" {
		return true, nil
	}
	if choice.Action == "" {
		return false, codexHostRefused(choice)
	}
	return false, a.printJSON(choice)
}

// threadChooseHost is thread start --agent codex with no --host.
func (a *app) threadChooseHost(ctx context.Context, cmd *command, t threadArgs) error {
	invalid := func() error { return a.threadRefuse("codex", apiv1.ThreadFailed, "invalid_input", ExitUsage) }
	if t.name != "" && !apiv1.ValidThreadName(t.name) {
		return invalid()
	}
	if _, ok := threadReadHandoff(t.handoff); !ok {
		return invalid()
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	choice, off, err := a.chooseCodexHost(ctx, c, t.repo, "", false)
	if err != nil {
		return err
	}
	if off {
		a.errf("%s", hostSelectionOff)
		return invalid()
	}
	switch choice.Action {
	case "use":
		t.host = choice.Host
		if !projectName.MatchString(t.host) {
			if rel := a.releaseCodexHost(ctx, c, choice.Host); rel != nil {
				a.errf("%s", rel.Error())
			}
			return invalid()
		}
		err = a.threadOnHost(ctx, cmd, t)
		if rel := a.releaseCodexHost(ctx, c, t.host); rel != nil {
			a.errf("%s", rel.Error())
		}
		return err
	case "report":
		return a.printJSON(choice)
	case "resume":
		if err := a.printJSON(choice); err != nil {
			return err
		}
		return fail(ExitFailed, "%s", resumeNotBuilt)
	case "create":
		if err := a.printJSON(choice); err != nil {
			return err
		}
		return fail(ExitFailed, "%s", commandCreatesNoHost)
	default:
		return codexHostRefused(choice)
	}
}
