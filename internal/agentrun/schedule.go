package agentrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// agent-run schedule (DESIGN-054 S5, OpenProject #397): create, list, show,
// edit and cancel scheduled agent work through /v1/scheduled-work, and a
// human's approve and retry. The operator serves the routes (S5b,
// internal/apiserver/scheduled_work.go) behind its default-off
// --enable-scheduled-work-api flag; with the flag off every one answers 404.
//
//	schedule create "<title>" --due <local time> --tz <zone> --not-after <instant>
//	      --mode headless|interactive --template <name> --repo <repo> (--prompt <text> | --prompt-file <path>)
//	      [--repeat ..] [--count N | --until <instant>] [--notify pushover:<ref>]... [--calendar calendar:<ref>] [--key <k>]
//	schedule list [--all]
//	schedule show <name>
//	schedule edit <name> [the create flags] [--once] [--no-notify] [--no-calendar]
//	schedule cancel <name>
//	schedule approve <name> [--spec-hash <hash>] [--at <instant>] [--yes]
//	schedule retry <name> [--at <instant>] [--yes]

func init() { commandHelp["schedule"] = scheduleHelp }

const scheduleHelp = `Usage: agent-run schedule create "<title>" --due <YYYY-MM-DDTHH:MM> --tz <zone> --not-after <time>
           --mode headless|interactive --template <name> --repo <repo>
           (--prompt <text> | --prompt-file <path>) [flags]
       agent-run schedule list [--all] [-o json]
       agent-run schedule show <name> [-o json]
       agent-run schedule edit <name> [flags]
       agent-run schedule cancel <name>
       agent-run schedule approve <name> [--at <time>] [--spec-hash <hash> --yes]
       agent-run schedule retry <name> [--at <time>] [--spec-hash <hash> --yes]

Scheduled work starts agent work at a set time: a headless task session that
reports a result, or an interactive remote session that Tom joins from his
phone. The operator records each occurrence and what became of it.

Scheduling grants no authority. At each due time the operator checks again that
the work may run, on one of three grounds: Tom owns the item and made its last
edit; the owner session made the last edit and is still running; or Tom approved
this exact spec. The dispatch template must admit that ground too. If none
holds, the occurrence is parked, and Tom is paged if it has a notify target. A
client or coordinator owner always needs Tom's approval. Nothing runs until the
operator is started with --enable-scheduled-work and has the dispatch template
the item names, and every command here fails with 404 until the operator serves
the scheduled-work API.

Flags of create and edit:
  --due <YYYY-MM-DDTHH:MM>   local time of the first occurrence, read in --tz
  --tz <zone>                IANA zone, such as America/New_York
  --not-after <time>         an RFC 3339 instant; nothing runs after it. Required
                             on create, and at most 180 days after creation
  --mode <mode>              headless (a task session) or interactive (a remote
                             session). Required on create
  --template <name>          the dispatch template. It fixes the agent, model,
                             effort, size, profile, allowed repos and time limit
  --repo <repo>              the repository; the template must allow it
  --prompt <text>            the work order, at most 8192 bytes
  --prompt-file <path>       read the work order from a file; - reads stdin
  --repeat <rule>            daily, weekly[:MO,TH] or monthly[:15]; a monthly day
                             a month lacks falls on that month's last day
  --count <n> | --until <t>  end the series (--until is an RFC 3339 instant)
  --once                     (edit) make a repeating item one-shot
  --notify pushover:<ref>    page this target; repeat the flag for up to 8.
                             Interactive work needs at least one
  --no-notify                (edit) remove every notify target
  --calendar calendar:<ref>  also show the item in a calendar; the operator's
                             record stays the source of truth
  --no-calendar              (edit) remove the calendar entry
  --key <key>                (create) a create retried with the same key makes
                             no second item; agent-run picks a key if you give none

A headless item with no --notify sends no pages; schedule show prints its
results. An occurrence found more than its grace period late (by default 15
minutes headless, 2 hours interactive) is skipped, not run late.

Who may do what. Only the owner or a human may edit an item. An edit by anyone
but the owner leaves the item needing Tom's approval of the new spec, and any
edit voids an earlier approval. The owner, a human or a coordinator may cancel.
A cancel stops later occurrences, not a session already running (agent-run
reap <session> stops that). list shows your own items; --all, for a human or a
coordinator, shows everyone's.

approve and retry are for a human (Tom) only; the API refuses them from a
session, client or coordinator. The y/N and --yes guard against a slip; they
prove nothing about who you are. The API accepts approve and retry only from
Tom's own token.
  approve  prints the exact spec and its hash, asks y/N on a terminal, and
           sends that hash; the API refuses it if the spec changed in the
           meantime. Without a terminal, read the hash with schedule show and
           give it: --spec-hash <hash> --yes (--yes needs --spec-hash). The
           hash binds the approval to the spec shown; it does not record that
           anyone read it. approve retries a parked occurrence only when --at
           names it; otherwise parked occurrences stay parked.
  retry    runs one parked occurrence again now, late on purpose, after the
           same y/N, or --at <time> --spec-hash <hash> --yes. With --yes,
           retry also needs --at: the hash pins the spec, not the occurrence.
           It grants no authority: the occurrence is checked again as at its
           due time. On a terminal, --at picks one of several parked
           occurrences.
Neither approve --at nor retry runs an occurrence claimed with an earlier spec;
a plain approve still approves the current spec for later occurrences. An
occurrence can be approved or retried at most 3 times. Without --yes and
without a terminal, approve and retry refuse and send nothing.
`

func (a *app) schedule(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("schedule takes create, list, show, edit, cancel, approve or retry; agent-run help schedule says more")
	}
	switch args[0] {
	case "create":
		return a.scheduleCreate(ctx, args[1:])
	case "list":
		return a.scheduleList(ctx, args[1:])
	case "show":
		return a.scheduleShow(ctx, args[1:])
	case "edit":
		return a.scheduleEdit(ctx, args[1:])
	case "cancel":
		return a.scheduleCancel(ctx, args[1:])
	case "approve":
		return a.scheduleDecide(ctx, "approve", args[1:])
	case "retry":
		return a.scheduleDecide(ctx, "retry", args[1:])
	case "help", "-h", "--help":
		return a.help([]string{"schedule"})
	}
	return usageError("schedule takes create, list, show, edit, cancel, approve or retry, not %q", args[0])
}

