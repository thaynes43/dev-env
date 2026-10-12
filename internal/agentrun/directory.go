package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var directoryProjectName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// directory is an explicit read-only command boundary: unknown subcommands
// never reach the repository/create fallback or connection/authentication.
func (a *app) directory(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return a.help([]string{"directory"})
	}
	if len(args) == 0 || args[0] != "list" {
		return usageError("directory takes the list subcommand; agent-run help directory lists its flags")
	}
	cmd := newCommand("directory", outputJSON)
	cmd.c.output = outputJSON
	var project, continuation string
	var limit int64
	var once, asJSON bool
	cmd.fs.StringVar(&project, "project", "", "")
	cmd.fs.Int64Var(&limit, "limit", 20, "")
	cmd.fs.StringVar(&continuation, "continue", "", "")
	cmd.fs.BoolVar(&once, "once", false, "")
	cmd.fs.BoolVar(&asJSON, "json", false, "")
	pos, err := cmd.parse(a, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 0 || !once || len(project) > 63 || !directoryProjectName.MatchString(project) || limit < 1 || limit > 20 || len(continuation) > 4096 || !utf8.ValidString(continuation) || strings.ContainsAny(continuation, "\x00\r\n") {
		return usageError("directory list requires --once, a valid --project, --limit 1..20 and a bounded --continue cursor, with no positional arguments")
	}
	if asJSON {
		cmd.c.output = outputJSON
	}
	q := url.Values{"project": {project}, "limit": {strconv.FormatInt(limit, 10)}}
	if continuation != "" {
		// The cursor is opaque. Only the server may confirm its query scope;
		// it is always an escaped query value, never a path or URL.
		q.Set("continue", continuation)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	// Refuse redirects and disable callWith's retries for this command alone.
	// Ordinary command connection/authentication and retry behaviour stay intact.
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	// Fence the whole body before decoding, as show --once does (history-05).
	one.rejectOversizeResponse = true
	var raw json.RawMessage
	status, err := a.callWith(ctx, &one, http.MethodGet, apiv1.SessionDirectoryPath, q, nil, &raw, false)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fail(ExitFailed, "directory list requires an HTTP 200 metadata page")
	}
	if !utf8.Valid(raw) {
		return fail(ExitFailed, "directory list returned invalid UTF-8 metadata")
	}
	if !strictWireJSON(raw, reflect.TypeFor[apiv1.SessionDirectoryList]()) {
		return fail(ExitFailed, "directory list returned an unconfirmed metadata page")
	}
	var page apiv1.SessionDirectoryList
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&page) != nil || d.Decode(new(any)) != io.EOF || page.Items == nil || page.ResourceVersion == "" || int64(len(page.Items)) > limit || len(page.Continue) > 4096 {
		return fail(ExitFailed, "directory list returned an unconfirmed metadata page")
	}
	for _, item := range page.Items {
		if item.Original.Project != project || item.Name == "" || item.UID == "" || item.ResourceVersion == "" {
			return fail(ExitFailed, "directory list returned an unconfirmed metadata identity or project")
		}
	}
	// Print the validated reply, preserving present-null fields that decoding
	// into optional typed pointers would otherwise omit when re-encoded.
	// A caller needing completeness must refuse a nonempty continuation.
	return a.printJSON(raw)
}
