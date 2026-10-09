package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestProjectSyncDefaultOffReadsNoSettingsOrCredentials(t *testing.T) {
	for _, args := range [][]string{{"project-sync"}, {"project-sync", "--initialize-new-workspace"}} {
		var out, errout bytes.Buffer
		code := run(context.Background(), args, strings.NewReader(""), &out, &errout, func(string) string { t.Fatal("disabled command read identity or credentials"); return "" }, noRunner{})
		if code != exitFailure || out.Len() != 0 || !strings.Contains(errout.String(), "disabled") {
			t.Fatalf("default-off command changed state: %d %s", code, errout.String())
		}
	}
}

func TestProjectSyncRefusesTaskAndImplicitCredentialRoutes(t *testing.T) {
	for _, mode := range []string{"missing projection", "session", "namespace"} {
		t.Run(mode, func(t *testing.T) {
			env := map[string]string{"DEV_ENV_POD_NAMESPACE": "dev-agents", "AGENTD_SESSION": "{\"name\":\"model-task\"}"}
			args := []string{"project-sync", "--enabled", "--accepted-catalog", "dev-env-system/dev-env-project-catalog", "--namespace", "dev-agents", "--workspace-id", "workspace-1", "--project-clone-owner", "thaynes43", "--github-token-file", "/creds/gh_token", "--kube-token-file", "/var/run/secrets/project-sync/token", "--kube-ca-file", "/var/run/secrets/project-sync/ca.crt"}
			switch mode {
			case "missing projection":
				args = args[:len(args)-2]
				delete(env, "AGENTD_SESSION")
			case "namespace":
				env["DEV_ENV_POD_NAMESPACE"] = "other"
				delete(env, "AGENTD_SESSION")
			}
			var out, errout bytes.Buffer
			code := run(context.Background(), args, strings.NewReader(""), &out, &errout, func(k string) string { return env[k] }, noRunner{})
			if code != exitUsage || out.Len() != 0 {
				t.Fatalf("unsafe command reached runtime: %d %s", code, errout.String())
			}
		})
	}
}
