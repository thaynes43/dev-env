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
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// The statusline inputs here are synthetic, built from the documented callback
// shape (DESIGN-059 4.2 and 11): rate_limits.five_hour and seven_day, each
// with used_percentage and resets_at. testdata/allowance holds one document
// the image's own helper wrote from synthetic input.

// statuslineFixture is a Claude session's settings with the helper present
// under SystemBin.
func statuslineFixture(t *testing.T, mode string) (Settings, protocol.Session) {
	t.Helper()
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.ClaudeLiveFile = filepath.Join(s.Home, "live", "access.json")
	writeFile(t, filepath.Join(s.SystemBin, claudeStatuslineHelper), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(s.SystemBin, claudeStatuslineHelper), 0o755); err != nil {
		t.Fatal(err)
	}
	return s, protocol.Session{Name: "s", Agent: protocol.AgentClaude, Mode: mode, Repo: "dev-env"}
}

func statusLineOf(t *testing.T, s Settings) map[string]any {
	t.Helper()
	m := readJSONMap(t, filepath.Join(s.ClaudeConfigDir, "settings.json"))
	line, _ := m[keyStatusLine].(map[string]any)
	return line
}

func TestRenderClaudeStatusline(t *testing.T) {
	for _, mode := range []string{protocol.ModeRemote, protocol.ModeLocal} {
		t.Run(mode, func(t *testing.T) {
			s, sess := statuslineFixture(t, mode)
			writeJSON(t, filepath.Join(s.ClaudeConfigDir, "settings.json"), map[string]any{"model": "claude-opus-5-5", "theme": "dark"})
			st := renderClaudeStatusline(s, sess)
			if st.State != StepOK || !strings.Contains(strings.Join(st.Notes, " "), "set to the usage helper") {
				t.Fatalf("first render: %+v", st)
			}
			helper := filepath.Join(s.SystemBin, claudeStatuslineHelper)
			want := shellQuote(helper) + " --output " + shellQuote(claudeUsageFile(s))
			line := statusLineOf(t, s)
			if line["type"] != "command" || line["command"] != want {
				t.Fatalf("statusLine %v, want command %q", line, want)
			}
			if m := readJSONMap(t, filepath.Join(s.ClaudeConfigDir, "settings.json")); m["model"] != "claude-opus-5-5" || m["theme"] != "dark" {
				t.Errorf("other keys lost: %v", m)
			}
			if fi, err := os.Lstat(claudeUsageDir(s)); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
				t.Fatalf("usage dir %v %v", fi, err)
			}
			// agentd renders at every boot and resume: the second render keeps it.
			before, _ := os.ReadFile(filepath.Join(s.ClaudeConfigDir, "settings.json"))
			st = renderClaudeStatusline(s, sess)
			after, _ := os.ReadFile(filepath.Join(s.ClaudeConfigDir, "settings.json"))
			if st.State != StepOK || !strings.Contains(strings.Join(st.Notes, " "), "already runs") || string(before) != string(after) {
				t.Fatalf("second render: %+v\n%s\n%s", st, before, after)
			}
		})
	}
}

func TestRenderClaudeStatuslineWrapsDisplay(t *testing.T) {
	s, sess := statuslineFixture(t, protocol.ModeRemote)
	display := `bash ~/.claude/line.sh "it's mine"`
	writeJSON(t, filepath.Join(s.ClaudeConfigDir, "settings.json"), map[string]any{keyStatusLine: map[string]any{"type": "command", "command": display, "padding": 2}})
	st := renderClaudeStatusline(s, sess)
	if st.State != StepOK || !strings.Contains(strings.Join(st.Notes, " "), "wraps the existing display") {
		t.Fatalf("render: %+v", st)
	}
	helper := filepath.Join(s.SystemBin, claudeStatuslineHelper)
	want := shellQuote(helper) + " --output " + shellQuote(claudeUsageFile(s)) + " --display /bin/sh -c " + shellQuote(display)
	line := statusLineOf(t, s)
	if line["command"] != want || line["padding"] != float64(2) || line["type"] != "command" {
		t.Fatalf("statusLine %v\nwant %q", line, want)
	}
	// Never wrapped a second time, on a second or a third render.
	for i := 0; i < 2; i++ {
		st = renderClaudeStatusline(s, sess)
		if st.State != StepOK || !strings.Contains(strings.Join(st.Notes, " "), "already runs") || statusLineOf(t, s)["command"] != want {
			t.Fatalf("render %d: %+v %v", i+2, st, statusLineOf(t, s))
		}
	}
	if strings.Count(want, "--display") != 1 {
		t.Fatalf("wrapped twice: %q", want)
	}
}

