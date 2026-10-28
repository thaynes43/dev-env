package pages

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 3, 21, 36, 0, time.UTC)

func page(key string) Page {
	return Page{Key: key, Correlation: "claude-1", Sender: "dev-env-keeper", Source: "notices/critical",
		Title: "Keeper needs login (claude)", Body: "Claude sign-in is needed.", Priority: 1,
		URL: "https://claude.ai/code", URLTitle: "Open the dev-env standby"}
}

func TestBeginIsIdempotentAndCountsAttempts(t *testing.T) {
	d := New()
	created, err := d.Begin(page("critical:claude:1"), t0)
	if err != nil || !created {
		t.Fatalf("first: %v %v", created, err)
	}
	created, err = d.Begin(page("critical:claude:1"), t0.Add(time.Minute))
	if err != nil || created {
		t.Fatalf("replay created a record: %v %v", created, err)
	}
	if len(d.Records) != 1 || d.Records[0].Attempts != 2 || !d.Records[0].LastAttemptAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("replay: %+v", d.Records)
	}
	if !d.Records[0].RecordedAt.Equal(t0) {
		t.Fatal("replay moved recordedAt")
	}
	if _, err := d.Begin(page("bad key with spaces"), t0); err == nil {
		t.Fatal("an invalid key was accepted")
	}
}

func TestStateTransitions(t *testing.T) {
	cases := []struct {
		name     string
		outcomes []Outcome
		want     State
	}{
		{"crash after record leaves recorded", nil, StateRecorded},
		{"accepted", []Outcome{OutcomeSent}, StateSent},
		{"4xx", []Outcome{OutcomeFailed}, StateFailed},
		{"timeout", []Outcome{OutcomeAmbiguous}, StateAmbiguous},
		{"5xx then accepted", []Outcome{OutcomeAmbiguous, OutcomeSent}, StateSent},
		{"ambiguous then refused stays ambiguous", []Outcome{OutcomeAmbiguous, OutcomeFailed}, StateAmbiguous},
		{"refused then accepted", []Outcome{OutcomeFailed, OutcomeSent}, StateSent},
		{"accepted then a repeat refused stays sent", []Outcome{OutcomeSent, OutcomeFailed}, StateSent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := New()
			for i, o := range c.outcomes {
				if _, err := d.Begin(page("k1"), t0.Add(time.Duration(i)*time.Minute)); err != nil {
					t.Fatal(err)
				}
				d.Finish("k1", o, 200, "req"+string(rune('a'+i)), t0.Add(time.Duration(i)*time.Minute))
			}
			if len(c.outcomes) == 0 {
				if _, err := d.Begin(page("k1"), t0); err != nil {
					t.Fatal(err)
				}
			}
			if got := d.Find("k1").State; got != c.want {
				t.Fatalf("state %s, want %s", got, c.want)
			}
		})
	}
}

func TestAttemptInFlightKeepsBetterEvidence(t *testing.T) {
	d := New()
	_, _ = d.Begin(page("k"), t0)
	d.Finish("k", OutcomeAmbiguous, 0, "", t0)
	_, _ = d.Begin(page("k"), t0.Add(time.Minute)) // a retry; a crash here must not erase "ambiguous"
	if d.Find("k").State != StateAmbiguous {
		t.Fatalf("state %s", d.Find("k").State)
	}
	d2 := New()
	_, _ = d2.Begin(page("k"), t0)
	d2.Finish("k", OutcomeFailed, 400, "", t0)
	_, _ = d2.Begin(page("k"), t0.Add(time.Minute)) // retry after a refusal: outcome pending again
	if d2.Find("k").State != StateRecorded {
		t.Fatalf("state %s", d2.Find("k").State)
	}
}

