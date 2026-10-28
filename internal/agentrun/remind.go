package agentrun

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// agent-run remind (DESIGN-032, OpenProject #206): create, list, show, edit and
// cancel reminders through /v1/reminders. The operator serves the routes only
// when it runs with --enable-reminders-api; otherwise they answer 404.
//
//	remind create "<title>" --due 2026-10-30T09:00 --tz America/New_York
//	      [--body <text>] [--repeat daily|weekly[:MO,TH]|monthly[:15]] [--count N | --until <RFC 3339>]
//	      [--targets pushover:default,calendar:<ref>] [--key <idempotency key>]
//	remind list [--all]
//	remind show <name>
//	remind edit <name> [--title ..] [--body ..] [--due ..] [--tz ..] [--repeat .. | --once] [--count|--until] [--targets ..]
//	remind cancel <name>

func init() { commandHelp["remind"] = remindHelp }

const remindHelp = `Usage: agent-run remind create "<title>" --due <YYYY-MM-DDTHH:MM> --tz <IANA zone> [flags]
       agent-run remind list [--all] [-o json]
       agent-run remind show <name> [-o json]
       agent-run remind edit <name> [--title T] [--body B] [--due D] [--tz Z] [--repeat R | --once] [--targets ..]
       agent-run remind cancel <name>

Reminders are durable. The due time is a local wall time read in --tz. A time the
zone skips (a spring-forward gap) fires one gap-length later; a time it repeats
(a fall-back fold) fires the first time. A reminder delivers a Pushover
notification when due, and a calendar entry for each calendar target.

Flags of create and edit:
  --due <YYYY-MM-DDTHH:MM>   local time of the first occurrence
  --tz <zone>                IANA zone, such as America/New_York
  --body <text>              the message
  --repeat <rule>            daily, weekly[:MO,TH], monthly[:15]; a monthly day a
                             month lacks fires on that month's last day
  --count <n> | --until <t>  end the series (--until is an RFC 3339 instant)
  --once                     (edit) make a repeating reminder one-shot
  --targets <a,b>            pushover:default and calendar:<ref>; default pushover:default
  --key <k>                  (create) a retried create with the same key makes no second reminder

Only the owner, a human or a coordinator may show, edit or cancel a reminder. list
shows your own; --all (a human or coordinator) shows everyone's.
`

func (a *app) remind(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usageError("remind takes create, list, show, edit or cancel; agent-run help remind says more")
	}
	switch args[0] {
	case "create":
		return a.remindCreate(ctx, args[1:])
	case "list":
		return a.remindList(ctx, args[1:])
	case "show":
		return a.remindShow(ctx, args[1:])
	case "edit":
		return a.remindEdit(ctx, args[1:])
	case "cancel":
		return a.remindCancel(ctx, args[1:])
	case "help", "-h", "--help":
		return a.help([]string{"remind"})
	}
	return usageError("remind takes create, list, show, edit or cancel, not %q", args[0])
}

type remindFlags struct {
	body, due, tz, repeat, until, targets, title, key string
	count                                             int
	once, all                                         bool
}

