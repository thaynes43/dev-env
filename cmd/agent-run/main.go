// Command agent-run is the dev-env v2 CLI: a client of the operator's /v1 API
// that runs anywhere, in a session pod, on a laptop or in CI (DESIGN-001 3.5). It
// is one static binary with no runtime dependency (D-06), built with
// CGO_ENABLED=0.
//
// Only `version` exists so far. The session verbs (-p, list, reap, fleet) arrive
// in plan 01 step 7 (KICKOFF section 4); the rest of DESIGN-001 3.5 follows in
// later plans.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/thaynes43/dev-env/internal/version"
)

const binaryName = "agent-run"

// Exit codes. 2 is a usage error, as in most CLIs.
const (
	exitOK    = 0
	exitUsage = 2
)

const usage = `Usage: agent-run <command>

Commands:
  version   Print the version, commit, Go version and platform.
  help      Print this help.

The session verbs (-p, list, reap, fleet) are not built yet. They arrive in
plan 01 step 7; v1's agent-run in the dev-env pod still runs sessions today.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process: it takes the arguments after the program name
// and returns the exit code, so tests can call it directly.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}

	switch args[0] {
	case "version", "--version":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "%s: version takes no arguments, got %q\n", binaryName, args[1:])
			return exitUsage
		}
		_, _ = fmt.Fprintln(stdout, version.Get().String(binaryName))
		return exitOK
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "%s: unknown command %q\n\n%s", binaryName, args[0], usage)
		return exitUsage
	}
}
