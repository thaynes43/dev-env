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
	"time"
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
	if len(args) > 0 && (args[0] == "answer" || args[0] == "wait") {
		return a.parentAnswerWait(ctx, args[0], args[1:])
	}
	if len(args) == 0 || (args[0] != "tell" && args[0] != "list" && args[0] != "read") {
		return usageError("parent requires tell, list, read, answer or wait; see agent-run help parent")
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
		return parentNoteError(0, err, verb)
	}
	one := parentNoteClient(c, 0)
	switch verb {
	case "tell":
		var result apiv1.InboxResult
		status, err := a.callWith(ctx, one, http.MethodPost, apiv1.ParentNotesPath, nil, req, &result, false)
		if err != nil {
			return parentNoteError(status, err, verb)
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
		status, err := a.callWith(ctx, one, http.MethodGet, apiv1.ParentNotesPath, q, nil, &result, false)
		if err != nil {
			return parentNoteError(status, err, verb)
		}
		if status != http.StatusOK || result.Box != box || result.Items == nil || len(result.Items) > limit || len(result.Continue) > 4096 {
			return fail(ExitFailed, "parent list returned an unconfirmed box or page")
		}
		for _, item := range result.Items {
			if !validParentNoteMetadata(item) && !validParentReplyMetadata(item, "") && !validParentPlainMetadata(item) {
				return fail(ExitFailed, "parent list returned an unconfirmed note")
			}
		}
		return a.printJSON(result)
	default: // read
		var result apiv1.InboxMessage
		status, err := a.callWith(ctx, one, http.MethodGet, apiv1.ParentNotesPath+"/"+pos[0], nil, nil, &result, false)
		if err != nil {
			return parentNoteError(status, err, verb)
		}
		if status != http.StatusOK || (!validParentNoteMetadata(result.InboxMetadata) && !validParentReplyMetadata(result.InboxMetadata, "") && !validParentPlainMetadata(result.InboxMetadata)) || result.Name != pos[0] || len(result.Text) == 0 || len(result.Text) > 16<<10 || !utf8.ValidString(result.Text) {
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
	return validParentMetadata(m) && parentNoteKind(m.Kind) && m.InReplyTo == nil
}

func validParentReplyMetadata(m apiv1.InboxMetadata, note string) bool {
	return validParentMetadata(m) && m.Kind == apiv1.InboxKindReply && m.InReplyTo != nil &&
		inboxMessagePattern.MatchString(m.InReplyTo.Name) && inboxUIDPattern.MatchString(m.InReplyTo.UID) &&
		(note == "" || m.InReplyTo.Name == note)
}

const parentCallerAccount = "dev-env-system/dev-env-meatball"

func validParentParty(p apiv1.TaskClaimOwner) bool {
	return len(p.Namespace) <= 63 && directoryProjectName.MatchString(p.Namespace) && inboxDNSName(p.Name) && inboxUIDPattern.MatchString(p.UID)
}

func validParentCaller(c *apiv1.InboxCaller) bool {
	return c != nil && c.ServiceAccount == parentCallerAccount && inboxUIDPattern.MatchString(c.PolicyUID)
}

// validParentEnd accepts exactly one party form. A session parent keeps two
// session identities. The Meatball account uses the caller form on its side.
func validParentEnd(session apiv1.TaskClaimOwner, caller *apiv1.InboxCaller) bool {
	sessionSet := session.Namespace != "" || session.Name != "" || session.UID != ""
	if sessionSet == (caller != nil) {
		return false
	}
	if caller != nil {
		return validParentCaller(caller)
	}
	return validParentParty(session)
}

func validParentMetadata(m apiv1.InboxMetadata) bool {
	return inboxMessagePattern.MatchString(m.Name) && inboxUIDPattern.MatchString(m.UID) &&
		len(m.Project) <= 63 && directoryProjectName.MatchString(m.Project) && inboxKeyPattern.MatchString(m.Key) &&
		validParentEnd(m.Sender, m.CallerSender) && validParentEnd(m.Recipient, m.CallerRecipient)
}

func validParentPlainMetadata(m apiv1.InboxMetadata) bool {
	return validParentMetadata(m) && (m.Kind == "" || m.Kind == apiv1.InboxKindPlain) && m.InReplyTo == nil && m.CallerSender != nil
}

// Copy both layers: the long wait changes only this request's timeout. All
// parent commands refuse redirects and oversized receipts before printing.
func parentNoteClient(c *conn, wait time.Duration) *conn {
	one, hc := *c, *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if wait > 0 {
		hc.Timeout = wait + 2*time.Minute
	}
	one.http, one.rejectOversizeResponse = &hc, true
	return &one
}

// Refusals never echo an API body, which may contain private note text.
// Tell sends once and preserves the shared client's uncertainty exit status.
func parentNoteError(status int, err error, verb string) error {
	code := ExitFailed
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	if (verb == "tell" || verb == "answer") && code == ExitUnconfirmed {
		if status == http.StatusServiceUnavailable {
			return fail(code, "parent %s outcome Unknown (503): parent notes or the durable inbox may not be enabled on this operator. No request was repeated. Retry only the identical intent with the same key", verb)
		}
		return fail(code, "parent %s outcome Unknown; no request was repeated. Retry only the identical intent with the same key", verb)
	}
	switch status {
	case http.StatusBadRequest:
		return fail(code, "parent request was refused (400); check key, kind, text and command arguments")
	case http.StatusUnprocessableEntity:
		return fail(code, "parent tell was refused (422); a child needs a project snapshot, no workspace, and a recorded session parent")
	case http.StatusNotFound:
		return fail(code, "parent note, recorded parent or authorized scope was not found (404); an operator older than parent notes also answers 404")
	case http.StatusServiceUnavailable:
		return fail(code, "parent request was refused (503); parent notes or the durable inbox are not enabled on this operator, or the inbox is unavailable")
	case http.StatusConflict:
		if verb == "answer" {
			return fail(code, "parent answer conflicts with the existing key or answer (409); nothing was replaced and no new key was generated")
		}
		return fail(code, "parent tell conflicts with the existing key (409); nothing was replaced and no new key was generated")
	case http.StatusTooManyRequests:
		return fail(code, "parent request was refused (429); review held waits and the sender and project inbox limits before retrying")
	case 0:
		return fail(code, "parent request has no confirmed HTTP response; check connectivity, token access and the API certificate or --ca-file")
	default:
		return fail(code, "parent request failed (HTTP status %d, exit %d)", status, code)
	}
}
