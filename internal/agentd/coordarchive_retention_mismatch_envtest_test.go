package agentd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/coordretention"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/taskclaim"
	"github.com/thaynes43/dev-env/internal/testenv"
)

// TestEnvtestArchiveMismatchLeavesTheRecord is DESIGN-024 E2 for altered
// archive bytes. After the mark, one byte of the stored object changes. Once
// the grace is over, the verify read refuses. The claim stays, and the
// altered bytes are not overwritten. The other E2 refusals stay on the fake
// client.
func TestEnvtestArchiveMismatchLeavesTheRecord(t *testing.T) {
	old := coordArchiveIsMounted
	coordArchiveIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { coordArchiveIsMounted = old })

	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
		}
	})
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{coord.AddToScheme, devv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const ns = "dev-env-system"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	spec := retentionClaimSpec()
	if !taskclaim.ValidSpec(spec) {
		t.Fatal("claim spec")
	}
	claim := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: taskclaim.Name(spec.Identity)}, Spec: spec}
	if err := admin.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	var order []string
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace, Client: &recordingClient{Client: admin, order: &order}, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Owner.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.TaskClaim: true},
		Pods:  shelfPod{}, Archive: &fsArchive{a: archive, order: &order}, Slots: shelf.NewExecSlots(2),
		Now: func() time.Time { return clock },
	}
	pass := func(at time.Time) string {
		t.Helper()
		clock = at
		return runnable.Pass(ctx)
	}
	if got := pass(start); got != "complete" || len(order) != 0 {
		t.Fatalf("mark pass %s order %v", got, order)
	}
	id := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: spec.Identity.Project, Name: claim.Name, UID: string(claim.UID)}
	altered := alterArchiveObject(t, root, id)
	if got := pass(start.Add(coordretention.DefaultGrace - time.Minute)); got != "complete" || len(order) != 0 {
		t.Fatalf("inside grace %s order %v", got, order)
	}
	if got := pass(start.Add(coordretention.DefaultGrace)); got != "complete" || len(order) != 1 || order[0] != "get:taskclaims" {
		t.Fatalf("mismatch pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	if got := readArchiveObject(t, root, id); !bytes.Equal(got, altered) {
		t.Fatal("altered archive bytes were overwritten")
	}
}

func alterArchiveObject(t *testing.T, root string, id protocol.CoordArchiveIdentity) []byte {
	t.Helper()
	path := archiveObjectPath(t, root, id)
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 2 {
		t.Fatalf("stored object: %v len %d", err, len(raw))
	}
	raw[len(raw)-2] ^= 0x01
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return raw
}

func readArchiveObject(t *testing.T, root string, id protocol.CoordArchiveIdentity) []byte {
	t.Helper()
	raw, err := os.ReadFile(archiveObjectPath(t, root, id))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func archiveObjectPath(t *testing.T, root string, id protocol.CoordArchiveIdentity) string {
	t.Helper()
	rel, err := id.RecordDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "events" {
			continue
		}
		if found != "" {
			t.Fatal("more than one revision directory")
		}
		found = filepath.Join(dir, entry.Name(), "object.json")
	}
	if found == "" {
		t.Fatal("no stored object")
	}
	return found
}
