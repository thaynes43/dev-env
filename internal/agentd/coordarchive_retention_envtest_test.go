package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// TestEnvtestArchiveBeforeDelete is DESIGN-024 E1 for one claim. The archive is
// the real coordination archive on a temp directory. The mount check is stubbed
// the same way the archive tests stub it. A context, a message, an ack, the
// exec transport, and the archived-read API remain.
func TestEnvtestArchiveBeforeDelete(t *testing.T) {
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
	start := time.Date(2026, 10, 17, 17, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	var order []string
	store := &fsArchive{a: archive, order: &order}
	deletes := &recordingClient{Client: admin, order: &order}
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace, Client: deletes, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Owner.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.TaskClaim: true},
		Pods:  shelfPod{}, Archive: store, Slots: shelf.NewExecSlots(2),
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
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	id := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: spec.Identity.Project, Name: claim.Name, UID: string(claim.UID)}
	looked, err := archive.Lookup([]protocol.CoordArchiveIdentity{id})
	if err != nil || len(looked) != 1 || looked[0].GraceStart == nil || !looked[0].Holds {
		t.Fatalf("marked archive %#v %v", looked, err)
	}
	if got := pass(start.Add(coordretention.DefaultGrace - time.Minute)); got != "complete" || len(order) != 0 {
		t.Fatalf("grace pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); err != nil {
		t.Fatal(err)
	}
	if got := pass(start.Add(coordretention.DefaultGrace)); got != "complete" || !verifyBeforeDelete(order) {
		t.Fatalf("delete pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(claim), &coord.TaskClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim after grace: %v", err)
	}
	stored, err := archive.GetSelected(protocol.CoordArchiveGetQuery{Resource: id.Resource, Name: id.Name, UID: id.UID})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(stored.Object)
	if hex.EncodeToString(sum[:]) != stored.Manifest.Object.SHA256 {
		t.Fatal("archived object hash does not match its manifest")
	}
}

func verifyBeforeDelete(order []string) bool {
	sawGet := false
	for _, step := range order {
		switch step {
		case "get":
			sawGet = true
		case "delete":
			return sawGet
		}
	}
	return false
}

func retentionClaimSpec() coord.TaskClaimSpec {
	owner := coord.SessionIdentity{Namespace: "dev-agents", Name: "sender", UID: "11111111-1111-4111-8111-111111111111"}
	return coord.TaskClaimSpec{
		Identity: coord.TaskIdentity{Project: "sample", Repository: "repo", TaskKey: "issue:301"},
		Owner:    owner,
		Actor:    coord.ClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env-agent", PodName: "sender", PodUID: "33333333-3333-4333-8333-333333333333"},
		Provenance: coord.ClaimProvenance{
			CatalogRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RulesRevision:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			GitHub:          "thaynes43/dev-env", DefaultBranch: "main", InitialPinState: "Unknown",
		},
	}
}

type shelfPod struct{}

func (shelfPod) Pod(context.Context) (string, error) { return "shelf", nil }

type fsArchive struct {
	a     CoordArchive
	order *[]string
}

func (f *fsArchive) Put(_ context.Context, req protocol.CoordArchivePutRequest, mark bool) (protocol.CoordArchivePutResult, error) {
	if mark != (req.Event != nil) {
		return protocol.CoordArchivePutResult{}, protocol.HistoryErrorf(protocol.HistoryErrInvalid, "", "--mark and absent event must be supplied together")
	}
	return f.a.PutRecord(req.Manifest, bytes.NewReader(req.Object), req.Event)
}

func (f *fsArchive) Mark(_ context.Context, req []protocol.CoordArchiveMarkRequest) ([]protocol.CoordArchiveMarkResult, error) {
	return f.a.Mark(req)
}

func (f *fsArchive) Lookup(_ context.Context, ids []protocol.CoordArchiveIdentity) ([]protocol.CoordArchiveLookupResult, error) {
	return f.a.Lookup(ids)
}

func (f *fsArchive) Get(_ context.Context, q protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	*f.order = append(*f.order, "get")
	return f.a.GetSelected(q)
}

type recordingClient struct {
	client.Client
	order *[]string
}

func (c recordingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	*c.order = append(*c.order, "delete")
	return c.Client.Delete(ctx, obj, opts...)
}
