package main

import (
	"slices"
	"testing"
)

func TestRemindersAPIIsOffByDefaultAndCalendarRefsNeedIt(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.remindersAPI || len(o.reminderCalendarRefs) != 0 || reminderConfig(o) != nil {
		t.Fatalf("reminders on by default: %v %+v", err, o.remindersAPI)
	}
	o, err = parseFlags([]string{"--enable-reminders-api"})
	if err != nil || !o.remindersAPI || reminderConfig(o) == nil || len(reminderConfig(o).CalendarRefs) != 0 {
		t.Fatalf("the flag: %v", err)
	}
	o, err = parseFlags([]string{"--enable-reminders-api", "--reminder-calendar-refs=haynesnetwork,other"})
	if err != nil || !slices.Equal(o.reminderCalendarRefs, []string{"haynesnetwork", "other"}) || !slices.Equal(reminderConfig(o).CalendarRefs, o.reminderCalendarRefs) {
		t.Fatalf("refs: %v %v", err, o.reminderCalendarRefs)
	}
	for _, args := range [][]string{
		{"--reminder-calendar-refs=haynesnetwork"},
		{"--enable-reminders-api", "--reminder-calendar-refs=tom@example.com"},
		{"--enable-reminders-api", "--reminder-calendar-refs=a,a"},
		{"--enable-reminders-api", "--reminder-calendar-refs=a,,b"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