func (rf *remindFlags) bind(cmd *command) {
	fs := cmd.fs
	fs.StringVar(&rf.body, "body", "", "")
	fs.StringVar(&rf.due, "due", "", "")
	fs.StringVar(&rf.tz, "tz", "", "")
	fs.StringVar(&rf.repeat, "repeat", "", "")
	fs.StringVar(&rf.until, "until", "", "")
	fs.StringVar(&rf.targets, "targets", "", "")
	fs.StringVar(&rf.title, "title", "", "")
	fs.StringVar(&rf.key, "key", "", "")
	fs.IntVar(&rf.count, "count", 0, "")
	fs.BoolVar(&rf.once, "once", false, "")
	fs.BoolVar(&rf.all, "all", false, "")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// recurrence builds the request's recurrence from --repeat, --count and --until.
func (rf *remindFlags) recurrence() (*apiv1.ReminderRecurrence, error) {
	if rf.repeat == "" {
		if rf.count != 0 || rf.until != "" {
			return nil, usageError("--count and --until end a series: add --repeat daily, weekly or monthly")
		}
		return nil, nil
	}
	freq, arg, _ := strings.Cut(rf.repeat, ":")
	rec := &apiv1.ReminderRecurrence{Freq: freq, Count: rf.count}
	switch freq {
	case "daily":
		if arg != "" {
			return nil, usageError("--repeat daily takes no argument, not %q", arg)
		}
	case "weekly":
		rec.ByDay = splitList(arg)
	case "monthly":
		if arg != "" {
			n, err := strconv.Atoi(arg)
			if err != nil || n < 1 || n > 31 {
				return nil, usageError("--repeat monthly:<day> takes a day from 1 to 31, not %q", arg)
			}
			rec.ByMonthDay = n
		}
	default:
		return nil, usageError("--repeat is daily, weekly[:MO,TH] or monthly[:15], not %q", rf.repeat)
	}
	if rf.until != "" {
		t, err := time.Parse(time.RFC3339, rf.until)
		if err != nil {
			return nil, usageError("--until %q is not an RFC 3339 instant such as 2026-12-31T23:59:00Z", rf.until)
		}
		rec.Until = &t
	}
	if rec.Count != 0 && rec.Until != nil {
		return nil, usageError("--count and --until are exclusive")
	}
	return rec, nil
}

func (a *app) remindCreate(ctx context.Context, args []string) error {
	cmd := newCommand("remind", outputJSON)
	var rf remindFlags
	rf.bind(cmd)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(strings.Join(pos, " "))
	switch {
	case title == "":
		return usageError(`remind create takes a title: remind create "<title>" --due <YYYY-MM-DDTHH:MM> --tz <zone>`)
	case rf.due == "" || rf.tz == "":
		return usageError("--due and --tz are required: the due time is a local time read in a zone")
	}
	rec, err := rf.recurrence()
	if err != nil {
		return err
	}
	req := apiv1.CreateReminderRequest{Key: rf.key, Title: title, Body: rf.body, Due: rf.due, TimeZone: rf.tz, Recurrence: rec, Targets: splitList(rf.targets)}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var rem apiv1.Reminder
	if _, err := a.call(ctx, c, http.MethodPost, apiv1.RemindersPath, nil, req, &rem); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(rem)
	}
	a.outf("created %s: %q, %s\n", rem.Name, rem.Title, describeSchedule(rem))
	a.outf("cancel it with: agent-run remind cancel %s\n", rem.Name)
	return nil
}

func describeSchedule(r apiv1.Reminder) string {
	s := r.Due + " " + r.TimeZone
	if rec := r.Recurrence; rec != nil {
		s += ", " + rec.Freq
		if len(rec.ByDay) > 0 {
			s += " on " + strings.Join(rec.ByDay, ",")
		}
		if rec.ByMonthDay != 0 {
			s += fmt.Sprintf(" on day %d", rec.ByMonthDay)
		}
		if rec.Count != 0 {
			s += fmt.Sprintf(", %d times", rec.Count)
		}
		if rec.Until != nil {
			s += ", until " + rec.Until.UTC().Format(time.RFC3339)
		}
	}
	return s
}

func (a *app) remindList(ctx context.Context, args []string) error {
	cmd := newCommand("remind", outputJSON)
	var rf remindFlags
	rf.bind(cmd)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError("remind list takes no arguments")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	q := url.Values{}
	if rf.all {
		q.Set("all", "true")
	}
	var l apiv1.ReminderList
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.RemindersPath, q, nil, &l); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(l)
	}
	if len(l.Reminders) == 0 {
		a.outf("No reminders.\n")
		return nil
	}
	tw := tabwriter.NewWriter(a.env.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tNEXT (UTC)\tSCHEDULE\tTITLE")
	for _, r := range l.Reminders {
		next := "-"
		if r.NextDue != nil {
			next = r.NextDue.UTC().Format("2006-01-02 15:04")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.State, next, describeSchedule(r), r.Title)
	}
	return tw.Flush()
}

func (a *app) remindShow(ctx context.Context, args []string) error {
	cmd := newCommand("remind", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("remind show takes one name; remind list shows them")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var r apiv1.Reminder
	if _, err := a.call(ctx, c, http.MethodGet, apiv1.ReminderPath(url.PathEscape(pos[0])), nil, nil, &r); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(r)
	}
	a.printReminder(r)
	return nil
}