func TestRequestIDKeptOnlyForAcceptedAndOnlyIfIDShaped(t *testing.T) {
	d := New()
	_, _ = d.Begin(page("a"), t0)
	d.Finish("a", OutcomeSent, 200, "5042853c-402d-4a18-abcc-e5aaf5a3ceba", t0)
	if d.Find("a").RequestID == "" {
		t.Fatal("request id dropped")
	}
	_, _ = d.Begin(page("b"), t0)
	d.Finish("b", OutcomeSent, 200, "has spaces and <markup>", t0)
	if d.Find("b").RequestID != "" {
		t.Fatal("a non-id string was stored")
	}
	_, _ = d.Begin(page("c"), t0)
	d.Finish("c", OutcomeFailed, 400, "5042853c", t0)
	if d.Find("c").RequestID != "" {
		t.Fatal("a refused page kept a request id")
	}
	d.Finish("missing", OutcomeSent, 200, "x", t0) // trimmed away: ignored, no panic
}

func TestRingBoundsAndTruncationMarkers(t *testing.T) {
	d := New()
	for i := 0; i < MaxRecords+7; i++ {
		_, _ = d.Begin(page("k"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+time.Duration(i).String()), t0.Add(time.Duration(i)*time.Minute))
	}
	if len(d.Records) != MaxRecords || d.DroppedCount != 7 {
		t.Fatalf("records %d dropped %d", len(d.Records), d.DroppedCount)
	}
	if d.OldestRetainedAt == nil || !d.OldestRetainedAt.Equal(t0.Add(7*time.Minute)) {
		t.Fatalf("oldestRetainedAt %v", d.OldestRetainedAt)
	}
	if d.RecordingSince == nil || !d.RecordingSince.Equal(t0) {
		t.Fatalf("recordingSince %v", d.RecordingSince)
	}
	raw, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(raw)
	if err != nil || back.DroppedCount != 7 || len(back.Records) != MaxRecords {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}

func TestByteBoundDropsOldest(t *testing.T) {
	d := New()
	big := strings.Repeat("😀", MaxBody) // 4 bytes each: 100 records exceed the byte bound
	for i := 0; i < MaxRecords; i++ {
		p := page("k" + time.Duration(i).String())
		p.Body = big
		_, _ = d.Begin(p, t0.Add(time.Duration(i)*time.Minute))
	}
	raw, _ := json.Marshal(d)
	if len(raw) > MaxBytes {
		t.Fatalf("document is %d bytes, bound %d", len(raw), MaxBytes)
	}
	if d.DroppedCount == 0 {
		t.Fatal("nothing was dropped to meet the byte bound")
	}
	if d.OldestRetainedAt == nil || d.OldestRetainedAt.Equal(t0) {
		t.Fatal("oldest marker not advanced")
	}
}

func TestClampingTitleBodyAndLink(t *testing.T) {
	p := page("k")
	p.Title = strings.Repeat("t", MaxTitle+40)
	p.Body = strings.Repeat("é", MaxBody+5) // multi-byte: cut on characters, not bytes
	d := New()
	_, _ = d.Begin(p, t0)
	r := d.Records[0]
	if len([]rune(r.Title)) != MaxTitle || len([]rune(r.Body)) != MaxBody || !r.Truncated {
		t.Fatalf("title %d body %d truncated %v", len([]rune(r.Title)), len([]rune(r.Body)), r.Truncated)
	}
	for i, bad := range []string{"http://claude.ai/code", "https://u:p@claude.ai/", "https://claude.ai/code?token=abc", "javascript:alert(1)", "https://claude.ai/" + strings.Repeat("a", MaxURL)} {
		q := page("link" + string(rune('a'+i)))
		q.URL = bad
		_, _ = d.Begin(q, t0)
		if got := d.Find(q.Key); got.URL != Redacted {
			t.Errorf("link %q stored as %q", bad, got.URL)
		}
	}
	q := page("invalid-utf8")
	q.Title = "a\xffb"
	_, _ = d.Begin(q, t0)
	if !strings.Contains(d.Find("invalid-utf8").Title, "�") {
		t.Error("invalid UTF-8 not replaced")
	}
}

// No field of a record can carry a credential, and a credential in the
// sender's own values never reaches a record because the sender passes none.
func TestNoSecretFieldIsStored(t *testing.T) {
	allowed := map[string]bool{"key": true, "correlation": true, "sender": true, "source": true, "provenance": true,
		"title": true, "body": true, "truncated": true, "priority": true, "url": true, "urlTitle": true,
		"recordedAt": true, "lastAttemptAt": true, "state": true, "attempts": true, "sentCount": true,
		"ambiguousCount": true, "requestId": true, "httpStatus": true}
	rt := reflect.TypeOf(Record{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if !allowed[name] {
			t.Errorf("unreviewed record field %q", name)
		}
		for _, bad := range []string{"token", "secret", "password", "user", "credential", "auth"} {
			if strings.Contains(strings.ToLower(name), bad) {
				t.Errorf("record field %q looks like a credential", name)
			}
		}
	}
	d := New()
	p := page("k")
	_, _ = d.Begin(p, t0)
	d.Finish("k", OutcomeSent, 200, "req123", t0)
	raw, _ := d.Marshal()
	for _, bad := range []string{`"token"`, `"user"`, `"secret"`, `"password"`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("document holds %s", bad)
		}
	}
}

func TestParseRefusesDamagedDocuments(t *testing.T) {
	good := New()
	_, _ = good.Begin(page("k"), t0)
	raw, _ := good.Marshal()
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"not json":      `nope`,
		"wrong version": `{"version":2,"records":[]}`,
		"bad state":     `{"version":1,"records":[{"key":"k","state":"delivered","recordedAt":"2026-10-28T03:21:36Z"}]}`,
		"duplicate key": `{"version":1,"records":[{"key":"k","state":"sent","recordedAt":"2026-10-28T03:21:36Z"},{"key":"k","state":"sent","recordedAt":"2026-10-28T03:21:36Z"}]}`,
		"no time":       `{"version":1,"records":[{"key":"k","state":"sent"}]}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestTimeShownInUTCAndNewYorkAcrossDST(t *testing.T) {
	for _, c := range []struct {
		utc  time.Time
		want string
	}{
		{time.Date(2026, 3, 8, 6, 59, 0, 0, time.UTC), "2026-03-08 06:59:00 UTC (2026-03-08 01:59:00 EST -05:00)"},
		{time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), "2026-03-08 07:00:00 UTC (2026-03-08 03:00:00 EDT -04:00)"},
		{time.Date(2026, 10, 28, 3, 21, 36, 0, time.UTC), "2026-10-28 03:21:36 UTC (2026-10-27 23:21:36 EDT -04:00)"},
		{time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC), "2026-11-01 06:00:00 UTC (2026-11-01 01:00:00 EST -05:00)"},
	} {
		if got := FormatTime(c.utc); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
}

func TestCoverageStatement(t *testing.T) {
	text := strings.Join(Coverage(nil), "\n")
	for _, w := range []string{"keeper pages only", "first keeper image", "NOT covered", "Alertmanager", "Gatus", "dev-env-ops", "Unknown", "not \"no page\"", "not proof the phone received"} {
		if !strings.Contains(text, w) {
			t.Errorf("coverage lacks %q", w)
		}
	}
	if !strings.HasPrefix(text, "KEEPER PAGES ONLY - not all Pushover history.") {
		t.Errorf("the first line is not the banner:\n%s", text)
	}
	if strings.Contains(strings.ToLower(text), "was delivered") || strings.Contains(strings.ToLower(text), "are delivered") {
		t.Error("coverage claims delivery")
	}
	d := New()
	_, _ = d.Begin(page("k"), t0)
	if !strings.Contains(strings.Join(Coverage(d), "\n"), "recorded since 2026-10-28 03:21:36 UTC") {
		t.Error("recording start missing")
	}
	d.DroppedCount = 3
	if !strings.Contains(strings.Join(Coverage(d), "\n"), "3 dropped") {
		t.Error("drop marker missing")
	}
}

func TestSelect(t *testing.T) {
	d := New()
	for i, st := range []State{StateSent, StateFailed, StateAmbiguous} {
		k := "k" + string(rune('a'+i))
		_, _ = d.Begin(page(k), t0.Add(time.Duration(i)*time.Hour))
		d.Find(k).State = st
	}
	got := d.Select(Filter{})
	if len(got) != 3 || got[0].Key != "kc" {
		t.Fatalf("newest first: %+v", got)
	}
	if got := d.Select(Filter{State: StateFailed}); len(got) != 1 || got[0].Key != "kb" {
		t.Fatalf("state: %+v", got)
	}
	if got := d.Select(Filter{Since: t0.Add(90 * time.Minute)}); len(got) != 1 {
		t.Fatalf("since: %+v", got)
	}
	if got := d.Select(Filter{Limit: 2}); len(got) != 2 {
		t.Fatalf("limit: %+v", got)
	}
}

func TestRedactURL(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"https://claude.ai/code", "https://claude.ai/code"},
		{"https://claude.ai/code?tab=2", "https://claude.ai/code?tab=2"},
		{"https://user:pass@claude.ai/code", Redacted},
		{"https://user@claude.ai/code", Redacted},
		{"https://claude.ai/code?token=abc", Redacted},
		{"https://claude.ai/code?a=1&API_KEY=abc", Redacted},
		{"https://claude.ai/code?client_secret=abc", Redacted},
		{"https://claude.ai/code?Auth=abc", Redacted},
		{"https://claude.ai/code?sig=abc", Redacted},
		{"https://claude.ai/code?x=1&password=abc", Redacted},
		{"https://claude.ai/code?%74oken=abc", Redacted},
		{"https://claude.ai/code#access_token=abc", Redacted},
		{"https://claude.ai/code?a=1;secret=abc", Redacted},
		{"http://claude.ai/code", Redacted},
		{"ftp://claude.ai/code", Redacted},
		{"not a url", Redacted},
		{"https://", Redacted},
	} {
		if got := RedactURL(c.in); got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactTextAndBodyAreRedactedBeforeStoring(t *testing.T) {
	p := page("k")
	p.Title = "see https://u:hunter2@host.example/x now"
	p.Body = "link https://claude.ai/code?token=SECRET123. then api_key=ABCDEF and Authorization: Bearer abcdefgh12345678 end; ok https://claude.ai/code."
	p.Correlation = "corr token=ZZZ"
	d := New()
	_, _ = d.Begin(p, t0)
	raw, _ := d.Marshal()
	for _, leak := range []string{"hunter2", "SECRET123", "ABCDEF", "abcdefgh12345678", "ZZZ"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("stored record leaks %q: %s", leak, raw)
		}
	}
	r := d.Records[0]
	if !strings.Contains(r.Body, "ok https://claude.ai/code.") || !strings.Contains(r.Body, Redacted) {
		t.Errorf("body: %q", r.Body)
	}
	if !strings.Contains(r.Title, Redacted) {
		t.Errorf("title: %q", r.Title)
	}
}

// Whatever wrote the stored record, reading it back shows it redacted.
func TestParseRedactsWhatWasStored(t *testing.T) {
	doc := `{"version":1,"records":[{"key":"k","state":"sent","recordedAt":"2026-10-28T03:21:36Z","lastAttemptAt":"2026-10-28T03:21:36Z",
	"title":"t","body":"go to https://x.example/a?secret=HUNTER2 now","url":"https://u:p@claude.ai/","urlTitle":"auth=ABC","requestId":"has spaces"}]}`
	d, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	r := d.Records[0]
	all := r.URL + r.URLTitle + r.Body + r.RequestID + r.Detail() + r.Summary()
	for _, leak := range []string{"HUNTER2", "u:p@", "ABC", "has spaces"} {
		if strings.Contains(all, leak) {
			t.Errorf("shown with %q: %s", leak, all)
		}
	}
	if r.URL != Redacted {
		t.Errorf("url %q", r.URL)
	}
}

func TestRedactTextCatchesColonAndJSONPairs(t *testing.T) {
	for _, c := range []struct{ in, leak string }{
		{"Authorization: Bearer abcdefgh12345678", "abcdefgh12345678"},
		{"api_key: ABCD1234", "ABCD1234"},
		{"client-secret:   S3CR3TVAL", "S3CR3TVAL"},
		{`{"token":"JSONTOKEN","ok":"fine"}`, "JSONTOKEN"},
		{`{"Password" : "JSONPW"}`, "JSONPW"},
		{`{"access_token":"AT123","x":1}`, "AT123"},
		{"x-auth-token = ABC and password=PW1", "PW1"},
	} {
		got := RedactText(c.in)
		if strings.Contains(got, c.leak) || !strings.Contains(got, Redacted) {
			t.Errorf("RedactText(%q) = %q", c.in, got)
		}
	}
	// Ordinary prose and the keeper's own fixed texts are left alone.
	for _, keep := range []string{
		"Keeper needs login (claude)",
		"Claude sign-in is needed. Until it is renewed the phone sessions cannot start. Renew from the standby session: agent-run keeper-login claude url (quickstart section 6).",
		"Claude sign-in was renewed; the keeper holds a working login again.",
		"Claude login lapses in 3 days (2026-11-16 17:51 UTC) \u2014 renew from the standby: agent-run keeper-login claude url (quickstart section 6).",
		"Open the dev-env standby", "https://claude.ai/code", "Opened: 12:30 UTC; design review: tomorrow",
	} {
		if got := RedactText(keep); got != keep {
			t.Errorf("RedactText changed %q to %q", keep, got)
		}
	}
}

// The limits of the heuristic, written down so they are not mistaken for coverage.
func TestRedactTextKnownLimits(t *testing.T) {
	for _, miss := range []string{"ghp_abcdefghijklmnopqrstuvwxyz0123456789", "value is hunter2 now", `{"count":12345}`} {
		if RedactText(miss) != miss {
			t.Errorf("the documented limit changed for %q", miss)
		}
	}
}

func TestNewResetCountsWhatWasLost(t *testing.T) {
	now := time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC)
	d := NewReset([]byte(`{"version":1,"lostRecords":4,"records":[{"key":"a"},{"key":"b"}]}`), now)
	if d.LostRecords != 6 || d.ResetAt == nil || !d.ResetAt.Equal(now) || d.RecordingSince == nil || len(d.Records) != 0 {
		t.Fatalf("%+v", d)
	}
	if d := NewReset([]byte(`not json "key" "key"`), now); d.LostRecords != 2 {
		t.Fatalf("%d", d.LostRecords)
	}
	if d := NewReset(nil, now); d.LostRecords != 0 || d.ResetAt == nil {
		t.Fatalf("%+v", d)
	}
	raw, _ := d.Marshal()
	back, err := Parse(raw)
	if err != nil || back.ResetAt == nil {
		t.Fatalf("round trip: %v", err)
	}
	text := strings.Join(Coverage(back), "\n")
	for _, w := range []string{"damaged and reset at 2026-10-28 04:00:00 UTC", "Pages before that are Unknown"} {
		if !strings.Contains(text, w) {
			t.Errorf("coverage lacks %q", w)
		}
	}
	if _, err := Parse([]byte(`{"version":2,"records":[]}`)); !errors.Is(err, ErrVersion) {
		t.Fatalf("version: %v", err)
	}
}

func TestCoverageStatesMatchTheHelp(t *testing.T) {
	text := strings.Join(Coverage(nil), "\n")
	for _, w := range []string{"a definite 4xx, or no request made", "unconfirmed 2xx", "keeper notify-test"} {
		if !strings.Contains(text, w) {
			t.Errorf("coverage lacks %q", w)
		}
	}
}
