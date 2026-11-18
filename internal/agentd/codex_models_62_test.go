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
