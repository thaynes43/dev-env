//go:build linux

package usagecollector

import "testing"

func TestQualifiedClaudeVersionIsAnExactSet(t *testing.T) {
	for output, want := range map[string]bool{
		"2.1.296 (Claude Code)":   true,
		"2.1.312 (Claude Code)":   true,
		"2.1.312 (Claude Code)\n": true,
		" 2.1.312 (Claude Code) ": true,
		"2.1.312":                 false,
		"2.1.313 (Claude Code)":   false,
		"2.1.31 (Claude Code)":    false,
		"2.1.3120 (Claude Code)":  false,
		"2.1.295 (Claude Code)":   false,
		"2.1.312 (Claude Code) x": false,
		"v2.1.312 (Claude Code)":  false,
		"2.1.312  (Claude Code)":  false,
		"2.1.312 (claude code)":   false,
		"3.0.0 (Claude Code)":     false,
		"":                        false,
		"2.1.312 (Claude Code)\n2.1.296 (Claude Code)": false,
	} {
		if got := qualifiedClaudeVersion(output); got != want {
			t.Fatalf("%q: got %v want %v", output, got, want)
		}
	}
}
