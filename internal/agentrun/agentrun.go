// Package agentrun is agent-run v2, the dev-env CLI (DESIGN-001 3.5, D-06,
// D-50): a client of the operator's /v1 API. Plan 01 builds `-p` (create a task
// session), `list`, `show`, `reap` and `fleet`; the other verbs of 3.5 arrive
// with the plans that build their routes.
//
// It imports the standard library, the API's wire types (internal/apiserver/apiv1)
// and agentd's protocol constants only, never the Kubernetes libraries, so the
// binary stays static and small. `make build` checks both.
package agentrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/version"
)

// BinaryName is the command's name in messages.
const BinaryName = "agent-run"

// Exit codes (D-50). Agents branch on them, so each one means one thing.
const (
	// ExitOK: done.
	ExitOK = 0
	// ExitFailed: the API refused the request (400, 409, 422, 500 and the
	// like), answered something unexpected, or a created session failed.
	ExitFailed = 1
	// ExitUsage: a bad command, flag or argument. Nothing was sent.
	ExitUsage = 2
	// ExitAuth: agent-run found no way to reach or authenticate to the API,
	// or the API answered 401 or 403. Retrying the same call will not help.
	ExitAuth = 3
	// ExitNotFound: no such session (404).
	ExitNotFound = 4
	// ExitRetry: try again later. A limit was reached (429), or the API was
	// unavailable (503) or unreachable after agent-run's own retries.
	ExitRetry = 5
)

// Env is what the CLI reads from and writes to outside its arguments. Tests
// replace every part of it.
type Env struct {
	Stdout, Stderr io.Writer
	// Stdin is read by --prompt-file -.
	Stdin io.Reader
	// Getenv reads an environment variable.
	Getenv func(string) string
	// Now is the clock.
	Now func() time.Time
	// Sleep waits for d or until ctx ends.
	Sleep func(ctx context.Context, d time.Duration) error
	// NewKey returns a fresh idempotency key for a create.
	NewKey func() string
	// SessionTokenFile is where a session pod's projected token for the
	// audience dev-env-operator is mounted, unless AGENTD_API_TOKEN_FILE says
	// otherwise.
	SessionTokenFile string
	// ServiceAccountDir holds a pod's own ServiceAccount token, namespace and
	// the cluster's CA, as the kubelet mounts them.
	ServiceAccountDir string
}

// DefaultEnv is the process's own environment.
func DefaultEnv() Env {
	return Env{
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
		Stdin:             os.Stdin,
		Getenv:            os.Getenv,
		Now:               time.Now,
		Sleep:             sleep,
		NewKey:            newKey,
		SessionTokenFile:  DefaultSessionTokenFile,
		ServiceAccountDir: DefaultServiceAccountDir,
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// newKey is "agent-run-" and 16 random hex digits: a label value, as the API
// requires of an idempotency key.
func newKey() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return BinaryName + "-" + hex.EncodeToString(b[:])
}

// exitError ends a command with a message and an exit code.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...)}
}

func usageError(format string, args ...any) error {
	return fail(ExitUsage, format, args...)
}

// app is one run of the CLI.
type app struct {
	env Env
}

// Run runs the CLI on args, the arguments after the program name, and returns
// the exit code.
func Run(ctx context.Context, args []string, env Env) int {
	a := &app{env: env}
	err := a.dispatch(ctx, args)
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		ee = &exitError{code: ExitFailed, msg: err.Error()}
	}
	if ee.msg != "" {
		a.errf("%s", ee.msg)
	}
	return ee.code
}

// errf writes one message to stderr, prefixed with the command's name.
func (a *app) errf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.env.Stderr, BinaryName+": "+format+"\n", args...)
}

func (a *app) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		_, _ = io.WriteString(a.env.Stderr, usageText)
		return &exitError{code: ExitUsage}
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version":
		if len(rest) > 0 {
			return usageError("version takes no arguments, got %q", rest)
		}
		_, _ = fmt.Fprintln(a.env.Stdout, version.Get().String(BinaryName))
		return nil
	case "help", "-h", "--help":
		return a.help(rest)
	case "run":
		return a.create(ctx, rest)
	case "list":
		return a.list(ctx, rest)
	case "show":
		return a.show(ctx, rest)
	case "reap":
		return a.reap(ctx, rest)
	case "fleet":
		return a.fleet(ctx, rest)
	}
	if msg, ok := notYet[cmd]; ok {
		return usageError("%s", msg)
	}
	// v1's form: flags first, or the repository as the first word. A word one
	// or two letters from a command is a typo, as v1 says too; a repository
	// with such a name is given with --repo.
	if !strings.HasPrefix(cmd, "-") {
		if near := nearestCommand(cmd); near != "" {
			return usageError("unknown command %q; did you mean %q? For a repository named %s, use --repo %s", cmd, near, cmd, cmd)
		}
	}
	return a.create(ctx, args)
}

// commands are the words agent-run takes first, built or not yet.
var commands = []string{"run", "list", "show", "reap", "fleet", "version", "help", "attach", "detach", "prune", "sweep", "codex-remote"}

// nearestCommand is the command within two edits of word, or "".
func nearestCommand(word string) string {
	best, bestD := "", 3
	for _, c := range commands {
		if d := editDistance(word, c); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// notYet answers v1's verbs that v2 does not build in plan 01.
var notYet = map[string]string{
	"attach":       "attach arrives with interactive sessions (plan 02); a task session has no terminal to attach to, so follow it with agent-run show <name>",
	"detach":       "detach arrives with interactive sessions (plan 02)",
	"prune":        "prune is gone in v2: the operator reaps sessions itself (DESIGN-001 4.3), and agent-run reap <name> reaps one now",
	"sweep":        "sweep is gone in v2: the operator reaps sessions itself (DESIGN-001 4.3), and agent-run reap <name> reaps one now",
	"codex-remote": "codex-remote arrives with the codex hub (plan 04); v1's agent-run in the dev-env pod still runs it",
}

func (a *app) help(args []string) error {
	if len(args) == 0 {
		_, _ = io.WriteString(a.env.Stdout, usageText)
		return nil
	}
	if len(args) > 1 {
		return usageError("help takes one command, got %q", args)
	}
	text, ok := commandHelp[args[0]]
	if !ok {
		return usageError("no command %q; agent-run help lists the commands", args[0])
	}
	_, _ = io.WriteString(a.env.Stdout, text)
	return nil
}
