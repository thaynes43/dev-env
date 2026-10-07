package apiv1

import (
	"slices"
	"testing"
)

func TestClaudeEffortLevels(t *testing.T) {
	for model, want := range map[string][]string{
		"claude-fable-5-1":           fullEffort,
		"claude-opus-5-5":            fullEffort,
		"claude-sonnet-5-5":          fullEffort,
		"claude-opus-5":              fullEffort,
		"claude-opus-4-8":            fullEffort,
		"claude-opus-4-7[1m]":        fullEffort,
		"claude-opus-4-6":            reducedEffort,
		"claude-sonnet-4-6[1m]":      reducedEffort,
		"claude-haiku-4-5":           nil,
		"claude-sonnet-4-5":          nil,
		"claude-opus-4-1-20250805":   nil,
		"claude-sonnet-4-20250514":   nil,
		"claude-3-7-sonnet-20250219": nil,
		"claude-mythos-6-0":          fullEffort, // a newer model gets the full set
	} {
		if got := ClaudeEffortLevels(model); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", model, got, want)
		}
	}
	for _, tc := range []struct {
		model, level string
		ok           bool
	}{
		{"claude-opus-5-5", "xhigh", true},
		{"claude-opus-5-5", "ultracode", true},
		{"claude-opus-4-6", "ultracode", false},
		{"claude-opus-4-6", "max", true},
		{"claude-haiku-4-5", "low", false},
		{"claude-opus-5-5", "ultra", false},
	} {
		if got := ClaudeEffortAccepted(tc.model, tc.level); got != tc.ok {
			t.Errorf("%s %s: %v, want %v", tc.model, tc.level, got, tc.ok)
		}
	}
}

// The table is shared: a caller that changes the returned slice must not change
// the next answer.
func TestClaudeEffortLevelsReturnsACopy(t *testing.T) {
	ClaudeEffortLevels("claude-opus-5-5")[0] = "changed"
	if got := ClaudeEffortLevels("claude-opus-5-5"); got[0] != "low" {
		t.Fatalf("the table changed under a caller: %v", got)
	}
}
