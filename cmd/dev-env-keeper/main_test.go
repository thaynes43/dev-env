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
	if o.opsGHToken || o.opsGHTokenSecret != keeper.DefaultOpsGHTokenSecret || o.opsGitHubAppDir != keeper.DefaultOpsGitHubAppDir || len(o.opsRepositories) != 0 || !maps.Equal(o.opsPermissions, keeper.DefaultOpsBotPermissions) {
		t.Errorf("ops defaults: enabled %v secret %q dir %q repos %v perms %v", o.opsGHToken, o.opsGHTokenSecret, o.opsGitHubAppDir, o.opsRepositories, o.opsPermissions)
	}
	k := o.keeperOptions("dev-env-keeper/test")
	if k.OpsGitHubApp != nil || len(k.GitHubApp.Repositories) != 0 || !maps.Equal(k.GitHubApp.Permissions, keeper.DefaultDevBotPermissions) {
		t.Fatal("flag off built an ops App or changed the dev mint")
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
		{"--enable-ops-gh-token"},
		{"--enable-ops-gh-token", "--ops-gh-token-repositories=thaynes43/haynes-ops"},
		{"--enable-ops-gh-token", "--ops-gh-token-repositories=haynes-ops,haynes-ops"},
		{"--enable-ops-gh-token", "--ops-gh-token-repositories=haynes-ops", "--ops-gh-token-secret=dev-env-gh-token"},
		{"--enable-ops-gh-token", "--ops-gh-token-repositories=haynes-ops", "--ops-github-app-dir=/etc/dev-env-keeper/github-dev-bot"},
		{"--ops-gh-token-permissions={}"},
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

func TestOpsTokenFlagMintsWithItsOwnPermissionsAndRepositories(t *testing.T) {
	o, err := parseFlags([]string{"--enable-ops-gh-token", "--ops-gh-token-repositories=haynes-ops, cigar-journal"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.opsGHToken || !maps.Equal(o.permissions, keeper.DefaultDevBotPermissions) || !maps.Equal(o.opsPermissions, keeper.DefaultOpsBotPermissions) {
		t.Fatalf("enabling ops changed the sets: dev %v ops %v", o.permissions, o.opsPermissions)
	}
	k := o.keeperOptions("dev-env-keeper/test")
	if k.OpsGitHubApp == nil || k.OpsGHTokenSecret.Name != keeper.DefaultOpsGHTokenSecret || k.OpsGHTokenSecret.Namespace != "dev-agents" {
		t.Fatalf("ops target: %+v", k.OpsGHTokenSecret)
	}
	if k.OpsGitHubApp.Dir != keeper.DefaultOpsGitHubAppDir || len(k.GitHubApp.Repositories) != 0 {
		t.Fatalf("dirs: ops %q dev repos %v", k.OpsGitHubApp.Dir, k.GitHubApp.Repositories)
	}
	if !maps.Equal(k.OpsGitHubApp.Permissions, keeper.DefaultOpsBotPermissions) || len(k.OpsGitHubApp.Repositories) != 2 || k.OpsGitHubApp.Repositories[0] != "haynes-ops" || k.OpsGitHubApp.Repositories[1] != "cigar-journal" {
		t.Fatalf("ops mint: perms %v repos %v", k.OpsGitHubApp.Permissions, k.OpsGitHubApp.Repositories)
	}
	if !maps.Equal(k.GitHubApp.Permissions, keeper.DefaultDevBotPermissions) {
		t.Fatalf("dev mint permissions %v", k.GitHubApp.Permissions)
	}
}

func TestProxmoxGrantsRequireExplicitEnableAndLeader(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.proxmoxGrants.Enabled {
		t.Fatal("credential grants enabled by default")
	}
	for _, args := range [][]string{{"--enable-proxmox-grants", "--leader-elect=false"}, {"--enable-proxmox-grants", "--ssh-ca-dir="}, {"--enable-proxmox-grants", "--proxmox-ssh-targets-file="}} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("accepted invalid configuration %v", args)
		}
	}
	o, err = parseFlags([]string{"--enable-proxmox-grants"})
	if err != nil || !o.proxmoxGrants.Enabled {
		t.Fatal("explicit feature flag was not accepted")
	}
}

// hw-ssh leases (D-89) are off by default and need leader election and a named Secret.
func TestHWSSHGrantsAreOffByDefaultAndFenced(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.hwsshGrants.Enabled || o.hwsshGrants.KeySecret != keeper.DefaultHWSSHKeySecret {
		t.Fatalf("defaults: %+v %v", o.hwsshGrants, err)
	}
	for _, args := range [][]string{{"--enable-hw-ssh-grants", "--leader-elect=false"}, {"--enable-hw-ssh-grants", "--hw-ssh-key-secret="}} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
	if o, err := parseFlags([]string{"--enable-hw-ssh-grants"}); err != nil || !o.hwsshGrants.Enabled {
		t.Fatalf("explicit enable refused: %v", err)
	}
}
