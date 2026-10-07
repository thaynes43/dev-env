package agentd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestGrantsLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	g := Grants{Dir: t.TempDir(), Server: "https://[::1]:443", Namespace: "dev-agents", Now: func() time.Time { return now }}
	first := GrantSpec{Name: "grant-first", Role: "dev-env-grant-workloads", Namespaces: []string{"frontend"}, Expires: now.Add(time.Hour)}
	if err := g.Install(first, []byte("fixture-token-first")); err != nil {
		t.Fatal(err)
	}
	load := func() {
		t.Helper()
		kc, err := clientcmd.LoadFromFile(g.KubeconfigPath())
		if err != nil {
			t.Fatal(err)
		}
		if kc.Contexts[protocol.DefaultContext].Namespace != "dev-agents" || kc.AuthInfos[agentUser].TokenFile != ServiceAccountTokenFile ||
			kc.Clusters[clusterName].Server != g.Server || kc.Clusters[clusterName].CertificateAuthority != ServiceAccountCAFile {
			t.Fatal("the kubeconfig does not preserve the pod identity, namespace or API trust")
		}
		if kc.AuthInfos[first.Name].Token != "" || kc.AuthInfos[first.Name].TokenFile != filepath.Join(g.Dir, first.Name, "token") {
			t.Fatal("the kubeconfig must reference the private token file")
		}
	}
	load()
	if g.currentContext() != protocol.DefaultContext {
		t.Fatal("an install must keep the baseline context selected")
	}
	for path, mode := range map[string]os.FileMode{
		filepath.Join(g.Dir, first.Name):               0o700,
		filepath.Join(g.Dir, first.Name, "token"):      0o600,
		filepath.Join(g.Dir, first.Name, "grant.json"): 0o600,
		g.KubeconfigPath():                             0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm() != mode {
			t.Fatalf("private file permission at %s", filepath.Base(path))
		}
	}
	if err := g.Use(first.Name); err != nil {
		t.Fatal(err)
	}
	if err := g.Install(first, []byte("fixture-token-replaced")); err != nil {
		t.Fatal(err)
	}
	if g.currentContext() != first.Name {
		t.Fatal("reinstallation changed the selected context")
	}
	token, err := os.ReadFile(filepath.Join(g.Dir, first.Name, "token"))
	if err != nil || !bytes.Equal(token, []byte("fixture-token-replaced")) {
		t.Fatal("reinstallation did not replace the token")
	}
	second := GrantSpec{Name: "grant-second", Role: "dev-env-grant-nodes", Expires: now.Add(2 * time.Hour)}
	if err := g.Install(second, []byte("fixture-token-second")); err != nil {
		t.Fatal(err)
	}
	kc, err := clientcmd.LoadFromFile(g.KubeconfigPath())
	if err != nil || len(kc.Contexts) != 3 || kc.Contexts[second.Name].Namespace != "dev-agents" || kc.Contexts[first.Name].Namespace != "frontend" {
		t.Fatal("grant scope or baseline contexts were lost")
	}
	list, err := g.List()
	if err != nil || len(list) != 2 || !list[0].Current {
		t.Fatal("list did not report the selected grant")
	}
	if err := g.Remove(first.Name); err != nil {
		t.Fatal(err)
	}
	if g.currentContext() != protocol.DefaultContext {
		t.Fatal("removing the selected grant did not restore the baseline context")
	}
	if err := g.Install(first, []byte("fixture-token-first")); err != nil {
		t.Fatal(err)
	}
	if err := g.Use(first.Name); err != nil {
		t.Fatal(err)
	}
	now = first.Expires
	if err := g.Use(first.Name); err == nil {
		t.Fatal("an expired grant was selected")
	}
	if err := g.Use(protocol.DefaultContext); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(g.Dir, first.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a write did not clean the expired token")
	}
	if err := g.Remove(second.Name); err != nil {
		t.Fatal(err)
	}
	if err := g.Remove(second.Name); err != nil {
		t.Fatal("repeated removal must succeed")
	}
	if _, err := os.Stat(g.KubeconfigPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the last removal must restore in-cluster fallback")
	}
}

