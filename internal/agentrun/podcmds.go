package agentrun

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// log and msg (DESIGN-001 3.5, 6.8, D-16, D-65) map one to one onto the API's
// routes: the operator runs agentd in the session's pod.

func (a *app) log(ctx context.Context, args []string) error {
	cmd := newCommand("log", outputJSON)
	var tail int
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
	c, err := a.connect(cmd.c.conn)
	if err != nil {
		return err
	}
	var l apiv1.SessionLog
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionLogPath(url.PathEscape(pos[0])), url.Values{"tail": {strconv.Itoa(tail)}}, nil, &l); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(l)
	}
	_, _ = io.WriteString(a.env.Stdout, l.Text)
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
	c, err := a.connect(cmd.c.conn)
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
