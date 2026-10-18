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

// TestEnvtestReplacedOwnerDeletesAfterGrace is DESIGN-024 E6's delete. The
// session keeps the owner's name and has a new UID. The claim that names the
// old UID is deleted after the grace. The claim that names the live UID stays.
func TestEnvtestReplacedOwnerDeletesAfterGrace(t *testing.T) {
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
	for _, name := range []string{ns, "dev-agents"} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	session := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "sender"}, Spec: devv1.AgentSessionSpec{
		Repo: "haynes-ops", Agent: devv1.AgentClaude, Mode: devv1.ModeTask, Model: "claude-haiku-4-5", Prompt: "read the replaced owner",
	}}
	if err := admin.Create(ctx, session); err != nil {
		t.Fatal(err)
	}
	replacedSpec := retentionClaimSpec()
	if string(session.UID) == replacedSpec.Owner.UID {
		t.Fatal("the API server reused the original owner UID")
	}
	replaced := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: taskclaim.Name(replacedSpec.Identity)}, Spec: replacedSpec}
	if err := admin.Create(ctx, replaced); err != nil {
		t.Fatal(err)
	}
	currentSpec := replacedSpec
	currentSpec.Identity.TaskKey = "issue:302"
	currentSpec.Owner.UID = string(session.UID)
	if !taskclaim.ValidSpec(currentSpec) {
		t.Fatal("live owner claim spec")
	}
	current := &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: taskclaim.Name(currentSpec.Identity)}, Spec: currentSpec}
	if err := admin.Create(ctx, current); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	var order []string
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace, Client: &recordingClient{Client: admin, order: &order}, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{"dev-agents": true},
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
	replacedID := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: replacedSpec.Identity.Project, Name: replaced.Name, UID: string(replaced.UID)}
	currentID := protocol.CoordArchiveIdentity{Resource: "taskclaims", Project: currentSpec.Identity.Project, Name: current.Name, UID: string(current.UID)}
	marked := lookupOne(t, archive, replacedID)
	if marked.GraceStart == nil || !marked.Holds || marked.LatestState != "absent" {
		t.Fatalf("replaced claim was not marked %#v", marked)
	}
	kept := lookupOne(t, archive, currentID)
	if kept.Holds || kept.GraceStart != nil || len(kept.Revisions) != 0 {
		t.Fatalf("live owner was marked %#v", kept)
	}
	if got := pass(start.Add(coordretention.DefaultGrace - time.Minute)); got != "complete" || len(order) != 0 {
		t.Fatalf("inside grace %s order %v", got, order)
	}
	live := liveClaimBytes(t, ctx, admin, replaced)
	if got := pass(start.Add(coordretention.DefaultGrace)); got != "complete" || !verifyBeforeDelete(order) {
		t.Fatalf("delete pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(replaced), &coord.TaskClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("replaced claim after grace: %v", err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(current), &coord.TaskClaim{}); err != nil {
		t.Fatalf("live owner after grace: %v", err)
	}
	stillKept := lookupOne(t, archive, currentID)
	if stillKept.Holds || stillKept.GraceStart != nil {
		t.Fatalf("live owner archived during the delete %#v", stillKept)
	}
	stored, err := archive.GetSelected(protocol.CoordArchiveGetQuery{Resource: replacedID.Resource, Name: replacedID.Name, UID: replacedID.UID})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(stored.Object)
	if hex.EncodeToString(sum[:]) != stored.Manifest.Object.SHA256 || !bytes.Equal(stored.Object, live) {
		t.Fatal("archived bytes are not the replaced claim read before the delete")
	}
}
