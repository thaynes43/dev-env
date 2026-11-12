package summonledger

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// The prune keeps a charge that counts, one with a live attempt mark, and one
// whose id the caller says has an unsettled session (7233); it drops the rest.
func TestPruneKeepsUnsettledSessionsCharge(t *testing.T) {
	now := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	old := now.Add(-80 * time.Minute)
	charges := []Charge{
		{ID: "counting", At: now.Add(-time.Minute), Attempt: now.Add(-time.Minute), Hash: hash, Name: "a"},
		{ID: "live-mark", At: old, Attempt: now.Add(-14 * time.Minute), Hash: hash, Name: "b"},
		{ID: "queued", At: old, Attempt: old, Hash: hash, Name: "c"},
		{ID: "settled", At: old, Attempt: old, Hash: hash, Name: "d"},
	}
	var got []string
	for _, ch := range Prune(charges, now, map[string]bool{"queued": true}) {
		got = append(got, ch.ID)
	}
	if strings.Join(got, ",") != "counting,live-mark,queued" {
		t.Fatalf("kept %v", got)
	}
	got = nil
	for _, ch := range Prune(charges, now, nil) {
		got = append(got, ch.ID)
	}
	if strings.Join(got, ",") != "counting,live-mark" {
		t.Fatalf("kept %v with no unsettled sessions", got)
	}
}

// Binds is the admission test: the id's charge bound to this name, not
// another, and no charge binds nothing.
func TestBindsNeedsTheSameName(t *testing.T) {
	at := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	e := Entry{Clock: at, Charges: []Charge{{ID: "x", At: at, Attempt: at, Hash: hash, Name: "rem-1-2"}}}
	if !e.Binds("x", "rem-1-2") || e.Binds("x", "rem-1") || e.Binds("y", "rem-1-2") {
		t.Fatal("Binds is not exact")
	}
	raw, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(raw, true)
	if err != nil || !back.Binds("x", "rem-1-2") {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	if _, err := Decode(`{"schema":"`+Schema+`","schema":"x"}`, true); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("a repeated member: %v", err)
	}
	if e, err := Decode("", false); err != nil || len(e.Charges) != 0 {
		t.Fatalf("an absent key: %v %+v", err, e)
	}
}
