package session

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
)

func TestPagesWorkspaceRendersAggregateDetailAndControls(t *testing.T) {
	raw := `[
		{"fingerprint":"disk-crit","labels":{"alertname":"DiskFull","severity":"critical","instance":"<script>db</script>"},"annotations":{"summary":"Disk summary","description":"Disk description","runbook_url":"javascript:alert(1)"},"status":{"state":"active"},"startsAt":"2026-10-28T11:00:00-04:00","updatedAt":"2026-10-28T16:00:00Z","generatorURL":"https://example.test/graph?g0.expr=up&dropped=1"},
		{"fingerprint":"disk-warn","labels":{"alertname":"DiskFull","severity":"warning"},"annotations":{"summary":"Warning summary","description":"Warning description"},"status":{"state":"suppressed"},"startsAt":"2026-10-28T12:00:00Z","updatedAt":"2026-10-28T12:30:00Z"},
		{"fingerprint":"missing-time","labels":{"alertname":"DiskFull","severity":"Unknown"},"annotations":{"summary":"Known severity word"},"status":{"state":"active"}},
		{"fingerprint":"no-severity","labels":{"alertname":"DiskFull"},"annotations":{"summary":"Severity absent"},"status":{"state":"unprocessed"},"startsAt":"1970-01-01T00:00:00Z","updatedAt":"2026-10-28T01:00:00Z"},
		{"fingerprint":"empty-name","labels":{"alertname":"","severity":"info"},"annotations":{"summary":"Empty name"},"status":{"state":"active"},"startsAt":"2026-10-28T02:00:00Z","updatedAt":"2026-10-28T02:30:00Z"}
	]`
	snapshot, err := pages.ParseAlertmanager([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Selection = "disk-warn"
	snapshot.Coverage = "caller coverage stays visible"
	agg, err := pages.AggregateAlerts(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	view := PagesWorkspace(snapshot, false)
	if view.Condition != "available" || view.CallerCoverage != snapshot.Coverage || view.Coverage != agg.Coverage {
		t.Fatalf("envelope %+v", view)
	}
	if view.Controls.DocumentLoaded || view.Controls.Notice != portal.PagesControlNotice || view.Controls.Applied != portal.PagesAppliedState || view.Controls.Escalate != portal.PagesDesiredUnset || view.Controls.Mute != portal.PagesDesiredUnset || view.Controls.Store != portal.PagesStoreDisconnected {
		t.Fatalf("controls %+v", view.Controls)
	}
	body, err := portal.RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "<h1") != 1 || strings.Contains(text, "<form") || strings.Contains(text, "<script>") || strings.Contains(text, "javascript:") || strings.Contains(text, "policy applied") || strings.Contains(text, "Saved preferences are loaded.") {
		t.Fatal("rendered page is not a bounded read-only document")
	}
	var selectedID string
	for _, group := range view.Groups {
		if group.Default {
			selectedID = group.ID
			found := false
			for _, alert := range group.Alerts {
				if alert.Fingerprint == "disk-warn" {
					found = true
				}
			}
			if !found {
				t.Fatalf("selected group %s is not disk-warn: %+v", group.ID, group.Alerts)
			}
		}
	}
	if selectedID == "" {
		t.Fatal("no selected group")
	}
	for _, want := range []string{
		`id="` + selectedID + `" class="pages-detail is-default"`,
		"disk-warn",
		"Warning summary",
		`datetime="2026-10-28T15:00:00Z"`,
		`datetime="2026-10-28T16:00:00Z"`,
		"Current snapshot only",
		"&lt;script&gt;db&lt;/script&gt;",
		`data-known="true">Unknown`,
		`data-known="false">Unknown`,
		`class="pages-name pages-empty-value"`,
		"caller coverage stays visible",
		`<fieldset disabled>`,
		portal.PagesControlNotice,
		"No preference document is loaded. This is not a loaded empty store.",
		`class="pages-detail is-default"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(text, "-04:00") || strings.Contains(text, "1970-01-01") || strings.Contains(text, "dropped=1") {
		t.Fatal("display invented an offset, a pre-floor instant, or an unsafe query")
	}
	marker := `class="pages-detail is-default"`
	selected := text[strings.Index(text, marker):]
	if end := strings.Index(selected, `class="pages-config"`); end > 0 {
		selected = selected[:end]
	}
	if !strings.Contains(selected, "disk-warn") || strings.Contains(selected, "disk-crit") {
		t.Fatal("selected detail does not match the source fingerprint")
	}
	for _, group := range view.Groups {
		if group.Count < 1 {
			t.Fatalf("unknown rendered as zero: %+v", group)
		}
		for _, aggGroup := range agg.Groups {
			if aggGroup.AlertName == group.AlertName && aggGroup.AlertNameKnown == group.AlertNameKnown && aggGroup.Severity == group.Severity && aggGroup.SeverityKnown == group.SeverityKnown {
				if group.EarliestStart != aggGroup.EarliestStart || group.LatestUpdate != aggGroup.LatestUpdate || group.Count != aggGroup.Count {
					t.Fatalf("view drifted from aggregate: %+v %+v", group, aggGroup)
				}
			}
		}
	}
	names := map[string]bool{}
	for _, item := range view.Controls.Types {
		if item.Desired != portal.PagesDesiredUnset || item.Applied != portal.PagesAppliedState {
			t.Fatalf("type control %+v", item)
		}
		names[item.Label] = true
	}
	if !names["DiskFull"] || !names["Empty"] || names["Unknown"] {
		t.Fatalf("types %+v", view.Controls.Types)
	}
	partial, err := portal.RenderPages(view, len(body)-1)
	if err == nil || partial != nil {
		t.Fatal("response bound returned a partial page")
	}
}

func TestPagesWorkspaceStatesStayDistinct(t *testing.T) {
	emptySnap, err := pages.ParseAlertmanager([]byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		snapshot  pages.AlertmanagerSnapshot
		disabled  bool
		condition string
		want      string
		forbid    []string
	}{
		{"empty", emptySnap, false, "empty", "Observed zero current alerts.", []string{"Alertmanager is unavailable.", "Pages not enabled.", "Current snapshot is invalid.", `class="pages-count"`}},
		{"unavailable", pages.UnavailableAlertmanager(), false, "unavailable", "Alertmanager is unavailable.", []string{"Pages not enabled.", "Observed zero current alerts.", "Current snapshot is invalid."}},
		{"disabled", pages.UnavailableAlertmanager(), true, "disabled", "Pages not enabled.", []string{"Alertmanager is unavailable.", "Observed zero current alerts.", "Current snapshot is invalid."}},
		{"invalid", pages.AlertmanagerSnapshot{Availability: "available", ObservedAt: "Unknown", Source: "Unknown"}, false, "invalid", "Current snapshot is invalid.", []string{"Alertmanager is unavailable.", "Observed zero current alerts.", "Pages not enabled."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := PagesWorkspace(tc.snapshot, tc.disabled)
			if view.Condition != tc.condition || len(view.Groups) != 0 || view.Controls.DocumentLoaded {
				t.Fatalf("%+v", view)
			}
			body, err := portal.RenderPages(view, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			if !strings.Contains(text, tc.want) || !strings.Contains(text, portal.PagesControlNotice) || strings.Contains(text, "<form") {
				t.Fatalf("missing state text: %s", tc.want)
			}
			for _, bad := range tc.forbid {
				if strings.Contains(text, bad) {
					t.Fatalf("collapsed into %s", bad)
				}
			}
		})
	}
}

func TestUnconfiguredControlsAreNotALoadedDocument(t *testing.T) {
	prefs, rev, err := (pages.UnconfiguredStore{}).Load(context.Background(), pages.Principal{})
	got := unconfiguredControls(prefs, rev, err, nil)
	if !errors.Is(err, pages.ErrNotConfigured) || rev != "" || prefs.Version != 0 || got.DocumentLoaded || got.Escalate != portal.PagesDesiredUnset || got.Applied != portal.PagesAppliedState {
		t.Fatalf("unconfigured %+v %q %v", got, rev, err)
	}
	saved := pages.Preferences{Version: pages.PreferencesVersion, Alerts: []pages.AlertPreference{{Fingerprint: "f", Escalate: pages.DesiredOn, Mute: pages.DesiredOff}}}
	got = unconfiguredControls(saved, "rev-opaque", nil, nil)
	if got.DocumentLoaded || strings.Contains(got.Applied, "rev-opaque") || got.Escalate != portal.PagesDesiredUnset {
		t.Fatalf("saved document presented: %+v", got)
	}
	source, err := os.ReadFile("pages.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "AggregateAlerts") || !strings.Contains(text, "UnconfiguredStore") || strings.Contains(text, ".Save(") || strings.Contains(text, "MethodPost") {
		t.Fatal("session pages gained a write or lost the aggregate")
	}
}

func TestDisplayInstantUsesSnapshotFloorAndYearCeiling(t *testing.T) {
	overflow := "9999-12-31T23:59:59-01:00"
	if displayInstant(overflow) != "Unknown" {
		t.Fatal("offset into UTC year 10000 rendered")
	}
	if displayInstant("9999-12-31T23:59:59Z") != "9999-12-31T23:59:59Z" {
		t.Fatal("year 9999 lost")
	}
	if displayInstant("9999-12-31T22:59:59-01:00") != "9999-12-31T23:59:59Z" {
		t.Fatal("in-range offset not normalized")
	}
	if displayInstant("") != "Unknown" || displayInstant("1970-01-01T00:00:00Z") != "Unknown" || displayInstant("1999-12-31T20:00:00-04:00") != "2000-01-01T00:00:00Z" {
		t.Fatal("floor display drifted")
	}
}