func (a *app) printReminder(r apiv1.Reminder) {
	a.outf("%s  %s\n", r.Name, r.State)
	a.outf("  title:    %s\n", r.Title)
	if r.Body != "" {
		a.outf("  body:     %s\n", r.Body)
	}
	a.outf("  schedule: %s\n", describeSchedule(r))
	a.outf("  targets:  %s\n", strings.Join(r.Targets, ", "))
	a.outf("  owner:    %s\n", r.Owner)
	if r.NextDue != nil {
		a.outf("  next due: %s (occurrence %d)\n", r.NextDue.UTC().Format(time.RFC3339), r.Occurrence)
	}
	if r.Message != "" {
		a.outf("  note:     %s\n", r.Message)
	}
	if r.Warning != "" {
		a.outf("  WARNING:  %s\n", r.Warning)
	}
	for _, d := range r.Deliveries {
		reason := ""
		if d.Reason != "" {
			reason = " (" + d.Reason + ")"
		}
		a.outf("  delivery: %s occurrence %d %s, %d attempt(s)%s\n", d.Target, d.Occurrence, d.State, d.Attempts, reason)
	}
	for _, cs := range r.Calendar {
		reason := ""
		if cs.Reason != "" {
			reason = " (" + cs.Reason + ")"
		}
		a.outf("  calendar: %s %s%s\n", cs.Target, cs.State, reason)
	}
	for _, at := range r.Attempts {
		a.outf("  attempt:  %s occurrence %d %s %s %s\n", at.At.UTC().Format(time.RFC3339), at.Occurrence, at.Target, at.Outcome, at.Detail)
	}
}

func (a *app) remindEdit(ctx context.Context, args []string) error {
	cmd := newCommand("remind", outputJSON)
	var rf remindFlags
	rf.bind(cmd)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("remind edit takes one name; remind list shows them")
	}
	set := map[string]bool{}
	cmd.fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var req apiv1.EditReminderRequest
	if set["title"] {
		req.Title = &rf.title
	}
	if set["body"] {
		req.Body = &rf.body
	}
	if set["due"] {
		req.Due = &rf.due
	}
	if set["tz"] {
		req.TimeZone = &rf.tz
	}
	if set["targets"] {
		req.Targets = splitList(rf.targets)
		if len(req.Targets) == 0 {
			return usageError("--targets needs at least one target")
		}
	}
	switch {
	case rf.once && (set["repeat"] || set["count"] || set["until"]):
		return usageError("--once makes a reminder one-shot; it cannot be combined with --repeat, --count or --until")
	case rf.once:
		req.ClearRecurrence = true
	case set["repeat"] || set["count"] || set["until"]:
		rec, err := rf.recurrence()
		if err != nil {
			return err
		}
		req.Recurrence = rec
	}
	if req.Title == nil && req.Body == nil && req.Due == nil && req.TimeZone == nil && req.Targets == nil && req.Recurrence == nil && !req.ClearRecurrence {
		return usageError("remind edit changes something: --title, --body, --due, --tz, --repeat, --once or --targets")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var r apiv1.Reminder
	if _, err := a.call(ctx, c, http.MethodPatch, apiv1.ReminderPath(url.PathEscape(pos[0])), nil, req, &r); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(r)
	}
	a.outf("edited %s: %q, %s\n", r.Name, r.Title, describeSchedule(r))
	return nil
}

func (a *app) remindCancel(ctx context.Context, args []string) error {
	cmd := newCommand("remind", outputJSON)
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError("remind cancel takes one name; remind list shows them")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	var r apiv1.Reminder
	if _, err := a.call(ctx, c, http.MethodPost, apiv1.ReminderCancelPath(url.PathEscape(pos[0])), nil, nil, &r); err != nil {
		return err
	}
	if cmd.c.output == outputJSON {
		return a.printJSON(r)
	}
	a.outf("cancelled %s\n", r.Name)
	if r.Warning != "" {
		a.outf("WARNING: %s\n", r.Warning)
	}
	return nil
}
