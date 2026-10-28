package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func testReminder() apiv1.Reminder {
	next := testNow.Add(2 * time.Hour)
	return apiv1.Reminder{Name: "rem-0123456789abcdef", Owner: "session/s", Title: "Renew the VPN", Body: "pay it",
		Due: "2026-10-30T09:00", TimeZone: "America/New_York", Targets: []string{"pushover:default", "calendar:haynesnetwork"},
		State: "pending", NextDue: &next, Occurrence: 1,
		Recurrence: &apiv1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"MO", "TH"}, Count: 4},
		Deliveries: []apiv1.ReminderDelivery{{Target: "pushover:default", Occurrence: 1, State: "retry", Attempts: 1, Reason: "not sent; will retry", UpdatedAt: testNow}},
		Calendar:   []apiv1.ReminderCalendar{{Target: "calendar:haynesnetwork", State: "skipped", Reason: "calendar: unconfigured", UpdatedAt: testNow}},
		CreatedAt:  testNow}
}

func TestRemindCreateListShowEditCancel(t *testing.T) {
	h := newHarness(t)
	rem := testReminder()
	var created apiv1.CreateReminderRequest
	var edited map[string]any
	var gotQuery string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.RemindersPath:
			created = decodeAs[apiv1.CreateReminderRequest](t, body)
			writeTestJSON(w, http.StatusCreated, rem)
		case r.Method == http.MethodGet && r.URL.Path == apiv1.RemindersPath:
			gotQuery = r.URL.RawQuery
			writeTestJSON(w, http.StatusOK, apiv1.ReminderList{Reminders: []apiv1.Reminder{rem}})
		case r.Method == http.MethodGet && r.URL.Path == apiv1.ReminderPath(rem.Name):
			writeTestJSON(w, http.StatusOK, rem)
		case r.Method == http.MethodPatch && r.URL.Path == apiv1.ReminderPath(rem.Name):
			edited = map[string]any{}
			if err := json.Unmarshal(body, &edited); err != nil {
				t.Fatal(err)
			}
			writeTestJSON(w, http.StatusOK, rem)
		case r.Method == http.MethodPost && r.URL.Path == apiv1.ReminderCancelPath(rem.Name):
			c := rem
			c.State, c.Warning = "cancelled", "a delivery was already in flight"
			writeTestJSON(w, http.StatusOK, c)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
		}
	}

	h.mustRun(ExitOK, "remind", "create", "Renew the VPN", "--due", "2026-10-30T09:00", "--tz", "America/New_York",
		"--body", "pay it", "--repeat", "weekly:MO,TH", "--count", "4", "--targets", "pushover:default, calendar:haynesnetwork", "--key", "vpn")
	if created.Title != "Renew the VPN" || created.Due != "2026-10-30T09:00" || created.TimeZone != "America/New_York" || created.Body != "pay it" || created.Key != "vpn" ||
		created.Recurrence == nil || created.Recurrence.Freq != "weekly" || strings.Join(created.Recurrence.ByDay, ",") != "MO,TH" || created.Recurrence.Count != 4 ||
		strings.Join(created.Targets, ",") != "pushover:default,calendar:haynesnetwork" {
		t.Errorf("request %+v", created)
	}
	contains(t, "stdout", h.stdout.String(), "created rem-0123456789abcdef", "weekly on MO,TH, 4 times", "agent-run remind cancel rem-0123456789abcdef")

	h.mustRun(ExitOK, "remind", "create", "-o", "json", "x", "--due", "2026-10-30T09:00", "--tz", "UTC")
	var echoed apiv1.Reminder
	if err := json.Unmarshal(h.stdout.Bytes(), &echoed); err != nil || echoed.Name != rem.Name {
		t.Errorf("json output %q: %v", h.stdout.String(), err)
	}
	if created.Recurrence != nil || len(created.Targets) != 0 {
		t.Errorf("a one-shot with default targets sent %+v", created)
	}

	h.mustRun(ExitOK, "remind", "list")
	contains(t, "stdout", h.stdout.String(), "NAME", "rem-0123456789abcdef  pending", "Renew the VPN")
	if gotQuery != "" {
		t.Errorf("list query %q", gotQuery)
	}
	h.mustRun(ExitOK, "remind", "list", "--all")
	if gotQuery != "all=true" {
		t.Errorf("list --all query %q", gotQuery)
	}

	h.mustRun(ExitOK, "remind", "show", rem.Name)
	contains(t, "stdout", h.stdout.String(), "rem-0123456789abcdef  pending", "title:    Renew the VPN", "targets:  pushover:default, calendar:haynesnetwork",
		"delivery: pushover:default occurrence 1 retry, 1 attempt(s) (not sent; will retry)", "calendar: calendar:haynesnetwork skipped (calendar: unconfigured)")

	h.mustRun(ExitOK, "remind", "edit", rem.Name, "--title", "Renew now", "--once")
	if edited["title"] != "Renew now" || edited["clearRecurrence"] != true || edited["body"] != nil || edited["recurrence"] != nil {
		t.Errorf("edit body %v", edited)
	}
	h.mustRun(ExitOK, "remind", "edit", rem.Name, "--body", "", "--repeat", "daily")
	if b, ok := edited["body"]; !ok || b != "" || edited["recurrence"] == nil {
		t.Errorf("an explicitly empty body must be sent: %v", edited)
	}

	h.mustRun(ExitOK, "remind", "cancel", rem.Name)
	contains(t, "stdout", h.stdout.String(), "cancelled rem-0123456789abcdef", "WARNING: a delivery was already in flight")
}

