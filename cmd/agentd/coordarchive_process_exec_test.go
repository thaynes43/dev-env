package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	utilexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	shelfpkg "github.com/thaynes43/dev-env/internal/shelf"
)

// TestShelfExecRunsTheRealCoordArchiveCommand runs the shelf client's
// coord-archive argv through the real command entry in this process. The
// stored object comes back. A separate process and a pods/exec stream remain.
// This is not another archive-before-delete proof.
func TestShelfExecRunsTheRealCoordArchiveCommand(t *testing.T) {
	restore := agentd.SetCoordArchiveMountCheck(func(string, string) error { return nil })
	t.Cleanup(restore)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, protocol.CoordArchiveDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	m, canonical, _ := coordCLIData(t)
	exec := &realCtlExec{env: map[string]string{"HOME": home}}
	archive := &shelfpkg.CoordinationArchive{Shelf: &shelfpkg.Shelf{
		Reader: shelfPodClient(t), Exec: exec, Namespace: "dev-agents",
	}}
	ctx := context.Background()
	put, err := archive.Put(ctx, protocol.CoordArchivePutRequest{Manifest: m, Object: canonical}, false)
	if err != nil || put.GraceStart != nil || put.Manifest.Object != m.Object || put.Manifest.Name != m.Name {
		t.Fatalf("put = %#v, %v, commands %q", put, err, exec.cmds)
	}
	got, err := archive.GetItem(ctx, protocol.CoordArchiveGetQuery{Resource: m.Resource, Name: m.Name})
	if err != nil || !bytes.Equal(got.Object, canonical) || got.Manifest.UID != m.UID {
		t.Fatalf("get-item = %#v, %v, commands %q", got.Manifest, err, exec.cmds)
	}
	want := []string{
		"agentd ctl coord-archive put",
		"agentd ctl coord-archive get-item --resource " + m.Resource + " --name " + m.Name,
	}
	if strings.Join(exec.cmds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %q, want %q", exec.cmds, want)
	}
}

func shelfPodClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "shelf-1", Namespace: "dev-agents",
			Labels: map[string]string{"app.kubernetes.io/name": shelfpkg.AppName}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
}

type realCtlExec struct {
	env  map[string]string
	cmds []string
}

func (e *realCtlExec) Run(ctx context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if namespace != "dev-agents" || pod != "shelf-1" || container != shelfpkg.Container {
		return fmt.Errorf("exec target %s/%s %s", namespace, pod, container)
	}
	if len(cmd) < 3 || cmd[0] != "agentd" || cmd[1] != "ctl" || cmd[2] != "coord-archive" {
		return fmt.Errorf("command %q", strings.Join(cmd, " "))
	}
	e.cmds = append(e.cmds, strings.Join(cmd, " "))
	code := run(ctx, cmd[1:], stdin, stdout, stderr, func(k string) string { return e.env[k] }, noRunner{})
	if code == exitOK {
		return nil
	}
	return utilexec.CodeExitError{Err: fmt.Errorf("agentd exit %d", code), Code: code}
}
