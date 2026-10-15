package apiserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/shelf"
)

type inboxArchiveListFake struct {
	list  protocol.CoordArchiveListResult
	calls int
	query protocol.CoordArchiveListQuery
}

func (*inboxArchiveListFake) Get(context.Context, protocol.CoordArchiveGetQuery) (protocol.CoordArchiveGetResult, error) {
	return protocol.CoordArchiveGetResult{}, errors.New("unexpected inbox archive get")
}
func (f *inboxArchiveListFake) List(_ context.Context, q protocol.CoordArchiveListQuery) (protocol.CoordArchiveListResult, error) {
	f.calls++
	f.query = q
	return f.list, nil
}

func archivedInboxListItem(t *testing.T, m *coord.InboxMessage) protocol.CoordArchiveListItem {
	t.Helper()
	m.TypeMeta = metav1.TypeMeta{APIVersion: protocol.CoordArchiveAPIVersion, Kind: "InboxMessage"}
	full, err := protocol.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(full)
	var projected map[string]any
	if json.Unmarshal(full, &projected) != nil {
		t.Fatal("fixture")
	}
	delete(projected["spec"].(map[string]any), "text")
	object, err := protocol.CanonicalJSON(projected)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.CoordArchiveManifest{Format: protocol.CoordArchiveFormat, Resource: "inboxmessages", Kind: "InboxMessage", APIVersion: protocol.CoordArchiveAPIVersion, Namespace: m.Namespace, Name: m.Name, UID: string(m.UID), ResourceVersion: m.ResourceVersion, Project: m.Spec.Project, RecipientUID: m.Spec.Recipient.UID, Object: protocol.CoordArchiveObjectDigest{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(full))}, ArchivedBy: "fixture"}
	return protocol.CoordArchiveListItem{UID: string(m.UID), Manifest: &protocol.CoordArchiveStoredManifest{CoordArchiveManifest: manifest, ArchivedAt: time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)}, Object: object}
}

func TestInboxArchiveListDefaultOffMetadataAndRecipientBinding(t *testing.T) {
	f := coordinatorFixture(t)
	bindCatalogFixture(t, f)
	enableInbox(f)
	sender, recipient := claimSession(t, f, "archive-sender", humanSA), claimSession(t, f, "archive-recipient", clientSA)
	w := f.do(http.MethodPost, apiv1.InboxPath, sender, inboxRequest(f, "archive-recipient", "archive-list"))
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	created := decode[apiv1.InboxResult](t, w).Message
	m, err := f.srv.Inbox.Get(context.Background(), created.Name)
	if err != nil {
		t.Fatal(err)
	}
	archive := &inboxArchiveListFake{list: protocol.CoordArchiveListResult{Items: []protocol.CoordArchiveListItem{archivedInboxListItem(t, m)}}}
	f.srv.CoordinationArchiveReads, f.srv.CoordinationArchive, f.srv.ShelfSlots = true, archive, shelf.NewExecSlots(2)
	path := apiv1.InboxPath + "?project=sample&limit=1"
	if w = f.do(http.MethodGet, path, recipient, nil); w.Code != http.StatusOK || archive.calls != 0 {
		t.Fatalf("live/default = %d calls=%d", w.Code, archive.calls)
	}
	w = f.do(http.MethodGet, path+"&archived=only", recipient, nil)
	page := decode[apiv1.InboxList](t, w)
	if w.Code != http.StatusOK || archive.calls != 1 || archive.query.Resource != "inboxmessages" || archive.query.RecipientUID != m.Spec.Recipient.UID || len(page.Items) != 1 || !page.Items[0].Archived || page.Items[0].ArchivedAt == nil || page.Items[0].Sender != created.Sender || page.Items[0].Recipient != created.Recipient || strings.Contains(w.Body.String(), "PRIVATE_INBOX_EXACT_TEXT") {
		t.Fatalf("archived page = %d calls=%d query=%#v body=%s", w.Code, archive.calls, archive.query, w.Body.String())
	}
	if w = f.do(http.MethodGet, apiv1.InboxPath+"?project=sample&archived=only&limit=101", recipient, nil); w.Code != http.StatusBadRequest || archive.calls != 1 {
		t.Fatalf("limit = %d calls=%d", w.Code, archive.calls)
	}
	item := archivedInboxListItem(t, m)
	item.Manifest.RecipientUID = "replacement-recipient"
	if _, _, err := f.srv.archivedInboxMetadata(*item.Manifest, item.Object); err == nil {
		t.Fatal("recipient manifest mismatch accepted")
	}
}
