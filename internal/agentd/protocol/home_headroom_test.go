package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var headroomAt = time.Date(2026, 11, 3, 22, 0, 0, 0, time.UTC)

func okHeadroom() HomeHeadroom {
	return HomeHeadroom{Version: HomeHeadroomVersion, State: HeadroomOK, ObservedAt: headroomAt,
		Bytes:  &HeadroomFigure{State: HeadroomOK, Total: 100, Available: 90},
		Inodes: &HeadroomFigure{State: HeadroomOK, Total: 1000, Available: 900}}
}

func TestHomeHeadroomValid(t *testing.T) {
	if !okHeadroom().Valid() {
		t.Fatal("a consistent ok observation must be valid")
	}
	cases := map[string]func(h *HomeHeadroom){
		"wrong version":           func(h *HomeHeadroom) { h.Version = 2 },
		"zero time":               func(h *HomeHeadroom) { h.ObservedAt = time.Time{} },
		"unknown state word":      func(h *HomeHeadroom) { h.State = "fine" },
		"unknown figure state":    func(h *HomeHeadroom) { h.Bytes.State = "unreadable" },
		"available above total":   func(h *HomeHeadroom) { h.Bytes.Available = 101 },
		"inodes above total":      func(h *HomeHeadroom) { h.Inodes.Available = 1001 },
		"zero total":              func(h *HomeHeadroom) { h.Bytes.Total = 0 },
		"reason on ok":            func(h *HomeHeadroom) { h.Reason = HeadroomReasonIO },
		"overall hides low":       func(h *HomeHeadroom) { h.Inodes.State = HeadroomLow },
		"overall overstates":      func(h *HomeHeadroom) { h.State = HeadroomLow },
		"unknown without reason":  func(h *HomeHeadroom) { h.State, h.Inodes = HeadroomUnknown, nil },
		"unknown with both":       func(h *HomeHeadroom) { h.State, h.Reason = HeadroomUnknown, HeadroomReasonBothInvalid },
		"unreadable with figures": func(h *HomeHeadroom) { h.State, h.Reason = HeadroomUnreadable, HeadroomReasonIO },
		"unreadable bad reason": func(h *HomeHeadroom) {
			h.State, h.Reason, h.Bytes, h.Inodes = HeadroomUnreadable, "ENOENT /x", nil, nil
		},
		"unreadable no reason":    func(h *HomeHeadroom) { h.State, h.Bytes, h.Inodes = HeadroomUnreadable, nil, nil },
		"unknown with io reason":  func(h *HomeHeadroom) { h.State, h.Reason, h.Inodes = HeadroomUnknown, HeadroomReasonIO, nil },
		"bytes-invalid yet bytes": func(h *HomeHeadroom) { h.State, h.Reason, h.Inodes = HeadroomUnknown, HeadroomReasonBytesInvalid, nil },
	}
	for name, mutate := range cases {
		h := okHeadroom()
		mutate(&h)
		if h.Valid() {
			t.Errorf("%s: accepted %+v", name, h)
		}
	}
}

func TestHomeHeadroomValidAcceptsEachClosedShape(t *testing.T) {
	low := okHeadroom()
	low.State, low.Bytes.State, low.Bytes.Available = HeadroomLow, HeadroomLow, 10
	exhausted := okHeadroom()
	exhausted.State, exhausted.Inodes.State, exhausted.Inodes.Available = HeadroomExhausted, HeadroomExhausted, 0
	unreadable := HomeHeadroom{Version: HomeHeadroomVersion, State: HeadroomUnreadable, Reason: HeadroomReasonTimeout, ObservedAt: headroomAt}
	unknownInodes := HomeHeadroom{Version: HomeHeadroomVersion, State: HeadroomUnknown, Reason: HeadroomReasonInodesInvalid, ObservedAt: headroomAt,
		Bytes: &HeadroomFigure{State: HeadroomOK, Total: 100, Available: 90}}
	unknownBoth := HomeHeadroom{Version: HomeHeadroomVersion, State: HeadroomUnknown, Reason: HeadroomReasonBothInvalid, ObservedAt: headroomAt}
	lowWithMissingHalf := HomeHeadroom{Version: HomeHeadroomVersion, State: HeadroomLow, ObservedAt: headroomAt,
		Bytes: &HeadroomFigure{State: HeadroomLow, Total: 100, Available: 5}}
	for name, h := range map[string]HomeHeadroom{"low": low, "exhausted": exhausted, "unreadable": unreadable,
		"unknown inodes": unknownInodes, "unknown both": unknownBoth, "low beats a missing half": lowWithMissingHalf} {
		if !h.Valid() {
			t.Errorf("%s rejected: %+v", name, h)
		}
	}
}

func TestHomeHeadroomJSONRoundTripAndStatusField(t *testing.T) {
	h := okHeadroom()
	raw, err := json.Marshal(Status{Session: "s", HomeHeadroom: &h})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"homeHeadroom":{`, `"state":"ok"`, `"available":900`, `"observedAt":"2026-11-03T22:00:00Z"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
	var back Status
	if err := json.Unmarshal(raw, &back); err != nil || back.HomeHeadroom == nil || !back.HomeHeadroom.Valid() || back.HomeHeadroom.Inodes.Total != 1000 {
		t.Fatalf("round trip: %v %+v", err, back.HomeHeadroom)
	}
	raw, _ = json.Marshal(Status{Session: "s"})
	if strings.Contains(string(raw), "homeHeadroom") {
		t.Fatalf("an unmeasured status must omit the field: %s", raw)
	}
	// Zero available is a measurement, not an absence.
	h.Inodes.Available, h.Inodes.State, h.State = 0, HeadroomExhausted, HeadroomExhausted
	raw, _ = json.Marshal(h)
	if !strings.Contains(string(raw), `"available":0`) {
		t.Fatalf("zero must be written: %s", raw)
	}
}
