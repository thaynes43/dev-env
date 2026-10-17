package agentrun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// grant list and show are the first slice of plan 07 step 7. They read the
// existing /v1/grants routes. Request, use, release, break-glass and the
// in-pod MCP server are not in this slice.

var grantPhases = []string{
	apiv1.GrantPending, apiv1.GrantActive, apiv1.GrantDenied,
	apiv1.GrantExpired, apiv1.GrantReleased, apiv1.GrantFailed,
}

func (a *app) grant(ctx context.Context, args []string) error {
	if len(args) == 0 {
		_, _ = io.WriteString(a.env.Stderr, commandHelp["grant"])
		return &exitError{code: ExitUsage}
	}
	switch args[0] {
	case "list":
		return a.grantList(ctx, args[1:])
	case "show":
		return a.grantShow(ctx, args[1:])
	case "help", "-h", "--help":
		_, _ = io.WriteString(a.env.Stdout, commandHelp["grant"])
		return nil
	}
	_, _ = io.WriteString(a.env.Stderr, commandHelp["grant"])
	return usageError("unknown grant command %q: it is list or show", args[0])
}

func (a *app) grantList(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	var session, phase string
	var mine bool
	cmd.fs.StringVar(&session, "session", "", "")
	cmd.fs.StringVar(&phase, "phase", "", "")
	cmd.fs.BoolVar(&mine, "mine", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("grant list takes --session, --phase and --mine, and no arguments; agent-run help grant")
	}
	if phase != "" && !grantPhaseKnown(phase) {
		return usageError("grant list: phase is one of %s, not %q", strings.Join(grantPhases, ", "), phase)
	}
	q := url.Values{}
	if session != "" {
		q.Set(apiv1.FilterSession, session)
	}
	if phase != "" {
		q.Set(apiv1.FilterPhase, phase)
	}
	if mine {
		q.Set(apiv1.FilterMine, "true")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var out apiv1.GrantList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantsPath, q, nil, &out); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	if len(out.Items) == 0 {
		a.outf("no grants\n")
		return nil
	}
	tw := a.table()
	_, _ = fmt.Fprintln(tw, "NAME\tPHASE\tTYPE\tSESSION\tTTL\tEXPIRES")
	for _, g := range out.Items {
		expires := "-"
		if g.ExpiresAt != nil && !g.ExpiresAt.IsZero() {
			expires = g.ExpiresAt.UTC().Format("2006-01-02 15:04")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", g.Name, g.Phase, g.Type, firstOf(g.Session, "-"), g.TTL, expires)
	}
	return tw.Flush()
}

func (a *app) grantShow(ctx context.Context, args []string) error {
	cmd := newCommand("grant", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || !validGrantCLIName(pos[0]) {
		return usageError("grant show takes one grant-<id> name; agent-run help grant")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var out apiv1.Grant
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.GrantPath(pos[0]), nil, nil, &out); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	expires := "-"
	if out.ExpiresAt != nil && !out.ExpiresAt.IsZero() {
		expires = out.ExpiresAt.UTC().Format(timeRFC3339)
	}
	a.outf("name: %s\nphase: %s\ntype: %s\nsession: %s\nttl: %s\nexpires: %s\nreason: %s\n",
		out.Name, out.Phase, out.Type, out.Session, out.TTL, expires, out.Reason)
	return nil
}

func grantPhaseKnown(phase string) bool {
	for _, known := range grantPhases {
		if phase == known {
			return true
		}
	}
	return false
}

func validGrantCLIName(name string) bool {
	if !strings.HasPrefix(name, "grant-") || len(name) <= len("grant-") || len(name) > 63 || strings.HasSuffix(name, "-") || strings.Contains(name, "/") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

const timeRFC3339 = "2006-01-02T15:04:05Z"
