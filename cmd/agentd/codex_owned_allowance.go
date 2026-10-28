package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
)

func codexOwnedAllowance(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("codex-owned-allowance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	enabled := fs.Bool("enabled", false, "explicitly enable the local diagnostic")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return exitUsage
	}
	if !*enabled {
		return exitFailure
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		return exitFailure
	}
	session, err := agentd.LoadSession(getenv)
	if err != nil {
		return exitFailure
	}
	result, err := agentd.CaptureOwnedAllowance(ctx, s, session, func() (agentd.OwnedAllowanceContext, error) {
		current, e := agentd.LoadSession(getenv)
		if e != nil {
			return agentd.OwnedAllowanceContext{}, e
		}
		settings, e := agentd.LoadSettings(getenv)
		if e != nil {
			return agentd.OwnedAllowanceContext{}, e
		}
		return agentd.OwnedAllowanceContext{Session: current, PodUID: settings.PodUID}, nil
	})
	if err != nil {
		return exitFailure
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return exitFailure
	}
	return exitOK
}
func codexOwnedAllowanceHelper(stderr io.Writer) int {
	if hostexecutor.AllowanceHelper() != nil {
		return exitFailure
	}
	return exitOK
}
