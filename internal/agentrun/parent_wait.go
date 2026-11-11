package agentrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func (a *app) parentAnswerWait(ctx context.Context, verb string, args []string) error {
	cmd := newCommand("parent", outputJSON)
	var req apiv1.AnswerParentRequest
	var duration time.Duration
	if verb == "answer" {
		cmd.fs.StringVar(&req.Key, "key", "", "")
	} else {
		cmd.fs.DurationVar(&duration, "for", 15*time.Minute, "")
	}
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !inboxMessagePattern.MatchString(pos[0]) {
		return usageError("parent %s requires one note name; see agent-run help parent", verb)
	}
	if verb == "answer" {
		if !inboxKeyPattern.MatchString(req.Key) {
			return usageError("parent answer requires a valid --key")
		}
		data, err := io.ReadAll(io.LimitReader(a.env.Stdin, (16<<10)+1))
		if err != nil || len(data) == 0 || len(data) > 16<<10 || !utf8.Valid(data) {
			return usageError("parent answer requires 1..16384 UTF-8 bytes on stdin")
		}
		req.Text = string(data)
	} else if duration < time.Second || duration > apiv1.ParentNoteMaxWaitSeconds*time.Second || duration%time.Second != 0 {
		return usageError("parent wait requires --for in whole seconds from 1s to 15m")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return parentNoteError(0, err, verb)
	}
	one := parentNoteClient(c, duration)
	path := apiv1.ParentNotesPath + "/" + pos[0] + "/" + verb
	if verb == "answer" {
		var result apiv1.InboxResult
		status, err := a.callWith(ctx, one, http.MethodPost, path, nil, req, &result, false)
		if err != nil {
			return parentNoteError(status, err, verb)
		}
		if ((status != http.StatusCreated || result.Outcome != "Created") && (status != http.StatusOK || result.Outcome != "Confirmed")) ||
			!validParentReplyMetadata(result.Message, pos[0]) || result.Message.Key != req.Key {
			return fail(ExitUnconfirmed, "parent answer returned an unconfirmed receipt; retry only the identical intent with the same key")
		}
		return a.printJSON(result)
	}
	var result apiv1.ParentNoteWaitResult
	query := url.Values{"seconds": {strconv.FormatInt(int64(duration/time.Second), 10)}}
	status, err := a.callWith(ctx, one, http.MethodGet, path, query, nil, &result, false)
	if err != nil {
		return parentNoteError(status, err, verb)
	}
	if status != http.StatusOK || !validParentWait(result, pos[0]) {
		return fail(ExitFailed, "parent wait returned an unconfirmed answer")
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(result)
	}
	if result.State == apiv1.ParentNoteWaiting {
		_, err = fmt.Fprintln(a.env.Stdout, apiv1.ParentNoteWaiting)
	} else {
		_, err = fmt.Fprint(a.env.Stdout, result.Reply.Text)
	}
	return err
}

func (a *app) parentWaitAny(ctx context.Context, args []string) error {
	cmd := newCommand("parent", outputJSON)
	var duration time.Duration
	var after string
	cmd.fs.DurationVar(&duration, "for", 15*time.Minute, "")
	cmd.fs.StringVar(&after, "after", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError("parent wait-any takes no note name; see agent-run help parent")
	}
	if duration < 0 || duration%time.Second != 0 {
		return usageError("parent wait-any requires --for in whole seconds from 0")
	}
	if after != "" && !apiv1.ValidResourceVersion(after) {
		return usageError("parent wait-any --after must be a resource version cursor")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return a.parentWaitAnyFailed(cmd.c.output, "", parentNoteError(0, err, "wait-any"))
	}
	// confirmed is a cursor a successful call in this command returned.
	// The caller's own --after is not one until a call confirms it.
	confirmed := ""
	var result apiv1.ParentNoteWaitAnyResult
	call := func(chunk time.Duration, cursor string) error {
		one := parentNoteClient(c, chunk)
		query := url.Values{"seconds": {strconv.FormatInt(int64(chunk/time.Second), 10)}}
		if cursor != "" {
			query.Set("after", cursor)
		}
		var got apiv1.ParentNoteWaitAnyResult
		status, err := a.callWith(ctx, one, http.MethodGet, apiv1.ParentNotesWaitAnyPath, query, nil, &got, false)
		if err != nil {
			return a.parentWaitAnyFailed(cmd.c.output, confirmed, parentNoteError(status, err, "wait-any"))
		}
		if status != http.StatusOK || !validParentWaitAny(got) {
			return a.parentWaitAnyFailed(cmd.c.output, confirmed, fail(ExitFailed, "parent wait-any returned an unconfirmed answer"))
		}
		result = got
		confirmed = got.Cursor
		return nil
	}
	if duration == 0 {
		// One call, no chunk and no watch on the server. seconds=0.
		if err := call(0, after); err != nil {
			return err
		}
	} else {
		cursor := after
		for remaining := duration; remaining > 0; {
			chunk := remaining
			maxChunk := time.Duration(apiv1.ParentNoteMaxWaitSeconds) * time.Second
			if chunk > maxChunk {
				chunk = maxChunk
			}
			remaining -= chunk
			if err := call(chunk, cursor); err != nil {
				return err
			}
			cursor = result.Cursor
			// A note or a resync ends the command. A longer wait calls again only
			// while the answer is still waiting, using the cursor it was given.
			if result.State != apiv1.ParentNoteWaiting {
				break
			}
		}
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(result)
	}
	return a.printParentWaitAny(result)
}

