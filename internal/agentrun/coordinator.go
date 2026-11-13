package agentrun

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
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

// CoordinatorStartInput is one coordinator session as the caller names it.
// Model is required. There is no effort field: the platform uses the model's preset.
// Title is optional role text. Empty means no title, and there is no default.
type CoordinatorStartInput struct {
	Item    int64
	Repo    string
	Project string
	Model   string
	Title   string
	Run     string
	Hours   int
	Workers int
}

// CoordinatorOutcome is a coordinator session and the tracker item it belongs to.
type CoordinatorOutcome struct {
	Item    int64
	Session apiv1.Session
}

// SessionDoor is the operator session routes coordinator start, status and stop use.
type SessionDoor interface {
	ListMine(context.Context) (apiv1.SessionList, error)
	Get(context.Context, string) (apiv1.Session, error)
	Create(context.Context, apiv1.CreateSessionRequest) (apiv1.Session, error)
	Delete(context.Context, string) (apiv1.Session, error)
}

// PrepareCoordinatorStart checks a start and builds the create body.
// It does no I/O. A missing or unknown model is refused here, and the body has no effort key.
func PrepareCoordinatorStart(in CoordinatorStartInput) (apiv1.CreateSessionRequest, error) {
	key, err := coordinatorKey(in.Item, in.Run)
	if err != nil {
		return apiv1.CreateSessionRequest{}, err
	}
	if in.Model == "" {
		return apiv1.CreateSessionRequest{}, usageError("--model is required; name the full model ID Tom chose")
	}
	agent := "claude"
	switch in.Model {
	case "claude-opus-5-5", "claude-fable-5-2":
	case "gpt-6-astra":
		agent = "codex"
	default:
		return apiv1.CreateSessionRequest{}, usageError("--model must be a full model ID: claude-opus-5-5, claude-fable-5-2 or gpt-6-astra")
	}
	if in.Hours < 1 || in.Hours > 4 {
		return apiv1.CreateSessionRequest{}, usageError("--hours must be from 1 to 4 (whole hours)")
	}
	if in.Workers < 1 || in.Workers > 4 {
		return apiv1.CreateSessionRequest{}, usageError("--workers must be from 1 to 4")
	}
	if (in.Repo == "") == (in.Project == "") {
		return apiv1.CreateSessionRequest{}, usageError("give exactly one of --repo or --project")
	}
	if in.Repo != "" && (!coordinatorRepo.MatchString(in.Repo) || len(in.Repo) > 100) {
		return apiv1.CreateSessionRequest{}, usageError("--repo must be a repository name under the GitHub owner, not a path")
	}
	if in.Project != "" && !projectName.MatchString(in.Project) {
		return apiv1.CreateSessionRequest{}, usageError("--project must be a declared project name: lowercase letters, digits and hyphens, at most 63 characters")
	}
	if agent == "codex" && in.Project == "" {
		return apiv1.CreateSessionRequest{}, usageError("gpt-6-astra needs --project: managed Codex tasks run in a declared project")
	}
	title := strings.TrimSpace(in.Title)
	if reason := sessiontitle.Validate(title); reason != "" {
		return apiv1.CreateSessionRequest{}, usageError("title: %s", sessiontitle.Explain(reason))
	}
	workerCap := int32(in.Workers)
	return apiv1.CreateSessionRequest{
		Repo: in.Repo, Project: in.Project, Agent: agent, Model: in.Model, Title: title, Mode: "task", Profile: "dev", Size: "M",
		Prompt:         fmt.Sprintf(coordinatorBriefTemplate, strconv.FormatInt(in.Item, 10), strconv.Itoa(in.Hours), strconv.Itoa(in.Workers)),
		Limits:         &apiv1.Limits{Timeout: fmt.Sprintf("%dh", in.Hours), MaxWorkers: &workerCap},
		IdempotencyKey: key,
	}, nil
}

