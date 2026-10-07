// Command agent-run is the dev-env v2 CLI: a client of the operator's /v1 API
// that runs anywhere, in a session pod, in the v1 pod, on a laptop or in CI
// (DESIGN-001 3.5). It is one static binary with no runtime dependency (D-06),
// built with CGO_ENABLED=0. The commands live in internal/agentrun (D-50).
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentrun"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := agentrun.Run(ctx, os.Args[1:], agentrun.DefaultEnv())
	stop()
	os.Exit(code)
}

// run is main without the process: it takes the arguments after the program name
// and returns the exit code, so tests can call it directly.
func run(args []string, stdout, stderr io.Writer) int {
	env := agentrun.DefaultEnv()
	env.Stdout, env.Stderr = stdout, stderr
	return agentrun.Run(context.Background(), args, env)
}