// parentWaitAnyAgain is the one sentence printed after a confirmed cursor when
// a later call fails. It names no endpoint, token or command line.
const parentWaitAnyAgain = "run the same command again with --after and that cursor"

// parentWaitAnyFailed keeps a cursor the server already confirmed when a later
// call fails. Text prints that cursor and parentWaitAnyAgain. JSON carries the
// cursor only. The exit code is the failure's own. When no call in this
// command confirmed a cursor, the message says to read the list first.
func (a *app) parentWaitAnyFailed(output, confirmed string, err error) error {
	if confirmed != "" {
		if output == outputJSON {
			printed := struct {
				Cursor string `json:"cursor"`
			}{Cursor: confirmed}
			if perr := a.printJSON(printed); perr != nil {
				return err
			}
		} else if _, perr := fmt.Fprintf(a.env.Stdout, "%s\n%s\n", confirmed, parentWaitAnyAgain); perr != nil {
			return err
		}
		return err
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return fail(ee.code, "%s; read agent-run parent list before starting again", ee.msg)
	}
	return err
}

// parentWaitAnyResyncLine is what a parent reads at the moment it must
// reconcile. It names every page of the list, because parent list returns one
// page at a time, and it names the cursor to continue from.
const parentWaitAnyResyncLine = "read every page of agent-run parent list (pass --continue until none is returned), then run agent-run parent wait-any --after with this cursor"

func (a *app) printParentWaitAny(result apiv1.ParentNoteWaitAnyResult) error {
	switch result.State {
	case apiv1.ParentNoteArrived:
		_, err := fmt.Fprintf(a.env.Stdout, "%s %s %s\n%s\n", result.Name, result.Child, result.Kind, result.Cursor)
		return err
	case apiv1.ParentNoteResync:
		_, err := fmt.Fprintf(a.env.Stdout, "%s\n%s\n%s\n", apiv1.ParentNoteResync, result.Cursor, parentWaitAnyResyncLine)
		return err
	default:
		_, err := fmt.Fprintf(a.env.Stdout, "%s\n%s\n", apiv1.ParentNoteWaiting, result.Cursor)
		return err
	}
}

func validParentWaitAny(result apiv1.ParentNoteWaitAnyResult) bool {
	if !apiv1.ValidResourceVersion(result.Cursor) {
		return false
	}
	switch result.State {
	case apiv1.ParentNoteWaiting, apiv1.ParentNoteResync:
		return result.Name == "" && result.Child == "" && result.Kind == "" && result.CreatedAt.IsZero()
	case apiv1.ParentNoteArrived:
		return inboxMessagePattern.MatchString(result.Name) && inboxDNSName(result.Child) && parentNoteKind(result.Kind) && !result.CreatedAt.IsZero()
	default:
		return false
	}
}

func validParentWait(result apiv1.ParentNoteWaitResult, name string) bool {
	if result.State == apiv1.ParentNoteWaiting {
		return result.Reply == nil
	}
	return result.State == apiv1.ParentNoteAnswered && result.Reply != nil &&
		validParentReplyMetadata(result.Reply.InboxMetadata, name) &&
		len(result.Reply.Text) > 0 && len(result.Reply.Text) <= 16<<10 && utf8.ValidString(result.Reply.Text)
}
