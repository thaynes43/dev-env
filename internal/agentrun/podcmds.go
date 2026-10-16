package agentrun

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// log and msg (DESIGN-001 3.5, 6.8, D-16, D-65) map one to one onto the API's
// routes: the operator runs agentd in the session's pod.

func (a *app) log(ctx context.Context, args []string) error {
	cmd := newCommand("log", outputJSON)
	var tail int
	var once bool
	cmd.fs.BoolVar(&once, "once", false, "")
	cmd.fs.IntVar(&tail, "tail", 200, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("log takes one session name; agent-run list shows them")
	}
	if tail < 1 || tail > 5000 {
		return usageError("--tail is a number of lines from 1 to 5000, not %d", tail)
	}
	if once && cmd.c.output != outputJSON {
		return usageError("log --once requires -o json")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	if once {
		hc := *c.http
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		one := *c
		one.http, one.rejectOversizeResponse = &hc, true
		var raw json.RawMessage
		status, err := a.callWith(ctx, &one, http.MethodGet, apiv1.SessionLogPath(url.PathEscape(pos[0])), url.Values{"tail": {strconv.Itoa(tail)}}, nil, &raw, false)
		if err != nil {
			return err
		}
		var log apiv1.SessionLog
		if status != http.StatusOK || !strictWireJSON(raw, reflect.TypeFor[apiv1.SessionLog]()) || json.Unmarshal(raw, &log) != nil || log.Session != pos[0] || log.Truncated {
			return fail(ExitFailed, "unconfirmed one-shot log")
		}
		return a.printJSON(raw)
	}
	var l apiv1.SessionLog
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionLogPath(url.PathEscape(pos[0])), url.Values{"tail": {strconv.Itoa(tail)}}, nil, &l); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(l)
	}
	_, _ = io.WriteString(a.env.Stdout, l.Text)
	if l.Truncated {
		a.errf("the log's last %d lines are more than the API sends; this is the newest part of them", tail)
	}
	return nil
}

func (a *app) msg(ctx context.Context, args []string) error {
	cmd := newCommand("msg", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usageError("msg takes a session name and the text: agent-run msg <name> \"<text>\" (- reads the text from stdin)")
	}
	text := strings.Join(pos[1:], " ")
	if text == "-" {
		b, err := io.ReadAll(io.LimitReader(a.env.Stdin, apiv1.MaxMessageBytes+1))
		if err != nil {
			return fail(ExitFailed, "read the message from stdin: %v", err)
		}
		text = string(b)
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return usageError("the message is empty")
	case len(text) > apiv1.MaxMessageBytes:
		return usageError("the message is %d bytes, more than %d", len(text), apiv1.MaxMessageBytes)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var r apiv1.MessageResult
	if _, err := a.callOnce(ctx, c, http.MethodPost, apiv1.SessionMessagesPath(url.PathEscape(pos[0])), apiv1.MessageRequest{Text: text}, &r); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(r)
	}
	a.outf("delivered to %s, from %s\n", r.Session, r.From)
	return nil
}
