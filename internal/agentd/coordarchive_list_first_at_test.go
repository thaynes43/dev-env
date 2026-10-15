package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/shelf"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type coordArchiveListExecutor struct {
	archive CoordArchive
	calls   int
}

func (e *coordArchiveListExecutor) Run(_ context.Context, namespace, pod, container string, cmd []string, _ io.Reader, stdout, _ io.Writer) error {
	e.calls++
	if namespace != "dev-agents" || pod != "shelf" || container != shelf.Container || len(cmd) != 10 || cmd[0] != "agentd" || cmd[1] != "ctl" || cmd[2] != "coord-archive" || cmd[3] != "list" || cmd[4] != "--resource" || cmd[5] != "publishedcontexts" || cmd[6] != "--project" || cmd[7] != "dev-env" || cmd[8] != "--limit" || cmd[9] != "1" {
		return errors.New("unexpected typed list command")
	}
	page, err := e.archive.List(protocol.CoordArchiveListQuery{Resource: "publishedcontexts", Project: "dev-env", Limit: 1})
	if err != nil {
		return err
	}
	body, err := json.Marshal(page)
	if err != nil {
		return err
	}
	_, err = stdout.Write(body)
	return err
}

func TestCoordArchiveListReturnsUIDFirstArchiveTimeForLatestRevision(t *testing.T) {
	a := coordArchiveRig(t)
	m, raw := coordStoredFixture(t, "publishedcontexts")
	firstAt := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return firstAt }
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	m.ResourceVersion = "43"
	object["metadata"].(map[string]any)["resourceVersion"] = m.ResourceVersion
	raw, _ = protocol.CanonicalJSON(object)
	bindCoordStoredFixture(t, &m, raw)
	a.Now = func() time.Time { return firstAt.Add(time.Hour) }
	if _, err := a.Put(m, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	page, err := a.List(protocol.CoordArchiveListQuery{Resource: m.Resource, Project: m.Project, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Manifest == nil || page.Items[0].Manifest.ResourceVersion != "43" || !page.Items[0].Manifest.ArchivedAt.Equal(firstAt) {
		t.Fatalf("latest revision/first archive receipt = %#v, %v", page, err)
	}

	// COMPOSED evidence part 1: the real temporary archive verifies stored
	// bytes, strips text, and reaches the typed shelf List client in-process.
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "shelf", Labels: map[string]string{"app.kubernetes.io/name": shelf.AppName}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}).Build()
	exec := &coordArchiveListExecutor{archive: a}
	typed := &shelf.CoordinationArchive{Shelf: &shelf.Shelf{Reader: reader, Exec: exec, Namespace: "dev-agents"}}
	page, err = typed.List(context.Background(), protocol.CoordArchiveListQuery{Resource: "publishedcontexts", Project: "dev-env", Limit: 1})
	if err != nil || exec.calls != 1 || len(page.Items) != 1 || page.Items[0].Manifest == nil || page.Items[0].Manifest.ResourceVersion != "43" || !page.Items[0].Manifest.ArchivedAt.Equal(firstAt) || bytes.Contains(page.Items[0].Object, []byte("<synthetic context>")) {
		t.Fatalf("real archive typed list = %#v, %v", page, err)
	}
	if err := os.WriteFile(filepath.Join(coordRevisionPath(t, a, m), "object.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err = typed.List(context.Background(), protocol.CoordArchiveListQuery{Resource: "publishedcontexts", Project: "dev-env", Limit: 1})
	if err != nil || exec.calls != 2 || len(page.Items) != 1 || page.Items[0].Error != protocol.HistoryErrCorrupt || page.Items[0].Manifest != nil || page.Items[0].Object != nil {
		t.Fatalf("real corrupt list = %#v, %v", page, err)
	}
}
