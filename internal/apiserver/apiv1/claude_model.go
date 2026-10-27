package apiv1

import (
	"regexp"
	"slices"
	"strings"
)

// ClaudeModelAliases are the names the Claude CLI resolves on the client side,
// from the table of the version it is. A pinned CLI can resolve one to an older
// model than its name suggests, so sessions take full ids only (D-39).
var ClaudeModelAliases = []string{"default", "best", "opus", "sonnet", "haiku", "fable", "opusplan"}

// claudeModelID is a full model id: claude-, then lowercase words and numbers
// joined by '-', with a version number in it, and an optional [1m] suffix.
var claudeModelID = regexp.MustCompile(`^claude-[a-z0-9]+(-[a-z0-9]+)*(\[1m\])?$`)

var modelHasDigit = regexp.MustCompile(`[0-9]`)

// ClaudeModelFault says why a Claude model value is not a full model id.
type ClaudeModelFault string

const (
	// ClaudeModelOK is a full model id.
	ClaudeModelOK ClaudeModelFault = ""
	// ClaudeModelAlias is a name the CLI resolves on its own, such as opus.
	ClaudeModelAlias ClaudeModelFault = "alias"
	// ClaudeModelLatest is an id ending in -latest, which moves with each release.
	ClaudeModelLatest ClaudeModelFault = "latest"
	// ClaudeModelNotFull is anything else that is not shaped like a full id.
	ClaudeModelNotFull ClaudeModelFault = "not-full"
)

// CheckClaudeModel is the one rule agent-run and the portal launch route share:
// a Claude model is a full id, never an alias.
func CheckClaudeModel(model string) ClaudeModelFault {
	base := strings.TrimSuffix(model, "[1m]")
	switch {
	case slices.Contains(ClaudeModelAliases, strings.ToLower(base)):
		return ClaudeModelAlias
	case strings.HasSuffix(base, "-latest"):
		return ClaudeModelLatest
	case !claudeModelID.MatchString(model) || !modelHasDigit.MatchString(base):
		return ClaudeModelNotFull
	}
	return ClaudeModelOK
}
