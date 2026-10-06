// Command dev-env-operator is the dev-env v2 control plane (DESIGN-001 3.1): it
// serves the /v1 API and reconciles AgentSession resources into pods and volumes.
// The access broker is a second mode of this binary (`dev-env-operator broker`,
// DESIGN-001 6.12), deployed as its own Deployment with its own ServiceAccount.
//
// Not built yet. Only `version` works. The controller and the /v1 API arrive in
// plan 01 (KICKOFF section 4, steps 1 to 3); the broker mode in plan 07.
package main

import (
	"fmt"
	"os"

	"github.com/thaynes43/dev-env/internal/version"
)

const binaryName = "dev-env-operator"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.Get().String(binaryName))
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s: not built yet; only `version` works. The controller and the /v1 API arrive in plan 01 (KICKOFF section 4, steps 1 to 3), the broker mode in plan 07.\n", binaryName)
	os.Exit(1)
}
