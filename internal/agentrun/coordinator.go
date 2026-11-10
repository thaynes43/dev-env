package agentrun

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

//go:embed coordinator_brief.txt
var coordinatorBriefTemplate string

var coordinatorRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type coordinatorResult struct {
	Item    int64         `json:"item,omitempty"`
	Session apiv1.Session `json:"session"`
}

func (a *app) coordinator(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.help([]string{"coordinator"})
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return a.help(append([]string{"coordinator"}, args[1:]...))
	}
	switch args[0] {
	case "start":
		return a.coordinatorStart(ctx, args[1:])
	case "status":
		return a.coordinatorStatus(ctx, args[1:])
	case "stop":
		return a.coordinatorStop(ctx, args[1:])
	case "brief":
		return a.coordinatorBrief(args[1:])
	default:
		return usageError("coordinator takes start, status, stop or brief")
	}
}

func coordinatorKey(item int64, run string) (string, error) {
	if item <= 0 {
		return "", usageError("--item must be a positive OpenProject item ID")
	}
	key := "coordinator-" + strconv.FormatInt(item, 10)
	if run != "" {
		if !projectName.MatchString(run) {
			return "", usageError("--run must use lowercase letters, digits or hyphens, starting and ending with a letter or digit")
		}
		key += "-" + run
	}
	if len(key) > 63 {
		return "", usageError("--run is too long: the item and run key must fit in 63 characters")
	}
	return key, nil
}

func (a *app) coordinatorStart(ctx context.Context, args []string) error {
	cmd := newCommand("coordinator start", outputJSON)
	var item int64
	var repo, project, model, run string
	var hours, workers int
	cmd.fs.Int64Var(&item, "item", 0, "")
	cmd.fs.StringVar(&repo, "repo", "", "")
	cmd.fs.StringVar(&project, "project", "", "")
	cmd.fs.StringVar(&model, "model", "", "")
	cmd.fs.StringVar(&run, "run", "", "")
	cmd.fs.IntVar(&hours, "hours", 0, "")
	cmd.fs.IntVar(&workers, "workers", 0, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError("coordinator start takes flags only; use --item and --repo or --project")
	}
	key, err := coordinatorKey(item, run)
	if err != nil {
		return err
	}
	if model == "" {
		return usageError("--model is required; name the full model ID Tom chose")
	}
	agent := "claude"
	switch model {
	case "claude-opus-5-5", "claude-fable-5-2":
	case "gpt-6-astra":
		agent = "codex"
	default:
		return usageError("--model must be a full model ID: claude-opus-5-5, claude-fable-5-2 or gpt-6-astra")
	}
	if hours < 1 || hours > 4 {
		return usageError("--hours must be from 1 to 4 (whole hours)")
	}
	if workers < 1 || workers > 4 {
		return usageError("--workers must be from 1 to 4")
	}
	if (repo == "") == (project == "") {
		return usageError("give exactly one of --repo or --project")
	}
	if repo != "" && (!coordinatorRepo.MatchString(repo) || len(repo) > 100) {
		return usageError("--repo must be a repository name under the GitHub owner, not a path")
	}
	if project != "" && !projectName.MatchString(project) {
		return usageError("--project must be a declared project name: lowercase letters, digits and hyphens, at most 63 characters")
	}
	if agent == "codex" && project == "" {
		return usageError("gpt-6-astra needs --project: managed Codex tasks run in a declared project")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	ctx = withCoordinatorLimits(ctx)
	existing, err := a.coordinatorByKey(ctx, c, key)
	if err != nil {
		return err
	}
	if existing != nil {
		return a.coordinatorRead(ctx, c, existing.Name, item, cmd.c.output)
	}
	cap := int32(workers)
	req := apiv1.CreateSessionRequest{
		Repo: repo, Project: project, Agent: agent, Model: model, Mode: "task", Profile: "dev", Size: "M",
		Prompt: fmt.Sprintf(coordinatorBriefTemplate, strconv.FormatInt(item, 10), strconv.Itoa(hours), strconv.Itoa(workers)),
		Limits: &apiv1.Limits{Timeout: fmt.Sprintf("%dh", hours), MaxWorkers: &cap}, IdempotencyKey: key,
	}
	var s apiv1.Session
	if _, err := a.call(ctx, c, http.MethodPost, apiv1.SessionsPath, nil, req, &s); err != nil {
		return codexProjectHint(err, req)
	}
	return a.printCoordinator(s, item, cmd.c.output)
}

// The list is caller-scoped by the API. Look at retained completed sessions too:
// the POST route alone deduplicates only unfinished requests (DESIGN-060 7.2).
func (a *app) coordinatorByKey(ctx context.Context, c *conn, key string) (*apiv1.Session, error) {
	var list apiv1.SessionList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionsPath, url.Values{apiv1.FilterMine: {"true"}}, nil, &list); err != nil {
		return nil, err
	}
	var found *apiv1.Session
	for i := range list.Sessions {
		s := &list.Sessions[i]
		if s.IdempotencyKey != key {
			continue
		}
		if found != nil {
			return nil, fail(ExitFailed, "more than one retained session for %s: %s, %s; use coordinator status with a session name", key, found.Name, s.Name)
		}
		found = s
	}
	return found, nil
}

