package main

import (
	"testing"
	"time"
)

func TestRemindersAreOffByDefault(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.reminders.Enabled || o.reminders.Namespace != "" || o.reminders.Every != 30*time.Second || o.reminders.MissGrace != 6*time.Hour {
		t.Fatalf("defaults: %v %+v", err, o.reminders)
	}
	o, err = parseFlags([]string{"--enable-reminders", "--reminder-namespace=dev-env-system", "--reminder-poll=10s"})
	if err != nil || !o.reminders.Enabled || o.reminders.Namespace != "dev-env-system" || o.reminders.Every != 10*time.Second {
		t.Fatalf("explicit: %v %+v", err, o.reminders)
	}
}
