package pages

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAggregateAvailableEmptyAndUnavailable(t *testing.T) {
	emptyAlerts := AlertmanagerSnapshot{Availability: "Available", ObservedAt: "Unknown", Source: "Unknown", Alerts: []AlertmanagerAlert{}}
	nilAlerts := AlertmanagerSnapshot{Availability: "Available", ObservedAt: "Unknown", Source: "Unknown"}
	for _, snapshot := range []AlertmanagerSnapshot{emptyAlerts, nilAlerts} {
		got, err := AggregateAlerts(snapshot)
		if err != nil || got.Availability != "Available" || got.Groups == nil || len(got.Groups) != 0 || got.Coverage != AggregateCoverage {
			t.Fatalf("empty: %+v %v", got, err)
		}
	}
	unavailable, err := AggregateAlerts(UnavailableAlertmanager())
	if err != nil || unavailable.Availability != "Unavailable" || len(unavailable.Groups) != 0 || unavailable.ObservedAt != "Unknown" || unavailable.Source != "Unknown" {
		t.Fatalf("unavailable: %+v %v", unavailable, err)
	}
	if unavailable.Availability == "Available" {
		t.Fatal("unavailable collapsed into empty")
	}
	offset := AlertmanagerSnapshot{Availability: "Unavailable", ObservedAt: "2026-10-28T20:00:00-04:00", Source: "held"}
	got, err := AggregateAlerts(offset)
	if err != nil || got.Availability != "Unavailable" || got.ObservedAt != "2026-10-29T00:00:00Z" || got.Source != "held" || len(got.Groups) != 0 {
		t.Fatalf("passive observed time: %+v %v", got, err)
	}
	if _, err := AggregateAlerts(AlertmanagerSnapshot{Availability: "available", ObservedAt: "Unknown", Source: "Unknown"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("lowercase availability accepted")
	}
	withAlerts := UnavailableAlertmanager()
	withAlerts.Alerts = []AlertmanagerAlert{groupedAlert("f", "A", true, "warning", true, "", "")}
	got, err = AggregateAlerts(withAlerts)
	if !errors.Is(err, ErrInvalid) || got.Availability != "Unavailable" || len(got.Groups) != 0 || got.Source != "Unknown" {
		t.Fatalf("unavailable with alerts: %+v %v", got, err)
	}
}

func TestAggregateGroupsCountAndUTCBounds(t *testing.T) {
	snapshot := AlertmanagerSnapshot{
		Availability: "Available",
		ObservedAt:   "2026-10-28T20:00:00-04:00",
		Source:       "local",
		Alerts: []AlertmanagerAlert{
			groupedAlert("f3", "Other", true, "critical", true, "2026-10-28T06:00:00Z", "2026-10-28T07:00:00Z"),
			groupedAlert("f2", "DiskFull", true, "warning", true, "2026-10-28T03:00:00Z", "2026-10-28T04:00:00Z"),
			groupedAlert("f1", "DiskFull", true, "critical", true, "2026-10-28T05:00:00+01:00", "2026-10-28T15:30:00Z"),
			groupedAlert("f4", "DiskFull", true, "critical", true, "2026-10-28T00:00:00Z", "2026-10-28T15:00:00Z"),
			groupedAlert("f5", "DiskFull", true, "critical", true, "2026-10-27T20:00:00-04:00", "2026-10-28T12:00:00-04:00"),
		},
	}
	reversed := snapshot
	reversed.Alerts = append([]AlertmanagerAlert(nil), snapshot.Alerts...)
	for i, j := 0, len(reversed.Alerts)-1; i < j; i, j = i+1, j-1 {
		reversed.Alerts[i], reversed.Alerts[j] = reversed.Alerts[j], reversed.Alerts[i]
	}
	first, err := AggregateAlerts(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AggregateAlerts(reversed)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("order changed result: %+v %+v %v", first, second, err)
	}
	if first.ObservedAt != "2026-10-29T00:00:00Z" || first.Source != "local" || first.Availability != "Available" {
		t.Fatalf("envelope: %+v", first)
	}
	want := []AlertGroup{
		{AlertName: "DiskFull", AlertNameKnown: true, Severity: "critical", SeverityKnown: true, Count: 3, EarliestStart: "2026-10-28T00:00:00Z", LatestUpdate: "2026-10-28T16:00:00Z"},
		{AlertName: "DiskFull", AlertNameKnown: true, Severity: "warning", SeverityKnown: true, Count: 1, EarliestStart: "2026-10-28T03:00:00Z", LatestUpdate: "2026-10-28T04:00:00Z"},
		{AlertName: "Other", AlertNameKnown: true, Severity: "critical", SeverityKnown: true, Count: 1, EarliestStart: "2026-10-28T06:00:00Z", LatestUpdate: "2026-10-28T07:00:00Z"},
	}
	if !reflect.DeepEqual(first.Groups, want) {
		t.Fatalf("groups: %+v", first.Groups)
	}
	if snapshot.Alerts[0].Fingerprint != "f3" {
		t.Fatal("input mutated")
	}
}

func TestAggregateMissingMalformedAndNoHistory(t *testing.T) {
	missing := AlertmanagerSnapshot{
		Availability: "Available", ObservedAt: "Unknown", Source: "Unknown",
		Alerts: []AlertmanagerAlert{
			groupedAlert("a", "DiskFull", true, "critical", true, "2026-10-28T00:00:00Z", "2026-10-28T01:00:00Z"),
			groupedAlert("b", "DiskFull", true, "critical", true, "", "2026-10-28T02:00:00Z"),
			groupedAlert("c", "DiskFull", true, "", false, "2026-10-28T03:00:00Z", ""),
			groupedAlert("d", "DiskFull", true, "Unknown", true, "2026-10-28T04:00:00Z", "2026-10-28T05:00:00Z"),
			groupedAlert("e", "", false, "critical", true, "2026-10-28T06:00:00Z", "2026-10-28T07:00:00Z"),
			func() AlertmanagerAlert {
				alert := groupedAlert("f", "Case", true, "warning", true, "2026-10-28T08:00:00Z", "2026-10-28T09:00:00Z")
				alert.Labels["Alertname"] = "Other"
				alert.Labels["Severity"] = "critical"
				alert.Recurrence = "every day"
				alert.Disposition = "acked"
				return alert
			}(),
			groupedAlert("g", "diskfull", true, "critical", true, "2026-10-28T01:00:00Z", "2026-10-28T02:00:00Z"),
		},
	}
	got, err := AggregateAlerts(missing)
	if err != nil {
		t.Fatal(err)
	}
	want := []AlertGroup{
		{AlertName: "Case", AlertNameKnown: true, Severity: "warning", SeverityKnown: true, Count: 1, EarliestStart: "2026-10-28T08:00:00Z", LatestUpdate: "2026-10-28T09:00:00Z"},
		{AlertName: "DiskFull", AlertNameKnown: true, Severity: "Unknown", SeverityKnown: true, Count: 1, EarliestStart: "2026-10-28T04:00:00Z", LatestUpdate: "2026-10-28T05:00:00Z"},
		{AlertName: "DiskFull", AlertNameKnown: true, Severity: "critical", SeverityKnown: true, Count: 2, EarliestStart: "Unknown", LatestUpdate: "2026-10-28T02:00:00Z"},
		{AlertName: "DiskFull", AlertNameKnown: true, Severity: "", SeverityKnown: false, Count: 1, EarliestStart: "2026-10-28T03:00:00Z", LatestUpdate: "Unknown"},
		{AlertName: "diskfull", AlertNameKnown: true, Severity: "critical", SeverityKnown: true, Count: 1, EarliestStart: "2026-10-28T01:00:00Z", LatestUpdate: "2026-10-28T02:00:00Z"},
		{AlertName: "", AlertNameKnown: false, Severity: "critical", SeverityKnown: true, Count: 1, EarliestStart: "2026-10-28T06:00:00Z", LatestUpdate: "2026-10-28T07:00:00Z"},
	}
	if !reflect.DeepEqual(got.Groups, want) {
		t.Fatalf("groups: %#v", got.Groups)
	}
	if strings.Contains(fmt.Sprintf("%+v", got), "every day") || strings.Contains(fmt.Sprintf("%+v", got), "acked") {
		t.Fatalf("history leaked: %+v", got)
	}

	parsed, err := ParseAlertmanager([]byte(`[{"fingerprint":"f1","labels":{"alertname":"A","severity":"warning"},"status":{"state":"active"},"startsAt":"2026-10-28T01:00:00+01:00","updatedAt":"2026-10-28T02:00:00Z"}]`))
	if err != nil {
		t.Fatal(err)
	}
	fromParser, err := AggregateAlerts(parsed)
	if err != nil || fromParser.ObservedAt != "Unknown" || fromParser.Source != "Unknown" || len(fromParser.Groups) != 1 || fromParser.Groups[0].EarliestStart != "2026-10-28T00:00:00Z" || fromParser.Groups[0].Count != 1 {
		t.Fatalf("parser output: %+v %v", fromParser, err)
	}

	bad := AlertmanagerSnapshot{Availability: "Available", ObservedAt: "Unknown", Source: "Unknown", Alerts: []AlertmanagerAlert{
		groupedAlert("ok", "A", true, "warning", true, "2026-10-28T00:00:00Z", "2026-10-28T00:00:00Z"),
		groupedAlert("bad", "A", true, "warning", true, "yesterday", "2026-10-28T00:00:00Z"),
	}}
	failed, err := AggregateAlerts(bad)
	if !errors.Is(err, ErrInvalid) || failed.Availability != "Unavailable" || len(failed.Groups) != 0 {
		t.Fatalf("malformed time counted: %+v %v", failed, err)
	}
	bad.Alerts[1].StartsAt = "Unknown"
	if _, err := AggregateAlerts(bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("literal Unknown alert time accepted")
	}
	for _, alerts := range [][]AlertmanagerAlert{
		{groupedAlert("dup", "A", true, "warning", true, "", ""), groupedAlert("dup", "A", true, "warning", true, "", "")},
		{groupedAlert("dup", "A", true, "warning", true, "", ""), groupedAlert("dup", "B", true, "critical", true, "", "")},
	} {
		got, err = AggregateAlerts(AlertmanagerSnapshot{Availability: "Available", ObservedAt: "Unknown", Source: "Unknown", Alerts: alerts})
		if !errors.Is(err, ErrInvalid) || len(got.Groups) != 0 {
			t.Fatalf("duplicate fingerprint counted: %+v %v", got, err)
		}
	}
	tooMany := make([]AlertmanagerAlert, alertmanagerMaxAlerts+1)
	for i := range tooMany {
		tooMany[i] = groupedAlert(fmt.Sprintf("f%d", i), "A", true, "warning", true, "", "")
	}
	got, err = AggregateAlerts(AlertmanagerSnapshot{Availability: "Available", ObservedAt: "Unknown", Source: "Unknown", Alerts: tooMany})
	if !errors.Is(err, ErrInvalid) || len(got.Groups) != 0 {
		t.Fatalf("over limit counted: %d %v", len(got.Groups), err)
	}
}

func TestAggregateTimeFloorAndParserAgreement(t *testing.T) {
	early := AlertmanagerSnapshot{
		Availability: "Available",
		ObservedAt:   "1970-01-01T00:00:00Z",
		Source:       "local",
		Alerts: []AlertmanagerAlert{
			groupedAlert("epoch", "A", true, "warning", true, "1970-01-01T00:00:00Z", "0001-01-01T00:00:00Z"),
			groupedAlert("year0", "A", true, "warning", true, "0000-01-01T00:00:00Z", "2026-10-28T00:00:00Z"),
			groupedAlert("floor", "B", true, "warning", true, "2000-01-01T00:00:00Z", "1999-12-31T20:00:00-04:00"),
		},
	}
	got, err := AggregateAlerts(early)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservedAt != "Unknown" || got.Availability != "Available" {
		t.Fatalf("epoch observation: %+v", got)
	}
	want := []AlertGroup{
		{AlertName: "A", AlertNameKnown: true, Severity: "warning", SeverityKnown: true, Count: 2, EarliestStart: "Unknown", LatestUpdate: "Unknown"},
		{AlertName: "B", AlertNameKnown: true, Severity: "warning", SeverityKnown: true, Count: 1, EarliestStart: "2000-01-01T00:00:00Z", LatestUpdate: "2000-01-01T00:00:00Z"},
	}
	if !reflect.DeepEqual(got.Groups, want) {
		t.Fatalf("floor groups: %#v", got.Groups)
	}
	kept, err := AggregateAlerts(AlertmanagerSnapshot{
		Availability: "Available", ObservedAt: "2000-01-01T00:00:00Z", Source: "local",
		Alerts: []AlertmanagerAlert{groupedAlert("only", "C", true, "warning", true, "1999-12-31T23:59:59Z", "0001-01-01T00:00:00Z")},
	})
	if err != nil || kept.ObservedAt != "2000-01-01T00:00:00Z" || kept.Groups[0].EarliestStart != "Unknown" || kept.Groups[0].LatestUpdate != "Unknown" || strings.Contains(fmt.Sprintf("%+v", kept), "0001-01-01") {
		t.Fatalf("boundary: %+v %v", kept, err)
	}

	raw := []byte(`{"observedAt":"2026-10-28T00:00:00Z","source":" local ","alerts":[{"fingerprint":"f1","labels":{"":"kept","alertname":"Case","severity":"warning"},"status":{"state":"active"},"startsAt":"2026-10-28T01:00:00+01:00","updatedAt":"2026-10-28T02:00:00Z"}]}`)
	parsed, err := ParseAlertmanager(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Source != " local " || parsed.Alerts[0].Labels[""] != "kept" {
		t.Fatalf("parser: %+v", parsed)
	}
	agreed, err := AggregateAlerts(parsed)
	if err != nil || agreed.Source != " local " || agreed.Availability != "Available" || len(agreed.Groups) != 1 || agreed.Groups[0].AlertName != "Case" || agreed.Groups[0].Severity != "warning" || agreed.Groups[0].EarliestStart != "2026-10-28T00:00:00Z" {
		t.Fatalf("parser aggregate: %+v %v", agreed, err)
	}
}

func TestSnapshotInstantUTCYearAfter9999IsUnknown(t *testing.T) {
	overflow := "9999-12-31T23:59:59-01:00"
	parsed, err := time.Parse(time.RFC3339Nano, overflow)
	if err != nil {
		t.Fatal(err)
	}
	parsed = parsed.UTC().Round(0)
	if parsed.Year() != 10000 {
		t.Fatalf("offset did not normalize to UTC year 10000: year %d", parsed.Year())
	}
	if _, ok, reason := snapshotInstant(overflow); reason != "" || ok {
		t.Fatalf("UTC year 10000 returned known: ok=%v reason=%s", ok, reason)
	}
	known := "9999-12-31T23:59:59Z"
	got, ok, reason := snapshotInstant(known)
	if reason != "" || !ok || got.Year() != 9999 || got.Format(time.RFC3339Nano) != known {
		t.Fatalf("year 9999: %v %v %s", got, ok, reason)
	}
	stayed := "9999-12-31T22:59:59-01:00"
	got, ok, reason = snapshotInstant(stayed)
	if reason != "" || !ok || got.Year() != 9999 || got.Format(time.RFC3339Nano) != "9999-12-31T23:59:59Z" {
		t.Fatalf("offset inside 9999: %v %v %s", got, ok, reason)
	}
	snapshot := AlertmanagerSnapshot{
		Availability: "Available",
		ObservedAt:   overflow,
		Source:       "local",
		Alerts: []AlertmanagerAlert{
			groupedAlert("overflow", "A", true, "warning", true, overflow, overflow),
			groupedAlert("known", "B", true, "warning", true, known, stayed),
		},
	}
	agg, err := AggregateAlerts(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if agg.ObservedAt != "Unknown" || strings.Contains(fmt.Sprintf("%+v", agg), "10000") {
		t.Fatalf("year 10000 rendered: %+v", agg)
	}
	if len(agg.Groups) != 2 || agg.Groups[0].AlertName != "A" || agg.Groups[0].EarliestStart != "Unknown" || agg.Groups[0].LatestUpdate != "Unknown" {
		t.Fatalf("overflow group: %+v", agg.Groups)
	}
	if agg.Groups[1].EarliestStart != known || agg.Groups[1].LatestUpdate != "9999-12-31T23:59:59Z" {
		t.Fatalf("known 9999 group: %+v", agg.Groups[1])
	}
}

func groupedAlert(fingerprint, name string, nameOK bool, severity string, severityOK bool, start, updated string) AlertmanagerAlert {
	labels := map[string]string{}
	shown := ""
	if nameOK {
		labels["alertname"] = name
		shown = name
	}
	if severityOK {
		labels["severity"] = severity
	}
	return AlertmanagerAlert{
		Fingerprint: fingerprint,
		Name:        shown,
		Status:      "active",
		StartsAt:    start,
		UpdatedAt:   updated,
		Labels:      labels,
		Disposition: "Unknown",
		Recurrence:  "Unknown",
	}
}
