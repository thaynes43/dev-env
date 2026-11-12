package agentrun

import (
	"strings"

	"github.com/thaynes43/dev-env/internal/sessiontitle"
)

// olderOperatorTitle is the fixed sentence when a create that carried a title
// is refused because the operator does not know the field. The create is not
// sent again without the title.
const olderOperatorTitle = "this operator does not accept a session title; agent-run did not retry the create without the title"

// normalizeTitle trims the ends and checks the title with sessiontitle.
// An empty result is no title. The reason is empty when the value can be sent.
func normalizeTitle(raw string) (string, string) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", ""
	}
	return title, sessiontitle.Validate(title)
}

func unknownTitleField(msg string) bool {
	return strings.Contains(msg, `unknown field "title"`) || strings.Contains(msg, `unknown field \"title\"`)
}

// titleRefusal replaces an older operator's unknown-field refusal. Any other
// error is returned unchanged, and the request is not rebuilt without the title.
func titleRefusal(err error, title string) error {
	if err == nil || title == "" || !unknownTitleField(err.Error()) {
		return err
	}
	return fail(ExitFailed, "%s", olderOperatorTitle)
}
