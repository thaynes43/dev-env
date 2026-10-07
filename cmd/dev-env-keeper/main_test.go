package main

import (
	"maps"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/keeper"
)

func TestParseFlagsDefaults(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.namespace != "dev-env-system" || o.secretNamespace != "dev-agents" || o.ghTokenSecret != "dev-env-gh-token" ||
		o.githubAppDir != "/etc/dev-env-keeper/github-dev-bot" || o.githubAPIURL != "https://api.github.com" ||
		o.interval != 40*time.Minute || !o.leaderElect || o.probeAddr != ":8081" {
		t.Errorf("defaults %+v", o)
	}
	if !maps.Equal(o.permissions, keeper.DefaultDevBotPermissions) {
		t.Errorf("default permissions %v, want v1's %v", o.permissions, keeper.DefaultDevBotPermissions)
	}

	t.Setenv("POD_NAMESPACE", "elsewhere")
	if o, err := parseFlags(nil); err != nil || o.namespace != "elsewhere" {
		t.Errorf("the namespace from the downward API: %q %v", o.namespace, err)
	}
}

func TestParseFlagsRefuses(t *testing.T) {
	for _, bad := range [][]string{
		{"--gh-token-permissions={}"},
		{"--gh-token-permissions={\"contents\":\"everything\"}"},
		{"--refresh-interval=10s"},
		{"--github-api-url=http://api.github.com"},
		{"--gh-token-secret="},
		{"stray"},
	} {
		if _, err := parseFlags(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	o, err := parseFlags([]string{`--gh-token-permissions={"contents":"read"}`, "--refresh-interval=30m"})
	if err != nil || o.permissions["contents"] != "read" || len(o.permissions) != 1 || o.interval != 30*time.Minute {
		t.Errorf("overrides: %+v %v", o, err)
	}
}
