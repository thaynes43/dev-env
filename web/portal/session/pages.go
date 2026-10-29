package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
)

const pagesPath = "/portal/projects/dev-env/environments/internal/pages"
const pagesSourceURL = "http://alertmanager-operated.observability.svc.cluster.local:9093/api/v2/alerts"

type pagesSource struct {
	client *http.Client
	now    func() time.Time
}

// PagesSourceCheck contains no alert content or upstream diagnostics. Nil count
// and observation mean Unknown, rather than an observed empty snapshot.
type PagesSourceCheck struct {
	Status     string  `json:"status"`
	Count      *int    `json:"count"`
	ObservedAt *string `json:"observedAt"`
}

// CheckPagesSource checks only the fixed mounted Pages source. It does not load
// the production server, database or browser authentication configuration.
func CheckPagesSource(ctx context.Context) PagesSourceCheck {
	return checkPagesSource(ctx, ProductionRoot, time.Now, nil)
}

func checkPagesSource(ctx context.Context, root string, now func() time.Time, dial func(context.Context, string, string) (net.Conn, error)) PagesSourceCheck {
	result := PagesSourceCheck{Status: "unavailable"}
	enabled, err := loadPagesEnabled(root)
	if err != nil {
		return result
	}
	if !enabled {
		result.Status = "disabled"
		return result
	}
	snapshot, err := newPagesSource(true, now, dial).read(ctx)
	if err != nil {
		return result
	}
	count := len(snapshot.Alerts)
	return PagesSourceCheck{Status: "available", Count: &count, ObservedAt: &snapshot.ObservedAt}
}

