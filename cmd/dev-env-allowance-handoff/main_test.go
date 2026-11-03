package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRefusalNeverConstructsClientOrEchoesInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "snapshot")
	if err := os.WriteFile(input, []byte(`{"secret-canary":"do-not-echo"}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	factory := func(string, string) (client.Client, error) { calls++; t.Fatal("unexpected client"); return nil, nil }
	base := []string{"--input", input, "--kubeconfig", "/fixture/kubeconfig", "--context", "fixture", "--namespace", "dev-env-system", "--slot", "dev-env-provider-allowances"}
	for _, mode := range []string{"prepare", "apply"} {
		args := append([]string{mode}, base...)
		if mode == "apply" {
			args = append(args, "--approved-hash", strings.Repeat("a", 64), "--backup-dir", dir)
		}
		var out bytes.Buffer
		if err := run(args, &out, time.Now, factory); err == nil || out.Len() != 0 || strings.Contains(err.Error(), "canary") {
			t.Fatal("refusal leaked input or accepted malformed snapshot")
		}
	}
	if calls != 0 {
		t.Fatal("client constructed")
	}
}
func TestPrivateSnapshotPermissionsAndSymlink(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "input")
	if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(p, 100); err == nil {
		t.Fatal("world-readable input accepted")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(p, 100); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(link, 100); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestKubeconfigRefusesExecProxyAndInsecureTransport(t *testing.T) {
	for name, extra := range map[string]string{
		"exec":     "    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: fixture\n      interactiveMode: Never\n",
		"proxy":    "    proxy-url: http://127.0.0.1:8123\n",
		"insecure": "    insecure-skip-tls-verify: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			clusterExtra, authExtra := "", ""
			if name == "exec" {
				authExtra = extra
			} else {
				clusterExtra = extra
			}
			raw := "apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: https://127.0.0.1:6443\n" + clusterExtra + "users:\n- name: fixture\n  user:\n" + authExtra + "    token: synthetic\ncontexts:\n- name: fixture\n  context:\n    cluster: fixture\n    user: fixture\ncurrent-context: fixture\n"
			p := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := makeClient(p, "fixture"); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}