func TestRenderClaudeStatuslineLeavesWhatItCannotRead(t *testing.T) {
	for name, line := range map[string]any{
		"a string":             "echo hi",
		"not a command":        map[string]any{"type": "static", "text": "x"},
		"number command":       map[string]any{"type": "command", "command": 7},
		"helper, other output": map[string]any{"type": "command", "command": "HELPER --output '/elsewhere/x.json'"},
	} {
		t.Run(name, func(t *testing.T) {
			s, sess := statuslineFixture(t, protocol.ModeRemote)
			if m, ok := line.(map[string]any); ok && m["command"] == "HELPER --output '/elsewhere/x.json'" {
				m["command"] = shellQuote(filepath.Join(s.SystemBin, claudeStatuslineHelper)) + " --output '/elsewhere/x.json'"
			}
			path := filepath.Join(s.ClaudeConfigDir, "settings.json")
			writeJSON(t, path, map[string]any{keyStatusLine: line})
			before, _ := os.ReadFile(path)
			st := renderClaudeStatusline(s, sess)
			after, _ := os.ReadFile(path)
			if st.State != StepWarn || string(before) != string(after) {
				t.Fatalf("%+v\n%s\n%s", st, before, after)
			}
		})
	}
	t.Run("settings holding null", func(t *testing.T) {
		s, sess := statuslineFixture(t, protocol.ModeRemote)
		writeFile(t, filepath.Join(s.ClaudeConfigDir, "settings.json"), "null")
		if st := renderClaudeStatusline(s, sess); st.State != StepOK || statusLineOf(t, s)["type"] != "command" {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("its own command without quotes", func(t *testing.T) {
		s, sess := statuslineFixture(t, protocol.ModeRemote)
		path := filepath.Join(s.ClaudeConfigDir, "settings.json")
		writeJSON(t, path, map[string]any{keyStatusLine: map[string]any{"type": "command", "command": filepath.Join(s.SystemBin, claudeStatuslineHelper) + " --output " + claudeUsageFile(s)}})
		before, _ := os.ReadFile(path)
		if st := renderClaudeStatusline(s, sess); st.State != StepOK || !strings.Contains(strings.Join(st.Notes, " "), "already runs") {
			t.Fatalf("%+v", st)
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatal("rewritten")
		}
	})
	t.Run("settings that are not an object", func(t *testing.T) {
		s, sess := statuslineFixture(t, protocol.ModeRemote)
		path := filepath.Join(s.ClaudeConfigDir, "settings.json")
		writeFile(t, path, "[1]")
		if st := renderClaudeStatusline(s, sess); st.State != StepWarn {
			t.Fatalf("%+v", st)
		}
		if b, _ := os.ReadFile(path); string(b) != "[1]" {
			t.Fatalf("rewritten: %s", b)
		}
	})
}

func TestRenderClaudeStatuslineSkips(t *testing.T) {
	s, sess := statuslineFixture(t, protocol.ModeTask)
	if st := renderClaudeStatusline(s, sess); st.State != StepSkip {
		t.Errorf("headless task: %+v", st)
	}
	sess.Mode, sess.Agent = protocol.ModeRemote, protocol.AgentCodex
	if st := renderClaudeStatusline(s, sess); st.State != StepSkip {
		t.Errorf("codex: %+v", st)
	}
	sess.Agent = protocol.AgentClaude
	// A pod that does not follow the keeper's Claude projection.
	live := s.ClaudeLiveFile
	s.ClaudeLiveFile = ""
	if st := renderClaudeStatusline(s, sess); st.State != StepSkip || !strings.Contains(strings.Join(st.Notes, " "), "projection") {
		t.Errorf("no projection: %+v", st)
	}
	s.ClaudeLiveFile = live
	if err := os.Remove(filepath.Join(s.SystemBin, claudeStatuslineHelper)); err != nil {
		t.Fatal(err)
	}
	if st := renderClaudeStatusline(s, sess); st.State != StepSkip {
		t.Errorf("no helper: %+v", st)
	}
	if exists(filepath.Join(s.ClaudeConfigDir, "settings.json")) {
		t.Error("a skipped render wrote settings.json")
	}
}

// statuslineDoc is the document agent-run usage -o json --claude-statusline
// writes: the helper's consumer, built from the same Go producer.
func statuslineDoc(t *testing.T, callback string, captured time.Time) []byte {
	t.Helper()
	item, err := providerallowance.ClaudeStatuslineRead([]byte(callback), captured)
	if err != nil {
		t.Fatal(err)
	}
	at := captured.UTC().Format(time.RFC3339Nano)
	doc := providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: item.Observation.ObservationID, ServedAt: at, Items: []providerallowance.ReadItem{item}, SourceStatuses: []providerallowance.SourceStatus{}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func callbackInput(fiveUsed, sevenUsed string, fiveReset, sevenReset time.Time) string {
	win := func(used string, reset time.Time) string {
		return `{"used_percentage":` + used + `,"resets_at":` + strconv.FormatInt(reset.Unix(), 10) + `}`
	}
	parts := []string{}
	if fiveUsed != "" {
		parts = append(parts, `"five_hour":`+win(fiveUsed, fiveReset))
	}
	if sevenUsed != "" {
		parts = append(parts, `"seven_day":`+win(sevenUsed, sevenReset))
	}
	return `{"session_id":"synthetic","version":"2.1.321","cost":{"total_api_duration_ms":5},"rate_limits":{` + strings.Join(parts, ",") + `}}`
}

func writeUsageFile(t *testing.T, s Settings, raw []byte) {
	t.Helper()
	if err := ensurePrivateDir(claudeUsageDir(s)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeUsageFile(s), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(claudeUsageFile(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadClaudeStatuslineAllowance(t *testing.T) {
	at := time.Date(2026, 11, 9, 23, 40, 0, 0, time.UTC)
	seven := at.Add(60 * time.Hour)
	five := at.Add(3 * time.Hour)

	t.Run("the helper's own document", func(t *testing.T) {
		s, _ := statuslineFixture(t, protocol.ModeRemote)
		raw, err := os.ReadFile(filepath.Join("testdata", "allowance", "claude-statusline-helper.json"))
		if err != nil {
			t.Fatal(err)
		}
		writeUsageFile(t, s, raw)
		got, reason := readClaudeStatuslineAllowance(s)
		if reason != "" || got == nil || got.Provider != protocol.AgentClaude || got.Source != protocol.AllowanceSourceClaudeStatusline || len(got.Windows) != 2 {
			t.Fatalf("%+v %q", got, reason)
		}
		for _, w := range got.Windows {
			if (w.Minutes == 300 && w.UsedPercent != "12.5") || (w.Minutes == protocol.WeeklyAllowanceMinutes && w.UsedPercent != "34") {
				t.Errorf("window %+v", w)
			}
		}
	})
	t.Run("seven day only, as observed on 2.1.321", func(t *testing.T) {
		s, _ := statuslineFixture(t, protocol.ModeRemote)
		writeUsageFile(t, s, statuslineDoc(t, callbackInput("", "34", time.Time{}, seven), at))
		got, reason := readClaudeStatuslineAllowance(s)
		want := protocol.AllowanceReading{Windows: []protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: "34", ResetsAt: seven}}}
		if reason != "" || got == nil || !got.ReadAt.Equal(at) || !got.SameValues(want) {
			t.Fatalf("%+v %q", got, reason)
		}
	})
	t.Run("a window whose reset passed is left out", func(t *testing.T) {
		s, _ := statuslineFixture(t, protocol.ModeRemote)
		writeUsageFile(t, s, statuslineDoc(t, callbackInput("90", "34", at.Add(-time.Minute), seven), at))
		got, _ := readClaudeStatuslineAllowance(s)
		if got == nil || len(got.Windows) != 1 || got.Windows[0].Minutes != protocol.WeeklyAllowanceMinutes {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no rate limits is no reading", func(t *testing.T) {
		s, _ := statuslineFixture(t, protocol.ModeRemote)
		writeUsageFile(t, s, statuslineDoc(t, `{"version":"2.1.321"}`, at))
		if got, reason := readClaudeStatuslineAllowance(s); got != nil || reason != "" {
			t.Fatalf("%+v %q", got, reason)
		}
	})
	t.Run("no file is no reading", func(t *testing.T) {
		s, _ := statuslineFixture(t, protocol.ModeRemote)
		if got, reason := readClaudeStatuslineAllowance(s); got != nil || reason != "" {
			t.Fatalf("%+v %q", got, reason)
		}
		if err := ensurePrivateDir(claudeUsageDir(s)); err != nil {
			t.Fatal(err)
		}
		if got, reason := readClaudeStatuslineAllowance(s); got != nil || reason != "" {
			t.Fatalf("%+v %q", got, reason)
		}
	})
	t.Run("not the helper's shape", func(t *testing.T) {
		for name, raw := range map[string][]byte{
			"not json":     []byte("nope"),
			"another item": []byte(strings.Replace(string(statuslineDoc(t, callbackInput("1", "2", five, seven), at)), claudeStatuslineProducer, "someone-else", 1)),
			"read schema":  []byte(`{"schema":"x"}`),
		} {
			s, _ := statuslineFixture(t, protocol.ModeRemote)
			writeUsageFile(t, s, raw)
			if got, reason := readClaudeStatuslineAllowance(s); got != nil || reason != allowanceShapeUnrecognized {
				t.Errorf("%s: %+v %q", name, got, reason)
			}
		}
	})
	t.Run("unsafe file or directory", func(t *testing.T) {
		good := statuslineDoc(t, callbackInput("1", "2", five, seven), at)
		cases := map[string]func(Settings){
			"file readable by others": func(s Settings) { _ = os.Chmod(claudeUsageFile(s), 0o644) },
			"directory open to group": func(s Settings) { _ = os.Chmod(claudeUsageDir(s), 0o750) },
			"file is a symlink": func(s Settings) {
				other := filepath.Join(s.Home, "elsewhere.json")
				_ = os.WriteFile(other, good, 0o600)
				_ = os.Remove(claudeUsageFile(s))
				_ = os.Symlink(other, claudeUsageFile(s))
			},
			"directory is a symlink": func(s Settings) {
				other := filepath.Join(s.Home, "otherdir")
				_ = os.Rename(claudeUsageDir(s), other)
				_ = os.Symlink(other, claudeUsageDir(s))
			},
			"file too large": func(s Settings) {
				_ = os.WriteFile(claudeUsageFile(s), []byte(strings.Repeat(" ", providerallowance.FixtureParseLimits.MaxBytes+1)), 0o600)
			},
			"file is a directory": func(s Settings) {
				_ = os.Remove(claudeUsageFile(s))
				_ = os.Mkdir(claudeUsageFile(s), 0o700)
			},
		}
		for name, mutate := range cases {
			s, _ := statuslineFixture(t, protocol.ModeRemote)
			writeUsageFile(t, s, good)
			mutate(s)
			if got, reason := readClaudeStatuslineAllowance(s); got != nil || reason != allowanceFileUnsafe {
				t.Errorf("%s: %+v %q", name, got, reason)
			}
		}
	})
}

func TestStatuslineReadingsKeepTime(t *testing.T) {
	at := time.Date(2026, 11, 9, 23, 40, 0, 0, time.UTC)
	reading := func(when time.Time, used string) *protocol.AllowanceReading {
		return &protocol.AllowanceReading{Provider: protocol.AgentClaude, Source: protocol.AllowanceSourceClaudeStatusline, ReadAt: when,
			Windows: []protocol.AllowanceWindow{{Minutes: protocol.WeeklyAllowanceMinutes, UsedPercent: used, ResetsAt: at.Add(48 * time.Hour)}}}
	}
	var k statuslineReadings
	if got := k.keep(nil); got != nil || k.last != nil {
		t.Fatal("nil remembered")
	}
	if got := k.keep(reading(at, "34")); !got.ReadAt.Equal(at) {
		t.Fatalf("first %v", got.ReadAt)
	}
	// A timer callback with the same numbers keeps the first time.
	if got := k.keep(reading(at.Add(10*time.Minute), "34")); !got.ReadAt.Equal(at) {
		t.Fatalf("same values moved the time to %v", got.ReadAt)
	}
	// A new value moves it.
	if got := k.keep(reading(at.Add(20*time.Minute), "35")); !got.ReadAt.Equal(at.Add(20 * time.Minute)) {
		t.Fatalf("a new value kept %v", got.ReadAt)
	}
	// A missing file in between does not erase the memory.
	k.keep(nil)
	if got := k.keep(reading(at.Add(30*time.Minute), "35")); !got.ReadAt.Equal(at.Add(20 * time.Minute)) {
		t.Fatalf("after a gap: %v", got.ReadAt)
	}
}
