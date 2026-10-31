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
	if len(args) == 0 || (args[0] != "send" && args[0] != "ask" && args[0] != "reply" && args[0] != "list" && args[0] != "read" && args[0] != "ack") {
		return usageError("inbox requires send, ask, reply, list, read or ack; see agent-run help inbox")
	}
	verb := args[0]
	publish := verb == "send" || verb == "ask" || verb == "reply"
	cmd := newCommand("inbox", outputJSON)
	var project, namespace, name, uid, key, cursor string
	var limit int
	var sent bool
	cmd.fs.StringVar(&project, "project", "", "")
	if publish {
		cmd.fs.StringVar(&key, "key", "", "")
	}
	if verb == "send" {
		cmd.fs.StringVar(&namespace, "recipient-namespace", "", "")
		cmd.fs.StringVar(&name, "recipient-name", "", "")
		cmd.fs.StringVar(&uid, "recipient-uid", "", "")
	}
	if verb == "ask" {
		cmd.fs.StringVar(&name, "to", "", "")
		cmd.fs.StringVar(&uid, "to-uid", "", "")
	}
	if verb == "list" {
		cmd.fs.IntVar(&limit, "limit", 20, "")
		cmd.fs.StringVar(&cursor, "continue", "", "")
		cmd.fs.BoolVar(&sent, "sent", false, "")
	}
	pos, err := cmd.parse(a, args[1:])
	if err != nil {
		return err
	}
	if len(project) > 63 || !directoryProjectName.MatchString(project) {
		return usageError("inbox requires a valid --project")
	}
	onMessage := verb == "read" || verb == "ack" || verb == "reply"
	if !onMessage && len(pos) != 0 || onMessage && (len(pos) != 1 || !inboxMessagePattern.MatchString(pos[0])) {
		return usageError("inbox: invalid arguments; see agent-run help inbox")
	}
	if verb == "send" && (len(namespace) > 63 || !directoryProjectName.MatchString(namespace) || !inboxDNSName(name) || !inboxUIDPattern.MatchString(uid) || !inboxKeyPattern.MatchString(key)) {
		return usageError("inbox send requires valid --recipient-namespace, --recipient-name, --recipient-uid and --key")
	}
	if verb == "ask" && (!inboxKeyPattern.MatchString(key) || (name != "" && !inboxDNSName(name)) || (uid != "" && (name == "" || !inboxUIDPattern.MatchString(uid)))) {
		return usageError("inbox ask requires a valid --key, an optional valid --to session and --to-uid only with --to")
	}
	if verb == "reply" && !inboxKeyPattern.MatchString(key) {
		return usageError("inbox reply requires a valid --key")
	}
	if verb == "list" && (limit < 1 || limit > 100 || len(cursor) > 4096 || !utf8.ValidString(cursor) || strings.ContainsAny(cursor, "\x00\r\n")) {
		return usageError("inbox list requires --limit 1..100 and a bounded cursor")
	}
	var body any
	var request apiv1.PublishInboxRequest
	if publish {
		text, err := io.ReadAll(io.LimitReader(a.env.Stdin, (16<<10)+1))
		if err != nil || len(text) == 0 || len(text) > 16<<10 || !utf8.Valid(text) {
			return usageError("inbox %s requires 1..16384 bytes of UTF-8 on stdin", verb)
		}
		request = apiv1.PublishInboxRequest{Project: project, Key: key, Text: string(text)}
		switch verb {
		case "send":
			request.Recipient = apiv1.TaskClaimOwner{Namespace: namespace, Name: name, UID: uid}
		case "ask":
			// With no --to, the server resolves the caller's recorded parent.
			request.Kind, request.ToParent = apiv1.InboxKindQuestion, name == ""
			request.Recipient = apiv1.TaskClaimOwner{Name: name, UID: uid}
		case "reply":
			request.Kind, request.InReplyTo = apiv1.InboxKindReply, &apiv1.InboxMessageRef{Name: pos[0]}
		}
		body = request
	}
	path := apiv1.InboxPath
	q := url.Values{"project": {project}}
	method := http.MethodGet
	if publish {
		method = http.MethodPost
		q = nil
	}
	if verb == "list" || verb == "read" {
		q.Set(apiv1.InboxViewQuery, apiv1.InboxViewThread)
	}
	if verb == "list" {
		q.Set("limit", strconv.Itoa(limit))
		if cursor != "" {
			q.Set("continue", cursor)
		}
		if sent {
			q.Set(apiv1.InboxBoxQuery, apiv1.InboxBoxSent)
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
	case "send", "ask", "reply":
		var result apiv1.InboxResult
		status, callErr := a.callWith(ctx, &one, method, path, q, body, &result, false)
		if status == http.StatusConflict {
			if verb == "reply" {
				return fail(ExitFailed, "inbox reply conflicts: the key holds a different intent, or the question already has a reply; nothing was replaced. Read the question to see its reply")
			}
			return fail(ExitFailed, "inbox intent conflicts with the existing key; no replacement key was generated")
		}
		if callErr != nil {
			return inboxPublishError(verb, status, callErr)
		}
		if (status != http.StatusCreated || result.Outcome != "Created") && (status != http.StatusOK || result.Outcome != "Confirmed") {
			return fail(ExitUnconfirmed, "inbox %s returned an unconfirmed receipt; retry only the identical intent with the same key", verb)
		}
		if !inboxReceiptMatches(result.Message, request) {
			return fail(ExitUnconfirmed, "inbox %s returned an unconfirmed identity; retry only the identical intent with the same key", verb)
		}
		return a.printJSON(result)
	case "list":
		var result apiv1.InboxList
		status, callErr := a.callWith(ctx, &one, method, path, q, nil, &result, false)
		if status == http.StatusBadRequest && sent {
			return fail(ExitFailed, "inbox list --sent was refused (400): this API does not list sent messages (not enabled, or older)")
		}
		if status == http.StatusBadRequest {
			// An API with questions off refuses the thread view; the plain list is read-only and safe to ask again.
			q.Del(apiv1.InboxViewQuery)
			status, callErr = a.callWith(ctx, &one, method, path, q, nil, &result, false)
		}
		if callErr != nil {
			return inboxSafeError(status, callErr)
		}
		if result.Items == nil || len(result.Items) > limit || len(result.Continue) > 4096 {
			return fail(ExitFailed, "inbox list returned more than the requested limit")
		}
		if sent && result.Box != apiv1.InboxBoxSent {
			// An older API ignores box=sent and would answer with the caller's inbox.
			return fail(ExitFailed, "the inbox API does not support --sent; nothing was listed")
		}
		if result.View != apiv1.InboxViewThread {
			_, _ = fmt.Fprintln(a.env.Stderr, "inbox: this API returned no thread view; kinds and question states are not shown")
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
		if status == http.StatusBadRequest {
			// An API with questions off refuses the thread view; the plain read is safe to ask again.
			q.Del(apiv1.InboxViewQuery)
			status, callErr = a.callWith(ctx, &one, method, path, q, nil, &result, false)
		}
		if callErr != nil {
			return inboxSafeError(status, callErr)
		}
		if result.Project != project || result.Name != pos[0] || result.UID == "" || len(result.Text) == 0 || len(result.Text) > 16<<10 || !utf8.ValidString(result.Text) {
			return fail(ExitFailed, "inbox read returned an unconfirmed message")
		}
		if cmd.c.output == outputJSON {
			return a.printJSON(result)
		}
		// Stdout stays the exact text; the correlation goes to stderr.
		if line := inboxThreadLine(result.InboxMetadata); line != "" {
			_, _ = fmt.Fprintln(a.env.Stderr, line)
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

// inboxReceiptMatches checks that a receipt is for this exact intent. A
// question's resolved recipient and a reply's recipient come from the server,
// so only the fields the caller chose are compared.
func inboxReceiptMatches(m apiv1.InboxMetadata, req apiv1.PublishInboxRequest) bool {
	if m.Project != req.Project || m.Key != req.Key || !inboxMessagePattern.MatchString(m.Name) || m.UID == "" {
		return false
	}
	switch req.Kind {
	case apiv1.InboxKindQuestion:
		return m.Kind == apiv1.InboxKindQuestion && m.InReplyTo == nil && inboxUIDPattern.MatchString(m.Recipient.UID) &&
			(req.ToParent || (m.Recipient.Name == req.Recipient.Name && (req.Recipient.UID == "" || m.Recipient.UID == req.Recipient.UID)))
	case apiv1.InboxKindReply:
		return m.Kind == apiv1.InboxKindReply && m.InReplyTo != nil && m.InReplyTo.Name == req.InReplyTo.Name && inboxUIDPattern.MatchString(m.InReplyTo.UID) && inboxUIDPattern.MatchString(m.Recipient.UID)
	}
	return m.Recipient == req.Recipient
}

// inboxThreadLine summarizes a question or reply for a text read. It prints
// only values of the expected shape; anything else reads as unconfirmed.
func inboxThreadLine(m apiv1.InboxMetadata) string {
	if m.Kind != apiv1.InboxKindQuestion && m.Kind != apiv1.InboxKindReply {
		return ""
	}
	t := m.Thread
	if t == nil || !inboxThreadStates[t.State] || !inboxMessagePattern.MatchString(t.Question.Name) ||
		(t.Reply != nil && !inboxMessagePattern.MatchString(t.Reply.Name)) || (t.WaitingOn != nil && !inboxDNSName(t.WaitingOn.Name)) {
		return "inbox: " + m.Kind + " state could not be confirmed"
	}
	line := "inbox: " + m.Kind + " " + t.Question.Name + " state=" + t.State
	if t.Reply != nil {
		line += " reply=" + t.Reply.Name
	}
	if t.WaitingOn != nil {
		line += " waiting-on=" + t.WaitingOn.Name
	}
	return line
}

var inboxThreadStates = map[string]bool{apiv1.InboxStateUnanswered: true, apiv1.InboxStateAnswered: true, apiv1.InboxStateAcknowledged: true,
	apiv1.InboxStateStale: true, apiv1.InboxStateRecipientEnded: true, apiv1.InboxStateUnknown: true}

// inboxPublishError makes the ask and reply refusals specific without echoing
// the API's body.
func inboxPublishError(verb string, status int, err error) error {
	var ee *exitError
	code := ExitFailed
	if errors.As(err, &ee) {
		code = ee.code
	}
	switch {
	case verb != "send" && status == http.StatusBadRequest:
		return fail(code, "inbox %s was refused (400): this API does not accept questions and replies (not enabled, or older), or the arguments are invalid", verb)
	case verb == "ask" && status == http.StatusUnprocessableEntity:
		return fail(code, "inbox ask was refused (422): this Session has no recorded parent Session; name the recipient with --to")
	case verb == "ask" && status == http.StatusNotFound:
		return fail(code, "inbox ask was refused (404): the recipient Session or recorded parent was not found in this project, or this Session's project scope was refused")
	case verb == "reply" && status == http.StatusUnprocessableEntity:
		return fail(code, "inbox reply was refused (422): only a question takes a reply; this message is a reply or a plain message")
	case verb == "reply" && status == http.StatusNotFound:
		return fail(code, "inbox reply was refused (404): no question with that name in this project is addressed to this Session, its asker has ended, or this Session's project scope was refused")
	}
	return inboxMutationError(status, err)
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
