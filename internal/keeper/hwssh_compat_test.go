package keeper

// Compatibility with the running 2.45 session (D-89). The helper below is the ACTUAL
// hw-ssh from the agent image 2.45.0, unmodified, committed as testdata:
//
//	source: git tag v2.45.0, path images/agent/tools/hw-ssh.sh
//	sha256: 84eb60d311e02668aa8b6592468f6867829fffeb6bfed7882af4b7f58a6217f6
//
// It knows nothing of leases. A 2.45 session is given the leased key with HW_SSH_KEY.
// The test uses the real installer script for the lease and for its removal, and a
// fake ssh on PATH that records the -i argument. Nothing here touches a real host or key.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const hwsshV245SHA256 = "84eb60d311e02668aa8b6592468f6867829fffeb6bfed7882af4b7f58a6217f6"

func run245Helper(t *testing.T, home string, env ...string) (stdout, stderr string, code int) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/usr/bin/env bash\nwhile [[ $# -gt 0 ]]; do if [[ $1 == -i ]]; then printf 'SSH-I=%s\\n' \"$2\"; fi; shift; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("testdata/hw-ssh-v2.45.0.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--raw", "dev-env@h.example", "id")
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + home, "HW_SSH_NOEXEC=1"}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func TestHWSSHLeaseWorksWithTheUnmodified245HelperAndEndsWithCleanup(t *testing.T) {
	raw, err := os.ReadFile("testdata/hw-ssh-v2.45.0.sh")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != hwsshV245SHA256 {
		t.Fatalf("the 2.45 helper testdata changed: %x", sum)
	}
	xdg, home := t.TempDir(), t.TempDir() // the session's home has no ~/.ssh/dev-env-hw
	key, fp := syntheticKey(t, "")
	const grant, guid, puid = "grant-1028-000000-abcd", "10000000-0000-4000-8000-000000000001", "40000000-0000-4000-8000-000000000001"
	leaseKey := filepath.Join(xdg, "dev-env-hwssh", grant, "key")
	exp := "2026-10-28T01:00:00Z"

	if code := runScript(t, xdg, puid, key, "install", grant, guid, puid, exp, fp); code != 0 {
		t.Fatalf("install exit %d", code)
	}
	// The override is used.
	out, errs, code := run245Helper(t, home, "HW_SSH_KEY="+leaseKey)
	if code != 0 || !strings.Contains(out, "SSH-I="+leaseKey) {
		t.Fatalf("exit %d out %q err %q", code, out, errs)
	}
	// A failed removal (the retry case) leaves the lease usable: the keeper must retry.
	if code := runScript(t, xdg, "40000000-0000-4000-8000-000000000999", nil, "remove", grant, guid, puid); code != 1 {
		t.Fatalf("a removal from the wrong pod exit %d", code)
	}
	if out, _, code = run245Helper(t, home, "HW_SSH_KEY="+leaseKey); code != 0 || !strings.Contains(out, "SSH-I="+leaseKey) {
		t.Fatalf("after a failed removal: exit %d out %q", code, out)
	}
	// The retried removal succeeds. The 2.45 helper then cannot find the key and does
	// not call ssh, with the override set or not.
	if code := runScript(t, xdg, puid, nil, "remove", grant, guid, puid); code != 0 {
		t.Fatalf("remove exit %d", code)
	}
	for name, env := range map[string][]string{"override still set": {"HW_SSH_KEY=" + leaseKey}, "override unset": nil} {
		out, errs, code := run245Helper(t, home, env...)
		if code != 1 || strings.Contains(out, "SSH-I=") || !strings.Contains(errs, "no key at") {
			t.Fatalf("%s: exit %d out %q err %q", name, code, out, errs)
		}
	}
	// What it does when the session already has the default key (a profile that carries
	// it): with the override unset it uses that key, as it did before any lease. A
	// removed lease therefore adds and removes nothing for such a session.
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "dev-env-hw"), []byte("profile key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, code = run245Helper(t, home); code != 0 || !strings.Contains(out, "SSH-I="+filepath.Join(home, ".ssh", "dev-env-hw")) {
		t.Fatalf("default key fallback: exit %d out %q", code, out)
	}
}
