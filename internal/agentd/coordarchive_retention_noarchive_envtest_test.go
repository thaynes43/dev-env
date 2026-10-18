package agentd

import (
	"context"
	"errors"
	"os"
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

// TestEnvtestNoArchiveDoesNotMark is DESIGN-024 E2 when the shelf pod is
// ready and the coordination archive is not a volume of its own. The probe
// lookup is the only archive call. The claim stays unmarked.
func TestEnvtestNoArchiveDoesNotMark(t *testing.T) {
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

	dir := t.TempDir()
	archive := CoordArchive{Root: dir, Home: dir, Now: time.Now}
	id := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: spec.Identity.Project, Name: claim.Name, UID: string(claim.UID)}
	if _, err := archive.Lookup([]protocol.CoordArchiveIdentity{id}); !isNoArchive(err) {
		t.Fatalf("mount check: %v", err)
	}
	var order []string
	store := &countingArchive{fsArchive: &fsArchive{a: archive, order: &order}}
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace,
		Client: &recordingClient{Client: admin, order: &order}, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Owner.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.TaskClaim: true},
		Pods:  shelfPod{}, Archive: store, Slots: shelf.NewExecSlots(2),
		Now: time.Now,
	}
	if got := runnable.Pass(ctx); got != "shelf_unavailable" || len(order) != 0 || store.lookups != 1 {
		t.Fatalf("pass %s order %v lookups %d", got, order, store.lookups)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "v1" || entry.Name() == protocol.HistoryArchiveIncoming {
			t.Fatalf("archive wrote %s", entry.Name())
		}
	}
}

type countingArchive struct {
	*fsArchive
	lookups int
}

func (c *countingArchive) Lookup(ctx context.Context, ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	c.lookups++
	return c.fsArchive.Lookup(ctx, ids)
}

func isNoArchive(err error) bool {
	var refusal *protocol.HistoryError
	return errors.As(err, &refusal) && refusal.Code == protocol.HistoryErrNoArchive
}
