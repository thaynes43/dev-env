package reminders

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

var targetRE = regexp.MustCompile(`^(pushover|calendar):([a-z0-9][a-z0-9-]{0,62})$`)

// Target is a parsed target reference.
type Target struct {
	Kind, Ref string
}

// ParseTarget parses a reference such as pushover:default or
// calendar:haynesnetwork. A reference is a short name the keeper resolves from
// its own configuration; an address, a key or a URL does not parse.
func ParseTarget(s string) (Target, error) {
	m := targetRE.FindStringSubmatch(s)
	if m == nil || len(s) > 128 {
		return Target{}, fmt.Errorf("target %q is not a reference: use pushover:default or calendar:<name>", s)
	}
	return Target{Kind: m[1], Ref: m[2]}, nil
}

// CheckTargets applies the fixed allowlist: pushover:default, and the calendar
// references the operator was configured with. Nothing else is a target.
func CheckTargets(targets, calendarRefs []string) error {
	if len(targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	if len(targets) > 8 {
		return fmt.Errorf("%d targets, more than 8", len(targets))
	}
	seen := map[string]bool{}
	for _, raw := range targets {
		t, err := ParseTarget(raw)
		if err != nil {
			return err
		}
		if seen[raw] {
			return fmt.Errorf("target %q is listed twice", raw)
		}
		seen[raw] = true
		switch {
		case raw == v1alpha1.ReminderDefaultPushover:
		case t.Kind == v1alpha1.ReminderTargetCalendar:
			ok := false
			for _, r := range calendarRefs {
				ok = ok || r == t.Ref
			}
			if !ok {
				if len(calendarRefs) == 0 {
					return fmt.Errorf("target %q: this operator has no calendar target configured", raw)
				}
				return fmt.Errorf("target %q is not one of: %s", raw, strings.Join(prefixed("calendar:", calendarRefs), ", "))
			}
		default:
			return fmt.Errorf("target %q is not allowed: the only Pushover reference is %s", raw, v1alpha1.ReminderDefaultPushover)
		}
	}
	return nil
}

func prefixed(p string, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = p + s
	}
	return out
}