func loadPagesEnabled(root string) (bool, error) {
	present, err := mountedPresent(filepath.Join(root, "config/pages-alertmanager.json"))
	if err != nil {
		return false, errProductionConfig
	}
	if !present {
		return false, nil
	}
	raw, err := readMountedBytes(root, "config/pages-alertmanager.json", 8192)
	if err != nil || !protocol.StrictPortalJSON(raw) {
		return false, errProductionConfig
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !exactKeys(fields, []string{"source_url"}) {
		return false, errProductionConfig
	}
	url, ok := jsonString(fields["source_url"])
	if !ok || url != pagesSourceURL {
		return false, errProductionConfig
	}
	return true, nil
}
func newPagesSource(enabled bool, now func() time.Time, dial func(context.Context, string, string) (net.Conn, error)) *pagesSource {
	if !enabled || now == nil {
		return nil
	}
	transport := &http.Transport{Proxy: nil, DialContext: dial, DisableKeepAlives: true}
	return &pagesSource{now: now, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *pagesSource) read(ctx context.Context) (pages.AlertmanagerSnapshot, error) {
	unavailable := pages.UnavailableAlertmanager()
	if s == nil || s.client == nil || s.now == nil {
		return unavailable, portal.ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pagesSourceURL, nil)
	if err != nil {
		return unavailable, portal.ErrUnavailable
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return unavailable, portal.ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return unavailable, portal.ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, pages.AlertmanagerMaxBytes+1))
	if err != nil || len(raw) > pages.AlertmanagerMaxBytes || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		return unavailable, portal.ErrUnavailable
	}
	snapshot, err := pages.ParseAlertmanager(raw)
	if err != nil {
		return unavailable, portal.ErrUnavailable
	}
	snapshot.ObservedAt = s.now().UTC().Format(time.RFC3339Nano)
	snapshot.Source = "Alertmanager /api/v2/alerts"
	snapshot.Coverage = "Current alerts only; notification and delivery history, triage disposition and recurrence are Unknown."
	return snapshot, nil
}
func servePages(w http.ResponseWriter, r *http.Request, c AssemblyConfig) {
	authHeaders(w)
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || c.Session == nil || c.Authorizer == nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	principal, err := c.Session.Authenticate(r)
	if errors.Is(err, portal.ErrDenied) {
		w.Header().Set("Location", "/auth/login")
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	if err != nil {
		authFailure(w, err)
		return
	}
	resource := portal.Resource{Project: "dev-env", Environment: "internal"}
	if err = c.Authorizer.Authorize(r.Context(), principal, resource); err != nil {
		authFailure(w, err)
		return
	}
	snapshot := pages.UnavailableAlertmanager()
	snapshot.Coverage = "Current alerts only; notification and delivery history, triage disposition and recurrence are Unknown."
	if c.Pages != nil {
		if got, e := c.Pages.read(r.Context()); e == nil {
			snapshot = got
		}
	}
	body, err := portal.RenderPages(PagesWorkspace(snapshot, c.Pages == nil), maximumResponse*7/8)
	if err != nil {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	current, err := c.Session.Authenticate(r)
	if err != nil || current != principal {
		authFailure(w, portal.ErrDenied)
		return
	}
	if err = c.Authorizer.Authorize(r.Context(), principal, resource); err != nil {
		authFailure(w, err)
		return
	}
	w.Header().Set("Content-Security-Policy", portal.PagesPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// PagesWorkspace builds the read-only Pages document from the current snapshot.
// Group bounds come from AggregateAlerts. Controls come from UnconfiguredStore
// and stay disconnected. This does not save, mute, or escalate.
func PagesWorkspace(snapshot pages.AlertmanagerSnapshot, disabled bool) portal.PagesWorkspace {
	agg, aggErr := pages.AggregateAlerts(snapshot)
	prefs, rev, loadErr := (pages.UnconfiguredStore{}).Load(context.Background(), pages.Principal{})
	condition := pagesCondition(agg, aggErr, disabled)
	groups := []portal.PagesGroup{}
	if condition == "available" {
		groups = buildPagesGroups(snapshot, agg.Groups)
	}
	return portal.PagesWorkspace{
		CallerCoverage: snapshot.Coverage,
		Coverage:       agg.Coverage,
		Condition:      condition,
		Availability:   agg.Availability,
		ObservedAt:     agg.ObservedAt,
		Source:         agg.Source,
		Groups:         groups,
		Controls:       unconfiguredControls(prefs, rev, loadErr, pagesTypeControls(groups)),
	}
}

func pagesCondition(agg pages.AlertAggregate, aggErr error, disabled bool) string {
	switch {
	case disabled:
		return "disabled"
	case aggErr != nil:
		return "invalid"
	case agg.Availability == "Available" && len(agg.Groups) == 0:
		return "empty"
	case agg.Availability == "Available":
		return "available"
	case agg.Availability == "Unavailable":
		return "unavailable"
	default:
		return "invalid"
	}
}

func unconfiguredControls(prefs pages.Preferences, rev pages.Revision, err error, types []portal.PagesTypeControl) portal.PagesControls {
	controls := portal.DisconnectedPagesControls(types)
	if errors.Is(err, pages.ErrNotConfigured) && rev == "" && prefs.Version == 0 && len(prefs.Alerts) == 0 && len(prefs.Types) == 0 {
		return controls
	}
	controls.DocumentLoaded = false
	return controls
}

func buildPagesGroups(snapshot pages.AlertmanagerSnapshot, groups []pages.AlertGroup) []portal.PagesGroup {
	out := make([]portal.PagesGroup, 0, len(groups))
	for i, group := range groups {
		nameDisplay, nameEmpty := presentLabel(group.AlertName, group.AlertNameKnown)
		severityDisplay, severityEmpty := presentLabel(group.Severity, group.SeverityKnown)
		out = append(out, portal.PagesGroup{
			ID:               fmt.Sprintf("pages-group-%d", i+1),
			AlertName:        group.AlertName,
			AlertNameKnown:   group.AlertNameKnown,
			AlertNameDisplay: nameDisplay,
			AlertNameEmpty:   nameEmpty,
			Severity:         group.Severity,
			SeverityKnown:    group.SeverityKnown,
			SeverityDisplay:  severityDisplay,
			SeverityEmpty:    severityEmpty,
			Count:            group.Count,
			EarliestStart:    group.EarliestStart,
			LatestUpdate:     group.LatestUpdate,
			Alerts:           alertsInGroup(snapshot.Alerts, group),
		})
	}
	markSelectedGroup(out, snapshot.Selection)
	return out
}

func markSelectedGroup(groups []portal.PagesGroup, selection string) {
	if len(groups) == 0 {
		return
	}
	chosen := 0
	if selection != "" {
		for i, group := range groups {
			for _, alert := range group.Alerts {
				if alert.Fingerprint == selection {
					chosen = i
				}
			}
		}
	}
	groups[chosen].Default = true
}

func alertsInGroup(alerts []pages.AlertmanagerAlert, group pages.AlertGroup) []portal.PagesAlert {
	out := []portal.PagesAlert{}
	for _, alert := range alerts {
		if !samePagesGroup(alert, group) {
			continue
		}
		name, nameOK := labelState(alert.Labels, "alertname")
		display, empty := presentLabel(name, nameOK)
		out = append(out, portal.PagesAlert{
			Fingerprint:  alert.Fingerprint,
			Name:         name,
			NameKnown:    nameOK,
			NameDisplay:  display,
			NameEmpty:    empty,
			Status:       textOrUnknown(alert.Status),
			StartsAt:     displayInstant(alert.StartsAt),
			EndsAt:       displayInstant(alert.EndsAt),
			UpdatedAt:    displayInstant(alert.UpdatedAt),
			Summary:      alert.Summary,
			Description:  alert.Description,
			Disposition:  textOrUnknown(alert.Disposition),
			Recurrence:   textOrUnknown(alert.Recurrence),
			Labels:       labelPairs(alert.Labels),
			GeneratorURL: alert.GeneratorURL,
			RunbookURL:   alert.RunbookURL,
		})
	}
	return out
}

func samePagesGroup(alert pages.AlertmanagerAlert, group pages.AlertGroup) bool {
	name, nameOK := labelState(alert.Labels, "alertname")
	severity, severityOK := labelState(alert.Labels, "severity")
	return nameOK == group.AlertNameKnown && severityOK == group.SeverityKnown && name == group.AlertName && severity == group.Severity
}

func labelState(labels map[string]string, key string) (string, bool) {
	if labels == nil {
		return "", false
	}
	value, ok := labels[key]
	if !ok {
		return "", false
	}
	return value, true
}

func labelPairs(labels map[string]string) []portal.PagesPair {
	if len(labels) == 0 {
		return []portal.PagesPair{}
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]portal.PagesPair, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, portal.PagesPair{Key: key, Value: labels[key]})
	}
	return pairs
}

func pagesTypeControls(groups []portal.PagesGroup) []portal.PagesTypeControl {
	out := []portal.PagesTypeControl{}
	seen := map[string]struct{}{}
	for _, group := range groups {
		key := fmt.Sprintf("%t\x00%s", group.AlertNameKnown, group.AlertName)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, portal.PagesTypeControl{
			AlertName:      group.AlertName,
			AlertNameKnown: group.AlertNameKnown,
			Label:          group.AlertNameDisplay,
			Desired:        portal.PagesDesiredUnset,
			Applied:        portal.PagesAppliedState,
		})
	}
	return out
}

func presentLabel(value string, known bool) (string, bool) {
	if !known {
		return "Unknown", false
	}
	if value == "" {
		return "Empty", true
	}
	return value, false
}

func textOrUnknown(value string) string {
	if value == "" {
		return "Unknown"
	}
	return value
}

// displayInstant renders one alert field in UTC. Empty, unparsable, before
// 2000-01-01T00:00:00Z, and UTC years after 9999 are Unknown. Group bounds
// come from AggregateAlerts, which owns snapshotTimeFloor.
func displayInstant(value string) string {
	if value == "" {
		return "Unknown"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "Unknown"
	}
	parsed = parsed.UTC().Round(0)
	if parsed.Before(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) || parsed.Year() > 9999 {
		return "Unknown"
	}
	return parsed.Format(time.RFC3339Nano)
}
