package pages

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// AggregateCoverage labels every aggregate as this snapshot only.
	AggregateCoverage = "Current snapshot only. Each group is alertname and severity, with the count of current alerts, the earliest start and the latest update. Notification delivery, history, triage and recurrence are not included."

	availabilityAvailable   = "Available"
	availabilityUnavailable = "Unavailable"
	timeUnknown             = "Unknown"
)

// AlertAggregate is one current snapshot grouped by alert name and severity.
// It is not notification delivery, history, triage, or recurrence.
type AlertAggregate struct {
	Coverage     string
	Availability string
	ObservedAt   string
	Source       string
	Groups       []AlertGroup
}

// AlertGroup is one alertname and severity pair in the current snapshot.
// AlertNameKnown and SeverityKnown distinguish a missing label from a label
// whose value is empty or the word Unknown. EarliestStart and LatestUpdate
// are UTC RFC3339Nano instants, or the exact sentinel Unknown.
type AlertGroup struct {
	AlertName      string
	AlertNameKnown bool
	Severity       string
	SeverityKnown  bool
	Count          int
	EarliestStart  string
	LatestUpdate   string
}

// AggregateAlerts groups snapshot by the exact labels alertname and severity.
// Duplicate fingerprints and other direct input the snapshot parser would
// refuse fail closed: the result is Unavailable, with no partial counts.
// The snapshot is not modified. The wall clock is not read.
func AggregateAlerts(snapshot AlertmanagerSnapshot) (AlertAggregate, error) {
	switch snapshot.Availability {
	case availabilityUnavailable:
		if len(snapshot.Alerts) != 0 {
			return failAggregate("alerts")
		}
		return passiveAggregate(snapshot)
	case availabilityAvailable:
		return aggregateAvailable(snapshot)
	default:
		return failAggregate("availability")
	}
}

func passiveAggregate(snapshot AlertmanagerSnapshot) (AlertAggregate, error) {
	observed, err := normalizeObserved(snapshot.ObservedAt)
	if err != nil || !validSnapshotSource(snapshot.Source) {
		return failAggregate("snapshot")
	}
	return AlertAggregate{
		Coverage:     AggregateCoverage,
		Availability: availabilityUnavailable,
		ObservedAt:   observed,
		Source:       snapshot.Source,
		Groups:       []AlertGroup{},
	}, nil
}

func aggregateAvailable(snapshot AlertmanagerSnapshot) (AlertAggregate, error) {
	observed, err := normalizeObserved(snapshot.ObservedAt)
	if err != nil || !validSnapshotSource(snapshot.Source) {
		return failAggregate("snapshot")
	}
	if len(snapshot.Alerts) > alertmanagerMaxAlerts {
		return failAggregate("limit")
	}
	groups := map[groupKey]*groupAcc{}
	seen := map[string]struct{}{}
	for _, alert := range snapshot.Alerts {
		prepared, reason := prepareAlert(alert, seen)
		if reason != "" {
			return failAggregate(reason)
		}
		acc := groups[prepared.key]
		if acc == nil {
			acc = &groupAcc{key: prepared.key}
			groups[prepared.key] = acc
		}
		acc.add(prepared)
	}
	out := make([]AlertGroup, 0, len(groups))
	for _, acc := range groups {
		out = append(out, acc.group())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AlertNameKnown != out[j].AlertNameKnown {
			return out[i].AlertNameKnown
		}
		if out[i].AlertName != out[j].AlertName {
			return out[i].AlertName < out[j].AlertName
		}
		if out[i].SeverityKnown != out[j].SeverityKnown {
			return out[i].SeverityKnown
		}
		return out[i].Severity < out[j].Severity
	})
	return AlertAggregate{
		Coverage:     AggregateCoverage,
		Availability: availabilityAvailable,
		ObservedAt:   observed,
		Source:       snapshot.Source,
		Groups:       out,
	}, nil
}

type groupKey struct {
	name     string
	nameOK   bool
	severity string
	sevOK    bool
}

type preparedAlert struct {
	key      groupKey
	start    time.Time
	startOK  bool
	update   time.Time
	updateOK bool
}

type groupAcc struct {
	key       groupKey
	count     int
	starts    int
	startMin  time.Time
	updates   int
	updateMax time.Time
}

func (g *groupAcc) add(alert preparedAlert) {
	g.count++
	if alert.startOK {
		if g.starts == 0 || alert.start.Before(g.startMin) {
			g.startMin = alert.start
		}
		g.starts++
	}
	if alert.updateOK {
		if g.updates == 0 || alert.update.After(g.updateMax) {
			g.updateMax = alert.update
		}
		g.updates++
	}
}

