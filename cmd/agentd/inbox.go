package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func inboxCommand(ctx context.Context, command string, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "explicit inbox project")
	name := fs.String("message", "", "immutable message name")
	limit := fs.Int("limit", 100, "maximum list metadata records, 1 to 100")
	cursor := fs.String("continue", "", "bounded list cursor")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return exitUsage
	}
	path, method, body := apiv1.InboxPath, http.MethodGet, []byte(nil)
	if command == "inbox-publish" {
		if *project != "" || *name != "" || *cursor != "" || *limit != 100 {
			_, _ = fmt.Fprintln(stderr, "agentd: inbox-publish takes intent from bounded JSON stdin only")
			return exitUsage
		}
		data, err := io.ReadAll(io.LimitReader(stdin, agentd.InboxMaxRequestBytes+1))
		if err != nil || len(data) > agentd.InboxMaxRequestBytes || !utf8.Valid(data) {
			_, _ = fmt.Fprintln(stderr, "agentd: inbox publication exceeds UTF-8 JSON bound")
			return exitUsage
		}
		var req apiv1.PublishInboxRequest
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&req) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || len(req.Text) == 0 || len(req.Text) > 16<<10 || !utf8.ValidString(req.Text) {
			_, _ = fmt.Fprintln(stderr, "agentd: expected explicit inbox intent and 1 to 16384 UTF-8 bytes")
			return exitUsage
		}
		body, method = data, http.MethodPost
	} else {
		if *project == "" {
			_, _ = fmt.Fprintln(stderr, "agentd: explicit inbox project is required")
			return exitUsage
		}
		q := url.Values{"project": {*project}}
		if command == "inbox-list" {
			if *name != "" || *limit < 1 || *limit > 100 || len(*cursor) > 4096 {
				return exitUsage
			}
			q.Set("limit", fmt.Sprint(*limit))
			if *cursor != "" {
				q.Set("continue", *cursor)
			}
		} else {
			if !validInboxCLIName(*name) || *limit != 100 || *cursor != "" {
				return exitUsage
			}
			path += "/" + *name
			if command == "inbox-ack" {
				path += "/ack"
				method, body = http.MethodPost, []byte("{}")
			}
		}
		path += "?" + q.Encode()
	}
	s := agentd.Settings{APIURL: strings.TrimSuffix(getenv("AGENTD_API_URL"), "/"), APITokenFile: getenv("AGENTD_API_TOKEN_FILE"), APICAFile: getenv("AGENTD_API_CA_FILE")}
	if s.APITokenFile == "" {
		s.APITokenFile = "/var/run/secrets/dev-env/token"
	}
	c, err := agentd.NewInboxClient(s)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd: inbox API identity or TLS unavailable")
		return exitFailure
	}
	data, status, err := c.Do(ctx, method, path, body)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentd:", err)
		return exitFailure
	}
	if _, err := stdout.Write(append(data, '\n')); err != nil {
		return exitFailure
	}
	if status/100 != 2 {
		return exitFailure
	}
	return exitOK
}
func validInboxCLIName(name string) bool {
	p := strings.Split(name, ".")
	if len(p) != 3 || p[0] != "message" || len(p[1]) != 32 || len(p[2]) != 32 {
		return false
	}
	for _, r := range p[1] + p[2] {
		if (r < 'a' || r > 'f') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
