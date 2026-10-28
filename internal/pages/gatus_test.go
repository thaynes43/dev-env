package pages

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var gatusAt = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

const gatusSample = `[{"key":"service-a","name":"Synthetic A","group":"Demo","results":[{"success":true,"timestamp":"2026-10-28T10:01:00Z","status":200,"duration":5},{"success":false,"timestamp":"2026-10-28T12:02:00+02:00","status":0,"duration":0}]},{"key":"service-b","name":"Synthetic B","group":"Demo","results":[]}]`

func TestGatusCurrentSnapshotOrderingAndUnknown(t *testing.T) {
	s, err := ParseGatus([]byte(gatusSample), gatusAt, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if s.Availability != "Available" || len(s.Endpoints) != 2 || s.Endpoints[0].Latest.Success == nil || *s.Endpoints[0].Latest.Success || s.Endpoints[0].Latest.Status == nil || *s.Endpoints[0].Latest.Status != 0 || s.Endpoints[0].Latest.DurationNanos == nil || *s.Endpoints[0].Latest.DurationNanos != 0 || s.Endpoints[0].Latest.Timestamp != "2026-10-28T10:02:00Z" || s.Endpoints[1].Latest.Success != nil {
		t.Fatalf("unexpected snapshot %+v", s)
	}
	if !strings.Contains(s.Coverage, "not notification") || !strings.Contains(s.Coverage, "Unknown") {
		t.Fatal(s.Coverage)
	}
}
func TestGatusEqualInstantsAndConflicts(t *testing.T) {
	a := `[{"key":"x","results":[{"timestamp":"2026-10-28T10:00:00Z","success":false},{"timestamp":"2026-10-28T12:00:00+02:00","success":false}]}]`
	b := `[{"key":"x","results":[{"timestamp":"2026-10-28T12:00:00+02:00","success":false},{"timestamp":"2026-10-28T10:00:00Z","success":false}]}]`
	x, e := ParseGatus([]byte(a), gatusAt, "fixture")
	if e != nil {
		t.Fatal(e)
	}
	y, e := ParseGatus([]byte(b), gatusAt, "fixture")
	if e != nil || !reflect.DeepEqual(x, y) {
		t.Fatal("permutation changed result", e)
	}
	for _, bad := range []string{strings.Replace(a, `"success":false}]`, `"success":true}]`, 1), strings.Replace(a, `"success":false}]`, `"success":false,"status":0}]`, 1)} {
		if s, e := ParseGatus([]byte(bad), gatusAt, "fixture"); e == nil || s.Availability != "Unavailable" {
			t.Fatal("conflict accepted")
		}
	}
}
func TestGatusFailuresAndEmpty(t *testing.T) {
	s, e := ParseGatus([]byte(`[]`), gatusAt, "fixture")
	if e != nil || s.Availability != "Available" || len(s.Endpoints) != 0 {
		t.Fatal(s, e)
	}
	for _, bad := range []string{"", `null`, `{}`, `[] []`, `[{"key":"x"},{"key":"x"}]`, `[{"key":null}]`, `[{"key":"x","results":[{"success":null,"timestamp":"2026-10-28T10:00:00Z"}]}]`, `[{"key":"x","results":[{"success":false,"timestamp":null}]}]`, `[{"key":"x","results":[{"success":false,"timestamp":"bad"}]}]`, `[{"key":"x","key":"y"}]`, `[{"key":"x","Key":"y"}]`, `[{"key":"x","results":[{"ſuccess":true,"timestamp":"2026-10-28T10:00:00Z"}]}]`, strings.Repeat("x", 2<<20+1)} {
		if got, e := ParseGatus([]byte(bad), gatusAt, "fixture"); e == nil || got.Availability != "Unavailable" {
			t.Fatalf("accepted %q", bad[:min(len(bad), 50)])
		}
	}
}
func TestGatusRedactionAndDiscard(t *testing.T) {
	raw := `[{"key":"safe-key","name":"token=secret https://example.invalid/path?token=secret","group":"\u001bgroup","results":[{"timestamp":"2026-10-28T10:00:00Z","success":true,"hostname":"PRIVATE-HOST","errors":["PRIVATE-ERROR"],"conditionResults":[{"message":"PRIVATE-CONDITION"}]}]}]`
	s, e := ParseGatus([]byte(raw), gatusAt, "fixture")
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(s)
	for _, secret := range []string{"secret", "example.invalid", "PRIVATE-HOST", "PRIVATE-ERROR", "PRIVATE-CONDITION", "\\u001b"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("discard/redaction failed")
		}
	}
}

func TestGatusBoundsAndTypes(t *testing.T) {
	endpoint := `{"key":"x","results":[{"timestamp":"2026-10-28T10:00:00Z","success":true}]}`
	result := `{"timestamp":"2026-10-28T10:00:00Z","success":true}`
	manyEndpoints := "[" + strings.Repeat(endpoint+",", 1000) + endpoint + "]"
	tooManyResults := `[{"key":"x","results":[` + strings.Repeat(result+",", 4000) + result + `]}]`
	totalResults := `[{"key":"x","results":[` + strings.Repeat(result+",", 2000) + result + `]},{"key":"y","results":[` + strings.Repeat(result+",", 2000) + result + `]}]`
	deep := `[{"key":"x","extra":` + strings.Repeat("[", 17) + `0` + strings.Repeat("]", 17) + `}]`
	bad := []string{manyEndpoints, tooManyResults, totalResults, deep, `[{"key":"x","name":null}]`, `[{"key":"x","results":[{"timestamp":"2026-10-28T10:00:00Z"}]}]`, `[{"key":"x","results":[{"timestamp":"2026-10-28T10:00:00Z","success":true,"status":null}]}]`, `[{"key":"x","results":[{"timestamp":"2026-10-28T10:00:00Z","success":true,"duration":-1}]}]`, `[{"key":"x","results":[{"timestamp":"2026-10-28T10:00:00Z","success":true,"duration":1.5}]}]`, `[{"key":"x","results":[{"timestamp":"2026-10-28T10:00:00Z","success":true,"status":"200"}]}]`}
	for i, input := range bad {
		if got, err := ParseGatus([]byte(input), gatusAt, "fixture"); err == nil || got.Availability != "Unavailable" {
			t.Fatalf("case %d accepted", i)
		}
	}
	if _, err := ParseGatus([]byte(`[]`), time.Time{}, "fixture"); err == nil {
		t.Fatal("zero observation time accepted")
	}
	if _, err := ParseGatus([]byte(`[]`), gatusAt, ""); err == nil {
		t.Fatal("missing source accepted")
	}
}
func TestGatusMissingResultsAndCaseSensitiveKeys(t *testing.T) {
	s, err := ParseGatus([]byte(`[{"key":"A","name":""},{"key":"a","results":null}]`), gatusAt, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Endpoints) != 2 || s.Endpoints[0].Name != "" || s.Endpoints[1].Name != "Unknown" || s.Endpoints[0].Latest.Success != nil || s.Endpoints[1].Latest.Status != nil {
		t.Fatal("unknown or case-sensitive key lost")
	}
}

func TestGatusEndpointPermutationInvariant(t *testing.T) {
	a := `[{"key":"z","results":[]},{"key":"a","results":[]}]`
	b := `[{"key":"a","results":[]},{"key":"z","results":[]}]`
	first, err := ParseGatus([]byte(a), gatusAt, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseGatus([]byte(b), gatusAt, "fixture")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("endpoint order affected snapshot")
	}
}
