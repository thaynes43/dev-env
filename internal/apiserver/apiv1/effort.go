package apiv1

import (
	"regexp"
	"slices"
	"strings"
)

// Claude's effort levels differ per model (code.claude.com/docs/en/model-config,
// "Effort levels"), and the CLI never refuses a mismatch: it clamps an unsupported
// level down, or ignores it on a model with no effort control. The schema leaves
// effort to the API for that reason (D-39), and agent-run checks the same table
// before it sends a create, so both refuse the same levels. The table is v1
// agent-run's claude_effort_levels (haynes-ops, probed 2026-09-01): only the
// reduced tiers are listed, and every other model, a newer one included, takes
// the full set. Haiku 4.5 and older have no effort control; Haiku 5.5 takes the
// full set (model-config table, checked 2026-10-19), so only the 1.x to 4.x
// Haiku ids are in the no-effort pattern.
var (
	// claudeNoEffort are the models with no effort control.
	claudeNoEffort = regexp.MustCompile(`^claude-(haiku-[0-4]|sonnet-4-[05]|opus-4-[015]|[a-z]+-4-20|3)`)
	// claudeNoXHigh are the models without xhigh.
	claudeNoXHigh = regexp.MustCompile(`^claude-(opus|sonnet)-4-6`)

	fullEffort    = []string{"low", "medium", "high", "xhigh", "max"}
	reducedEffort = []string{"low", "medium", "high", "max"}
)

// EffortUltracode is a Claude Code setting layered on xhigh, which --effort
// takes at launch; it is offered wherever the model takes xhigh.
const EffortUltracode = "ultracode"

// ClaudeEffortLevels returns the levels a Claude model takes, lowest first, or
// nil for a model with no effort control. It leaves out ultracode, which
// ClaudeEffortAccepted allows wherever xhigh is.
func ClaudeEffortLevels(model string) []string {
	m := strings.TrimSuffix(model, "[1m]")
	switch {
	case claudeNoEffort.MatchString(m):
		return nil
	case claudeNoXHigh.MatchString(m):
		return slices.Clone(reducedEffort)
	}
	return slices.Clone(fullEffort)
}

// ClaudeEffortAccepted reports whether the model honours the level as asked.
func ClaudeEffortAccepted(model, level string) bool {
	levels := ClaudeEffortLevels(model)
	if level == EffortUltracode {
		return slices.Contains(levels, "xhigh")
	}
	return slices.Contains(levels, level)
}
