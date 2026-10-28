package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const AlertmanagerBanner = "Current Alertmanager snapshot — not notification or delivery history"
const AlertmanagerMaxBytes = 2 << 20
const alertmanagerMaxAlerts = 1000
const alertmanagerMaxString = 8192

// AlertmanagerSnapshot is caller-supplied data, not authenticated provenance.
type AlertmanagerSnapshot struct {
	Coverage     string              `json:"coverage"`
	Availability string              `json:"availability"`
	ObservedAt   string              `json:"observedAt"`
	Source       string              `json:"source"`
	Alerts       []AlertmanagerAlert `json:"alerts"`
	Selection    string              `json:"selection"`
}
type AlertmanagerAlert struct {
	Fingerprint  string            `json:"fingerprint"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	UpdatedAt    string            `json:"updatedAt"`
	Labels       map[string]string `json:"labels"`
	Summary      string            `json:"summary"`
	Description  string            `json:"description"`
	GeneratorURL string            `json:"generatorURL"`
	RunbookURL   string            `json:"runbookURL"`
	Disposition  string            `json:"disposition"`
	Recurrence   string            `json:"recurrence"`
}

const alertCoverage = "Current alerts only; notification and delivery history, triage disposition and recurrence are Unknown. Source and observation time are caller supplied."

func UnavailableAlertmanager() AlertmanagerSnapshot {
	return AlertmanagerSnapshot{Coverage: alertCoverage, Availability: "Unavailable", ObservedAt: "Unknown", Source: "Unknown", Alerts: []AlertmanagerAlert{}}
}

// ParseAlertmanager accepts the API array or an explicit snapshot envelope.
func ParseAlertmanager(raw []byte) (AlertmanagerSnapshot, error) {
	fail := func() (AlertmanagerSnapshot, error) {
		return UnavailableAlertmanager(), errors.New("invalid or over-limit Alertmanager snapshot")
	}
	if len(raw) > AlertmanagerMaxBytes {
		return fail()
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	value, parseErr := decodeAlertValue(dec, "", 0)
	if parseErr != nil || dec.Decode(new(any)) != io.EOF || !boundedAlertValue(value, 0) {
		return fail()
	}
	var entries []any
	observed, source := "Unknown", "Unknown"
	switch v := value.(type) {
	case []any:
		entries = v
	case map[string]any:
		var ok bool
		entries, ok = v["alerts"].([]any)
		if !ok {
			return fail()
		}
		observed, ok = v["observedAt"].(string)
		if !ok || !validAlertTime(observed) || observed == "" {
			return fail()
		}
		source, ok = v["source"].(string)
		if !ok || strings.TrimSpace(source) == "" || strings.Contains(source, "://") || source != cleanAlertText(source) {
			return fail()
		}
		source = cleanAlertText(source)
	default:
		return fail()
	}
	if len(entries) > alertmanagerMaxAlerts {
		return fail()
	}
	result := AlertmanagerSnapshot{Coverage: alertCoverage, Availability: "Available", ObservedAt: observed, Source: source, Alerts: make([]AlertmanagerAlert, 0, len(entries))}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !validAlertTypes(entry) {
			return fail()
		}
		b, err := json.Marshal(entry)
		if err != nil {
			return fail()
		}
		var in struct {
			Fingerprint string            `json:"fingerprint"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			Status      struct {
				State string `json:"state"`
			} `json:"status"`
			StartsAt     string `json:"startsAt"`
			EndsAt       string `json:"endsAt"`
			UpdatedAt    string `json:"updatedAt"`
			GeneratorURL string `json:"generatorURL"`
		}
		if json.Unmarshal(b, &in) != nil || in.Fingerprint == "" || in.Fingerprint != cleanAlertText(in.Fingerprint) || in.Labels == nil || seen[in.Fingerprint] || !validAlertTime(in.StartsAt) || !validAlertTime(in.EndsAt) || !validAlertTime(in.UpdatedAt) {
			return fail()
		}
		if in.Status.State != "active" && in.Status.State != "suppressed" && in.Status.State != "unprocessed" {
			return fail()
		}
		seen[in.Fingerprint] = true
		labels := make(map[string]string, len(in.Labels))
		for k, v := range in.Labels {
			if cleanAlertText(k) != k {
				return fail()
			}
			labels[k] = cleanAlertText(v)
		}
		result.Alerts = append(result.Alerts, AlertmanagerAlert{Fingerprint: cleanAlertText(in.Fingerprint), Name: labels["alertname"], Status: in.Status.State, StartsAt: in.StartsAt, EndsAt: in.EndsAt, UpdatedAt: in.UpdatedAt, Labels: labels, Summary: cleanAlertText(in.Annotations["summary"]), Description: cleanAlertText(in.Annotations["description"]), GeneratorURL: safeAlertURL(in.GeneratorURL), RunbookURL: safeAlertURL(in.Annotations["runbook_url"]), Disposition: "Unknown", Recurrence: "Unknown"})
	}
	return result, nil
}

// decodeAlertValue rejects ambiguous JSON member names before Go's struct decoder
// can fold case or silently overwrite duplicate keys. Label names remain case-sensitive.
func decodeAlertValue(d *json.Decoder, parent string, depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("nesting limit")
	}
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		folded := map[string]bool{}
		for d.More() {
			kt, e := d.Token()
			if e != nil {
				return nil, e
			}
			k, ok := kt.(string)
			if !ok {
				return nil, errors.New("invalid member")
			}
			if _, exists := obj[k]; exists {
				return nil, errors.New("duplicate member")
			}
			if parent != "labels" && parent != "annotations" {
				for _, canonical := range structuralAlertFields {
					if strings.EqualFold(k, canonical) {
						if folded[canonical] || k != canonical {
							return nil, errors.New("ambiguous member")
						}
						folded[canonical] = true
					}
				}
			}
			v, e := decodeAlertValue(d, k, depth+1)
			if e != nil {
				return nil, e
			}
			obj[k] = v
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, errors.New("invalid object")
		}
		return obj, nil
	case '[':
		var a []any
		for d.More() {
			v, e := decodeAlertValue(d, parent, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
			if len(a) > alertmanagerMaxAlerts*4 {
				return nil, errors.New("array limit")
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, errors.New("invalid array")
		}
		return a, nil
	}
	return nil, errors.New("invalid delimiter")
}

