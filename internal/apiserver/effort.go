package apiserver

import (
	"regexp"
	"slices"
	"strings"
)

// Claude's effort levels differ per model (code.claude.com/docs/en/model-config,
// "Effort levels"), and the CLI never refuses a mismatch: it clamps an unsupported
// level down, or ignores it on a model with no effort control. The schema leaves
// effort to the API for that reason (D-39). The table is v1 agent-run's
// claude_effort_levels (haynes-ops, probed 2026-09-01): only the reduced tiers are
// listed, and every other model, a newer one included, takes the full set.
var (
	// claudeNoEffort are the models with no effort control.
	claudeNoEffort = regexp.MustCompile(`^claude-(haiku-|sonnet-4-[05]|opus-4-[015]|[a-z]+-4-20|3)`)
	// claudeNoXHigh are the models without xhigh.
	claudeNoXHigh = regexp.MustCompile(`^claude-(opus|sonnet)-4-6`)

	fullEffort    = []string{"low", "medium", "high", "xhigh", "max"}
	reducedEffort = []string{"low", "medium", "high", "max"}
)

// ultracode is a Claude Code setting layered on xhigh, which --effort takes at
// launch; it is offered wherever the model takes xhigh.
const ultracode = "ultracode"

// claudeEffortLevels returns the levels a Claude model takes, nil for none.
func claudeEffortLevels(model string) []string {
	m := strings.TrimSuffix(model, "[1m]")
	switch {
	case claudeNoEffort.MatchString(m):
		return nil
	case claudeNoXHigh.MatchString(m):
		return reducedEffort
	}
	return fullEffort
}

// claudeEffortAccepted reports whether the model honours the level as asked.
func claudeEffortAccepted(model, level string) bool {
	levels := claudeEffortLevels(model)
	if level == ultracode {
		return slices.Contains(levels, "xhigh")
	}
	return slices.Contains(levels, level)
}
