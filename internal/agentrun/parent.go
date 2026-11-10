package agentrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func parentNoteKind(kind string) bool {
	return kind == apiv1.ParentNoteQuestion || kind == apiv1.ParentNoteResult || kind == apiv1.ParentNoteBlocker
}

func (a *app) parent(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return a.help([]string{"parent"})
	}
	if len(args) == 0 || (args[0] != "tell" && args[0] != "list" && args[0] != "read") {
		return usageError("parent requires tell, list or read; see agent-run help parent")
	}
	verb := args[0]
	cmd := newCommand("parent", outputJSON)
	var req apiv1.TellParentRequest
	var cursor string
	var sent bool
	var limit int
	if verb == "tell" {
		cmd.fs.StringVar(&req.Key, "key", "", "")
		cmd.fs.StringVar(&req.Kind, "kind", apiv1.ParentNoteQuestion, "")
	}
	if verb == "list" {
		cmd.fs.BoolVar(&sent, "sent", false, "")
		cmd.fs.IntVar(&limit, "limit", 20, "")
		cmd.fs.StringVar(&cursor, "continue", "", "")
	}
	pos, err := cmd.parse(a, args[1:])
	if err != nil {
		return err
	}
	if verb == "read" && (len(pos) != 1 || !inboxMessagePattern.MatchString(pos[0])) || verb != "read" && len(pos) != 0 {
		return usageError("parent: invalid arguments; see agent-run help parent")
	}
	if verb == "tell" {
		if !inboxKeyPattern.MatchString(req.Key) || !parentNoteKind(req.Kind) {
			return usageError("parent tell requires a valid --key and --kind question, result or blocker")
		}
		data, err := io.ReadAll(io.LimitReader(a.env.Stdin, (16<<10)+1))
		if err != nil || len(data) == 0 || len(data) > 16<<10 || !utf8.Valid(data) {
			return usageError("parent tell requires 1..16384 UTF-8 bytes on stdin")
		}
		req.Text = string(data)
	}
	if verb == "list" && (limit < 1 || limit > 100 || len(cursor) > 4096 || !utf8.ValidString(cursor) || strings.ContainsAny(cursor, "\x00\r\n")) {
		return usageError("parent list requires --limit 1..100 and a bounded cursor")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return parentNoteError(0, err, verb == "tell")
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	switch verb {
	case "tell":
		var result apiv1.InboxResult
		status, err := a.callWith(ctx, &one, http.MethodPost, apiv1.ParentNotesPath, nil, req, &result, false)
		if err != nil {
			return parentNoteError(status, err, true)
		}
		if (status != http.StatusCreated || result.Outcome != "Created") && (status != http.StatusOK || result.Outcome != "Confirmed") {
			return fail(ExitUnconfirmed, "parent tell returned an unconfirmed receipt; retry only the identical intent with the same key")
		}
		if !validParentNoteMetadata(result.Message) || result.Message.Key != req.Key || result.Message.Kind != req.Kind {
			return fail(ExitUnconfirmed, "parent tell returned an unconfirmed identity; retry only the identical intent with the same key")
		}
		return a.printJSON(result)
	case "list":
		box := apiv1.ParentNotesInbox
		if sent {
			box = apiv1.ParentNotesSent
		}
		q := url.Values{"box": {box}, "limit": {strconv.Itoa(limit)}}
		if cursor != "" {
			q.Set("continue", cursor)
		}
		var result apiv1.InboxList
		status, err := a.callWith(ctx, &one, http.MethodGet, apiv1.ParentNotesPath, q, nil, &result, false)
		if err != nil {
			return parentNoteError(status, err, false)
		}
		if status != http.StatusOK || result.Box != box || result.Items == nil || len(result.Items) > limit || len(result.Continue) > 4096 {
			return fail(ExitFailed, "parent list returned an unconfirmed box or page")
		}
		for _, item := range result.Items {
			if !validParentNoteMetadata(item) {
				return fail(ExitFailed, "parent list returned an unconfirmed note")
			}
		}
		return a.printJSON(result)
	default: // read
		var result apiv1.InboxMessage
		status, err := a.callWith(ctx, &one, http.MethodGet, apiv1.ParentNotesPath+"/"+pos[0], nil, nil, &result, false)
		if err != nil {
			return parentNoteError(status, err, false)
		}
		if status != http.StatusOK || !validParentNoteMetadata(result.InboxMetadata) || result.Name != pos[0] || len(result.Text) == 0 || len(result.Text) > 16<<10 || !utf8.ValidString(result.Text) {
			return fail(ExitFailed, "parent read returned an unconfirmed note")
		}
		if cmd.c.output == outputJSON {
			return a.printJSON(result)
		}
		_, err = fmt.Fprint(a.env.Stdout, result.Text)
		return err
	}
}

func validParentNoteMetadata(m apiv1.InboxMetadata) bool {
	validParty := func(p apiv1.TaskClaimOwner) bool {
		return len(p.Namespace) <= 63 && directoryProjectName.MatchString(p.Namespace) && inboxDNSName(p.Name) && inboxUIDPattern.MatchString(p.UID)
	}
	return inboxMessagePattern.MatchString(m.Name) && inboxUIDPattern.MatchString(m.UID) &&
		len(m.Project) <= 63 && directoryProjectName.MatchString(m.Project) && inboxKeyPattern.MatchString(m.Key) &&
		parentNoteKind(m.Kind) && m.InReplyTo == nil && validParty(m.Sender) && validParty(m.Recipient)
}

// Refusals never echo an API body, which may contain private note text.
// Tell sends once and preserves the shared client's uncertainty exit status.
func parentNoteError(status int, err error, mutation bool) error {
	code := ExitFailed
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	if mutation && code == ExitUnconfirmed {
		return fail(code, "parent tell outcome Unknown; no request was repeated. Retry only the identical intent with the same key")
	}
	switch status {
	case http.StatusBadRequest:
		return fail(code, "parent request was refused (400); check key, kind, text and command arguments")
	case http.StatusUnprocessableEntity:
		return fail(code, "parent tell was refused (422); a child needs a project snapshot, no workspace, and a recorded session parent")
	case http.StatusNotFound:
		return fail(code, "parent note, recorded parent or authorized scope was not found (404); the routes may be disabled")
	case http.StatusConflict:
		return fail(code, "parent tell conflicts with the existing key (409); nothing was replaced and no new key was generated")
	case http.StatusTooManyRequests:
		return fail(code, "parent request was refused (429); review the sender and project inbox limits before retrying")
	case 0:
		return fail(code, "parent request has no confirmed HTTP response; check connectivity, token access and the API certificate or --ca-file")
	default:
		return fail(code, "parent request failed (HTTP status %d, exit %d)", status, code)
	}
}
