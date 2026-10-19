package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const rolloutThread = "12345678-1234-1234-1234-123456789abc"

func rolloutLine(kind, model string) string {
	if kind == "turn_context" {
		return `{"timestamp":"2026-10-19T00:00:00Z","ordinal":1,"type":"turn_context","payload":{"turn_id":"t","model":` + model + `,"effort":"max"}}`
	}
	return `{"timestamp":"2026-10-19T00:00:00Z","ordinal":2,"type":"` + kind + `","payload":{"type":"agent_message","message":"SECRET-MESSAGE-TEXT"}}`
}

func writeRollout(t *testing.T, home, day, thread string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", day)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-10-"+day+"T01-02-03-"+thread+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCodexRolloutModels(t *testing.T) {
	long := `{"type":"event_msg","payload":{"message":"` + strings.Repeat("x", maxRolloutLine+10) + `"},"x":"\"type\":\"turn_context\""}`
	for _, tc := range []struct {
		name  string
		lines []string
		want  []string
	}{
		{"one model across turns", []string{rolloutLine("event_msg", ""), rolloutLine("turn_context", `"gpt-6-astra"`), rolloutLine("turn_context", `"gpt-6-astra"`)}, []string{"gpt-6-astra"}},
		{"two models sorted", []string{rolloutLine("turn_context", `"gpt-6.1-sol"`), rolloutLine("turn_context", `"gpt-6-astra"`)}, []string{"gpt-6-astra", "gpt-6.1-sol"}},
		{"long line skipped", []string{long, rolloutLine("turn_context", `"gpt-6-astra"`)}, []string{"gpt-6-astra"}},
		{"no turn context", []string{rolloutLine("event_msg", "")}, nil},
		{"null model", []string{rolloutLine("turn_context", `null`)}, nil},
		{"non-string model", []string{rolloutLine("turn_context", `7`)}, nil},
		{"invalid model", []string{rolloutLine("turn_context", `"gpt 6"`)}, nil},
		{"invalid beside valid", []string{rolloutLine("turn_context", `"gpt-6-astra"`), rolloutLine("turn_context", `"a b"`)}, nil},
		{"malformed line ignored", []string{`{"type":"turn_context" nope`, rolloutLine("turn_context", `"gpt-6-astra"`)}, []string{"gpt-6-astra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeRollout(t, t.TempDir(), "19", rolloutThread, tc.lines...)
			got, ok := codexRolloutModels(p)
			if ok != (tc.want != nil) || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v ok=%v, want %v", got, ok, tc.want)
			}
		})
	}
	t.Run("too many models", func(t *testing.T) {
		var lines []string
		for i := 0; i <= protocol.MaxServedModels; i++ {
			lines = append(lines, rolloutLine("turn_context", `"m`+string(rune('a'+i))+`"`))
		}
		if _, ok := codexRolloutModels(writeRollout(t, t.TempDir(), "19", rolloutThread, lines...)); ok {
			t.Fatal("kept a list the observation cannot hold")
		}
	})
	t.Run("last line without newline", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "r.jsonl")
		if err := os.WriteFile(p, []byte(rolloutLine("turn_context", `"gpt-6-astra"`)), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := codexRolloutModels(p); !ok || len(got) != 1 {
			t.Fatal("lost an unterminated final record", got)
		}
	})
	t.Run("symlink refused", func(t *testing.T) {
		dir := t.TempDir()
		target := writeRollout(t, dir, "19", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
		link := filepath.Join(dir, "link.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, ok := codexRolloutModels(link); ok {
			t.Fatal("followed a symlink")
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		if _, ok := codexRolloutModels(t.TempDir()); ok {
			t.Fatal("read a directory")
		}
	})
}

func TestFindCodexRollout(t *testing.T) {
	home := t.TempDir()
	old := writeRollout(t, home, "18", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
	newest := writeRollout(t, home, "19", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
	writeRollout(t, home, "19", "aaaaaaaa-1234-1234-1234-123456789abc", rolloutLine("turn_context", `"other"`))
	if got := findCodexRollout(home, rolloutThread); got != newest || got == old {
		t.Fatal("did not pick the newest rollout of the thread", got)
	}
	if findCodexRollout(home, "../../etc") != "" || findCodexRollout(home, "*") != "" || findCodexRollout("", rolloutThread) != "" {
		t.Fatal("accepted a bad thread id or home")
	}
	if findCodexRollout(t.TempDir(), rolloutThread) != "" {
		t.Fatal("found a rollout in an empty home")
	}
	t.Run("symlinked directory skipped", func(t *testing.T) {
		outside, h := t.TempDir(), t.TempDir()
		writeRollout(t, outside, "19", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
		if err := os.MkdirAll(filepath.Join(h, "sessions"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "sessions", "2026"), filepath.Join(h, "sessions", "2026")); err != nil {
			t.Fatal(err)
		}
		if findCodexRollout(h, rolloutThread) != "" {
			t.Fatal("followed a symlinked directory")
		}
	})
	t.Run("symlinked file skipped", func(t *testing.T) {
		h := t.TempDir()
		target := writeRollout(t, t.TempDir(), "19", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
		dir := filepath.Join(h, "sessions", "2026", "10", "19")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "rollout-x-"+rolloutThread+".jsonl")); err != nil {
			t.Fatal(err)
		}
		if findCodexRollout(h, rolloutThread) != "" {
			t.Fatal("followed a symlinked rollout")
		}
	})
}

func TestApplyCodexRolloutModels(t *testing.T) {
	launch := func(home string) Launch {
		return Launch{Provider: protocol.AgentCodex, ConversationID: rolloutThread, NativeThreadConfirmed: true, Env: []string{"CODEX_HOME=" + home}}
	}
	observed := func() protocol.UsageObservation {
		u := protocol.UnknownUsage()
		five, seven := int64(5), int64(7)
		u.InputTokens = protocol.TokenObservation{State: protocol.UsageReported, Amount: &five}
		u.OutputTokens = protocol.TokenObservation{State: protocol.UsageReported, Amount: &seven}
		return u
	}
	t.Run("one model takes the counters", func(t *testing.T) {
		home := t.TempDir()
		writeRollout(t, home, "19", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
		u := observed()
		if !applyCodexRolloutModels(&u, launch(home)) || u.ActualModelState != protocol.UsageProviderReported || len(u.ServedModels) != 1 ||
			u.ServedModels[0].Model != "gpt-6-astra" || *u.ServedModels[0].InputTokens.Amount != 5 || *u.ServedModels[0].OutputTokens.Amount != 7 || u.ServedModels[0].CacheReadInputTokens.Amount != nil {
			t.Fatal("served model not filled", u)
		}
	})
	t.Run("several models keep unknown counters", func(t *testing.T) {
		home := t.TempDir()
		writeRollout(t, home, "19", rolloutThread, rolloutLine("turn_context", `"gpt-6.1-sol"`), rolloutLine("turn_context", `"gpt-6-astra"`))
		u := observed()
		if !applyCodexRolloutModels(&u, launch(home)) || len(u.ServedModels) != 2 || u.ServedModels[0].InputTokens.Amount != nil || u.ServedModels[1].OutputTokens.State != protocol.UsageUnknown {
			t.Fatal("split tokens it was never given", u.ServedModels)
		}
	})
	t.Run("unknown stays unknown", func(t *testing.T) {
		home := t.TempDir()
		u := observed()
		if applyCodexRolloutModels(&u, launch(home)) || u.ActualModelState != protocol.UsageUnknown || u.ServedModels != nil {
			t.Fatal("invented a model with no rollout")
		}
		l := launch(home)
		l.NativeThreadConfirmed = false
		writeRollout(t, home, "19", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
		if applyCodexRolloutModels(&u, l) {
			t.Fatal("read a rollout for an unconfirmed thread")
		}
	})
	t.Run("stream-reported model wins", func(t *testing.T) {
		home := t.TempDir()
		writeRollout(t, home, "19", rolloutThread, rolloutLine("turn_context", `"gpt-6-astra"`))
		u := observed()
		codexServedModel(&u, []byte(`"gpt-6.1-sol"`))
		if applyCodexRolloutModels(&u, launch(home)) || u.ServedModels[0].Model != "gpt-6.1-sol" {
			t.Fatal("rollout replaced an explicit stream model")
		}
	})
}

func TestRunAgentCodexReportsRolloutModel(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.WorkDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	home, dir := t.TempDir(), t.TempDir()
	rollout := filepath.Join(home, "sessions", "2026", "10", "19")
	// The CLI writes its rollout, then its exec stream, as the real one does.
	cli := fakeCLI(t, dir, `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli 0.160.1'; exit 0; fi
mkdir -p "`+rollout+`"
cat > "`+filepath.Join(rollout, "rollout-2026-10-19T01-02-03-"+rolloutThread+".jsonl")+`" <<'EOF2'
`+rolloutLine("turn_context", `"gpt-6-astra"`)+`
`+rolloutLine("event_msg", "")+`
EOF2
printf '%s\n' '{"type":"thread.started","thread_id":"`+rolloutThread+`"}' '{"type":"turn.started"}' '{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":7,"cached_input_tokens":3}}'
`)
	l := Launch{Provider: protocol.AgentCodex, Session: "s-1", Argv: []string{cli, "exec", "--json"}, Prompt: "task", Dir: dir,
		Env: []string{"CODEX_HOME=" + home}, LogPath: filepath.Join(s.WorkDir(), "s-1.log"), EventsPath: s.statePath(eventsFile), BootID: "b1"}
	if err := bindInitialUsage(&l, protocol.Session{SessionUID: "original-session", Agent: protocol.AgentCodex, Mode: protocol.ModeTask, Model: "gpt-6-astra"}, "original-pod"); err != nil {
		t.Fatal(err)
	}
	pane := &syncBuffer{}
	if code := RunAgent(writeLaunch(t, s, l), pane, make(chan os.Signal), time.Second); code != 0 {
		t.Fatalf("exit %d\n%s", code, pane.String())
	}
	out := pane.String()
	if !strings.Contains(out, "served model gpt-6-astra (provider reported)") || strings.Contains(out, "SECRET-MESSAGE-TEXT") {
		t.Fatal("pane lacks the served model, or leaked rollout text:\n" + out)
	}
	var res taskResult
	if err := readJSONFile(s.statePath(resultFile), &res); err != nil {
		t.Fatal(err)
	}
	u := res.UsageObservation
	if u == nil || u.Validate() != nil || u.ActualModelState != protocol.UsageProviderReported || len(u.ServedModels) != 1 || u.ServedModels[0].Model != "gpt-6-astra" || *u.ServedModels[0].InputTokens.Amount != 5 {
		t.Fatal("served model not in the retained observation", u)
	}
	if data, _ := os.ReadFile(s.statePath(resultFile)); strings.Contains(string(data), "SECRET-MESSAGE-TEXT") {
		t.Fatal("rollout text reached the result")
	}
}