func (g *groupAcc) group() AlertGroup {
	earliest, latest := timeUnknown, timeUnknown
	if g.count > 0 && g.starts == g.count {
		earliest = g.startMin.UTC().Format(time.RFC3339Nano)
	}
	if g.count > 0 && g.updates == g.count {
		latest = g.updateMax.UTC().Format(time.RFC3339Nano)
	}
	return AlertGroup{
		AlertName:      g.key.name,
		AlertNameKnown: g.key.nameOK,
		Severity:       g.key.severity,
		SeverityKnown:  g.key.sevOK,
		Count:          g.count,
		EarliestStart:  earliest,
		LatestUpdate:   latest,
	}
}

func prepareAlert(alert AlertmanagerAlert, seen map[string]struct{}) (preparedAlert, string) {
	if alert.Fingerprint == "" || len(alert.Fingerprint) > alertmanagerMaxString || alert.Fingerprint != cleanAlertText(alert.Fingerprint) || !utf8.ValidString(alert.Fingerprint) {
		return preparedAlert{}, "fingerprint"
	}
	if _, ok := seen[alert.Fingerprint]; ok {
		return preparedAlert{}, "fingerprint"
	}
	seen[alert.Fingerprint] = struct{}{}
	if alert.Labels == nil || len(alert.Labels) > 1000 {
		return preparedAlert{}, "labels"
	}
	for key, value := range alert.Labels {
		if len(key) > alertmanagerMaxString || len(value) > alertmanagerMaxString || key != cleanAlertText(key) || value != cleanAlertText(value) || !utf8.ValidString(key) || !utf8.ValidString(value) {
			return preparedAlert{}, "labels"
		}
	}
	switch alert.Status {
	case "active", "suppressed", "unprocessed":
	default:
		return preparedAlert{}, "status"
	}
	name, nameOK := alert.Labels["alertname"]
	if !nameOK {
		name = ""
	}
	if (nameOK && alert.Name != name) || (!nameOK && alert.Name != "") {
		return preparedAlert{}, "alertname"
	}
	severity, severityOK := alert.Labels["severity"]
	if !severityOK {
		severity = ""
	}
	start, startOK, reason := optionalInstant(alert.StartsAt)
	if reason != "" {
		return preparedAlert{}, reason
	}
	updated, updateOK, reason := optionalInstant(alert.UpdatedAt)
	if reason != "" {
		return preparedAlert{}, reason
	}
	if _, _, reason = optionalInstant(alert.EndsAt); reason != "" {
		return preparedAlert{}, reason
	}
	return preparedAlert{
		key:      groupKey{name: name, nameOK: nameOK, severity: severity, sevOK: severityOK},
		start:    start,
		startOK:  startOK,
		update:   updated,
		updateOK: updateOK,
	}, ""
}

func optionalInstant(value string) (time.Time, bool, string) {
	if value == "" {
		return time.Time{}, false, ""
	}
	parsed, ok, reason := snapshotInstant(value)
	if reason != "" {
		return time.Time{}, false, reason
	}
	return parsed, ok, ""
}

func snapshotInstant(value string) (time.Time, bool, string) {
	if !utf8.ValidString(value) || !validAlertTime(value) {
		return time.Time{}, false, "time"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false, "time"
	}
	parsed = parsed.UTC().Round(0)
	if parsed.Before(minPreferenceTime) {
		return time.Time{}, false, ""
	}
	return parsed, true, ""
}

func normalizeObserved(value string) (string, error) {
	if value == timeUnknown {
		return timeUnknown, nil
	}
	parsed, ok, reason := snapshotInstant(value)
	if reason != "" {
		return "", errors.New("observedAt")
	}
	if !ok {
		return timeUnknown, nil
	}
	return parsed.Format(time.RFC3339Nano), nil
}

func validSnapshotSource(source string) bool {
	if source == timeUnknown {
		return true
	}
	if strings.TrimSpace(source) == "" || len(source) > alertmanagerMaxString || strings.Contains(source, "://") || !utf8.ValidString(source) {
		return false
	}
	return source == cleanAlertText(source)
}

func failAggregate(reason string) (AlertAggregate, error) {
	return AlertAggregate{
		Coverage:     AggregateCoverage,
		Availability: availabilityUnavailable,
		ObservedAt:   timeUnknown,
		Source:       timeUnknown,
		Groups:       []AlertGroup{},
	}, fmt.Errorf("%w: %s", ErrInvalid, reason)
}
