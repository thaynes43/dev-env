package session

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

func TestUsageAndSessionsKeepCountsAfterTaskReclaimAndRetainedReap(t *testing.T) {
	rows := []string{
		activityRow("task", reportedActivity),
		convRow("interactive", "remote", "claude", convFigure("claude-opus-5-5", 1234, "2026-10-25T11:55:00Z")),
	}
	before := convDecode(t, rows...)
	page := sessionsPage(before.Sessions, true, "observed", convNow, portal.ProductFilter{})
	if len(page.Usage.Rows) != 2 || page.Usage.Stats[0].Value != "1 of 2" {
		t.Fatal("invocation counted more than once", page.Usage)
	}
	for _, phase := range []string{"Suspended", "Archived"} {
		stopped := append([]string(nil), rows...)
		for i := range stopped {
			stopped[i] = strings.Replace(stopped[i], `"phase":"Running"`, `"phase":"`+phase+`"`, 1)
		}
		after := convDecode(t, stopped...)
		p := sessionsPage(after.Sessions, true, "observed", convNow, portal.ProductFilter{})
		if !reflect.DeepEqual(page.Usage.Breakdowns, p.Usage.Breakdowns) || !reflect.DeepEqual(page.Usage.Stats, p.Usage.Stats) {
			t.Fatal(phase, "changed Usage totals")
		}
		if !reflect.DeepEqual(convSection(t, page, "interactive"), convSection(t, p, "interactive")) {
			t.Fatal(phase, "changed Sessions conversation panel")
		}
		// No allowance reader is connected by the token display.
		for _, a := range p.Usage.Allowance {
			if a.Value != "Not connected" {
				t.Fatal("tokens became allowance", a)
			}
		}
	}
}

// A retained Session repeated across inventory pages must not contribute a
// second initial invocation or a second cumulative conversation snapshot.
func TestUsageRejectsRepeatedSessionsAcrossPages(t *testing.T) {
	task := activityRowPhase("task", "Suspended", reportedActivity)
	conversation := strings.Replace(convRow("interactive", "remote", "claude", convFigure("claude-opus-5-5", 1234, "2026-10-25T11:55:00Z")), `"phase":"Running"`, `"phase":"Suspended"`, 1)
	for _, tc := range []struct{ name, first, second string }{
		{"task replay", task, task},
		{"conversation replay", conversation, conversation},
		{"same UID renamed", task, strings.Replace(task, `"name":"task"`, `"name":"renamed"`, 1)},
		{"same name replaced UID", task, strings.Replace(task, `"uid":"uid-task"`, `"uid":"replacement-uid"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			s := sessionReadFixture(t, func() time.Time { return convNow }, func(*http.Request) (*http.Response, error) {
				reads++
				body := activityPage(tc.second)
				if reads == 1 {
					body = strings.Replace(activityPage(tc.first), `"complete":true`, `"complete":false,"nextCursor":"next"`, 1)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			page, err := s.Read(t.Context(), portal.ProductFilter{})
			var failure *sessionReadError
			if !errors.As(err, &failure) || failure.ReasonCode != "response_contract" || failure.Page != 2 || failure.RowIndex != 0 {
				t.Fatalf("duplicate inventory accepted for usage: err=%v page=%+v", err, page.Usage)
			}
			if page.Usage != nil || len(page.Groups) != 0 || reads != 2 {
				t.Fatal("duplicate read retained partial totals or retried", page, reads)
			}
		})
	}
}

func TestUsageCountsDistinctRetainedSessionsAcrossPagesOnce(t *testing.T) {
	reads := 0
	s := sessionReadFixture(t, func() time.Time { return convNow }, func(*http.Request) (*http.Response, error) {
		reads++
		body := activityPage(activityRowPhase("second", "Suspended", reportedActivity))
		if reads == 1 {
			body = strings.Replace(activityPage(activityRowPhase("first", "Suspended", reportedActivity)), `"complete":true`, `"complete":false,"nextCursor":"next"`, 1)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	page, err := s.Read(t.Context(), portal.ProductFilter{})
	if err != nil || reads != 2 || !page.Complete || page.Usage == nil || page.Usage.Stats[0].Value != "2 of 2" {
		t.Fatalf("distinct invocations lost: %v %+v", err, page.Usage)
	}
	// The first breakdown groups both tasks as Headless. Input is counted
	// once per Session; cache reads and Unknown counters stay separate.
	for _, row := range page.Usage.Breakdowns[0].Rows {
		if row.Name != "Headless" {
			continue
		}
		cells := map[string]string{}
		for _, cell := range row.Cells {
			cells[cell.Label] = cell.Value
		}
		if cells["Input"] != "2,469,134" || cells["Output"] != "0" || cells["Cache read"] != "10,000" {
			t.Fatal("wrong paginated totals", cells)
		}
		return
	}
	t.Fatal("missing Headless usage group")
}
