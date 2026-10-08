package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type credentialRunner struct {
	last agentd.Cmd
	code int
}

func (r *credentialRunner) LookPath(name string) (string, error) { return name, nil }
func (r *credentialRunner) Run(_ context.Context, cmd agentd.Cmd) (agentd.Result, error) {
	r.last = cmd
	res := agentd.Result{ExitCode: r.code, Stdout: []byte("provider echoed fixture-private-value"), Stderr: []byte("fixture-private-value")}
	if r.code != 0 {
		return res, &agentd.CmdError{Name: cmd.Name, ExitCode: r.code}
	}
	return res, nil
}

func TestCredentialCtlCommands(t *testing.T) {
	env := map[string]string{protocol.GrantsDirEnv: t.TempDir(), protocol.PodUIDEnv: "pod-one"}
	p := protocol.CredentialPayload{Version: 1, Credential: "proxmox", TokenID: "dev-env@pve!grant-uid-one", TokenSecret: "fixture-private-value"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	runner := &credentialRunner{}
	call := func(args []string, stdin io.Reader) (int, string) {
		t.Helper()
		var out bytes.Buffer
		code := run(context.Background(), append([]string{"ctl"}, args...), stdin, &out, &out, func(k string) string { return env[k] }, runner)
		if strings.Contains(out.String(), p.TokenSecret) {
			t.Fatal("credential ctl disclosed private material")
		}
		return code, out.String()
	}
	install := protocol.CredentialInstallCommand("grant-pve", "uid-one", "pod-one", time.Now().Add(time.Hour))[2:]
	if code, _ := call(protocol.CredentialInstallCommand("grant-pve", "uid-one", "pod-other", time.Now().Add(time.Hour))[2:], refuseRead{}); code != exitFailure {
		t.Fatal("wrong pod read private stdin")
	}
	if code, _ := call(install, bytes.NewReader(data)); code != exitOK {
		t.Fatalf("install exit %d", code)
	}
	if code, out := call([]string{"credential-list", "-o", "json"}, refuseRead{}); code != exitOK || !strings.Contains(out, `"grantUID":"uid-one"`) || strings.Contains(out, p.TokenID) {
		t.Fatal("public metadata list failed")
	}
	if code, out := call([]string{"credential-available", "--credential", "proxmox"}, refuseRead{}); code != exitOK || out != "" || runner.last.Name != "" {
		t.Fatal("availability preflight executed a command or exposed material")
	}
	if code, _ := call(protocol.CredentialRemoveCommand("grant-pve", "stale-uid", "pod-one")[2:], refuseRead{}); code != exitOK {
		t.Fatal("stale cleanup should be a safe no-op")
	}
	if code, out := call([]string{"credential-use", "--credential", "proxmox", "--", "pve", "delete", "/test", "--yes"}, strings.NewReader("input")); code != exitOK || !strings.Contains(out, "[redacted]") {
		t.Fatal("credential child command failed")
	}
	if runner.last.Name != "pve" || !slices.Equal(runner.last.Args, []string{"delete", "/test", "--yes"}) || !slices.Contains(runner.last.Env, "PVE_OPERATOR_TOKEN_SECRET="+p.TokenSecret) || !slices.Contains(runner.last.Env, "PVE_GRANT_ACTIVE=1") {
		t.Fatal("child credential or arguments missing")
	}
	runner.code = 7
	if code, _ := call([]string{"credential-use", "--credential", "proxmox", "--", "pve", "get", "/test"}, refuseRead{}); code != 7 {
		t.Fatal("credential-use swallowed child exit status")
	}
	runner.code = protocol.ExitNoCredential
	if code, _ := call([]string{"credential-use", "--credential", "proxmox", "--", "pve", "get", "/test"}, refuseRead{}); code != protocol.ExitNoCredential {
		t.Fatal("credential-use swallowed child exit 4")
	}
	if code, _ := call(protocol.CredentialRemoveCommand("grant-pve", "uid-one", "pod-other")[2:], refuseRead{}); code != exitFailure {
		t.Fatal("remove accepted wrong pod")
	}
	if code, _ := call(protocol.CredentialRemoveCommand("grant-pve", "uid-one", "pod-one")[2:], refuseRead{}); code != exitOK {
		t.Fatal("remove failed")
	}
	if code, out := call([]string{"credential-use", "--credential", "proxmox", "--", "pve", "get", "/test"}, refuseRead{}); code != protocol.ExitNoCredential || out != "" {
		t.Fatal("missing credential must be a silent distinct exit")
	}
	if code, out := call([]string{"credential-available", "--credential", "proxmox"}, refuseRead{}); code != protocol.ExitNoCredential || out != "" {
		t.Fatal("availability fallback must be a silent distinct exit")
	}
	env[protocol.GrantsDirEnv] = env[protocol.GrantsDirEnv] + "/missing"
	if code, _ := call(install, bytes.NewReader(data)); code != protocol.ExitNoGrantsDir {
		t.Fatal("missing grants volume lost compatibility exit")
	}
}
