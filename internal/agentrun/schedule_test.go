package agentrun

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var (
	testSpecHash = strings.Repeat("ab", 32)
	testOldHash  = strings.Repeat("cd", 32)
	testParkedAt = testNow.Add(-2 * time.Hour)
	testWorkName = "sched-0123456789abcdef0123"
)

// testScheduledWork is a headless item as the API shows it: one succeeded,
// one skipped and one parked occurrence (stored oldest first), and an
// approval of an earlier spec.
func testScheduledWork() apiv1.ScheduledWork {
	next := testNow.Add(48 * time.Hour)
	claimed := testParkedAt.Add(time.Minute)
	retried := testParkedAt.Add(30 * time.Minute)
	return apiv1.ScheduledWork{
		Name: testWorkName,
		Spec: apiv1.ScheduledWorkSpec{
			Title: "Loki retention trend audit", Owner: "session/dev-env-1104/uid-1", Editor: "human/dev-env-system/tom",
			Due: "2026-10-01T09:00", TimeZone: "America/New_York",
			Recurrence: &apiv1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"MO", "TH"}},
			NotAfter:   time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), Mode: apiv1.ScheduledWorkHeadless,
			Work:   apiv1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "Audit Loki retention.\nOpen a PR with the trend."},
			Notify: []string{"pushover:default"}, Calendar: "calendar:haynesnetwork",
		},
		SpecHash: testSpecHash, State: apiv1.ScheduledWorkActive, NextDue: &next,
		Approval: &apiv1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: testNow.Add(-24 * time.Hour), SpecHash: testOldHash},
		Counts:   apiv1.ScheduledWorkCounts{Succeeded: 1, Skipped: 3, Parked: 1},
		Occurrences: []apiv1.ScheduledWorkOccurrence{
			{At: testNow.Add(-7 * 24 * time.Hour), State: apiv1.ScheduledOccurrenceSucceeded, Session: "sw-0123456789ab-202609291300", SpecHash: testSpecHash,
				Execution: &apiv1.ScheduledWorkExecution{State: "Succeeded", Reason: "TaskExitedZero"},
				Reported: &apiv1.ScheduledWorkReported{Summary: "retention trend flat", PRURLs: []string{"https://github.com/thaynes43/haynes-ops/pull/4100"},
					HeadSHAs: []string{strings.Repeat("e", 40)}},
				Pages: []apiv1.ScheduledWorkPage{{Event: "due", Reminder: "rem-due-1", State: "sent"}, {Event: "result", Reminder: "rem-result-1", State: "failed"}}},
			{At: testNow.Add(-6 * 24 * time.Hour), State: apiv1.ScheduledOccurrenceSkipped, Reason: "missed beyond grace", Missed: 3},
			{At: testParkedAt, State: apiv1.ScheduledOccurrenceParked, Reason: "no standing authority", Attempt: 1, ClaimedAt: &claimed, RetriedAt: &retried, SpecHash: testSpecHash},
		},
		CreatedAt: testNow.Add(-30 * 24 * time.Hour),
	}
}

// scheduleCreateArgs is a create that passes every local check.
func scheduleCreateArgs(extra ...string) []string {
	return append([]string{"schedule", "create", "Loki retention trend audit", "--due", "2026-12-03T09:00", "--tz", "America/New_York",
		"--not-after", "2027-02-01T00:00:00Z", "--mode", "headless", "--template", "ops-audit", "--repo", "haynes-ops",
		"--prompt", "Audit Loki retention."}, extra...)
}

// withTerminal makes stdin count as a terminal that answers answer.
func withTerminal(t *testing.T, h *harness, answer string) {
	t.Helper()
	saved := scheduleStdinIsTerminal
	scheduleStdinIsTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { scheduleStdinIsTerminal = saved })
	h.env.Stdin = strings.NewReader(answer)
}

func refuseAll(t *testing.T, h *harness) {
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		t.Errorf("a refused command reached the API: %s %s", r.Method, r.URL.Path)
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
	}
}

func TestScheduleCreate(t *testing.T) {
	h := newHarness(t)
	view := testScheduledWork()
	view.Message = "the owner is a session: it runs while that session is live, else it needs Tom's approval"
	var raw []byte
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.ScheduledWorkPath {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
			return
		}
		raw = body
		writeTestJSON(w, http.StatusCreated, view)
	}
	h.mustRun(ExitOK, scheduleCreateArgs("--repeat", "weekly:MO,TH", "--count", "4", "--notify", "pushover:default", "--notify", "pushover:ops",
		"--calendar", "calendar:haynesnetwork", "--key", "loki-audit")...)
	got := decodeAs[apiv1.CreateScheduledWorkRequest](t, raw)
	want := apiv1.CreateScheduledWorkRequest{
		Key: "loki-audit", Title: "Loki retention trend audit", Due: "2026-12-03T09:00", TimeZone: "America/New_York",
		Recurrence: &apiv1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"MO", "TH"}, Count: 4},
		NotAfter:   time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), Mode: "headless",
		Work:   apiv1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "Audit Loki retention."},
		Notify: []string{"pushover:default", "pushover:ops"}, Calendar: "calendar:haynesnetwork",
	}
	if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
		t.Errorf("request\n got %s\nwant %s", g, w)
	}
	// The wire names S5b decodes.
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"key", "title", "due", "timeZone", "recurrence", "notAfter", "mode", "work", "notify", "calendar"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("request has no %q: %s", k, raw)
		}
	}
	if work, _ := keys["work"].(map[string]any); work["template"] != "ops-audit" || work["repo"] != "haynes-ops" || work["prompt"] != "Audit Loki retention." {
		t.Errorf("work %v", keys["work"])
	}
	for _, k := range []string{"owner", "editor", "cancelled"} {
		if _, ok := keys[k]; ok {
			t.Errorf("a create must not carry %q, the server sets it: %s", k, raw)
		}
	}
	contains(t, "stdout", h.stdout.String(), `created sched-0123456789abcdef0123: "Loki retention trend audit", headless, 2026-10-01T09:00 America/New_York, weekly on MO,TH`,
		"nothing runs after 2027-02-01T00:00:00Z", "note: the owner is a session", "key loki-audit", "agent-run schedule show sched-0123456789abcdef0123",
		"agent-run schedule cancel sched-0123456789abcdef0123")

	// No --key: agent-run picks one, so a retried create makes no second item.
	h.mustRun(ExitOK, scheduleCreateArgs("-o", "json")...)
	if got := decodeAs[apiv1.CreateScheduledWorkRequest](t, raw); got.Key != "agent-run-testkey" || got.Recurrence != nil || got.Notify != nil || got.Calendar != "" {
		t.Errorf("a create without --key: %+v", got)
	}
	var echoed apiv1.ScheduledWork
	if err := json.Unmarshal(h.stdout.Bytes(), &echoed); err != nil || echoed.Name != testWorkName || echoed.SpecHash != testSpecHash {
		t.Errorf("json output %q: %v", h.stdout.String(), err)
	}

	// Interactive work with a notify target, the work order from a file.
	prompt := h.write("order.txt", "Wait for Tom, then review the alert backlog.\n")
	h.mustRun(ExitOK, "schedule", "create", "Alert backlog review", "--due", "2026-12-03T09:00", "--tz", "UTC", "--not-after", "2027-01-01T00:00:00Z",
		"--mode", "interactive", "--template", "ops-interactive", "--repo", "haynes-ops", "--prompt-file", prompt, "--notify", "pushover:default")
	if got := decodeAs[apiv1.CreateScheduledWorkRequest](t, raw); got.Mode != "interactive" || got.Work.Prompt != "Wait for Tom, then review the alert backlog.\n" ||
		!slices.Equal(got.Notify, []string{"pushover:default"}) {
		t.Errorf("interactive create: %+v", got)
	}

	// --prompt-file - reads stdin.
	h.env.Stdin = strings.NewReader("from stdin")
	h.mustRun(ExitOK, "schedule", "create", "x", "--due", "2026-12-03T09:00", "--tz", "UTC", "--not-after", "2027-01-01T00:00:00Z",
		"--mode", "headless", "--template", "t", "--repo", "r", "--prompt-file", "-")
	if got := decodeAs[apiv1.CreateScheduledWorkRequest](t, raw); got.Work.Prompt != "from stdin" {
		t.Errorf("stdin prompt %q", got.Work.Prompt)
	}
}