// ExecuteCoordinatorStart is the shared start: look up the caller's retained
// session for the item key, or post one create. Validation finishes before any door call.
func ExecuteCoordinatorStart(ctx context.Context, door SessionDoor, in CoordinatorStartInput) (CoordinatorOutcome, error) {
	req, err := PrepareCoordinatorStart(in)
	if err != nil {
		return CoordinatorOutcome{}, err
	}
	existing, err := findCoordinator(ctx, door, req.IdempotencyKey)
	if err != nil {
		return CoordinatorOutcome{}, err
	}
	if existing != nil {
		sess, err := door.Get(ctx, existing.Name)
		if err != nil {
			return CoordinatorOutcome{}, err
		}
		return CoordinatorOutcome{Item: in.Item, Session: sess}, nil
	}
	sess, err := door.Create(ctx, req)
	if err != nil {
		return CoordinatorOutcome{}, err
	}
	return CoordinatorOutcome{Item: in.Item, Session: sess}, nil
}

// CoordinatorStatusInput selects one session by name or by tracker item.
type CoordinatorStatusInput struct {
	Name string
	Item int64
	Run  string
}

// ExecuteCoordinatorStatus reads one session. A name and an item together are refused
// before any door call. An unknown item key is a not-found error from the lookup.
func ExecuteCoordinatorStatus(ctx context.Context, door SessionDoor, in CoordinatorStatusInput) (CoordinatorOutcome, error) {
	var name string
	item := in.Item
	switch {
	case in.Name == "" && in.Item == 0 && in.Run == "":
		return CoordinatorOutcome{}, usageError("coordinator status takes one NAME or --item ID [--run LABEL]")
	case in.Name != "" && (in.Item != 0 || in.Run != ""):
		return CoordinatorOutcome{}, usageError("coordinator status takes one NAME or --item ID [--run LABEL]")
	case in.Name != "":
		if !projectName.MatchString(in.Name) {
			return CoordinatorOutcome{}, usageError("coordinator status needs a valid session name")
		}
		name = in.Name
		item = 0
	default:
		key, err := coordinatorKey(in.Item, in.Run)
		if err != nil {
			return CoordinatorOutcome{}, err
		}
		found, err := findCoordinator(ctx, door, key)
		if err != nil {
			return CoordinatorOutcome{}, err
		}
		if found == nil {
			return CoordinatorOutcome{}, fail(ExitNotFound, "no retained coordinator session for %s under this caller", key)
		}
		name = found.Name
	}
	sess, err := door.Get(ctx, name)
	if err != nil {
		return CoordinatorOutcome{}, err
	}
	if item == 0 && strings.HasPrefix(sess.IdempotencyKey, "coordinator-") {
		id, _, _ := strings.Cut(strings.TrimPrefix(sess.IdempotencyKey, "coordinator-"), "-")
		item, _ = strconv.ParseInt(id, 10, 64)
	}
	return CoordinatorOutcome{Item: item, Session: sess}, nil
}

// ExecuteCoordinatorStop reaps one session by name. An invalid name is refused
// before any door call.
func ExecuteCoordinatorStop(ctx context.Context, door SessionDoor, name string) (apiv1.Session, error) {
	if !projectName.MatchString(name) {
		return apiv1.Session{}, usageError("coordinator stop takes one valid session name")
	}
	return door.Delete(ctx, name)
}

// OperatorRefused reports an operator answer of unauthorized, forbidden or not found.
func OperatorRefused(err error) bool {
	var ee *exitError
	if !errors.As(err, &ee) {
		return false
	}
	return ee.code == ExitAuth || ee.code == ExitNotFound
}

