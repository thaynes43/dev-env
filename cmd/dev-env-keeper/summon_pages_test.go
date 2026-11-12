package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/keeper"
)

// Story 782: the pager's flags are off or empty by default; a bad or
// incomplete set disables only the pager, logs why once without a value, and
// never stops parsing.
func TestSummonPageFlags(t *testing.T) {
	var logs bytes.Buffer
	previous := noticeFailureLog
	noticeFailureLog = &logs
	t.Cleanup(func() { noticeFailureLog = previous })
	o, err := parseFlags(nil)
	if err != nil || o.summonPages.Enabled || o.summonPages.PortalOrigin != "" || o.summonPages.Drill != "" ||
		o.summonPages.StateConfigMap != keeper.DefaultSummonPagesConfigMap || o.summonPages.SessionNamespace != "dev-agents" {
		t.Fatalf("summon pages on by default: %+v %v", o.summonPages, err)
	}
	o, err = parseFlags([]string{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example", "--summon-pages-drill=install-1", "--session-namespace=agents"})
	if err != nil || !o.summonPages.Enabled || o.summonPages.Drill != "install-1" || o.summonPages.SessionNamespace != "agents" ||
		!o.keeperOptions("ua").SummonPages.Enabled || o.keeperOptions("ua").SummonPages.PortalOrigin != "https://console.example" {
		t.Fatalf("explicit flags: %+v %v", o.summonPages, err)
	}
	if logs.Len() != 0 {
		t.Fatalf("valid flags logged: %s", logs.String())
	}
	for _, args := range [][]string{
		{"--enable-summon-pages"},
		{"--enable-summon-pages", "--summon-pages-portal-origin=http://console.example"},
		{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example/?token=secret"},
		{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example", "--summon-pages-drill=Secret_Drill"},
		{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example", "--pushover-url=http://push.example"},
		{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example", "--leader-elect=false"},
		{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example", "--summon-pages-state-configmap=" + keeper.DefaultAskNotificationConfigMap},
		{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example", "--summon-pages-state-configmap=shared", "--notice-state-configmap=shared"},
	} {
		logs.Reset()
		o, err := parseFlags(args)
		if err != nil || o.summonPages.Enabled || o.keeperOptions("ua").SummonPages.Enabled {
			t.Errorf("invalid pager flags stopped parsing or stayed on: %v: %+v %v", args, o.summonPages, err)
		}
		if strings.Count(logs.String(), `"msg":"summon pages disabled: invalid configuration"`) != 1 || strings.Contains(logs.String(), "console.example") ||
			strings.Contains(strings.ToLower(logs.String()), "secret") {
			t.Errorf("missing fixed diagnostic or flag values leaked: %s", logs.String())
		}
	}
}