// TestScheduleCreateUsageErrors: every local check refuses before anything is
// sent, with its own message.
func TestScheduleCreateUsageErrors(t *testing.T) {
	without := func(flagName string) []string {
		args := scheduleCreateArgs()
		i := slices.Index(args, flagName)
		return slices.Delete(args, i, i+2)
	}
	replace := func(flagName, value string) []string {
		args := scheduleCreateArgs()
		args[slices.Index(args, flagName)+1] = value
		return args
	}
	cases := []struct {
		name  string
		args  []string
		files map[string]string
		want  string
	}{
		{"no verb", []string{"schedule"}, nil, "schedule takes create, list"},
		{"unknown verb", []string{"schedule", "fly"}, nil, `not "fly"`},
		{"no title", []string{"schedule", "create", "--due", "2026-12-03T09:00"}, nil, "schedule create takes a title"},
		{"no due", without("--due"), nil, "--due and --tz are required"},
		{"no tz", without("--tz"), nil, "--due and --tz are required"},
		{"no not-after", without("--not-after"), nil, "--not-after is required"},
		{"no mode", without("--mode"), nil, "--mode is required"},
		{"no template", without("--template"), nil, "--template and --repo are required"},
		{"no repo", without("--repo"), nil, "--template and --repo are required"},
		{"bad due", replace("--due", "2026-12-03 09:00"), nil, `--due "2026-12-03 09:00" is not a local time`},
		{"bad tz", replace("--tz", "Mars/Olympus_Mons"), nil, `--tz "Mars/Olympus_Mons" is not an IANA zone`},
		{"tz Local", replace("--tz", "Local"), nil, `--tz "Local" is not an IANA zone`},
		{"bad mode", replace("--mode", "batch"), nil, `--mode is headless (a task session) or interactive (a remote session Tom joins), not "batch"`},
		{"bad not-after", replace("--not-after", "2027-02-01"), nil, `--not-after "2027-02-01" is not an RFC 3339 instant`},
		{"past not-after", replace("--not-after", "2026-10-01T00:00:00Z"), nil, "--not-after 2026-10-01T00:00:00Z has passed"},
		{"bad repeat", scheduleCreateArgs("--repeat", "hourly"), nil, "--repeat is daily, weekly"},
		{"bad weekday", scheduleCreateArgs("--repeat", "weekly:MO,XX"), nil, `takes MO, TU, WE, TH, FR, SA or SU, not "XX"`},
		{"count without repeat", scheduleCreateArgs("--count", "3"), nil, "--count and --until end a series"},
		{"count and until", scheduleCreateArgs("--repeat", "daily", "--count", "3", "--until", "2026-12-31T00:00:00Z"), nil, "--count and --until are exclusive"},
		{"prompt and prompt-file", scheduleCreateArgs("--prompt-file", "@order"), map[string]string{"order": "x"}, "--prompt or --prompt-file, not both"},
		{"empty prompt and prompt-file", append(without("--prompt"), "--prompt", "", "--prompt-file", "@order"), map[string]string{"order": "x"}, "--prompt or --prompt-file, not both"},
		{"no prompt", without("--prompt"), nil, "the work order is empty"},
		{"blank prompt", replace("--prompt", " \n\t"), nil, "the work order is empty"},
		{"prompt too long", replace("--prompt", strings.Repeat("x", 8193)), nil, "the work order is 8193 bytes, more than 8192"},
		{"prompt-file not UTF-8", append(without("--prompt"), "--prompt-file", "@order"), map[string]string{"order": "bad \xff\xfe"}, "the work order is not UTF-8 text"},
		{"prompt-file missing", append(without("--prompt"), "--prompt-file", "/nonexistent/order.txt"), nil, "--prompt-file:"},
		{"notify not pushover", scheduleCreateArgs("--notify", "default"), nil, `--notify takes pushover:<ref>, such as pushover:default, not "default"`},
		{"notify calendar", scheduleCreateArgs("--notify", "calendar:haynesnetwork"), nil, "a calendar is not a page; give it with --calendar"},
		{"interactive without notify", replace("--mode", "interactive"), nil, "interactive work needs --notify pushover:<ref>"},
		{"bad calendar", scheduleCreateArgs("--calendar", "haynesnetwork"), nil, `--calendar takes calendar:<ref>`},
		{"key too long", scheduleCreateArgs("--key", strings.Repeat("k", 129)), nil, "--key is 129 bytes, more than 128"},
		{"approve flag on create", scheduleCreateArgs("--yes"), nil, "flag provided but not defined: -yes"},
		{"-p is not --prompt", append(without("--prompt"), "-p", "x"), nil, "flag provided but not defined: -p"},
		{"list argument", []string{"schedule", "list", "extra"}, nil, "schedule list takes no arguments"},
		{"show no name", []string{"schedule", "show"}, nil, "schedule show takes one name"},
		{"cancel two names", []string{"schedule", "cancel", "a", "b"}, nil, "schedule cancel takes one name"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			h := newHarness(t)
			refuseAll(t, h)
			args := slices.Clone(tc.args)
			for i, a := range args {
				if name, ok := strings.CutPrefix(a, "@"); ok {
					args[i] = h.write(name, tc.files[name])
				}
			}
			if code := h.run(args...); code != ExitUsage {
				t.Errorf("exit %d, want %d (usage)\nstderr: %s", code, ExitUsage, h.stderr.String())
			}
			contains(t, "stderr", h.stderr.String(), tc.want)
			if n := len(h.api.requests()); n != 0 {
				t.Errorf("%d requests sent", n)
			}
		})
	}
	// The control: the base create passes every check.
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusCreated, testScheduledWork())
	}
	h.mustRun(ExitOK, scheduleCreateArgs()...)
	h.mustRun(ExitOK, scheduleCreateArgs("--prompt", strings.Repeat("x", 8192))...)
}