func (a *app) coordinatorStatus(ctx context.Context, args []string) error {
	cmd := newCommand("coordinator status", outputJSON)
	var item int64
	var run string
	cmd.fs.Int64Var(&item, "item", 0, "")
	cmd.fs.StringVar(&run, "run", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	var name, key string
	switch {
	case len(pos) == 0:
		key, err = coordinatorKey(item, run)
		if err != nil {
			return err
		}
	case len(pos) == 1 && item == 0 && run == "":
		name = pos[0]
		if !projectName.MatchString(name) {
			return usageError("coordinator status needs a valid session name")
		}
	default:
		return usageError("coordinator status takes one NAME or --item ID [--run LABEL]")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	ctx = withCoordinatorLimits(ctx)
	if key != "" {
		s, err := a.coordinatorByKey(ctx, c, key)
		if err != nil {
			return err
		}
		if s == nil {
			return fail(ExitNotFound, "no retained coordinator session for %s under this caller", key)
		}
		name = s.Name
	}
	return a.coordinatorRead(ctx, c, name, item, cmd.c.output)
}

func (a *app) coordinatorRead(ctx context.Context, c *conn, name string, item int64, output string) error {
	var s apiv1.Session
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionPath(url.PathEscape(name)), nil, nil, &s); err != nil {
		return err
	}
	if item == 0 && strings.HasPrefix(s.IdempotencyKey, "coordinator-") {
		id, _, _ := strings.Cut(strings.TrimPrefix(s.IdempotencyKey, "coordinator-"), "-")
		item, _ = strconv.ParseInt(id, 10, 64)
	}
	return a.printCoordinator(s, item, output)
}

func (a *app) printCoordinator(s apiv1.Session, item int64, output string) error {
	if output == outputJSON {
		return a.printJSON(coordinatorResult{Item: item, Session: s})
	}
	a.outf("name: %s\nstate: %s\n", s.Name, s.Phase)
	if item > 0 {
		a.outf("item: %d\n", item)
	}
	if s.Reaping {
		a.outf("reaping: true\n")
	}
	if v := s.TaskVerdict(); v != nil {
		a.outf("task: %s (%s): %s\n", v.State, v.Reason, v.Message)
	}
	if o := s.TaskOutcome; o != nil && o.Validate() == nil {
		a.outf("summary: %s\n", o.Record.Summary)
		for _, p := range o.Record.PRURLs {
			a.outf("PR: %s\n", p)
		}
		for _, h := range o.Record.HeadSHAs {
			a.outf("head: %s\n", h)
		}
	}
	return nil
}

func (a *app) coordinatorStop(ctx context.Context, args []string) error {
	cmd := newCommand("coordinator stop", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !projectName.MatchString(pos[0]) {
		return usageError("coordinator stop takes one valid session name")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var s apiv1.Session
	if _, err := a.call(withCoordinatorLimits(ctx), c, http.MethodDelete, apiv1.SessionPath(pos[0]), nil, nil, &s); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(s)
	}
	a.outf("reaping %s: %s\n", s.Name, reapText(s))
	return nil
}

func (a *app) coordinatorBrief(args []string) error {
	cmd := newCommand("coordinator brief")
	var item int64
	var hours, workers int
	cmd.fs.Int64Var(&item, "item", 0, "")
	cmd.fs.IntVar(&hours, "hours", 0, "")
	cmd.fs.IntVar(&workers, "workers", 0, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError("coordinator brief takes optional --item, --hours and --workers")
	}
	id, h, w := "<item>", "<hours>", "<workers>"
	if item != 0 || hours != 0 || workers != 0 {
		if item <= 0 || hours < 1 || hours > 4 || workers < 1 || workers > 4 {
			return usageError("give --item ID, --hours 1 to 4 and --workers 1 to 4 together")
		}
		id, h, w = strconv.FormatInt(item, 10), strconv.Itoa(hours), strconv.Itoa(workers)
	}
	a.outf(coordinatorBriefTemplate, id, h, w)
	return nil
}
