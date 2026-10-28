package agentrun

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
)

func pagesConfigMap(t *testing.T, d *pages.Document) string {
	t.Helper()
	data := map[string]string{"state.json": `{"version":1}`}
	if d != nil {
		raw, err := d.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		data[pages.ConfigMapKey] = string(raw)
	}
	out, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": "dev-env-keeper-notices"}, "data": data})
	return string(out)
}

func samplePages(t *testing.T) *pages.Document {
	t.Helper()
	d := pages.New()
	t0 := time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC) // before the spring DST change
	add := func(key, title string, at time.Time, o pages.Outcome, status int) {
		if _, err := d.Begin(pages.Page{Key: key, Correlation: "claude-1", Sender: "dev-env-keeper", Source: "notices/critical", Title: title, Body: "line one\nline two", Priority: 1, URL: "https://claude.ai/code", URLTitle: "Open"}, at); err != nil {
			t.Fatal(err)
		}
		d.Finish(key, o, status, "req-abc123", at)
	}
	add("critical:claude:1", "Keeper needs login (claude)", t0, pages.OutcomeSent, 200)
	add("warning:claude:2026-03-09:lapse-2026-03-12", "Claude login lapses in 3 days", t0.Add(48*time.Hour), pages.OutcomeAmbiguous, 503)
	add("resolved:claude:1", "Keeper login restored (claude)", t0.Add(72*time.Hour), pages.OutcomeFailed, 400)
	return d
}

func TestPagesListShowsCoverageAndBothZones(t *testing.T) {
	h := newHarness(t)
	h.env.Stdin = strings.NewReader(pagesConfigMap(t, samplePages(t)))
	h.mustRun(ExitOK, "pages", "list")
	out := h.stdout.String()
	contains(t, "list", out,
		"keeper pages only", "NOT covered", "Alertmanager", "Gatus", "dev-env-ops", "Unknown",
		"recorded since 2026-03-08 06:30:00 UTC (2026-03-08 01:30:00 EST -05:00)",
		"2026-03-10 06:30:00 UTC (2026-03-10 02:30:00 EDT -04:00)", // after the DST change
		"sent", "ambiguous", "failed", "key critical:claude:1")
	if !strings.HasPrefix(out, "KEEPER PAGES ONLY - not all Pushover history.") {
		t.Errorf("the first line is not the banner:\n%s", out)
	}
	if strings.Contains(out, "was delivered") || strings.Contains(out, "are delivered") {
		t.Errorf("claims delivery:\n%s", out)
	}
	// Newest first.
	if strings.Index(out, "Keeper login restored") > strings.Index(out, "Keeper needs login") {
		t.Errorf("not newest first:\n%s", out)
	}
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("an offline command made %d API requests", n)
	}
}

func TestPagesListFiltersAndJSON(t *testing.T) {
	h := newHarness(t)
	in := pagesConfigMap(t, samplePages(t))
	h.env.Stdin = strings.NewReader(in)
	h.mustRun(ExitOK, "pages", "list", "--state", "failed", "-o", "json")
	var got pagesListJSON
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, h.stdout.String())
	}
	if len(got.Records) != 1 || got.Records[0].State != pages.StateFailed || got.Records[0].RecordedAtDisplay == "" {
		t.Fatalf("%+v", got.Records)
	}
	if len(got.Coverage.NotCovered) == 0 || got.Coverage.RecordingSince == nil || len(got.Coverage.Statement) == 0 {
		t.Fatalf("coverage: %+v", got.Coverage)
	}
	h.env.Stdin = strings.NewReader(in)
	h.mustRun(ExitOK, "pages", "list", "--limit", "1", "--sender", "dev-env-keeper")
	if strings.Count(h.stdout.String(), "\n    key ") != 1 {
		t.Errorf("limit:\n%s", h.stdout.String())
	}
	// A filter that matches nothing says so without claiming no page was sent.
	h.env.Stdin = strings.NewReader(in)
	h.mustRun(ExitOK, "pages", "list", "--sender", "alertmanager")
	contains(t, "empty", h.stdout.String(), "No recorded page matches", "not proof that no page was sent")
	// --since takes a duration or a time; both before every sample here.
	h.env.Stdin = strings.NewReader(in)
	h.mustRun(ExitOK, "pages", "list", "--since", "2026-03-11T00:00:00Z")
	if strings.Count(h.stdout.String(), "\n    key ") != 1 {
		t.Errorf("since:\n%s", h.stdout.String())
	}
}

