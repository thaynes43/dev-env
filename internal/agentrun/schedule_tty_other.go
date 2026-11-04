//go:build !linux && !darwin

package agentrun

import "os"

// isTerminal is false where agent-run has no terminal check, so approve and
// retry there need --yes.
func isTerminal(*os.File) bool { return false }
