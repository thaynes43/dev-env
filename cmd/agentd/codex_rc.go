package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/thaynes43/dev-env/internal/agentd"
)

func codexRCCtl(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 1 || args[0] != "preflight" && args[0] != "start" {
		return exitUsage
	}
	settings, err := agentd.LoadSettings(getenv)
	if err != nil {
		return exitFailure
	}
	session, err := agentd.LoadSession(getenv)
	if err != nil {
		return exitFailure
	}
	request, err := agentd.ReadCodexRCDispatch(stdin)
	if err != nil {
		return exitUsage
	}
	receipt, err := agentd.CodexRCControl(ctx, settings, session, args[0], request)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "codex RC operation refused")
		return exitFailure
	}
	if json.NewEncoder(stdout).Encode(receipt) != nil {
		return exitFailure
	}
	return exitOK
}
