package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver"
)

// The reminders API (DESIGN-032, OpenProject #206) is off by default. It needs
// the Reminder CRD and a reviewed Role on it in haynes-ops before it can work.

var reminderCalendarRef = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// parseReminderCalendarRefs validates the calendar references reminders may name.
func parseReminderCalendarRefs(enabled bool, raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	if !enabled {
		return nil, errors.New("reminder calendar references require enable-reminders-api")
	}
	var refs []string
	for _, r := range strings.Split(raw, ",") {
		if !reminderCalendarRef.MatchString(r) || slices.Contains(refs, r) {
			return nil, fmt.Errorf("reminder calendar reference %q is not a short unique name", r)
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// reminderConfig is the Server's setting: nil while the flag is off.
func reminderConfig(o options) *apiserver.ReminderConfig {
	if !o.remindersAPI {
		return nil
	}
	return &apiserver.ReminderConfig{Namespace: o.templatesNamespace, CalendarRefs: o.reminderCalendarRefs}
}
