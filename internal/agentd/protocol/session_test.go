package protocol

import (
	"strings"
	"testing"
	"time"
)

func validTask() Session {
	return Session{
		Name:   "haynes-ops-1005-202504",
		Repo:   "haynes-ops",
		Base:   "origin/main",
		Agent:  AgentClaude,
		Mode:   ModeTask,
		Model:  "claude-opus-5-5",
		Effort: "xhigh",
		Prompt: "fix the typo in README.md",
		Limits: &Limits{Timeout: "40m0s", MaxTurns: 120},
	}
}

func TestParseSessionRoundTrip(t *testing.T) {
	doc := `{"name":"dev-env-1006-172226","repo":"dev-env","agent":"claude","mode":"task",
	  "model":"claude-haiku-4-5","prompt":"say ok","limits":{"timeout":"3h0m0s","maxTurns":5},
	  "someFutureField":{"x":1}}`
	s, err := ParseSession([]byte(doc))
	if err != nil {
		t.Fatalf("ParseSession: %v", err)
	}
	if s.Name != "dev-env-1006-172226" || s.Repo != "dev-env" || s.Model != "claude-haiku-4-5" {
		t.Errorf("parsed %+v", s)
	}
	if got := s.TimeoutDuration(); got != 3*time.Hour {
		t.Errorf("TimeoutDuration = %v, want 3h", got)
	}
	if got := s.MaxTurns(); got != 5 {
		t.Errorf("MaxTurns = %d, want 5", got)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Session)
		wantErr string
	}{
		{"valid task", func(*Session) {}, ""},
		{"valid local", func(s *Session) { s.Mode = ModeLocal; s.Prompt = "" }, ""},
		{"valid codex", func(s *Session) { s.Agent = AgentCodex; s.Model = "gpt-6-astra" }, ""},
		{"1m suffix", func(s *Session) { s.Model = "claude-opus-4-6[1m]" }, ""},
		{"alias opus", func(s *Session) { s.Model = "opus" }, "aliases are refused"},
		{"alias fable", func(s *Session) { s.Model = "fable" }, "aliases are refused"},
		{"alias sonnet 1m", func(s *Session) { s.Model = "sonnet[1m]" }, "aliases are refused"},
		{"no version", func(s *Session) { s.Model = "claude-opus" }, "aliases are refused"},
		{"empty codex model", func(s *Session) { s.Agent = AgentCodex; s.Model = " " }, "model is empty"},
		{"bad name", func(s *Session) { s.Name = "Has_Upper" }, "DNS label"},
		{"long name", func(s *Session) { s.Name = strings.Repeat("a", 64) }, "DNS label"},
		{"repo traversal", func(s *Session) { s.Repo = ".." }, "repository name"},
		{"repo slash", func(s *Session) { s.Repo = "a/b" }, "repository name"},
		{"base option", func(s *Session) { s.Base = "--upload-pack=x" }, "not a ref"},
		{"agent", func(s *Session) { s.Agent = "gemini" }, "not claude, codex or opencode"},
		{"mode", func(s *Session) { s.Mode = "both" }, "not task, local or remote"},
		{"task without prompt", func(s *Session) { s.Prompt = "  " }, "needs a prompt"},
		{"prompt outside task", func(s *Session) { s.Mode = ModeRemote }, "task mode only"},
		{"huge prompt", func(s *Session) { s.Prompt = strings.Repeat("x", MaxPromptBytes+1) }, "more than"},
		{"effort", func(s *Session) { s.Effort = "x high" }, "not a level name"},
		{"timeout", func(s *Session) { s.Limits.Timeout = "forever" }, "positive duration"},
		{"negative timeout", func(s *Session) { s.Limits.Timeout = "-1m" }, "positive duration"},
		{"turns", func(s *Session) { s.Limits.MaxTurns = -1 }, "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validTask()
			tc.mutate(&s)
			err := s.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseSessionRejectsBadJSON(t *testing.T) {
	if _, err := ParseSession([]byte(`{"name":`)); err == nil {
		t.Fatal("ParseSession accepted truncated JSON")
	}
}
