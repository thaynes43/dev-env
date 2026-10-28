package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
)

func TestCodexHostDisabledAndSessionRefusal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { panic("disabled read environment") }
	if run(context.Background(), []string{"codex-host", "run"}, nil, &stdout, &stderr, getenv, agentd.ExecRunner{}) != exitOK || stdout.String() != "{\"code\":\"Disabled\",\"idle\":false}\n" {
		t.Fatal("disabled route")
	}
	stdout.Reset()
	getenv = func(key string) string {
		if key == "AGENTD_SESSION" {
			return "{}"
		}
		return ""
	}
	if run(context.Background(), []string{"codex-host", "--enabled", "run"}, nil, &stdout, &stderr, getenv, agentd.ExecRunner{}) != exitUsage {
		t.Fatal("Session accepted as host")
	}
}

func TestCodexHostStatusFailureWritesOneUnknownDocument(t *testing.T) {
	// Missing host identity refuses in preflight before any native invocation.
	code, out, errOut := runArgs([]string{"codex-host", "--enabled", "status"}, map[string]string{"HOME": t.TempDir()})
	if code != exitFailure || errOut != "" {
		t.Fatalf("status failure: code=%d stderr=%q", code, errOut)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(out))
	var status agentd.CodexHostStatus
	if decoder.Decode(&status) != nil || status != (agentd.CodexHostStatus{Code: "Unknown"}) {
		t.Fatal("status failure did not publish fixed Unknown metadata")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("status failure published more than one JSON document")
	}
}

func TestPrivateCodexHostRejectsContradictoryConfigurationBeforeAuthority(t *testing.T) {
	for _, mode := range []string{"bootstrap disabled", "roots disabled", "shared workspace", "shared path", "catalog file", "wrong catalog", "foreign provider home"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			env := map[string]string{"HOME": home, "AGENTD_ENABLE_PRIVATE_CODEX_HOST": "true", "AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP": "true", "AGENTD_ENABLE_PRIVATE_PROJECT_ROOTS": "true", "AGENTD_PROJECT_CATALOG": "dev-env-system/dev-env-project-catalog", "AGENTD_PROJECT_CLONE_OWNER": "fixture", "DEV_ENV_POD_UID": "pod-1"}
			args := []string{"codex-host", "--enabled", "run"}
			switch mode {
			case "bootstrap disabled":
				env["AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP"] = "false"
			case "roots disabled":
				env["AGENTD_ENABLE_PRIVATE_PROJECT_ROOTS"] = "false"
			case "shared workspace":
				env["AGENTD_WORKSPACE_ID"] = "shared"
			case "shared path":
				env["AGENTD_SHARED_DIR"] = "/work/shared"
			case "catalog file":
				args = []string{"codex-host", "--enabled", "--catalog-file", "/caller/catalog.json", "run"}
			case "wrong catalog":
				env["AGENTD_PROJECT_CATALOG"] = "caller/catalog"
			case "foreign provider home":
				env["CODEX_HOME"] = "/foreign/provider"
			}
			getenv := func(key string) string {
				if key == "KUBERNETES_SERVICE_HOST" || key == "KUBERNETES_SERVICE_PORT" {
					t.Fatal("invalid config consulted API identity")
				}
				return env[key]
			}
			var stdout, stderr bytes.Buffer
			if run(context.Background(), args, nil, &stdout, &stderr, getenv, agentd.ExecRunner{}) != exitUsage {
				t.Fatal("invalid private configuration accepted")
			}
		})
	}
}

func TestAllowanceReadPreservesHostGuard(t *testing.T) {
	for _, key := range []string{"AGENTD_SESSION", "AGENTD_SESSION_FILE"} {
		var out, errOut bytes.Buffer
		getenv := func(k string) string {
			if k == key {
				return "fixture"
			}
			return ""
		}
		if run(context.Background(), []string{"codex-host", "--enabled", "allowance-read"}, nil, &out, &errOut, getenv, agentd.ExecRunner{}) != exitUsage {
			t.Fatal("session accepted", key)
		}
	}
	code, out, _ := runArgs([]string{"codex-host", "allowance-read"}, nil)
	if code != exitOK || out != "{\"code\":\"Disabled\",\"idle\":false}\n" {
		t.Fatal("disabled allowance read")
	}
}
