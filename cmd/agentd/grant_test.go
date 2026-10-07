package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type refuseRead struct{}

func (refuseRead) Read([]byte) (int, error) { panic("token read before the pod UID was checked") }

func TestGrantCtlCommands(t *testing.T) {
	env := map[string]string{
		protocol.GrantsDirEnv: t.TempDir(), protocol.PodUIDEnv: "pod-one", protocol.PodNamespaceEnv: "dev-agents",
		"KUBERNETES_SERVICE_HOST": "127.0.0.1", "KUBERNETES_SERVICE_PORT": "443",
	}
	call := func(args []string, stdin io.Reader) (int, string) {
		t.Helper()
		var out bytes.Buffer
		code := run(context.Background(), append([]string{"ctl"}, args...), stdin, &out, &out, func(k string) string { return env[k] }, noRunner{})
		if strings.Contains(out.String(), "fixture-token") {
			t.Fatal("ctl output exposed a token")
		}
		return code, out.String()
	}
	cmd := protocol.GrantInstallCommand("grant-test", "dev-env-grant-workloads", []string{"frontend"}, time.Now().Add(time.Hour), "pod-one")[2:]
	if code, _ := call(cmd, strings.NewReader("fixture-token\n")); code != exitOK {
		t.Fatalf("install exit %d", code)
	}
	if code, out := call([]string{"grant-list", "-o", "json"}, refuseRead{}); code != exitOK || !strings.Contains(out, `"name": "grant-test"`) {
		t.Fatalf("list exit %d", code)
	}
	if code, _ := call([]string{"grant-use", "grant-test"}, refuseRead{}); code != exitOK {
		t.Fatalf("use exit %d", code)
	}
	if code, _ := call(protocol.GrantRemoveCommand("grant-test", "wrong-pod")[2:], refuseRead{}); code != exitFailure {
		t.Fatal("remove accepted a replacement pod UID")
	}
	if code, _ := call(protocol.GrantRemoveCommand("grant-test", "pod-one")[2:], refuseRead{}); code != exitOK {
		t.Fatalf("remove exit %d", code)
	}
	if code, out := call([]string{"grant-list", "-o", "json"}, refuseRead{}); code != exitOK || strings.TrimSpace(out) != "[]" {
		t.Fatal("last removal left an installed grant")
	}
	wrong := protocol.GrantInstallCommand("grant-test", "dev-env-grant-workloads", []string{"frontend"}, time.Now().Add(time.Hour), "wrong-pod")[2:]
	if code, _ := call(wrong, refuseRead{}); code != exitFailure {
		t.Fatal("install accepted a replacement pod UID")
	}
	env[protocol.GrantsDirEnv] = filepath.Join(t.TempDir(), "missing")
	if code, _ := call(cmd, strings.NewReader("fixture-token")); code != protocol.ExitNoGrantsDir {
		t.Fatalf("legacy pod exit %d, want %d", code, protocol.ExitNoGrantsDir)
	}
}