func TestGrantsRejectBeforeWriting(t *testing.T) {
	now := time.Now()
	g := Grants{Dir: filepath.Join(t.TempDir(), "absent"), Server: "https://127.0.0.1:443", Namespace: "dev-agents"}
	valid := GrantSpec{Name: "grant-test", Role: "dev-env-grant-workloads", Namespaces: []string{"frontend"}, Expires: now.Add(time.Hour)}
	if err := g.Install(valid, []byte("fixture-token")); !errors.Is(err, ErrNoGrantsDir) {
		t.Fatalf("missing volume: %v", err)
	}
	if _, err := os.Stat(g.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("install created a directory outside the mounted volume")
	}
	g.Dir = t.TempDir()
	for _, bad := range []GrantSpec{
		{Name: "grant-../../escape", Role: valid.Role, Expires: valid.Expires},
		{Name: valid.Name, Role: "bad/role", Expires: valid.Expires},
		{Name: valid.Name, Role: valid.Role, Namespaces: []string{"frontend", "frontend"}, Expires: valid.Expires},
		{Name: valid.Name, Role: valid.Role, Expires: now},
	} {
		if err := g.Install(bad, []byte("fixture-token")); err == nil {
			t.Fatal("an invalid grant was installed")
		}
	}
	if err := g.Remove("../../escape"); err == nil {
		t.Fatal("a traversal removal was accepted")
	}
	entries, err := os.ReadDir(g.Dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid input wrote to the store")
	}
}

func TestGrantsConcurrentInstall(t *testing.T) {
	g := Grants{Dir: t.TempDir(), Server: "https://127.0.0.1:443", Namespace: "dev-agents"}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, name := range []string{"grant-one", "grant-two"} {
		wg.Go(func() {
			errs <- g.Install(GrantSpec{Name: name, Role: "dev-env-grant-nodes", Expires: time.Now().Add(time.Hour)}, []byte("fixture-token"))
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	kc, err := clientcmd.LoadFromFile(g.KubeconfigPath())
	if err != nil || len(kc.Contexts) != 3 {
		t.Fatal("concurrent installs lost a grant")
	}
}

func TestReadGrantToken(t *testing.T) {
	for _, in := range []string{"fixture-token", "fixture-token\n", "fixture-token\r\n"} {
		got, err := ReadToken(strings.NewReader(in))
		if err != nil || string(got) != "fixture-token" {
			t.Fatal("valid stdin token was refused")
		}
	}
	for _, in := range []string{"", "fixture-token\nsecond", "fixture-token with space", strings.Repeat("x", maxTokenBytes+1), strings.Repeat("x", maxTokenBytes) + "\r\njunk", "fixture-token\x00", "fixture-token\r"} {
		_, err := ReadToken(strings.NewReader(in))
		if err == nil || strings.Contains(err.Error(), "fixture-token") {
			t.Fatal("invalid token was accepted or included in the error")
		}
	}
}

func TestGrantsFromEnv(t *testing.T) {
	env := map[string]string{"KUBERNETES_SERVICE_HOST": "2001:db8::1", "KUBERNETES_SERVICE_PORT": "443", protocol.PodNamespaceEnv: "dev-agents"}
	g := GrantsFromEnv(func(k string) string { return env[k] })
	if g.Dir != protocol.GrantsDir || g.Server != "https://[2001:db8::1]:443" || g.Namespace != "dev-agents" {
		t.Fatal("grant environment did not preserve the pod address and namespace")
	}
}

func TestGrantsRefuseSymlinkDirectories(t *testing.T) {
	store, disk := t.TempDir(), t.TempDir()
	g := Grants{Dir: store, Server: "https://127.0.0.1:443", Namespace: "dev-agents"}
	spec := GrantSpec{Name: "grant-test", Role: "dev-env-grant-nodes", Expires: time.Now().Add(time.Hour)}
	if err := os.Symlink(disk, filepath.Join(store, spec.Name)); err != nil {
		t.Fatal(err)
	}
	if err := g.Install(spec, []byte("fixture-token")); err == nil {
		t.Fatal("a grant directory symlink was accepted")
	}
	root := filepath.Join(t.TempDir(), "grants")
	if err := os.Symlink(disk, root); err != nil {
		t.Fatal(err)
	}
	g.Dir = root
	if err := g.Install(spec, []byte("fixture-token")); !errors.Is(err, ErrNoGrantsDir) {
		t.Fatal("a grants root symlink was accepted")
	}
	if entries, err := os.ReadDir(disk); err != nil || len(entries) != 0 {
		t.Fatal("a rejected symlink wrote credentials to disk")
	}
}
