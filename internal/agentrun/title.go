package agentrun

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
)

// olderOperatorTitle is the fixed sentence when a create that carried a title
// is refused because the operator does not know the field. The create is not
// sent again without the title.
const olderOperatorTitle = "this operator does not accept a session title; agent-run did not retry the create without the title"

// normalizeTitle trims the ends and checks the title with sessiontitle.
// An empty result is no title. The reason is empty when the value can be sent.
func normalizeTitle(raw string) (string, string) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", ""
	}
	return title, sessiontitle.Validate(title)
}

func unknownTitleField(msg string) bool {
	return strings.Contains(msg, `unknown field "title"`) || strings.Contains(msg, `unknown field \"title\"`)
}

// title records or clears a session's role text (DESIGN-065 rules 8.1 and 8.8).
func (a *app) title(ctx context.Context, args []string) error {
	cmd := newCommand("title", outputJSON)
	clear := cmd.fs.Bool("clear", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if (*clear && len(pos) != 1) || (!*clear && len(pos) != 2) {
		return usageError("title takes a session and the new text, or --clear; agent-run help title")
	}
	name := pos[0]
	text := ""
	if !*clear {
		var reason string
		text, reason = normalizeTitle(pos[1])
		if text == "" {
			return usageError("title text is empty; use --clear to remove a title")
		}
		if reason != "" {
			return fail(ExitUsage, "%s", sessiontitle.Explain(reason))
		}
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var cur apiv1.Session
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionPath(url.PathEscape(name)), nil, nil, &cur); err != nil {
		if missingSession(err) {
			return fail(ExitNotFound, "no such session")
		}
		return err
	}
	body := struct {
		Title  string `json:"title"`
		Expect string `json:"expect"`
	}{Title: text, Expect: cur.Title}
	var next apiv1.Session
	status, err := a.callOnce(ctx, c, http.MethodPut, apiv1.SessionTitlePath(url.PathEscape(name)), body, &next)
	if status == http.StatusMethodNotAllowed || (status == http.StatusNotFound && !missingSession(err)) {
		return fail(ExitFailed, "this operator has no session title route; nothing was changed")
	}
	if missingSession(err) {
		return fail(ExitNotFound, "no such session")
	}
	if err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(next)
	}
	shown := next.Name
	if next.Title != "" {
		shown = sessiontitle.Compose(next.Model, next.Title)
	}
	a.outf("%s\nIt is typed when the session is idle with an empty input line.\n", shown)
	return nil
}

func missingSession(err error) bool {
	var ee *exitError
	return errors.As(err, &ee) && ee.code == ExitNotFound && strings.Contains(ee.msg, "session ") && strings.Contains(ee.msg, "not found") && !strings.Contains(ee.msg, "no route")
}

// titleRefusal replaces an older operator's unknown-field refusal. Any other
// error is returned unchanged, and the request is not rebuilt without the title.
func titleRefusal(err error, title string) error {
	if err == nil || title == "" || !unknownTitleField(err.Error()) {
		return err
	}
	return fail(ExitFailed, "%s", olderOperatorTitle)
}
