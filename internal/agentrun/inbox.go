package agentrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var inboxKeyPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`)
var inboxUIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
var inboxMessagePattern = regexp.MustCompile(`^message\.[a-f0-9]{32}\.[a-f0-9]{32}$`)

func inboxDNSName(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if len(part) > 63 || !directoryProjectName.MatchString(part) {
			return false
		}
	}
	return true
}

func (a *app) inbox(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return a.help([]string{"inbox"})
	}
	if len(args) == 0 || (args[0] != "send" && args[0] != "list" && args[0] != "read" && args[0] != "ack") {
		return usageError("inbox requires send, list, read or ack; see agent-run help inbox")
	}
	verb := args[0]
	cmd := newCommand("inbox", outputJSON)
	var project, namespace, name, uid, key, cursor string
	var limit int
	cmd.fs.StringVar(&project, "project", "", "")
	if verb == "send" {
		cmd.fs.StringVar(&namespace, "recipient-namespace", "", "")
		cmd.fs.StringVar(&name, "recipient-name", "", "")
		cmd.fs.StringVar(&uid, "recipient-uid", "", "")
		cmd.fs.StringVar(&key, "key", "", "")
	}
	if verb == "list" {
		cmd.fs.IntVar(&limit, "limit", 20, "")
		cmd.fs.StringVar(&cursor, "continue", "", "")
	}
	pos, err := cmd.parse(a, args[1:])
	if err != nil {
		return err
	}
	if len(project) > 63 || !directoryProjectName.MatchString(project) {
		return usageError("inbox requires a valid --project")
	}
	if (verb == "send" || verb == "list") && len(pos) != 0 || (verb == "read" || verb == "ack") && (len(pos) != 1 || !inboxMessagePattern.MatchString(pos[0])) {
		return usageError("inbox: invalid arguments; see agent-run help inbox")
	}
	if verb == "send" && (len(namespace) > 63 || !directoryProjectName.MatchString(namespace) || !inboxDNSName(name) || !inboxUIDPattern.MatchString(uid) || !inboxKeyPattern.MatchString(key)) {
		return usageError("inbox send requires valid --recipient-namespace, --recipient-name, --recipient-uid and --key")
	}
	if verb == "list" && (limit < 1 || limit > 100 || len(cursor) > 4096 || !utf8.ValidString(cursor) || strings.ContainsAny(cursor, "\x00\r\n")) {
		return usageError("inbox list requires --limit 1..100 and a bounded cursor")
	}
	var body any
	if verb == "send" {
		text, err := io.ReadAll(io.LimitReader(a.env.Stdin, (16<<10)+1))
		if err != nil || len(text) == 0 || len(text) > 16<<10 || !utf8.Valid(text) {
			return usageError("inbox send requires 1..16384 bytes of UTF-8 on stdin")
		}
		body = apiv1.PublishInboxRequest{Project: project, Recipient: apiv1.TaskClaimOwner{Namespace: namespace, Name: name, UID: uid}, Key: key, Text: string(text)}
	}
	path := apiv1.InboxPath
	q := url.Values{"project": {project}}
	method := http.MethodGet
	if verb == "send" {
		method = http.MethodPost
		q = nil
	}
	if verb == "list" {
		q.Set("limit", strconv.Itoa(limit))
		if cursor != "" {
			q.Set("continue", cursor)
		}
	}
	if verb == "read" || verb == "ack" {
		path += "/" + pos[0]
	}
	if verb == "ack" {
		path += "/ack"
		method = http.MethodPost
		body = struct{}{}
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	switch verb {
	case "send":
		var result apiv1.InboxResult
		status, callErr := a.callWith(ctx, &one, method, path, q, body, &result, false)
		if status == http.StatusConflict {
			return fail(ExitFailed, "inbox intent conflicts with the existing key; no replacement key was generated")
		}
		if callErr != nil {
			return inboxMutationError(status, callErr)
		}
		if (status != http.StatusCreated || result.Outcome != "Created") && (status != http.StatusOK || result.Outcome != "Confirmed") {
			return fail(ExitUnconfirmed, "inbox send returned an unconfirmed receipt; retry only the identical intent with the same key")
		}
		if result.Message.Project != project || result.Message.Key != key || result.Message.Recipient != (apiv1.TaskClaimOwner{Namespace: namespace, Name: name, UID: uid}) || !inboxMessagePattern.MatchString(result.Message.Name) || result.Message.UID == "" {
			return fail(ExitUnconfirmed, "inbox send returned an unconfirmed identity; retry only the identical intent with the same key")
		}
		return a.printJSON(result)
	case "list":
		var result apiv1.InboxList
		status, callErr := a.callWith(ctx, &one, method, path, q, nil, &result, false)
		if callErr != nil {
			return inboxSafeError(status, callErr)
		}
		if result.Items == nil || len(result.Items) > limit || len(result.Continue) > 4096 {
			return fail(ExitFailed, "inbox list returned more than the requested limit")
		}
		for _, item := range result.Items {
			if item.Project != project || !inboxMessagePattern.MatchString(item.Name) || item.UID == "" {
				return fail(ExitFailed, "inbox list returned an unconfirmed identity or project")
			}
		}
		return a.printJSON(result)
	case "read":
		var result apiv1.InboxMessage
		status, callErr := a.callWith(ctx, &one, method, path, q, nil, &result, false)
		if callErr != nil {
			return inboxSafeError(status, callErr)
		}
		if result.Project != project || result.Name != pos[0] || result.UID == "" || len(result.Text) == 0 || len(result.Text) > 16<<10 || !utf8.ValidString(result.Text) {
			return fail(ExitFailed, "inbox read returned an unconfirmed message")
		}
		if cmd.c.output == outputJSON {
			return a.printJSON(result)
		}
		_, err = fmt.Fprint(a.env.Stdout, result.Text)
		return err
	default:
		var result apiv1.InboxAckResult
		status, callErr := a.callWith(ctx, &one, method, path, q, body, &result, false)
		if callErr != nil {
			return inboxMutationError(status, callErr)
		}
		if (status != http.StatusCreated || result.Outcome != "Created") && (status != http.StatusOK || result.Outcome != "Confirmed") {
			return fail(ExitUnconfirmed, "inbox ack returned an unconfirmed receipt; retry only the same acknowledgement")
		}
		if result.Ack.Project != project || result.Ack.MessageName != pos[0] || result.Ack.MessageUID == "" || result.Ack.UID == "" {
			return fail(ExitUnconfirmed, "inbox ack returned an unconfirmed identity; retry only the same acknowledgement")
		}
		return a.printJSON(result)
	}
}

// Both inbox mutations have server-defined idempotent intent; no automatic retry.
func inboxMutationError(status int, err error) error {
	var ee *exitError
	if errors.As(err, &ee) && ee.code == ExitUnconfirmed {
		return fail(ExitUnconfirmed, "inbox outcome Unknown; no request was repeated. Explicitly retry only the identical intent and key (or the same acknowledgement)")
	}
	return inboxSafeError(status, err)
}

// Do not echo arbitrary API error bodies, which could contain credentials or text.
func inboxSafeError(status int, err error) error {
	var ee *exitError
	code := ExitFailed
	if errors.As(err, &ee) {
		code = ee.code
	}
	switch status {
	case 0:
		switch code {
		case ExitAuth:
			return fail(code, "inbox connection or token authentication failed; check token access and the API certificate or --ca-file")
		case ExitRetry:
			return fail(code, "inbox API connection or token acquisition failed before the inbox request; check connectivity and retry")
		case ExitUnconfirmed:
			return fail(code, "inbox read outcome Unknown; no request was repeated; check connectivity before reading again")
		default:
			return fail(code, "inbox request failed before a confirmed HTTP response; check connection and authentication")
		}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fail(code, "inbox request was refused (%d); check the project, recipient, key, text and command arguments", status)
	case http.StatusTooManyRequests:
		return fail(code, "inbox request was refused (429); an inbox limit or rate limit may have been reached; review limits before retrying")
	case http.StatusUnauthorized, http.StatusForbidden:
		return fail(code, "inbox request was refused (%d); check API authentication and project scope", status)
	case http.StatusNotFound:
		return fail(code, "inbox record or authorized scope was not found (404)")
	default:
		return fail(code, "inbox request failed (HTTP status %d, exit %d)", status, code)
	}
}
