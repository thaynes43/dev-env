package apiserver

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// TitleRename is the operator's --title-rename-sessions set. The zero value
// names no session. A heartbeat for a session that is not named has no title
// object, so agentd types nothing.
type TitleRename struct {
	all   bool
	names map[string]struct{}
}

// ParseTitleRenameSessions parses a comma-separated list of session names, or
// the single value *. Empty names no session. A malformed value is refused.
func ParseTitleRenameSessions(raw string) (TitleRename, error) {
	if raw == "" {
		return TitleRename{}, nil
	}
	if raw == "*" {
		return TitleRename{all: true}, nil
	}
	if strings.Contains(raw, "*") {
		return TitleRename{}, fmt.Errorf("--title-rename-sessions: %q must be empty, a list of session names, or *", raw)
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 64 {
		return TitleRename{}, fmt.Errorf("--title-rename-sessions: at most 64 names")
	}
	names := make(map[string]struct{}, len(parts))
	for _, name := range parts {
		if name == "" || strings.TrimSpace(name) != name {
			return TitleRename{}, fmt.Errorf("--title-rename-sessions: %q must be empty, a list of session names, or *", raw)
		}
		if errs := validation.IsDNS1123Label(name); len(errs) != 0 {
			return TitleRename{}, fmt.Errorf("--title-rename-sessions: %q is not a session name", name)
		}
		if _, ok := names[name]; ok {
			return TitleRename{}, fmt.Errorf("--title-rename-sessions: %q is repeated", name)
		}
		names[name] = struct{}{}
	}
	return TitleRename{names: names}, nil
}

// Allows reports whether a heartbeat for name carries the title object.
func (t TitleRename) Allows(name string) bool {
	if t.all {
		return true
	}
	_, ok := t.names[name]
	return ok
}
