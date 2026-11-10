package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// otherDevice is a directory on another filesystem than home, so agentd takes
// it for a mounted shared volume. The test is skipped where there is none.
func otherDevice(t *testing.T, home string) string {
	t.Helper()
	var hm syscall.Stat_t
	if err := syscall.Stat(home, &hm); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{os.Getenv("XDG_RUNTIME_DIR"), "/dev/shm"} {
		var st syscall.Stat_t
		if c == "" || syscall.Stat(c, &st) != nil || st.Dev == hm.Dev {
			continue
		}
		dir, err := os.MkdirTemp(c, "agentd-test-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir
	}
	t.Skip("no filesystem other than the test's to stand for the shared volume")
	return ""
}

func TestCtlRescueStrictHome(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	for _, args := range [][]string{
		{"ctl", "rescue", "--strict-home"},
		{"ctl", "rescue", "--stop-agent", "--strict-home"},
		{"ctl", "rescue", "--strict-home", "--stop-agent"},
	} {
		code, out, errOut := runArgs(args, env)
		if code != exitOK || !strings.Contains(out, `"strictHome": {}`) || !strings.Contains(out, `"volumeEmpty": true`) {
			t.Errorf("%q: %d %s %s", args, code, out, errOut)
		}
		if stop := strings.Contains(out, `"wasRunning"`); stop != strings.Contains(strings.Join(args, " "), "--stop-agent") {
			t.Errorf("%q: the agent stop is reported %t", args, stop)
		}
	}
	for _, args := range [][]string{
		{"ctl", "rescue", "--strict-home", "--strict-home"},
		{"ctl", "rescue", "--stop-agent", "--stop-agent", "--strict-home"},
		{"ctl", "rescue", "--strict-home", "--workspace-stop-proof-stdin"},
		{"ctl", "rescue", "--stop-agent", "--workspace-stop-proof-stdin", "--strict-home"},
		{"ctl", "rescue", "--strict-home", "x"},
		{"ctl", "rescue", "--strict-home=true"},
		{"ctl", "status", "--strict-home"},
	} {
		if code, _, errOut := runArgs(args, env); code != exitUsage || !strings.Contains(errOut, "--strict-home") {
			t.Errorf("%q: exit %d, want usage: %s", args, code, errOut)
		}
	}

	// A repository directly under ~/work that no clone lists: refused with
	// the report on exit 1; without the flag the rescue is as before.
	if err := os.MkdirAll(filepath.Join(home, "work", "stray", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runArgs([]string{"ctl", "rescue", "--stop-agent", "--strict-home"}, env)
	var rep protocol.RescueReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != exitFailure || rep.OK || rep.StrictHome == nil ||
		rep.StrictHome.Refused != "stray repository "+filepath.Join(home, "work", "stray") {
		t.Fatalf("strict refusal: %d %s %s %v", code, out, errOut, err)
	}
	code, out, _ = runArgs([]string{"ctl", "rescue", "--stop-agent"}, env)
	if code != exitOK || strings.Contains(out, "strictHome") {
		t.Errorf("without the flag: %d %s", code, out)
	}
}

func TestCtlRescueVerify(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"ctl", "rescue-verify", "x"},
		{"ctl", "rescue-verify", "--id"},
		{"ctl", "rescue-verify", "--id", ""},
		{"ctl", "rescue-verify", "--id", "../x/20261110-0213"},
		{"ctl", "rescue-verify", "--id", "s-1"},
		{"ctl", "rescue-verify", "--session", "s-1"},
	} {
		if code, out, errOut := runArgs(args, map[string]string{"HOME": home}); code != exitUsage || out != "" || !strings.Contains(errOut, "rescue-verify") {
			t.Errorf("%q: %d %q %q", args, code, out, errOut)
		}
	}
	// A shared directory on the session volume is not the shared volume: a
	// failure, with nothing on stdout.
	if code, out, errOut := runArgs([]string{"ctl", "rescue-verify"}, map[string]string{"HOME": home}); code != exitFailure || out != "" || !strings.Contains(errOut, "rescue-verify") {
		t.Errorf("no shared volume: %d %q %q", code, out, errOut)
	}

	env := map[string]string{"HOME": home, "AGENTD_SHARED_DIR": otherDevice(t, home)}
	code, out, errOut := runArgs([]string{"ctl", "rescue-verify"}, env)
	var res protocol.RescueVerifyResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != exitOK || !res.OK || res.Version != 1 || res.SharedTotalBytes <= 0 {
		t.Fatalf("space only: %d %s %s %v", code, out, errOut, err)
	}
	code, out, errOut = runArgs([]string{"ctl", "rescue-verify", "--id", "s-1/20261110-0213"}, env)
	res = protocol.RescueVerifyResult{}
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != exitFailure || res.OK || res.Refusal != protocol.RescueVerifyBundleMissing || res.ID != "s-1/20261110-0213" {
		t.Fatalf("a missing rescue: %d %s %s %v", code, out, errOut, err)
	}
}

func TestCtlDropLog(t *testing.T) {
	home := t.TempDir()
	session := `{"name":"r-1","repo":"r","agent":"claude","mode":"task","model":"claude-opus-5-5","prompt":"p"}`
	if code, _, errOut := runArgs([]string{"ctl", "drop-log", "x"}, map[string]string{"HOME": home, "AGENTD_SESSION": session}); code != exitUsage || !strings.Contains(errOut, "drop-log") {
		t.Errorf("drop-log x: %d %s", code, errOut)
	}
	if code, out, _ := runArgs([]string{"ctl", "drop-log"}, map[string]string{"HOME": home}); code != exitFailure || out != "" {
		t.Errorf("no session: %d %q", code, out)
	}
	if code, out, _ := runArgs([]string{"ctl", "drop-log"}, map[string]string{"HOME": home, "AGENTD_SESSION": session}); code != exitFailure || out != "" {
		t.Errorf("no shared volume: %d %q", code, out)
	}

	shared := otherDevice(t, home)
	env := map[string]string{"HOME": home, "AGENTD_SESSION": session, "AGENTD_SHARED_DIR": shared}
	for _, f := range []string{"r-1.log", "r-2.log"} {
		if err := os.MkdirAll(filepath.Join(shared, "logs"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(shared, "logs", f), []byte("log\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := runArgs([]string{"ctl", "drop-log"}, env)
	var res protocol.DropLogResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != exitOK || !res.Removed || res.Path != "logs/r-1.log" || res.Bytes != 4 {
		t.Fatalf("drop-log: %d %s %s %v", code, out, errOut, err)
	}
	if _, err := os.Stat(filepath.Join(shared, "logs", "r-2.log")); err != nil {
		t.Errorf("another session's log went: %v", err)
	}
	if code, out, _ := runArgs([]string{"ctl", "drop-log"}, env); code != exitOK || !strings.Contains(out, `"removed": false`) {
		t.Errorf("again: %d %s", code, out)
	}
}
