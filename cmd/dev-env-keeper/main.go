// Command dev-env-keeper is the only holder of dev-env v2's rotating credentials
// and GitHub App keys (DESIGN-001 3.1). It mints and refreshes them and writes
// the short-lived results into Secrets that session pods read. It runs as one
// replica, so each rotating refresh token has exactly one owner.
//
// It is a binary of its own, shipped in the operator image (D-38).
//
// Not built yet. Only `version` works. The minimal keeper, which mints the gh
// token every 40 minutes (D-13), arrives in plan 01 (KICKOFF section 4, step 6).
package main

import (
	"fmt"
	"os"

	"github.com/thaynes43/dev-env/internal/version"
)

const binaryName = "dev-env-keeper"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.Get().String(binaryName))
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "%s: not built yet; only `version` works. The minimal keeper (the gh token every 40 minutes) arrives in plan 01 (KICKOFF section 4, step 6).\n", binaryName)
	os.Exit(1)
}
