package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// Story 782 fix round 1, F2: a pager flag with a malformed value, the boolean
// included, or given last with no value, disables only the pager and logs why
// without the value. Other flags keep today's parsing and failures.
func TestSummonPageFlagsMalformed(t *testing.T) {
	var logs bytes.Buffer
	previous := noticeFailureLog
	noticeFailureLog = &logs
	t.Cleanup(func() { noticeFailureLog = previous })
	const origin = "--summon-pages-portal-origin=https://console.example"
	for _, tc := range []struct {
		args   []string
		reason string
	}{
		{[]string{"--enable-summon-pages=SECRETVALUE", origin}, "not true or false"},
		{[]string{"--enable-summon-pages=", origin}, "not true or false"},
		{[]string{origin, "--enable-summon-pages=SECRETVALUE", "--leader-elect=true"}, "not true or false"},
		{[]string{"--enable-summon-pages=true", origin, "--enable-summon-pages=SECRETVALUE"}, "not true or false"},
		{[]string{"--enable-summon-pages", origin, "--summon-pages-drill"}, "given last with no value"},
		{[]string{"--enable-summon-pages", origin, "-summon-pages-state-configmap"}, "given last with no value"},
		{[]string{"--enable-summon-pages", "--summon-pages-portal-origin"}, "given last with no value"},
		{[]string{"--summon-pages-drill"}, "given last with no value"},
	} {
		logs.Reset()
		o, err := parseFlags(tc.args)
		if err != nil || o.summonPages.Enabled || o.keeperOptions("ua").SummonPages.Enabled {
			t.Errorf("%v: stopped parsing or stayed on: %+v %v", tc.args, o.summonPages, err)
			continue
		}
		if !o.leaderElect || o.namespace == "" || o.interval != keeper.DefaultInterval {
			t.Errorf("%v: another flag lost its value: %+v", tc.args, o)
		}
		if strings.Count(logs.String(), `"msg":"summon pages disabled: invalid configuration"`) != 1 || !strings.Contains(logs.String(), tc.reason) ||
			strings.Contains(logs.String(), "SECRETVALUE") || strings.Contains(logs.String(), "console.example") {
			t.Errorf("%v: diagnostic %s", tc.args, logs.String())
		}
	}
	for _, args := range [][]string{
		{"--leader-elect=oops"},
		{"--enable-summon-pages", origin, "--refresh-interval"},
		{"--refresh-interval", "--summon-pages-drill"},
		{"--enable-summon-pages", "oops"},
		{"--enable-summon-pages", origin, "--no-such-flag"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%v: another flag's failure was not kept", args)
		}
	}
}

// Story 782 fix round 1, F2, the startup proof: the keeper binary's main, run
// with a malformed pager flag, logs the pager off and goes on to start; it
// stops only at the missing cluster configuration this test gives it. A
// malformed flag of another kind still stops it at parsing. The child gets
// no cluster: no kubeconfig, and no in-cluster variables, which client-go
// would otherwise fall back to inside a pod.
func TestSummonPageFlagsKeeperStarts(t *testing.T) {
	const helperEnv, argsEnv = "DEV_ENV_TEST_SUMMON_PAGE_FLAGS", "DEV_ENV_TEST_SUMMON_PAGE_ARGS"
	if os.Getenv(helperEnv) == "true" {
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" || os.Getenv("KUBERNETES_SERVICE_PORT") != "" {
			t.Fatal("the child could reach the cluster it runs in")
		}
		os.Args = append([]string{binaryName}, strings.Split(os.Getenv(argsEnv), "\x1f")...)
		main()
		return
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KUBERNETES_") && !strings.HasPrefix(kv, "KUBECONFIG=") {
			env = append(env, kv)
		}
	}
	dir := t.TempDir()
	for _, tc := range []struct {
		args   []string
		starts bool
	}{
		{[]string{"--enable-summon-pages=SECRETVALUE"}, true},
		{[]string{"--enable-summon-pages", "--summon-pages-portal-origin=https://console.example", "--summon-pages-drill"}, true},
		{[]string{"--leader-elect=oops"}, false},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSummonPageFlagsKeeperStarts$")
		command.Env = append(append([]string(nil), env...), helperEnv+"=true", argsEnv+"="+strings.Join(tc.args, "\x1f"),
			"KUBECONFIG="+filepath.Join(dir, "missing-kubeconfig"), "POD_NAMESPACE=dev-env-system")
		raw, err := command.CombinedOutput()
		cancel()
		out := string(raw)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Errorf("%v: exit %v, output %s", tc.args, err, out)
			continue
		}
		started := strings.Contains(out, `"msg":"starting"`)
		if started != tc.starts || strings.Contains(out, "SECRETVALUE") {
			t.Errorf("%v: started %v, output %s", tc.args, started, out)
		}
		if tc.starts && (strings.Count(out, `"msg":"summon pages disabled: invalid configuration"`) != 1 || strings.Contains(out, "invalid boolean") || strings.Contains(out, "needs an argument")) {
			t.Errorf("%v: output %s", tc.args, out)
		}
	}
}
