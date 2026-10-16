package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func taskClaimCommand(ctx context.Context, command string, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "explicit project")
	name := fs.String("claim", "", "immutable claim name")
	uid := fs.String("uid", "", "optional original claim UID")
	limit := fs.Int("limit", 100, "one page of 1..100 records")
	cursor := fs.String("continue", "", "opaque continuation cursor")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return exitUsage
	}
	var intent struct {
		Identity apiv1.TaskClaimIdentity `json:"identity"`
	}
	switch command {
	case "task-claim-create":
		if fs.NFlag() != 0 {
			return exitUsage
		}
		data, err := io.ReadAll(io.LimitReader(stdin, agentd.TaskClaimMaxRequestBytes+1))
		if err != nil || len(data) > agentd.TaskClaimMaxRequestBytes || !utf8.Valid(data) {
			return exitUsage
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&intent) != nil || d.Decode(new(any)) != io.EOF || !agentd.ValidTaskClaimIdentity(intent.Identity) {
			return exitUsage
		}
	case "task-claim-list":
		if *name != "" || *uid != "" || !agentd.ValidTaskClaimPage(*project, *limit, *cursor) {
			return exitUsage
		}
	case "task-claim-get":
		if *limit != 100 || *cursor != "" || !agentd.ValidTaskClaimRead(*project, *name, *uid) {
			return exitUsage
		}
	default:
		return exitUsage
	}
	s := agentd.Settings{APIURL: strings.TrimSuffix(getenv("AGENTD_API_URL"), "/"), APITokenFile: getenv("AGENTD_API_TOKEN_FILE"), APICAFile: getenv("AGENTD_API_CA_FILE")}
	if s.APITokenFile == "" {
		s.APITokenFile = "/var/run/secrets/dev-env/token"
	}
	c, err := agentd.NewTaskClaimClient(s)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: task claim API identity or TLS unavailable")
		return exitFailure
	}
	var data []byte
	var status int
	switch command {
	case "task-claim-create":
		data, status, err = c.Create(ctx, intent.Identity)
	case "task-claim-list":
		data, status, err = c.List(ctx, *project, *limit, *cursor)
	case "task-claim-get":
		data, status, err = c.Get(ctx, *project, *name, *uid)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd:", err)
		return exitFailure
	}
	if _, err = stdout.Write(append(data, '\n')); err != nil {
		return exitFailure
	}
	if status/100 != 2 {
		return exitFailure
	}
	return exitOK
}
