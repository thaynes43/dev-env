package agentrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	hostSelectionOff          = "Host selection is off."
	resumeNotBuilt            = "Resume is not built yet."
	commandCreatesNoHost      = "This command does not create a host."
	selectionNotUsedUnreached = "Host selection was not used: the operator could not be reached."
	selectionNotUsedNoCreds   = "Host selection was not used: no credentials."
	selectionNotUsedNoAnswer  = "Host selection was not used: the operator did not answer."
	selectionNotUsedSettings  = "Host selection was not used: the connection settings were refused."
)

// codexHostReleaseTimeout bounds the release call itself. It starts when the
// release runs, not when the lease is acquired, so a long thread start does
// not expire it.
var codexHostReleaseTimeout = 10 * time.Second

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
	choice, off, _, err := a.chooseCodexHost(ctx, c, repo, profile, check, false)
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

// chooseCodexHost posts one select. retry uses the client's standard call, the
// same retries create uses. The bool is true when the route is absent (any
// 404): the flag is off, or the operator is older. A refusal is a choice with
// an empty action, not an error. The launch id is not a secret and is not logged.
func (a *app) chooseCodexHost(ctx context.Context, c *conn, repo, profile string, check, retry bool) (apiv1.CodexHostChoice, bool, string, error) {
	id, err := newCodexLaunchID()
	if err != nil {
		return apiv1.CodexHostChoice{}, false, "", err
	}
	req := apiv1.CodexHostSelectRequest{Repo: repo, Profile: profile, LaunchID: id}
	if retry {
		return a.chooseCodexHostRetry(ctx, c, req, id, check)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return apiv1.CodexHostChoice{}, false, "", fail(ExitFailed, "could not build the selection request")
	}
	target := c.url + apiv1.CodexHostsSelectPath
	if check {
		target += "?check=true"
	}
	status, raw, err := a.send(ctx, c, http.MethodPost, target, payload)
	if err != nil {
		return apiv1.CodexHostChoice{}, false, "", selectTransport(err)
	}
	if status == http.StatusNotFound {
		return apiv1.CodexHostChoice{}, true, "", nil
	}
	if status != http.StatusOK {
		return apiv1.CodexHostChoice{}, false, "", &selectResponse{status: status, err: refusal(c, status, raw)}
	}
	var choice apiv1.CodexHostChoice
	if json.Unmarshal(raw, &choice) != nil {
		return apiv1.CodexHostChoice{}, false, "", fail(ExitFailed, "the selection answer is not the host choice")
	}
	return choice, false, id, nil
}

func (a *app) chooseCodexHostRetry(ctx context.Context, c *conn, req apiv1.CodexHostSelectRequest, id string, check bool) (apiv1.CodexHostChoice, bool, string, error) {
	q := url.Values{}
	if check {
		q.Set("check", "true")
	}
	var choice apiv1.CodexHostChoice
	status, err := a.callWith(ctx, c, http.MethodPost, apiv1.CodexHostsSelectPath, q, req, &choice, true)
	if status == http.StatusNotFound {
		return apiv1.CodexHostChoice{}, true, "", nil
	}
	if err != nil {
		return apiv1.CodexHostChoice{}, false, "", err
	}
	return choice, false, id, nil
}

func newCodexLaunchID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fail(ExitFailed, "could not make a launch id")
	}
	return hex.EncodeToString(b[:]), nil
}

func (a *app) releaseCodexHost(ctx context.Context, c *conn, host, launchID string) error {
	payload, err := json.Marshal(apiv1.CodexHostReleaseRequest{Host: host, LaunchID: launchID})
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
	choice, off, _, err := a.chooseCodexHost(ctx, c, repo, profile, true, true)
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
	// The port-forward belongs to connCtx, not to the command context, so a
	// cancelled launch can still release. This command closes it afterwards.
	connCtx, stopConn := context.WithCancel(context.WithoutCancel(ctx))
	defer stopConn()
	c, err := a.connect(connCtx, cmd.c.conn)
	if err != nil {
		// Nothing has been sent to select yet, so every failure here is
		// "no response" and main's result stands. A failure with no stage is
		// a refused connection setting: say so, and keep its own sentence.
		line, ok := threadSelectionFallback(err)
		if !ok {
			line = selectionNotUsedSettings + " " + err.Error()
		}
		a.errf("%s", line)
		return invalid()
	}
	choice, off, launchID, err := a.chooseCodexHost(ctx, c, t.repo, "", false, false)
	if err != nil {
		if line, ok := threadSelectionFallback(err); ok {
			a.errf("%s", line)
			return invalid()
		}
		return err
	}
	if off {
		a.errf("%s", hostSelectionOff)
		return invalid()
	}
	switch choice.Action {
	case "use":
		t.host = choice.Host
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexHostReleaseTimeout)
			defer cancel()
			if rel := a.releaseCodexHost(releaseCtx, c, t.host, launchID); rel != nil {
				a.errf("%s", rel.Error())
			}
		}()
		if !projectName.MatchString(t.host) {
			return invalid()
		}
		return a.threadOnHost(ctx, cmd, t)
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

// Stages of a select attempt that ended before any HTTP response.
const (
	stageCredentials = "credentials"
	stageMinting     = "minting"
	stageCA          = "ca"
	stageKubectl     = "kubectl"
	stagePortForward = "port-forward"
	stageTransport   = "transport"
)

// selectNoResponse is a failure before select answered. stage is where it
// happened. Error and the exit code are the underlying error's, so other
// commands are unchanged.
type selectNoResponse struct {
	stage string
	err   error
}

func (e *selectNoResponse) Error() string { return e.err.Error() }
func (e *selectNoResponse) Unwrap() error { return e.err }

func noSelectResponse(stage string, err error) error {
	if err == nil {
		return nil
	}
	var already *selectNoResponse
	if errors.As(err, &already) {
		return err
	}
	return &selectNoResponse{stage: stage, err: err}
}

// selectResponse is an HTTP response from select. status is the code the
// operator sent. Error and the exit code are the refusal's.
type selectResponse struct {
	status int
	err    error
}

func (e *selectResponse) Error() string { return e.err.Error() }
func (e *selectResponse) Unwrap() error { return e.err }

func selectTransport(err error) error {
	var none *selectNoResponse
	if errors.As(err, &none) {
		return err
	}
	return noSelectResponse(stageTransport, err)
}

// threadSelectionFallback is the one line thread start adds when select was
// not reached, answered 404, or answered 5xx. A received 401, 403, 409 or 422
// is returned unchanged. The decision reads the error's type, not its text.
func threadSelectionFallback(err error) (string, bool) {
	var resp *selectResponse
	if errors.As(err, &resp) {
		switch {
		case resp.status == http.StatusNotFound:
			return hostSelectionOff, true
		case resp.status >= 500:
			return selectionNotUsedNoAnswer, true
		default:
			return "", false
		}
	}
	var none *selectNoResponse
	if errors.As(err, &none) {
		if none.stage == stageCredentials {
			return selectionNotUsedNoCreds, true
		}
		return selectionNotUsedUnreached, true
	}
	return "", false
}
