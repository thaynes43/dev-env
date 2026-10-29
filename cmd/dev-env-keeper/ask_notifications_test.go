package main

import (
	"testing"

	"github.com/thaynes43/dev-env/internal/keeper"
)

func TestAskNotificationFlags(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.askNotifications.Enabled || o.askNotifications.QuietHours != "" || o.askNotifications.PortalOrigin != "" || o.askNotifications.StateConfigMap != keeper.DefaultAskNotificationConfigMap {
		t.Fatalf("Ask notifications on by default: %+v %v", o.askNotifications, err)
	}
	o, err = parseFlags([]string{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-quiet-hours=09:00-21:00", "--ask-notify-timezone=America/New_York"})
	if err != nil || !o.askNotifications.Enabled || o.askNotifications.QuietHours != "09:00-21:00" {
		t.Fatalf("explicit flags: %+v %v", o.askNotifications, err)
	}
	for _, args := range [][]string{
		{"--enable-ask-notifications"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=http://portal.example"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://token@portal.example"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example/?token=secret"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example/path"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example#"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--pushover-url=http://push.example"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--leader-elect=false"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-quiet-hours=bad"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-quiet-hours=09:00-09:00"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-timezone=unknown"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
