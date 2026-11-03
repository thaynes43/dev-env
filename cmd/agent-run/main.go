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
	"path/filepath"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentrun"
	"github.com/thaynes43/dev-env/internal/usagecollector"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "internal-allowance-capture" {
		// Independent of the exec stream/operator leader. A hard interruption
		// leaves a durable uncertain marker and cannot authorize replay.
		hardStop := time.AfterFunc(115*time.Second, func() { os.Exit(1) })
		err := usagecollector.RunInternal(ctx, os.Stdin, os.Stdout)
		hardStop.Stop()
		stop()
		if err != nil {
			// Acquisition already emitted any closed diagnostic frames. An
			// unrelated text line would invalidate that strict side channel.
			os.Exit(1)
		}
		return
	}
	// The image links declare-activity to agent-run (D-66).
	if filepath.Base(os.Args[0]) == "declare-activity" {
		args = append([]string{"declare-activity"}, args...)
	}
	code := agentrun.Run(ctx, args, agentrun.DefaultEnv())
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
