package pages

import (
	"strings"
	"testing"
)

const sampleAlert = `[{"fingerprint":"f1","labels":{"alertname":"<script>x</script>"},"annotations":{"summary":"hello","runbook_url":"https://example.org/run?token=secret"},"status":{"state":"active"},"startsAt":"2026-10-28T00:00:00Z","endsAt":"2026-10-29T00:00:00Z","generatorURL":"javascript:alert(1)"}]`

func TestAlertmanagerSnapshotAndHTML(t *testing.T) {
	s, err := ParseAlertmanager([]byte(sampleAlert))
	if err != nil {
		t.Fatal(err)
	}
	if s.Alerts[0].Status != "active" || s.Alerts[0].Disposition != "Unknown" || s.Alerts[0].GeneratorURL != "" || s.Alerts[0].RunbookURL != "https://example.org/run" {
		t.Fatalf("unexpected alert: %+v", s.Alerts[0])
	}
	var b strings.Builder
	if err := RenderAlertmanagerHTML(&b, s); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "<script>") || strings.Contains(b.String(), "secret") || !strings.Contains(b.String(), "&lt;script&gt;") {
		t.Fatal(b.String())
	}
}
func TestAlertmanagerInvalidAndEmpty(t *testing.T) {
	for _, input := range []string{"", `null`, `{}`, `[{"fingerprint":"x"}]`, strings.Replace(sampleAlert, `"active"`, `"resolved"`, 1), strings.TrimSuffix(sampleAlert, "]") + "," + strings.TrimPrefix(sampleAlert, "["), strings.Repeat("x", AlertmanagerMaxBytes+1)} {
		s, err := ParseAlertmanager([]byte(input))
		if err == nil || s.Availability != "Unavailable" {
			t.Fatalf("accepted invalid input %q", input[:min(len(input), 40)])
		}
	}
	s, err := ParseAlertmanager([]byte(`[]`))
	if err != nil || s.Availability != "Available" || len(s.Alerts) != 0 {
		t.Fatal(s, err)
	}
}

func TestAlertmanagerEnvelopeAndBounds(t *testing.T) {
	for _, state := range []string{"active", "suppressed", "unprocessed"} {
		input := strings.Replace(sampleAlert, `"active"`, `"`+state+`"`, 1)
		s, err := ParseAlertmanager([]byte(`{"observedAt":"2026-10-28T00:00:00Z","source":"local","alerts":` + input + `}`))
		if err != nil || s.Alerts[0].Status != state || s.ObservedAt == "Unknown" {
			t.Fatal(state, err)
		}
	}
	for _, input := range []string{strings.Replace(sampleAlert, "hello", strings.Repeat("a", 8193), 1), `{"observedAt":"bad","source":"local","alerts":[]}`} {
		if _, err := ParseAlertmanager([]byte(input)); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}

func TestAlertmanagerRejectsAmbiguousJSONAndPreservesGraphLink(t *testing.T) {
	for _, input := range []string{
		strings.Replace(sampleAlert, `"fingerprint":"f1"`, `"fingerprint":"f1","fingerprint":"f2"`, 1),
		strings.Replace(sampleAlert, `"fingerprint":"f1"`, `"fingerprint":"f1","Fingerprint":"f2"`, 1),
		strings.Replace(sampleAlert, `"alertname"`, `"alert\u0001name"`, 1),
	} {
		if _, err := ParseAlertmanager([]byte(input)); err == nil {
			t.Fatal("ambiguous input accepted")
		}
	}
	got := safeAlertURL("https://prom.example/graph?g0.expr=up%3D0&g0.tab=1&token=secret#fragment")
	if !strings.Contains(got, "g0.expr=up%3D0") || !strings.Contains(got, "g0.tab=1") || strings.Contains(got, "secret") {
		t.Fatal(got)
	}
}

func TestAlertmanagerStructuralAliasesAndNulls(t *testing.T) {
	for _, input := range []string{
		strings.Replace(sampleAlert, `"status"`, `"ſtatus"`, 1),
		strings.Replace(sampleAlert, `"status":{`, `"ſtatus":{"state":"suppressed"},"status":{`, 1),
		strings.Replace(sampleAlert, `"state"`, `"ſtate"`, 1),
		strings.Replace(sampleAlert, `"state":"active"`, `"state":"active","ſtate":"suppressed"`, 1),
		strings.Replace(sampleAlert, `"startsAt":"2026-10-28T00:00:00Z"`, `"startsAt":null`, 1),
		strings.Replace(sampleAlert, `"generatorURL":"javascript:alert(1)"`, `"generatorURL":null`, 1),
		strings.Replace(sampleAlert, `"alertname":"<script>x</script>"`, `"alertname":null`, 1),
		strings.Replace(sampleAlert, `"summary":"hello"`, `"summary":null`, 1),
		strings.Replace(sampleAlert, `"annotations":{"summary":"hello","runbook_url":"https://example.org/run?token=secret"}`, `"annotations":null`, 1),
		strings.Replace(sampleAlert, `"labels":{"alertname":"<script>x</script>"}`, `"labels":null`, 1),
		strings.Replace(sampleAlert, `"status":{"state":"active"}`, `"status":null`, 1),
	} {
		if s, err := ParseAlertmanager([]byte(input)); err == nil || s.Availability != "Unavailable" {
			t.Fatalf("accepted %s", input)
		}
	}
	input := `[{"fingerprint":"f","labels":{"Name":"one","name":"two"},"status":{"state":"active"},"annotations":{"summary":"ok"}}]`
	s, err := ParseAlertmanager([]byte(input))
	if err != nil || s.Alerts[0].Labels["Name"] != "one" || s.Alerts[0].Labels["name"] != "two" {
		t.Fatal(s, err)
	}
}