// The list is caller-scoped by the API. Look at retained completed sessions too:
// the POST route alone deduplicates only unfinished requests (DESIGN-060 7.2).
func findCoordinator(ctx context.Context, door SessionDoor, key string) (*apiv1.Session, error) {
	list, err := door.ListMine(ctx)
	if err != nil {
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

func (a *app) coordinatorStart(ctx context.Context, args []string) error {
	cmd := newCommand("coordinator start", outputJSON)
	var in CoordinatorStartInput
	cmd.fs.Int64Var(&in.Item, "item", 0, "")
	cmd.fs.StringVar(&in.Repo, "repo", "", "")
	cmd.fs.StringVar(&in.Project, "project", "", "")
	cmd.fs.StringVar(&in.Model, "model", "", "")
	cmd.fs.StringVar(&in.Run, "run", "", "")
	cmd.fs.IntVar(&in.Hours, "hours", 0, "")
	cmd.fs.IntVar(&in.Workers, "workers", 0, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError("coordinator start takes flags only; use --item and --repo or --project")
	}
	if _, err := PrepareCoordinatorStart(in); err != nil {
		return err
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	out, err := ExecuteCoordinatorStart(ctx, &appSessions{a: a, c: c}, in)
	if err != nil {
		return err
	}
	return a.printCoordinator(out.Session, out.Item, cmd.c.output)
}

func (a *app) coordinatorStatus(ctx context.Context, args []string) error {
	cmd := newCommand("coordinator status", outputJSON)
	var in CoordinatorStatusInput
	cmd.fs.Int64Var(&in.Item, "item", 0, "")
	cmd.fs.StringVar(&in.Run, "run", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0:
		if _, err := coordinatorKey(in.Item, in.Run); err != nil {
			return err
		}
	case len(pos) == 1 && in.Item == 0 && in.Run == "":
		in.Name = pos[0]
		if !projectName.MatchString(in.Name) {
			return usageError("coordinator status needs a valid session name")
		}
	default:
		return usageError("coordinator status takes one NAME or --item ID [--run LABEL]")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	out, err := ExecuteCoordinatorStatus(ctx, &appSessions{a: a, c: c}, in)
	if err != nil {
		return err
	}
	return a.printCoordinator(out.Session, out.Item, cmd.c.output)
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
	s, err := ExecuteCoordinatorStop(ctx, &appSessions{a: a, c: c}, pos[0])
	if err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(s)
	}
	a.outf("%s %s: %s\n", reapAction(s), s.Name, reapText(s))
	return nil
}

// appSessions is the CLI's SessionDoor. Creates are one-shot and do not follow redirects.
type appSessions struct {
	a *app
	c *conn
}

func (s *appSessions) ListMine(ctx context.Context) (apiv1.SessionList, error) {
	var list apiv1.SessionList
	_, err := s.a.call(withCoordinatorLimits(ctx), s.c, http.MethodGet, apiv1.SessionsPath, url.Values{apiv1.FilterMine: {"true"}}, nil, &list)
	return list, err
}

func (s *appSessions) Get(ctx context.Context, name string) (apiv1.Session, error) {
	var sess apiv1.Session
	_, err := s.a.call(withCoordinatorLimits(ctx), s.c, http.MethodGet, apiv1.SessionPath(url.PathEscape(name)), nil, nil, &sess)
	return sess, err
}

func (s *appSessions) Create(ctx context.Context, req apiv1.CreateSessionRequest) (apiv1.Session, error) {
	hc := *s.c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *s.c
	one.http, one.rejectOversizeResponse = &hc, true
	var sess apiv1.Session
	// POST dedupes only unfinished tasks. An automatic retry could create
	// again after the first task exits; explicit retries redo the lookup.
	_, err := s.a.callOnce(withCoordinatorLimits(ctx), &one, http.MethodPost, apiv1.SessionsPath, req, &sess)
	if err != nil {
		return apiv1.Session{}, codexProjectHint(err, req)
	}
	return sess, nil
}

func (s *appSessions) Delete(ctx context.Context, name string) (apiv1.Session, error) {
	var sess apiv1.Session
	_, err := s.a.call(withCoordinatorLimits(ctx), s.c, http.MethodDelete, apiv1.SessionPath(name), nil, nil, &sess)
	return sess, err
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
