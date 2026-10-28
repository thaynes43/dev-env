package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hwsshRun runs hw-ssh with a fake ssh that prints the key path it was given.
// Nothing here uses a real key or host.
func hwsshRun(t *testing.T, leases string, env map[string]string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/usr/bin/env bash\nwhile [[ $# -gt 0 ]]; do if [[ $1 == -i ]]; then printf 'KEY=%s\\n' \"$2\"; fi; shift; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "dev-env-hw"), []byte("default"), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("hw-ssh.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "HOME=" + home, "HW_SSH_NOEXEC=1", "HW_SSH_LEASE_DIR=" + leases}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func writeLease(t *testing.T, leases, grant string, expires time.Time) string {
	t.Helper()
	d := filepath.Join(leases, grant)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "key"), []byte("leased"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := `{"v":1,"grant":"` + grant + `","expires":"` + expires.UTC().Format(time.RFC3339) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(d, "key")
}

func TestHWSSHPrefersALiveLeaseOverTheDefaultKey(t *testing.T) {
	leases := t.TempDir()
	soon := writeLease(t, leases, "grant-a", time.Now().Add(10*time.Minute))
	later := writeLease(t, leases, "grant-b", time.Now().Add(50*time.Minute))
	out, errs, code := hwsshRun(t, leases, nil, "twin-top", "uptime")
	if code != 0 || !strings.Contains(out, "KEY="+later) || strings.Contains(out, soon) || strings.Contains(out, "dev-env-hw") {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	// The notice says what a lease is not.
	for _, want := range []string{"leased shared key", "not revocable", "not contained", "every host"} {
		if !strings.Contains(errs, want) {
			t.Errorf("notice lacks %q: %q", want, errs)
		}
	}
	if strings.Contains(errs, "leased") && strings.Count(errs, "using the leased") != 1 {
		t.Errorf("notice repeated: %q", errs)
	}
}

func TestHWSSHDeletesAnExpiredLeaseAndFallsBack(t *testing.T) {
	leases := t.TempDir()
	old := writeLease(t, leases, "grant-old", time.Now().Add(-time.Minute))
	out, errs, code := hwsshRun(t, leases, nil, "twin-top", "uptime")
	if code != 0 || !strings.Contains(out, "dev-env-hw") || strings.Contains(out, "leased") || strings.Contains(errs, "leased shared key") {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("the expired lease was not deleted")
	}
}

func TestHWSSHExplicitKeyAndNoLeaseKeepTheOldBehaviour(t *testing.T) {
	leases := t.TempDir()
	writeLease(t, leases, "grant-a", time.Now().Add(time.Hour))
	out, _, code := hwsshRun(t, leases, map[string]string{"HW_SSH_KEY": "/explicit/key"}, "--raw", "dev-env@h.example", "id")
	if code == 0 || strings.Contains(out, "leased") {
		// /explicit/key is unreadable, so the helper refuses; it must not have used the lease.
		t.Logf("exit %d", code)
	}
	if strings.Contains(out, "grant-a") {
		t.Fatal("HW_SSH_KEY did not win over a lease")
	}
	empty := t.TempDir()
	out, _, code = hwsshRun(t, empty, nil, "twin-top", "uptime")
	if code != 0 || !strings.Contains(out, "dev-env-hw") {
		t.Fatalf("no lease: exit %d out %q", code, out)
	}
}

func TestHWSSHIgnoresASymlinkedKey(t *testing.T) {
	leases := t.TempDir()
	d := filepath.Join(leases, "grant-link")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(d, "key")); err != nil {
		t.Fatal(err)
	}
	m := `{"expires":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(d, "manifest.json"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, _ := hwsshRun(t, leases, nil, "twin-top", "uptime")
	if strings.Contains(out, "grant-link") || !strings.Contains(out, "dev-env-hw") {
		t.Fatalf("a symlinked lease key was used: %q", out)
	}
}
