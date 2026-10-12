package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func contextCtl(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	kind := fs.String("kind", "", "")
	key := fs.String("key", "", "")
	project := fs.String("project", "", "")
	name := fs.String("name", "", "")
	cursor := fs.String("continue", "", "")
	limit := fs.Int("limit", 20, "")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "agentd: invalid context command arguments")
		return exitUsage
	}
	s, err := agentd.LoadSettings(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: context settings unavailable")
		return exitFailure
	}
	sess, err := agentd.LoadSession(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: context session unavailable")
		return exitFailure
	}
	h, err := agentd.NewHeartbeatClient(s, sess.Name)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: context operator identity unavailable")
		return exitFailure
	}
	var result any
	switch args[0] {
	case "context-publish":
		if *project != "" || *name != "" || *cursor != "" || *limit != 20 {
			return exitUsage
		}
		text, readErr := io.ReadAll(io.LimitReader(stdin, apiv1.MaxContextBytes+1))
		if readErr != nil {
			_, _ = fmt.Fprintln(stderr, "agentd: context stdin unavailable")
			return exitFailure
		}
		req := apiv1.PublishContextRequest{Kind: *kind, Key: *key, Text: string(text)}
		if !apiv1.ValidContextRequest(req) {
			_, _ = fmt.Fprintln(stderr, "agentd: context requires kind, key and UTF-8 stdin text of at most 64KiB")
			return exitUsage
		}
		result, err = agentd.PublishContext(ctx, s, sess, h, req)
	case "context-read":
		if *kind != "" || *key != "" || *cursor != "" || *limit != 20 {
			return exitUsage
		}
		result, err = h.ReadPublishedContext(ctx, *project, *name)
	case "context-list":
		if *kind != "" || *key != "" || *name != "" {
			return exitUsage
		}
		result, err = h.ListPublishedContexts(ctx, *project, *cursor, *limit)
	}
	if errors.Is(err, agentd.ErrContextLimit) {
		_, _ = fmt.Fprintln(stderr, "agentd:", err)
		return exitFailure
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: context request refused or unconfirmed")
		return exitFailure
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return exitFailure
	}
	return exitOK
}