func TestScheduleListAndShow(t *testing.T) {
	h := newHarness(t)
	view := testScheduledWork()
	view.Warning = "an occurrence is running"
	var gotQuery string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.ScheduledWorkPath:
			gotQuery = r.URL.RawQuery
			writeTestJSON(w, http.StatusOK, apiv1.ScheduledWorkList{Items: []apiv1.ScheduledWork{view}})
		case r.Method == http.MethodGet && r.URL.Path == apiv1.ScheduledWorkItemPath(view.Name):
			writeTestJSON(w, http.StatusOK, view)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
		}
	}
	h.mustRun(ExitOK, "schedule", "list")
	contains(t, "stdout", h.stdout.String(), "NAME", "MODE", "sched-0123456789abcdef0123  headless  active", "2026-10-08 21:00", "Loki retention trend audit")
	if gotQuery != "" {
		t.Errorf("list query %q", gotQuery)
	}
	h.mustRun(ExitOK, "schedule", "list", "--all", "-o", "json")
	if gotQuery != "all=true" {
		t.Errorf("list --all query %q", gotQuery)
	}
	var l apiv1.ScheduledWorkList
	if err := json.Unmarshal(h.stdout.Bytes(), &l); err != nil || len(l.Items) != 1 {
		t.Errorf("list json %q: %v", h.stdout.String(), err)
	}

	h.mustRun(ExitOK, "schedule", "show", view.Name)
	out := h.stdout.String()
	contains(t, "show", out,
		"sched-0123456789abcdef0123  active",
		"title:      Loki retention trend audit",
		"mode:       headless",
		"schedule:   2026-10-01T09:00 America/New_York, weekly on MO,TH",
		"not after:  2027-02-01T00:00:00Z",
		"template:   ops-audit", "repo:       haynes-ops",
		"notify:     pushover:default", "calendar:   calendar:haynesnetwork",
		"owner:      session/dev-env-1104/uid-1", "editor:     human/dev-env-system/tom",
		"spec hash:  "+testSpecHash,
		"prompt:     47 bytes, between the bars\n    | Audit Loki retention.\n    | Open a PR with the trend.\n",
		"approval:   VOID: by human/dev-env-system/tom at 2026-10-05T21:00:00Z, of an earlier spec (hash "+testOldHash+")",
		"next due:   2026-10-08T21:00:00Z",
		"counts:     1 succeeded, 1 parked, 3 skipped",
		"WARNING:    an occurrence is running",
		"occurrences, newest first:",
		"2026-10-06T19:00:00Z  Parked\n      reason:     no standing authority\n      attempt:    1 of at most 3, last retried 2026-10-06T19:30:00Z",
		"claimed:    2026-10-06T19:01:00Z",
		"missed:     3 due times",
		"session:    sw-0123456789ab-202609291300",
		"execution:  Succeeded (TaskExitedZero)",
		"reported:   retention trend flat (the agent's own claim, not checked)",
		"PR:         https://github.com/thaynes43/haynes-ops/pull/4100",
		"head:       "+strings.Repeat("e", 40),
		"page:       due, attempt 0: sent (rem-due-1)", "page:       result, attempt 0: failed (rem-result-1)")
	parked, skipped, succeeded := strings.Index(out, "Parked\n"), strings.Index(out, "Skipped\n"), strings.Index(out, "Succeeded\n")
	if parked < 0 || parked >= skipped || skipped >= succeeded {
		t.Errorf("occurrences are not newest first (parked %d, skipped %d, succeeded %d):\n%s", parked, skipped, succeeded, out)
	}

	h.mustRun(ExitOK, "schedule", "show", view.Name, "-o", "json")
	var echoed apiv1.ScheduledWork
	if err := json.Unmarshal(h.stdout.Bytes(), &echoed); err != nil || echoed.SpecHash != testSpecHash || len(echoed.Occurrences) != 3 {
		t.Errorf("show json %q: %v", h.stdout.String(), err)
	}

	// A current approval, no recurrence, no notify, no occurrence.
	view.Approval.SpecHash = testSpecHash
	view.Spec.Recurrence, view.Spec.Notify, view.Spec.Calendar, view.Occurrences, view.Counts, view.Warning = nil, nil, "", nil, apiv1.ScheduledWorkCounts{}, ""
	h.mustRun(ExitOK, "schedule", "show", view.Name)
	contains(t, "show", h.stdout.String(), "approval:   by human/dev-env-system/tom at 2026-10-05T21:00:00Z, of this spec",
		"notify:     none (no pages)", "counts:     none yet", "occurrences: none recorded yet")
	view.Approval = nil
	h.mustRun(ExitOK, "schedule", "show", view.Name)
	contains(t, "show", h.stdout.String(), "approval:   none")

	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.ScheduledWorkList{})
	}
	h.mustRun(ExitOK, "schedule", "list")
	contains(t, "stdout", h.stdout.String(), "No scheduled work.")
}

// TestScheduleShowEscapesControlText: an item's text cannot drive the
// terminal, so what a person reads before approving is what the item holds.
func TestScheduleShowEscapesControlText(t *testing.T) {
	h := newHarness(t)
	view := testScheduledWork()
	view.Spec.Title = "Audit\x1b[2J"
	view.Spec.Work.Prompt = "visible\rhidden\x1b[8m concealed\u202eevil\nnext\tline \xff"
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, view) }
	h.mustRun(ExitOK, "schedule", "show", view.Name)
	out := h.stdout.String()
	// JSON carries a byte that is not UTF-8 as U+FFFD, which is printable;
	// shown's own escape of such a byte is checked directly below.
	contains(t, "show", out, `title:      Audit\u001b[2J`, `    | visible\u000dhidden\u001b[8m concealed\u202eevil`, "    | next\tline \ufffd")
	if strings.ContainsAny(out, "\x1b\r\u202e") {
		t.Errorf("a control character reached the terminal: %q", out)
	}
	if got := shown("a\x00b\u2028c\U000E0001\xffz", false); got != `a\u0000b\u2028c\U000e0001\xffz` {
		t.Errorf("shown = %q", got)
	}
	if got := shown("a\nb\tc", false); got != `a\u000ab\u0009c` {
		t.Errorf("shown single-line = %q", got)
	}
	if got := shown("a\nb\tc", true); got != "a\nb\tc" {
		t.Errorf("shown multiline = %q", got)
	}
}

func TestScheduleEdit(t *testing.T) {
	h := newHarness(t)
	view := testScheduledWork()
	var edited map[string]any
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPatch || r.URL.Path != apiv1.ScheduledWorkItemPath(view.Name) {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
			return
		}
		edited = map[string]any{}
		if err := json.Unmarshal(body, &edited); err != nil {
			t.Fatal(err)
		}
		writeTestJSON(w, http.StatusOK, view)
	}
	h.mustRun(ExitOK, "schedule", "edit", view.Name, "--title", "Loki audit", "--once", "--prompt", "New order.", "--no-notify", "--no-calendar")
	if got := mustJSON(t, edited); got != `{"calendar":"","clearRecurrence":true,"notify":[],"title":"Loki audit","work":{"prompt":"New order."}}` {
		t.Errorf("edit body %s", got)
	}
	contains(t, "stdout", h.stdout.String(), `edited sched-0123456789abcdef0123: "Loki retention trend audit", headless`, "spec hash now "+testSpecHash,
		"the approval by human/dev-env-system/tom was of the earlier spec and no longer applies", "agent-run schedule approve sched-0123456789abcdef0123")

	h.mustRun(ExitOK, "schedule", "edit", view.Name, "--due", "2026-12-04T10:30", "--tz", "Europe/London", "--mode", "interactive",
		"--template", "ops-interactive", "--repo", "dev-env", "--repeat", "monthly:15", "--until", "2026-12-31T00:00:00Z",
		"--not-after", "2027-01-15T00:00:00Z", "--notify", "pushover:default", "--calendar", "calendar:haynesnetwork")
	want := `{"calendar":"calendar:haynesnetwork","due":"2026-12-04T10:30","mode":"interactive","notAfter":"2027-01-15T00:00:00Z",` +
		`"notify":["pushover:default"],"recurrence":{"byMonthDay":15,"freq":"monthly","until":"2026-12-31T00:00:00Z"},"timeZone":"Europe/London",` +
		`"work":{"repo":"dev-env","template":"ops-interactive"}}`
	if got := mustJSON(t, edited); got != want {
		t.Errorf("edit body\n got %s\nwant %s", got, want)
	}

	// An explicitly empty title is sent, for the API to judge.
	h.mustRun(ExitOK, "schedule", "edit", view.Name, "--title", "")
	if v, ok := edited["title"]; !ok || v != "" || len(edited) != 1 {
		t.Errorf("an explicit empty title: %v", edited)
	}
	// A current approval draws no note.
	view.Approval.SpecHash = testSpecHash
	h.mustRun(ExitOK, "schedule", "edit", view.Name, "--repo", "dev-env", "-o", "json")
	if strings.Contains(h.stdout.String(), "no longer applies") {
		t.Errorf("json output carried text: %s", h.stdout.String())
	}
}

func TestScheduleEditUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no name", []string{"edit"}, "schedule edit takes one name"},
		{"nothing to change", []string{"edit", testWorkName}, "schedule edit changes something"},
		{"once and repeat", []string{"edit", testWorkName, "--once", "--repeat", "daily"}, "--once makes an item one-shot"},
		{"no-notify and notify", []string{"edit", testWorkName, "--no-notify", "--notify", "pushover:default"}, "--no-notify removes every notify target"},
		{"no-calendar and calendar", []string{"edit", testWorkName, "--no-calendar", "--calendar", "calendar:haynesnetwork"}, "--no-calendar removes the calendar entry"},
		{"interactive without notify", []string{"edit", testWorkName, "--mode", "interactive", "--no-notify"}, "interactive work needs --notify"},
		{"bad tz", []string{"edit", testWorkName, "--tz", "Nowhere/City"}, `--tz "Nowhere/City" is not an IANA zone`},
		{"empty tz", []string{"edit", testWorkName, "--tz", ""}, `--tz "" is not an IANA zone`},
		{"bad due", []string{"edit", testWorkName, "--due", "tomorrow"}, `--due "tomorrow" is not a local time`},
		{"bad mode", []string{"edit", testWorkName, "--mode", "Headless"}, `not "Headless"`},
		{"bad repeat", []string{"edit", testWorkName, "--repeat", "weekly:FUN"}, `not "FUN"`},
		{"bad until", []string{"edit", testWorkName, "--repeat", "daily", "--until", "soon"}, `--until "soon"`},
		{"past not-after", []string{"edit", testWorkName, "--not-after", "2026-10-06T21:00:00Z"}, "has passed"},
		{"bad notify", []string{"edit", testWorkName, "--notify", "pushover:Default"}, "--notify takes pushover:<ref>"},
		{"bad calendar", []string{"edit", testWorkName, "--calendar", "calendar:"}, "--calendar takes calendar:<ref>"},
		{"prompt and prompt-file", []string{"edit", testWorkName, "--prompt", "a", "--prompt-file", "-"}, "not both"},
		{"empty prompt", []string{"edit", testWorkName, "--prompt", ""}, "the work order is empty"},
		{"key is for create", []string{"edit", testWorkName, "--key", "k"}, "flag provided but not defined: -key"},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			h := newHarness(t)
			refuseAll(t, h)
			if code := h.run(append([]string{"schedule"}, tc.args...)...); code != ExitUsage {
				t.Errorf("exit %d, want %d (usage)\nstderr: %s", code, ExitUsage, h.stderr.String())
			}
			contains(t, "stderr", h.stderr.String(), tc.want)
		})
	}
}

func TestScheduleCancel(t *testing.T) {
	h := newHarness(t)
	view := testScheduledWork()
	view.State, view.Spec.Cancelled, view.Warning = apiv1.ScheduledWorkCancelled, true, "occurrence 2026-10-06T20:00:00Z is still running"
	view.Occurrences = append(view.Occurrences,
		apiv1.ScheduledWorkOccurrence{At: testNow.Add(-time.Hour), State: apiv1.ScheduledOccurrenceRunning, Session: "sw-0123456789ab-202610062000"},
		apiv1.ScheduledWorkOccurrence{At: testNow.Add(-30 * time.Minute), State: apiv1.ScheduledOccurrenceFailed, Session: "sw-0123456789ab-202610062030"})
	var calls int
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.ScheduledWorkCancelPath(view.Name) || len(body) != 0 {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
			return
		}
		calls++
		writeTestJSON(w, http.StatusOK, view)
	}
	h.mustRun(ExitOK, "schedule", "cancel", view.Name)
	out := h.stdout.String()
	contains(t, "stdout", out, "cancelled sched-0123456789abcdef0123: no later occurrence runs",
		"session sw-0123456789ab-202610062000 (occurrence 2026-10-06T20:00:00Z, Running) keeps running; a cancel does not stop it: agent-run reap sw-0123456789ab-202610062000 stops it",
		"WARNING: occurrence 2026-10-06T20:00:00Z is still running")
	if strings.Contains(out, "202610062030") || strings.Contains(out, "202609291300") {
		t.Errorf("a finished occurrence's session was offered for reaping:\n%s", out)
	}
	h.mustRun(ExitOK, "schedule", "cancel", view.Name, "-o", "json")
	if calls != 2 || !json.Valid(h.stdout.Bytes()) {
		t.Errorf("calls %d, json %q", calls, h.stdout.String())
	}
}

// decisionAPI serves an item for approve and retry and records the decision.
type decisionAPI struct {
	view           apiv1.ScheduledWork
	raw            []byte // the GET answer when set, instead of view
	approve, retry []byte
	answer         func(w http.ResponseWriter) bool // a refusal instead of the decision when it returns true
}

func (d *decisionAPI) handle(t *testing.T, h *harness) {
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.ScheduledWorkItemPath(d.view.Name):
			if d.raw != nil {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(d.raw)
				return
			}
			writeTestJSON(w, http.StatusOK, d.view)
		case r.Method == http.MethodPost && (r.URL.Path == apiv1.ScheduledWorkApprovePath(d.view.Name) || r.URL.Path == apiv1.ScheduledWorkRetryPath(d.view.Name)):
			if strings.HasSuffix(r.URL.Path, "/approve") {
				d.approve = body
			} else {
				d.retry = body
			}
			if d.answer != nil && d.answer(w) {
				return
			}
			// The answer: the approval recorded and the named occurrence claimed.
			res := d.view
			res.Approval = &apiv1.ScheduledWorkApproval{By: "human/dev-env-system/tom", At: testNow, SpecHash: d.view.SpecHash}
			var named struct {
				At *time.Time `json:"at"`
			}
			_ = json.Unmarshal(body, &named)
			res.Occurrences = slices.Clone(d.view.Occurrences)
			for i := range res.Occurrences {
				if named.At != nil && res.Occurrences[i].At.Equal(*named.At) {
					res.Occurrences[i].State, res.Occurrences[i].Reason = apiv1.ScheduledOccurrenceClaimed, ""
				}
			}
			writeTestJSON(w, http.StatusOK, res)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
		}
	}
}

func (d *decisionAPI) posts() int {
	n := 0
	if d.approve != nil {
		n++
	}
	if d.retry != nil {
		n++
	}
	return n
}

var shownHash = regexp.MustCompile(`(?m)^  spec hash:  ([0-9a-f]{64})$`)

// yes are the flags of a non-interactive decision: --yes needs the hash read.
var scheduleYes = []string{"--yes", "--spec-hash", strings.Repeat("ab", 32)}

func decideArgs(verb string, extra ...string) []string {
	return append(append([]string{"schedule", verb, testWorkName}, scheduleYes...), extra...)
}

