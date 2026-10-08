package agentrun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// rescue list and rescue restore (D-67). The bundles never leave the cluster:
// list shows what the shelf pod holds, and restore is a create whose new
// session fetches one rescue into refs/rescued/ on its first boot.

func (a *app) rescue(ctx context.Context, args []string) error {
	if len(args) == 0 {
		_, _ = io.WriteString(a.env.Stderr, commandHelp["rescue"])
		return &exitError{code: ExitUsage}
	}
	switch args[0] {
	case "list":
		return a.rescueList(ctx, args[1:])
	case "restore":
		return a.createSession(ctx, args[1:], true)
	case "help", "-h", "--help":
		_, _ = io.WriteString(a.env.Stdout, commandHelp["rescue"])
		return nil
	}
	_, _ = io.WriteString(a.env.Stderr, commandHelp["rescue"])
	return usageError("unknown rescue command %q: it is list or restore", args[0])
}

func (a *app) rescueList(ctx context.Context, args []string) error {
	cmd := newCommand("rescue", outputJSON)
	var session string
	cmd.fs.StringVar(&session, "session", "", "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("rescue list takes no arguments, got %q; --session <name> narrows it to one session", pos)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	out, err := a.fetchRescues(ctx, c, session)
	if err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(out)
	}
	if len(out.Rescues) == 0 {
		a.outf("no rescues\n")
	} else {
		tw := a.table()
		_, _ = fmt.Fprintln(tw, "ID\tCREATED\tREPOS\tSIZE\tSTATE\tKEPT")
		for _, r := range out.Rescues {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.CreatedAt.UTC().Format("2006-01-02 15:04"),
				rescueRepos(r), humanBytes(r.Bytes), rescueState(r), rescueKept(r))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	for _, p := range out.Unrecognized {
		a.outf("unrecognized (never pruned): %s\n", p)
	}
	return nil
}

func (a *app) fetchRescues(ctx context.Context, c *conn, session string) (apiv1.RescueList, error) {
	var q url.Values
	if session != "" {
		q = url.Values{"session": {session}}
	}
	var out apiv1.RescueList
	_, err := a.call(ctx, c, http.MethodGet, apiv1.RescuesPath, q, nil, &out)
	return out, err
}

func rescueRepos(r apiv1.Rescue) string {
	names := repoNames(r)
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

func repoNames(r apiv1.Rescue) []string {
	names := make([]string, 0, len(r.Repos))
	for _, rr := range r.Repos {
		names = append(names, rr.Repo)
	}
	return names
}

// rescueState is complete, incomplete (finished, but a bundle is missing),
// unfinished (no manifest) or the manifest's error.
func rescueState(r apiv1.Rescue) string {
	switch {
	case r.Error != "":
		msg := []rune(r.Error)
		if len(msg) > 40 {
			msg = msg[:40]
		}
		return "error: " + string(msg)
	case r.Complete:
		return "complete"
	case r.Finished:
		return "incomplete"
	}
	return "unfinished"
}

func rescueKept(r apiv1.Rescue) string {
	switch {
	case r.SessionExists:
		return "session exists"
	case r.PruneAfter != nil:
		return "until " + r.PruneAfter.UTC().Format("2006-01-02")
	}
	return "-"
}

// humanBytes is a size in bytes as 512B, 12K or 3.4M: one decimal below 10.
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	v := float64(n)
	for _, unit := range []string{"K", "M", "G", "T"} {
		v /= 1024
		if v < 1024 || unit == "T" {
			if v < 10 {
				return fmt.Sprintf("%.1f%s", v, unit)
			}
			return fmt.Sprintf("%.0f%s", v, unit)
		}
	}
	return ""
}

// resolveRestore finds the rescue and the repo a restore will fetch. Only a
// complete rescue can be restored. Without --repo, the rescue must hold
// exactly one clone.
func (a *app) resolveRestore(ctx context.Context, c *conn, id, session, repo string) (apiv1.RescueRepo, error) {
	list, err := a.fetchRescues(ctx, c, session)
	if err != nil {
		return apiv1.RescueRepo{}, err
	}
	i := slices.IndexFunc(list.Rescues, func(r apiv1.Rescue) bool { return r.ID == id })
	if i < 0 {
		return apiv1.RescueRepo{}, fail(ExitNotFound, "no rescue %s; agent-run rescue list shows them", id)
	}
	r := list.Rescues[i]
	if !r.Complete {
		return apiv1.RescueRepo{}, fail(ExitFailed, "rescue %s is %s: only a complete rescue can be restored", id, rescueState(r))
	}
	names := repoNames(r)
	switch {
	case repo != "":
		for _, rr := range r.Repos {
			if rr.Repo == repo {
				return rr, nil
			}
		}
		return apiv1.RescueRepo{}, fail(ExitFailed, "rescue %s holds no bundle for %s; it holds %s", id, repo, firstOf(strings.Join(names, ", "), "none"))
	case len(r.Repos) == 1:
		return r.Repos[0], nil
	case len(r.Repos) == 0:
		return apiv1.RescueRepo{}, fail(ExitFailed, "rescue %s holds no bundles to restore", id)
	}
	return apiv1.RescueRepo{}, usageError("rescue %s holds several repos (%s): say which with --repo <name>", id, strings.Join(names, ", "))
}

// restoreHints are the lines printed after a restore's create: where the
// rescued work lands and what may be used from it (D-67, D-10).
func restoreHints(id, session string, rr apiv1.RescueRepo, hadBase bool) []string {
	hints := []string{fmt.Sprintf("Its clone fetches rescue %s into refs/rescued/ on its first boot.", id)}
	for _, ref := range rr.Refs {
		if rest, ok := strings.CutPrefix(ref, "refs/heads/rescue/"); ok {
			hints = append(hints, fmt.Sprintf("  uncommitted work: refs/rescued/heads/rescue/%s (look and copy from it; never push a rescue branch, D-10)", rest))
		}
	}
	branch := "refs/heads/agent/" + session
	if !hadBase && slices.Contains(rr.Refs, branch) {
		hints = append(hints, "  the worktree starts at refs/rescued/heads/agent/"+session)
	}
	return hints
}