func TestRemindUsageErrors(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		t.Errorf("a usage error reached the API: %s %s", r.Method, r.URL.Path)
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no")
	}
	for _, args := range [][]string{
		{"remind"},
		{"remind", "fly"},
		{"remind", "create"},
		{"remind", "create", "x"},
		{"remind", "create", "x", "--due", "2026-10-30T09:00"},
		{"remind", "create", "x", "--due", "2026-10-30T09:00", "--tz", "UTC", "--repeat", "hourly"},
		{"remind", "create", "x", "--due", "2026-10-30T09:00", "--tz", "UTC", "--count", "3"},
		{"remind", "create", "x", "--due", "2026-10-30T09:00", "--tz", "UTC", "--repeat", "daily", "--count", "3", "--until", "2026-12-31T00:00:00Z"},
		{"remind", "create", "x", "--due", "2026-10-30T09:00", "--tz", "UTC", "--repeat", "daily", "--until", "soon"},
		{"remind", "create", "x", "--due", "2026-10-30T09:00", "--tz", "UTC", "--repeat", "monthly:32"},
		{"remind", "create", "x", "--due", "2026-10-30T09:00", "--tz", "UTC", "--repeat", "daily:3"},
		{"remind", "list", "extra"},
		{"remind", "show"},
		{"remind", "edit", "rem-a"},
		{"remind", "edit", "rem-a", "--once", "--repeat", "daily"},
		{"remind", "edit", "rem-a", "--targets", ","},
		{"remind", "cancel"},
		{"remind", "cancel", "a", "b"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("agent-run %q: exit %d, want %d", args, code, ExitUsage)
		}
	}
	h.mustRun(ExitOK, "help", "remind")
	contains(t, "help", h.stdout.String(), "agent-run remind create", "gap-length", "--targets")
	h.mustRun(ExitOK, "remind", "create", "-h")
	contains(t, "-h", h.stdout.String(), "agent-run remind create")
	h.mustRun(ExitOK, "help")
	contains(t, "help", h.stdout.String(), "agent-run remind create|list|show|edit|cancel")
}

func TestRemindShowsAnAPIRefusal(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		apiError(w, http.StatusUnprocessableEntity, apiv1.CodeInvalid, "targets: target \"pushover:other\" is not allowed",
			apiv1.FieldError{Field: "targets", Message: "not allowed"})
	}
	if code := h.run("remind", "create", "x", "--due", "2026-10-30T09:00", "--tz", "UTC", "--targets", "pushover:other"); code == ExitOK {
		t.Error("an API refusal exited 0")
	}
	contains(t, "stderr", h.stderr.String(), "not allowed")
}
