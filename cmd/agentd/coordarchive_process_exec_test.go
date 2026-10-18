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
	"time"

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

// TestCoordArchiveProcessExecRunsTheRealCommand is the in-process exec of
// DESIGN-024's shelf transport. Put, lookup, and get-item go through the
// shelf coordination-archive client. The executor runs agentd's real command
// entry, and the object read back matches the bytes that were stored.
func TestCoordArchiveProcessExecRunsTheRealCommand(t *testing.T) {
	restore := agentd.SetCoordArchiveMountCheck(func(string, string) error { return nil })
	t.Cleanup(restore)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, protocol.CoordArchiveDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := &processCoordExec{env: map[string]string{"HOME": home}}
	archive := &shelfpkg.CoordinationArchive{Shelf: &shelfpkg.Shelf{
		Reader:    shelfPodClient(t),
		Exec:      exec,
		Namespace: "dev-agents",
		Timeout:   time.Second,
	}}
	manifest, object, _ := coordCLIData(t)
	ctx := context.Background()
	put, err := archive.Put(ctx, protocol.CoordArchivePutRequest{Manifest: manifest, Object: object}, false)
	if err != nil || put.GraceStart != nil || put.Manifest.Object != manifest.Object || put.Manifest.ArchivedAt.IsZero() || put.Manifest.ArchivedAt.Location() != time.UTC {
		t.Fatalf("put = %#v, %v", put, err)
	}
	lookup, err := archive.Lookup(ctx, []protocol.CoordArchiveIdentity{{
		Resource: manifest.Resource, Project: manifest.Project, Name: manifest.Name, UID: manifest.UID,
	}})
	if err != nil || len(lookup) != 1 || !lookup[0].Holds || lookup[0].GraceStart != nil || len(lookup[0].Revisions) != 1 || lookup[0].Revisions[0].SHA256 != manifest.Object.SHA256 {
		t.Fatalf("lookup = %#v, %v", lookup, err)
	}
	got, err := archive.GetItem(ctx, protocol.CoordArchiveGetQuery{Resource: manifest.Resource, Name: manifest.Name, UID: manifest.UID})
	if err != nil || !bytes.Equal(got.Object, object) || got.Manifest.Object != manifest.Object {
		t.Fatalf("get-item = %#v, %v", got, err)
	}
	want := []string{
		"agentd ctl coord-archive put",
		"agentd ctl coord-archive lookup",
		"agentd ctl coord-archive get-item --resource " + manifest.Resource + " --name " + manifest.Name + " --uid " + manifest.UID,
	}
	if strings.Join(exec.cmds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands =\n%s\nwant\n%s", strings.Join(exec.cmds, "\n"), strings.Join(want, "\n"))
	}
}

func TestCoordArchiveProcessExecHoldsAndClearsRetentionGrace(t *testing.T) {
	restore := agentd.SetCoordArchiveMountCheck(func(string, string) error { return nil })
	t.Cleanup(restore)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, protocol.CoordArchiveDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := &processCoordExec{env: map[string]string{"HOME": home}}
	archive := &shelfpkg.CoordinationArchive{Shelf: &shelfpkg.Shelf{Reader: shelfPodClient(t), Exec: exec, Namespace: "dev-agents", Timeout: time.Second}}
	manifest, object, _ := coordCLIData(t)
	id := protocol.CoordArchiveIdentity{Resource: manifest.Resource, Project: manifest.Project, Name: manifest.Name, UID: manifest.UID}
	start := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	event := protocol.CoordArchiveEvent{Format: protocol.CoordArchiveEventFormat, State: "absent", At: start, Rule: "owner-absent", By: "retention fixture", Sessions: []protocol.CoordArchiveObservation{{Role: "owner", Namespace: "dev-agents", Name: "owner", UID: "44444444-4444-4444-8444-444444444444", Observed: "not-found"}}}
	ctx := context.Background()
	if _, err := archive.Put(ctx, protocol.CoordArchivePutRequest{Manifest: manifest, Object: object, Event: &event}, true); err != nil {
		t.Fatal(err)
	}
	event.State, event.At = "hold", start.Add(time.Hour)
	event.Sessions[0].Observed = "present"
	marked, err := archive.Mark(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: event}})
	if err != nil || len(marked) != 1 || marked[0].GraceStart == nil || !marked[0].GraceStart.Equal(start) {
		t.Fatalf("hold must preserve the old grace until present is durable: %#v, %v", marked, err)
	}
	looked, err := archive.Lookup(ctx, []protocol.CoordArchiveIdentity{id})
	if err != nil || len(looked) != 1 || looked[0].LatestState != "hold" || looked[0].LatestAt == nil || !looked[0].LatestAt.Equal(event.At) {
		t.Fatalf("hold must survive the shelf JSON round trip: %#v, %v", looked, err)
	}
	event.State, event.At = "present", start.Add(2*time.Hour)
	marked, err = archive.Mark(ctx, []protocol.CoordArchiveMarkRequest{{CoordArchiveIdentity: id, Event: event}})
	if err != nil || len(marked) != 1 || marked[0].GraceStart != nil {
		t.Fatalf("present must clear grace: %#v, %v", marked, err)
	}
	event.State, event.At = "absent", start.Add(3*time.Hour)
	event.Sessions[0].Observed = "not-found"
	put, err := archive.Put(ctx, protocol.CoordArchivePutRequest{Manifest: manifest, Object: object, Event: &event}, true)
	if err != nil || put.GraceStart == nil || !put.GraceStart.Equal(event.At) {
		t.Fatalf("new absence must start a fresh grace: %#v, %v", put, err)
	}
}

func shelfPodClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dev-env-shelf", Namespace: "dev-agents",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)),
			Labels:            map[string]string{"app.kubernetes.io/name": shelfpkg.AppName},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
}

// processCoordExec is the pod exec fake. It runs the agentd command the shelf
// client built, in this process, against the test's archive directory.
type processCoordExec struct {
	env  map[string]string
	cmds []string
}

func (e *processCoordExec) Run(ctx context.Context, namespace, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if namespace != "dev-agents" || pod != "dev-env-shelf" || container != shelfpkg.Container {
		return fmt.Errorf("exec target %s/%s container %s", namespace, pod, container)
	}
	if len(cmd) < 3 || cmd[0] != "agentd" || cmd[1] != "ctl" || cmd[2] != "coord-archive" {
		return fmt.Errorf("command %q", cmd)
	}
	e.cmds = append(e.cmds, strings.Join(cmd, " "))
	code := run(ctx, cmd[1:], stdin, stdout, stderr, func(key string) string { return e.env[key] }, noRunner{})
	if code == exitOK {
		return nil
	}
	return utilexec.CodeExitError{Err: fmt.Errorf("agentd exited %d", code), Code: code}
}
