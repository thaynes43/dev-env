package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/keeper"
	"github.com/thaynes43/dev-env/internal/sessionraise"
)

func TestRaiseNoticeFlags(t *testing.T) {
	var logs bytes.Buffer
	previous := noticeFailureLog
	noticeFailureLog = &logs
	t.Cleanup(func() { noticeFailureLog = previous })
	for _, tc := range []struct {
		name string
		args []string
		on   bool
		key  string
	}{
		{name: "default off", key: sessionraise.KeeperSecretName},
		{name: "explicit off", args: []string{"--enable-session-raise-notices=false"}, key: sessionraise.KeeperSecretName},
		{name: "on", args: []string{"--enable-session-raise-notices"}, on: true, key: sessionraise.KeeperSecretName},
		{name: "override", args: []string{"--enable-session-raise-notices=true", "--session-raise-keeper-key-secret=other-key", "--session-namespace=sessions"}, on: true, key: "other-key"},
		{name: "key flag alone stays off", args: []string{"--session-raise-keeper-key-secret=other-key"}, key: "other-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			o, err := parseFlags(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			k := o.keeperOptions("test")
			if k.RaisePages.Enabled != tc.on || k.RaisePages.KeySecret != tc.key || k.RaisePages.SessionNamespace != o.proxmoxGrants.SessionNamespace {
				t.Fatalf("raise options: %+v", k.RaisePages)
			}
			if k.GitHubApp == nil || k.GHTokenSecret.Name != keeper.DefaultGHTokenSecret || logs.Len() != 0 {
				t.Fatalf("raise flags disturbed credentials or logged: %s", logs.String())
			}
		})
	}
}

func TestRaiseNoticeInvalidFlagsDisableOnlyRaise(t *testing.T) {
	var logs bytes.Buffer
	previous := noticeFailureLog
	noticeFailureLog = &logs
	t.Cleanup(func() { noticeFailureLog = previous })
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "malformed bool", args: []string{"--enable-session-raise-notices=PRIVATEVALUE"}},
		{name: "empty bool", args: []string{"--enable-session-raise-notices="}},
		{name: "malformed then on", args: []string{"--enable-session-raise-notices=PRIVATEVALUE", "--enable-session-raise-notices=true"}},
		{name: "on then malformed", args: []string{"--enable-session-raise-notices=true", "--enable-session-raise-notices=PRIVATEVALUE"}},
		{name: "missing key value", args: []string{"--enable-session-raise-notices", "--session-raise-keeper-key-secret"}},
		{name: "missing key value off", args: []string{"-session-raise-keeper-key-secret"}},
		{name: "empty key", args: []string{"--enable-session-raise-notices", "--session-raise-keeper-key-secret="}},
		{name: "leader off", args: []string{"--enable-session-raise-notices", "--leader-elect=false"}},
		{name: "invalid notices", args: []string{"--enable-session-raise-notices", "--pushover-url=http://invalid.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			// Unrelated credential options and the summon pager remain intact.
			args := append([]string{"--enable-codex-auth", "--refresh-interval=31m"}, tc.args...)
			if tc.name == "leader off" {
				args = tc.args // Codex itself requires leader election.
			} else if tc.name != "invalid notices" {
				args = append(args, "--enable-summon-pages", "--summon-pages-portal-origin=https://console.example")
			}
			// Missing string values must remain last on the command line.
			if strings.HasPrefix(tc.name, "missing key value") {
				args = append([]string{"--enable-codex-auth", "--refresh-interval=31m"}, tc.args...)
			}
			o, err := parseFlags(args)
			if err != nil || o.keeperOptions("test").RaisePages.Enabled {
				t.Fatalf("invalid raise flags stopped parsing or stayed on: %v", err)
			}
			if tc.name != "leader off" && (!o.codexAuth.Enabled || o.interval.String() != "31m0s") {
				t.Fatal("invalid raise flags disturbed credential options")
			}
			if tc.name == "malformed bool" && !o.summonPages.Enabled {
				t.Fatal("invalid raise flag disabled the independent summon pager")
			}
			if strings.Count(logs.String(), `"msg":"raise notices disabled: invalid configuration"`) != 1 || strings.Contains(logs.String(), "PRIVATEVALUE") || strings.Contains(logs.String(), "invalid.example") {
				t.Fatalf("missing fixed diagnostic or leaked value: %s", logs.String())
			}
		})
	}
	// Unrelated parse failures keep their existing fatal behavior.
	for _, args := range [][]string{{"--leader-elect=oops"}, {"--enable-session-raise-notices", "--refresh-interval"}} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("unrelated invalid flag accepted: %v", args)
		}
	}
}
