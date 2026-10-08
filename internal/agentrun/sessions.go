package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func (a *app) list(ctx context.Context, args []string) error {
	cmd := newCommand("list", outputName, outputJSON)
	var repo, state string
	var mine bool
	cmd.fs.StringVar(&repo, "repo", "", "")
	cmd.fs.StringVar(&state, "state", "", "")
	cmd.fs.BoolVar(&mine, "mine", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("list takes no arguments, got %q; agent-run show <name> shows one session", pos)
	}
	q := url.Values{}
	if repo != "" {
		q.Set(apiv1.FilterRepo, repo)
	}
	if state != "" {
		q.Set(apiv1.FilterState, state)
	}
	if mine {
		q.Set(apiv1.FilterMine, "true")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var out apiv1.SessionList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionsPath, q, nil, &out); err != nil {
		return err
	}
	switch cmd.c.output {
	case outputJSON:
		return a.printJSON(out)
	case outputName:
		for _, s := range out.Sessions {
			a.outf("%s\n", s.Name)
		}
		return nil
	}
	if len(out.Sessions) == 0 {
		a.outf("No sessions.\n")
		return nil
	}
	tw := a.table()
	_, _ = fmt.Fprintln(tw, "NAME\tPHASE\tAGENT\tNODE\tAGE\tPARENT\tNOTE")
	now := a.env.Now()
	for _, s := range out.Sessions {
		agent := "-"
		if s.AgentStatus != nil && s.AgentStatus.State != "" {
			agent = s.AgentStatus.State
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Phase, agent, dash(s.Node),
			age(now, s.CreatedAt), dash(s.Parent), note(s))
	}
	return tw.Flush()
}

// note is a list row's last column: what is unusual about the session.
func note(s apiv1.Session) string {
	var parts []string
	if s.Reaping {
		parts = append(parts, "reaping")
	}
	if s.Outdated {
		parts = append(parts, "outdated")
	}
	switch s.Phase {
	case "Pending":
		if s.Pending != "" {
			parts = append(parts, s.Pending)
		}
	case "Failed":
		if m := podReadyMessage(s); m != "" {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, "; ")
}

func (a *app) show(ctx context.Context, args []string) error {
	cmd := newCommand("show", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("show takes one session name; agent-run list shows them")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var s apiv1.Session
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.SessionPath(url.PathEscape(pos[0])), nil, nil, &s); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(s)
	}
	a.printSession(s)
	return nil
}

// printSession is show's text form: one fact per line.
func (a *app) printSession(s apiv1.Session) {
	now := a.env.Now()
	tw := tabwriter.NewWriter(a.env.Stdout, 0, 0, 1, ' ', 0)
	row := func(k, format string, args ...any) {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", k, fmt.Sprintf(format, args...))
	}
	row("name", "%s", s.Name)
	phase := s.Phase
	if s.Node != "" {
		phase += " on " + s.Node
	}
	if s.Reaping {
		phase += ", reaping: rescue, then suspend and archive"
	}
	row("phase", "%s", phase)
	if s.SuspendedBy != "" && s.OperatingMode == "Suspended" {
		row("suspended by", "%s", s.SuspendedBy)
	}
	if s.Pending != "" && s.Phase == "Pending" {
		row("pending", "%s", s.Pending)
	}
	row("repo", "%s from %s", s.Repo, firstOf(s.Base, "origin/main"))
	row("agent", "%s %s, %s, %s mode", s.Agent, s.Model, effortText(s.Effort), s.Mode)
	row("size", "%s", firstOf(s.Size, "M"))
	if s.Profile != "" {
		row("profile", "%s", s.Profile)
	}
	if l := s.Limits; l != nil {
		row("limits", "timeout %s, max turns %s", dash(l.Timeout), countOrNone(l.MaxTurns))
	}
	if l := s.Lifecycle; l != nil {
		row("timers", "idle suspend after %s, archive after %s", firstOf(l.IdleSuspendAfter, "the templates'"), firstOf(l.ArchiveAfter, "the templates'"))
	}
	row("parent", "%s", dash(s.Parent))
	row("created", "%s (%s ago)", s.CreatedAt.UTC().Format(time.RFC3339), age(now, s.CreatedAt))
	if s.Revision != "" {
		rev := s.Revision
		if s.Outdated {
			rev += " (outdated: the current templates differ)"
		}
		row("revision", "%s", rev)
	}
	if st := s.AgentStatus; st != nil {
		row("agent state", "%s", firstOf(st.State, "-"))
		if st.Branch != "" {
			row("branch", "%s at %s", st.Branch, dash(st.Head))
		}
		if st.LastActivity != nil {
			row("last activity", "%s ago", age(now, *st.LastActivity))
		}
		if st.LastHeartbeat != nil {
			row("last heartbeat", "%s ago", age(now, *st.LastHeartbeat))
		}
		if t := st.Task; t != nil {
			res := fmt.Sprintf("exit %d at %s, %d turns", t.ExitCode, t.FinishedAt.UTC().Format(time.RFC3339), t.NumTurns)
			if t.TimedOut {
				res += ", timed out"
			}
			if t.IsError {
				res += ", error " + firstOf(t.Subtype, "reported")
			}
			row("task", "%s", res)
		}
		if st.Message != "" {
			row("message", "%s", st.Message)
		}
		for _, p := range st.Problems {
			row("problem", "%s", p)
		}
	}
	if u := s.Usage; u != nil && (u.CostUSD != "" || u.InputTokens != 0 || u.OutputTokens != 0) {
		row("usage", "$%s, %d tokens in, %d out", firstOf(u.CostUSD, "0"), u.InputTokens, u.OutputTokens)
	}
	for _, c := range s.Conditions {
		row("condition", "%s=%s %s: %s", c.Type, c.Status, c.Reason, c.Message)
	}
	_ = tw.Flush()
	if s.Prompt != "" {
		a.outf("prompt:\n")
		for _, line := range strings.Split(strings.TrimRight(s.Prompt, "\n"), "\n") {
			a.outf("  %s\n", line)
		}
	}
}

