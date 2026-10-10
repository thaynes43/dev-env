package main

import (
	"fmt"
	"io"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
)

// This entry remains unavailable until a trusted off-pod budget adapter is
// wired. Flags, environment variables and a local file are not that authority.
func ownedCodexHost(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, hostexecutor.ErrDisabled)
	return exitFailure
}

func ownedCodexHostHelper(stderr io.Writer) int {
	if err := hostexecutor.Helper(); err != nil {
		_, _ = fmt.Fprintln(stderr, "owned executor helper failed")
		return exitFailure
	}
	return exitOK
}
