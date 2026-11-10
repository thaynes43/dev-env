package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Every rollout here is synthetic. Its token_count records follow the shape the
// coordinator read on 2026-11-09 from codex-cli 0.160.1 (DESIGN-059 4.1 and
// 11): payload.rate_limits with primary and secondary windows of
// used_percent, window_minutes and resets_at, beside plan_type and credits.
// No value comes from a real account.

var codexAllowanceAt = time.Date(2026, 11, 9, 23, 42, 10, 123_000_000, time.UTC)

func unixText(at time.Time) string { return strconv.FormatInt(at.Unix(), 10) }

// tokenCount is one synthetic token_count record. primary and secondary are
// raw JSON windows (or "null").
func tokenCount(at time.Time, primary, secondary string) string {
	return `{"timestamp":"` + at.Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10}},` +
		`"rate_limits":{"limit_id":"codex","limit_name":null,"primary":` + primary + `,"secondary":` + secondary +
		`,"credits":{"has_credits":false,"balance":"SECRET-CREDITS"},"plan_type":"SECRET-PLAN"}}}`
}

func rolloutWindow(used string, minutes int, reset time.Time) string {
	return `{"used_percent":` + used + `,"window_minutes":` + strconv.Itoa(minutes) + `,"resets_at":` + unixText(reset) + `}`
}

func weekly(used string) string {
	return rolloutWindow(used, protocol.WeeklyAllowanceMinutes, codexAllowanceAt.Add(36*time.Hour))
}

func TestCodexRolloutAllowance(t *testing.T) {
	reset := codexAllowanceAt.Add(36 * time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		lines  []string
		want   []protocol.AllowanceWindow
		reason string
	}{
		{"weekly as primary, secondary null", []string{rolloutLine("event_msg", ""), tokenCount(codexAllowanceAt, weekly("65.0"), "null")},
			[]protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: "65", ResetsAt: reset}}, ""},
		{"weekly as secondary, picked by length", []string{tokenCount(codexAllowanceAt, rolloutWindow("12.5", 300, codexAllowanceAt.Add(2*time.Hour)), weekly("40"))},
			[]protocol.AllowanceWindow{{Minutes: 300, UsedPercent: "12.5", ResetsAt: codexAllowanceAt.Add(2 * time.Hour).Truncate(time.Second)}, {Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: "40", ResetsAt: reset}}, ""},
		{"newest record wins", []string{tokenCount(codexAllowanceAt.Add(-time.Hour), weekly("10"), "null"), tokenCount(codexAllowanceAt, weekly("11"), "null"), rolloutLine("event_msg", "")},
			[]protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: "11", ResetsAt: reset}}, ""},
		{"newest record without rate limits skipped", []string{tokenCount(codexAllowanceAt, weekly("20"), "null"),
			`{"timestamp":"2026-11-09T23:50:00Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":null}}`},
			[]protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: "20", ResetsAt: reset}}, ""},
		{"window without reset left out", []string{tokenCount(codexAllowanceAt, `{"used_percent":5,"window_minutes":300,"resets_at":null}`, weekly("7"))},
			[]protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: "7", ResetsAt: reset}}, ""},
		{"both windows null", []string{tokenCount(codexAllowanceAt, "null", "null")}, nil, ""},
		{"no token count", []string{rolloutLine("event_msg", ""), rolloutLine("turn_context", `"gpt-6-astra"`)}, nil, ""},
		{"percent over 100", []string{tokenCount(codexAllowanceAt, weekly("100.5"), "null")}, nil, allowanceShapeUnrecognized},
		{"percent as a string", []string{tokenCount(codexAllowanceAt, weekly(`"65"`), "null")}, nil, allowanceShapeUnrecognized},
		{"fractional minutes", []string{tokenCount(codexAllowanceAt, `{"used_percent":1,"window_minutes":1.5,"resets_at":`+unixText(reset)+`}`, "null")}, nil, allowanceShapeUnrecognized},
		{"case-folded key", []string{tokenCount(codexAllowanceAt, `{"used_percent":1,"Used_Percent":99,"window_minutes":300,"resets_at":`+unixText(reset)+`}`, "null")}, nil, allowanceShapeUnrecognized},
		{"same length twice", []string{tokenCount(codexAllowanceAt, weekly("1"), weekly("2"))}, nil, allowanceOutOfBounds},
		{"reset before the record", []string{tokenCount(codexAllowanceAt, rolloutWindow("1", 300, codexAllowanceAt.Add(-time.Hour)), "null")}, nil, allowanceOutOfBounds},
		{"reset past one window", []string{tokenCount(codexAllowanceAt, rolloutWindow("1", 300, codexAllowanceAt.Add(8*time.Hour)), "null")}, nil, allowanceOutOfBounds},
		{"bad timestamp", []string{strings.Replace(tokenCount(codexAllowanceAt, weekly("1"), "null"), codexAllowanceAt.Format(time.RFC3339Nano), "yesterday", 1)}, nil, allowanceShapeUnrecognized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeRollout(t, t.TempDir(), "19", rolloutThread, tc.lines...)
			got, reason := codexRolloutAllowance(p)
			if reason != tc.reason {
				t.Fatalf("reason %q, want %q", reason, tc.reason)
			}
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got a reading %+v", got)
				}
				return
			}
			want := protocol.AllowanceReading{Provider: protocol.AgentCodex, Source: protocol.AllowanceSourceCodexRollout, ReadAt: codexAllowanceAt, Windows: tc.want}
			if got == nil || !got.Valid() || !got.ReadAt.Equal(codexAllowanceAt) || !got.SameValues(want) || got.Provider != want.Provider || got.Source != want.Source {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestCodexRolloutAllowanceBounds(t *testing.T) {
	t.Run("only whole lines", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "r.jsonl")
		body := tokenCount(codexAllowanceAt.Add(-time.Hour), weekly("3"), "null") + "\n" + tokenCount(codexAllowanceAt, weekly("4"), "null")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, _ := codexRolloutAllowance(p)
		if got == nil || got.Windows[0].UsedPercent != "3" {
			t.Fatalf("read a line still being written: %+v", got)
		}
	})
	t.Run("a record further back than the tail is not read", func(t *testing.T) {
		filler := rolloutLine("event_msg", "")
		lines := []string{tokenCount(codexAllowanceAt, weekly("9"), "null")}
		for size := 0; size <= maxRolloutTail; size += len(filler) + 1 {
			lines = append(lines, filler)
		}
		if got, reason := codexRolloutAllowance(writeRollout(t, t.TempDir(), "19", rolloutThread, lines...)); got != nil || reason != "" {
			t.Fatalf("read past the tail: %+v %q", got, reason)
		}
		lines = append(lines, tokenCount(codexAllowanceAt, weekly("10"), "null"))
		if got, _ := codexRolloutAllowance(writeRollout(t, t.TempDir(), "19", rolloutThread, lines...)); got == nil || got.Windows[0].UsedPercent != "10" {
			t.Fatalf("missed the record in the tail: %+v", got)
		}
	})
	t.Run("symlink and missing file are no reading", func(t *testing.T) {
		dir := t.TempDir()
		target := writeRollout(t, dir, "19", rolloutThread, tokenCount(codexAllowanceAt, weekly("1"), "null"))
		link := filepath.Join(dir, "link.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{link, filepath.Join(dir, "absent.jsonl"), dir} {
			if got, reason := codexRolloutAllowance(p); got != nil || reason != "" {
				t.Errorf("%s: %+v %q", p, got, reason)
			}
		}
	})
	t.Run("nothing else is kept", func(t *testing.T) {
		got, _ := codexRolloutAllowance(writeRollout(t, t.TempDir(), "19", rolloutThread, tokenCount(codexAllowanceAt, weekly("1"), "null")))
		if got == nil {
			t.Fatal("no reading")
		}
		if s := strings.ToLower(protocolJSON(t, got)); strings.Contains(s, "secret") || strings.Contains(s, "plan") || strings.Contains(s, "credit") {
			t.Fatalf("the reading kept another field: %s", s)
		}
	})
}

func TestCodexAllowanceRollout(t *testing.T) {
	t.Run("the launch's confirmed thread first", func(t *testing.T) {
		s := testSettings(t, t.TempDir())
		mine := writeRollout(t, s.CodexHome, "18", rolloutThread, tokenCount(codexAllowanceAt, weekly("1"), "null"))
		writeRollout(t, s.CodexHome, "19", "22345678-1234-1234-1234-123456789abc", tokenCount(codexAllowanceAt, weekly("2"), "null"))
		if err := writeJSONFile(s.statePath(launchFile), Launch{Session: "s", ConversationID: rolloutThread, NativeThreadConfirmed: true}); err != nil {
			t.Fatal(err)
		}
		if got := codexAllowanceRollout(s); got != mine {
			t.Fatalf("got %q, want %q", got, mine)
		}
	})
	t.Run("else the most recently written of the newest two days", func(t *testing.T) {
		s := testSettings(t, t.TempDir())
		old := writeRollout(t, s.CodexHome, "17", rolloutThread, tokenCount(codexAllowanceAt, weekly("1"), "null"))
		a := writeRollout(t, s.CodexHome, "18", "22345678-1234-1234-1234-123456789abc", tokenCount(codexAllowanceAt, weekly("2"), "null"))
		b := writeRollout(t, s.CodexHome, "19", "32345678-1234-1234-1234-123456789abc", tokenCount(codexAllowanceAt, weekly("3"), "null"))
		now := time.Now()
		for p, at := range map[string]time.Time{old: now, a: now.Add(-time.Minute), b: now.Add(-2 * time.Minute)} {
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
		if got := codexAllowanceRollout(s); got != a {
			t.Fatalf("got %q, want %q", got, a)
		}
	})
	t.Run("none", func(t *testing.T) {
		if got := codexAllowanceRollout(testSettings(t, t.TempDir())); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

func protocolJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
