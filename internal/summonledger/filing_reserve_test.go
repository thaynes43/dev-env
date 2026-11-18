package summonledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFilingTimestampReserveSurvivesOtherWrites(t *testing.T) {
	at := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
	e := Entry{Clock: at, extra: map[string]json.RawMessage{"padding": json.RawMessage(`""`)}, Charges: []Charge{
		{ID: FilingID("one"), At: at, Attempt: at, Hash: RequestHash("remote", "escalation", "esc-one", "urgent"), Name: "esc-one"},
		{ID: FilingID("two"), At: at, Attempt: at, Hash: RequestHash("remote", "escalation", "esc-two", "urgent"), Name: "esc-two"},
	}}
	raw, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	// Thirty-six bytes: ten for the clock, ten per attempt, three per name.
	e.extra["padding"] = json.RawMessage(`"` + strings.Repeat("p", MaxEntry-len(raw)-36) + `"`)
	raw, err = Encode(e)
	if err != nil || len(raw) != MaxEntry-36 {
		t.Fatalf("charge reserve: bytes=%d err=%v", len(raw), err)
	}
	decoded, err := Decode(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	// A different writer cannot consume one reserved byte by growing its
	// refusal counter, even though the on-wire document would still fit.
	decoded.UrgentRefusals = 10
	if _, err := Encode(decoded); err == nil {
		t.Fatal("another writer consumed the timestamp reserve")
	}
	decoded.UrgentRefusals = 0
	decoded.Clock = at.Add(123456789 * time.Nanosecond)
	for i := range decoded.Charges {
		decoded.Charges[i].Attempt = decoded.Clock
		decoded.Charges[i].Name += "-99"
	}
	marked, err := Encode(decoded)
	if err != nil || len(marked) != MaxEntry {
		t.Fatalf("longest mark: bytes=%d err=%v", len(marked), err)
	}
	for _, ch := range decoded.Charges {
		if !ch.At.Equal(at) {
			t.Fatal("mark moved charge time")
		}
	}
}
