package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func codexHost(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("codex-host", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := agentd.CodexHostOptions{}
	flags.BoolVar(&options.Enabled, "enabled", false, "")
	flags.StringVar(&options.CatalogFile, "catalog-file", "", "")
	flags.StringVar(&options.InstructionsFile, "instructions-file", "", "")
	flags.BoolVar(&options.DeclaredShutdown, "declared-shutdown", false, "")
	var grace int
	flags.IntVar(&grace, "termination-grace-seconds", 0, "")
	if flags.Parse(args) != nil || flags.NArg() != 1 {
		return exitUsage
	}
	encode := func(status agentd.CodexHostStatus) error { return json.NewEncoder(stdout).Encode(status) }
	if !options.Enabled {
		if encode(agentd.CodexHostStatus{Code: "Disabled"}) != nil {
			return exitFailure
		}
		return exitOK
	}
	if getenv(protocol.SessionEnv) != "" || getenv(protocol.SessionFileEnv) != "" || grace < 0 || grace > 600 {
		return exitUsage
	}
	// Declared shutdown budget is independently tied to the trusted pod config.
	if options.DeclaredShutdown {
		actual, err := strconv.Atoi(getenv("AGENTD_HOST_TERMINATION_GRACE_SECONDS"))
		if err != nil || actual != grace || actual == 0 {
			return exitUsage
		}
	}
	options.TerminationGrace = time.Duration(grace) * time.Second
	settings, err := agentd.LoadSettings(getenv)
	if err != nil {
		return exitUsage
	}
	switch flags.Arg(0) {
	case "status":
		var status agentd.CodexHostStatus
		status, err = agentd.ObserveCodexHost(ctx, settings, options)
		if encode(status) != nil {
			return exitFailure
		}
		if err != nil {
			return exitFailure
		}
	case "run":
		err = agentd.RunCodexHost(ctx, settings, options, encode)
	case "pair":
		tty, ok := stdout.(*os.File)
		if !ok {
			return exitUsage
		}
		err = agentd.PairCodexHost(ctx, settings, options, tty)
	case "stop":
		err = agentd.StopCodexHost(ctx, settings, options)
	default:
		return exitUsage
	}
	if err != nil {
		if encode(agentd.CodexHostStatus{Code: "Refused"}) != nil {
			return exitFailure
		}
		return exitFailure
	}
	return exitOK
}
