package agentrun

import (
	"fmt"
	"regexp"
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

// claudeAliases are the names the Claude CLI resolves on the client side, from
// the table of the version it is. A pinned CLI can resolve one to an older
// model than its name suggests, so sessions take full ids only (D-39).
var claudeAliases = []string{"default", "best", "opus", "sonnet", "haiku", "fable", "opusplan"}

// claudeModelID is a full model id: claude-, then lowercase words and numbers
// joined by '-', with a version number in it, and an optional [1m] suffix.
var claudeModelID = regexp.MustCompile(`^claude-[a-z0-9]+(-[a-z0-9]+)*(\[1m\])?$`)

var hasDigit = regexp.MustCompile(`[0-9]`)

// checkClaudeModel refuses anything but a full Claude model id. from names
// where the value came from, for the message.
func checkClaudeModel(model, from string) error {
	base := strings.TrimSuffix(model, "[1m]")
	if slices.Contains(claudeAliases, strings.ToLower(base)) {
		return usageError("%s %q is an alias, which the CLI resolves to whatever its own version knows; give the full model id, such as %s", from, model, DefaultClaudeModel)
	}
	if strings.HasSuffix(base, "-latest") {
		return usageError("%s %q is an alias that moves with each release; give the full model id, such as %s", from, model, DefaultClaudeModel)
	}
	if !claudeModelID.MatchString(model) || !hasDigit.MatchString(base) {
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
