package agentd

import "testing"

func TestCodexThreadExplicit62Models(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra", "gpt-unknown-921"} {
		req := CodexThreadRequest{Model: model, Effort: "medium", Name: "model-probe", Handoff: "synthetic"}
		if got := validCodexThreadRequest(req); got != (model != "gpt-unknown-921") {
			t.Fatalf("%s accepted=%v", model, got)
		}
	}
}

func TestCodexRCSelectionExplicit62Models(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra", "gpt-unknown-921"} {
		s := codexRCThreadSelection{InstallationID: "fixture-installation", EnvironmentID: "fixture-environment", Cwd: "/accepted/repo", Model: model}
		if got := s.valid(); got != (model != "gpt-unknown-921") {
			t.Fatalf("%s accepted=%v", model, got)
		}
	}
}
