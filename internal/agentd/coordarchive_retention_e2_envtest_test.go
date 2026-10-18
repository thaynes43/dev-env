package agentd

import (
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

// TestEnvtestArchiveMissingResetsAndRemarks is DESIGN-024 E2 for a revision
// removed after the mark. The journal stays. The grace pass writes a reset and
// does not delete the claim. The next pass stores the claim again and opens a
// new grace. The other E2 refusals stay on the fake client.
func TestEnvtestArchiveMissingResetsAndRemarks(t *testing.T) {
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
	start := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
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
	marked := lookupOne(t, archive, id)
	if marked.GraceStart == nil || !marked.GraceStart.Equal(start) || !marked.Holds || marked.LatestState != "absent" {
		t.Fatalf("marked archive %#v", marked)
	}
	removeArchiveRevisions(t, root, id)
	missing := lookupOne(t, archive, id)
	if missing.Holds || missing.GraceStart == nil || !missing.GraceStart.Equal(start) || missing.LatestState != "absent" {
		t.Fatalf("revision removed %#v", missing)
	}
	if got := pass(start.Add(coordretention.DefaultGrace - time.Minute)); got != "complete" || len(order) != 0 {
		t.Fatalf("inside grace %s order %v", got, order)
	}
	pending := lookupOne(t, archive, id)
	if pending.Holds || pending.GraceStart == nil || !pending.GraceStart.Equal(start) || pending.LatestState != "absent" {
		t.Fatalf("grace still open %#v", pending)
	}
	due := start.Add(coordretention.DefaultGrace)
	if got := pass(due); got != "complete" || len(order) != 0 {
		t.Fatalf("missing revision %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	reset := lookupOne(t, archive, id)
	if reset.Holds || reset.GraceStart != nil || reset.LatestState != "reset" || reset.LatestAt == nil || !reset.LatestAt.Equal(due) {
		t.Fatalf("reset %#v", reset)
	}
	remarkedAt := due.Add(time.Minute)
	if got := pass(remarkedAt); got != "complete" || len(order) != 0 {
		t.Fatalf("remark %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	again := lookupOne(t, archive, id)
	if again.GraceStart == nil || !again.GraceStart.Equal(remarkedAt) || !again.Holds || again.LatestState != "absent" {
		t.Fatalf("new grace %#v", again)
	}
}

func lookupOne(t *testing.T, archive CoordArchive, id protocol.CoordArchiveIdentity) protocol.CoordArchiveLookupResult {
	t.Helper()
	got, err := archive.Lookup([]protocol.CoordArchiveIdentity{id})
	if err != nil || len(got) != 1 || got[0].CoordArchiveIdentity != id {
		t.Fatalf("lookup %#v %v", got, err)
	}
	return got[0]
}

func removeArchiveRevisions(t *testing.T, root string, id protocol.CoordArchiveIdentity) {
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
	removed := 0
	for _, entry := range entries {
		if entry.Name() == "events" || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			t.Fatal(err)
		}
		removed++
	}
	if removed != 1 {
		t.Fatalf("removed %d revision directories", removed)
	}
}