// scheduleDueLayout is the local wall time of --due, DESIGN-032's.
const scheduleDueLayout = "2006-01-02T15:04"

var (
	scheduleNotifyRef   = regexp.MustCompile(`^pushover:[a-z0-9][a-z0-9-]{0,62}$`)
	scheduleCalendarRef = regexp.MustCompile(`^calendar:[a-z0-9][a-z0-9-]{0,62}$`)
	scheduleSpecHash    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	scheduleWeekdays    = []string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}
)

// scheduleStdinIsTerminal reports whether stdin is a terminal a person can
// answer y/N on. A pipe, a file and a character device such as /dev/null are
// not. The terminal guards against a slip, such as a script that pipes "y";
// it proves nothing about who is at it: the API identifies the human from the
// token. A variable, so a test can stand in a terminal.
var scheduleStdinIsTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && isTerminal(f)
}

// scheduleFlags are the flags of create and edit.
type scheduleFlags struct {
	title, due, tz, mode, template, repo, prompt, promptFile string
	repeat, until, notAfter, calendar, key                   string
	count                                                    int
	notify                                                   stringList
	once, noNotify, noCalendar                               bool
}

func (sf *scheduleFlags) bind(cmd *command, edit bool) {
	fs := cmd.fs
	fs.StringVar(&sf.due, "due", "", "")
	fs.StringVar(&sf.tz, "tz", "", "")
	fs.StringVar(&sf.mode, "mode", "", "")
	fs.StringVar(&sf.template, "template", "", "")
	fs.StringVar(&sf.repo, "repo", "", "")
	fs.StringVar(&sf.prompt, "prompt", "", "")
	fs.StringVar(&sf.promptFile, "prompt-file", "", "")
	fs.StringVar(&sf.repeat, "repeat", "", "")
	fs.IntVar(&sf.count, "count", 0, "")
	fs.StringVar(&sf.until, "until", "", "")
	fs.StringVar(&sf.notAfter, "not-after", "", "")
	fs.Var(&sf.notify, "notify", "")
	fs.StringVar(&sf.calendar, "calendar", "", "")
	if edit {
		fs.StringVar(&sf.title, "title", "", "")
		fs.BoolVar(&sf.once, "once", false, "")
		fs.BoolVar(&sf.noNotify, "no-notify", false, "")
		fs.BoolVar(&sf.noCalendar, "no-calendar", false, "")
	} else {
		fs.StringVar(&sf.key, "key", "", "")
	}
}

func checkScheduleDue(due string) error {
	if _, err := time.Parse(scheduleDueLayout, due); err != nil {
		return usageError("--due %q is not a local time such as 2026-12-03T09:00 (YYYY-MM-DDTHH:MM, read in --tz)", due)
	}
	return nil
}

func checkScheduleZone(tz string) error {
	if tz == "" || tz == "Local" {
		return usageError("--tz %q is not an IANA zone name such as America/New_York", tz)
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return usageError("--tz %q is not an IANA zone name such as America/New_York", tz)
	}
	return nil
}

func checkScheduleMode(mode string) error {
	if mode != apiv1.ScheduledWorkHeadless && mode != apiv1.ScheduledWorkInteractive {
		return usageError("--mode is headless (a task session) or interactive (a remote session Tom joins), not %q", mode)
	}
	return nil
}

func checkScheduleNotify(targets []string) error {
	if len(targets) > apiv1.MaxScheduledWorkNotify {
		return usageError("--notify takes at most %d targets, not %d", apiv1.MaxScheduledWorkNotify, len(targets))
	}
	for i, t := range targets {
		switch {
		case slices.Contains(targets[:i], t):
			return usageError("--notify %s is given twice", t)
		case strings.HasPrefix(t, "calendar:"):
			return usageError("--notify %q: a calendar is not a page; give it with --calendar", t)
		case !scheduleNotifyRef.MatchString(t):
			return usageError("--notify takes pushover:<ref>, such as pushover:default, not %q", t)
		}
	}
	return nil
}

func checkScheduleCalendar(ref string) error {
	if !scheduleCalendarRef.MatchString(ref) {
		return usageError("--calendar takes calendar:<ref>, such as calendar:haynesnetwork, not %q", ref)
	}
	return nil
}

// scheduleInstant parses an RFC 3339 instant given to flag.
func scheduleInstant(flagName, s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, usageError("--%s %q is not an RFC 3339 instant such as 2027-02-01T00:00:00Z", flagName, s)
	}
	return t.UTC(), nil
}

// scheduleNotAfter parses --not-after, which must lie ahead.
func (a *app) scheduleNotAfter(s string) (time.Time, error) {
	t, err := scheduleInstant("not-after", s)
	if err != nil {
		return t, err
	}
	if !t.After(a.env.Now()) {
		return t, usageError("--not-after %s has passed; nothing would ever run", t.Format(time.RFC3339))
	}
	return t, nil
}

// schedulePrompt reads the work order from --prompt or --prompt-file.
func (a *app) schedulePrompt(sf *scheduleFlags, set map[string]bool) (string, error) {
	if set["prompt"] && set["prompt-file"] {
		return "", usageError("give the work order with --prompt or --prompt-file, not both")
	}
	prompt := sf.prompt
	if set["prompt-file"] {
		var err error
		if prompt, err = a.readPrompt(sf.promptFile); err != nil {
			return "", err
		}
	}
	switch {
	case strings.TrimSpace(prompt) == "":
		return "", usageError("the work order is empty: say what the session should do with --prompt or --prompt-file")
	case len(prompt) > apiv1.MaxScheduledWorkPromptBytes:
		return "", usageError("the work order is %d bytes, more than %d", len(prompt), apiv1.MaxScheduledWorkPromptBytes)
	case !utf8.ValidString(prompt):
		return "", usageError("the work order is not UTF-8 text")
	}
	return prompt, nil
}