var structuralAlertFields = []string{"alerts", "observedAt", "source", "fingerprint", "labels", "annotations", "status", "state", "startsAt", "endsAt", "updatedAt", "generatorURL"}

// Present recognized fields must have their documented types; omission remains optional.
func validAlertTypes(entry any) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"fingerprint", "startsAt", "endsAt", "updatedAt", "generatorURL"} {
		if v, present := obj[key]; present {
			if _, ok := v.(string); !ok {
				return false
			}
		}
	}
	for _, key := range []string{"labels", "annotations"} {
		if v, present := obj[key]; present {
			m, ok := v.(map[string]any)
			if !ok {
				return false
			}
			for _, value := range m {
				if _, ok := value.(string); !ok {
					return false
				}
			}
		}
	}
	if v, present := obj["status"]; present {
		status, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if state, present := status["state"]; present {
			if _, ok := state.(string); !ok {
				return false
			}
		}
	}
	return true
}

func terminalAlertText(s string) string {
	var out strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			out.WriteString(strconv.QuoteRuneToASCII(r)[1 : len(strconv.QuoteRuneToASCII(r))-1])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func boundedAlertValue(v any, depth int) bool {
	if depth > 16 {
		return false
	}
	switch x := v.(type) {
	case string:
		return len(x) <= alertmanagerMaxString
	case []any:
		if len(x) > alertmanagerMaxAlerts*4 {
			return false
		}
		for _, e := range x {
			if !boundedAlertValue(e, depth+1) {
				return false
			}
		}
	case map[string]any:
		if len(x) > 1000 {
			return false
		}
		for k, e := range x {
			if len(k) > alertmanagerMaxString || !boundedAlertValue(e, depth+1) {
				return false
			}
		}
	}
	return true
}
func validAlertTime(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}
func cleanAlertText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
func safeAlertURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	q := u.Query()
	safe := url.Values{}
	for _, key := range []string{"g0.expr", "g0.tab"} {
		if values, ok := q[key]; ok {
			safe[key] = values
		}
	}
	u.RawQuery = safe.Encode()
	u.ForceQuery = false
	u.Fragment = ""
	return u.String()
}

var alertHTML = template.Must(template.New("alertmanager").Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><style>body{font-family:system-ui,sans-serif;max-width:72rem;margin:auto;padding:1rem;overflow-wrap:anywhere} article{border-top:1px solid #aaa;padding:1rem 0} p,dd{white-space:pre-wrap} dt{font-weight:bold}</style><title>Pages · Alertmanager</title></head><body><h1>Pages · Alertmanager</h1><p>{{.Banner}}</p><p>{{.Snapshot.Coverage}}</p><p>Selection: {{.Snapshot.Selection}} · Availability: {{.Snapshot.Availability}} · Observed at: {{.Snapshot.ObservedAt}} · Source: {{.Snapshot.Source}}</p>{{range .Snapshot.Alerts}}<article><h2>{{.Name}}</h2><p>Fingerprint: {{.Fingerprint}} · Status: {{.Status}}</p><p>Starts: {{.StartsAt}} · Ends: {{.EndsAt}} · Updated: {{.UpdatedAt}}</p><p>Disposition: {{.Disposition}} · Recurrence: {{.Recurrence}}</p><p>{{.Summary}}</p><p>{{.Description}}</p><dl>{{range $k,$v := .Labels}}<dt>{{$k}}</dt><dd>{{$v}}</dd>{{end}}</dl>{{if .GeneratorURL}}<a href="{{.GeneratorURL}}">Generator</a>{{else}}<span>Generator source link unavailable</span>{{end}} {{if .RunbookURL}}<a href="{{.RunbookURL}}">Runbook</a>{{else}}<span>Runbook source link unavailable</span>{{end}}</article>{{else}}<p>{{if eq .Snapshot.Availability "Available"}}No alerts in this result.{{else}}Unavailable.{{end}}</p>{{end}}</body></html>`))

func RenderAlertmanagerHTML(w io.Writer, s AlertmanagerSnapshot) error {
	return alertHTML.Execute(w, struct {
		Banner   string
		Snapshot AlertmanagerSnapshot
	}{AlertmanagerBanner, s})
}
func RenderAlertmanagerText(w io.Writer, s AlertmanagerSnapshot) error {
	if _, err := fmt.Fprintf(w, "Pages · Alertmanager\n%s\n%s\nAvailability: %s\nObserved at: %s\nSource: %s\n", AlertmanagerBanner, s.Coverage, terminalAlertText(s.Availability), terminalAlertText(s.ObservedAt), terminalAlertText(s.Source)); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "Selection: "+terminalAlertText(s.Selection))
	if len(s.Alerts) == 0 && s.Availability == "Available" {
		_, _ = fmt.Fprintln(w, "No alerts in this result.")
	}
	for _, a := range s.Alerts {
		b, _ := json.Marshal(a)
		if _, err := fmt.Fprintln(w, string(b)); err != nil {
			return err
		}
	}
	return nil
}
