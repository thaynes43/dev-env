package apiserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/shelf"
)

func TestContextArchiveGetReadsTheRealArchive(t *testing.T) {
	f, archive, when := realArchiveFixture(t)
	bindCatalogFixture(t, f)
	enableContexts(f)
	source, reader := contextSession(t, f, "archive-source", humanSA), contextSession(t, f, "archive-reader", clientSA)
	w := f.do(http.MethodPost, apiv1.PublishedContextsPath, source, contextReq())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	published, err := f.srv.PublishedContexts.Get(context.Background(), decode[apiv1.PublishContextResult](t, w).Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	stored := published.DeepCopy()
	stored.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "PublishedContext"}
	putRealObject(t, archive, stored, protocol.CoordArchiveManifest{
		Resource: "publishedcontexts", Kind: "PublishedContext", Namespace: stored.Namespace, Name: stored.Name,
		UID: string(stored.UID), ResourceVersion: stored.ResourceVersion, Project: stored.Spec.Project,
	})
	if err := f.c.Delete(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	useRealArchive(f, archive)

	w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"/"+published.Name+"?project=sample", reader, nil)
	got := decode[apiv1.PublishedContext](t, w)
	if w.Code != http.StatusOK || !got.Metadata.Archived || got.Metadata.ArchivedAt == nil || !got.Metadata.ArchivedAt.Equal(when) || got.Text != contextReq().Text || got.Metadata.Name != published.Name {
		t.Fatalf("archived context = %d %#v %s", w.Code, got.Metadata, w.Body.String())
	}
	w = f.do(http.MethodGet, apiv1.PublishedContextsPath+"?project=sample&archived=only", reader, nil)
	list := decode[apiv1.PublishedContextList](t, w)
	if w.Code != http.StatusOK || len(list.Items) != 1 || !list.Items[0].Archived || list.Items[0].Name != published.Name || list.Items[0].ContentSHA256 != published.Spec.ContentSHA256 {
		t.Fatalf("archived context list = %d %#v %s", w.Code, list, w.Body.String())
	}
}

func TestInboxArchiveGetReadsTheRealArchive(t *testing.T) {
	f, archive, when := realArchiveFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender, recipient := claimSession(t, f, "archive-sender", humanSA), claimSession(t, f, "archive-recipient", clientSA)
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-real-message"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	message, err := f.srv.Inbox.Get(context.Background(), decode[apiv1.InboxResult](t, w).Message.Name)
	if err != nil {
		t.Fatal(err)
	}
	putRealMessage(t, archive, message)
	if err := f.c.Delete(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	useRealArchive(f, archive)
	path := apiv1.InboxPath + "/" + message.Name + "?project=sample"
	w = f.do(http.MethodGet, path, recipient, nil)
	got := decode[apiv1.InboxMessage](t, w)
	if w.Code != http.StatusOK || !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(when) || got.Text != message.Spec.Text || got.Name != message.Name {
		t.Fatalf("archived message = %d %#v %s", w.Code, got.InboxMetadata, w.Body.String())
	}
	w = f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only", recipient, nil)
	list := decode[apiv1.InboxList](t, w)
	if w.Code != http.StatusOK || len(list.Items) != 1 || !list.Items[0].Archived || list.Items[0].Name != message.Name {
		t.Fatalf("archived message list = %d %#v %s", w.Code, list, w.Body.String())
	}
}

func TestInboxAckArchiveGetReadsTheRealArchive(t *testing.T) {
	f, archive, when := realArchiveFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender, recipient := claimSession(t, f, "ack-sender", humanSA), claimSession(t, f, "ack-recipient", clientSA)
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "ack-recipient", "archive-real-ack"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	message, err := f.srv.Inbox.Get(context.Background(), decode[apiv1.InboxResult](t, w).Message.Name)
	if err != nil {
		t.Fatal(err)
	}
	ackPath := apiv1.InboxPath + "/" + message.Name + "/ack?project=sample"
	if w = f.do(http.MethodPost, ackPath, recipient, bytes.NewBufferString(`{}`)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	ack, err := f.srv.Inbox.ReadAck(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	putRealMessage(t, archive, message)
	storedAck := ack.DeepCopy()
	storedAck.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxReadAck"}
	putRealObject(t, archive, storedAck, protocol.CoordArchiveManifest{
		Resource: "inboxreadacks", Kind: "InboxReadAck", Namespace: storedAck.Namespace, Name: storedAck.Name,
		UID: string(storedAck.UID), ResourceVersion: storedAck.ResourceVersion, Project: storedAck.Spec.Project,
		RecipientUID: storedAck.Spec.Recipient.UID, MessageUID: storedAck.Spec.MessageUID,
	})
	if err := f.c.Delete(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	useRealArchive(f, archive)
	w = f.do(http.MethodGet, apiv1.InboxPath+"/"+message.Name+"?project=sample", recipient, nil)
	gotMessage := decode[apiv1.InboxMessage](t, w)
	if w.Code != http.StatusOK || !gotMessage.Archived || gotMessage.Text != message.Spec.Text {
		t.Fatalf("archived message before ack = %d %s", w.Code, w.Body.String())
	}
	w = f.do(http.MethodGet, ackPath, recipient, nil)
	got := decode[apiv1.InboxReadAck](t, w)
	if w.Code != http.StatusOK || !got.Archived || got.ArchivedAt == nil || !got.ArchivedAt.Equal(when) || got.Name != inbox.AckName(string(message.UID), message.Spec.Recipient.UID) || got.MessageUID != string(message.UID) {
		t.Fatalf("archived ack = %d %#v %s", w.Code, got, w.Body.String())
	}
}

func realArchiveFixture(t *testing.T) (*fixture, *agentd.CoordArchive, time.Time) {
	t.Helper()
	restore := agentd.SetCoordArchiveMountCheck(func(string, string) error { return nil })
	t.Cleanup(restore)
	when := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)
	home := t.TempDir()
	root := filepath.Join(home, protocol.CoordArchiveDirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := &agentd.CoordArchive{Root: root, Home: home, Now: func() time.Time { return when }}
	return coordinatorFixture(t), archive, when
}

func useRealArchive(f *fixture, archive *agentd.CoordArchive) {
	f.srv.CoordinationArchiveReads = true
	f.srv.CoordinationArchive = realCoordArchive{a: *archive}
	f.srv.ShelfSlots = shelf.NewExecSlots(2)
}

func putRealMessage(t *testing.T, archive *agentd.CoordArchive, message *coord.InboxMessage) {
	t.Helper()
	stored := message.DeepCopy()
	stored.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"}
	putRealObject(t, archive, stored, protocol.CoordArchiveManifest{
		Resource: "inboxmessages", Kind: "InboxMessage", Namespace: stored.Namespace, Name: stored.Name,
		UID: string(stored.UID), ResourceVersion: stored.ResourceVersion, Project: stored.Spec.Project,
		RecipientUID: stored.Spec.Recipient.UID,
	})
}

func putRealObject(t *testing.T, archive *agentd.CoordArchive, obj any, manifest protocol.CoordArchiveManifest) {
	t.Helper()
	raw, err := protocol.CanonicalJSON(obj)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	manifest.Format = protocol.CoordArchiveFormat
	manifest.APIVersion = protocol.CoordArchiveAPIVersion
	manifest.Object = protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}
	manifest.ArchivedBy = "dev-env-operator"
	if _, err := archive.Put(manifest, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}