func TestPagesUnknownWhenThereIsNoRecord(t *testing.T) {
	h := newHarness(t)
	h.env.Stdin = strings.NewReader(pagesConfigMap(t, nil))
	h.mustRun(ExitOK, "pages", "list")
	contains(t, "no key", h.stdout.String(), "Unknown: the ConfigMap has no pages.json yet", "first keeper image that has this code", "NOT covered")
	if strings.Contains(h.stdout.String(), "No pages") || strings.Contains(h.stdout.String(), "no pages were") {
		t.Errorf("claims there were no pages:\n%s", h.stdout.String())
	}
	h.env.Stdin = strings.NewReader(pagesConfigMap(t, nil))
	h.mustRun(ExitOK, "pages", "list", "-o", "json")
	var got pagesListJSON
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil || got.Unknown == "" || len(got.Records) != 0 || len(got.Coverage.NotCovered) == 0 {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestPagesShow(t *testing.T) {
	h := newHarness(t)
	in := pagesConfigMap(t, samplePages(t))
	h.env.Stdin = strings.NewReader(in)
	h.mustRun(ExitOK, "pages", "show", "critical:claude:1")
	contains(t, "show", h.stdout.String(), "keeper pages only", "state:", "sent", "dev-env-keeper", "notices/critical", "claude-1",
		"Keeper needs login (claude)", "https://claude.ai/code", "req-abc123", "  line one", "  line two",
		"2026-03-08 06:30:00 UTC (2026-03-08 01:30:00 EST -05:00)")
	// Flags may follow the key.
	h.env.Stdin = strings.NewReader(in)
	h.mustRun(ExitOK, "pages", "show", "critical:claude:1", "-o", "json")
	contains(t, "json banner", h.stdout.String(), "KEEPER PAGES ONLY")
	// An unknown key is Unknown, exit 4, never "no page".
	h.env.Stdin = strings.NewReader(in)
	h.mustRun(ExitNotFound, "pages", "show", "critical:claude:999")
	contains(t, "unknown key", h.stdout.String(), "Unknown", "not proof that no such page was sent", "NOT covered")
}

func TestPagesFromFileAndUsage(t *testing.T) {
	h := newHarness(t)
	path := h.write("cm.json", pagesConfigMap(t, samplePages(t)))
	h.mustRun(ExitOK, "pages", "list", "--from-file", path)
	// A bare pages.json is accepted too.
	raw, _ := samplePages(t).Marshal()
	h.mustRun(ExitOK, "pages", "list", "--from-file", h.write("pages.json", string(raw)))
	contains(t, "bare", h.stdout.String(), "key critical:claude:1")

	h.mustRun(ExitOK, "pages") // no verb: the help, as maintenance does
	h.mustRun(ExitUsage, "pages", "delete")
	h.mustRun(ExitUsage, "pages", "show")
	h.mustRun(ExitUsage, "pages", "list", "extra")
	h.mustRun(ExitUsage, "pages", "list", "--state", "delivered", "--from-file", path)
	h.mustRun(ExitUsage, "pages", "list", "--limit", "0", "--from-file", path)
	h.mustRun(ExitUsage, "pages", "list", "--since", "yesterday-ish", "--from-file", path)
	h.mustRun(ExitUsage, "pages", "list", "-o", "yaml", "--from-file", path)
	h.mustRun(ExitUsage, "pages", "list", "--from-file", h.dir+"/missing.json")
	// Damaged input is an error, never an empty history.
	h.env.Stdin = strings.NewReader(`{"kind":"ConfigMap","data":{"pages.json":"{broken"}}`)
	h.mustRun(ExitUsage, "pages", "list")
	contains(t, "damaged", h.stderr.String(), "treat every page as Unknown")
	h.env.Stdin = strings.NewReader(`not json`)
	h.mustRun(ExitUsage, "pages", "list")
	h.env.Stdin = strings.NewReader(`{"kind":"Secret","data":{}}`)
	h.mustRun(ExitUsage, "pages", "list")
	h.mustRun(ExitOK, "pages", "--help")
	contains(t, "help", h.stdout.String(), "KEEPER PAGES ONLY", "Usage: agent-run pages list", "kubectl -n dev-env-system get cm dev-env-keeper-notices")
}

func TestPagesIsListedInHelp(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "help")
	contains(t, "help", h.stdout.String(), "agent-run pages list|show")
	h.mustRun(ExitOK, "help", "pages")
	contains(t, "help pages", h.stdout.String(), "not proof the phone received it", "not \"no page\"")
}

func TestPagesNeverShowsACredentialInTheStoredRecord(t *testing.T) {
	h := newHarness(t)
	doc := `{"version":1,"records":[{"key":"k1","state":"sent","sender":"dev-env-keeper","source":"x","recordedAt":"2026-10-28T03:21:36Z","lastAttemptAt":"2026-10-28T03:21:36Z",
	"title":"t","body":"open https://x.example/a?token=BODYSECRET and password=PWSECRET","url":"https://user:URLPASS@claude.ai/code?x=1","urlTitle":"u"}]}`
	cm, _ := json.Marshal(map[string]any{"kind": "ConfigMap", "data": map[string]string{"pages.json": doc}})
	for _, args := range [][]string{{"pages", "list"}, {"pages", "list", "-o", "json"}, {"pages", "show", "k1"}, {"pages", "show", "k1", "-o", "json"}} {
		h.env.Stdin = strings.NewReader(string(cm))
		h.mustRun(ExitOK, args...)
		out := h.stdout.String()
		for _, leak := range []string{"BODYSECRET", "PWSECRET", "URLPASS", "user:"} {
			if strings.Contains(out, leak) {
				t.Errorf("%v shows %q:\n%s", args, leak, out)
			}
		}
		if len(args) > 2 || args[1] == "show" {
			contains(t, strings.Join(args, " "), out, "REDACTED")
		}
	}
}