func TestScheduleApprove(t *testing.T) {
	t.Run("yes with spec-hash and at sends both", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		h.mustRun(ExitOK, decideArgs("approve", "--at", "2026-10-06T19:00:00Z")...)
		out := h.stdout.String()
		req := decodeAs[apiv1.ApproveScheduledWorkRequest](t, d.approve)
		if req.SpecHash != testSpecHash || req.At == nil || !req.At.Equal(testParkedAt) {
			t.Errorf("approve request %+v", req)
		}
		contains(t, "stdout", out, "You are approving this exact spec of sched-0123456789abcdef0123:",
			"title:      Loki retention trend audit", "    | Audit Loki retention.", "notify:     pushover:default", "calendar:   calendar:haynesnetwork",
			"spec hash:  "+testSpecHash, "approval:   VOID: by human/dev-env-system/tom",
			"Parked occurrence 2026-10-06T19:00:00Z: no standing authority; attempt 1 of at most 3",
			"The approval binds to spec hash "+testSpecHash, "Approving also runs the occurrence at 2026-10-06T19:00:00Z again now",
			"approved sched-0123456789abcdef0123: spec hash "+testSpecHash,
			"the occurrence at 2026-10-06T19:00:00Z is now Claimed; agent-run schedule show sched-0123456789abcdef0123 follows it")
		if strings.Contains(h.stderr.String(), "[y/N]") {
			t.Errorf("--yes still asked: %s", h.stderr.String())
		}
	})
	t.Run("approve retries a parked occurrence only when --at names it", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		h.mustRun(ExitOK, decideArgs("approve")...)
		var keys map[string]any
		if err := json.Unmarshal(d.approve, &keys); err != nil || len(keys) != 1 || keys["specHash"] != testSpecHash {
			t.Errorf("approve body %s: %v", d.approve, err)
		}
		contains(t, "stdout", h.stdout.String(), "Parked occurrence 2026-10-06T19:00:00Z stays parked (no standing authority; attempt 1 of at most 3): add --at 2026-10-06T19:00:00Z to retry it with this approval")
		if strings.Contains(h.stdout.String(), "is now") {
			t.Errorf("reported a retry it did not ask for:\n%s", h.stdout.String())
		}
	})
	t.Run("yes needs spec-hash", func(t *testing.T) {
		h := newHarness(t)
		refuseAll(t, h)
		h.mustRun(ExitUsage, "schedule", "approve", testWorkName, "--yes")
		contains(t, "stderr", h.stderr.String(), "--yes needs --spec-hash, the hash you read: run agent-run schedule show sched-0123456789abcdef0123, then agent-run schedule approve sched-0123456789abcdef0123 --spec-hash <hash> --yes. Nothing was sent")
	})
	t.Run("no terminal and no yes refuses before anything is sent", func(t *testing.T) {
		h := newHarness(t)
		refuseAll(t, h)
		h.mustRun(ExitUsage, "schedule", "approve", testWorkName, "--spec-hash", testSpecHash)
		contains(t, "stderr", h.stderr.String(), "schedule approve asks y/N on a terminal, and there is none. Run agent-run schedule show sched-0123456789abcdef0123, then agent-run schedule approve sched-0123456789abcdef0123 --spec-hash <hash> --yes. Nothing was sent")
	})
	for _, answer := range []string{"y\n", "Yes\n", " y "} {
		t.Run("terminal agrees "+strings.TrimSpace(answer), func(t *testing.T) {
			h := newHarness(t)
			withTerminal(t, h, answer)
			d := &decisionAPI{view: testScheduledWork()}
			d.handle(t, h)
			h.mustRun(ExitOK, "schedule", "approve", testWorkName)
			stderr := h.stderr.String()
			// The whole decision is on the stream the question is on.
			contains(t, "stderr", stderr, "You are approving this exact spec", "    | Open a PR with the trend.", "Approve spec "+testSpecHash+" of sched-0123456789abcdef0123? [y/N] ")
			if strings.Index(stderr, "You are approving") > strings.Index(stderr, "[y/N]") {
				t.Errorf("the question came before the spec:\n%s", stderr)
			}
			if strings.Contains(h.stdout.String(), "You are approving") {
				t.Errorf("the decision went to stdout:\n%s", h.stdout.String())
			}
			m := shownHash.FindStringSubmatch(stderr)
			req := decodeAs[apiv1.ApproveScheduledWorkRequest](t, d.approve)
			if m == nil || req.SpecHash != m[1] || req.At != nil {
				t.Errorf("request %+v, shown %v", req, m)
			}
		})
	}
	for name, answer := range map[string]string{"no": "n\n", "empty": "\n", "eof": "", "other": "approve\n", "yess": "yess\n"} {
		t.Run("terminal declines "+name, func(t *testing.T) {
			h := newHarness(t)
			withTerminal(t, h, answer)
			d := &decisionAPI{view: testScheduledWork()}
			d.handle(t, h)
			h.mustRun(ExitFailed, "schedule", "approve", testWorkName)
			contains(t, "stderr", h.stderr.String(), "[y/N]", "not approved; no approval was sent")
			if d.posts() != 0 {
				t.Errorf("a declined approval was sent: %s", d.approve)
			}
		})
	}
	t.Run("spec-hash pins what was seen", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		h.mustRun(ExitFailed, "schedule", "approve", testWorkName, "--yes", "--spec-hash", testOldHash)
		contains(t, "stderr", h.stderr.String(), "changed after you saw it: its hash is now "+testSpecHash+", not "+testOldHash, "No approval was sent")
		if d.posts() != 0 {
			t.Errorf("a stale approval was sent: %s", d.approve)
		}
		// On a terminal too.
		withTerminal(t, h, "y\n")
		h.mustRun(ExitFailed, "schedule", "approve", testWorkName, "--spec-hash", testOldHash)
		if d.posts() != 0 {
			t.Errorf("a stale approval was sent: %s", d.approve)
		}
	})
	t.Run("bad spec-hash", func(t *testing.T) {
		h := newHarness(t)
		refuseAll(t, h)
		h.mustRun(ExitUsage, "schedule", "approve", testWorkName, "--yes", "--spec-hash", strings.ToUpper(testSpecHash))
		contains(t, "stderr", h.stderr.String(), "--spec-hash is the 64 lowercase hex characters")
	})
	t.Run("a spec field this agent-run cannot show", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		var doc map[string]any
		if err := json.Unmarshal([]byte(mustJSON(t, d.view)), &doc); err != nil {
			t.Fatal(err)
		}
		doc["spec"].(map[string]any)["work"].(map[string]any)["profile"] = "full"
		doc["templateDigest"] = "elsewhere is fine"
		d.raw = []byte(mustJSON(t, doc))
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("approve")...)
		contains(t, "stderr", h.stderr.String(), `has a field this agent-run cannot show (json: unknown field "profile")`, "upgrade agent-run")
		if d.posts() != 0 {
			t.Errorf("approved a spec it could not show: %s", d.approve)
		}
		// A field outside the spec is not part of what the hash covers.
		delete(doc["spec"].(map[string]any)["work"].(map[string]any), "profile")
		d.raw = []byte(mustJSON(t, doc))
		h.mustRun(ExitOK, decideArgs("approve")...)
	})
	t.Run("no spec", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork(), raw: []byte(`{"name":"` + testWorkName + `","specHash":"` + testSpecHash + `","spec":null}`)}
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("approve")...)
		contains(t, "stderr", h.stderr.String(), "the API showed no spec")
		if d.posts() != 0 {
			t.Error("approved with no spec")
		}
	})
	for name, hash := range map[string]string{"absent": "", "short": "abc", "upper": strings.ToUpper(testSpecHash)} {
		t.Run("view spec hash "+name, func(t *testing.T) {
			h := newHarness(t)
			d := &decisionAPI{view: testScheduledWork()}
			d.view.SpecHash = hash
			d.handle(t, h)
			h.mustRun(ExitFailed, decideArgs("approve")...)
			contains(t, "stderr", h.stderr.String(), "the API gave no spec hash")
			if d.posts() != 0 {
				t.Error("approved without a hash")
			}
		})
	}
	t.Run("another name in the answer", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		other := testScheduledWork()
		other.Name = "sched-other"
		d.raw = []byte(mustJSON(t, other))
		h.mustRun(ExitFailed, decideArgs("approve")...)
		contains(t, "stderr", h.stderr.String(), `the API answered for "sched-other", not "sched-0123456789abcdef0123"; no approval was sent`)
		if d.posts() != 0 {
			t.Error("approved another item")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.view.Spec.Cancelled = true
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("approve")...)
		contains(t, "stderr", h.stderr.String(), "is cancelled; there is nothing to approve")
		if d.posts() != 0 {
			t.Error("approved a cancelled item")
		}
	})
	t.Run("several parked occurrences", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		other := testNow.Add(-90 * time.Minute)
		d.view.Occurrences = append(d.view.Occurrences, apiv1.ScheduledWorkOccurrence{At: other, State: apiv1.ScheduledOccurrenceParked, Reason: "template missing"})
		d.handle(t, h)
		h.mustRun(ExitOK, decideArgs("approve")...)
		contains(t, "stdout", h.stdout.String(), "Parked occurrence 2026-10-06T19:00:00Z stays parked", "Parked occurrence 2026-10-06T19:30:00Z stays parked (template missing")
		if req := decodeAs[apiv1.ApproveScheduledWorkRequest](t, d.approve); req.At != nil {
			t.Errorf("request %+v", req)
		}
		h.mustRun(ExitOK, decideArgs("approve", "--at", "2026-10-06T19:30:00Z")...)
		if req := decodeAs[apiv1.ApproveScheduledWorkRequest](t, d.approve); req.At == nil || !req.At.Equal(other) {
			t.Errorf("request %+v", req)
		}
	})
	t.Run("an occurrence at the attempt limit is not retried", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.view.Occurrences[2].Attempt = apiv1.MaxScheduledWorkAttempt
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("approve", "--at", "2026-10-06T19:00:00Z")...)
		contains(t, "stderr", h.stderr.String(), "the occurrence of sched-0123456789abcdef0123 at 2026-10-06T19:00:00Z has used its 3 retries; a fourth is refused. No approval was sent")
		if d.posts() != 0 {
			t.Error("retried past the attempt limit")
		}
		// The spec alone can still be approved.
		h.mustRun(ExitOK, decideArgs("approve")...)
		contains(t, "stdout", h.stdout.String(), "stays parked (no standing authority; attempt 3 of at most 3): it has used its retries")
	})
	t.Run("a parked occurrence claimed with an earlier spec", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.view.Occurrences[2].SpecHash = testOldHash
		d.handle(t, h)
		h.mustRun(ExitOK, decideArgs("approve", "--at", "2026-10-06T19:00:00Z")...)
		contains(t, "stdout", h.stdout.String(), "It was claimed with an earlier spec (hash "+testOldHash+"), so the API skips it instead of running it.")
	})
	t.Run("json keeps stdout for the answer", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		h.mustRun(ExitOK, decideArgs("approve", "-o", "json")...)
		var res apiv1.ScheduledWork
		if err := json.Unmarshal(h.stdout.Bytes(), &res); err != nil || res.Approval == nil || res.Approval.SpecHash != testSpecHash {
			t.Errorf("json %q: %v", h.stdout.String(), err)
		}
		contains(t, "stderr", h.stderr.String(), "You are approving this exact spec", "spec hash:  "+testSpecHash)
	})
	t.Run("an answer for another item after the write", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork(), answer: func(w http.ResponseWriter) bool {
			other := testScheduledWork()
			other.Name = "sched-other"
			writeTestJSON(w, http.StatusOK, other)
			return true
		}}
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("approve")...)
		contains(t, "stderr", h.stderr.String(), `the API's answer names "sched-other", not "sched-0123456789abcdef0123"; the approval may have taken effect: check agent-run schedule show sched-0123456789abcdef0123`)
	})
	t.Run("a lost answer is not repeated", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork(), answer: func(w http.ResponseWriter) bool {
			apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "synthetic")
			return true
		}}
		d.handle(t, h)
		h.mustRun(ExitUnconfirmed, decideArgs("approve")...)
		posts := 0
		for _, r := range h.api.requests() {
			if r.method == http.MethodPost {
				posts++
			}
		}
		if posts != 1 || len(h.sleeps) != 0 {
			t.Errorf("%d posts, sleeps %v", posts, h.sleeps)
		}
		contains(t, "stderr", h.stderr.String(), "do not repeat it")
	})
}

