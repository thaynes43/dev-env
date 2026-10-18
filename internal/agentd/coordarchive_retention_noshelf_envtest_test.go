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

// TestEnvtestNoShelfDoesNotMark is DESIGN-024 E2 when no shelf pod is ready.
// The live claim stays, and the real coordination archive has no revision.
func TestEnvtestNoShelfDoesNotMark(t *testing.T) {
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
	archive := CoordArchive{Root: root, Home: home, Now: time.Now}
	var order []string
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace,
		Client: &recordingClient{Client: admin, order: &order}, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Owner.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.TaskClaim: true},
		Pods:  noShelfPod{}, Archive: &fsArchive{a: archive, order: &order}, Slots: shelf.NewExecSlots(2),
		Now: time.Now,
	}
	if got := runnable.Pass(ctx); got != "shelf_unavailable" || len(order) != 0 {
		t.Fatalf("pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	id := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: spec.Identity.Project, Name: claim.Name, UID: string(claim.UID)}
	got := lookupOne(t, archive, id)
	if got.Holds || got.GraceStart != nil || got.LatestState != "" || len(got.Revisions) != 0 {
		t.Fatalf("archive without a shelf %#v", got)
	}
}

type noShelfPod struct{}

func (noShelfPod) Pod(context.Context) (string, error) { return "", shelf.ErrNoShelf }
