package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
)

// codexPairCtl is `ctl codex-pair` (Story110 T17): the coordinator's way to get
// a phone pairing code from a Codex --interactive session's daemon. The code
// goes to stdout and nowhere else: not a log, a file, an argument or an error.
// The few words on stderr never carry it.
func codexPairCtl(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintf(stderr, "%s: usage: ctl codex-pair\n", binaryName)
		return exitUsage
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
		return exitFailure
	}
	sess, err := agentd.LoadSession(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
		return exitFailure
	}
	if err := agentd.CodexPair(ctx, s, sess, stdout, time.Now); err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: codex-pair: %v\n", binaryName, err)
		return exitFailure
	}
	_, _ = fmt.Fprintf(stderr, "%s: enter the code in the ChatGPT app (Remote, add a computer) within about ten minutes\n", binaryName)
	return exitOK
}
