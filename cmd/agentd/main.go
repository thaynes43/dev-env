// Command agentd is the supervisor inside each session pod, a child of tini
// (DESIGN-001 3.6). It renders config at boot, clones the repo, starts or
// resumes the agent in tmux, sends heartbeats, and answers
// `agentd ctl status|rescue|prepare-restart|deliver` for the operator.
//
// Not built yet. Only `version` works. Boot, heartbeat and `ctl status|rescue`
// arrive in plan 01 (KICKOFF section 4, step 4).
package main

import (
	"fmt"
	"os"

	"github.com/thaynes43/dev-env/internal/version"
)

const binaryName = "agentd"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.Get().String(binaryName))
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s: not built yet; only `version` works. Boot, heartbeat and `ctl status|rescue` arrive in plan 01 (KICKOFF section 4, step 4).\n", binaryName)
	os.Exit(1)
}
