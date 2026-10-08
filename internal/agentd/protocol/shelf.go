package protocol

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// What the shelf (D-67) and the operator exchange: the list of rescues on the
// shared volume, and a prune. The shelf is a pod that mounts only the shared
// volume and runs `agentd shelf`; the operator reaches it by exec.

// LogsRoot is the shared volume's directory of session logs (D-65):
// logs/<session>.log.
const LogsRoot = "logs"

// MinPruneAge is the youngest age a prune may remove. A prune asked to remove
// anything younger is refused: a misread retention must not empty the shelf.
const MinPruneAge = 24 * time.Hour

// rescueName is a rescue directory's name: the report's stamp (20060102-1504),
// with -2, -3 and so on for a later rescue in the same minute (D-48).
var rescueName = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}(-[0-9]+)?$`)

// ValidSessionName reports whether name can be a session's: an RFC 1123 label
// of at most 63 characters.
func ValidSessionName(name string) bool {
	return len(name) > 0 && len(name) <= 63 && dnsLabel.MatchString(name)
}

// ValidRescueName reports whether name is a rescue directory's name.
func ValidRescueName(name string) bool { return rescueName.MatchString(name) }

// RescueID is a rescue's id, <session>/<name>: its directory under rescue/.
func RescueID(session, name string) string { return session + "/" + name }

// ParseRescueID splits <session>/<name> and checks both parts, so the id can
// never name a path outside rescue/.
func ParseRescueID(id string) (session, name string, err error) {
	session, name, ok := strings.Cut(id, "/")
	if !ok || !ValidSessionName(session) || !ValidRescueName(name) {
		return "", "", fmt.Errorf("rescue %q is not <session>/<stamp>, such as dev-env-1008-001530/20261008-0024", id)
	}
	return session, name, nil
}

// RescueEntry is one rescue directory, as `agentd ctl rescues` lists it.
type RescueEntry struct {
	// ID is <session>/<name>.
	ID      string `json:"id"`
	Session string `json:"session"`
	Name    string `json:"name"`
	// Dir is rescue/<session>/<name>, relative to the shared volume's root.
	Dir string `json:"dir"`
	// CreatedAt is the manifest's, or the directory's modification time for a
	// rescue that wrote no manifest.
	CreatedAt time.Time `json:"createdAt"`
	// ModifiedAt is the directory's modification time. A prune counts a
	// rescue's age from the later of the two.
	ModifiedAt time.Time `json:"modifiedAt"`
	// Finished: the directory holds a manifest. A directory without one is a
	// rescue that did not finish (D-48), and nothing in it can be restored.
	Finished bool `json:"finished"`
	// Manifest is the manifest as written; Error says why it could not be read.
	Manifest *RescueManifest `json:"manifest,omitempty"`
	Error    string          `json:"error,omitempty"`
	// Bytes is the size of the files in the directory.
	Bytes int64 `json:"bytes"`
}

// Age is how old the entry counts as at now: from the later of its creation
// and its last change.
func (e RescueEntry) Age(now time.Time) time.Duration {
	t := e.CreatedAt
	if e.ModifiedAt.After(t) {
		t = e.ModifiedAt
	}
	return now.Sub(t)
}

// RescueList is what `agentd ctl rescues` prints, newest first.
type RescueList struct {
	Rescues []RescueEntry `json:"rescues"`
	// Unrecognized are paths under rescue/ that do not fit the layout. They
	// are listed so a human sees them, and never pruned.
	Unrecognized []string `json:"unrecognized,omitempty"`
}

// PruneRequest is what the operator sends `agentd ctl prune` on stdin.
type PruneRequest struct {
	// OlderThan is the retention, a Go duration such as 720h (D-09's 30
	// days, the templates' lifecycle.bundleRetention). It must be at least
	// MinPruneAge.
	OlderThan string `json:"olderThan"`
	// Keep lists every session that still exists. Nothing of theirs is
	// removed, however old: a suspended session's archive relies on its
	// bundle, and an archived one is restored from it.
	Keep []string `json:"keep"`
	// DryRun reports what a prune would remove and removes nothing.
	DryRun bool `json:"dryRun,omitempty"`
}

// PruneReport is what `agentd ctl prune` prints.
type PruneReport struct {
	OlderThan string `json:"olderThan"`
	DryRun    bool   `json:"dryRun,omitempty"`
	// Removed are the rescue directories and logs removed (or, on a dry run,
	// that would be), relative to the shared volume's root.
	Removed []PrunedPath `json:"removed"`
	// Kept counts rescue directories and logs left in place.
	Kept int `json:"kept"`
	// Errors are paths that could not be removed, with the reason.
	Errors []string `json:"errors,omitempty"`
}

// PrunedPath is one removed rescue directory or log.
type PrunedPath struct {
	Path    string `json:"path"`
	Session string `json:"session"`
	// Age is how old it was, as a Go duration string.
	Age   string `json:"age"`
	Bytes int64  `json:"bytes"`
}

// RescueDirOf is RescueDir for an id that ParseRescueID accepted.
func RescueDirOf(id string) (string, error) {
	session, name, err := ParseRescueID(id)
	if err != nil {
		return "", err
	}
	return path.Join(RescueRoot, session, name), nil
}