func (a *app) reap(ctx context.Context, args []string) error {
	cmd := newCommand("reap", outputJSON)
	var force bool
	cmd.fs.BoolVar(&force, "force", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError("reap takes the names of the sessions to reap; agent-run list shows them")
	}
	if force {
		a.errf("--force changes nothing in v2: every reap rescues the session's work first, and nothing skips that (D-45)")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var (
		reaped []apiv1.Session
		first  error
	)
	for _, name := range pos {
		var s apiv1.Session
		if _, err := a.call(ctx, c, http.MethodDelete, apiv1.SessionPath(url.PathEscape(name)), nil, nil, &s); err != nil {
			if first == nil {
				first = err
			}
			if len(pos) > 1 {
				a.errf("%s: %v", name, err)
			}
			continue
		}
		reaped = append(reaped, s)
		if cmd.c.output == outputText {
			a.outf("reaping %s: the operator rescues its work to a bundle, then stops its pod and archives its volume\n", s.Name)
		}
	}
	if cmd.c.output == outputJSON {
		if err := a.printJSON(apiv1.SessionList{Sessions: append([]apiv1.Session{}, reaped...)}); err != nil {
			return err
		}
	}
	if first != nil && len(pos) > 1 {
		// Each failure is already printed; keep the first one's exit code.
		var ee *exitError
		if errors.As(first, &ee) {
			return &exitError{code: ee.code}
		}
		return &exitError{code: ExitFailed}
	}
	return first
}

func (a *app) fleet(ctx context.Context, args []string) error {
	cmd := newCommand("fleet", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("fleet takes no arguments, got %q", pos)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var f apiv1.Fleet
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.FleetPath, nil, nil, &f); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(f)
	}
	rev := f.Revision
	if rev == "" {
		rev = "unknown: " + firstOf(f.RevisionError, "the API gave no reason")
	}
	a.outf("revision: %s\n", rev)
	a.outf("phases:   %s\n", phaseCounts(f.Phases))
	if len(f.Nodes) > 0 {
		nodes := make([]string, 0, len(f.Nodes))
		for _, n := range f.Nodes {
			nodes = append(nodes, fmt.Sprintf("%s %d", n.Name, n.Sessions))
		}
		a.outf("nodes:    %s\n", strings.Join(nodes, ", "))
	}
	if len(f.Outdated) > 0 {
		a.outf("outdated: %s\n", strings.Join(f.Outdated, ", "))
	}
	if len(f.Sessions) == 0 {
		a.outf("\nNo session holds or waits for a pod.\n")
		return nil
	}
	a.outf("\n")
	now := a.env.Now()
	tw := a.table()
	_, _ = fmt.Fprintln(tw, "NAME\tPHASE\tAGENT\tNODE\tHEARTBEAT\tAGE\tNOTE")
	for _, s := range f.Sessions {
		beat := "-"
		if s.LastHeartbeat != nil {
			beat = age(now, *s.LastHeartbeat) + " ago"
		}
		var parts []string
		if s.Reaping {
			parts = append(parts, "reaping")
		}
		if s.Outdated {
			parts = append(parts, "outdated")
		}
		if s.Phase == "Pending" && s.Pending != "" {
			parts = append(parts, s.Pending)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Phase, dash(s.AgentState), dash(s.Node),
			beat, age(now, s.CreatedAt), strings.Join(parts, "; "))
	}
	return tw.Flush()
}

// phaseCounts is "Running 3, Pending 1", in the lifecycle's order.
func phaseCounts(m map[string]int) string {
	if len(m) == 0 {
		return "no sessions"
	}
	order := map[string]int{"Pending": 0, "Running": 1, "Idle": 2, "Draining": 3, "Suspended": 4, "Archived": 5, "Failed": 6}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, iok := order[keys[i]]
		oj, jok := order[keys[j]]
		switch {
		case iok && jok:
			return oi < oj
		case iok != jok:
			return iok
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// table is a column writer whose Flush prints to stdout with no spaces at the
// ends of lines, which an empty last column would leave.
func (a *app) table() *tableWriter {
	t := &tableWriter{out: a.env.Stdout}
	t.Writer = tabwriter.NewWriter(&t.buf, 0, 0, 2, ' ', 0)
	return t
}

type tableWriter struct {
	*tabwriter.Writer
	buf bytes.Buffer
	out io.Writer
}

func (t *tableWriter) Flush() error {
	if err := t.Writer.Flush(); err != nil {
		return err
	}
	for _, line := range strings.SplitAfter(t.buf.String(), "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimRight(line, " \n")
		if strings.HasSuffix(line, "\n") {
			trimmed += "\n"
		}
		if _, err := io.WriteString(t.out, trimmed); err != nil {
			return err
		}
	}
	t.buf.Reset()
	return nil
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.env.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// age is a duration since t, short: 42s, 5m, 3h, 2d.
func age(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func countOrNone(n int32) string {
	if n == 0 {
		return "none"
	}
	return fmt.Sprint(n)
}
