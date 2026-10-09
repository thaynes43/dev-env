package protocol

import "testing"

func TestCodexEffortPinnedModelManifest(t *testing.T) {
	models := map[string]string{
		"gpt-6-astra": "ultra", "gpt-6.1-sol": "ultra", "gpt-6-sol": "ultra",
		"gpt-5.6-sol": "ultra", "gpt-5.6-terra": "ultra",
		"gpt-6-luna": "max", "gpt-5.6-luna": "max", "gpt-5.5": "xhigh",
	}
	levels := []string{"", "low", "medium", "high", "xhigh", "max", "ultra", "ultracode"}
	for model, highest := range models {
		for _, effort := range levels {
			want := effort != "ultracode" && (effort != "ultra" || highest == "ultra") && (effort != "max" || highest != "xhigh")
			if got := ValidateCodexEffort(model, effort) == nil; got != want {
				t.Errorf("pinned manifest %s/%s accepted=%v, want %v", model, effort, got, want)
			}
		}
	}
	for _, alias := range []string{"sol", "luna", "default", "gpt-6"} {
		if ValidateCodexEffort(alias, "") == nil {
			t.Errorf("model alias %s received default effort authority", alias)
		}
	}
}

func TestCodexSessionRejectsUnsupportedModelEffort(t *testing.T) {
	s := Session{Name: "task", Repo: "demo", Agent: AgentCodex, Mode: ModeTask, Model: "gpt-6-luna", Effort: "ultra", Prompt: "synthetic task"}
	if s.Validate() == nil {
		t.Fatal("protocol accepted a Luna ultra session")
	}
	s.Effort = "max"
	if err := s.Validate(); err != nil {
		t.Fatal("protocol refused the pinned Luna maximum", err)
	}
}
