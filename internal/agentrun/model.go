package agentrun

import (
	"fmt"
	"slices"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// DefaultClaudeModel is agent-run's Claude model when neither --model nor
// DEV_ENV_CLAUDE_MODEL names one: the pod-wide default since 2026-09-23, as in
// v1's agent-run (CLAUDE_DEFAULT_MODEL). Fable is never a default.
const DefaultClaudeModel = "claude-opus-5-5"

// envDefaultModel is the pod's default Claude model, which dev-init sets in v1.
const envDefaultModel = "DEV_ENV_CLAUDE_MODEL"

// checkClaudeModel refuses anything but a full Claude model id, by the rule in
// apiv1 that the API's portal launch route shares. from names where the value
// came from, for the message.
func checkClaudeModel(model, from string) error {
	switch apiv1.CheckClaudeModel(model) {
	case apiv1.ClaudeModelAlias:
		return usageError("%s %q is an alias, which the CLI resolves to whatever its own version knows; give the full model id, such as %s", from, model, DefaultClaudeModel)
	case apiv1.ClaudeModelLatest:
		return usageError("%s %q is an alias that moves with each release; give the full model id, such as %s", from, model, DefaultClaudeModel)
	case apiv1.ClaudeModelNotFull:
		return usageError("%s %q is not a full Claude model id, such as %s", from, model, DefaultClaudeModel)
	}
	return nil
}

// effortPreference is the default's order: xhigh, else the highest level
// below it the model takes. v1's agent-run defaults the same way, so the level
// a session runs at is the one it asked for, not the CLI's silent clamp.
var effortPreference = []string{"xhigh", "high", "medium", "low"}

// claudeEffort returns the effort to send for a Claude model: the asked level
// if the model takes it, the default if none was asked, and nothing for a
// model with no effort control. It refuses a level the model would not honour,
// as the API does (D-46): the CLI would clamp it without saying so.
func claudeEffort(model, asked string) (string, error) {
	levels := apiv1.ClaudeEffortLevels(model)
	if asked == "" {
		for _, l := range effortPreference {
			if slices.Contains(levels, l) {
				return l, nil
			}
		}
		return "", nil
	}
	if apiv1.ClaudeEffortAccepted(model, asked) {
		return asked, nil
	}
	if levels == nil {
		return "", usageError("--effort %s: model %s has no effort control; leave --effort out", asked, model)
	}
	takes := strings.Join(levels, ", ")
	if slices.Contains(levels, "xhigh") {
		takes += " or " + apiv1.EffortUltracode
	}
	return "", usageError("--effort %s: model %s takes %s", asked, model, takes)
}

// effortText is the effort for a summary line.
func effortText(effort string) string {
	if effort == "" {
		return "no effort level"
	}
	return fmt.Sprintf("effort %s", effort)
}
