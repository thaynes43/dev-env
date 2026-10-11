package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
)

func TestProjectRefreshDisabledReadsNoSettingsOrCredentials(t *testing.T) {
	var out, stderr bytes.Buffer
	code := projectRefresh(context.Background(), nil, &out, &stderr, func(key string) string {
		if key != "AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP" {
			t.Fatalf("disabled refresh read %s", key)
		}
		return ""
	}, noRunner{})
	if code != exitFailure || out.Len() != 0 || !strings.Contains(stderr.String(), "disabled") {
		t.Fatal("disabled refresh performed work")
	}
}

func TestPrivateProjectCatalogSourceRejectsRootsWithoutBootstrap(t *testing.T) {
	if _, err := privateProjectCatalogSource(agentd.Settings{PrivateProjectRoots: true}, func(key string) string {
		t.Fatalf("invalid configuration read identity %s", key)
		return ""
	}); err == nil {
		t.Fatal("roots without trusted bootstrap accepted")
	}
}

func TestProjectRefreshRefusesCallerAuthorityAndSharedSettings(t *testing.T) {
	for _, mode := range []string{"caller file", "missing binding", "wrong binding", "missing owner", "shared workspace"} {
		t.Run(mode, func(t *testing.T) {
			env := map[string]string{"HOME": t.TempDir(), "AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP": "true", "AGENTD_PROJECT_CATALOG": "dev-env-system/dev-env-project-catalog", "AGENTD_PROJECT_CLONE_OWNER": "fixture"}
			args := []string{"project-refresh"}
			switch mode {
			case "caller file":
				args = append(args, "--catalog-file", "caller.json")
			case "missing binding":
				delete(env, "AGENTD_PROJECT_CATALOG")
			case "wrong binding":
				env["AGENTD_PROJECT_CATALOG"] = "caller/catalog"
			case "missing owner":
				delete(env, "AGENTD_PROJECT_CLONE_OWNER")
			case "shared workspace":
				env["AGENTD_WORKSPACE_ID"] = "legacy"
			}
			code, _, _ := runArgs(args, env)
			if code == exitOK {
				t.Fatal("refresh admitted caller authority")
			}
			if _, err := os.Stat(filepath.Join(env["HOME"], ".agentd")); !os.IsNotExist(err) {
				t.Fatal("invalid settings mutated private state")
			}
		})
	}
}

func TestProjectRefreshRetainsUnconfirmedAPIFailure(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "AGENTD_ENABLE_PRIVATE_PROJECT_BOOTSTRAP": "true", "AGENTD_PROJECT_CATALOG": "dev-env-system/dev-env-project-catalog", "AGENTD_PROJECT_CLONE_OWNER": "fixture", "KUBERNETES_SERVICE_HOST": "secret-canary.invalid", "KUBERNETES_SERVICE_PORT": "443"}
	code, out, stderr := runArgs([]string{"project-refresh"}, env)
	var report agentd.PrivateProjectReport
	if code != exitFailure || json.Unmarshal([]byte(out), &report) != nil || report.State != "Failed" || report.Failure == "" || report.FinishedAt.IsZero() {
		t.Fatalf("unconfirmed API outcome: %d %s %s", code, out, stderr)
	}
	if strings.Contains(out+stderr, "secret-canary") {
		t.Fatal("API identity leaked")
	}
	data, err := os.ReadFile(filepath.Join(home, ".agentd", "project-bootstrap.json"))
	if err != nil || json.Unmarshal(data, &report) != nil || report.State != "Failed" {
		t.Fatal("failed bootstrap receipt not retained")
	}
}