// scheduleRecurrence builds the recurrence with remind's parser, then checks
// the weekdays it passes through.
func scheduleRecurrence(sf *scheduleFlags) (*apiv1.ReminderRecurrence, error) {
	rf := remindFlags{repeat: sf.repeat, count: sf.count, until: sf.until}
	rec, err := rf.recurrence()
	if err != nil || rec == nil {
		return rec, err
	}
	for _, d := range rec.ByDay {
		if !slices.Contains(scheduleWeekdays, d) {
			return nil, usageError("--repeat weekly:<days> takes MO, TU, WE, TH, FR, SA or SU, not %q", d)
		}
	}
	return rec, nil
}

func visited(cmd *command) map[string]bool {
	set := map[string]bool{}
	cmd.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func (a *app) scheduleCreate(ctx context.Context, args []string) error {
	cmd := newCommand("schedule", outputJSON)
	var sf scheduleFlags
	sf.bind(cmd, false)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	set := visited(cmd)
	title := strings.TrimSpace(strings.Join(pos, " "))
	switch {
	case title == "":
		return usageError(`schedule create takes a title: schedule create "<title>" --due <YYYY-MM-DDTHH:MM> --tz <zone> ...`)
	case sf.due == "" || sf.tz == "":
		return usageError("--due and --tz are required: the due time is a local time read in a zone")
	case sf.notAfter == "":
		return usageError("--not-after is required: every scheduled item ends, at most 180 days after creation")
	case sf.mode == "":
		return usageError("--mode is required: headless (a task session) or interactive (a remote session Tom joins)")
	case sf.template == "" || sf.repo == "":
		return usageError("--template and --repo are required: the template fixes how the session runs, and must allow the repo")
	}
	if err := checkScheduleDue(sf.due); err != nil {
		return err
	}
	if err := checkScheduleZone(sf.tz); err != nil {
		return err
	}
	if err := checkScheduleMode(sf.mode); err != nil {
		return err
	}
	notAfter, err := a.scheduleNotAfter(sf.notAfter)
	if err != nil {
		return err
	}
	rec, err := scheduleRecurrence(&sf)
	if err != nil {
		return err
	}
	prompt, err := a.schedulePrompt(&sf, set)
	if err != nil {
		return err
	}
	notify := []string(sf.notify)
	if err := checkScheduleNotify(notify); err != nil {
		return err
	}
	if sf.mode == apiv1.ScheduledWorkInteractive && len(notify) == 0 {
		return usageError("interactive work needs --notify pushover:<ref>: Tom is paged the session's link")
	}
	if sf.calendar != "" {
		if err := checkScheduleCalendar(sf.calendar); err != nil {
			return err
		}
	}
	key := sf.key
	switch {
	case len(key) > apiv1.MaxScheduledWorkKeyBytes:
		return usageError("--key is %d bytes, more than %d", len(key), apiv1.MaxScheduledWorkKeyBytes)
	case key == "":
		// A create is retried on a lost answer; the key makes the retry
		// return the first item rather than make a second.
		key = a.env.NewKey()
	}
	req := apiv1.CreateScheduledWorkRequest{
		Key: key, Title: title, Due: sf.due, TimeZone: sf.tz, Recurrence: rec, NotAfter: notAfter, Mode: sf.mode,
		Work:   apiv1.ScheduledWorkOrder{Template: sf.template, Repo: sf.repo, Prompt: prompt},
		Notify: notify, Calendar: sf.calendar,
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var w apiv1.ScheduledWork
	retried := false
	status, err := a.callWithRetryObserver(ctx, c, http.MethodPost, apiv1.ScheduledWorkPath, nil, req, &w, true, func(int, error) { retried = true })
	if err != nil {
		err = scheduleRefusal("create", "", status, err)
		if retried && status == http.StatusConflict {
			err = withNote(err, "agent-run sent this create again after an attempt it could not confirm; the first attempt may have made the item, so check agent-run schedule list before you create it again")
		}
		return withNote(err, fmt.Sprintf("the create used key %s: run the same command with --key %s to retry it without making a second item", shown(key, false), shown(key, false)))
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(w)
	}
	a.outf("created %s: \"%s\", %s, %s; nothing runs after %s\n", shown(w.Name, false), shown(w.Spec.Title, false), shown(w.Spec.Mode, false),
		describeWorkSchedule(w.Spec), w.Spec.NotAfter.UTC().Format(time.RFC3339))
	if w.Message != "" {
		a.outf("note: %s\n", shown(w.Message, false))
	}
	a.outf("key %s: a create retried with --key %s makes no second item\n", shown(key, false), shown(key, false))
	a.outf("show it with: agent-run schedule show %s\n", shown(w.Name, false))
	a.outf("cancel it with: agent-run schedule cancel %s\n", shown(w.Name, false))
	return nil
}

// describeWorkSchedule is remind's schedule description, for the same fields,
// made safe for a terminal.
func describeWorkSchedule(s apiv1.ScheduledWorkSpec) string {
	return shown(describeSchedule(apiv1.Reminder{Due: s.Due, TimeZone: s.TimeZone, Recurrence: s.Recurrence}), false)
}

func (a *app) scheduleList(ctx context.Context, args []string) error {
	cmd := newCommand("schedule", outputJSON)
	var all bool
	cmd.fs.BoolVar(&all, "all", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("schedule list takes no arguments")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	q := url.Values{}
	if all {
		q.Set("all", "true")
	}
	var l apiv1.ScheduledWorkList
	if status, err := a.call(ctx, c, http.MethodGet, apiv1.ScheduledWorkPath, q, nil, &l); err != nil {
		return scheduleRefusal("list", "", status, err)
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(l)
	}
	if len(l.Items) == 0 {
		a.outf("No scheduled work.\n")
		return nil
	}
	tw := tabwriter.NewWriter(a.env.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tMODE\tSTATE\tNEXT (UTC)\tSCHEDULE\tTITLE")
	for _, w := range l.Items {
		next := "-"
		if w.NextDue != nil {
			next = w.NextDue.UTC().Format("2006-01-02 15:04")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", shown(w.Name, false), shown(w.Spec.Mode, false), shown(w.State, false), next, describeWorkSchedule(w.Spec), shown(w.Spec.Title, false))
	}
	return tw.Flush()
}

func (a *app) scheduleShow(ctx context.Context, args []string) error {
	cmd := newCommand("schedule", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("schedule show takes one name; schedule list shows them")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var w apiv1.ScheduledWork
	if status, err := a.call(ctx, c, http.MethodGet, apiv1.ScheduledWorkItemPath(url.PathEscape(pos[0])), nil, nil, &w); err != nil {
		return scheduleRefusal("show", pos[0], status, err)
	}
	if w.Name != pos[0] {
		return fail(ExitFailed, "the API answered for \"%s\", not \"%s\"", shown(w.Name, false), shown(pos[0], false))
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(w)
	}
	out := a.env.Stdout
	pf(out, "%s  %s\n", shown(w.Name, false), shown(w.State, false))
	printScheduleSpec(out, w)
	printScheduleApproval(out, w)
	if w.NextDue != nil {
		pf(out, "  next due:   %s\n", w.NextDue.UTC().Format(time.RFC3339))
	}
	pf(out, "  counts:     %s\n", describeScheduleCounts(w.Counts))
	if w.Message != "" {
		pf(out, "  note:       %s\n", shown(w.Message, false))
	}
	if w.Warning != "" {
		pf(out, "  WARNING:    %s\n", shown(w.Warning, false))
	}
	occ := slices.Clone(w.Occurrences)
	slices.SortStableFunc(occ, func(x, y apiv1.ScheduledWorkOccurrence) int { return y.At.Compare(x.At) })
	if len(occ) == 0 {
		pf(out, "  occurrences: none recorded yet\n")
	} else {
		pf(out, "  occurrences, newest first:\n")
	}
	for _, o := range occ {
		printScheduleOccurrence(out, o)
	}
	return nil
}

// printScheduleSpec prints every field of the spec (a test pins the field
// list), so the hash an approval sends binds it to exactly the spec shown. That
// is not evidence that anyone read it, nor of who approved: the API identifies
// the human from the token.
func printScheduleSpec(out io.Writer, w apiv1.ScheduledWork) {
	s := w.Spec
	pf(out, "  title:      %s\n", shown(s.Title, false))
	pf(out, "  mode:       %s\n", shown(s.Mode, false))
	pf(out, "  schedule:   %s\n", describeWorkSchedule(s))
	pf(out, "  not after:  %s\n", s.NotAfter.UTC().Format(time.RFC3339))
	pf(out, "  template:   %s\n", shown(s.Work.Template, false))
	pf(out, "  repo:       %s\n", shown(s.Work.Repo, false))
	notify := "none (no pages)"
	if len(s.Notify) > 0 {
		notify = shown(strings.Join(s.Notify, ", "), false)
	}
	pf(out, "  notify:     %s\n", notify)
	if s.Calendar != "" {
		pf(out, "  calendar:   %s\n", shown(s.Calendar, false))
	}
	pf(out, "  owner:      %s\n", shown(s.Owner, false))
	pf(out, "  editor:     %s\n", shown(s.Editor, false))
	if s.Cancelled {
		pf(out, "  cancelled:  yes\n")
	}
	pf(out, "  spec hash:  %s\n", shown(w.SpecHash, false))
	pf(out, "  prompt:     %d bytes, between the bars\n", len(s.Work.Prompt))
	for _, line := range strings.Split(shown(s.Work.Prompt, true), "\n") {
		pf(out, "    | %s\n", line)
	}
	pf(out, "  end of prompt (%d bytes)\n", len(s.Work.Prompt))
}

func printScheduleApproval(out io.Writer, w apiv1.ScheduledWork) {
	ap := w.Approval
	switch {
	case ap == nil:
		pf(out, "  approval:   none\n")
	case ap.SpecHash == w.SpecHash:
		pf(out, "  approval:   by %s at %s, of this spec\n", shown(ap.By, false), ap.At.UTC().Format(time.RFC3339))
	default:
		pf(out, "  approval:   VOID: by %s at %s, of an earlier spec (hash %s)\n", shown(ap.By, false), ap.At.UTC().Format(time.RFC3339), shown(ap.SpecHash, false))
	}
}

func describeScheduleCounts(c apiv1.ScheduledWorkCounts) string {
	var parts []string
	for _, p := range []struct {
		n    int
		name string
	}{{c.Succeeded, "succeeded"}, {c.Failed, "failed"}, {c.Ended, "ended"}, {c.Parked, "parked"}, {c.Skipped, "skipped"}, {c.Ambiguous, "ambiguous"}, {c.Cancelled, "cancelled"}} {
		if p.n != 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.name))
		}
	}
	if len(parts) == 0 {
		return "none yet"
	}
	return strings.Join(parts, ", ")
}

func printScheduleOccurrence(out io.Writer, o apiv1.ScheduledWorkOccurrence) {
	pf(out, "    %s  %s\n", o.At.UTC().Format(time.RFC3339), shown(o.State, false))
	if o.Reason != "" {
		pf(out, "      reason:     %s\n", shown(o.Reason, false))
	}
	if o.Missed > 1 {
		pf(out, "      missed:     %d due times\n", o.Missed)
	}
	if o.Attempt > 0 || o.RetriedAt != nil {
		retried := ""
		if o.RetriedAt != nil {
			retried = ", last retried " + o.RetriedAt.UTC().Format(time.RFC3339)
		}
		pf(out, "      attempt:    %d of at most %d%s\n", o.Attempt, apiv1.MaxScheduledWorkAttempt, retried)
	}
	if o.Session != "" {
		pf(out, "      session:    %s\n", shown(o.Session, false))
	}
	if o.ClaimedAt != nil {
		pf(out, "      claimed:    %s\n", o.ClaimedAt.UTC().Format(time.RFC3339))
	}
	if o.SpecHash != "" {
		pf(out, "      spec hash:  %s (claimed with)\n", shown(o.SpecHash, false))
	}
	if o.Delivery != "" {
		pf(out, "      delivery:   %s\n", shown(o.Delivery, false))
	}
	if e := o.Execution; e != nil {
		reason := ""
		if e.Reason != "" {
			reason = " (" + shown(e.Reason, false) + ")"
		}
		pf(out, "      execution:  %s%s\n", shown(e.State, false), reason)
	}
	if r := o.Reported; r != nil {
		pf(out, "      reported:   %s (the agent's own claim, not checked)\n", shown(r.Summary, false))
		for _, u := range r.PRURLs {
			pf(out, "      PR:         %s\n", shown(u, false))
		}
		for _, h := range r.HeadSHAs {
			pf(out, "      head:       %s\n", shown(h, false))
		}
	}
	for _, p := range o.Pages {
		state := p.State
		if state == "" {
			state = "pending"
		}
		ref := ""
		if p.Reminder != "" {
			ref = " (" + shown(p.Reminder, false) + ")"
		}
		pf(out, "      page:       %s, attempt %d: %s%s\n", shown(p.Event, false), p.Attempt, shown(state, false), ref)
	}
}

func (a *app) scheduleEdit(ctx context.Context, args []string) error {
	cmd := newCommand("schedule", outputJSON)
	var sf scheduleFlags
	sf.bind(cmd, true)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("schedule edit takes one name; schedule list shows them")
	}
	set := visited(cmd)
	var req apiv1.EditScheduledWorkRequest
	if set["title"] {
		req.Title = &sf.title
	}
	if set["due"] {
		if err := checkScheduleDue(sf.due); err != nil {
			return err
		}
		req.Due = &sf.due
	}
	if set["tz"] {
		if err := checkScheduleZone(sf.tz); err != nil {
			return err
		}
		req.TimeZone = &sf.tz
	}
	if set["mode"] {
		if err := checkScheduleMode(sf.mode); err != nil {
			return err
		}
		req.Mode = &sf.mode
	}
	var work apiv1.EditScheduledWorkOrder
	if set["template"] {
		work.Template = &sf.template
	}
	if set["repo"] {
		work.Repo = &sf.repo
	}
	if set["prompt"] || set["prompt-file"] {
		prompt, err := a.schedulePrompt(&sf, set)
		if err != nil {
			return err
		}
		work.Prompt = &prompt
	}
	if work != (apiv1.EditScheduledWorkOrder{}) {
		req.Work = &work
	}
	switch {
	case sf.once && (set["repeat"] || set["count"] || set["until"]):
		return usageError("--once makes an item one-shot; it cannot be combined with --repeat, --count or --until")
	case sf.once:
		req.ClearRecurrence = true
	case set["repeat"] || set["count"] || set["until"]:
		rec, err := scheduleRecurrence(&sf)
		if err != nil {
			return err
		}
		req.Recurrence = rec
	}
	if set["not-after"] {
		t, err := a.scheduleNotAfter(sf.notAfter)
		if err != nil {
			return err
		}
		req.NotAfter = &t
	}
	switch {
	case sf.noNotify && set["notify"]:
		return usageError("--no-notify removes every notify target; it cannot be combined with --notify")
	case sf.noNotify:
		none := []string{}
		req.Notify = &none
	case set["notify"]:
		notify := []string(sf.notify)
		if err := checkScheduleNotify(notify); err != nil {
			return err
		}
		req.Notify = &notify
	}
	if sf.noNotify && set["mode"] && sf.mode == apiv1.ScheduledWorkInteractive {
		return usageError("interactive work needs --notify pushover:<ref>: Tom is paged the session's link")
	}
	switch {
	case sf.noCalendar && set["calendar"]:
		return usageError("--no-calendar removes the calendar entry; it cannot be combined with --calendar")
	case sf.noCalendar:
		none := ""
		req.Calendar = &none
	case set["calendar"]:
		if err := checkScheduleCalendar(sf.calendar); err != nil {
			return err
		}
		req.Calendar = &sf.calendar
	}
	if req.Title == nil && req.Due == nil && req.TimeZone == nil && req.Mode == nil && req.Work == nil && req.Recurrence == nil &&
		!req.ClearRecurrence && req.NotAfter == nil && req.Notify == nil && req.Calendar == nil {
		return usageError("schedule edit changes something: --title, --due, --tz, --mode, --template, --repo, --prompt, --repeat, --once, --not-after, --notify or --calendar")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var w apiv1.ScheduledWork
	// Sent once: the edit carries no precondition, so a repeat after a lost
	// answer could overwrite a change made in between.
	if status, err := a.callOnce(ctx, c, http.MethodPatch, apiv1.ScheduledWorkItemPath(url.PathEscape(pos[0])), req, &w); err != nil {
		return scheduleRefusal("edit", pos[0], status, err)
	}
	if err := answerNames(w, pos[0], "edit"); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(w)
	}
	a.outf("edited %s: \"%s\", %s, %s; spec hash now %s\n", shown(w.Name, false), shown(w.Spec.Title, false), shown(w.Spec.Mode, false), describeWorkSchedule(w.Spec), shown(w.SpecHash, false))
	if ap := w.Approval; ap != nil && ap.SpecHash != w.SpecHash {
		a.outf("the approval by %s was of the earlier spec and no longer applies: it needs approving again (agent-run schedule approve %s)\n", shown(ap.By, false), shown(w.Name, false))
	}
	if w.Message != "" {
		a.outf("note: %s\n", shown(w.Message, false))
	}
	return nil
}

func (a *app) scheduleCancel(ctx context.Context, args []string) error {
	cmd := newCommand("schedule", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("schedule cancel takes one name; schedule list shows them")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var w apiv1.ScheduledWork
	if status, err := a.call(ctx, c, http.MethodPost, apiv1.ScheduledWorkCancelPath(url.PathEscape(pos[0])), nil, nil, &w); err != nil {
		return scheduleRefusal("cancel", pos[0], status, err)
	}
	if err := answerNames(w, pos[0], "cancel"); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(w)
	}
	a.outf("cancelled %s: no later occurrence runs\n", shown(w.Name, false))
	for _, o := range w.Occurrences {
		if o.Session != "" && scheduleInFlight(o.State) {
			a.outf("session %s (occurrence %s, %s) keeps running; a cancel does not stop it: agent-run reap %s stops it\n",
				shown(o.Session, false), o.At.UTC().Format(time.RFC3339), shown(o.State, false), shown(o.Session, false))
		}
	}
	if w.Warning != "" {
		a.outf("WARNING: %s\n", shown(w.Warning, false))
	}
	return nil
}

// scheduleInFlight reports whether an occurrence state can have a session
// still at work.
func scheduleInFlight(state string) bool {
	switch state {
	case apiv1.ScheduledOccurrenceClaimed, apiv1.ScheduledOccurrenceDispatched, apiv1.ScheduledOccurrenceDelivered,
		apiv1.ScheduledOccurrenceOpened, apiv1.ScheduledOccurrenceRunning:
		return true
	}
	return false
}

// decisionNoun names what approve and retry send.
var decisionNoun = map[string]string{"approve": "approval", "retry": "retry"}

// scheduleDecide is approve and retry. The hash it sends binds the decision
// to the exact spec this command printed, so an edit made after the fetch is
// never approved or retried. The hash is not evidence of who decided or that
// anyone read the spec: the API identifies the human from the token.
func (a *app) scheduleDecide(ctx context.Context, verb string, args []string) error {
	cmd := newCommand("schedule", outputJSON)
	var specHash, at string
	var yes bool
	cmd.fs.StringVar(&specHash, "spec-hash", "", "")
	cmd.fs.StringVar(&at, "at", "", "")
	cmd.fs.BoolVar(&yes, "yes", false, "")
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("schedule %s takes one name; schedule list shows them", verb)
	}
	name := pos[0]
	if specHash != "" && !scheduleSpecHash.MatchString(specHash) {
		return usageError("--spec-hash is the 64 lowercase hex characters schedule show prints, not %q", specHash)
	}
	var atT *time.Time
	if at != "" {
		t, err := scheduleInstant("at", at)
		if err != nil {
			return err
		}
		atT = &t
	}
	pin := "--spec-hash <hash> --yes"
	if verb == "retry" {
		pin = "--at <time> " + pin
	}
	how := fmt.Sprintf("agent-run schedule show %s, then agent-run schedule %s %s %s. Nothing was sent", name, verb, name, pin)
	switch {
	case yes && specHash == "":
		// --yes alone would approve whatever spec the API holds at the fetch.
		return usageError("--yes needs --spec-hash, the hash you read: run %s", how)
	case yes && verb == "retry" && at == "":
		// The hash pins the spec; without --at, --yes would retry whichever
		// occurrence is parked at the fetch.
		return usageError("--yes needs --at for a retry: the hash pins the spec, not the occurrence. Run %s", how)
	case !yes && !scheduleStdinIsTerminal(a.env.Stdin):
		return usageError("schedule %s asks y/N on a terminal, and there is none. Run %s", verb, how)
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	w, err := a.scheduleForDecision(ctx, c, verb, name)
	if err != nil {
		return err
	}
	noun := decisionNoun[verb]
	if w.Spec.Cancelled {
		return fail(ExitFailed, "%s is cancelled; there is nothing to %s. No %s was sent", shown(name, false), verb, noun)
	}
	if specHash != "" && specHash != w.SpecHash {
		return fail(ExitFailed, "the spec of %s changed after you saw it: its hash is now %s, not %s. Read it again with agent-run schedule show %s. No %s was sent",
			shown(name, false), w.SpecHash, specHash, shown(name, false), noun)
	}
	parked, err := scheduleParked(w, verb, atT)
	if err != nil {
		return err
	}
	// A question goes with the whole decision on stderr, the stream a person
	// answers on; with --yes, stdout carries it unless it carries JSON.
	out := a.env.Stdout
	if !yes || cmd.c.output == outputJSON {
		out = a.env.Stderr
	}
	question := a.printDecision(out, verb, w, parked)
	if !yes && !a.scheduleConfirm(question) {
		return fail(ExitFailed, "not %s; no %s was sent", map[string]string{"approve": "approved", "retry": "retried"}[verb], noun)
	}
	var res apiv1.ScheduledWork
	var status int
	// callOnce: either one may start work, so a lost answer is reported, not
	// repeated.
	if verb == "approve" {
		req := apiv1.ApproveScheduledWorkRequest{SpecHash: w.SpecHash}
		if parked != nil {
			t := parked.At
			req.At = &t
		}
		status, err = a.callOnce(ctx, c, http.MethodPost, apiv1.ScheduledWorkApprovePath(url.PathEscape(name)), req, &res)
	} else {
		req := apiv1.RetryScheduledWorkRequest{At: parked.At, SpecHash: w.SpecHash}
		status, err = a.callOnce(ctx, c, http.MethodPost, apiv1.ScheduledWorkRetryPath(url.PathEscape(name)), req, &res)
	}
	if err != nil {
		return scheduleRefusal(verb, name, status, err)
	}
	if err := answerNames(res, name, noun); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(res)
	}
	if verb == "approve" {
		a.outf("approved %s: spec hash %s\n", shown(name, false), w.SpecHash)
	} else {
		a.outf("retried %s\n", shown(name, false))
	}
	if parked != nil {
		// What the API recorded, not a promise: the next due-time check may
		// park it again.
		state := "not in the answer"
		for _, o := range res.Occurrences {
			if o.At.Equal(parked.At) {
				state = shown(o.State, false)
				if o.Reason != "" {
					state += " (" + shown(o.Reason, false) + ")"
				}
			}
		}
		a.outf("the occurrence at %s is now %s; agent-run schedule show %s follows it\n", parked.At.UTC().Format(time.RFC3339), state, shown(name, false))
	}
	if res.Message != "" {
		a.outf("note: %s\n", shown(res.Message, false))
	}
	return nil
}

// answerNames refuses an answer for another item than the one asked for. A
// write may already have been applied to the item asked for.
func answerNames(w apiv1.ScheduledWork, name, what string) error {
	if w.Name == name {
		return nil
	}
	return fail(ExitFailed, "the API's answer names \"%s\", not \"%s\"; the %s may have taken effect: check agent-run schedule show %s", shown(w.Name, false), shown(name, false), what, shown(name, false))
}

// scheduleForDecision reads the item to approve or retry, so the spec this
// command prints is the one the hash it sends was computed from. It refuses an
// answer for another item, a spec with a field this agent-run does not know
// (it could not print it), and a view without a spec hash.
func (a *app) scheduleForDecision(ctx context.Context, c *conn, verb, name string) (apiv1.ScheduledWork, error) {
	noun := decisionNoun[verb]
	var w apiv1.ScheduledWork
	var raw json.RawMessage
	if status, err := a.call(ctx, c, http.MethodGet, apiv1.ScheduledWorkItemPath(url.PathEscape(name)), nil, nil, &raw); err != nil {
		return w, scheduleRefusal("show", name, status, err)
	}
	var doc struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(raw, &w); err != nil || json.Unmarshal(raw, &doc) != nil {
		return w, fail(ExitFailed, "the API's view of %s is not the expected JSON; no %s was sent", shown(name, false), noun)
	}
	if w.Name != name {
		return w, fail(ExitFailed, "the API answered for \"%s\", not \"%s\"; no %s was sent", shown(w.Name, false), shown(name, false), noun)
	}
	spec := bytes.TrimSpace(doc.Spec)
	dec := json.NewDecoder(bytes.NewReader(spec))
	dec.DisallowUnknownFields()
	var strict apiv1.ScheduledWorkSpec
	if len(spec) == 0 || bytes.Equal(spec, []byte("null")) {
		return w, fail(ExitFailed, "the API showed no spec for %s, so there is nothing to %s; no %s was sent", shown(name, false), verb, noun)
	}
	if err := dec.Decode(&strict); err != nil {
		return w, fail(ExitFailed, "the API's spec of %s has a field this agent-run cannot show (%v): upgrade agent-run before you %s it, so you see all of what you %s. No %s was sent",
			shown(name, false), err, verb, verb, noun)
	}
	if !scheduleSpecHash.MatchString(w.SpecHash) {
		return w, fail(ExitFailed, "the API gave no spec hash for %s, so a %s could not name the spec shown; no %s was sent", shown(name, false), noun, noun)
	}
	return w, nil
}

func parkedOccurrences(w apiv1.ScheduledWork) []*apiv1.ScheduledWorkOccurrence {
	var parked []*apiv1.ScheduledWorkOccurrence
	for i := range w.Occurrences {
		if w.Occurrences[i].State == apiv1.ScheduledOccurrenceParked {
			parked = append(parked, &w.Occurrences[i])
		}
	}
	return parked
}

// claimedEarlier reports whether o was claimed with a spec other than w's
// current one. An occurrence parked before any claim has no claim hash.
func claimedEarlier(w apiv1.ScheduledWork, o *apiv1.ScheduledWorkOccurrence) bool {
	return o.SpecHash != "" && o.SpecHash != w.SpecHash
}

// scheduleParked picks the parked occurrence a decision retries: the one at
// at, or for a retry the only one. An approval retries one only when at names
// it. An occurrence that has used its retries is refused, and so is one
// claimed with an earlier spec, which the API would refuse to retry.
func scheduleParked(w apiv1.ScheduledWork, verb string, at *time.Time) (*apiv1.ScheduledWorkOccurrence, error) {
	noun, name := decisionNoun[verb], shown(w.Name, false)
	var o *apiv1.ScheduledWorkOccurrence
	switch {
	case at != nil:
		for i := range w.Occurrences {
			if w.Occurrences[i].At.Equal(*at) {
				o = &w.Occurrences[i]
				break
			}
		}
		if o == nil {
			return nil, fail(ExitFailed, "%s has no occurrence at %s; agent-run schedule show %s lists them. No %s was sent", name, at.Format(time.RFC3339), name, noun)
		}
		if o.State != apiv1.ScheduledOccurrenceParked {
			return nil, fail(ExitFailed, "the occurrence of %s at %s is %s, not Parked; only a parked occurrence is retried. No %s was sent", name, at.Format(time.RFC3339), shown(o.State, false), noun)
		}
	case verb == "approve":
		return nil, nil
	default:
		parked := parkedOccurrences(w)
		switch len(parked) {
		case 0:
			return nil, fail(ExitFailed, "%s has no parked occurrence, so there is nothing to retry. No retry was sent", name)
		case 1:
			o = parked[0]
		default:
			ats := make([]string, 0, len(parked))
			for _, p := range parked {
				ats = append(ats, p.At.UTC().Format(time.RFC3339))
			}
			return nil, fail(ExitFailed, "%s has %d parked occurrences (%s); name one with --at. No retry was sent", name, len(parked), strings.Join(ats, ", "))
		}
	}
	if o.Attempt >= apiv1.MaxScheduledWorkAttempt {
		return nil, fail(ExitFailed, "the occurrence of %s at %s has used its %d retries; a fourth is refused. No %s was sent",
			name, o.At.UTC().Format(time.RFC3339), apiv1.MaxScheduledWorkAttempt, noun)
	}
	if claimedEarlier(w, o) {
		// The API answers approve --at and retry of such an occurrence 409
		// (ApproveScheduledWorkRequest, RetryScheduledWorkRequest), so asking
		// the person to confirm it would confirm a refusal.
		instead := "Approve the current spec for the next occurrence instead"
		if verb == "approve" {
			instead = "Approve without --at to approve the current spec for later occurrences"
		}
		return nil, fail(ExitFailed, "the occurrence of %s at %s was claimed with an earlier spec (hash %s); the API does not retry an occurrence claimed with an earlier spec. %s. No %s was sent",
			name, o.At.UTC().Format(time.RFC3339), shown(o.SpecHash, false), instead, noun)
	}
	return o, nil
}

// printDecision shows what an approve or retry binds to and returns the y/N
// question.
func (a *app) printDecision(out io.Writer, verb string, w apiv1.ScheduledWork, parked *apiv1.ScheduledWorkOccurrence) string {
	name := shown(w.Name, false)
	if verb == "approve" {
		pf(out, "You are approving this exact spec of %s:\n", name)
	} else {
		pf(out, "You are retrying an occurrence of %s, with this spec:\n", name)
	}
	printScheduleSpec(out, w)
	printScheduleApproval(out, w)
	if parked != nil {
		pf(out, "Parked occurrence %s: %s; attempt %d of at most %d\n", parked.At.UTC().Format(time.RFC3339),
			shown(firstOf(parked.Reason, "no reason given"), false), parked.Attempt, apiv1.MaxScheduledWorkAttempt)
	}
	if verb == "retry" {
		pf(out, "A retry runs it again now, late on purpose, and grants no authority: it is checked again as at its due time.\n")
		return fmt.Sprintf("Retry the occurrence of %s at %s now? [y/N] ", name, parked.At.UTC().Format(time.RFC3339))
	}
	if parked == nil {
		for _, o := range parkedOccurrences(w) {
			at := o.At.UTC().Format(time.RFC3339)
			what := "add --at " + at + " to retry it with this approval"
			switch {
			case claimedEarlier(w, o):
				what = "it was claimed with an earlier spec (hash " + shown(o.SpecHash, false) + "), so it cannot be retried"
			case o.Attempt >= apiv1.MaxScheduledWorkAttempt:
				what = "it has used its retries"
			}
			pf(out, "Parked occurrence %s stays parked (%s; attempt %d of at most %d): %s.\n", at,
				shown(firstOf(o.Reason, "no reason given"), false), o.Attempt, apiv1.MaxScheduledWorkAttempt, what)
		}
	}
	pf(out, "The approval binds to spec hash %s (every field above but editor and cancelled). Any later edit voids it.\n", w.SpecHash)
	if parked != nil {
		at := parked.At.UTC().Format(time.RFC3339)
		pf(out, "Approving also runs the occurrence at %s again now, late on purpose.\n", at)
		return fmt.Sprintf("Approve spec %s of %s and retry the occurrence at %s? [y/N] ", w.SpecHash, name, at)
	}
	return fmt.Sprintf("Approve spec %s of %s? [y/N] ", w.SpecHash, name)
}

// scheduleConfirm asks question on stderr and reads one line of stdin; only y
// or yes agrees, and anything else, end of input included, declines.
func (a *app) scheduleConfirm(question string) bool {
	_, _ = io.WriteString(a.env.Stderr, question)
	line, err := bufio.NewReader(io.LimitReader(a.env.Stdin, 256)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// scheduleFieldFlags name a refusal's request fields by the flags that set
// them. The shared refusal prints these JSON paths as they are.
var scheduleFieldFlags = []struct{ field, flag string }{
	{"work.prompt", "--prompt or --prompt-file"}, {"work.template", "--template"}, {"work.repo", "--repo"},
	{"notAfter", "--not-after"}, {"timeZone", "--tz"}, {"due", "--due"}, {"mode", "--mode"},
	{"recurrence", "--repeat"}, {"notify", "--notify"}, {"calendar", "--calendar"}, {"key", "--key"},
	{"specHash", "--spec-hash"}, {"at", "--at"},
}

// withNote adds a line to a command's error, keeping its exit code.
func withNote(err error, note string) error {
	var ee *exitError
	if !errors.As(err, &ee) {
		return err
	}
	return &exitError{code: ee.code, msg: ee.msg + "\n" + note}
}

// scheduleRefusal names the fields of an API refusal by their flags and adds
// what to do next, by route and status. The exit code stays the refusal's.
func scheduleRefusal(verb, name string, status int, err error) error {
	var ee *exitError
	if err == nil || status == 0 || !errors.As(err, &ee) {
		return err
	}
	msg := ee.msg
	for _, f := range scheduleFieldFlags {
		msg = strings.ReplaceAll(msg, "\n  "+f.field+": ", "\n  "+f.flag+" ("+f.field+"): ")
	}
	var hint string
	switch status {
	case http.StatusBadRequest:
		hint = "the API could not read the request: agent-run and the operator may be different versions (agent-run version prints this one's)"
	case http.StatusForbidden:
		switch verb {
		case "approve", "retry":
			hint = "approve and retry are for a human (Tom) only; a session, client or coordinator cannot " + verb
		case "edit":
			hint = "only the owner or a human may edit scheduled work; a coordinator may cancel it"
		case "cancel":
			hint = "only the owner, a human or a coordinator may cancel scheduled work"
		default:
			hint = "only the owner, a human or a coordinator may see scheduled work; list --all is for a human or a coordinator"
		}
	case http.StatusNotFound:
		hint = "agent-run schedule list shows your scheduled work; an operator that does not serve the scheduled-work API answers 404 on every route"
	case http.StatusConflict:
		switch verb {
		case "create":
			hint = "a --key names one item of yours: the same key with other settings is refused; use another key, or edit the item"
		case "approve":
			hint = fmt.Sprintf("the API refuses an approval when the item is cancelled, the occurrence named by --at is no longer Parked or has used its %d retries, or the spec changed after you saw it; read it again with agent-run schedule show %s",
				apiv1.MaxScheduledWorkAttempt, shown(name, false))
		case "retry":
			hint = fmt.Sprintf("the API refuses a retry when the item is cancelled, the occurrence is no longer Parked or has used its %d retries, or the spec changed after you saw it; agent-run schedule show %s shows which",
				apiv1.MaxScheduledWorkAttempt, shown(name, false))
		default:
			hint = fmt.Sprintf("agent-run schedule show %s shows where it stands now", shown(name, false))
		}
	}
	if hint != "" {
		msg += "\n" + hint
	}
	return &exitError{code: ee.code, msg: msg}
}

// scheduleInvisible are characters that render as nothing and are in neither
// the format category nor the default-ignorable properties shown checks.
var scheduleInvisible = []rune{'\u2028', '\u2029', '\u034f', '\u115f', '\u1160', '\u3164', '\uffa0', '\u180b', '\u180c', '\u180d'}

// shown renders API text for a terminal. A control or format character (an
// escape sequence, a carriage return, a bidirectional override), a variation
// selector, a default-ignorable or filler character that renders as nothing,
// and a byte that is not UTF-8 are printed as escapes rather than passed to
// the terminal, so what a person reads is what the item holds. multiline keeps
// newlines and tabs.
func shown(s string, multiline bool) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case multiline && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) ||
			unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || slices.Contains(scheduleInvisible, r):
			if r > 0xffff {
				fmt.Fprintf(&b, `\U%08x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// pf prints to a display writer; a failed write to the terminal has no remedy.
func pf(out io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }
