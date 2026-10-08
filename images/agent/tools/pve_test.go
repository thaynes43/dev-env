package tools

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func pveFixture(t *testing.T, vars map[string]string, args ...string) (int, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	capture, agentCapture := filepath.Join(dir, "curl.request"), filepath.Join(dir, "agentd.request")
	for name, script := range map[string]string{
		"curl": `#!/usr/bin/env bash
set -euo pipefail
read -r header
printf '%s\n' "$header" "$@" >> "$PVE_TEST_CAPTURE"
if [[ "${PVE_TEST_HTTP_FAIL:-}" == 1 ]]; then
  printf '{"message":"provider echoed %s"}\n403' "${PVE_OPERATOR_TOKEN_SECRET:-}"
elif [[ "$*" == *"/api2/json/cluster/resources"* ]]; then
  printf '{"data":[{"vmid":999,"node":"fixture-node","type":"qemu"}]}\n200'
else
  printf '{"data":{"ok":true}}\n200'
fi
`,
		"agentd": `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$PVE_TEST_AGENT_CAPTURE"
case "${PVE_TEST_GRANT:-}" in
  missing) exit 4 ;;
  invalid) exit 1 ;;
  failed) exit 7 ;;
esac
[[ "$2" != credential-available ]] || exit 0
[[ "${PVE_TEST_GRANT:-}" != race-expiry ]] || exit 4
while [[ "$1" != -- ]]; do shift; done
shift
export PVE_OPERATOR_TOKEN_ID='dev-env@pve!grant-fixture'
export PVE_OPERATOR_TOKEN_SECRET='fixture-grant-secret'
export PVE_GRANT_ACTIVE=1
if [[ "${PVE_TEST_GRANT:-}" == child-four ]]; then "$@"; exit 4; fi
exec "$@"
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := filepath.Abs("pve.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "PVE_") || key == "DEV_ENV_GRANTS_DIR" || key == "PATH" {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "PATH="+dir+":"+os.Getenv("PATH"), "DEV_ENV_GRANTS_DIR="+t.TempDir(), "PVE_TEST_CAPTURE="+capture, "PVE_TEST_AGENT_CAPTURE="+agentCapture, "PVE_TOKEN_ID=fixture-reader", "PVE_TOKEN_SECRET=fixture-read-secret")
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var e *exec.ExitError
		if !errors.As(err, &e) {
			t.Fatal("pve could not execute")
		}
		code = e.ExitCode()
	}
	for _, secret := range []string{"fixture-read-secret", "fixture-grant-secret", "fixture-v1-secret"} {
		if strings.Contains(string(out), secret) {
			t.Fatal("helper output exposed credential")
		}
	}
	request, _ := os.ReadFile(capture)
	install, _ := os.ReadFile(agentCapture)
	return code, string(out), string(request), string(install)
}

func TestPVECredentialSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		vars   map[string]string
		args   []string
		code   int
		header string
		grant  bool
	}{
		{"grant", nil, []string{"get", "/test", "--raw"}, 0, "dev-env@pve!grant-fixture=fixture-grant-secret", true},
		{"v1 operator", map[string]string{"PVE_OPERATOR_TOKEN_ID": "fixture-v1", "PVE_OPERATOR_TOKEN_SECRET": "fixture-v1-secret"}, []string{"get", "/test"}, 0, "fixture-v1=fixture-v1-secret", false},
		{"ro anywhere", nil, []string{"get", "/test", "--raw", "--ro"}, 0, "fixture-reader=fixture-read-secret", false},
		{"expired falls back", map[string]string{"PVE_TEST_GRANT": "missing"}, []string{"get", "/test"}, 0, "fixture-reader=fixture-read-secret", true},
		{"expired refuses write", map[string]string{"PVE_TEST_GRANT": "missing"}, []string{"delete", "/test", "--yes"}, 1, "", true},
		{"invalid fails closed", map[string]string{"PVE_TEST_GRANT": "invalid"}, []string{"get", "/test"}, 1, "", true},
		{"exit preserved", map[string]string{"PVE_TEST_GRANT": "failed"}, []string{"get", "/test"}, 7, "", true},
		{"child exit 4 preserved", map[string]string{"PVE_TEST_GRANT": "child-four"}, []string{"delete", "/test", "--yes"}, 4, "dev-env@pve!grant-fixture=fixture-grant-secret", true},
		{"expiry race not retried", map[string]string{"PVE_TEST_GRANT": "race-expiry"}, []string{"delete", "/test", "--yes"}, 4, "", true},
		{"marker alone refused", map[string]string{"PVE_GRANT_ACTIVE": "1"}, []string{"get", "/test"}, 1, "", false},
		{"provider echo redacted", map[string]string{"PVE_TEST_HTTP_FAIL": "1"}, []string{"get", "/test"}, 1, "dev-env@pve!grant-fixture=fixture-grant-secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, request, grant := pveFixture(t, tc.vars, tc.args...)
			if code != tc.code {
				t.Fatalf("helper exit %d, want %d", code, tc.code)
			}
			if tc.header == "" {
				if request != "" {
					t.Fatal("refused invocation called provider")
				}
			} else if !strings.HasPrefix(request, "Authorization: PVEAPIToken="+tc.header+"\n") {
				t.Fatal("helper selected wrong credential")
			}
			if (grant != "") != tc.grant {
				t.Fatal("helper selected wrong grant route")
			}
			if strings.Count(request, "Authorization:") > 1 {
				t.Fatal("child exit retried a provider operation")
			}
		})
	}
}

func TestPVEFlagsCRUDAndEndpoints(t *testing.T) {
	for _, method := range []string{"post", "put", "delete"} {
		code, _, request, _ := pveFixture(t, map[string]string{"PVE_API_URL": "https://pve.example.invalid"}, method, "/test", "value=fixture", "--yes", "--raw")
		if code != 0 || !strings.Contains(request, "https://pve.example.invalid/api2/json/test\n") || !strings.Contains(request, "\n"+strings.ToUpper(method)+"\n") || !strings.Contains(request, "\nvalue=fixture\n") {
			t.Fatal("CRUD request or trailing flags changed")
		}
		if lines := strings.SplitN(request, "\n", 2); len(lines) != 2 || strings.Contains(lines[1], "fixture-grant-secret") {
			t.Fatal("credential appeared in curl argv")
		}
	}
	code, _, request, _ := pveFixture(t, nil, "get", "/test", "--node", "fixture", "--raw")
	if code != 0 || !strings.Contains(request, "https://fixture.haynesnetwork:8006/api2/json/test\n") {
		t.Fatal("trailing node flag changed")
	}
	code, _, request, _ = pveFixture(t, nil, "get", "/test")
	if code != 0 || !strings.Contains(request, "https://pvedash.haynesnetwork/api2/json/test\n") {
		t.Fatal("default endpoint changed")
	}
	code, _, _, _ = pveFixture(t, nil, "vm", "999", "config", "--any", "--raw")
	if code != 0 {
		t.Fatal("trailing any flag changed")
	}
	if !slices.Contains(strings.Split(request, "\n"), "@-") {
		t.Fatal("curl authorization did not use private stdin")
	}
}
