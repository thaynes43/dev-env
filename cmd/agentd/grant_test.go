package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type refuseRead struct{}

func (refuseRead) Read([]byte) (int, error) { panic("token read before the pod UID was checked") }

func TestGrantCtlCommands(t *testing.T) {
	projectionDir := t.TempDir()
	ownTokenFile := filepath.Join(projectionDir, "token")
	ownCAFile := filepath.Join(projectionDir, "ca.crt")
	for path, contents := range map[string]string{ownTokenFile: "synthetic-own-token\n", ownCAFile: "synthetic-own-ca\n"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{
		protocol.GrantsDirEnv: t.TempDir(), protocol.PodUIDEnv: "pod-one", protocol.PodNamespaceEnv: "dev-agents",
		"KUBERNETES_SERVICE_HOST": "127.0.0.1", "KUBERNETES_SERVICE_PORT": "443",
	}
	store := agentd.Grants{Dir: env[protocol.GrantsDirEnv], Server: "https://127.0.0.1:443", Namespace: "dev-agents", OwnTokenFile: ownTokenFile, OwnCAFile: ownCAFile}
	call := func(args []string, stdin io.Reader) (int, string) {
		t.Helper()
		var out bytes.Buffer
		code := grantCtlWithGrants(store, args, stdin, &out, &out, func(k string) string { return env[k] })
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
	kubeconfig, err := clientcmd.LoadFromFile(store.KubeconfigPath())
	if err != nil {
		t.Fatal(err)
	}
	defaultContext, ok := kubeconfig.Contexts[protocol.DefaultContext]
	if !ok || kubeconfig.CurrentContext != protocol.DefaultContext || len(kubeconfig.Contexts) != 1 || defaultContext.Namespace != "dev-agents" {
		t.Fatal("last removal did not retain only the own-ServiceAccount default context")
	}
	if _, ok := kubeconfig.Contexts["grant-test"]; ok {
		t.Fatal("last removal retained the selected grant context")
	}
	if kubeconfig.AuthInfos[defaultContext.AuthInfo].TokenFile != ownTokenFile || kubeconfig.Clusters[defaultContext.Cluster].CertificateAuthority != ownCAFile {
		t.Fatal("retained default context did not reference the synthetic own-ServiceAccount projection")
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "grant-test", "token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("last removal retained the selected grant token")
	}
	wrong := protocol.GrantInstallCommand("grant-test", "dev-env-grant-workloads", []string{"frontend"}, time.Now().Add(time.Hour), "wrong-pod")[2:]
	if code, _ := call(wrong, refuseRead{}); code != exitFailure {
		t.Fatal("install accepted a replacement pod UID")
	}
	missingStore := store
	missingStore.Dir = filepath.Join(t.TempDir(), "missing")
	var missingOut bytes.Buffer
	if code := grantCtlWithGrants(missingStore, cmd, strings.NewReader("fixture-token"), &missingOut, &missingOut, func(k string) string { return env[k] }); code != protocol.ExitNoGrantsDir {
		t.Fatalf("legacy pod exit %d, want %d", code, protocol.ExitNoGrantsDir)
	}
}
