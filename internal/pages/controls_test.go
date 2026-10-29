package pages

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPreferencesRoundTripAndAllowlists(t *testing.T) {
	until := time.Date(2026, 10, 29, 15, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
	input := Preferences{
		Version: PreferencesVersion,
		Alerts: []AlertPreference{
			{Fingerprint: "b", Escalate: DesiredOn, Mute: DesiredOff},
			{Fingerprint: "a<b&c>", Escalate: DesiredUnset, Mute: DesiredOn, MuteUntil: &until},
		},
		Types: []TypePreference{
			{AlertName: "diskfull", AutoResponder: DesiredOff},
			{AlertName: "DiskFull", AutoResponder: DesiredUnset},
		},
		List: ListPreference{Sort: "alertname", Filter: "all"},
	}
	originalFirst := input.Alerts[0].Fingerprint
	raw, err := MarshalPreferences(input)
	if err != nil {
		t.Fatal(err)
	}
	if input.Alerts[0].Fingerprint != originalFirst {
		t.Fatal("marshal mutated the caller")
	}
	const want = `{"version":1,"alerts":[{"fingerprint":"a<b&c>","escalate":"unset","mute":"on","muteUntil":"2026-10-29T19:00:00Z"},{"fingerprint":"b","escalate":"on","mute":"off"}],"types":[{"alertName":"DiskFull","autoResponder":"unset"},{"alertName":"diskfull","autoResponder":"off"}],"list":{"sort":"alertname","filter":"all"}}`
	if string(raw) != want {
		t.Fatalf("canonical:\n%s", raw)
	}
	parsed, err := ParsePreferences(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := MarshalPreferences(parsed)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("unstable: %s %v", again, err)
	}
	if parsed.Alerts[0].MuteUntil == nil || !parsed.Alerts[0].MuteUntil.Equal(until) || parsed.Alerts[0].MuteUntil.Location() != time.UTC {
		t.Fatalf("time: %+v", parsed.Alerts[0].MuteUntil)
	}
	reversedKeys := `{"list":{"filter":"all","sort":"severity"},"types":[{"autoResponder":"on","alertName":"NodeDown"}],"alerts":[{"mute":"off","escalate":"unset","fingerprint":"z"}],"version":1}`
	parsed, err = ParsePreferences([]byte(reversedKeys))
	if err != nil {
		t.Fatal(err)
	}
	again, err = MarshalPreferences(parsed)
	if err != nil || string(again) != `{"version":1,"alerts":[{"fingerprint":"z","escalate":"unset","mute":"off"}],"types":[{"alertName":"NodeDown","autoResponder":"on"}],"list":{"sort":"severity","filter":"all"}}` {
		t.Fatalf("reordered keys: %s %v", again, err)
	}
	for _, sortKey := range PreferenceSorts() {
		for _, filter := range PreferenceFilters() {
			body := fmt.Sprintf(`{"version":1,"alerts":[],"types":[],"list":{"sort":%q,"filter":%q}}`, sortKey, filter)
			got, err := ParsePreferences([]byte(body))
			if err != nil || got.List.Sort != sortKey || got.List.Filter != filter {
				t.Fatalf("allowlist %s %s: %+v %v", sortKey, filter, got, err)
			}
			out, err := MarshalPreferences(got)
			if err != nil || string(out) != body {
				t.Fatalf("allowlist bytes %s: %s %v", body, out, err)
			}
		}
	}
	for _, desired := range []Desired{DesiredUnset, DesiredOn, DesiredOff} {
		body := fmt.Sprintf(`{"version":1,"alerts":[{"fingerprint":"f","escalate":%q,"mute":%q}],"types":[{"alertName":"A","autoResponder":%q}],"list":{"sort":"count","filter":"active"}}`, desired, desired, desired)
		got, err := ParsePreferences([]byte(body))
		if err != nil || got.Alerts[0].Escalate != desired || got.Alerts[0].Mute != desired || got.Types[0].AutoResponder != desired || got.Alerts[0].MuteUntil != nil {
			t.Fatalf("desired %s: %+v %v", desired, got, err)
		}
	}
	escaped, err := ParsePreferences([]byte(`{"version":1,"alerts":[{"\u0066ingerprint":"a","escalate":"unset","mute":"off"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`))
	if err != nil || escaped.Alerts[0].Fingerprint != "a" {
		t.Fatal(escaped, err)
	}
	withSpace, err := ParsePreferences([]byte(want + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	stable, err := MarshalPreferences(withSpace)
	if err != nil || string(stable) != want {
		t.Fatalf("trailing space: %s %v", stable, err)
	}
	empty, err := MarshalPreferences(Preferences{Version: 1, List: ListPreference{Sort: "latestUpdate", Filter: "unprocessed"}})
	if err != nil || !strings.Contains(string(empty), `"alerts":[]`) || !strings.Contains(string(empty), `"types":[]`) {
		t.Fatal(string(empty), err)
	}
}

func TestPreferencesRejectsMalformed(t *testing.T) {
	base := `{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"off"}],"types":[{"alertName":"DiskFull","autoResponder":"on"}],"list":{"sort":"alertname","filter":"all"}}`
	cases := []string{
		`{"Version":1,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"},"extra":1}`,
		`{"version":1,"version":1,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all","ſilter":"all"}}`,
		`{"verſion":1,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		base + `{}`,
		`{"version":null,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":null,"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1.0,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1e0,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":"1","alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":0,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":2,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"alerts":[],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","fingerprint":"b","escalate":"unset","mute":"off"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","\u0066ingerprint":"b","escalate":"unset","mute":"off"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"off","muteUntil":null}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","escalate":"ON","mute":"off"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"off","muteuntil":"2026-10-29T00:00:00Z"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"off"},{"fingerprint":"a","escalate":"on","mute":"on"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[],"types":[{"alertName":"DiskFull","autoResponder":"on"},{"alertName":"DiskFull","autoResponder":"off"}],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":" a","escalate":"unset","mute":"off"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a\u0000b","escalate":"unset","mute":"off"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"` + strings.Repeat("a", MaxFingerprintBytes+1) + `","escalate":"unset","mute":"off"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[],"types":[{"alertName":"` + strings.Repeat("n", MaxAlertNameBytes+1) + `","autoResponder":"off"}],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[],"types":[],"list":{"sort":"Alertname","filter":"all"}}`,
		`{"version":1,"alerts":[],"types":[],"list":{"sort":"alertname","filter":"firing"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"off","muteUntil":"1999-12-31T23:59:59Z"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"off","muteUntil":"not-a-time"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`,
		`[]`,
		`{`,
		base[:len(base)-1],
	}
	for _, input := range cases {
		got, err := ParsePreferences([]byte(input))
		if !errors.Is(err, ErrInvalid) || got.Version != 0 || len(got.Alerts) != 0 || len(got.Types) != 0 || got.List != (ListPreference{}) {
			t.Fatalf("accepted %s -> %+v %v", clip(input), got, err)
		}
	}
	raw := []byte(base)
	raw = append(raw, 0xff)
	if _, err := ParsePreferences(raw); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid utf-8 accepted")
	}
	if _, err := ParsePreferences(bytes.Repeat([]byte(" "), MaxPreferenceBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversize accepted")
	}
	if _, err := ParsePreferences([]byte(documentWithAlerts(MaxAlertPreferences))); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePreferences([]byte(documentWithAlerts(MaxAlertPreferences + 1))); !errors.Is(err, ErrInvalid) {
		t.Fatal("201 alerts accepted")
	}
	if _, err := ParsePreferences([]byte(documentWithTypes(MaxTypePreferences))); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePreferences([]byte(documentWithTypes(MaxTypePreferences + 1))); !errors.Is(err, ErrInvalid) {
		t.Fatal("101 types accepted")
	}
	earliest, latest := PreferenceTimeBounds()
	minBody := `{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"on","muteUntil":"` + earliest.Format(time.RFC3339Nano) + `"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`
	if _, err := ParsePreferences([]byte(minBody)); err != nil {
		t.Fatal(err)
	}
	maxBody := `{"version":1,"alerts":[{"fingerprint":"a","escalate":"unset","mute":"off","muteUntil":"` + latest.Format(time.RFC3339Nano) + `"}],"types":[],"list":{"sort":"alertname","filter":"all"}}`
	if _, err := ParsePreferences([]byte(maxBody)); !errors.Is(err, ErrInvalid) {
		t.Fatal("exclusive latest bound accepted", err)
	}
	both, err := ParsePreferences([]byte(`{"version":1,"alerts":[],"types":[{"alertName":"DiskFull","autoResponder":"on"},{"alertName":"diskfull","autoResponder":"off"}],"list":{"sort":"alertname","filter":"all"}}`))
	if err != nil || both.Types[0].AlertName != "DiskFull" || both.Types[1].AlertName != "diskfull" {
		t.Fatalf("case identities: %+v %v", both.Types, err)
	}
	if _, err := MarshalPreferences(Preferences{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero value marshaled")
	}
}

func TestMemoryStoreAndUnconfigured(t *testing.T) {
	var (
		_ ControlStore = UnconfiguredStore{}
		_ ControlStore = memoryStore{}
	)
	ctx := context.Background()
	alice := Principal{Issuer: "https://issuer.example/portal/", Subject: "alice"}
	bob := Principal{Issuer: "https://issuer.example/portal/", Subject: "bob@example.com"}
	otherIssuer := Principal{Issuer: "https://other.example/", Subject: "alice"}
	store := newMemoryStore()
	reader := store.reader()
	first := preferenceDoc("count")
	rev, err := store.Save(ctx, alice, first, "")
	if err != nil || rev != "1" {
		t.Fatal(rev, err)
	}
	loaded, gotRev, err := reader.Load(ctx, alice)
	if err != nil || gotRev != rev || !samePreferences(t, loaded, first) {
		t.Fatal(gotRev, err)
	}
	loaded.List.Sort = "changed"
	if loaded.Alerts[0].MuteUntil != nil {
		*loaded.Alerts[0].MuteUntil = time.Time{}
	}
	again, _, err := reader.Load(ctx, alice)
	if err != nil || !samePreferences(t, again, first) {
		t.Fatal("load returned shared memory", err)
	}
	second := preferenceDoc("severity")
	next, err := reader.Save(ctx, alice, second, rev)
	if err != nil || next != "2" {
		t.Fatal(next, err)
	}
	if _, err := store.Save(ctx, alice, preferenceDoc("alertname"), rev); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision applied", err)
	}
	if _, _, err := store.Load(ctx, bob); !errors.Is(err, errMemoryNotFound) {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, bob, preferenceDoc("latestUpdate"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, otherIssuer, preferenceDoc("earliestStart"), ""); err != nil {
		t.Fatal(err)
	}
	aliceNow, aliceRev, err := store.Load(ctx, alice)
	if err != nil || aliceRev != "2" || !samePreferences(t, aliceNow, second) {
		t.Fatal("principal isolation failed", aliceRev, err)
	}
	if _, err := store.Save(ctx, Principal{}, first, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	long := Principal{Issuer: alice.Issuer, Subject: strings.Repeat("s", MaxPrincipalBytes+1)}
	if _, err := store.Save(ctx, long, first, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, alice, Preferences{}, next); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Save(cancelled, alice, preferenceDoc("count"), next); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := store.Load(cancelled, alice); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	alive, aliveRev, err := reader.Load(ctx, alice)
	if err != nil || aliveRev != "2" || !samePreferences(t, alive, second) {
		t.Fatal("cancelled save wrote", aliveRev, err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]struct {
		rev Revision
		err error
	}, 2)
	updates := []Preferences{preferenceDoc("count"), preferenceDoc("severity")}
	handles := []memoryStore{store.reader(), store.reader()}
	for i := range updates {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i].rev, results[i].err = handles[i].Save(ctx, alice, updates[i], next)
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for _, result := range results {
		switch {
		case result.err == nil && result.rev == "3":
			wins++
		case errors.Is(result.err, ErrConflict) && result.rev == "":
		default:
			t.Fatalf("concurrent result: %+v", result)
		}
	}
	if wins != 1 {
		t.Fatalf("wins: %d", wins)
	}
	final, finalRev, err := reader.Load(ctx, alice)
	if err != nil || finalRev != "3" {
		t.Fatal(finalRev, err)
	}
	matched := samePreferences(t, final, updates[0]) || samePreferences(t, final, updates[1])
	if !matched {
		t.Fatalf("winner not stored: %+v", final.List)
	}

	var blank ControlStore = UnconfiguredStore{}
	prefs, rev, err := blank.Load(cancelled, Principal{})
	if !errors.Is(err, ErrNotConfigured) || rev != "" || prefs.Version != 0 || prefs.Alerts != nil || prefs.Types != nil || prefs.List != (ListPreference{}) {
		t.Fatalf("unconfigured load: %+v %q %v", prefs, rev, err)
	}
	rev, err = blank.Save(ctx, alice, first, "1")
	if !errors.Is(err, ErrNotConfigured) || rev != "" {
		t.Fatal(rev, err)
	}
	state, connection := Applied()
	if state != AppliedState || connection != AppliedConnection || strings.Contains(string(mustMarshal(t, first)), "applied") {
		t.Fatal(state, connection)
	}
	if _, _, err := store.Load(ctx, alice); err != nil {
		t.Fatal(err)
	}
}

func preferenceDoc(sortKey string) Preferences {
	until := time.Date(2026, 10, 29, 19, 0, 0, 0, time.UTC)
	return Preferences{
		Version: PreferencesVersion,
		Alerts:  []AlertPreference{{Fingerprint: "f", Escalate: DesiredUnset, Mute: DesiredOn, MuteUntil: &until}},
		Types:   []TypePreference{{AlertName: "DiskFull", AutoResponder: DesiredOff}},
		List:    ListPreference{Sort: sortKey, Filter: "all"},
	}
}

func samePreferences(t *testing.T, got, want Preferences) bool {
	t.Helper()
	left, err := MarshalPreferences(got)
	if err != nil {
		t.Fatal(err)
	}
	right, err := MarshalPreferences(want)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(left, right)
}

func mustMarshal(t *testing.T, prefs Preferences) []byte {
	t.Helper()
	raw, err := MarshalPreferences(prefs)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func documentWithAlerts(n int) string {
	var b strings.Builder
	b.WriteString(`{"version":1,"alerts":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"fingerprint":"f%03d","escalate":"unset","mute":"off"}`, i)
	}
	b.WriteString(`],"types":[],"list":{"sort":"alertname","filter":"all"}}`)
	return b.String()
}

func documentWithTypes(n int) string {
	var b strings.Builder
	b.WriteString(`{"version":1,"alerts":[],"types":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"alertName":"n%03d","autoResponder":"off"}`, i)
	}
	b.WriteString(`],"list":{"sort":"alertname","filter":"all"}}`)
	return b.String()
}

func clip(s string) string {
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

type memoryRow struct {
	prefs Preferences
	rev   uint64
}

type memoryBackend struct {
	mu   sync.Mutex
	rows map[string]memoryRow
}

type memoryStore struct {
	backend *memoryBackend
}

func newMemoryStore() memoryStore {
	return memoryStore{backend: &memoryBackend{rows: map[string]memoryRow{}}}
}

func (s memoryStore) reader() memoryStore {
	return memoryStore{backend: s.backend}
}

func (s memoryStore) Load(ctx context.Context, principal Principal) (Preferences, Revision, error) {
	if err := ctx.Err(); err != nil {
		return Preferences{}, "", err
	}
	if !principal.Valid() {
		return Preferences{}, "", fmt.Errorf("%w: principal", ErrInvalid)
	}
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	row, ok := s.backend.rows[principal.Issuer+"\x00"+principal.Subject]
	if !ok {
		return Preferences{}, "", errMemoryNotFound
	}
	return clonePreferences(row.prefs), Revision(strconv.FormatUint(row.rev, 10)), nil
}

func (s memoryStore) Save(ctx context.Context, principal Principal, prefs Preferences, expected Revision) (Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !principal.Valid() {
		return "", fmt.Errorf("%w: principal", ErrInvalid)
	}
	canon, err := canonicalPreferences(prefs)
	if err != nil {
		return "", err
	}
	s.backend.mu.Lock()
	defer s.backend.mu.Unlock()
	key := principal.Issuer + "\x00" + principal.Subject
	row, ok := s.backend.rows[key]
	switch {
	case !ok && expected == "":
		row.rev = 1
	case ok && Revision(strconv.FormatUint(row.rev, 10)) == expected && row.rev != ^uint64(0):
		row.rev++
	default:
		return "", ErrConflict
	}
	row.prefs = clonePreferences(canon)
	s.backend.rows[key] = row
	return Revision(strconv.FormatUint(row.rev, 10)), nil
}

func clonePreferences(prefs Preferences) Preferences {
	out := prefs
	out.Alerts = append([]AlertPreference(nil), prefs.Alerts...)
	if out.Alerts == nil {
		out.Alerts = []AlertPreference{}
	}
	for i := range out.Alerts {
		if out.Alerts[i].MuteUntil != nil {
			stamp := *out.Alerts[i].MuteUntil
			out.Alerts[i].MuteUntil = &stamp
		}
	}
	out.Types = append([]TypePreference(nil), prefs.Types...)
	if out.Types == nil {
		out.Types = []TypePreference{}
	}
	return out
}

var errMemoryNotFound = errors.New("pages: test controls not found")
