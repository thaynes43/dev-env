package agentrun

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// declare-activity v2 (DESIGN-001 6.9, D-17, D-66): v1's command and flags, as a
// client of /v1/activities. agent-run answers to the name declare-activity (the
// image links it), and to `agent-run declare-activity ...`.
//
//	declare-activity start "<what you are doing>" --scope <a,b,c> [--ttl 45m]
//	declare-activity end <id>
//	declare-activity list
func (a *app) declareActivity(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "start":
		return a.activityStart(ctx, args[1:])
	case "end":
		return a.activityEnd(ctx, args[1:])
	case "list":
		return a.activityList(ctx, args[1:])
	case "prune":
		a.outf("prune is automatic in v2: the operator deletes a declaration when it expires\n")
		return nil
	case "help", "-h", "--help":
		return a.help([]string{"declare-activity"})
	}
	return usageError("declare-activity takes start, end or list, not %q; declare-activity help says more", args[0])
}

func (a *app) activityStart(ctx context.Context, args []string) error {
	cmd := newCommand("declare-activity", outputJSON)
	var scope, ttl string
	cmd.fs.StringVar(&scope, "scope", "", "")
	cmd.fs.StringVar(&ttl, "ttl", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	what := strings.TrimSpace(strings.Join(pos, " "))
	if what == "" {
		return usageError("say what you are doing: declare-activity start \"<what>\" --scope <a,b,c>")
	}
	if strings.TrimSpace(scope) == "" {
		return usageError("--scope is required: the namespaces, apps or nodes your work can disturb, or cluster (at most 2h)")
	}
	req := apiv1.DeclareActivityRequest{Description: what, TTL: ttl}
	for _, tok := range strings.Split(scope, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			req.Scope = append(req.Scope, tok)
		}
	}
	c, err := a.connect(cmd.c.conn)
	if err != nil {
		return err
	}
	var act apiv1.Activity
	if _, err := a.call(ctx, c, http.MethodPost, apiv1.ActivitiesPath, nil, req, &act); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(act)
	}
	a.outf("declared %s: %q, scope %s, until %s\n", act.Name, act.Description, strings.Join(act.Scope, ","), act.ExpiresAt.UTC().Format(time.RFC3339))
	a.outf("END IT EARLY when you finish: declare-activity end %s\n", act.Name)
	return nil
}

func (a *app) activityEnd(ctx context.Context, args []string) error {
	cmd := newCommand("declare-activity", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("declare-activity end takes one id; declare-activity list shows them")
	}
	c, err := a.connect(cmd.c.conn)
	if err != nil {
		return err
	}
	var act apiv1.Activity
	if _, err := a.call(ctx, c, http.MethodDelete, apiv1.ActivityPath(url.PathEscape(pos[0])), nil, nil, &act); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(act)
	}
	a.outf("ended %s\n", act.Name)
	return nil
}

func (a *app) activityList(ctx context.Context, args []string) error {
	cmd := newCommand("declare-activity", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("declare-activity list takes no arguments")
	}
	c, err := a.connect(cmd.c.conn)
	if err != nil {
		return err
	}
	var l apiv1.ActivityList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.ActivitiesPath, nil, nil, &l); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(l)
	}
	if len(l.Activities) == 0 {
		a.outf("No activities declared.\n")
		return nil
	}
	now := a.env.Now()
	tw := tabwriter.NewWriter(a.env.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tLEFT\tSCOPE\tBY\tWHAT")
	for _, act := range l.Activities {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", act.Name, act.ExpiresAt.Sub(now).Round(time.Minute), strings.Join(act.Scope, ","), act.DeclaredBy, act.Description)
	}
	return tw.Flush()
}