// TestScheduleStdinTerminal: /dev/null, a file and a pipe are not terminals,
// though /dev/null is a character device.
func TestScheduleStdinTerminal(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pr.Close(); _ = pw.Close() }()
	for name, r := range map[string]io.Reader{"/dev/null": null, "file": file, "pipe": pr, "reader": strings.NewReader("y\n")} {
		if scheduleStdinIsTerminal(r) {
			t.Errorf("%s counts as a terminal", name)
		}
	}
}

// TestScheduleDecisionWithoutTerminal: approve and retry from /dev/null, a file
// that says y, or a pipe that says y refuse before any request, and run
// nothing.
func TestScheduleDecisionWithoutTerminal(t *testing.T) {
	stdins := map[string]func(t *testing.T) *os.File{
		"dev-null": func(t *testing.T) *os.File {
			f, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			return f
		},
		"file-y": func(t *testing.T) *os.File {
			f, err := os.CreateTemp(t.TempDir(), "stdin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("y\n"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			return f
		},
		"pipe-y": func(t *testing.T) *os.File {
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pw.WriteString("y\n"); err != nil {
				t.Fatal(err)
			}
			_ = pw.Close()
			return pr
		},
	}
	for _, verb := range []string{"approve", "retry"} {
		for name, open := range stdins {
			t.Run(verb+"/"+name, func(t *testing.T) {
				h := newHarness(t)
				refuseAll(t, h)
				f := open(t)
				defer func() { _ = f.Close() }()
				h.env.Stdin = f
				h.mustRun(ExitUsage, "schedule", verb, testWorkName)
				contains(t, "stderr", h.stderr.String(), "Nothing was sent")
				if n := len(h.api.requests()); n != 0 || len(h.ran) != 0 {
					t.Errorf("%d requests, ran %v", n, h.ran)
				}
			})
		}
	}
}

func TestScheduleRetry(t *testing.T) {
	t.Run("yes sends the parked occurrence and the hash", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		h.mustRun(ExitOK, decideArgs("retry")...)
		out := h.stdout.String()
		req := decodeAs[apiv1.RetryScheduledWorkRequest](t, d.retry)
		if req.SpecHash != testSpecHash || !req.At.Equal(testParkedAt) {
			t.Errorf("retry request %+v", req)
		}
		if d.approve != nil {
			t.Error("a retry sent an approval")
		}
		contains(t, "stdout", out, "You are retrying an occurrence of sched-0123456789abcdef0123, with this spec:",
			"Parked occurrence 2026-10-06T19:00:00Z: no standing authority; attempt 1 of at most 3",
			"grants no authority", "retried sched-0123456789abcdef0123", "the occurrence at 2026-10-06T19:00:00Z is now Claimed")
	})
	t.Run("terminal", func(t *testing.T) {
		h := newHarness(t)
		withTerminal(t, h, "n\n")
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		h.mustRun(ExitFailed, "schedule", "retry", testWorkName)
		contains(t, "stderr", h.stderr.String(), "You are retrying an occurrence", "Retry the occurrence of sched-0123456789abcdef0123 at 2026-10-06T19:00:00Z now? [y/N] ", "not retried; no retry was sent")
		if d.posts() != 0 {
			t.Error("a declined retry was sent")
		}
		h.env.Stdin = strings.NewReader("y\n")
		h.mustRun(ExitOK, "schedule", "retry", testWorkName)
		m := shownHash.FindStringSubmatch(h.stderr.String())
		if req := decodeAs[apiv1.RetryScheduledWorkRequest](t, d.retry); m == nil || req.SpecHash != m[1] {
			t.Errorf("an agreed retry sent %+v, shown %v", req, m)
		}
	})
	t.Run("yes needs spec-hash", func(t *testing.T) {
		h := newHarness(t)
		refuseAll(t, h)
		h.mustRun(ExitUsage, "schedule", "retry", testWorkName, "--yes")
		contains(t, "stderr", h.stderr.String(), "then agent-run schedule retry sched-0123456789abcdef0123 --spec-hash <hash> --yes. Nothing was sent")
	})
	t.Run("spec-hash pins what was seen", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.handle(t, h)
		h.mustRun(ExitFailed, "schedule", "retry", testWorkName, "--yes", "--spec-hash", testOldHash)
		contains(t, "stderr", h.stderr.String(), "changed after you saw it", "No retry was sent")
		if d.posts() != 0 {
			t.Error("a stale retry was sent")
		}
	})
	t.Run("nothing parked", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.view.Occurrences = d.view.Occurrences[:2]
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("retry")...)
		contains(t, "stderr", h.stderr.String(), "has no parked occurrence, so there is nothing to retry")
		if d.posts() != 0 {
			t.Error("retried nothing")
		}
	})
	t.Run("several parked need --at", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.view.Occurrences = append(d.view.Occurrences, apiv1.ScheduledWorkOccurrence{At: testNow.Add(-90 * time.Minute), State: apiv1.ScheduledOccurrenceParked})
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("retry")...)
		contains(t, "stderr", h.stderr.String(), "has 2 parked occurrences (2026-10-06T19:00:00Z, 2026-10-06T19:30:00Z); name one with --at")
		if d.posts() != 0 {
			t.Error("retried without choosing")
		}
	})
	t.Run("the attempt limit", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.view.Occurrences[2].Attempt = apiv1.MaxScheduledWorkAttempt
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("retry")...)
		contains(t, "stderr", h.stderr.String(), "has used its 3 retries; a fourth is refused. No retry was sent")
		if d.posts() != 0 {
			t.Error("retried past the attempt limit")
		}
	})
	t.Run("--at", func(t *testing.T) {
		h := newHarness(t)
		d := &decisionAPI{view: testScheduledWork()}
		d.view.Occurrences[0].State = apiv1.ScheduledOccurrenceSucceeded + "\x1b[2J"
		d.handle(t, h)
		h.mustRun(ExitFailed, decideArgs("retry", "--at", "2026-09-29T21:00:00Z")...)
		contains(t, "stderr", h.stderr.String(), `is Succeeded\u001b[2J, not Parked`)
		if strings.Contains(h.stderr.String(), "\x1b") {
			t.Errorf("a control character reached the terminal: %q", h.stderr.String())
		}
		h.mustRun(ExitFailed, decideArgs("retry", "--at", "2026-09-29T21:01:00Z")...)
		contains(t, "stderr", h.stderr.String(), "has no occurrence at 2026-09-29T21:01:00Z")
		if d.posts() != 0 {
			t.Error("retried an occurrence that is not parked")
		}
		h.mustRun(ExitUsage, decideArgs("retry", "--at", "yesterday")...)
		contains(t, "stderr", h.stderr.String(), `--at "yesterday" is not an RFC 3339 instant`)
		h.mustRun(ExitOK, decideArgs("retry", "--at", "2026-10-06T15:00:00-04:00")...)
		if req := decodeAs[apiv1.RetryScheduledWorkRequest](t, d.retry); !req.At.Equal(testParkedAt) {
			t.Errorf("request %+v", req)
		}
	})
}

