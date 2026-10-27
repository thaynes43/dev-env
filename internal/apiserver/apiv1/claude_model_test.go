package apiv1

import "testing"

func TestCheckClaudeModel(t *testing.T) {
	for model, want := range map[string]ClaudeModelFault{
		"claude-opus-5-5":    ClaudeModelOK,
		"claude-sonnet-5-5":  ClaudeModelOK,
		"claude-opus-5[1m]":  ClaudeModelOK,
		"opus":               ClaudeModelAlias,
		"Sonnet":             ClaudeModelAlias,
		"fable":              ClaudeModelAlias,
		"haiku":              ClaudeModelAlias,
		"opusplan":           ClaudeModelAlias,
		"default":            ClaudeModelAlias,
		"opus[1m]":           ClaudeModelAlias,
		"claude-opus-latest": ClaudeModelLatest,
		"claude-opus":        ClaudeModelNotFull,
		"claude-Opus-5-5":    ClaudeModelNotFull,
		"claude-opus-5-5 x":  ClaudeModelNotFull,
		"gpt-6-astra":        ClaudeModelNotFull,
		"":                   ClaudeModelNotFull,
	} {
		if got := CheckClaudeModel(model); got != want {
			t.Errorf("%q: %q, want %q", model, got, want)
		}
	}
}

func TestPortalLaunchModelsAreFullIdsWithEffort(t *testing.T) {
	models := PortalLaunchModels()
	if len(models) == 0 {
		t.Fatal("no models")
	}
	for _, m := range models {
		if CheckClaudeModel(m) != ClaudeModelOK || ClaudeEffortLevels(m) == nil {
			t.Errorf("%s is not a full id the effort table knows", m)
		}
	}
	models[0] = "changed"
	if PortalLaunchModels()[0] == "changed" {
		t.Error("the list aliases its source")
	}
	if got := PortalLaunchEfforts(); len(got) == 0 || got[0] != "low" {
		t.Errorf("efforts %v", got)
	}
}
