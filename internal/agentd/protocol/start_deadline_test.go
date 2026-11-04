package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestParseStartDeadline(t *testing.T) {
	for raw, want := range map[string]time.Time{
		"2026-11-04T10:05:00Z":           time.Date(2026, 11, 4, 10, 5, 0, 0, time.UTC),
		"2026-11-04T10:05:00.250Z":       time.Date(2026, 11, 4, 10, 5, 0, 250e6, time.UTC),
		"2026-11-04T10:05:00.123456789Z": time.Date(2026, 11, 4, 10, 5, 0, 123456789, time.UTC),
	} {
		got, err := ParseStartDeadline(raw)
		if err != nil || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("%q: %v %v, want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"",
		" ",
		"tomorrow",
		"2026-11-04",
		"2026-11-04T10:05:00",       // no zone
		"2026-11-04T10:05:00+00:00", // UTC, but not written as UTC
		"2026-11-04T10:05:00-05:00", // not UTC
		"2026-11-04 10:05:00Z",      // not RFC 3339
		"2026-11-04T10:05:00z",      // lower-case zone
		" 2026-11-04T10:05:00Z",     // padded
		"2026-11-04T10:05:00Z\n",    // padded
		"2026-11-04T25:05:00Z",      // out of range
		"1777777777",                // Unix seconds
		"2026-11-04T10:05:00." + strings.Repeat("0", 60) + "Z", // over the size limit
	} {
		if got, err := ParseStartDeadline(raw); err == nil {
			t.Errorf("%q: accepted as %v", raw, got)
		}
	}
}
