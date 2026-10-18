package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/contextpublication"
	"github.com/thaynes43/dev-env/internal/coordretention"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/testenv"
)

// TestEnvtestWorstCasePublicationDeletes is DESIGN-024 E2's worst-case
// publication. Sixty-four kibibytes of U+0001 are archived, verified, and
// deleted. The stored JSON escapes each byte, and the object stays inside
// the archive bound.
func TestEnvtestWorstCasePublicationDeletes(t *testing.T) {
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
	spec := retentionContextSpec()
	spec.Identity.Key = "retention-worst"
	spec.Text = strings.Repeat("\u0001", apiv1.MaxContextBytes)
	spec.ContentSHA256 = apiv1.ContextHash(spec.Text)
	spec.ByteCount = int32(len(spec.Text))
	if len(spec.Text) != apiv1.MaxContextBytes || !contextpublication.ValidSpec(spec) {
		t.Fatal("worst-case context spec")
	}
	published := &coord.PublishedContext{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: contextpublication.Name(spec.Identity)}, Spec: spec}
	if err := admin.Create(ctx, published); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	var order []string
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace, Client: &recordingClient{Client: admin, order: &order}, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Source.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.PublishedContext: true},
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
	id := protocol.CoordArchiveIdentity{Resource: "publishedcontexts", Project: spec.Project, Name: published.Name, UID: string(published.UID)}
	marked := lookupOne(t, archive, id)
	if marked.GraceStart == nil || !marked.Holds {
		t.Fatalf("marked archive %#v", marked)
	}
	live := liveKindBytes(t, ctx, admin, client.ObjectKeyFromObject(published), "PublishedContext")
	if got := pass(start.Add(coordretention.DefaultGrace)); got != "complete" || !verifyBeforeDelete(order) {
		t.Fatalf("delete pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(published), &coord.PublishedContext{}); !apierrors.IsNotFound(err) {
		t.Fatalf("context after grace: %v", err)
	}
	stored, err := archive.GetSelected(protocol.CoordArchiveGetQuery{Resource: id.Resource, Name: id.Name, UID: id.UID})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(stored.Object)
	escaped := bytes.Count(stored.Object, []byte(`\u0001`))
	if hex.EncodeToString(sum[:]) != stored.Manifest.Object.SHA256 || !bytes.Equal(stored.Object, live) || escaped != apiv1.MaxContextBytes || len(stored.Object) <= 300<<10 || len(stored.Object) > protocol.MaxCoordArchiveObjectBytes {
		t.Fatalf("worst-case archive bytes %d escapes %d", len(stored.Object), escaped)
	}
}
