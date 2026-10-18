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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/coordretention"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
	"github.com/thaynes43/dev-env/internal/testenv"
)

// TestEnvtestAckDeleteRetriesAfterConflict is DESIGN-024 E3. The grace pass
// deletes the message and then conflicts on the acknowledgement. The next
// pass deletes that acknowledgement.
func TestEnvtestAckDeleteRetriesAfterConflict(t *testing.T) {
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
	spec := retentionMessageSpec()
	spec.Key = "retention-3"
	if !inbox.ValidMessage(spec) {
		t.Fatal("message spec")
	}
	message := &coord.InboxMessage{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: inbox.MessageName(spec.Sender, spec.Key)}, Spec: spec}
	if err := admin.Create(ctx, message); err != nil {
		t.Fatal(err)
	}
	ackSpec := coord.InboxReadAckSpec{Project: spec.Project, MessageName: message.Name, MessageUID: string(message.UID), Recipient: spec.Recipient, Actor: spec.Actor}
	if !inbox.ValidAck(ackSpec) {
		t.Fatal("ack spec")
	}
	ack := &coord.InboxReadAck{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: inbox.AckName(ackSpec.MessageUID, ackSpec.Recipient.UID)}, Spec: ackSpec}
	if err := admin.Create(ctx, ack); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)
	clock := start
	archive := CoordArchive{Root: root, Home: home, Now: func() time.Time { return clock }}
	var order []string
	runnable := &coordretention.Runnable{
		Enabled: true, Grace: coordretention.DefaultGrace,
		Client: &recordingClient{Client: &ackConflictOnce{Client: admin}, order: &order}, Reader: admin,
		Namespace: ns, Reconciled: map[string]bool{spec.Sender.Namespace: true},
		Kinds: map[coordretention.Kind]bool{coordretention.InboxMessage: true, coordretention.InboxReadAck: true},
		Pods:  shelfPod{}, Archive: &fsArchive{a: archive, order: &order}, Slots: shelf.NewExecSlots(4),
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
	due := start.Add(coordretention.DefaultGrace)
	if got := pass(due); got != "complete" || !messageDeletedBeforeAck(order) || !recordedBefore(order, "get:inboxmessages", "delete:InboxMessage") || !recordedBefore(order, "get:inboxreadacks", "delete:InboxReadAck") {
		t.Fatalf("conflict pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(message), &coord.InboxMessage{}); !apierrors.IsNotFound(err) {
		t.Fatalf("message after the conflict: %v", err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(ack), ack); err != nil {
		t.Fatal(err)
	}
	if ack.Labels["touched"] != "yes" {
		t.Fatalf("ack after the conflict %#v", ack.Labels)
	}
	liveAck := liveKindBytes(t, ctx, admin, client.ObjectKeyFromObject(ack), "InboxReadAck")
	sum := sha256.Sum256(liveAck)
	order = nil
	if got := pass(due.Add(time.Minute)); got != "complete" || len(order) != 2 || order[0] != "get:inboxreadacks" || order[1] != "delete:InboxReadAck" {
		t.Fatalf("retry pass %s order %v", got, order)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(ack), &coord.InboxReadAck{}); !apierrors.IsNotFound(err) {
		t.Fatalf("ack after the retry: %v", err)
	}
	stored, err := archive.GetSelected(protocol.CoordArchiveGetQuery{
		Resource: "inboxreadacks", Name: ack.Name, UID: string(ack.UID),
		ResourceVersion: ack.ResourceVersion, SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil || !bytes.Equal(stored.Object, liveAck) {
		t.Fatalf("retried ack revision: %v", err)
	}
}

// ackConflictOnce changes the acknowledgement before its first delete.
type ackConflictOnce struct {
	client.Client
	done bool
}

func (c *ackConflictOnce) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	kind := ""
	if u, ok := obj.(*unstructured.Unstructured); ok {
		kind = u.GetKind()
	}
	if !c.done && kind == "InboxReadAck" {
		c.done = true
		var live coord.InboxReadAck
		if err := c.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: obj.GetName()}, &live); err != nil {
			return err
		}
		if live.Labels == nil {
			live.Labels = map[string]string{}
		}
		live.Labels["touched"] = "yes"
		if err := c.Update(ctx, &live); err != nil {
			return err
		}
	}
	return c.Client.Delete(ctx, obj, opts...)
}