// TestScheduleRefusals: 400, 403, 404 and 409 keep the refusal's exit code and
// say what to do next.
func TestScheduleRefusals(t *testing.T) {
	approve, retry := decideArgs("approve")[1:], decideArgs("retry")[1:]
	cases := []struct {
		name   string
		args   []string
		status int
		code   string
		exit   int
		want   []string
	}{
		{"approve 403", approve, 403, apiv1.CodeForbidden, ExitAuth,
			[]string{"not allowed (403 forbidden): only a human", "approve and retry are for a human (Tom) only; a session, client or coordinator cannot approve"}},
		{"retry 403", retry, 403, apiv1.CodeForbidden, ExitAuth, []string{"cannot retry"}},
		{"approve 409", approve, 409, apiv1.CodeConflict, ExitFailed,
			[]string{"(409 conflict): the spec hash does not match",
				"the API refuses an approval when the item is cancelled, the occurrence named by --at is no longer Parked or has used its 3 retries, or the spec changed after you saw it; read it again with agent-run schedule show sched-0123456789abcdef0123"}},
		{"retry 409", retry, 409, apiv1.CodeConflict, ExitFailed,
			[]string{"the API refuses a retry when the item is cancelled, the occurrence is no longer Parked or has used its 3 retries, or the spec changed after you saw it; agent-run schedule show sched-0123456789abcdef0123 shows which"}},
		{"edit 403", []string{"edit", testWorkName, "--title", "x"}, 403, apiv1.CodeForbidden, ExitAuth, []string{"only the owner or a human may edit scheduled work; a coordinator may cancel it"}},
		{"edit 409", []string{"edit", testWorkName, "--title", "x"}, 409, apiv1.CodeConflict, ExitFailed, []string{"shows where it stands now"}},
		{"cancel 403", []string{"cancel", testWorkName}, 403, apiv1.CodeForbidden, ExitAuth, []string{"only the owner, a human or a coordinator may cancel"}},
		{"show 404", []string{"show", testWorkName}, 404, apiv1.CodeNotFound, ExitNotFound,
			[]string{"(404 not_found)", "agent-run schedule list shows your scheduled work; an operator that does not serve the scheduled-work API answers 404 on every route"}},
		{"list 403", []string{"list", "--all"}, 403, apiv1.CodeForbidden, ExitAuth, []string{"list --all is for a human or a coordinator"}},
		{"create 409", scheduleCreateArgs("--key", "loki")[1:], 409, apiv1.CodeConflict, ExitFailed,
			[]string{"a --key names one item of yours", "the create used key loki: run the same command with --key loki to retry it without making a second item"}},
		{"create 400", scheduleCreateArgs()[1:], 400, apiv1.CodeBadRequest, ExitFailed,
			[]string{"(400 bad_request)", "agent-run and the operator may be different versions", "the create used key agent-run-testkey"}},
		{"create 422", scheduleCreateArgs()[1:], 422, apiv1.CodeInvalid, ExitFailed,
			[]string{"--template (work.template): no such template", "--prompt or --prompt-file (work.prompt): too long", "--not-after (notAfter): more than 180 days ahead",
				"--tz (timeZone): unknown zone", "--due (due): in the past"}},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			h := newHarness(t)
			view := testScheduledWork()
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.Method == http.MethodGet && r.URL.Path == apiv1.ScheduledWorkItemPath(testWorkName) && tc.name[:4] != "show" {
					writeTestJSON(w, http.StatusOK, view)
					return
				}
				msg := map[int]string{403: "only a human", 409: "the spec hash does not match", 404: "no scheduled work " + testWorkName, 400: "unknown field", 422: "invalid"}[tc.status]
				var fields []apiv1.FieldError
				if tc.status == 422 {
					fields = []apiv1.FieldError{{Field: "work.template", Message: "no such template"}, {Field: "work.prompt", Message: "too long"},
						{Field: "notAfter", Message: "more than 180 days ahead"}, {Field: "timeZone", Message: "unknown zone"}, {Field: "due", Message: "in the past"}}
				}
				apiError(w, tc.status, tc.code, msg, fields...)
			}
			h.mustRun(tc.exit, append([]string{"schedule"}, tc.args...)...)
			contains(t, "stderr", h.stderr.String(), tc.want...)
		})
	}
}

// TestScheduleCreateRetriedThenConflict: a 409 after agent-run's own retry
// says the first attempt may have made the item.
func TestScheduleCreateRetriedThenConflict(t *testing.T) {
	h := newHarness(t)
	calls := 0
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		calls++
		if calls == 1 {
			apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "synthetic")
			return
		}
		apiError(w, http.StatusConflict, apiv1.CodeConflict, "key names a different item of yours")
	}
	h.mustRun(ExitFailed, scheduleCreateArgs("--key", "loki")...)
	contains(t, "stderr", h.stderr.String(), "agent-run sent this create again after an attempt it could not confirm; the first attempt may have made the item, so check agent-run schedule list before you create it again",
		"the create used key loki")
	if calls != 2 {
		t.Errorf("%d calls", calls)
	}
	// Without a retry, no such note.
	calls = 1
	h.mustRun(ExitFailed, scheduleCreateArgs("--key", "loki")...)
	if strings.Contains(h.stderr.String(), "sent this create again") {
		t.Errorf("a first-try 409 claimed a retry: %s", h.stderr.String())
	}
}

// TestScheduleEditIsSentOnce: an edit has no precondition, so a lost answer is
// reported, not repeated.
func TestScheduleEditIsSentOnce(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "synthetic")
	}
	h.mustRun(ExitUnconfirmed, "schedule", "edit", testWorkName, "--title", "x")
	if n := len(h.api.requests()); n != 1 || len(h.sleeps) != 0 {
		t.Errorf("%d requests, sleeps %v", n, h.sleeps)
	}
	contains(t, "stderr", h.stderr.String(), "do not repeat it")
}

// TestScheduleAnswerNamesTheItem: show, edit and cancel refuse an answer for
// another item.
func TestScheduleAnswerNamesTheItem(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"show", testWorkName}, `the API answered for "sched-other", not "sched-0123456789abcdef0123"`},
		{[]string{"edit", testWorkName, "--title", "x"}, `the API's answer names "sched-other", not "sched-0123456789abcdef0123"; the edit may have taken effect: check agent-run schedule show sched-0123456789abcdef0123`},
		{[]string{"cancel", testWorkName}, `the API's answer names "sched-other", not "sched-0123456789abcdef0123"; the cancel may have taken effect`},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			h := newHarness(t)
			other := testScheduledWork()
			other.Name = "sched-other"
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, other) }
			h.mustRun(ExitFailed, append([]string{"schedule"}, tc.args...)...)
			contains(t, "stderr", h.stderr.String(), tc.want)
		})
	}
}

