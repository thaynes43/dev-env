package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/keeper"
)

func TestAskNotificationFlags(t *testing.T) {
	var logs bytes.Buffer
	previous := noticeFailureLog
	noticeFailureLog = &logs
	t.Cleanup(func() { noticeFailureLog = previous })
	o, err := parseFlags(nil)
	if err != nil || o.askNotifications.Enabled || o.askNotifications.Window != "" || o.askNotifications.PortalOrigin != "" || o.askNotifications.StateConfigMap != keeper.DefaultAskNotificationConfigMap {
		t.Fatalf("Ask notifications on by default: %+v %v", o.askNotifications, err)
	}
	o, err = parseFlags([]string{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-window=09:00-21:00", "--ask-notify-timezone=America/New_York"})
	if err != nil || !o.askNotifications.Enabled || o.askNotifications.Window != "09:00-21:00" {
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
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-window=bad"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-window=09:00-09:00"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-timezone=unknown"},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-state-configmap=" + keeper.DefaultNoticeStateConfigMap},
		{"--enable-ask-notifications", "--ask-notify-portal-origin=https://portal.example", "--ask-notify-state-configmap=shared", "--notice-state-configmap=shared"},
	} {
		logs.Reset()
		if o, err := parseFlags(args); err != nil || o.askNotifications.Enabled {
			t.Errorf("invalid Ask config stopped parsing or remained enabled: %v: %+v %v", args, o.askNotifications, err)
		}
		if strings.Count(logs.String(), `"msg":"ask notifications disabled: invalid configuration"`) != 1 || strings.Contains(logs.String(), "portal.example") || strings.Contains(logs.String(), "secret") {
			t.Errorf("missing fixed Ask diagnostic or flag values leaked: %s", logs.String())
		}
	}
}