func TestScheduleNotifyLimits(t *testing.T) {
	nine := []string{}
	for i := range 9 {
		nine = append(nine, "--notify", fmt.Sprintf("pushover:t%d", i))
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"create twice", scheduleCreateArgs("--notify", "pushover:default", "--notify", "pushover:default"), "--notify pushover:default is given twice"},
		{"create nine", scheduleCreateArgs(nine...), "--notify takes at most 8 targets, not 9"},
		{"edit twice", []string{"schedule", "edit", testWorkName, "--notify", "pushover:a", "--notify", "pushover:a"}, "--notify pushover:a is given twice"},
		{"edit nine", append([]string{"schedule", "edit", testWorkName}, nine...), "--notify takes at most 8 targets, not 9"},
	} {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			h := newHarness(t)
			refuseAll(t, h)
			h.mustRun(ExitUsage, tc.args...)
			contains(t, "stderr", h.stderr.String(), tc.want)
		})
	}
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusCreated, testScheduledWork())
	}
	h.mustRun(ExitOK, scheduleCreateArgs(nine[:16]...)...)
}

// TestScheduleShowMarksThePromptEnd: the prompt's last line is followed by a
// marker, so trailing lines cannot pass for other fields.
func TestScheduleShowMarksThePromptEnd(t *testing.T) {
	h := newHarness(t)
	view := testScheduledWork()
	view.Spec.Work.Prompt = "Do it.\n  approval:   by human/dev-env-system/tom\n"
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, view) }
	h.mustRun(ExitOK, "schedule", "show", view.Name)
	contains(t, "show", h.stdout.String(), "    | Do it.\n    |   approval:   by human/dev-env-system/tom\n    | \n  end of prompt (49 bytes)\n  approval:   VOID")
}

// TestScheduleShownEscapesInvisibleText: characters that render as nothing
// are escaped too.
func TestScheduleShownEscapesInvisibleText(t *testing.T) {
	for _, r := range []rune{0xFE0F, 0xE0100, 0x180B, 0x180C, 0x180D, 0x034F, 0x115F, 0x1160, 0x3164, 0xFFA0, 0x2065, 0x200B} {
		in := "a" + string(r) + "b"
		got := shown(in, false)
		want := fmt.Sprintf(`a\u%04xb`, r)
		if r > 0xffff {
			want = fmt.Sprintf(`a\U%08xb`, r)
		}
		if got != want {
			t.Errorf("shown(%U) = %q, want %q", r, got, want)
		}
	}
	if got := shown("plain é 你好 -> ok", false); got != "plain é 你好 -> ok" {
		t.Errorf("printable text changed: %q", got)
	}
	// A listed name is escaped.
	h := newHarness(t)
	view := testScheduledWork()
	view.Name = "sched-\x1b[2J"
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.ScheduledWorkList{Items: []apiv1.ScheduledWork{view}})
	}
	h.mustRun(ExitOK, "schedule", "list")
	if strings.Contains(h.stdout.String(), "\x1b") {
		t.Errorf("a control character reached the terminal: %q", h.stdout.String())
	}
}

// TestScheduleSpecDisplayCoversEveryField: printScheduleSpec shows every
// field of the spec, its work order and its recurrence, so an approval covers
// only what was shown. A new field fails here until it is displayed and listed.
func TestScheduleSpecDisplayCoversEveryField(t *testing.T) {
	want := map[reflect.Type][]string{
		reflect.TypeOf(apiv1.ScheduledWorkSpec{}):  {"title", "owner", "editor", "due", "timeZone", "recurrence", "notAfter", "mode", "work", "notify", "calendar", "cancelled"},
		reflect.TypeOf(apiv1.ScheduledWorkOrder{}): {"template", "repo", "prompt"},
		reflect.TypeOf(apiv1.ReminderRecurrence{}): {"freq", "byDay", "byMonthDay", "until", "count"},
	}
	var spec apiv1.ScheduledWorkSpec
	var sentinels []string
	n := 0
	var fill func(v reflect.Value)
	fill = func(v reflect.Value) {
		tp := v.Type()
		names, ok := want[tp]
		if !ok {
			t.Fatalf("no field list for %s", tp)
		}
		var got []string
		for i := range tp.NumField() {
			f := tp.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			got = append(got, name)
			fv := v.Field(i)
			n++
			switch {
			case f.Type == reflect.TypeOf(time.Time{}):
				at := time.Date(2030, 1, n, 0, 0, 0, 0, time.UTC)
				fv.Set(reflect.ValueOf(at))
				sentinels = append(sentinels, at.Format(time.RFC3339))
			case f.Type == reflect.TypeOf(&time.Time{}):
				at := time.Date(2031, 1, n, 0, 0, 0, 0, time.UTC)
				fv.Set(reflect.ValueOf(&at))
				sentinels = append(sentinels, at.Format(time.RFC3339))
			case f.Type.Kind() == reflect.String:
				s := fmt.Sprintf("sentinel-%s-%d", name, n)
				fv.SetString(s)
				sentinels = append(sentinels, s)
			case f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.String:
				s := fmt.Sprintf("sentinel-%s-%d", name, n)
				fv.Set(reflect.ValueOf([]string{s}))
				sentinels = append(sentinels, s)
			case f.Type.Kind() == reflect.Int:
				fv.SetInt(int64(900 + n))
				sentinels = append(sentinels, fmt.Sprint(900+n))
			case f.Type.Kind() == reflect.Bool:
				fv.SetBool(true)
				sentinels = append(sentinels, name+":  yes")
			case f.Type.Kind() == reflect.Struct:
				fill(fv)
			case f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct:
				fv.Set(reflect.New(f.Type.Elem()))
				fill(fv.Elem())
			default:
				t.Fatalf("%s.%s: no sentinel for %s", tp, f.Name, f.Type)
			}
		}
		if !slices.Equal(got, names) {
			t.Errorf("%s fields %v, the display list has %v: show the new field in printScheduleSpec, then list it here", tp, got, names)
		}
	}
	fill(reflect.ValueOf(&spec).Elem())
	var b strings.Builder
	printScheduleSpec(&b, apiv1.ScheduledWork{Spec: spec, SpecHash: testSpecHash})
	for _, s := range sentinels {
		if !strings.Contains(b.String(), s) {
			t.Errorf("printScheduleSpec does not show %q:\n%s", s, b.String())
		}
	}
}

func TestScheduleHelp(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "help", "schedule")
	help := strings.Join(strings.Fields(h.stdout.String()), " ")
	contains(t, "help", help, "agent-run schedule create",
		"agent-run schedule approve <name> [--at <time>] [--spec-hash <hash> --yes]",
		"agent-run schedule retry <name> [--at <time>] [--spec-hash <hash> --yes]",
		"Scheduling grants no authority",
		"the occurrence is parked, and Tom is paged if it has a notify target",
		"Nothing runs until the operator is started with --enable-scheduled-work and has the dispatch template the item names",
		"every command here fails with 404 until the operator serves the scheduled-work API", "approve and retry are for a human (Tom) only",
		"The y/N and --yes guard against a slip; they prove nothing about who you are. The API accepts approve and retry only from Tom's own token.",
		"--yes needs --spec-hash", "retries a parked occurrence only when --at names it",
		"Interactive work needs at least one", "at most 8192 bytes", "at most 180 days after creation",
		"Without --yes and without a terminal, approve and retry refuse and send nothing")
	for _, gone := range []string{"never loses", "starts it twice"} {
		if strings.Contains(help, gone) {
			t.Errorf("help still promises %q", gone)
		}
	}
	h.mustRun(ExitOK, "schedule", "approve", "-h")
	contains(t, "-h", h.stdout.String(), "agent-run schedule approve")
	h.mustRun(ExitOK, "schedule", "help")
	contains(t, "schedule help", h.stdout.String(), "agent-run schedule retry")
	h.mustRun(ExitOK, "help")
	contains(t, "help", h.stdout.String(), "agent-run schedule create|list|show|edit|cancel|approve|retry")
}
